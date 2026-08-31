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

func rev(login, state, head, at string, id int64) forge.Review {
	return forge.Review{ReviewID: id, ReviewerLogin: login, State: state,
		ReviewedHeadOID: forge.OID(head), SubmittedAt: at}
}

func TestBuildReviewSection(t *testing.T) {
	perms := map[string]forge.Permission{"alice": forge.PermissionWrite, "eve": forge.PermissionRead}
	entries, err := BuildReviewSection([]forge.Review{
		rev("alice", "APPROVED", "aaaa", "2026-08-28T01:00:00Z", 1),
		rev("alice", "CHANGES_REQUESTED", "aaaa", "2026-08-28T02:00:00Z", 2), // 後者 supersede
		rev("alice", "COMMENTED", "aaaa", "2026-08-28T03:00:00Z", 3),         // COMMENTED 不改變有效狀態
		rev("eve", "APPROVED", "aaaa", "2026-08-28T01:00:00Z", 4),            // 明確 read 權限：不入 section
	}, perms)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ReviewerLogin != "alice" || entries[0].State != "CHANGES_REQUESTED" {
		t.Fatalf("僅 alice 的 current-effective（CR）應入 section：%+v", entries)
	}
	if entries[0].SubmittedAt != "2026-08-28T02:00:00Z" {
		t.Fatalf("SubmittedAt 應正規化為 UTC RFC3339Nano：%q", entries[0].SubmittedAt)
	}
}

// TestBuildReviewSectionPermissionNoneExcluded：明確 none（key 存在、值為
// none）——安全排除，不入 section，不視為錯誤（區分於 key 缺漏，rev10）。
func TestBuildReviewSectionPermissionNoneExcluded(t *testing.T) {
	perms := map[string]forge.Permission{"bob": forge.PermissionNone}
	entries, err := BuildReviewSection([]forge.Review{
		rev("bob", "CHANGES_REQUESTED", "aaaa", "2026-08-28T01:00:00Z", 1),
	}, perms)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("明確 none 應安全排除：%+v", entries)
	}
}

// TestBuildReviewSectionPermissionKeyMissingFailsLoud：perms 完全沒有該
// reviewer 的 key（未查詢／查詢失敗）——不得等同 none，須 fail loud
// （B5 §6 fail-closed；rev10 修正原「無紀錄→None：不入」的 fail-open 缺口）。
func TestBuildReviewSectionPermissionKeyMissingFailsLoud(t *testing.T) {
	perms := map[string]forge.Permission{}
	_, err := BuildReviewSection([]forge.Review{
		rev("bob", "CHANGES_REQUESTED", "aaaa", "2026-08-28T01:00:00Z", 1),
	}, perms)
	if err == nil || !strings.Contains(err.Error(), "缺少") {
		t.Fatalf("permission key 缺漏應 fail loud，got %v", err)
	}
}

// TestBuildReviewSectionUnknownPermissionValueFailsLoud：key 存在但值非
// 已知列舉（含空字串）——fail loud，不得靜默視為不具效力。
func TestBuildReviewSectionUnknownPermissionValueFailsLoud(t *testing.T) {
	cases := map[string]forge.Permission{"typo": forge.Permission("writeXX"), "empty": forge.Permission("")}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			perms := map[string]forge.Permission{"bob": p}
			_, err := BuildReviewSection([]forge.Review{
				rev("bob", "APPROVED", "aaaa", "2026-08-28T01:00:00Z", 1),
			}, perms)
			if err == nil || !strings.Contains(err.Error(), "未知") {
				t.Fatalf("未知 permission 值應 fail loud，got %v", err)
			}
		})
	}
}

// TestBuildReviewSectionUnknownStateFailsLoud：非白名單 review state 不得
// 靜默跳過（rev10 新規則）。
func TestBuildReviewSectionUnknownStateFailsLoud(t *testing.T) {
	perms := map[string]forge.Permission{"alice": forge.PermissionWrite}
	_, err := BuildReviewSection([]forge.Review{
		rev("alice", "REQUEST_CHANGES_TYPO", "aaaa", "2026-08-28T01:00:00Z", 1),
	}, perms)
	if err == nil || !strings.Contains(err.Error(), "未知") {
		t.Fatalf("未知 state 應 fail loud，got %v", err)
	}
}

// TestBuildReviewSectionPendingNotRequirePermission：COMMENTED／PENDING
// 不參與 current-effective，可不要求 permission（即使 key 缺漏也不報錯，
// 因為根本不查）。
func TestBuildReviewSectionPendingNotRequirePermission(t *testing.T) {
	perms := map[string]forge.Permission{}
	entries, err := BuildReviewSection([]forge.Review{
		rev("alice", "PENDING", "aaaa", "2026-08-28T01:00:00Z", 1),
		rev("alice", "COMMENTED", "aaaa", "2026-08-28T02:00:00Z", 2),
	}, perms)
	if err != nil {
		t.Fatalf("PENDING／COMMENTED 不應要求 permission：%v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("PENDING／COMMENTED 不入 section：%+v", entries)
	}
}

func TestBuildReviewSectionTimezoneAndParseError(t *testing.T) {
	perms := map[string]forge.Permission{"alice": forge.PermissionWrite}
	// 03:00+08:00 實際早於 01:00Z——字典序會誤判為較新（rev2）
	entries, err := BuildReviewSection([]forge.Review{
		rev("alice", "CHANGES_REQUESTED", "aaaa", "2026-08-28T03:00:00+08:00", 1),
		rev("alice", "APPROVED", "aaaa", "2026-08-28T01:00:00Z", 2),
	}, perms)
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].State != "APPROVED" {
		t.Fatalf("current-effective 應依實際時間為 APPROVED：%+v", entries)
	}
	if _, err := BuildReviewSection([]forge.Review{
		rev("alice", "APPROVED", "aaaa", "bad-time", 1)}, perms); err == nil {
		t.Fatal("submitted_at 非 RFC3339 應 fail loud")
	}
}

// TestBuildReviewSectionTieBreakReviewID：同一 reviewer 兩筆 review 的
// submitted_at 相同時，取 review_id 較大者為 current-effective。
func TestBuildReviewSectionTieBreakReviewID(t *testing.T) {
	perms := map[string]forge.Permission{"alice": forge.PermissionWrite}
	entries, err := BuildReviewSection([]forge.Review{
		rev("alice", "CHANGES_REQUESTED", "aaaa", "2026-08-28T01:00:00Z", 5),
		rev("alice", "APPROVED", "aaaa", "2026-08-28T01:00:00Z", 9),
	}, perms)
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].State != "APPROVED" || entries[0].ReviewID != 9 {
		t.Fatalf("tie-break 應取 review_id 較大者：%+v", entries)
	}
}

// TestBuildReviewSectionSubmittedAtNormalization：rev10 新增契約——寫入
// ReviewEntry 的 SubmittedAt 為 UTC RFC3339Nano canonical value，且
// 2026-08-28T01:00:00Z 與 2026-08-28T01:00:00+00:00 兩種輸入表示同一時刻，
// 必須產出完全相同的 section bytes（固定 digest preimage，避免決議時
// 重讀重算產生假 mismatch）。
func TestBuildReviewSectionSubmittedAtNormalization(t *testing.T) {
	perms := map[string]forge.Permission{"alice": forge.PermissionWrite}
	a, err := BuildReviewSection([]forge.Review{
		rev("alice", "APPROVED", "aaaa", "2026-08-28T01:00:00Z", 1)}, perms)
	if err != nil {
		t.Fatal(err)
	}
	b, err := BuildReviewSection([]forge.Review{
		rev("alice", "APPROVED", "aaaa", "2026-08-28T01:00:00+00:00", 1)}, perms)
	if err != nil {
		t.Fatal(err)
	}
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	if string(ab) != string(bb) {
		t.Fatalf("Z 與 +00:00 兩種表示應產出相同 section bytes：%s vs %s", ab, bb)
	}
	// 帶 fractional seconds 的輸入不得被截斷精度（用 RFC3339Nano，非 RFC3339）。
	frac, err := BuildReviewSection([]forge.Review{
		rev("alice", "APPROVED", "aaaa", "2026-08-28T01:00:00.123456789Z", 1)}, perms)
	if err != nil {
		t.Fatal(err)
	}
	if frac[0].SubmittedAt != "2026-08-28T01:00:00.123456789Z" {
		t.Fatalf("RFC3339Nano 正規化不得丟失 fractional seconds：%q", frac[0].SubmittedAt)
	}
}

// TestBuildReviewSectionOrderIndependent：reviews 輸入順序反轉，section
// bytes 完全相同（B5 共同規則——forge 回傳順序不得影響 digest）。
func TestBuildReviewSectionOrderIndependent(t *testing.T) {
	perms := map[string]forge.Permission{"alice": forge.PermissionWrite, "bob": forge.PermissionMaintain}
	reviews := []forge.Review{
		rev("alice", "APPROVED", "aaaa", "2026-08-28T01:00:00Z", 1),
		rev("bob", "APPROVED", "aaaa", "2026-08-28T01:00:00Z", 2),
	}
	m1, err := BuildReviewSection(reviews, perms)
	if err != nil {
		t.Fatal(err)
	}
	reversed := []forge.Review{reviews[1], reviews[0]}
	m2, err := BuildReviewSection(reversed, perms)
	if err != nil {
		t.Fatal(err)
	}
	b1, _ := json.Marshal(m1)
	b2, _ := json.Marshal(m2)
	if string(b1) != string(b2) {
		t.Fatalf("輸入順序不得影響 section bytes：%s vs %s", b1, b2)
	}
}

// TestReviewEntryCanonicalJSONKeyOrder——canonical digest 跨實作契約的
// 鍵序 golden test（比照 Task 4 TestManifestCanonicalJSONKeyOrder 形狀）。
func TestReviewEntryCanonicalJSONKeyOrder(t *testing.T) {
	entries := []ReviewEntry{
		{ReviewerLogin: "alice", Permission: "write", ReviewID: 1, State: "APPROVED",
			ReviewedHeadSHA: "aaaa", SubmittedAt: "2026-08-28T01:00:00Z"},
	}
	got, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"reviewer_login":"alice","permission":"write","review_id":1,"state":"APPROVED","reviewed_head_sha":"aaaa","submitted_at":"2026-08-28T01:00:00Z"}]`
	if string(got) != want {
		t.Fatalf("canonical JSON 鍵序契約破裂：\n got=%s\nwant=%s", got, want)
	}
}

func TestVerifyReviewSection(t *testing.T) {
	head := forge.OID("aaaa")
	ok := []ReviewEntry{{ReviewerLogin: "alice", Permission: "write", ReviewID: 1,
		State: "APPROVED", ReviewedHeadSHA: "aaaa", SubmittedAt: "2026-08-28T01:00:00Z"}}
	if err := VerifyReviewSection(ok, head); err != nil {
		t.Fatal(err)
	}
	if err := VerifyReviewSection(nil, head); err == nil {
		t.Fatal("零 review 應不符")
	}
	staleHead := []ReviewEntry{{ReviewerLogin: "alice", Permission: "write", ReviewID: 1,
		State: "APPROVED", ReviewedHeadSHA: "bbbb", SubmittedAt: "2026-08-28T01:00:00Z"}}
	if err := VerifyReviewSection(staleHead, head); err == nil {
		t.Fatal("過期 head 的 approval 不算")
	}
	withCR := []ReviewEntry{ok[0], {ReviewerLogin: "carol",
		Permission: "write", ReviewID: 2, State: "CHANGES_REQUESTED",
		ReviewedHeadSHA: "aaaa", SubmittedAt: "2026-08-28T01:00:00Z"}}
	if err := VerifyReviewSection(withCR, head); err == nil {
		t.Fatal("存在具效力 CHANGES_REQUESTED 應不符（owner 裁決：零 CR）")
	}
	dismissed := []ReviewEntry{ok[0], {ReviewerLogin: "dave", Permission: "write", ReviewID: 2,
		State: "DISMISSED", ReviewedHeadSHA: "aaaa", SubmittedAt: "2026-08-28T02:00:00Z"}}
	if err := VerifyReviewSection(dismissed, head); err != nil {
		t.Fatalf("DISMISSED 不計入亦不阻擋：%v", err)
	}
}

// TestVerifyReviewSectionDismissedOnlyNoApproval：具效力 reviewer 的
// current-effective 是 DISMISSED、且無其他 APPROVED——不得通過（DISMISSED
// 不計入亦不阻擋 ≠ 視為 approval）。
func TestVerifyReviewSectionDismissedOnlyNoApproval(t *testing.T) {
	entries := []ReviewEntry{{ReviewerLogin: "alice", Permission: "write", ReviewID: 1,
		State: "DISMISSED", ReviewedHeadSHA: "aaaa", SubmittedAt: "2026-08-28T01:00:00Z"}}
	if err := VerifyReviewSection(entries, forge.OID("aaaa")); err == nil {
		t.Fatal("僅 DISMISSED、無 APPROVED 應不符")
	}
}

// TestVerifyReviewSectionStructuralInvariants：Verify 自身履行的四項結構
// 性檢查（rev10——沿 Task 4 exported verifier 原則），各自獨立負向案例；
// 全部以 literal 手刻 entries 直接呼叫 Verify，不經 Build。
func TestVerifyReviewSectionStructuralInvariants(t *testing.T) {
	head := forge.OID("aaaa")
	base := func() ReviewEntry {
		return ReviewEntry{ReviewerLogin: "alice", Permission: "write", ReviewID: 1,
			State: "APPROVED", ReviewedHeadSHA: "aaaa", SubmittedAt: "2026-08-28T01:00:00Z"}
	}
	cases := []struct {
		name    string
		entries []ReviewEntry
		wantErr string
	}{
		{name: "reviewer_login 非嚴格遞增（重複）",
			entries: []ReviewEntry{base(), base()},
			wantErr: "非嚴格遞增"},
		{name: "reviewer_login 非嚴格遞增（逆序）",
			entries: []ReviewEntry{
				{ReviewerLogin: "bob", Permission: "write", ReviewID: 1, State: "APPROVED",
					ReviewedHeadSHA: "aaaa", SubmittedAt: "2026-08-28T01:00:00Z"},
				{ReviewerLogin: "alice", Permission: "write", ReviewID: 2, State: "APPROVED",
					ReviewedHeadSHA: "aaaa", SubmittedAt: "2026-08-28T01:00:00Z"},
			},
			wantErr: "非嚴格遞增"},
		{name: "permission 未知列舉值",
			entries: func() []ReviewEntry { e := base(); e.Permission = "superuser"; return []ReviewEntry{e} }(),
			wantErr: "未知"},
		{name: "permission 為明確不具效力值（read）不應出現於 section",
			entries: func() []ReviewEntry { e := base(); e.Permission = "read"; return []ReviewEntry{e} }(),
			wantErr: "不具效力"},
		{name: "state 不在白名單",
			entries: func() []ReviewEntry { e := base(); e.State = "COMMENTED"; return []ReviewEntry{e} }(),
			wantErr: "白名單"},
		{name: "submitted_at 非 RFC3339",
			entries: func() []ReviewEntry { e := base(); e.SubmittedAt = "not-a-time"; return []ReviewEntry{e} }(),
			wantErr: "非 RFC3339"},
		{name: "submitted_at 非 canonical（+00:00 而非 Z）",
			entries: func() []ReviewEntry {
				e := base()
				e.SubmittedAt = "2026-08-28T01:00:00+00:00"
				return []ReviewEntry{e}
			}(),
			wantErr: "非 canonical"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := VerifyReviewSection(tc.entries, head)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}
