// Detached worktree lifecycle (§4-4/§4-6). A Worktree is a scratch git
// worktree checked out at a specific commit so a task can apply and run a
// patch without touching the main repo tree. Its lifecycle is expressed as
// durable registry transitions in an append-only JSONL log (internal/journal)
// rather than trusted-on-faith filesystem state, so a crash at any point can
// be recovered deterministically from the registry alone:
//
//	{"_type":"wt_intent","evidence_id":..,"dir":..,"at":..}   // ① before any FS mutation
//	(② git worktree add --detach <dir> <commit> creates the directory)
//	{"_type":"wt_active","evidence_id":..,"at":..}            // ③ after add succeeds
//	{"_type":"wt_removed","evidence_id":..,"at":..}           // ④ after remove+prune succeeds
//
// Order is frozen: the path is derived (never MkdirTemp, which would create
// the directory before any durable record of it exists) and the intent line
// is durably appended BEFORE any filesystem object is created. That way a
// crash before step ② leaves only a registry line pointing at a directory
// that was never created — recoverable by projection, never an unaccounted
// orphan directory with no paper trail.
//
// Crash recovery reduces to a projection over the registry: an evidence id
// with an intent but no active record may have no directory, or a half-built
// one (crash during step ②) — either way it is cleaned up idempotently and
// marked removed. An evidence id with an active record but no removed record
// and not in the caller's live set is an orphan for the same reason (crash
// or missed cleanup after the task that owned it finished) and is cleaned up
// the same way. See CleanupOrphans.
package evidence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/journal"
)

// evidenceIDPattern is the only shape NewWorktree accepts for evidenceID
// before using it, unsanitized, to derive a filesystem path
// (os.TempDir()+"wb-evidence-"+evidenceID): a bare ULID's alphabet plus '_'
// and '-', capped at 64 bytes. Anything else — a path separator, "..",
// whitespace, or an oversized/empty string — is rejected outright rather
// than trusted, since a caller-controlled evidenceID landing unchecked in a
// path is a path-traversal / directory-escape vector (review defense).
var evidenceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Worktree is a detached git worktree checked out at a specific commit,
// tracked by evidence id in the durable registry described above.
type Worktree struct {
	Dir        string
	EvidenceID string
}

// registryRecord is the on-disk shape of every registry line. Dir is only
// populated on wt_intent lines (omitempty keeps wt_active/wt_removed lines
// matching the brief's frozen shape exactly).
type registryRecord struct {
	Type       string `json:"_type"`
	EvidenceID string `json:"evidence_id"`
	Dir        string `json:"dir,omitempty"`
	At         string `json:"at"`
}

// NewWorktree derives the worktree path, durably records intent, creates the
// detached worktree at commitOID, then durably records it active. The path
// is derived — not os.MkdirTemp, which creates the directory as a side
// effect of allocating the name — so that intent is always durable before
// any directory can exist at that path (§ order above). evidenceID is
// expected to already be unique (a ULID minted by the caller); NewWorktree
// does not mint one itself.
func NewWorktree(repoRoot, commitOID, registryPath, evidenceID string) (*Worktree, error) {
	if !evidenceIDPattern.MatchString(evidenceID) {
		return nil, fmt.Errorf("evidence: new worktree: invalid evidence id %q", evidenceID)
	}
	dir := filepath.Join(os.TempDir(), "wb-evidence-"+evidenceID)

	reg, err := journal.Open(registryPath)
	if err != nil {
		return nil, fmt.Errorf("evidence: new worktree: open registry: %w", err)
	}
	defer reg.Close()

	intent := registryRecord{Type: "wt_intent", EvidenceID: evidenceID, Dir: dir, At: nowRFC3339()}
	if err := appendRecord(reg, intent); err != nil {
		return nil, fmt.Errorf("evidence: new worktree: append intent: %w", err)
	}

	// If this fails (bad commit, dir collision, crash mid-add), the intent
	// line already stands: the directory (absent or half-built) is left for
	// CleanupOrphans to reconcile rather than cleaned up inline here.
	if _, err := runGit(repoRoot, nil, "worktree", "add", "--detach", dir, commitOID); err != nil {
		return nil, fmt.Errorf("evidence: new worktree: git worktree add: %w", err)
	}

	active := registryRecord{Type: "wt_active", EvidenceID: evidenceID, At: nowRFC3339()}
	if err := appendRecord(reg, active); err != nil {
		return nil, fmt.Errorf("evidence: new worktree: append active: %w", err)
	}

	return &Worktree{Dir: dir, EvidenceID: evidenceID}, nil
}

// ApplyPatch validates patch applies cleanly (--check) before touching
// anything, lists the paths it would touch, then applies it for real. patch
// bytes are always passed via stdin (never interpolated into a shell
// string), and any failure at any of the three steps returns an error
// without partially applying — --check runs first specifically so a patch
// that fails is guaranteed not to have mutated the worktree.
//
// touched is derived from `git apply --numstat -z`, but with one deviation
// from a literal reading of that command: empirically (git 2.55.0),
// `git apply --numstat -z` on a rename/copy patch lists only the new path,
// not "old\x00new" the way `git diff --numstat -z` does for the same
// content. Since a caller checking oracle-surface membership on only the
// new path could be evaded by an oracle file renamed to a non-oracle path
// (or vice versa), ApplyPatch additionally scans the raw patch text for
// git's standard "rename from "/"copy from " extended-header lines and
// folds those old paths in too, so touched always contains both sides of a
// rename/copy regardless of what --numstat -z actually emits.
//
// Those header paths go through unquoteCStyle before being folded in.
// `-z` disables numstat's own path quoting, but it has no effect on the
// "rename from "/"copy from " lines — those come from the patch text
// itself, which git quotes (core.quotepath default) whenever a path
// contains a byte it considers unsafe: any non-ASCII UTF-8 byte, a
// backslash, a double quote, or a control character. An unquoted rename
// header on a non-ASCII path (e.g. rename from "t\303\244b.txt") would
// otherwise land in touched as that literal escaped string instead of the
// real path, silently diverging from the raw bytes numstat -z returns for
// the same file. Left unhandled, this is also an oracle-surface bypass: an
// attacker can hand-craft a patch with an already-quoted, non-escaped old
// path (rename from "src/oracle.txt") and rely on git apply accepting it
// verbatim while a caller comparing touched against the oracle surface sees
// only the quoted literal and never matches.
func (w *Worktree) ApplyPatch(patch []byte) (touched []string, err error) {
	if _, err := runGit(w.Dir, patch, "apply", "--check"); err != nil {
		return nil, fmt.Errorf("evidence: apply patch: check failed: %w", err)
	}
	numstat, err := runGit(w.Dir, patch, "apply", "--numstat", "-z")
	if err != nil {
		return nil, fmt.Errorf("evidence: apply patch: numstat failed: %w", err)
	}
	if _, err := runGit(w.Dir, patch, "apply"); err != nil {
		return nil, fmt.Errorf("evidence: apply patch: apply failed: %w", err)
	}
	return parseTouchedPaths(numstat, patch), nil
}

// parseTouchedPaths merges `git apply --numstat -z` paths with any
// "rename from "/"copy from " old paths found in the raw patch text. See
// ApplyPatch's doc comment for why both sources are needed.
func parseTouchedPaths(numstatZ []byte, patch []byte) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, tok := range bytes.Split(numstatZ, []byte{0}) {
		if len(tok) == 0 {
			continue
		}
		// "<added>\t<deleted>\t<path>"
		parts := bytes.SplitN(tok, []byte{'\t'}, 3)
		if len(parts) == 3 {
			add(string(parts[2]))
		}
	}
	for _, line := range strings.Split(string(patch), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if p, ok := strings.CutPrefix(line, "rename from "); ok {
			add(unquoteCStyle(p))
		} else if p, ok := strings.CutPrefix(line, "copy from "); ok {
			add(unquoteCStyle(p))
		}
	}
	return out
}

// unquoteCStyle reverses git's quote_c_style path quoting, which
// "rename from "/"copy from " extended-header lines are subject to (see
// ApplyPatch's doc comment). A path is only ever wrapped in quotes when git
// decided quoting was necessary, so a string that isn't wrapped in a
// leading and trailing '"' is returned unchanged — including the
// adversarial case of a hand-crafted header with no matching outer quotes
// at all.
func unquoteCStyle(s string) string {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}
	inner := s[1 : len(s)-1]
	out := make([]byte, 0, len(inner))
	for i := 0; i < len(inner); i++ {
		c := inner[i]
		if c != '\\' || i+1 >= len(inner) {
			out = append(out, c)
			continue
		}
		i++
		switch e := inner[i]; {
		case e == 'a':
			out = append(out, '\a')
		case e == 'b':
			out = append(out, '\b')
		case e == 'f':
			out = append(out, '\f')
		case e == 'n':
			out = append(out, '\n')
		case e == 'r':
			out = append(out, '\r')
		case e == 't':
			out = append(out, '\t')
		case e == 'v':
			out = append(out, '\v')
		case e == '\\' || e == '"':
			out = append(out, e)
		case e >= '0' && e <= '7':
			// \NNN: up to 3 octal digits encoding one raw byte.
			val := int(e - '0')
			for k := 0; k < 2 && i+1 < len(inner) && inner[i+1] >= '0' && inner[i+1] <= '7'; k++ {
				i++
				val = val*8 + int(inner[i]-'0')
			}
			out = append(out, byte(val))
		default:
			// Unknown escape: not a form git itself ever produces; keep it
			// literally rather than guess, so an unexpected input is never
			// silently misparsed into some other path.
			out = append(out, '\\', e)
		}
	}
	return string(out)
}

// Remove tears down the worktree (git worktree remove --force + prune) and
// durably records it removed. See removeWorktreeDir for the idempotent
// behavior this relies on when the directory is absent or half-built.
func (w *Worktree) Remove(repoRoot, registryPath string) error {
	if err := removeWorktreeDir(repoRoot, w.Dir); err != nil {
		return fmt.Errorf("evidence: remove worktree: %w", err)
	}

	reg, err := journal.Open(registryPath)
	if err != nil {
		return fmt.Errorf("evidence: remove worktree: open registry: %w", err)
	}
	defer reg.Close()

	removed := registryRecord{Type: "wt_removed", EvidenceID: w.EvidenceID, At: nowRFC3339()}
	if err := appendRecord(reg, removed); err != nil {
		return fmt.Errorf("evidence: remove worktree: append removed: %w", err)
	}
	return nil
}

// CleanupOrphans projects the registry and reconciles every evidence id that
// is not already removed:
//   - intent but no active: the directory may not exist yet, or may be
//     half-built (crash during `git worktree add`) — clean up and mark
//     removed regardless of liveIDs (there is no legitimate long-lived
//     "intent only" state; NewWorktree either reaches active or the caller
//     crashed).
//   - active but no removed and not in liveIDs: an orphan left behind by a
//     crash or a missed Remove call — clean up and mark removed.
//
// Every other state (already removed; active and live) is left alone.
func CleanupOrphans(repoRoot, registryPath string, liveIDs map[string]bool) error {
	reg, err := journal.Open(registryPath)
	if err != nil {
		return fmt.Errorf("evidence: cleanup orphans: open registry: %w", err)
	}
	defer reg.Close()

	type projected struct {
		dir                              string
		hasIntent, hasActive, hasRemoved bool
	}
	states := map[string]*projected{}
	var order []string
	for _, ln := range reg.Lines() {
		var rec registryRecord
		if err := json.Unmarshal(ln, &rec); err != nil {
			return fmt.Errorf("evidence: cleanup orphans: malformed registry line: %w", err)
		}
		st, ok := states[rec.EvidenceID]
		if !ok {
			st = &projected{}
			states[rec.EvidenceID] = st
			order = append(order, rec.EvidenceID)
		}
		switch rec.Type {
		case "wt_intent":
			st.hasIntent = true
			st.dir = rec.Dir
		case "wt_active":
			st.hasActive = true
		case "wt_removed":
			st.hasRemoved = true
		default:
			return fmt.Errorf("evidence: cleanup orphans: unknown registry record type %q", rec.Type)
		}
	}

	for _, id := range order {
		st := states[id]
		if st.hasRemoved {
			continue
		}
		orphan := (st.hasIntent && !st.hasActive) || (st.hasActive && !liveIDs[id])
		if !orphan {
			continue
		}
		if err := removeWorktreeDir(repoRoot, st.dir); err != nil {
			return fmt.Errorf("evidence: cleanup orphans: %s: %w", id, err)
		}
		removed := registryRecord{Type: "wt_removed", EvidenceID: id, At: nowRFC3339()}
		if err := appendRecord(reg, removed); err != nil {
			return fmt.Errorf("evidence: cleanup orphans: append removed for %s: %w", id, err)
		}
	}
	return nil
}

// removeWorktreeDir idempotently tears down dir as a git worktree of
// repoRoot. `git worktree remove --force` only recognizes directories git
// itself registered via a completed `worktree add`; a directory that was
// never created, or one that was manually staged mid-crash (mkdir without a
// completed add), makes remove fail with "is not a working tree" — in that
// case there is no git admin state to clean, so this falls back to a plain
// os.RemoveAll (a no-op if dir does not exist at all). `worktree prune`
// always runs afterward as a safety net for any other stale admin entries.
func removeWorktreeDir(repoRoot, dir string) error {
	if _, err := runGit(repoRoot, nil, "worktree", "remove", "--force", dir); err != nil {
		if !isNotAWorkingTree(err) {
			return fmt.Errorf("git worktree remove: %w", err)
		}
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			return fmt.Errorf("remove half-built worktree dir %q: %w", dir, rmErr)
		}
	}
	if _, err := runGit(repoRoot, nil, "worktree", "prune"); err != nil {
		return fmt.Errorf("git worktree prune: %w", err)
	}
	return nil
}

// isNotAWorkingTree reports whether err is git's "fatal: '<dir>' is not a
// working tree" — the specific, expected failure of `worktree remove` on a
// directory git never registered. Commands run with LC_ALL=C/LANG=C (see
// runGit) so this English substring match is stable regardless of the
// caller's locale.
func isNotAWorkingTree(err error) bool {
	return err != nil && strings.Contains(err.Error(), "is not a working tree")
}

// runGit runs `git -C dir <args>`, feeding stdin (if non-nil) via a
// controlled in-memory reader rather than a shell string — required for
// ApplyPatch, which must never let patch bytes touch shell quoting. Locale
// is pinned to C so error text (used by isNotAWorkingTree) is stable.
func runGit(dir string, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("git %v: %s: %w", args, strings.TrimSpace(stderr.String()), err)
	}
	return stdout.Bytes(), nil
}

func appendRecord(reg *journal.Journal, rec registryRecord) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return reg.Append(line)
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
