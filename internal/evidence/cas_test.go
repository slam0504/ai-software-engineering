package evidence

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestPutCASWritesUnderSha256WithNoTempLeftover(t *testing.T) {
	dir := t.TempDir()
	data := []byte("hello CAS")

	digest, path, err := PutCAS(dir, data)
	if err != nil {
		t.Fatalf("PutCAS: %v", err)
	}

	wantDigest := "sha256:b6516af51d41cc3600f54e3cf10cf5946fccc323480338c5504fa9a93421e2e6"
	if digest != wantDigest {
		t.Errorf("digest = %q, want %q", digest, wantDigest)
	}
	wantPath := filepath.Join(dir, "sha256", "b6516af51d41cc3600f54e3cf10cf5946fccc323480338c5504fa9a93421e2e6")
	if path != wantPath {
		t.Errorf("path = %q, want %q", path, wantPath)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	if string(got) != string(data) {
		t.Errorf("file content = %q, want %q", got, data)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", dir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			t.Errorf("unexpected temp leftover in %q: %s", dir, e.Name())
		}
	}
}

func TestOpenCASRoundTrip(t *testing.T) {
	dir := t.TempDir()
	data := []byte("round trip content")

	digest, _, err := PutCAS(dir, data)
	if err != nil {
		t.Fatalf("PutCAS: %v", err)
	}

	got, err := OpenCAS(dir, digest)
	if err != nil {
		t.Fatalf("OpenCAS: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("OpenCAS content = %q, want %q", got, data)
	}
}

// TestOpenCASDetectsTampering guards §3.9: a CAS object mutated on disk
// after PutCAS wrote it must be rejected on read, not silently returned.
func TestOpenCASDetectsTampering(t *testing.T) {
	dir := t.TempDir()
	digest, path, err := PutCAS(dir, []byte("original content"))
	if err != nil {
		t.Fatalf("PutCAS: %v", err)
	}

	if err := os.WriteFile(path, []byte("tampered content"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}

	if _, err := OpenCAS(dir, digest); err == nil {
		t.Fatal("want OpenCAS to reject tampered content, got nil error")
	}
}

func TestOpenCASRejectsMalformedDigest(t *testing.T) {
	dir := t.TempDir()
	cases := []string{
		"",
		"not-a-digest",
		"sha256:tooshort",
		"sha256:" + string(make([]byte, 64)), // 64 NUL bytes: right length, not hex
		"md5:d41d8cd98f00b204e9800998ecf8427e",
	}
	for _, digest := range cases {
		if _, err := OpenCAS(dir, digest); err == nil {
			t.Errorf("OpenCAS(%q): want error for malformed digest, got nil", digest)
		}
	}
}

func TestOpenCASMissingObject(t *testing.T) {
	dir := t.TempDir()
	missing := "sha256:" + strings.Repeat("0", 64)
	if _, err := OpenCAS(dir, missing); err == nil {
		t.Fatal("want error for missing CAS object, got nil")
	}
}

func TestPutCASReplayIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	data := []byte("replayed content")

	digest1, path1, err := PutCAS(dir, data)
	if err != nil {
		t.Fatalf("PutCAS (first): %v", err)
	}
	digest2, path2, err := PutCAS(dir, data)
	if err != nil {
		t.Fatalf("PutCAS (replay): %v", err)
	}

	if digest1 != digest2 {
		t.Errorf("replay digest = %q, want %q", digest2, digest1)
	}
	if path1 != path2 {
		t.Errorf("replay path = %q, want %q", path2, path1)
	}

	got, err := OpenCAS(dir, digest2)
	if err != nil {
		t.Fatalf("OpenCAS after replay: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("content after replay = %q, want %q", got, data)
	}
}

// TestPutCASConcurrentSameContentConverges runs 8 goroutines concurrently
// PutCAS-ing the SAME content into a shared dir (run with -race): each
// creates its own temp file (os.CreateTemp is unique per call) and renames
// it onto the same final <dir>/sha256/<hex> path, so this exercises the
// rename-onto-existing-target overwrite path PutCAS's doc comment describes
// as its idempotent-replay strategy, but under genuine concurrency rather
// than sequential replay (which TestPutCASReplayIsIdempotent already
// covers). Every goroutine must see the same digest and path, no goroutine
// may error, and the resulting CAS object must round-trip the original
// content.
func TestPutCASConcurrentSameContentConverges(t *testing.T) {
	dir := t.TempDir()
	data := []byte("concurrent identical content")
	const n = 8

	digests := make([]string, n)
	paths := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			digests[i], paths[i], errs[i] = PutCAS(dir, data)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: PutCAS: %v", i, err)
		}
		if digests[i] != digests[0] {
			t.Errorf("goroutine %d digest = %q, want %q", i, digests[i], digests[0])
		}
		if paths[i] != paths[0] {
			t.Errorf("goroutine %d path = %q, want %q", i, paths[i], paths[0])
		}
	}

	got, err := OpenCAS(dir, digests[0])
	if err != nil {
		t.Fatalf("OpenCAS after concurrent PutCAS: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("content after concurrent PutCAS = %q, want %q", got, data)
	}
}

func TestCleanOrphanTempsOnlyRemovesTempFiles(t *testing.T) {
	dir := t.TempDir()
	digest, casPath, err := PutCAS(dir, []byte("keep me"))
	if err != nil {
		t.Fatalf("PutCAS: %v", err)
	}

	tmpPath := filepath.Join(dir, ".tmp-orphan-123")
	if err := os.WriteFile(tmpPath, []byte("leftover"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", tmpPath, err)
	}
	// A non-.tmp- file directly under dir must also survive (defends
	// against an over-broad prefix match).
	otherPath := filepath.Join(dir, "not-a-temp.txt")
	if err := os.WriteFile(otherPath, []byte("unrelated"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", otherPath, err)
	}

	removed, err := CleanOrphanTemps(dir)
	if err != nil {
		t.Fatalf("CleanOrphanTemps: %v", err)
	}
	if len(removed) != 1 || removed[0] != tmpPath {
		t.Errorf("removed = %v, want [%q]", removed, tmpPath)
	}

	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Errorf("want %q removed, stat err = %v", tmpPath, err)
	}
	if _, err := os.Stat(otherPath); err != nil {
		t.Errorf("want %q to survive, stat err = %v", otherPath, err)
	}
	if _, err := os.Stat(casPath); err != nil {
		t.Errorf("want CAS object %q to survive, stat err = %v", casPath, err)
	}

	got, err := OpenCAS(dir, digest)
	if err != nil {
		t.Fatalf("OpenCAS after clean: %v", err)
	}
	if string(got) != "keep me" {
		t.Errorf("content after clean = %q, want %q", got, "keep me")
	}
}

func TestCleanOrphanTempsOnNonexistentDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	removed, err := CleanOrphanTemps(dir)
	if err != nil {
		t.Fatalf("CleanOrphanTemps on missing dir: %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("removed = %v, want empty", removed)
	}
}

// ---- §3.7 crash-boundary injection ----
//
// Each test hand-stages the on-disk state a crash would leave at that exact
// boundary, then runs the recovery operation available at this layer
// (CleanOrphanTemps, or a PutCAS replay) and asserts the invariant that must
// survive: journal never points at a missing file, and recovery is
// idempotent. The journal itself is Task 20's responsibility — at this
// layer "journal append succeeds" reduces to "the CAS object the journal
// entry would reference exists and passes content-address verification".

// Boundary (a): crash after the temp file was fsynced but before rename —
// only a .tmp-* file exists, no CAS object, no journal line. Recovery must
// clean the temp file and have no other effect.
func TestCrashBoundaryA_TempSyncedBeforeRename(t *testing.T) {
	dir := t.TempDir()
	casDir := filepath.Join(dir, "sha256")

	// Hand-stage: a synced temp file sitting in dir, no sha256/ dir at all
	// (as if PutCAS crashed after tmp.Sync()+Close() but before the
	// sha256/ mkdir + rename).
	tmpPath := filepath.Join(dir, ".tmp-crash-a")
	if err := os.WriteFile(tmpPath, []byte("orphaned by crash before rename"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", tmpPath, err)
	}

	removed, err := CleanOrphanTemps(dir)
	if err != nil {
		t.Fatalf("CleanOrphanTemps: %v", err)
	}
	if len(removed) != 1 || removed[0] != tmpPath {
		t.Fatalf("removed = %v, want [%q]", removed, tmpPath)
	}
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Errorf("want temp file gone after recovery, stat err = %v", err)
	}
	if _, err := os.Stat(casDir); !os.IsNotExist(err) {
		t.Errorf("want no sha256/ dir created as a side effect, stat err = %v", err)
	}
}

// Boundary (b): crash after rename but before the containing directory was
// fsynced — the CAS object is in place (on most filesystems the rename is
// visible even without the dir fsync; the fsync only guarantees it survives
// a subsequent power loss) but no journal line references it yet. Recovery
// must leave the CAS object alone (an unreferenced CAS object is a harmless
// orphan), and a PutCAS replay of the same content must be idempotent —
// same digest, same path.
func TestCrashBoundaryB_RenamedBeforeDirSync(t *testing.T) {
	dir := t.TempDir()
	data := []byte("boundary b content")

	digest, path, err := PutCAS(dir, data)
	if err != nil {
		t.Fatalf("PutCAS: %v", err)
	}

	// Simulate "recovery runs before any journal line was appended": the
	// only cleanup available at this layer is CleanOrphanTemps, and it
	// must not touch the CAS object.
	if _, err := CleanOrphanTemps(dir); err != nil {
		t.Fatalf("CleanOrphanTemps: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("want CAS object to survive recovery as a harmless orphan, stat err = %v", err)
	}

	// A replay (e.g. the task that produced this mutation re-runs after
	// the crash, before any journal line was committed) must be
	// idempotent.
	digest2, path2, err := PutCAS(dir, data)
	if err != nil {
		t.Fatalf("PutCAS (replay): %v", err)
	}
	if digest2 != digest {
		t.Errorf("replay digest = %q, want %q", digest2, digest)
	}
	if path2 != path {
		t.Errorf("replay path = %q, want %q", path2, path)
	}
}

// Boundary (c): crash after the directory sync but before the journal
// append. Same on-disk precondition as (b); the additional invariant is
// that after a replay, the object a journal append would reference exists
// and is content-address-valid (i.e. the journal append that Task 20 will
// perform is safe to run).
func TestCrashBoundaryC_DirSyncedBeforeJournalAppend(t *testing.T) {
	dir := t.TempDir()
	data := []byte("boundary c content")

	digest, _, err := PutCAS(dir, data)
	if err != nil {
		t.Fatalf("PutCAS: %v", err)
	}

	// Replay before the (not-yet-implemented) journal append.
	digest2, _, err := PutCAS(dir, data)
	if err != nil {
		t.Fatalf("PutCAS (replay): %v", err)
	}
	if digest2 != digest {
		t.Fatalf("replay digest = %q, want %q", digest2, digest)
	}

	// The invariant a journal append depends on: the referenced object
	// exists and passes content-address verification.
	got, err := OpenCAS(dir, digest2)
	if err != nil {
		t.Fatalf("OpenCAS: object a journal append would reference must be readable and valid: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("content = %q, want %q", got, data)
	}
}
