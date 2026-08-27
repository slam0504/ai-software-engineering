package domainspec

import (
	"bytes"
	"os"
	"testing"
)

// TestMutationBundleRuleFlipsCaught（spec §5 出口 6a）：對 gate2-bundle.yaml
// 的 R31 做一次「拿掉 override 檢查」的 mutation testing 突變，證明
// DiffBundles 對規則翻轉有鑑別力——不是隨便跑過就綠，而是真的能點名翻轉點。
//
// mutation 目標：R31 的 when 以 `&&` 連言收尾於
// `sel.override_reason == ”`；替換成 `false` 讓整條連言恆假（R31 從此不會
// 命中任何案例），效果等同「刪掉整條規則」——比 Task 8
// TestDiffBundlesDetectsFlip 移除 `&& sel.override_reason == ”` 子句（讓
// override 檢查失效但其餘四個 AND 分量仍生效）更徹底，兩者互補：Task 8 驗
// 「override 檢查被繞過」，本測試驗「R31 整條規則消失」。
//
// 探測後才動手（非猜測）：testdata/corpus/isolated-R31.json 的固定 fixture
// 剛好落在 R31 命中邊界（override_reason==""、selected_risk_tier 介於
// minimum／planner 之間），baseline 觸發 R31→blocked；本 mutation 移除 R31
// 後，同一筆 snapshot 沒有其他規則命中→pass。經
// `go test -run TestProbeMutationR31 -v` 實測（暫存 probe，未落地）確認
// loadCorpus(t) 全部 42 筆案例中唯一翻轉的是 isolated-R31（其餘 covers R31
// 的案例 precedence-R31-vs-R26 因 R26 獨立命中，Truth 未變，不進 FlipRow）。
func TestMutationBundleRuleFlipsCaught(t *testing.T) {
	// (a) R31 mutation → DiffBundles 必抓翻轉，且明確點名 isolated-R31。
	src, err := os.ReadFile("testdata/gate2-bundle.yaml")
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}

	needle := []byte(`sel.override_reason == ''`)
	if !bytes.Contains(src, needle) {
		t.Fatalf("mutation target substring not found in gate2-bundle.yaml (fixture drifted?)")
	}
	mutated := bytes.Replace(src, needle, []byte(`false`), 1)
	if bytes.Equal(mutated, src) {
		t.Fatal("mutation must actually change the bundle bytes（替換目標字串不存在即紅）")
	}

	base, err := LoadBundle(src, gate2BundleStaticCostLimit)
	if err != nil {
		t.Fatalf("baseline bundle must load: %v", err)
	}
	cand, err := LoadBundle(mutated, gate2BundleStaticCostLimit)
	if err != nil {
		t.Fatalf("mutated bundle must still load: %v", err)
	}
	if cand.Digest == base.Digest {
		t.Fatal("mutated bundle digest must differ from baseline digest")
	}

	cases := loadCorpus(t)

	// coversR31：由 corpus 的 covers_rules 宣告建立，不是憑案例命名猜測——
	// 任何 covers_rules 不含 "R31" 的案例若翻轉，代表 mutation 影響範圍超出
	// R31、DiffBundles 或 bundle 本身有問題。
	coversR31 := map[string]bool{}
	for _, c := range cases {
		for _, id := range c.CoversRules {
			if id == "R31" {
				coversR31[c.Name] = true
			}
		}
	}

	rows, err := DiffBundles(base, cand, cases, gate2BundleRuntimeCostLimit)
	if err != nil {
		t.Fatalf("diff bundles: %v", err)
	}

	flippedIsolated := false
	for _, row := range rows {
		if !coversR31[row.CaseName] {
			t.Fatalf("unexpected flip on case %q（covers_rules 未含 R31，mutation 影響範圍超出預期）", row.CaseName)
		}
		if row.CaseName == "isolated-R31" {
			flippedIsolated = true
			if row.BaselineOutcome != TruthFalse || row.CandidateOutcome != TruthTrue {
				t.Fatalf("expected isolated-R31 blocked->pass flip, got baseline=%s candidate=%s", row.BaselineOutcome, row.CandidateOutcome)
			}
		}
	}
	if !flippedIsolated {
		t.Fatal("isolated-R31 must be among the flips（出口 6a：mutation 必須點名到 R31 隔離案例）")
	}
}

// (b) harness guard 鑑別力（出口 6b，全程式化、無手動實驗）——引用既有測試
// 名稱作為證據，不重複實作：
//
//   - TestOracleFreshnessDetectsCorruption（root
//     domainspec_oracle_freshness_test.go，Task 7）：固化 corpus 的 verdict
//     若被腐化（與 production 重算結果不符），VerifyOracleFreshness 必須
//     點名——證明 oracle 沒有被「複製 production 輸出再回填」架空。
//   - TestCompareCaseContract（compare_test.go，Task 6）：CompareCase 的
//     mismatch／error 分支——比對邏輯本身弱化（例如漏比某個 GoVerdict 欄位）
//     必須讓對應 fixture 由 match 變 mismatch，紅在正題。
//
// 兩者皆已在既有測試套件執行並通過（`go test ./... -count=1`），收斂報告
// （docs/superpowers/specs/2026-08-26-domainspec-spike-results.md）引用其
// 輸出作為出口 6b 的證據。
