package main

import (
	"errors"
	"testing"
)

// A1b-1 驗收條件 5：新檔入口（前端 createNewFile）被排除在「寫入前外部變更檢查」
// 之外，理由**不是**「新檔路徑必然不存在」，而是後端本來就有對稱的拒絕：
//
//	目標已存在 → expectedDigest 必須等於現檔 digest（app.go:3766／4478）。
//	              createNewFile 送的是空字串，而既存檔 digest 恆為非空 "sha256:…"，
//	              兩者必然不相等 → 直接拒絕。
//	目標不存在 → 非空 expectedDigest 視為呼叫端假設過期（app.go:3770／4482）→ 拒絕。
//
// 設計稿要求「以測試證明該拒絕路徑仍成立，不得只引用程式碼」，故本檔把兩個方向
// 各自釘住。既有的 TestSpecWriteConflictOnStaleExpectedDigest 用的是**非空但錯誤**
// 的 digest，涵蓋不到「空 digest 撞既存檔」這一格——那正是新檔入口的實際形狀。

func TestSpecWriteEmptyDigestOnExistingFileConflicts(t *testing.T) {
	a, _ := newTestApp(t)
	if _, err := a.SpecWrite("spec/glossary.md", "v1", ""); err != nil {
		t.Fatalf("前置：建立新檔應成功: %v", err)
	}
	// 模擬「列表之後、寫入之前，該路徑被外部建立」：新檔入口仍送空 digest。
	if _, err := a.SpecWrite("spec/glossary.md", "v2", ""); !errors.Is(err, ErrSpecWriteConflict) {
		t.Fatalf("空 expected_digest 撞既存檔必須衝突，前端新檔入口才不需自行檢查: %v", err)
	}
	// 拒絕必須是「沒寫進去」，不是寫了再回錯。
	sf, err := a.SpecRead("spec/glossary.md")
	if err != nil {
		t.Fatal(err)
	}
	if sf.Content != "v1" {
		t.Fatalf("被拒絕的寫入不得改動檔案內容: got %q, want %q", sf.Content, "v1")
	}
}

func TestSpecWriteNonEmptyDigestOnMissingFileConflicts(t *testing.T) {
	a, _ := newTestApp(t)
	if _, err := a.SpecWrite("spec/glossary.md", "v1", "sha256:whatever"); !errors.Is(err, ErrSpecWriteConflict) {
		t.Fatalf("檔案不存在時非空 expected_digest 必須衝突（呼叫端假設過期）: %v", err)
	}
	if _, err := a.SpecRead("spec/glossary.md"); err == nil {
		t.Fatal("被拒絕的寫入不得建立檔案")
	}
}

func TestPlanWriteEmptyDigestOnExistingFileConflicts(t *testing.T) {
	a, _ := newTestApp(t)
	if _, err := a.PlanWrite("plan/P1.yaml", "plan_id: P1\n", ""); err != nil {
		t.Fatalf("前置：建立新檔應成功: %v", err)
	}
	if _, err := a.PlanWrite("plan/P1.yaml", "plan_id: P1x\n", ""); !errors.Is(err, ErrPlanWriteConflict) {
		t.Fatalf("空 expected_digest 撞既存檔必須衝突: %v", err)
	}
	pf, err := a.PlanRead("plan/P1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if pf.Content != "plan_id: P1\n" {
		t.Fatalf("被拒絕的寫入不得改動檔案內容: got %q", pf.Content)
	}
}

func TestPlanWriteNonEmptyDigestOnMissingFileConflicts(t *testing.T) {
	a, _ := newTestApp(t)
	if _, err := a.PlanWrite("plan/P1.yaml", "plan_id: P1\n", "sha256:whatever"); !errors.Is(err, ErrPlanWriteConflict) {
		t.Fatalf("檔案不存在時非空 expected_digest 必須衝突: %v", err)
	}
	if _, err := a.PlanRead("plan/P1.yaml"); err == nil {
		t.Fatal("被拒絕的寫入不得建立檔案")
	}
}
