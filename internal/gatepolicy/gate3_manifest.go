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
