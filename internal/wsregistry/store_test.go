package wsregistry

import (
	"errors"
	"fmt"
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

func TestResetViewClearsLegacyTranscript(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "ws.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(s, map[string]LegacyEntry{
		"claude": {ViewStartEventID: "v1", HasLegacyTranscript: true},
	}, func() string { return "w1" }); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetView("w1", "hiwater"); err != nil {
		t.Fatal(err)
	}
	e, _ := s.Get("w1")
	if e.LegacyTranscript {
		t.Fatal("前移 boundary 後 LegacyTranscript 必須清除（新 view 世代不含 legacy）")
	}
	if e.ViewStartEventID != "hiwater" {
		t.Fatalf("boundary 應前移：%q", e.ViewStartEventID)
	}
}

func TestBackfillLegacyTranscriptAtomic(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "ws.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(s, map[string]LegacyEntry{
		"claude": {ResumeSessionID: "x"},
	}, func() string { return "w1" }); err != nil {
		t.Fatal(err)
	}
	if err := s.BackfillLegacyTranscript([]string{"w1"}); err != nil {
		t.Fatal(err)
	}
	if !s.LegacyTranscriptBackfilled() {
		t.Fatal("marker 應落盤")
	}
	e, _ := s.Get("w1")
	if !e.LegacyTranscript {
		t.Fatal("候選 entry 應被標記")
	}
	s2, _ := Open(filepath.Join(dir, "ws.json"))
	if !s2.LegacyTranscriptBackfilled() {
		t.Fatal("marker 未持久化")
	}
	e2, _ := s2.Get("w1")
	if !e2.LegacyTranscript {
		t.Fatal("entry 標記未持久化")
	}
}

// spec §4 凍結分支：零候選（掃描成功、確定無待補）仍落 marker——「已檢查過」
// 的語意，下次啟動不再重跑。
func TestBackfillLegacyTranscriptZeroCandidateStillSetsMarker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ws.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BackfillLegacyTranscript(nil); err != nil {
		t.Fatal(err)
	}
	if !s.LegacyTranscriptBackfilled() {
		t.Fatal("零候選仍應落 marker（代表已檢查過）")
	}
	s2, _ := Open(path)
	if !s2.LegacyTranscriptBackfilled() {
		t.Fatal("marker 未持久化")
	}
}

// spec §4 凍結分支：tombstone 不算候選——wsids 內已 Remove 的 entry 跳過、
// 不標記，marker 照常落盤。
func TestBackfillLegacyTranscriptSkipsTombstone(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "ws.json"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	if _, err := Migrate(s, map[string]LegacyEntry{
		"claude": {ResumeSessionID: "x"},
		"codex":  {ResumeSessionID: "y"},
	}, func() string { n++; return fmt.Sprintf("w%d", n) }); err != nil {
		t.Fatal(err)
	}
	var removed, kept string
	for _, e := range s.Live() {
		if e.Provider == "codex" {
			removed = e.WSID
		} else {
			kept = e.WSID
		}
	}
	if err := s.Remove(removed, "user_removed"); err != nil {
		t.Fatal(err)
	}
	if err := s.BackfillLegacyTranscript([]string{kept, removed}); err != nil {
		t.Fatal(err)
	}
	e, _ := s.Get(kept)
	if !e.LegacyTranscript {
		t.Fatal("live 候選應被標記")
	}
	de, _ := s.Get(removed)
	if de.LegacyTranscript {
		t.Fatal("tombstone entry 不得被標記")
	}
	if !s.LegacyTranscriptBackfilled() {
		t.Fatal("marker 應落盤")
	}
}

// spec §4 凍結分支（不可逆錯誤防護的核心）：persist 失敗時 entry 標記與 marker
// **同時**回滾——marker 單獨留下會讓下次啟動不再重試、標記永久缺失。
// 注入手法沿用 TestPersistFailureRollsBackMemory（chmod 父目錄唯讀）。
func TestBackfillLegacyTranscriptPersistFailureRollsBackBoth(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "ws.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(s, map[string]LegacyEntry{
		"claude": {ResumeSessionID: "x"},
	}, func() string { return "w1" }); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skip("平台不支援權限測試")
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := s.BackfillLegacyTranscript([]string{"w1"}); err == nil {
		t.Fatal("persist 失敗必須回錯")
	}
	if s.LegacyTranscriptBackfilled() {
		t.Fatal("persist 失敗後 marker 必須回滾（否則永不重試）")
	}
	if e, _ := s.Get("w1"); e.LegacyTranscript {
		t.Fatal("persist 失敗後 entry 標記必須回滾")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.BackfillLegacyTranscript([]string{"w1"}); err != nil {
		t.Fatalf("修復後重試必須成功：%v", err)
	}
	if !s.LegacyTranscriptBackfilled() {
		t.Fatal("重試後 marker 應落盤")
	}
}

func TestClearLegacyTranscript(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ws.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(s, map[string]LegacyEntry{
		"claude": {ViewStartEventID: "v1", HasLegacyTranscript: true},
	}, func() string { return "w1" }); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearLegacyTranscript("w1"); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.Get("w1"); e.LegacyTranscript {
		t.Fatal("清除後 flag 應為 false")
	}
	s2, _ := Open(path)
	if e, _ := s2.Get("w1"); e.LegacyTranscript {
		t.Fatal("清除未持久化")
	}
	if e, _ := s2.Get("w1"); e.ViewStartEventID != "v1" {
		t.Fatalf("清旗標不得動 boundary：%q", e.ViewStartEventID)
	}
	// 哨兵：不存在／tombstone 是良性跳過訊號，不是一般錯誤。
	if err := s.ClearLegacyTranscript("nope"); !errors.Is(err, ErrEntryNotFound) {
		t.Fatalf("不存在應回 ErrEntryNotFound：%v", err)
	}
	if err := s.Remove("w1", "user_removed"); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearLegacyTranscript("w1"); !errors.Is(err, ErrTombstoned) {
		t.Fatalf("tombstone 應回 ErrTombstoned：%v", err)
	}
}

// ---- B3：write-once 兩形狀（同值冪等、同 TaskRun 不同 digest 拒絕、不同
// TaskRun 重綁拒絕）----
func TestSetTaskRunBindingWriteOnce(t *testing.T) {
	s, _ := openStore(t)
	if err := s.Put(Entry{WSID: "w1", Provider: "claude", CreatedAt: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTaskRunBinding("w1", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "sha256:aa"); err != nil {
		t.Fatal(err)
	}
	e, _ := s.Get("w1")
	if e.TaskRunID != "01ARZ3NDEKTSV4RRFFQ69G5FAV" || e.SnapshotDigest != "sha256:aa" {
		t.Fatalf("綁定欄位未落：%+v", e)
	}
	// 冪等：同值再寫成功（resume 依 journal 回填）
	if err := s.SetTaskRunBinding("w1", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "sha256:aa"); err != nil {
		t.Fatalf("同值應冪等：%v", err)
	}

	t.Run("same_taskrun_different_digest_rejected", func(t *testing.T) {
		if err := s.SetTaskRunBinding("w1", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "sha256:bb"); err == nil {
			t.Fatal("同 TaskRun、不同 digest 應拒絕")
		}
		if e, _ := s.Get("w1"); e.SnapshotDigest != "sha256:aa" {
			t.Fatalf("拒絕後不得改動既有欄位，got %+v", e)
		}
	})

	t.Run("different_taskrun_rebind_rejected", func(t *testing.T) {
		// **測資陷阱**：digest 刻意與 w1 既有值相同。若這裡用不同的 digest，
		// 「只比對 TaskRunID」與「只比對 digest」兩種單欄位比較 mutation 都會
		// 因為被保留的那個欄位也不相等而落入拒絕分支，兩者皆存活（本列與上一
		// 個 subtest 合起來才覆蓋兩種方向）。
		if err := s.SetTaskRunBinding("w1", "01BX5ZZKBKACTAV9WEVGEMMVRZ", "sha256:aa"); err == nil {
			t.Fatal("不同 TaskRun 重綁應拒絕")
		}
		if e, _ := s.Get("w1"); e.TaskRunID != "01ARZ3NDEKTSV4RRFFQ69G5FAV" {
			t.Fatalf("拒絕後不得改動既有欄位，got %+v", e)
		}
	})
}

// ---- B1：空輸入，各自獨立的負向案例 ----
func TestSetTaskRunBindingRejectsEmptyTaskRunID(t *testing.T) {
	s, _ := openStore(t)
	if err := s.Put(Entry{WSID: "w1", Provider: "claude", CreatedAt: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTaskRunBinding("w1", "", "sha256:aa"); err == nil {
		t.Fatal("空 taskRunID 應拒絕")
	}
	if e, _ := s.Get("w1"); e.TaskRunID != "" || e.SnapshotDigest != "" {
		t.Fatalf("拒絕後不得寫入任何欄位：%+v", e)
	}
}

func TestSetTaskRunBindingRejectsEmptySnapshotDigest(t *testing.T) {
	s, _ := openStore(t)
	if err := s.Put(Entry{WSID: "w1", Provider: "claude", CreatedAt: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTaskRunBinding("w1", "01ARZ3NDEKTSV4RRFFQ69G5FAV", ""); err == nil {
		t.Fatal("空 snapshotDigest 應拒絕")
	}
	if e, _ := s.Get("w1"); e.TaskRunID != "" || e.SnapshotDigest != "" {
		t.Fatalf("拒絕後不得寫入任何欄位：%+v", e)
	}
}

func TestSetTaskRunBindingNotFound(t *testing.T) {
	s, _ := openStore(t)
	if err := s.SetTaskRunBinding("nope", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "sha256:aa"); !errors.Is(err, ErrEntryNotFound) {
		t.Fatalf("不存在的 wsid 應回 ErrEntryNotFound，got %v", err)
	}
}

func TestSetTaskRunBindingTombstoned(t *testing.T) {
	s, _ := openStore(t)
	if err := s.Put(Entry{WSID: "w1", Provider: "claude", CreatedAt: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove("w1", "user_removed"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTaskRunBinding("w1", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "sha256:aa"); !errors.Is(err, ErrTombstoned) {
		t.Fatalf("tombstoned entry 應回 ErrTombstoned，got %v", err)
	}
}

// ---- B4：跨 WSID 1:1 基數。測資陷阱（owner 明訂）：跨 WSID 掃描排在「目標
// 已綁」guard 之後——若目標 entry 本身已綁，會先被前一個 guard 攔下，鑑別
// 力落在錯的 guard。這裡的目標 entry（w2／w3）必須乾淨未綁；且刻意用「同
// TaskRunID、不同 digest」而非同值 pair，避免錯誤實作把檢查寫成「比較整個
// pair」也能通過（同值 pair 在錯誤實作下會被誤判成不同綁定而放行）。----
func TestSetTaskRunBindingCrossWSIDCardinality(t *testing.T) {
	s, _ := openStore(t)
	for _, w := range []string{"w1", "w2", "w3"} {
		if err := s.Put(Entry{WSID: w, Provider: "claude", CreatedAt: "t"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetTaskRunBinding("w1", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "sha256:aa"); err != nil {
		t.Fatal(err)
	}

	t.Run("duplicate_taskrun_different_digest_rejected", func(t *testing.T) {
		// w2 目標乾淨未綁；同 TaskRunID、不同 digest。
		if err := s.SetTaskRunBinding("w2", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "sha256:cc"); err == nil {
			t.Fatal("duplicate TaskRunID 跨 WSID 應拒絕（1:1 雙向），即使 digest 不同")
		}
		if e, ok := s.Get("w2"); ok && e.TaskRunID != "" {
			t.Fatalf("拒絕後 w2 不得被寫入：%+v", e)
		}
	})

	t.Run("tombstoned_occupancy_not_transferable", func(t *testing.T) {
		if err := s.Remove("w1", "user_removed"); err != nil {
			t.Fatal(err)
		}
		// w3 目標乾淨未綁；佔用方（w1）已 tombstoned——abandoned 綁定仍不可
		// 轉移（B5 §3.6）。同樣用不同 digest 避免同值 pair 掩蓋鑑別力。
		if err := s.SetTaskRunBinding("w3", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "sha256:dd"); err == nil {
			t.Fatal("tombstoned 佔用的 TaskRunID 不可轉移")
		}
		if e, ok := s.Get("w3"); ok && e.TaskRunID != "" {
			t.Fatalf("拒絕後 w3 不得被寫入：%+v", e)
		}
	})
}

// ---- B2：partial pair 兩個方向。繞過 SetTaskRunBinding 本身、用 Put 直接
// 塞欄位造出 partial pair（Put 不驗證綁定欄位配對，寫得進去）。----
func TestSetTaskRunBindingPartialPairCorruption(t *testing.T) {
	t.Run("taskrun_only", func(t *testing.T) {
		s, _ := openStore(t)
		if err := s.Put(Entry{WSID: "w1", Provider: "claude", CreatedAt: "t",
			TaskRunID: "01ARZ3NDEKTSV4RRFFQ69G5FAV"}); err != nil { // SnapshotDigest 缺
			t.Fatal(err)
		}
		// 必須由訊息斷言命中 corruption：partial-pair guard 之後緊接著
		// 「已綁定」guard，本測資 old.TaskRunID 非空、digest 不等，guard
		// 移除後會被「已綁定」順帶擋下而仍回錯——只判 err == nil 抓不到。
		if err := s.SetTaskRunBinding("w1", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "sha256:aa"); err == nil ||
			!strings.Contains(err.Error(), "partial pair") {
			t.Fatalf("partial pair（僅 TaskRunID）應視為 corruption 拒絕，不得靜默補全，也不得由「已綁定」guard 順帶擋下：%v", err)
		}
	})

	t.Run("digest_only", func(t *testing.T) {
		s, _ := openStore(t)
		if err := s.Put(Entry{WSID: "w1", Provider: "claude", CreatedAt: "t",
			SnapshotDigest: "sha256:aa"}); err != nil { // TaskRunID 缺
			t.Fatal(err)
		}
		// 對稱補強：本方向目前沒有遮蔽路徑（old.TaskRunID 為空，不會進
		// 「已綁定」guard），但斷言形狀與 taskrun_only 對齊，防禦未來 guard
		// 順序調整造成的迴歸。
		if err := s.SetTaskRunBinding("w1", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "sha256:aa"); err == nil ||
			!strings.Contains(err.Error(), "partial pair") {
			t.Fatalf("partial pair（僅 SnapshotDigest）應視為 corruption 拒絕，不得靜默補全：%v", err)
		}
	})
}

// ---- B5：冪等不落盤。注入 stepWrite 錯誤，同值重寫仍應成功——證明冪等
// 路徑沒有呼叫 persist（若呼叫了，這裡會因注入的錯誤而失敗）。----
func TestSetTaskRunBindingIdempotentDoesNotPersist(t *testing.T) {
	s, _ := openStore(t)
	if err := s.Put(Entry{WSID: "w1", Provider: "claude", CreatedAt: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTaskRunBinding("w1", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "sha256:aa"); err != nil {
		t.Fatal(err)
	}
	failAt(s, stepWrite, errors.New("disk full"))
	if err := s.SetTaskRunBinding("w1", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "sha256:aa"); err != nil {
		t.Fatalf("冪等路徑不應呼叫 persist，got %v", err)
	}
}

// ---- B6：reopen round-trip ＋ 舊檔零遷移 ----
func TestSetTaskRunBindingRoundTripAcrossOpen(t *testing.T) {
	s, path := openStore(t)
	if err := s.Put(Entry{WSID: "w1", Provider: "claude", CreatedAt: "t"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTaskRunBinding("w1", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "sha256:aa"); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := reopened.Get("w1")
	if !ok || e.TaskRunID != "01ARZ3NDEKTSV4RRFFQ69G5FAV" || e.SnapshotDigest != "sha256:aa" {
		t.Fatalf("重新 Open 後綁定欄位必須仍在：%+v ok=%v", e, ok)
	}
}

func TestSetTaskRunBindingLegacyFileZeroMigration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ws.json")
	legacy := `{
  "schema_version": 2,
  "entries": {
    "w1": {"wsid": "w1", "provider": "claude", "created_at": "t"}
  }
}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("舊檔（無新欄位）Open 不應報錯：%v", err)
	}
	e, ok := s.Get("w1")
	if !ok || e.TaskRunID != "" || e.SnapshotDigest != "" {
		t.Fatalf("舊檔零遷移：新欄位應為空，got %+v ok=%v", e, ok)
	}
}

// ---- B7：failure matrix，改寫為可編譯測試（原為散文註解）----
func TestSetTaskRunBindingPersistFailureMatrix(t *testing.T) {
	t.Run("pre_rename_failure_rolls_back", func(t *testing.T) {
		s, path := openStore(t)
		if err := s.Put(Entry{WSID: "w1", Provider: "claude", CreatedAt: "t"}); err != nil {
			t.Fatal(err)
		}
		failAt(s, stepWrite, errors.New("disk full"))
		err := s.SetTaskRunBinding("w1", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "sha256:aa")
		if err == nil {
			t.Fatal("stepWrite 失敗必須回錯")
		}
		if errors.Is(err, ErrRegistryUncertain) || s.Uncertain() {
			t.Fatalf("rename 前失敗不得 latch：%v", err)
		}
		if e, _ := s.Get("w1"); e.TaskRunID != "" || e.SnapshotDigest != "" {
			t.Fatalf("記憶體必須回滾（無綁定欄位），got %+v", e)
		}
		if e, _ := diskEntry(t, path, "w1"); e.TaskRunID != "" {
			t.Fatalf("磁碟必須維持舊值，got %+v", e)
		}
	})

	t.Run("dir_sync_failure_latches_uncertain", func(t *testing.T) {
		s, path := openStore(t)
		if err := s.Put(Entry{WSID: "w1", Provider: "claude", CreatedAt: "t"}); err != nil {
			t.Fatal(err)
		}
		failAt(s, stepDirSync, errors.New("dir fsync EIO"))
		err := s.SetTaskRunBinding("w1", "01ARZ3NDEKTSV4RRFFQ69G5FAV", "sha256:aa")
		if !errors.Is(err, ErrRegistryUncertain) {
			t.Fatalf("directory sync 失敗必須回 ErrRegistryUncertain，got %v", err)
		}
		if !s.Uncertain() {
			t.Fatal("directory sync 失敗必須 latch uncertain")
		}
		// 不回滾：rename 已成功，退回舊值等於宣稱一個 process 內無法證明的事實
		// （沿 TestDirectorySyncFailureLatchesUncertainAndRefusesAllWrites 的
		// 「(2) 不回滾」既有斷言形狀，現 fsync_test.go:243-245）。
		if e, _ := s.Get("w1"); e.TaskRunID != "01ARZ3NDEKTSV4RRFFQ69G5FAV" {
			t.Fatalf("記憶體不得退回舊值，got %+v", e)
		}
		if e, _ := diskEntry(t, path, "w1"); e.TaskRunID != "01ARZ3NDEKTSV4RRFFQ69G5FAV" {
			t.Fatalf("rename 已成功，磁碟應為新值，got %+v", e)
		}
	})
}
