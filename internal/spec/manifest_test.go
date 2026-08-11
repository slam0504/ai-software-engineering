package spec

import "testing"

func TestManifestDigestDeterministicAndOrdered(t *testing.T) {
	a := []FileEntry{{Path: "spec/nfr/perf.md", SHA256: "bb"}, {Path: "spec/features/a.feature", SHA256: "aa"}}
	b := []FileEntry{{Path: "spec/features/a.feature", SHA256: "aa"}, {Path: "spec/nfr/perf.md", SHA256: "bb"}}
	da, err := ManifestDigest(a)
	if err != nil {
		t.Fatal(err)
	}
	db, _ := ManifestDigest(b)
	if da != db {
		t.Fatalf("order must not affect digest: %s vs %s", da, db)
	}
	if len(da) != len("sha256:")+64 {
		t.Fatalf("bad digest shape: %s", da)
	}
}

func TestScopeVersionInCanonical(t *testing.T) {
	// 改 scope_version 必須改 digest（否則改 scope 可排除檔案而不觸發 STALE）
	e := []FileEntry{{Path: "spec/glossary.md", SHA256: "aa"}}
	d1, _ := ManifestDigest(e)
	orig := ScopeVersion
	t.Cleanup(func() { setScopeVersionForTest(orig) })
	setScopeVersionForTest(orig + 1)
	d2, _ := ManifestDigest(e)
	if d1 == d2 {
		t.Fatal("scope_version must be part of canonical content")
	}
}

func TestInScope(t *testing.T) {
	for _, p := range []string{"spec/features/x.feature", "spec/nfr/a.md", "spec/glossary.md", "spec/context-map/c4.mmd"} {
		if !InScope(p) {
			t.Errorf("want in-scope: %s", p)
		}
	}
	for _, p := range []string{"spec/other.md", "app.go", "spec/glossary.md.bak"} {
		if InScope(p) {
			t.Errorf("want out-of-scope: %s", p)
		}
	}
}
