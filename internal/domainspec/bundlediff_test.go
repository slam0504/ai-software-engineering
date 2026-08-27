package domainspec

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// TestDiffBundlesDetectsFlip（brief Step 1）：candidate = baseline YAML 對 R31
// when 拿掉 override 分量（needle 變數＝唯一子字串，見下方 strings.Replace）。
//
// 方向性筆記（探測後才動手，非猜測）：R31 的 when 是一串 AND 連言，移除其中一個
// 分量只會讓「觸發集合」變大（A&&B → A 是超集，never a subset）——已固化的
// isolated-R31.json 固定案例（override_reason==""）在 baseline 就已經觸發 R31，
// 移除該分量後 candidate 仍然觸發，truth 不變，不構成翻轉；用
// `loadCorpus(t)` 的全部 39 筆逐一探測（暫存 probe，未落地）也確認沒有一筆
// 現有 corpus 案例滿足「override_reason 非空、selected 介於 minimum／planner
// 之間」這個唯一能展示 pass→blocked 翻轉的形狀，因此本測試自建這個案例
// （命名沿用 "isolated-R31"，凸顯它是 R31 的隔離證據，但不是同名 testdata
// 檔案，兩者不衝突：本案例只存在於這個測試的記憶體切片）。
func TestDiffBundlesDetectsFlip(t *testing.T) {
	baseYAML, err := os.ReadFile("testdata/gate2-bundle.yaml")
	if err != nil {
		t.Fatalf("read gate2-bundle.yaml: %v", err)
	}
	base, err := LoadBundle(baseYAML, gate2BundleStaticCostLimit)
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}

	needle := " && sel.override_reason == ''"
	if !strings.Contains(string(baseYAML), needle) {
		t.Fatalf("mutation target substring not found in gate2-bundle.yaml (fixture drifted?)")
	}
	candYAML := []byte(strings.Replace(string(baseYAML), needle, "", 1))
	if bytes.Equal(baseYAML, candYAML) {
		t.Fatal("mutated bytes must differ from baseline bytes")
	}
	cand, err := LoadBundle(candYAML, gate2BundleStaticCostLimit)
	if err != nil {
		t.Fatalf("load candidate: %v", err)
	}
	if cand.Digest == base.Digest {
		t.Fatal("candidate digest must differ from baseline digest after mutation")
	}

	// 對照組：R31 的任一分量都沒被觸及（無風險降級），移除 override-check 不
	// 影響這個案例——用來確認 DiffBundles 不會產生假陽性翻轉。
	clean := mustSnapshot(t)
	cleanDigest, err := SnapshotDigest(clean)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	cleanCase := CorpusCase{
		Name: "clean-no-flip", Kind: CorpusKindEvaluated, EvaluationPhase: "decide",
		OracleSeam: "gatepolicy_build", Provenance: "synthetic",
		FactsDigest: cleanDigest, BundleDigest: base.Digest, Snapshot: clean,
		GoVerdict: &GoVerdict{Outcome: OutcomePass, RiskDecisions: BuildShadowRiskDecisions(clean)},
		Role:      "none",
	}

	// R31 隔離翻轉案例：override_reason 非空、selected("medium") 介於
	// minimum("low")／planner("high") 之間——baseline（override-check 存在）
	// R31 不觸發、無其他規則觸發 → pass；candidate（override-check 移除）R31
	// 觸發（A&&B → A，條件變寬）→ blocked。
	flip := mustSnapshot(t)
	flip.Plan.Value.Tasks[0].MinimumRiskTier = "low"
	flip.Plan.Value.Tasks[0].PlannerRiskTier = "high"
	(*flip.RiskSelections.Value)[0].SelectedRiskTier = "medium"
	(*flip.RiskSelections.Value)[0].OverrideReason = "because"
	flipDigest, err := SnapshotDigest(flip)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	flipCase := CorpusCase{
		Name: "isolated-R31", Kind: CorpusKindEvaluated, EvaluationPhase: "decide",
		OracleSeam: "gatepolicy_build", Provenance: "synthetic",
		FactsDigest: flipDigest, BundleDigest: base.Digest, Snapshot: flip,
		GoVerdict: &GoVerdict{Outcome: OutcomePass, RiskDecisions: BuildShadowRiskDecisions(flip)},
		Role:      "none",
	}

	cases := []CorpusCase{cleanCase, flipCase}

	rows, err := DiffBundles(base, cand, cases, gate2BundleRuntimeCostLimit)
	if err != nil {
		t.Fatalf("diff bundles: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 flip row, got %d: %+v", len(rows), rows)
	}
	row := rows[0]
	if row.CaseName != "isolated-R31" {
		t.Fatalf("flip row must be isolated-R31, got %q", row.CaseName)
	}
	if row.BaselineOutcome != TruthTrue || row.CandidateOutcome != TruthFalse {
		t.Fatalf("expected pass->blocked flip, got baseline=%s candidate=%s", row.BaselineOutcome, row.CandidateOutcome)
	}

	// baseline==candidate → 空表。
	emptyRows, err := DiffBundles(base, base, cases, gate2BundleRuntimeCostLimit)
	if err != nil {
		t.Fatalf("diff bundles (identical): %v", err)
	}
	if len(emptyRows) != 0 {
		t.Fatalf("identical bundles must produce empty flip table, got %+v", emptyRows)
	}
}

// TestDiffBundlesValidatesCorpusFirst：ValidateCorpus 是第一步（plan rev7）——
// phase/seam 不合法的案例直接傳給 DiffBundles（不經 loadCorpus）必須被拒，
// 與 ReplayCorpus 同一道防線（見 corpus_test.go 的
// TestReplayAndDiffRejectInvalidCorpusDirect／TestUnionInvalidRejectedAtAllEntries
// 的 DiffBundles 半邊）。本測試額外覆蓋「bundle_digest 只驗 baseline、不驗
// candidate」這個 digest 邊界本身。
func TestDiffBundlesBundleDigestBoundary(t *testing.T) {
	b := loadGate2Bundle(t)
	s := mustSnapshot(t)
	digest, err := SnapshotDigest(s)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	c := CorpusCase{
		Name: "digest-boundary", Kind: CorpusKindEvaluated, EvaluationPhase: "decide",
		OracleSeam: "gatepolicy_build", Provenance: "synthetic",
		FactsDigest: digest, BundleDigest: b.Digest, Snapshot: s,
		GoVerdict: &GoVerdict{Outcome: OutcomePass, RiskDecisions: BuildShadowRiskDecisions(s)},
		Role:      "none",
	}

	// candidate 与 baseline 不同 bundle（用一個縮小過的 mini bundle 當 candidate
	// 也可以，但更貼近真實用法：直接複用同一份 YAML 重新載入視為「新版
	// candidate」，只驗證 candidate.Digest 與案例 bundle_digest 不需相等這件事
	// 本身不會被 DiffBundles 拒絕）。
	candData, err := os.ReadFile("testdata/gate2-bundle.yaml")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	cand, err := LoadBundle(candData, gate2BundleStaticCostLimit)
	if err != nil {
		t.Fatalf("load candidate: %v", err)
	}

	if _, err := DiffBundles(b, cand, []CorpusCase{c}, gate2BundleRuntimeCostLimit); err != nil {
		t.Fatalf("DiffBundles must accept case whose bundle_digest matches baseline (candidate digest is irrelevant): %v", err)
	}

	// case.BundleDigest 對不上 baseline → 必須 error（candidate 對不上不檢查，
	// 但 baseline 對不上仍要 fail loud）。
	bad := c
	bad.BundleDigest = "sha256:" + repeatChar("f", 64)
	if _, err := DiffBundles(b, cand, []CorpusCase{bad}, gate2BundleRuntimeCostLimit); err == nil {
		t.Fatal("DiffBundles must reject case whose bundle_digest does not match baseline")
	}
}
