// gate3_manifest.go——B5 spec §5.1(5)(6) canonical manifest 收斂。
// Digest 沿 domainspec canonical 先例：struct 宣告序＝spec 字面序，
// encoding/json 的欄位序即 canonical 序；陣列由 Build* 排序後才進 struct。
package gatepolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/forge"
)

type RequiredCheckEntry struct {
	Context string `json:"context"`
	AppID   *int64 `json:"app_id"`
}
type CheckRunEntry struct {
	Context       string `json:"context"`
	RequiredAppID *int64 `json:"required_app_id"`
	RunName       string `json:"run_name"`
	RunAppID      int64  `json:"run_app_id"`
	RunID         int64  `json:"run_id"`
	HeadSHA       string `json:"head_sha"`
	Status        string `json:"status"`
	Conclusion    string `json:"conclusion"`
}
type RequiredCheckManifest struct {
	ManifestSchema int                  `json:"manifest_schema"`
	RequiredChecks []RequiredCheckEntry `json:"required_checks"`
	Runs           []CheckRunEntry      `json:"runs"`
}

func ManifestDigest(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func keyOf(ctx string, app *int64) string {
	if app == nil {
		return ctx + "\x00*"
	}
	return fmt.Sprintf("%s\x00%d", ctx, *app)
}

// BuildRequiredCheckManifest：attribution＋current-effective＋排序＋一對一
// coverage（B5 §5.1(5)）。任何違規（重複 required key、歸屬歧義、缺漏、
// 一 run 多歸屬）→ error，不產出部分 manifest。
func BuildRequiredCheckManifest(rc forge.RequiredChecks, head forge.OID) (RequiredCheckManifest, error) {
	seen := map[string]bool{}
	for _, r := range rc.Required {
		k := keyOf(r.Context, r.AppID)
		if seen[k] {
			return RequiredCheckManifest{}, fmt.Errorf("required check 重複 key：%s", k)
		}
		seen[k] = true
	}
	used := map[int64]string{} // run_id → 已歸屬的 required key（一 run 至多歸屬一 required）
	var runs []CheckRunEntry
	for _, r := range rc.Required {
		var candidates []forge.CheckRun
		apps := map[int64]bool{}
		for _, cr := range rc.Runs {
			if cr.Name != r.Context {
				continue
			}
			if r.AppID != nil && cr.AppID != *r.AppID {
				continue
			}
			candidates = append(candidates, cr)
			apps[cr.AppID] = true
		}
		if r.AppID == nil && len(apps) > 1 {
			return RequiredCheckManifest{}, fmt.Errorf("required %q（app_id 不限）歸屬歧義：%d 個不同 app 的同名 run", r.Context, len(apps))
		}
		if len(candidates) == 0 {
			return RequiredCheckManifest{}, fmt.Errorf("required %s missing：無可歸屬 run", keyOf(r.Context, r.AppID))
		}
		// current-effective：started_at 最新、tie run_id 大者。RFC3339 嚴格
		// parse 後以 time.Time 比較（rev2 修——字典序在不同時區偏移下
		// 不等於時間序）；格式錯誤 fail loud。
		effIdx := -1
		var effTime time.Time
		for i, c := range candidates {
			ts, perr := time.Parse(time.RFC3339, c.StartedAt)
			if perr != nil {
				return RequiredCheckManifest{}, fmt.Errorf("run %d started_at 非 RFC3339：%q", c.RunID, c.StartedAt)
			}
			if effIdx < 0 || ts.After(effTime) ||
				(ts.Equal(effTime) && c.RunID > candidates[effIdx].RunID) {
				effIdx, effTime = i, ts
			}
		}
		eff := candidates[effIdx]
		if prev, ok := used[eff.RunID]; ok {
			return RequiredCheckManifest{}, fmt.Errorf("run %d 多重歸屬：%s 與 %s", eff.RunID, prev, keyOf(r.Context, r.AppID))
		}
		used[eff.RunID] = keyOf(r.Context, r.AppID)
		runs = append(runs, CheckRunEntry{Context: r.Context, RequiredAppID: r.AppID,
			RunName: eff.Name, RunAppID: eff.AppID, RunID: eff.RunID, HeadSHA: string(eff.HeadOID),
			Status: eff.Status, Conclusion: eff.Conclusion})
	}
	required := append([]forge.RequiredCheckRef(nil), rc.Required...)
	sort.Slice(required, func(i, j int) bool {
		return keyOf(required[i].Context, required[i].AppID) < keyOf(required[j].Context, required[j].AppID)
	})
	sort.Slice(runs, func(i, j int) bool {
		return keyOf(runs[i].Context, runs[i].RequiredAppID) < keyOf(runs[j].Context, runs[j].RequiredAppID)
	})
	entries := make([]RequiredCheckEntry, len(required))
	for i, r := range required {
		entries[i] = RequiredCheckEntry{Context: r.Context, AppID: r.AppID}
	}
	return RequiredCheckManifest{ManifestSchema: 1, RequiredChecks: entries, Runs: runs}, nil
}

// VerifyRequiredCheckManifest：獨立重驗 §5.1(5) 全部驗證條件——**不依賴
// BuildRequiredCheckManifest 已先執行**（owner 裁定 erratum，plan rev9：
// exported verifier 必須自己履行宣稱的保證，不能借用 Build 的前提）。
// 逐條檢查：
//  1. required key (context, app_id) 必須唯一（Verify 自己拒絕重複，不
//     借用 Build 已去重的前提）。
//  2. 每筆 run 的 required key 必須存在於 required 集合，且同一 key 恰
//     一筆 run（不存在／重複覆蓋 fail loud）。
//  3. 每個 required key 最後都必須被覆蓋（無缺漏 fail loud）。
//  4. 同一 run_id 不得歸屬多個 required key（多重歸屬 fail loud）。
//  5. attribution 重驗：run_name == context，且 required_app_id == nil
//     或 run_app_id == required_app_id。
//  6. 全 completed+success、全 head_sha == promotion_head（既有規則）。
func VerifyRequiredCheckManifest(m RequiredCheckManifest, head forge.OID) error {
	if m.ManifestSchema != 1 {
		return fmt.Errorf("manifest_schema %d 不支援", m.ManifestSchema)
	}
	required := map[string]RequiredCheckEntry{}
	for _, rc := range m.RequiredChecks {
		k := keyOf(rc.Context, rc.AppID)
		if _, dup := required[k]; dup {
			return fmt.Errorf("required check 重複 key：%s", k)
		}
		required[k] = rc
	}
	covered := map[string]bool{}
	usedRun := map[int64]string{}
	for _, r := range m.Runs {
		k := keyOf(r.Context, r.RequiredAppID)
		rc, ok := required[k]
		if !ok {
			return fmt.Errorf("run %d 歸屬 required key %s 不存在於 required 集合（多餘）", r.RunID, k)
		}
		if covered[k] {
			return fmt.Errorf("required key %s 重複覆蓋：多筆 run 歸屬同一 required check", k)
		}
		if prevKey, dup := usedRun[r.RunID]; dup {
			return fmt.Errorf("run %d 多重歸屬：%s 與 %s", r.RunID, prevKey, k)
		}
		if r.RunName != rc.Context {
			return fmt.Errorf("run %d attribution 不符：run_name=%q ≠ context=%q", r.RunID, r.RunName, rc.Context)
		}
		if rc.AppID != nil && r.RunAppID != *rc.AppID {
			return fmt.Errorf("run %d attribution 不符：run_app_id=%d ≠ required_app_id=%d", r.RunID, r.RunAppID, *rc.AppID)
		}
		covered[k] = true
		usedRun[r.RunID] = k
		if r.Status != "completed" || r.Conclusion != "success" {
			return fmt.Errorf("required %q 非 success（status=%s conclusion=%s）", k, r.Status, r.Conclusion)
		}
		if r.HeadSHA != string(head) {
			return fmt.Errorf("required %q head %s ≠ promotion_head %s", k, r.HeadSHA, head)
		}
	}
	var missing []string
	for k := range required {
		if !covered[k] {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		// 比照 gate2.go:183-189 先例：多筆缺漏時收集後排序再組訊息，
		// 避免 map 疊代序讓回報的缺漏 key 不確定；列出全部缺漏 key
		// 以提高診斷價值（owner 裁定方向，P2-1 follow-up）。
		sort.Strings(missing)
		return fmt.Errorf("required missing：無覆蓋 run：%s", strings.Join(missing, ", "))
	}
	return nil
}

type ReviewEntry struct {
	ReviewerLogin   string `json:"reviewer_login"`
	Permission      string `json:"permission"`
	ReviewID        int64  `json:"review_id"`
	State           string `json:"state"`
	ReviewedHeadSHA string `json:"reviewed_head_sha"`
	SubmittedAt     string `json:"submitted_at"`
}

// BuildReviewSection：每具效力 reviewer 至多一筆 current-effective review
// （B5 §5.1(6)）。eligibility＝permission ∈ {write,maintain,admin}；不具
// 效力者完全不入（approval 不放行、CHANGES_REQUESTED 亦不阻擋）。
// current-effective＝state ∈ {APPROVED,CHANGES_REQUESTED,DISMISSED} 中
// submitted_at（解析後 time.Time）最新者（tie 取 review_id 大者）；
// COMMENTED／PENDING 不參與、不入 section。
//
// Fail-closed 規則（rev10——修正 perms map 零值語意造成的 CR 方向
// fail-open，B5 §6）：
//   - state ∈ {APPROVED,CHANGES_REQUESTED,DISMISSED} 的 review，其
//     reviewer 的 permission key 必須存在於 perms；缺漏（查無／未查詢）
//     不得等同 PermissionNone，須 fail loud。
//   - permission 值必須是已知列舉（admin／maintain／write／read／none）；
//     未知值（含空字串）→fail loud。
//   - COMMENTED／PENDING 不參與 current-effective，可不要求 permission。
//   - 未知 review state（非上述五種）不得靜默跳過，須 fail loud。
//
// SubmittedAt 正規化（rev10 新增契約——固定 digest preimage）：寫入
// ReviewEntry 的 SubmittedAt 為解析後時間值的 UTC RFC3339Nano 表示
// （ts.UTC().Format(time.RFC3339Nano)，非 time.RFC3339——避免丟失
// fractional seconds）；current-effective 收斂比較仍用解析後的 time.Time。
func BuildReviewSection(reviews []forge.Review, perms map[string]forge.Permission) ([]ReviewEntry, error) {
	type effRev struct {
		r  forge.Review
		at time.Time
	}
	eff := map[string]effRev{}
	for _, r := range reviews {
		switch r.State {
		case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
			p, ok := perms[r.ReviewerLogin]
			if !ok {
				return nil, fmt.Errorf("review %d（reviewer %s, state %s）缺少 permission 查詢結果，不得等同 none（fail closed，B5 §6）", r.ReviewID, r.ReviewerLogin, r.State)
			}
			switch p {
			case forge.PermissionAdmin, forge.PermissionMaintain, forge.PermissionWrite, forge.PermissionRead, forge.PermissionNone:
			default:
				return nil, fmt.Errorf("review %d（reviewer %s）permission 值未知：%q", r.ReviewID, r.ReviewerLogin, string(p))
			}
			if !p.Eligible() {
				continue
			}
		case "COMMENTED", "PENDING":
			continue
		default:
			return nil, fmt.Errorf("review %d（reviewer %s）未知 state：%q（不得靜默跳過）", r.ReviewID, r.ReviewerLogin, r.State)
		}
		ts, perr := time.Parse(time.RFC3339, r.SubmittedAt)
		if perr != nil {
			return nil, fmt.Errorf("review %d submitted_at 非 RFC3339：%q", r.ReviewID, r.SubmittedAt)
		}
		cur, ok := eff[r.ReviewerLogin]
		if !ok || ts.After(cur.at) || (ts.Equal(cur.at) && r.ReviewID > cur.r.ReviewID) {
			eff[r.ReviewerLogin] = effRev{r: r, at: ts}
		}
	}
	logins := make([]string, 0, len(eff))
	for l := range eff {
		logins = append(logins, l)
	}
	sort.Strings(logins)
	out := make([]ReviewEntry, 0, len(logins))
	for _, l := range logins {
		e := eff[l]
		out = append(out, ReviewEntry{ReviewerLogin: l, Permission: string(perms[l]),
			ReviewID: e.r.ReviewID, State: e.r.State, ReviewedHeadSHA: string(e.r.ReviewedHeadOID),
			SubmittedAt: e.at.UTC().Format(time.RFC3339Nano)})
	}
	return out, nil
}

// VerifyReviewSection：結構性重驗 section 自身的 canonical／決議不變量
// （rev10——沿 Task 4 exported verifier 原則：exported verifier 必須自行
// 履行其宣稱的契約，不能只依賴 Build 已先驗證的前提）：
//  1. reviewer_login 嚴格遞增（連帶保證排序與唯一性）。
//  2. permission 是已知列舉值且必須 eligible（write／maintain／admin）。
//  3. state 僅能是 APPROVED／CHANGES_REQUESTED／DISMISSED。
//  4. submitted_at 合法 RFC3339，且等於重新格式化的 UTC RFC3339Nano
//     canonical value（非 canonical 表示 → fail loud）。
//  5. 至少一筆 current-effective APPROVED @ head、零 CHANGES_REQUESTED；
//     DISMISSED 不計入亦不阻擋。
//
// 範圍聲明：本函式僅驗證 section 自身的 canonical／決議不變量，
// **不證明**其完整來自 Forge——例如 caller 把某具效力 reviewer 的
// CHANGES_REQUESTED 整筆刪除後，剩餘 section 仍可能滿足以上五項全部
// 檢查（遞增、permission 列舉且 eligible、state 白名單、canonical
// timestamp、零 CR），這是 []ReviewEntry 單獨無法證明的資訊缺口。
// 完整性由 C1 於決議時重新 GetReviews、查齊 permissions、
// BuildReviewSection、VerifyReviewSection、組合 manifest 並比對 digest
// 保證（B5 spec §5.3(5)）。
func VerifyReviewSection(entries []ReviewEntry, head forge.OID) error {
	approvedAtHead := false
	prevLogin := ""
	for i, e := range entries {
		if i > 0 && e.ReviewerLogin <= prevLogin {
			return fmt.Errorf("reviewer_login 非嚴格遞增：%q 之後接 %q", prevLogin, e.ReviewerLogin)
		}
		prevLogin = e.ReviewerLogin

		switch forge.Permission(e.Permission) {
		case forge.PermissionWrite, forge.PermissionMaintain, forge.PermissionAdmin:
		case forge.PermissionRead, forge.PermissionNone:
			return fmt.Errorf("reviewer %s permission=%s 不具效力，不應出現於 section", e.ReviewerLogin, e.Permission)
		default:
			return fmt.Errorf("reviewer %s permission 值未知：%q", e.ReviewerLogin, e.Permission)
		}

		switch e.State {
		case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
		default:
			return fmt.Errorf("reviewer %s state 不在白名單：%q", e.ReviewerLogin, e.State)
		}

		ts, perr := time.Parse(time.RFC3339, e.SubmittedAt)
		if perr != nil {
			return fmt.Errorf("reviewer %s submitted_at 非 RFC3339：%q", e.ReviewerLogin, e.SubmittedAt)
		}
		if canonical := ts.UTC().Format(time.RFC3339Nano); e.SubmittedAt != canonical {
			return fmt.Errorf("reviewer %s submitted_at 非 canonical UTC RFC3339Nano：%q（應為 %q）", e.ReviewerLogin, e.SubmittedAt, canonical)
		}

		switch e.State {
		case "CHANGES_REQUESTED":
			return fmt.Errorf("reviewer %s 有 current-effective CHANGES_REQUESTED（零 CR 條件）", e.ReviewerLogin)
		case "APPROVED":
			if e.ReviewedHeadSHA == string(head) {
				approvedAtHead = true
			}
		}
	}
	if !approvedAtHead {
		return fmt.Errorf("無 current-effective APPROVED 於 promotion_head %s", head)
	}
	return nil
}
