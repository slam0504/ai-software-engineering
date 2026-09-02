package replayindex

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// appendGarbage：直接在 dir/<wsid>.turns.jsonl 檔尾 append 一段無法解析的原
// 始位元組（純檔案 I/O，不經過 Index），模擬 crash 發生在 appendTurnRecord
// 寫到一半、留下半筆壞行在檔尾的情形（§3.5.6 尾端 corruption）。
func appendGarbage(t *testing.T, dir, wsid, garbage string) {
	t.Helper()
	path := filepath.Join(dir, wsid+".turns.jsonl")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(garbage); err != nil {
		t.Fatal(err)
	}
}

// corruptMiddleLine：把 dir/<wsid>.turns.jsonl 的第一行改成無法解析的內容，
// 保留其餘行不動——第一行之後仍有 valid 行，模擬 §3.5.6 的中段 corruption
// （crash 或磁碟損壞打壞了檔案中間的一筆，不是最後一筆）。
func corruptMiddleLine(t *testing.T, dir, wsid string) {
	t.Helper()
	path := filepath.Join(dir, wsid+".turns.jsonl")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimRight(b, "\n"), []byte("\n"))
	if len(lines) < 2 {
		t.Fatalf("corruptMiddleLine 需要至少 2 行才能保證壞行之後仍有 valid 行，實際 %d 行", len(lines))
	}
	lines[0] = []byte("{not json")
	var buf bytes.Buffer
	for _, l := range lines {
		buf.Write(l)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// quarantineExists：dir 底下是否存在任何 *.quarantine-* 檔（resetTurnFilesLocked
// 的命名慣例，見 rebuild.go）。
func quarantineExists(t *testing.T, dir string) bool {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "*.quarantine-*"))
	if err != nil {
		t.Fatal(err)
	}
	return len(matches) > 0
}

func TestTailCorruptionTruncatesAndContinues(t *testing.T) {
	dir, audit := seedAuditWithTurns(t, 3)
	var notices int
	i, _ := OpenWith(dir, Config{Notify: func(string) { notices++ }})
	appendGarbage(t, dir, "w1", "{not json\n")
	if err := i.VerifyOrRebuild(audit); err != nil {
		t.Fatal(err)
	}
	if turns, _ := i.recentTurns("w1", 10); len(turns) != 3 {
		t.Fatalf("尾端 corruption 應 truncate 續用：%d", len(turns))
	}
	if quarantineExists(t, dir) || notices != 0 {
		t.Fatal("尾端 corruption 不該 quarantine、不需通知")
	}
}

func TestMidCorruptionQuarantinesAndRebuilds(t *testing.T) {
	dir, audit := seedAuditWithTurns(t, 3)
	var notices int
	i, _ := OpenWith(dir, Config{Notify: func(string) { notices++ }})
	corruptMiddleLine(t, dir, "w1")
	if err := i.VerifyOrRebuild(audit); err != nil {
		t.Fatal(err)
	}
	if !quarantineExists(t, dir) {
		t.Fatal("中段 corruption 必須 quarantine（§3.5.6）")
	}
	if turns, _ := i.recentTurns("w1", 10); len(turns) != 3 {
		t.Fatalf("必須全量重建：%d", len(turns))
	}
	// 重建範圍是全域（quarantine 全部既有 turn file、從頭全量重掃），不是只
	// 精修受損的 w1——未受損的 sibling wsid（w2）在重建後也必須完整、不重
	// 複、不遺漏，這正是這次 review 核心爭議「全域重建範圍是否安全」的直接
	// 證據。
	if turns, _ := i.recentTurns("w2", 10); len(turns) != 3 {
		t.Fatalf("全域重建不該誤殺未受損的 sibling wsid（w2）：%d", len(turns))
	}
	if notices != 1 {
		t.Fatalf("必須發一次復原通知：%d", notices)
	}
}

// TestTailCorruptionActuallyTruncatesDisk：品質重點——尾端 truncate 必須真的
// 改寫磁碟上的 <wsid>.turns.jsonl，不能只在記憶體忽略最後一行；否則下次啟動
// （新的 Index 實例、無任何記憶體殘留）又會撞到同一個壞行。刻意不透過
// recentTurns 間接推論，直接讀磁碟位元組核對壞行已消失、valid 內容還在。
func TestTailCorruptionActuallyTruncatesDisk(t *testing.T) {
	dir, audit := seedAuditWithTurns(t, 3)
	i, _ := OpenWith(dir, Config{})
	appendGarbage(t, dir, "w1", "{not json\n")
	if err := i.VerifyOrRebuild(audit); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "w1.turns.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte("not json")) {
		t.Fatalf("尾端 corruption 必須真的從磁碟移除，不能只在記憶體忽略：%s", b)
	}
	lines := bytes.Split(bytes.TrimRight(b, "\n"), []byte("\n"))
	if len(lines) != 3 {
		t.Fatalf("truncate 後磁碟應只剩 3 筆 valid record：%d", len(lines))
	}

	// 用一個全新的 Index 實例（模擬下次啟動、無任何記憶體殘留）重新讀取，
	// 確認不會再撞到同一個壞行。
	j, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	turns, err := j.recentTurns("w1", 10)
	if err != nil {
		t.Fatalf("下次啟動重新讀取不該再撞到已 truncate 的壞行：%v", err)
	}
	if len(turns) != 3 {
		t.Fatalf("下次啟動仍應看到全部 3 筆 valid turn：%d", len(turns))
	}
}

// TestMidCorruptionDoesNotLoseEvents：品質重點——中段 quarantine 之後的全量
// 重建，資料來源是 events.jsonl（唯一權威），確認重建後的 turn record 內容
// 精確對應 audit、不是「猜」出來的空殼或截斷版本。
func TestMidCorruptionDoesNotLoseEvents(t *testing.T) {
	dir, audit := seedAuditWithTurns(t, 3)
	i, _ := OpenWith(dir, Config{})
	corruptMiddleLine(t, dir, "w1")
	if err := i.VerifyOrRebuild(audit); err != nil {
		t.Fatal(err)
	}
	turns, err := i.recentTurns("w1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 3 {
		t.Fatalf("全量重建必須精確復原全部 3 筆 turn：%d", len(turns))
	}
	for k, turn := range turns {
		wantFirst := "w1-user-t" + strconv.Itoa(k)
		wantLast := "w1-done-t" + strconv.Itoa(k)
		if turn.FirstEventID != wantFirst || turn.LastEventID != wantLast {
			t.Fatalf("turn %d 內容錯誤（不可猜測事件）：got first=%s last=%s want first=%s last=%s",
				k, turn.FirstEventID, turn.LastEventID, wantFirst, wantLast)
		}
	}

	// 未受損的 sibling wsid（w2）同樣經過全域重建，內容也必須精確對應
	// audit、不重複不遺漏——涵蓋「全域重建範圍」這個決策的另一半證據。
	w2turns, err := i.recentTurns("w2", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(w2turns) != 3 {
		t.Fatalf("全域重建必須精確復原 sibling wsid w2 的全部 3 筆 turn：%d", len(w2turns))
	}
	for k, turn := range w2turns {
		wantFirst := "w2-user-t" + strconv.Itoa(k)
		wantLast := "w2-done-t" + strconv.Itoa(k)
		if turn.FirstEventID != wantFirst || turn.LastEventID != wantLast {
			t.Fatalf("w2 turn %d 內容錯誤（不可猜測事件）：got first=%s last=%s want first=%s last=%s",
				k, turn.FirstEventID, turn.LastEventID, wantFirst, wantLast)
		}
	}
}

// writeRawTurnFile：純檔案 I/O 直接把 lines 逐行寫進 dir/<wsid>.turns.jsonl
// （不經過 Index／appendTurnRecord），讓下面兩則邊界測試能精確控制檔案裡每
// 一行是 valid TurnRecord 或任意壞行，不受 seedAuditWithTurns 產生的固定內
// 容侷限。
func writeRawTurnFile(t *testing.T, dir, wsid string, lines []string) {
	t.Helper()
	var buf bytes.Buffer
	for _, l := range lines {
		buf.WriteString(l)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, wsid+".turns.jsonl"), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// validTurnRecordLine：組出一筆可解析的 TurnRecord JSON 行，供 writeRawTurnFile
// 的呼叫端標出「這一行是 valid」。
func validTurnRecordLine(t *testing.T, eventID string) string {
	t.Helper()
	b, err := json.Marshal(TurnRecord{FirstEventID: eventID, LastEventID: eventID})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestMidCorruptionWithConsecutiveBadLinesIsStillDetected：中段判定邊界
// (b)——壞行之後緊接著又是另一行壞行，再之後才出現 valid 行。判定演算法必
// 須「掃到底找第一筆可解析」，不能只看壞行的下一行：若只看下一行（也是壞
// 行）就判定沒有 valid 行在後、當成尾端 truncate，會直接把後面真正 valid 的
// 記錄連同壞行一起截掉、遺失事件——這正是 mutation 驗證要抓的錯誤實作。
func TestMidCorruptionWithConsecutiveBadLinesIsStillDetected(t *testing.T) {
	dir := t.TempDir()
	i, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	lines := []string{
		validTurnRecordLine(t, "e0"),
		"{not json 1",
		"{not json 2",                // 壞行的下一行仍是壞行
		validTurnRecordLine(t, "e1"), // 再下一行才是 valid：整體仍是中段
	}
	writeRawTurnFile(t, dir, "w1", lines)

	if _, err := i.recentTurns("w1", 10); err == nil {
		t.Fatal("壞行之後即使緊接著另一個壞行，只要再往後掃到 valid 行仍須判定中段、fail loud，不能誤判尾端而截斷遺失後面的 valid 記錄")
	}

	// 中段判定不該讓 readTurnFileLocked 自行動磁碟（沒有 audit path、無法安
	// 全重建）——確認檔案內容原封不動，遺失防線交給握有 audit 的
	// VerifyOrRebuild，不是這裡默默截斷。
	b, err := os.ReadFile(filepath.Join(dir, "w1.turns.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var want bytes.Buffer
	for _, l := range lines {
		want.WriteString(l)
		want.WriteByte('\n')
	}
	if !bytes.Equal(b, want.Bytes()) {
		t.Fatalf("中段判定不該改動磁碟內容：got=%q want=%q", b, want.Bytes())
	}
}

// TestSingleLineFileFullyCorruptIsTailTruncatedToEmpty：中段判定邊界
// (d)——單行檔案、該行本身就是壞行。壞行之後沒有任何行（valid 或壞行都沒
// 有），依判定規則（壞行之後沒有 valid 行 ⇒ 尾端）屬於尾端 corruption，應
// truncate 續用（清空該檔），不是回錯、也不是誤判成中段。
func TestSingleLineFileFullyCorruptIsTailTruncatedToEmpty(t *testing.T) {
	dir := t.TempDir()
	i, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	writeRawTurnFile(t, dir, "w1", []string{"{not json at all"})

	turns, err := i.recentTurns("w1", 10)
	if err != nil {
		t.Fatalf("單行檔案全壞、後面沒有 valid 行屬於尾端 corruption，不該 fail loud：%v", err)
	}
	if len(turns) != 0 {
		t.Fatalf("truncate 後應該沒有任何 valid turn：%d", len(turns))
	}

	b, err := os.ReadFile(filepath.Join(dir, "w1.turns.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(bytes.TrimSpace(b)) != 0 {
		t.Fatalf("尾端 truncate 必須真的清空磁碟上的單行全壞檔：%q", b)
	}
}
