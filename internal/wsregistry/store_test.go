package wsregistry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreOnlyPersistsDurableWhitelist(t *testing.T) {
	p := filepath.Join(t.TempDir(), "workspace-sessions.json")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Entry{WSID: "w1", Provider: "claude", CreatedAt: "2026-08-14T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	for _, forbidden := range []string{"starting", "active", "busy", "approval_pending", "ending"} {
		if strings.Contains(string(b), forbidden) {
			t.Fatalf("runtime state 不得持久化，出現 %q: %s", forbidden, b)
		}
	}
	if !strings.Contains(string(b), `"schema_version": 2`) {
		t.Fatalf("需帶 schema_version=2: %s", b)
	}
}

func TestRemoveKeepsTombstoneButDeleteUncommittedDoesNot(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "ws.json"))
	_ = s.Put(Entry{WSID: "w1", Provider: "codex", CreatedAt: "t"})
	_ = s.Put(Entry{WSID: "w2", Provider: "codex", CreatedAt: "t"})
	if err := s.Remove("w1", "user_removed"); err != nil {
		t.Fatal(err)
	}
	if e, ok := s.Get("w1"); !ok || e.RemovedAt == "" || e.RemoveReason != "user_removed" {
		t.Fatalf("使用者移除必須留 tombstone：%+v ok=%v", e, ok)
	}
	if err := s.DeleteUncommitted("w2"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("w2"); ok {
		t.Fatal("DeleteUncommitted 必須整筆刪除（建立失敗不得永久留痕）")
	}
	if len(s.Live()) != 0 {
		t.Fatalf("tombstone 與已刪除都不得出現在 Live()：%+v", s.Live())
	}
}

func TestPersistFailureRollsBackMemory(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(filepath.Join(dir, "ws.json"))
	_ = s.Put(Entry{WSID: "w1", Provider: "claude", CreatedAt: "t"})
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skip("平台不支援權限測試")
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := s.Put(Entry{WSID: "w2", Provider: "claude", CreatedAt: "t"}); err == nil {
		t.Fatal("persist 失敗必須回錯")
	}
	if _, ok := s.Get("w2"); ok {
		t.Fatal("persist 失敗後記憶體必須回滾")
	}
}

// TestRoundTripAcrossOpen（review I-2）：Open 讀取路徑本身要被測試覆蓋，
// 不能只驗證記憶體狀態或原始 bytes——tombstone 與 Live() 排除都要在
// 「重新 Open」之後仍然成立。
func TestRoundTripAcrossOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ws.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Entry{WSID: "w1", Provider: "claude", CreatedAt: "t1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Entry{WSID: "w2", Provider: "claude", CreatedAt: "t2"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("w1", "user_removed"); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := reopened.Get("w1"); !ok || e.RemovedAt == "" {
		t.Fatalf("重新 Open 後 tombstone 必須還在：%+v ok=%v", e, ok)
	}
	live := reopened.Live()
	if len(live) != 1 || live[0].WSID != "w2" {
		t.Fatalf("重新 Open 後 Live() 應只剩 w2：%+v", live)
	}
	if reopened.Migrated() {
		t.Fatal("未呼叫過 MarkMigrated，重新 Open 後 Migrated() 應為 false")
	}
}

// TestMarkMigratedAtomicityAcrossOpen（review 補測項 2a）：entries 與
// migrated marker 必須在同一次 persist 內一起出現，重新 Open 後兩者要
// 同時可見。
func TestMarkMigratedAtomicityAcrossOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ws.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	migrated := []Entry{
		{WSID: "w1", Provider: "claude", CreatedAt: "t1"},
		{WSID: "w2", Provider: "codex", CreatedAt: "t2"},
	}
	if err := s.MarkMigrated(migrated); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.Migrated() {
		t.Fatal("重新 Open 後 Migrated() 必須為 true")
	}
	if len(reopened.Live()) != 2 {
		t.Fatalf("重新 Open 後 entries 必須都在：%+v", reopened.Live())
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `"schema_version": 2`) {
		t.Fatalf("需同時帶 schema_version=2: %s", b)
	}
}

// TestMarkMigratedPersistFailureRollsBack（review 補測項 2b）：這條在
// Task 5 從 Migrate() 那層永遠測不到，因為那層看不到「entries 已寫、
// marker 未寫」的中間態——必須在 store 層直接驗證。
func TestMarkMigratedPersistFailureRollsBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ws.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Entry{WSID: "w1", Provider: "claude", CreatedAt: "t1"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skip("平台不支援權限測試")
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := s.MarkMigrated([]Entry{{WSID: "w2", Provider: "codex", CreatedAt: "t2"}}); err == nil {
		t.Fatal("persist 失敗必須回錯")
	}
	if s.Migrated() {
		t.Fatal("persist 失敗後 Migrated() 必須維持 false")
	}
	if e, ok := s.Get("w1"); !ok || e.Provider != "claude" {
		t.Fatalf("persist 失敗後舊 entries 必須完整保留：%+v ok=%v", e, ok)
	}
	if _, ok := s.Get("w2"); ok {
		t.Fatal("persist 失敗後新 entries 不得出現")
	}
}

// TestMarkMigratedRejectsEmptyOrDuplicateWSID（review I-5）：整批取代語意
// 下，空 WSID 或重複 WSID 代表呼叫端已經有 bug，必須 fail loud；空 slice
// 本身合法（legacy 遷移來源本來就沒有 entries）。
func TestMarkMigratedRejectsEmptyOrDuplicateWSID(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ws.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkMigrated([]Entry{{WSID: "", Provider: "claude", CreatedAt: "t"}}); err == nil {
		t.Fatal("MarkMigrated 必須拒絕空 WSID")
	}
	if err := s.MarkMigrated([]Entry{
		{WSID: "w1", Provider: "claude", CreatedAt: "t1"},
		{WSID: "w1", Provider: "codex", CreatedAt: "t2"},
	}); err == nil {
		t.Fatal("MarkMigrated 必須拒絕重複 WSID")
	}
	if s.Migrated() {
		t.Fatal("guard 拒絕的呼叫不得標記為已遷移")
	}
	if err := s.MarkMigrated(nil); err != nil {
		t.Fatalf("空 slice 必須被允許：%v", err)
	}
	if !s.Migrated() {
		t.Fatal("空 slice 的合法呼叫必須標記為已遷移")
	}
}

// TestLayoutRoundTripDoesNotAliasInternalState（review I-1）：Layout()
// 回傳值必須是深拷貝，呼叫端在鎖外原地修改不得污染 store 內部狀態。
func TestLayoutRoundTripDoesNotAliasInternalState(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ws.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetLayout(Layout{Pins: []string{"w1"}, Focused: "w1"}); err != nil {
		t.Fatal(err)
	}
	l := s.Layout()
	l.Pins[0] = "polluted" // 直接索引寫入，不觸發 append 的重新配置——若
	// Layout() 回傳的 slice 與 store 內部共用 backing array，這裡就會真的
	// 污染到 store（用 append 重寫可能因容量不足而配置新陣列，測不出來）

	fresh := s.Layout()
	if len(fresh.Pins) != 1 || fresh.Pins[0] != "w1" {
		t.Fatalf("呼叫端修改 Layout() 回傳值不得污染 store 內部狀態：%+v", fresh)
	}
}

func TestSetLayoutPersistFailureRollsBack(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "ws.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetLayout(Layout{Pins: []string{"w1"}, Focused: "w1"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skip("平台不支援權限測試")
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := s.SetLayout(Layout{Pins: []string{"w2"}, Focused: "w2"}); err == nil {
		t.Fatal("persist 失敗必須回錯")
	}
	if l := s.Layout(); len(l.Pins) != 1 || l.Pins[0] != "w1" {
		t.Fatalf("persist 失敗後 Layout 必須回滾：%+v", l)
	}
}

// TestRemoveErrorsOnMissingWSIDButDeleteUncommittedIsIdempotent（review
// 補測項 4）：Remove 對不存在的 wsid 代表 UI／state 已不同步，必須回錯；
// DeleteUncommitted 對不存在的 wsid 是常態性的 abort 路徑（Put persist
// 失敗時已自行回滾記憶體），必須冪等。
func TestRemoveErrorsOnMissingWSIDButDeleteUncommittedIsIdempotent(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ws.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("nope", "x"); err == nil {
		t.Fatal("Remove 對不存在的 wsid 必須回錯")
	}
	if err := s.DeleteUncommitted("nope"); err != nil {
		t.Fatalf("DeleteUncommitted 對不存在的 wsid 必須是 no-op：%v", err)
	}
	if err := s.DeleteUncommitted("nope"); err != nil {
		t.Fatalf("DeleteUncommitted 必須冪等，連呼兩次都必須回 nil：%v", err)
	}
}

// TestPutAndDeleteUncommittedRefuseTombstonedEntry（review I-4）：Put 不得
// 靜默復活 tombstone；DeleteUncommitted 不得抹掉已 tombstone 的 audit 痕跡。
func TestPutAndDeleteUncommittedRefuseTombstonedEntry(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ws.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Entry{WSID: "w1", Provider: "claude", CreatedAt: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("w1", "user_removed"); err != nil {
		t.Fatal(err)
	}

	if err := s.Put(Entry{WSID: "w1", Provider: "claude", CreatedAt: "t2"}); err == nil {
		t.Fatal("Put 不得靜默復活 tombstone")
	}
	if e, ok := s.Get("w1"); !ok || e.RemovedAt == "" {
		t.Fatalf("被拒絕的 Put 後 tombstone 必須完整保留：%+v ok=%v", e, ok)
	}

	if err := s.DeleteUncommitted("w1"); err == nil {
		t.Fatal("DeleteUncommitted 不得刪除已 tombstone 的 entry")
	}
	if e, ok := s.Get("w1"); !ok || e.RemovedAt == "" {
		t.Fatalf("被拒絕的 DeleteUncommitted 後 tombstone 必須完整保留：%+v ok=%v", e, ok)
	}
}

// TestOpenRejectsUnsupportedSchemaVersionButAcceptsMissing（review round 3，
// I-3 NOT ADDRESSED 修正）：非目前 schemaVersion 的檔案必須拒絕載入
// （防的是靜默資料遺失——不擋的話下次任何寫入會把 version 蓋回目前值並
// 落盤）；schema_version 缺欄位（值為 0）視為舊檔，必須維持既有接受行為，
// 否則之後有人「順手」把 != 0 的判斷拿掉不會被任何測試發現。
func TestOpenRejectsUnsupportedSchemaVersionButAcceptsMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ws.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":3,"entries":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("非目前 schema_version 必須拒絕載入")
	}

	path2 := filepath.Join(t.TempDir(), "ws2.json")
	if err := os.WriteFile(path2, []byte(`{"entries":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path2)
	if err != nil {
		t.Fatalf("缺 schema_version（視為 0／舊檔）必須接受: %v", err)
	}
	if len(s.Live()) != 0 {
		t.Fatalf("預期空 entries: %+v", s.Live())
	}
}

// TestSetLayoutDeepCopiesCallerSlice（review round 3 補測）：呼叫端自己持有
// 傳入 SetLayout 的 slice、事後修改，不得污染 store 內部狀態。與
// TestLayoutRoundTripDoesNotAliasInternalState 互補——那條只證明 Layout()
// 出口有拷貝，這條證明 SetLayout 入口也有拷貝（若把入口拷貝拿掉，出口
// 拷貝仍會讓那條測試 PASS，測不出入口的問題）。
func TestSetLayoutDeepCopiesCallerSlice(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ws.json"))
	if err != nil {
		t.Fatal(err)
	}
	pins := []string{"w1", "w2"}
	if err := s.SetLayout(Layout{Pins: pins, Focused: "w1"}); err != nil {
		t.Fatal(err)
	}
	pins[0] = "polluted" // 呼叫端在鎖外原地修改自己手上的 slice

	if got := s.Layout().Pins[0]; got != "w1" {
		t.Fatalf("SetLayout 必須深拷貝呼叫端傳入的 slice：got %q want w1", got)
	}
}

// TestLiveIsSortedByCreatedAtThenWSID（裁決）：map 迭代順序不穩定，Live()
// 必須自行排序（CreatedAt 遞增，WSID tie-break），避免 session 清單 UI
// 跳動、避免呼叫端測試不穩定。
func TestLiveIsSortedByCreatedAtThenWSID(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "ws.json"))
	if err != nil {
		t.Fatal(err)
	}
	entries := []Entry{
		{WSID: "w3", Provider: "claude", CreatedAt: "2026-08-14T02:00:00Z"},
		{WSID: "w1", Provider: "claude", CreatedAt: "2026-08-14T00:00:00Z"},
		{WSID: "wB", Provider: "claude", CreatedAt: "2026-08-14T01:00:00Z"},
		{WSID: "wA", Provider: "claude", CreatedAt: "2026-08-14T01:00:00Z"},
	}
	for _, e := range entries {
		if err := s.Put(e); err != nil {
			t.Fatal(err)
		}
	}
	live := s.Live()
	got := make([]string, len(live))
	for i, e := range live {
		got[i] = e.WSID
	}
	want := []string{"w1", "wA", "wB", "w3"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Live() 順序錯誤：got %v want %v", got, want)
		}
	}
}

func TestLegacyTranscriptSchemaRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "workspace-sessions.json")
	old := `{"schema_version":2,"entries":{"w1":{"wsid":"w1","provider":"claude"}},"migrated":true}`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.LegacyTranscriptBackfilled() {
		t.Fatal("舊檔缺 marker 應預設 false")
	}
	e, _ := s.Get("w1")
	if e.LegacyTranscript {
		t.Fatal("舊 entry 缺欄位應預設 false")
	}
}
