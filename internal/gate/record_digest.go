package gate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func RecordDigest(rec ApprovalRecord) (string, error) {
	b, err := json.Marshal(rec)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
