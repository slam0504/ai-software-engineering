package spec

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const ScopeVersion = 1 // 對外常數；內部以 scopeVersion 供測試覆寫

var scopeVersion = ScopeVersion

var ScopePatterns = []string{"spec/features/**", "spec/nfr/**", "spec/glossary.md", "spec/context-map/**"}

func setScopeVersionForTest(v int) { scopeVersion = v }

type FileEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type canonical struct {
	ScopeVersion int         `json:"scope_version"`
	Patterns     []string    `json:"patterns"`
	Files        []FileEntry `json:"files"`
}

// ManifestDigest is the package-level SpecScope wrapper (kept for existing
// callers). It copies SpecScope and overrides Version with scopeVersion —
// not SpecScope directly — so setScopeVersionForTest's override still takes
// effect here without mutating the shared SpecScope value.
func ManifestDigest(entries []FileEntry) (string, error) {
	sc := SpecScope
	sc.Version = scopeVersion
	return sc.ManifestDigest(entries)
}

func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// InScope is the package-level SpecScope.Match wrapper (kept for existing
// callers).
func InScope(rel string) bool {
	return SpecScope.Match(rel)
}

// specInScope is SpecScope's Match implementation. `**` 語意＝該目錄下任意
// 深度。純前綴比對 ＋ glossary.md 精確比對即可，不需 glob 函式庫；`..` 已在
// app 層 resolveInWorkspace 擋掉。
func specInScope(rel string) bool {
	rel = strings.TrimPrefix(rel, "./")
	if rel == "spec/glossary.md" {
		return true
	}
	for _, dir := range []string{"spec/features/", "spec/nfr/", "spec/context-map/"} {
		if strings.HasPrefix(rel, dir) {
			return true
		}
	}
	return false
}
