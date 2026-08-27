package domainspec

import "fmt"

// FlipRow：baseline／candidate 對同一 corpus case 的翻轉紀錄（spec §5 出口 5）。
// 只收三欄（outcome／unknown_leaves／status）任一改變的案例——Outcome 是
// Evaluate 的全域 Truth（不是 corpus 固化的 GoVerdict.Outcome；GoVerdict 只在
// ReplayCorpus／VerifyOracleFreshness 的比對路徑使用）。
type FlipRow struct {
	CaseName                          string
	BaselineOutcome, CandidateOutcome Truth
	BaselineUnknown, CandidateUnknown []string
	BaselineStatus, CandidateStatus   Status
}

// stringSlicesEqual：逐位比對（Result.UnknownLeaves 已由 Evaluate 排序輸出，
// 出口 4 的 deterministic 保證），不需要另外排序。
func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// DiffBundles：baseline／candidate bundle diff（spec §5 出口 5；digest 邊界依
// plan rev5／rev6／rev7 定案，與 ReplayCorpus 的嚴格檢查解衝突）：
//
//  1. 第一步 ValidateCorpus(cases)（fail loud——直接收 []CorpusCase 的入口不得
//     依賴呼叫端已驗過，Go constructor 可繞過 loadCorpus(t)）。
//  2. 逐 evaluated 案例：case.BundleDigest 只對 baseline 驗證（不符→error）；
//     candidate 走獨立評估路徑（直接 Evaluate，不經 ReplayCorpus）——
//     candidate.Digest 與案例 bundle_digest 必然不同，屬預期，不檢查。
//  3. facts_digest 重算與 ValidateFactsSnapshot 逐案例仍檢查（與 ReplayCorpus
//     同一道防繞過防線，不得為了 diff 關掉 fail-loud）。
//  4. baseline／candidate 各自對同一 snapshot 呼叫 Evaluate；三欄
//     （outcome/unknown_leaves/status）任一改變才列入輸出。
//  5. acquisition_failed 案例略過（沒有 facts 可評估，不計入 diff）。
func DiffBundles(baseline, candidate *CompiledBundle, cases []CorpusCase, limit uint64) ([]FlipRow, error) {
	if baseline == nil {
		return nil, fmt.Errorf("domainspec: diff bundles: nil baseline")
	}
	if candidate == nil {
		return nil, fmt.Errorf("domainspec: diff bundles: nil candidate")
	}
	if err := ValidateCorpus(cases); err != nil {
		return nil, err
	}

	var rows []FlipRow
	for _, c := range cases {
		if c.Kind == CorpusKindAcquisitionFailed {
			continue
		}

		if c.BundleDigest != baseline.Digest {
			return nil, fmt.Errorf("domainspec: diff bundles: case %q: bundle_digest drift against baseline: case=%s baseline=%s", c.Name, c.BundleDigest, baseline.Digest)
		}
		digest, err := SnapshotDigest(c.Snapshot)
		if err != nil {
			return nil, fmt.Errorf("domainspec: diff bundles: case %q: snapshot digest: %w", c.Name, err)
		}
		if digest != c.FactsDigest {
			return nil, fmt.Errorf("domainspec: diff bundles: case %q: facts_digest drift: recomputed %s != declared %s", c.Name, digest, c.FactsDigest)
		}
		if err := ValidateFactsSnapshot(c.Snapshot); err != nil {
			return nil, fmt.Errorf("domainspec: diff bundles: case %q: snapshot: %w", c.Name, err)
		}

		baseResult, err := Evaluate(baseline, c.Snapshot, limit)
		if err != nil {
			return nil, fmt.Errorf("domainspec: diff bundles: case %q: baseline evaluate: %w", c.Name, err)
		}
		candResult, err := Evaluate(candidate, c.Snapshot, limit)
		if err != nil {
			return nil, fmt.Errorf("domainspec: diff bundles: case %q: candidate evaluate: %w", c.Name, err)
		}

		if baseResult.Truth == candResult.Truth &&
			baseResult.Status == candResult.Status &&
			stringSlicesEqual(baseResult.UnknownLeaves, candResult.UnknownLeaves) {
			continue
		}

		rows = append(rows, FlipRow{
			CaseName:         c.Name,
			BaselineOutcome:  baseResult.Truth,
			CandidateOutcome: candResult.Truth,
			BaselineUnknown:  baseResult.UnknownLeaves,
			CandidateUnknown: candResult.UnknownLeaves,
			BaselineStatus:   baseResult.Status,
			CandidateStatus:  candResult.Status,
		})
	}
	return rows, nil
}
