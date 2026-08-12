package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PutCAS writes data into dir as a content-addressed object and returns its
// digest ("sha256:<hex>") and final path (<dir>/sha256/<hex>).
//
// §3.7 freezes the durability order — each step is a crash boundary and
// cas_test.go injects a crash at each one by hand-staging the resulting disk
// state and asserting the invariant that must survive it:
//
//  1. os.MkdirAll(dir) so the temp file below has somewhere to land.
//  2. Write data to a same-directory temp file (os.CreateTemp(dir, ".tmp-*")
//     — same filesystem as the final path, so the rename in step 5 is atomic).
//  3. f.Sync() then Close() — content is durable before it is ever visible
//     under a permanent name.
//  4. os.MkdirAll(<dir>/sha256) so the target directory exists, then
//     os.Rename the temp file into <dir>/sha256/<hex>.
//  5. Open the sha256/ directory and Sync it — the rename (a directory
//     entry change) is durable, not just the file content.
//
// Replaying the same content is idempotent: renaming a temp file onto an
// existing same-digest target overwrites it with byte-identical content, so
// PutCAS does not special-case replay with a Stat short-circuit — the extra
// temp-file churn is cheap and keeps a single code path for both the first
// write and any replay.
func PutCAS(dir string, data []byte) (digest string, path string, err error) {
	sum := sha256.Sum256(data)
	hexDigest := hex.EncodeToString(sum[:])
	digest = "sha256:" + hexDigest

	if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
		return "", "", fmt.Errorf("evidence: put CAS: mkdir %q: %w", dir, mkErr)
	}

	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return "", "", fmt.Errorf("evidence: put CAS: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	// No-op once Rename below has moved tmpPath to its final name; only
	// fires if we bail out before the rename.
	defer os.Remove(tmpPath)

	if _, werr := tmp.Write(data); werr != nil {
		_ = tmp.Close()
		return "", "", fmt.Errorf("evidence: put CAS: write temp: %w", werr)
	}
	if serr := tmp.Sync(); serr != nil {
		_ = tmp.Close()
		return "", "", fmt.Errorf("evidence: put CAS: sync temp: %w", serr)
	}
	if cerr := tmp.Close(); cerr != nil {
		return "", "", fmt.Errorf("evidence: put CAS: close temp: %w", cerr)
	}

	casDir := filepath.Join(dir, "sha256")
	if mkErr := os.MkdirAll(casDir, 0o755); mkErr != nil {
		return "", "", fmt.Errorf("evidence: put CAS: mkdir %q: %w", casDir, mkErr)
	}
	finalPath := filepath.Join(casDir, hexDigest)
	if rerr := os.Rename(tmpPath, finalPath); rerr != nil {
		return "", "", fmt.Errorf("evidence: put CAS: rename: %w", rerr)
	}

	dirFile, err := os.Open(casDir)
	if err != nil {
		return "", "", fmt.Errorf("evidence: put CAS: open %q for dir sync: %w", casDir, err)
	}
	defer dirFile.Close()
	if serr := dirFile.Sync(); serr != nil {
		return "", "", fmt.Errorf("evidence: put CAS: sync dir %q: %w", casDir, serr)
	}

	return digest, finalPath, nil
}

// OpenCAS reads back the object addressed by digest and re-verifies its
// content against that digest before returning it — the content-addressed
// read-side check required by §3.9. A digest that doesn't parse, a missing
// object, or an object whose recomputed digest doesn't match (bit rot,
// on-disk tampering) are all reported as errors rather than trusted.
func OpenCAS(dir string, digest string) ([]byte, error) {
	hexDigest, err := parseCASDigest(digest)
	if err != nil {
		return nil, fmt.Errorf("evidence: open CAS: %w", err)
	}

	path := filepath.Join(dir, "sha256", hexDigest)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("evidence: open CAS: read %q: %w", path, err)
	}

	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if got != hexDigest {
		return nil, fmt.Errorf("evidence: open CAS: digest mismatch for %q: want sha256:%s, got sha256:%s", path, hexDigest, got)
	}
	return data, nil
}

// parseCASDigest validates the "sha256:<64 lowercase hex chars>" digest
// format and returns the hex part. Rejecting a malformed digest here means
// OpenCAS never builds a path from unvalidated input.
func parseCASDigest(digest string) (string, error) {
	hexDigest, ok := strings.CutPrefix(digest, "sha256:")
	if !ok {
		return "", fmt.Errorf("digest %q: missing %q prefix", digest, "sha256:")
	}
	if len(hexDigest) != 64 {
		return "", fmt.Errorf("digest %q: hex part must be 64 chars, got %d", digest, len(hexDigest))
	}
	if _, err := hex.DecodeString(hexDigest); err != nil {
		return "", fmt.Errorf("digest %q: invalid hex: %w", digest, err)
	}
	return hexDigest, nil
}

// CleanOrphanTemps removes leftover ".tmp-*" files directly under dir — the
// residue of a PutCAS that crashed at or before §3.7 boundary (a) (temp
// synced, rename never happened). It never touches dir/sha256/: a CAS
// object with no journal entry pointing at it yet (boundary (b) or (c)) is
// a harmless orphan, not corruption, and cleaning it here would risk
// deleting content a journal replay is about to reference.
func CleanOrphanTemps(dir string) (removed []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("evidence: clean orphan temps: read dir %q: %w", dir, err)
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), ".tmp-") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if rerr := os.Remove(p); rerr != nil {
			return removed, fmt.Errorf("evidence: clean orphan temps: remove %q: %w", p, rerr)
		}
		removed = append(removed, p)
	}
	return removed, nil
}
