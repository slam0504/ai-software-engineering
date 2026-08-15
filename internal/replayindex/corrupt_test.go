package replayindex

import (
	"bytes"
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
	if turns, _ := i.RecentTurns("w1", 10); len(turns) != 3 {
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
	if turns, _ := i.RecentTurns("w1", 10); len(turns) != 3 {
		t.Fatalf("必須全量重建：%d", len(turns))
	}
	if notices != 1 {
		t.Fatalf("必須發一次復原通知：%d", notices)
	}
}

// TestTailCorruptionActuallyTruncatesDisk：品質重點——尾端 truncate 必須真的
// 改寫磁碟上的 <wsid>.turns.jsonl，不能只在記憶體忽略最後一行；否則下次啟動
// （新的 Index 實例、無任何記憶體殘留）又會撞到同一個壞行。刻意不透過
// RecentTurns 間接推論，直接讀磁碟位元組核對壞行已消失、valid 內容還在。
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
	turns, err := j.RecentTurns("w1", 10)
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
	turns, err := i.RecentTurns("w1", 10)
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
}
