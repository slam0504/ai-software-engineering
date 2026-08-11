package spec

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
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

func ManifestDigest(entries []FileEntry) (string, error) {
	files := append([]FileEntry(nil), entries...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	c := canonical{ScopeVersion: scopeVersion, Patterns: ScopePatterns, Files: files}
	b, err := json.Marshal(c) // struct 欄位序固定 → canonical；無時間欄位
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// InScope：`**` 語意＝該目錄下任意深度。純前綴比對 ＋ glossary.md 精確比對即可，
// 不需 glob 函式庫；`..` 已在 app 層 resolveInWorkspace 擋掉。
func InScope(rel string) bool {
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
