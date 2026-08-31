package gatepolicy

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/forge"
)

func i64(v int64) *int64 { return &v }

func TestBuildRequiredCheckManifest(t *testing.T) {
	head := forge.OID("aaaa")
	run := func(name string, app, id int64, started, concl string) forge.CheckRun {
		return forge.CheckRun{Name: name, AppID: app, RunID: id, HeadOID: head,
			Status: "completed", Conclusion: concl, StartedAt: started}
	}
	cases := []struct {
		name    string
		rc      forge.RequiredChecks
		wantErr string // 空字串＝成功
		check   func(t *testing.T, m RequiredCheckManifest)
	}{
		{name: "app_id 為 nil 可由任一 app 的 run 覆蓋（bijection 而非 key 相等）",
			rc: forge.RequiredChecks{
				Required: []forge.RequiredCheckRef{{Context: "ci"}},
				Runs:     []forge.CheckRun{run("ci", 42, 1, "2026-08-28T01:00:00Z", "success")}},
			check: func(t *testing.T, m RequiredCheckManifest) {
				if len(m.Runs) != 1 || m.Runs[0].RunAppID != 42 || m.Runs[0].RequiredAppID != nil {
					t.Fatalf("run 應記錄 required_app_id=nil 與 run_app_id=42：%+v", m.Runs)
				}
			}},
		{name: "同名多次執行取 started_at 最新（tie 取 run_id 大者）",
			rc: forge.RequiredChecks{
				Required: []forge.RequiredCheckRef{{Context: "ci", AppID: i64(42)}},
				Runs: []forge.CheckRun{
					run("ci", 42, 1, "2026-08-28T01:00:00Z", "failure"),
					run("ci", 42, 2, "2026-08-28T02:00:00Z", "success")}},
			check: func(t *testing.T, m RequiredCheckManifest) {
				if m.Runs[0].RunID != 2 {
					t.Fatalf("current-effective 應為 run 2：%+v", m.Runs)
				}
			}},
		{name: "不同時區偏移依實際時間比較（rev2——字典序會選錯）",
			rc: forge.RequiredChecks{
				Required: []forge.RequiredCheckRef{{Context: "ci", AppID: i64(42)}},
				Runs: []forge.CheckRun{
					// 03:00+08:00 ＝ 前一日 19:00Z，字典序卻大於 01:00Z——實際較舊
					run("ci", 42, 1, "2026-08-28T03:00:00+08:00", "failure"),
					run("ci", 42, 2, "2026-08-28T01:00:00Z", "success")}},
			check: func(t *testing.T, m RequiredCheckManifest) {
				if m.Runs[0].RunID != 2 {
					t.Fatalf("應依實際時間取 run 2（01:00Z 晚於 27 日 19:00Z）：%+v", m.Runs)
				}
			}},
		{name: "started_at 非 RFC3339 → fail loud",
			rc: forge.RequiredChecks{
				Required: []forge.RequiredCheckRef{{Context: "ci", AppID: i64(42)}},
				Runs:     []forge.CheckRun{run("ci", 42, 1, "not-a-time", "success")}},
			wantErr: "非 RFC3339"},
		{name: "required_app_id 為 nil 且同名 run 來自多 app → 歸屬歧義 fail loud",
			rc: forge.RequiredChecks{
				Required: []forge.RequiredCheckRef{{Context: "ci"}},
				Runs: []forge.CheckRun{
					run("ci", 42, 1, "2026-08-28T01:00:00Z", "success"),
					run("ci", 43, 2, "2026-08-28T02:00:00Z", "success")}},
			wantErr: "歸屬歧義"},
		{name: "required 缺對應 run → 無缺漏違反",
			rc: forge.RequiredChecks{
				Required: []forge.RequiredCheckRef{{Context: "ci"}, {Context: "lint"}},
				Runs:     []forge.CheckRun{run("ci", 42, 1, "2026-08-28T01:00:00Z", "success")}},
			wantErr: "missing"},
		{name: "forge 回傳重複 required key → fail loud",
			rc: forge.RequiredChecks{
				Required: []forge.RequiredCheckRef{{Context: "ci", AppID: i64(42)}, {Context: "ci", AppID: i64(42)}},
				Runs:     []forge.CheckRun{run("ci", 42, 1, "2026-08-28T01:00:00Z", "success")}},
			wantErr: "重複"},
		{name: "非 required 的 run 不入 manifest（無多餘）",
			rc: forge.RequiredChecks{
				Required: []forge.RequiredCheckRef{{Context: "ci", AppID: i64(42)}},
				Runs: []forge.CheckRun{
					run("ci", 42, 1, "2026-08-28T01:00:00Z", "success"),
					run("extra", 42, 9, "2026-08-28T01:00:00Z", "success")}},
			check: func(t *testing.T, m RequiredCheckManifest) {
				if len(m.Runs) != 1 {
					t.Fatalf("extra run 不得入 manifest：%+v", m.Runs)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, err := BuildRequiredCheckManifest(tc.rc, head)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if m.ManifestSchema != 1 {
				t.Fatalf("manifest_schema 必為 1")
			}
			tc.check(t, m)
		})
	}
}

func TestRequiredCheckManifestDigestOrderIndependent(t *testing.T) {
	head := forge.OID("aaaa")
	rc := forge.RequiredChecks{
		Required: []forge.RequiredCheckRef{{Context: "b", AppID: i64(1)}, {Context: "a", AppID: i64(1)}},
		Runs: []forge.CheckRun{
			{Name: "a", AppID: 1, RunID: 1, HeadOID: head, Status: "completed", Conclusion: "success", StartedAt: "2026-08-28T01:00:00Z"},
			{Name: "b", AppID: 1, RunID: 2, HeadOID: head, Status: "completed", Conclusion: "success", StartedAt: "2026-08-28T01:00:00Z"}}}
	m1, err := BuildRequiredCheckManifest(rc, head)
	if err != nil {
		t.Fatal(err)
	}
	// 反轉 forge 回傳順序
	rc.Required[0], rc.Required[1] = rc.Required[1], rc.Required[0]
	rc.Runs[0], rc.Runs[1] = rc.Runs[1], rc.Runs[0]
	m2, err := BuildRequiredCheckManifest(rc, head)
	if err != nil {
		t.Fatal(err)
	}
	d1, _ := ManifestDigest(m1)
	d2, _ := ManifestDigest(m2)
	if d1 != d2 || !strings.HasPrefix(d1, "sha256:") {
		t.Fatalf("forge 回傳順序不得影響 digest：%s vs %s", d1, d2)
	}
}

func TestVerifyRequiredCheckManifest(t *testing.T) {
	head := forge.OID("aaaa")
	base := RequiredCheckManifest{ManifestSchema: 1,
		RequiredChecks: []RequiredCheckEntry{{Context: "ci", AppID: i64(42)}},
		Runs: []CheckRunEntry{{Context: "ci", RequiredAppID: i64(42), RunName: "ci",
			RunAppID: 42, RunID: 1, HeadSHA: "aaaa", Status: "completed", Conclusion: "success"}}}
	if err := VerifyRequiredCheckManifest(base, head); err != nil {
		t.Fatal(err)
	}
	pending := base
	pending.Runs = []CheckRunEntry{{Context: "ci", RequiredAppID: i64(42), RunName: "ci",
		RunAppID: 42, RunID: 1, HeadSHA: "aaaa", Status: "in_progress"}}
	if err := VerifyRequiredCheckManifest(pending, head); err == nil {
		t.Fatal("pending 應不符（三態皆為不符，B5 §5.3(3)）")
	}
	wrongHead := base
	wrongHead.Runs[0].HeadSHA = "bbbb"
	if err := VerifyRequiredCheckManifest(wrongHead, head); err == nil {
		t.Fatal("head 不符應 fail")
	}
}

// TestVerifyRequiredCheckManifestBijection——owner 裁定 erratum 修正：
// VerifyRequiredCheckManifest 不得依賴 BuildRequiredCheckManifest 已先執行，
// 必須自己完成 §5.1(5) 全部 bijection 檢查（required key 唯一、run key 存在
// 於 required 集合且恰一筆、required key 必須被覆蓋、run_id 不得多重歸屬、
// attribution 重驗）。全部案例以 literal 手刻 manifest 直接呼叫 Verify，
// 不經 Build——證明 Verify 自身的保證，不是借用 Build 的前提。
func TestVerifyRequiredCheckManifestBijection(t *testing.T) {
	head := forge.OID("aaaa")
	ok := func(context string, app *int64, runName string, runApp int64, runID int64, status, concl string) CheckRunEntry {
		return CheckRunEntry{Context: context, RequiredAppID: app, RunName: runName, RunAppID: runApp,
			RunID: runID, HeadSHA: "aaaa", Status: status, Conclusion: concl}
	}
	cases := []struct {
		name    string
		m       RequiredCheckManifest
		wantErr string
	}{
		{name: "等長但 key 不對應（owner 反例）——一項重複、另一項缺漏，必須 FAIL",
			m: RequiredCheckManifest{ManifestSchema: 1,
				RequiredChecks: []RequiredCheckEntry{{Context: "ci", AppID: i64(42)}, {Context: "lint", AppID: i64(42)}},
				Runs: []CheckRunEntry{
					ok("ci", i64(42), "ci", 42, 1, "completed", "success"),
					ok("ci", i64(42), "ci", 42, 2, "completed", "success"),
				}},
			wantErr: "重複覆蓋"},
		{name: "required key 重複（Verify 端自己拒絕，非借用 Build 前提）",
			m: RequiredCheckManifest{ManifestSchema: 1,
				RequiredChecks: []RequiredCheckEntry{{Context: "ci", AppID: i64(42)}, {Context: "ci", AppID: i64(42)}},
				Runs: []CheckRunEntry{
					ok("ci", i64(42), "ci", 42, 1, "completed", "success"),
				}},
			wantErr: "重複"},
		{name: "run 的 required key 不存在於 required 集合（多餘）",
			m: RequiredCheckManifest{ManifestSchema: 1,
				RequiredChecks: []RequiredCheckEntry{{Context: "ci", AppID: i64(42)}},
				Runs: []CheckRunEntry{
					ok("lint", i64(42), "lint", 42, 1, "completed", "success"),
				}},
			wantErr: "不存在於 required 集合"},
		{name: "同一 required key 被兩筆 run 重複覆蓋",
			m: RequiredCheckManifest{ManifestSchema: 1,
				RequiredChecks: []RequiredCheckEntry{{Context: "ci", AppID: i64(42)}},
				Runs: []CheckRunEntry{
					ok("ci", i64(42), "ci", 42, 1, "completed", "success"),
					ok("ci", i64(42), "ci", 42, 2, "completed", "success"),
				}},
			wantErr: "重複覆蓋"},
		{name: "required key 缺漏（無對應 run）",
			m: RequiredCheckManifest{ManifestSchema: 1,
				RequiredChecks: []RequiredCheckEntry{{Context: "ci", AppID: i64(42)}, {Context: "lint", AppID: i64(42)}},
				Runs: []CheckRunEntry{
					ok("ci", i64(42), "ci", 42, 1, "completed", "success"),
				}},
			wantErr: "missing"},
		{name: "多個 required key 同時缺漏 → 錯誤訊息確定性列出全部缺漏 key（排序，非 map 疊代序）——P2-1 follow-up",
			m: RequiredCheckManifest{ManifestSchema: 1,
				RequiredChecks: []RequiredCheckEntry{
					{Context: "zeta"}, {Context: "lint"}, {Context: "alpha", AppID: i64(1)}},
				Runs: nil},
			// 宣告序刻意與排序後序不同（zeta, lint, alpha）——若實作仍是
			// map 遍歷＋回傳單一 key，這個完整訊息斷言必定不吻合（不穩定
			// 或直接缺漏其餘 key）；只有排序後列出全部三個 key 才會通過。
			wantErr: "alpha\x001, lint\x00*, zeta\x00*"},
		{name: "同一 run_id 歸屬兩個不同 required key（多重歸屬）",
			m: RequiredCheckManifest{ManifestSchema: 1,
				RequiredChecks: []RequiredCheckEntry{{Context: "ci", AppID: i64(42)}, {Context: "lint", AppID: i64(42)}},
				Runs: []CheckRunEntry{
					ok("ci", i64(42), "ci", 42, 1, "completed", "success"),
					ok("lint", i64(42), "lint", 42, 1, "completed", "success"),
				}},
			wantErr: "多重歸屬"},
		{name: "attribution 不符：run_name ≠ context",
			m: RequiredCheckManifest{ManifestSchema: 1,
				RequiredChecks: []RequiredCheckEntry{{Context: "ci", AppID: i64(42)}},
				Runs: []CheckRunEntry{
					ok("ci", i64(42), "not-ci", 42, 1, "completed", "success"),
				}},
			wantErr: "attribution 不符"},
		{name: "attribution 不符：run_app_id ≠ required_app_id",
			m: RequiredCheckManifest{ManifestSchema: 1,
				RequiredChecks: []RequiredCheckEntry{{Context: "ci", AppID: i64(42)}},
				Runs: []CheckRunEntry{
					ok("ci", i64(42), "ci", 43, 1, "completed", "success"),
				}},
			wantErr: "attribution 不符"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := VerifyRequiredCheckManifest(tc.m, head)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// TestManifestCanonicalJSONKeyOrder——canonical digest 跨實作契約的鍵序
// golden test（B6 Task 4 follow-up 2，owner 裁定）。三個 struct 的欄位
// 宣告序目前與 spec §5.1(5) 字面序一致（RequiredCheckEntry={context,
// app_id}；CheckRunEntry={context, required_app_id, run_name, run_app_id,
// run_id, head_sha, status, conclusion}；RequiredCheckManifest=
// {manifest_schema, required_checks, runs}），但先前沒有任何測試斷言
// json.Marshal 的實際鍵序——若日後有人調換欄位宣告順序，只有跨實作
// digest 比對失敗時才會暴露。
//
// 刻意不轉 map[string]any 比較：Go 的 map 沒有固定疊代序、也不記錄
// 原始 JSON 鍵序，轉成 map 後兩個鍵序不同但值集合相同的 JSON 會比較
// 相等，等於完全測不到鍵序漂移。canonical digest 的定義基礎正是「struct
// 宣告序＝spec 字面序＝json.Marshal 輸出序」，所以必須直接比對
// json.Marshal 輸出的精確位元組／字串，而非其反解後的資料值。
//
// fixture 為完整三層 struct，且 AppID／RequiredAppID 的 nil 與非 nil
// 兩種形狀各出現一次（ci 帶 app_id=42、lint 的 app_id／required_app_id
// 皆為 nil），確保 golden 字串同時覆蓋兩種指標序列化形狀。
func TestManifestCanonicalJSONKeyOrder(t *testing.T) {
	m := RequiredCheckManifest{
		ManifestSchema: 1,
		RequiredChecks: []RequiredCheckEntry{
			{Context: "ci", AppID: i64(42)},
			{Context: "lint", AppID: nil},
		},
		Runs: []CheckRunEntry{
			{Context: "ci", RequiredAppID: i64(42), RunName: "ci", RunAppID: 42, RunID: 1,
				HeadSHA: "aaaa", Status: "completed", Conclusion: "success"},
			{Context: "lint", RequiredAppID: nil, RunName: "lint", RunAppID: 99, RunID: 2,
				HeadSHA: "aaaa", Status: "completed", Conclusion: "success"},
		},
	}
	got, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"manifest_schema":1,"required_checks":[{"context":"ci","app_id":42},{"context":"lint","app_id":null}],"runs":[{"context":"ci","required_app_id":42,"run_name":"ci","run_app_id":42,"run_id":1,"head_sha":"aaaa","status":"completed","conclusion":"success"},{"context":"lint","required_app_id":null,"run_name":"lint","run_app_id":99,"run_id":2,"head_sha":"aaaa","status":"completed","conclusion":"success"}]}`
	if string(got) != want {
		t.Fatalf("canonical JSON 鍵序契約破裂：\n got=%s\nwant=%s", got, want)
	}
}
