package main

import (
	"strings"
	"testing"
)

// TestWriteConflictMessage：A1a-1 契約 T15——ErrSpecWriteConflict／ErrPlanWriteConflict
// 的訊息都必須含子字串 "write conflict: expected_digest"，供前端（SpecWorkspace／
// PlanWorkspace 的 write mock reject 訊息）以字串比對辨識這是樂觀鎖 digest 衝突，藉此
// 決定是否標記 data-conflict="true"。兩個 sentinel 已存在於 app.go
// （ErrSpecWriteConflict / ErrPlanWriteConflict），本測試只驗證訊息格式，不驗證
// errors.Is 語意——預期現在就會通過（非 expected-red）。
func TestWriteConflictMessage(t *testing.T) {
	const want = "write conflict: expected_digest"

	if !strings.Contains(ErrSpecWriteConflict.Error(), want) {
		t.Fatalf("ErrSpecWriteConflict.Error() = %q, want substring %q", ErrSpecWriteConflict.Error(), want)
	}
	if !strings.Contains(ErrPlanWriteConflict.Error(), want) {
		t.Fatalf("ErrPlanWriteConflict.Error() = %q, want substring %q", ErrPlanWriteConflict.Error(), want)
	}
}
