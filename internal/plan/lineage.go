package plan

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

// GitRunner is the minimal git access VerifyLineage needs. plan stays a
// pure domain package (see types.go doc comment) — the actual process
// spawning lives in whatever implements this interface (e.g. spec.GitRepo's
// git() helper, or a test double), never in this package.
type GitRunner interface {
	Git(args ...string) ([]byte, error)
}

// exitCoder matches *exec.ExitError's ExitCode() method structurally, so
// VerifyLineage can distinguish "git merge-base --is-ancestor said no"
// (exit 1) from a genuine command failure (bad ref, corrupt repo, ...)
// without importing os/exec — that would pull process-spawning concerns
// into a package that must stay I/O-free itself.
type exitCoder interface{ ExitCode() int }

// VerifyLineage checks that ancestor is a git ancestor of descendant AND
// that every path touched in the ancestor..descendant range satisfies
// allow — including BOTH sides of a rename/copy. This is the shared
// machinery behind §3.0 (plan lineage: an approved plan revision must only
// ever touch plan/**) and §3.4 rules 2-3 (oracle lineage): a rename that
// crosses the scope boundary (e.g. code file renamed into plan/, or a
// declared oracle renamed out of oracle scope) must not slip through
// undetected just because it isn't a plain add/modify/delete.
//
// Path enumeration uses `git diff --name-status -z --find-renames
// ancestor..descendant`. The -z (NUL-delimited) format is required to
// safely parse paths containing spaces or non-ASCII bytes, and its record
// shape is fixed: an A/M/D record is two NUL-separated fields (status,
// path); an R<score>/C<score> record is three (status, old path, new
// path). See splitNULFields.
func VerifyLineage(g GitRunner, ancestor, descendant string, allow func(path string) bool) error {
	if _, err := g.Git("merge-base", "--is-ancestor", ancestor, descendant); err != nil {
		var ec exitCoder
		if errors.As(err, &ec) && ec.ExitCode() == 1 {
			return fmt.Errorf("plan: lineage: %s is not an ancestor of %s", ancestor, descendant)
		}
		return err
	}

	out, err := g.Git("diff", "--name-status", "-z", "--find-renames", ancestor+".."+descendant)
	if err != nil {
		return err
	}

	fields := splitNULFields(out)
	for i := 0; i < len(fields); {
		status := fields[i]
		i++
		if status == "" {
			continue
		}
		switch status[0] {
		case 'R', 'C':
			if i+1 >= len(fields) {
				return fmt.Errorf("plan: lineage: malformed diff entry: status %q missing paths", status)
			}
			oldPath, newPath := fields[i], fields[i+1]
			i += 2
			if !allow(oldPath) {
				return fmt.Errorf("plan: lineage: %s %s -> %s: old path outside allowed scope", status, oldPath, newPath)
			}
			if !allow(newPath) {
				return fmt.Errorf("plan: lineage: %s %s -> %s: new path outside allowed scope", status, oldPath, newPath)
			}
		default:
			if i >= len(fields) {
				return fmt.Errorf("plan: lineage: malformed diff entry: status %q missing path", status)
			}
			p := fields[i]
			i++
			if !allow(p) {
				return fmt.Errorf("plan: lineage: %s %s: path outside allowed scope", status, p)
			}
		}
	}
	return nil
}

// splitNULFields splits a `git diff --name-status -z` record stream on NUL.
// Real output always ends with a trailing NUL after the last field; that
// trailing empty field must be dropped, or the main loop would try to read
// one field past the end of the last record.
func splitNULFields(out []byte) []string {
	trimmed := bytes.TrimRight(out, "\x00")
	if len(trimmed) == 0 {
		return nil
	}
	return strings.Split(string(trimmed), "\x00")
}
