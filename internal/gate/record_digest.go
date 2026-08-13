package gate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// RecordDigest 依賴 ApprovalRecord 的 JSON 序列化保持 canonical：未來替
// ApprovalRecord 加欄位時必須標 `omitempty`，否則既存 record 重算出的 digest
// 全數不符，既有 binding 會被判 stale。
func RecordDigest(rec ApprovalRecord) (string, error) {
	b, err := json.Marshal(rec)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
