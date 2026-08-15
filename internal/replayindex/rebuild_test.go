package replayindex

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/appcore"
	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// seedRig：測試專用的 audit＋index 雙寫工具。把每筆事件同時（1）以production
// 相同格式（JSON＋"\n"）append 進真實 auditPath 檔、byte-accurate 記錄
// offset；（2）餵給一個「seeding index」（開在 dir，走真實 Observe 路徑）。
// 兩者天生一致，讓 seedAuditWithTurns／seedAuditWithOpenTurn 建出的
// (dir, auditPath) 精確模擬「crash 發生前 index 曾經完全跟上 audit」的起點
// ——之後測試再對 dir 做局部破壞（truncateIndexTo／writeBogusCheckpoint），
// 模擬 crash 造成的落後或不可信 checkpoint。
type seedRig struct {
	t      *testing.T
	f      *os.File
	offset int64
	idx    *Index
}

func newSeedRig(t *testing.T, dir, auditPath string) *seedRig {
	t.Helper()
	f, err := os.OpenFile(auditPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	idx, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return &seedRig{t: t, f: f, idx: idx}
}

func (r *seedRig) write(env contract.Envelope) {
	r.t.Helper()
	b, err := json.Marshal(env)
	if err != nil {
		r.t.Fatal(err)
	}
	n, err := fmt.Fprintf(r.f, "%s\n", b)
	if err != nil {
		r.t.Fatal(err)
	}
	start := r.offset
	end := start + int64(n)
	r.offset = end
	receipt := appcore.AppendReceipt{StartOffset: start, EndOffset: end, EventID: env.EventID}
	if err := r.idx.Observe(env, receipt); err != nil {
		r.t.Fatalf("seedRig.write Observe: %v", err)
	}
}

func (r *seedRig) userMsg(wsid, id string) {
	r.write(contract.Envelope{EventID: id, Kind: string(contract.KindMessage), Role: "user", WorkspaceSessionID: wsid})
}

func (r *seedRig) delta(wsid, id string) {
	r.write(contract.Envelope{EventID: id, Kind: string(contract.KindDelta), WorkspaceSessionID: wsid})
}

func (r *seedRig) done(wsid, id string) {
	r.write(contract.Envelope{EventID: id, Kind: string(contract.KindStateChange), State: string(contract.StateDone), WorkspaceSessionID: wsid})
}

// seedAuditWithTurns：建出一組 (index dir, audit path)，audit 交錯 wsid "w1"
// 與 "w2" 各自 n 個完整 turn；每個 turn k 是 w1 開、w2 開、w1 delta、w2
// delta、w2 收尾、w1 收尾——兩個 WSID 在收尾前同時各自開著 turn、各自累積
// 事件，模擬 brief 強調的「checkpointOffset 是單一全域欄位，落後量是所有
// open turn 累積量之和」，不只是單一 WSID 的單一 turn。dir 是完全跟上 audit
// 的 index（checkpoint＋w1.turns.jsonl／w2.turns.jsonl 皆反映全部 n 個 turn）。
func seedAuditWithTurns(t *testing.T, n int) (dir, auditPath string) {
	t.Helper()
	tmp := t.TempDir()
	dir = filepath.Join(tmp, "index")
	auditPath = filepath.Join(tmp, "events.jsonl")
	r := newSeedRig(t, dir, auditPath)
	for k := 0; k < n; k++ {
		base := fmt.Sprintf("t%d", k)
		r.userMsg("w1", "w1-user-"+base)
		r.userMsg("w2", "w2-user-"+base) // w1 turn 仍開著時，w2 也開了一個 turn
		r.delta("w1", "w1-delta-"+base)
		r.delta("w2", "w2-delta-"+base)
		r.done("w2", "w2-done-"+base)
		r.done("w1", "w1-done-"+base)
	}
	return dir, auditPath
}

// seedAuditWithOpenTurn：wsid "w1" 開一個 turn（event id "user-msg-1"）不收
// 尾；接著 wsid "w2" 跑一個完整 turn。w2 收尾的 boundary 會把全域
// checkpointOffset／LastEventID 推到 w1 開 turn 之後——模擬「checkpoint 越過
// 未完成 turn」（checkpointOffset 是單一全域欄位，不是 per-WSID）。
func seedAuditWithOpenTurn(t *testing.T) (dir, auditPath string) {
	t.Helper()
	tmp := t.TempDir()
	dir = filepath.Join(tmp, "index")
	auditPath = filepath.Join(tmp, "events.jsonl")
	r := newSeedRig(t, dir, auditPath)
	r.userMsg("w1", "user-msg-1") // 開 turn，不收尾
	r.userMsg("w2", "user-msg-2")
	r.delta("w2", "delta-2")
	r.done("w2", "done-2")
	return dir, auditPath
}

// truncateIndexTo：把 dir 底下 wsid 的 turns.jsonl 砍到只剩前 n 筆、checkpoint
// 一併回撥至第 n 筆 turn 的結尾——模擬「crash 發生時 index 只確實持久化了前
// n 個 turn」，之後 audit 裡更完整的內容尚未反映在 checkpoint／turns.jsonl。
func truncateIndexTo(t *testing.T, dir, wsid string, n int) {
	t.Helper()
	path := filepath.Join(dir, wsid+".turns.jsonl")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimRight(b, "\n"), []byte("\n"))
	if n <= 0 || n > len(lines) {
		t.Fatalf("truncateIndexTo: n=%d 超出現有 turn 數 %d", n, len(lines))
	}
	kept := lines[:n]
	var buf bytes.Buffer
	for _, l := range kept {
		buf.Write(l)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	var last TurnRecord
	if err := json.Unmarshal(kept[len(kept)-1], &last); err != nil {
		t.Fatal(err)
	}
	writeCheckpointFileForTest(t, dir, last.EndOffset, last.LastEventID, nil)
}

// writeBogusCheckpoint：直接把 checkpoint.json 覆寫成指定（可能不可信的）
// offset／last_event_id，模擬 crash 導致 checkpoint 超前或對不上 audit 內容。
func writeBogusCheckpoint(t *testing.T, dir string, offset int64, lastEventID string) {
	t.Helper()
	writeCheckpointFileForTest(t, dir, offset, lastEventID, nil)
}

func writeCheckpointFileForTest(t *testing.T, dir string, offset int64, lastEventID string, openTurns map[string]int64) {
	t.Helper()
	if openTurns == nil {
		openTurns = map[string]int64{}
	}
	cf := checkpointFile{Offset: offset, LastEventID: lastEventID, OpenTurns: openTurns}
	b, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "checkpoint.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func auditSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

// firstEventIDAt：獨立於 production 的 eventIDAtOffset，直接讀 audit 檔驗證
// 內容——刻意不呼叫 production 函式，避免測試對著自己的實作自我驗證。
func firstEventIDAt(t *testing.T, path string, off int64) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	var line []byte
	buf := make([]byte, 1)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			line = append(line, buf[0])
			if buf[0] == '\n' {
				break
			}
		}
		if rerr != nil {
			break
		}
	}
	var env contract.Envelope
	if err := json.Unmarshal(bytes.TrimRight(line, "\n"), &env); err != nil {
		t.Fatal(err)
	}
	return env.EventID
}

// TestIndexBehindIsCaughtUp：落後補掃須跨 w1／w2 兩個 WSID 各自重建，不是
// 只顧單一 WSID——checkpointOffset 是單一全域欄位，crash 前若兩個 WSID 都
// 只確實持久化到各自第 1 個 turn，補掃必須把兩者都追回全部 3 個 turn。
func TestIndexBehindIsCaughtUp(t *testing.T) {
	dir, audit := seedAuditWithTurns(t, 3)
	i, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	truncateIndexTo(t, dir, "w1", 1)
	truncateIndexTo(t, dir, "w2", 1)
	if err := i.VerifyOrRebuild(audit); err != nil {
		t.Fatal(err)
	}
	if turns, _ := i.RecentTurns("w1", 10); len(turns) != 3 {
		t.Fatalf("落後必須補掃（w1）：%d", len(turns))
	}
	if turns, _ := i.RecentTurns("w2", 10); len(turns) != 3 {
		t.Fatalf("落後必須補掃（w2）：%d", len(turns))
	}
}

// TestIndexAheadIsRepaired：不可信 checkpoint 觸發的全量重建同樣須跨多個
// WSID 正確重建，且既有的 *.turns.jsonl 必須被 quarantine（改名保留、非刪
// 除，§3.5.6 用字），不是直接消失。
func TestIndexAheadIsRepaired(t *testing.T) {
	dir, audit := seedAuditWithTurns(t, 2)
	i, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	staleW1 := filepath.Join(dir, "w1.turns.jsonl")
	staleW2 := filepath.Join(dir, "w2.turns.jsonl")
	writeBogusCheckpoint(t, dir, 1<<30, "e-nonexistent")
	if err := i.VerifyOrRebuild(audit); err != nil {
		t.Fatal(err)
	}
	off, last := i.Checkpoint()
	if off > auditSize(t, audit) || last == "e-nonexistent" {
		t.Fatalf("超前的不可信 checkpoint 未修復：%d %s", off, last)
	}
	// 修復後仍應保有兩個 WSID 各兩個完整 turn（不可信重建須以「丟棄舊快取
	// ＋全量重掃」完成，不能留下 0 筆、遺漏其中一個 WSID，或重複筆數）。
	if turns, _ := i.RecentTurns("w1", 10); len(turns) != 2 {
		t.Fatalf("超前修復後應精確重建 2 個 turn、不重複不遺漏（w1）：%d", len(turns))
	}
	if turns, _ := i.RecentTurns("w2", 10); len(turns) != 2 {
		t.Fatalf("超前修復後應精確重建 2 個 turn、不重複不遺漏（w2）：%d", len(turns))
	}
	// 舊的 turns.jsonl 內容必須被 quarantine（改名保留一份），不是直接被
	// 刪除、憑空消失（§3.5.6）——原檔名之後會被全量重掃重新建立（內容是
	// 剛剛驗證過的、正確的 2 個 turn），所以不斷言原檔名不存在，只斷言舊
	// 內容有被搬移保留一份。
	quarantined, err := filepath.Glob(staleW1 + ".quarantine-*")
	if err != nil || len(quarantined) != 1 {
		t.Fatalf("應留下唯一一份 w1 quarantine 檔：matches=%v err=%v", quarantined, err)
	}
	if quarantined2, err := filepath.Glob(staleW2 + ".quarantine-*"); err != nil || len(quarantined2) != 1 {
		t.Fatalf("應留下唯一一份 w2 quarantine 檔：matches=%v err=%v", quarantined2, err)
	}
}

// writeRawAuditEvents：直接以純檔案 I/O 寫入 n 筆合法 JSONL 事件到 path（不
// 經過 seedRig／Index，不觸發任何 checkpoint 落盤）——只為快速產生一個遠大
// 於 maxLineWindow 的 audit 檔，避免 TestCheckpointVerificationDoesNotScanWholeFile
// 為了造出夠大的檔案而付出 seedRig 逐筆 Observe（含每個 boundary 都做一次
// atomic rename 落盤）的開銷。回傳最後一筆事件的 EndOffset／EventID，供呼叫
// 端組出對應的 checkpoint.json。
func writeRawAuditEvents(t *testing.T, path string, n int) (lastEndOffset int64, lastEventID string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	var offset int64
	for k := 0; k < n; k++ {
		id := fmt.Sprintf("e-%d", k)
		env := contract.Envelope{
			EventID: id, Kind: string(contract.KindStateChange),
			State: string(contract.StateDone), WorkspaceSessionID: "w1",
		}
		b, err := json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		n, err := fmt.Fprintf(w, "%s\n", b)
		if err != nil {
			t.Fatal(err)
		}
		offset += int64(n)
		lastEndOffset = offset
		lastEventID = id
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	return lastEndOffset, lastEventID
}

// countingAuditFile：包住真實 *os.File、累計 ReadAt 實際讀到的 byte
// 數——供 TestCheckpointVerificationDoesNotScanWholeFile 量測 checkpoint 驗
// 證的真實 I/O 量，不是靠猜測或計時。
type countingAuditFile struct {
	f         *os.File
	bytesRead int64
}

func (c *countingAuditFile) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.f.ReadAt(p, off)
	c.bytesRead += int64(n)
	return n, err
}

func (c *countingAuditFile) Close() error { return c.f.Close() }

// TestCheckpointVerificationDoesNotScanWholeFile：mutation 驗證（Important
// #2）——checkpoint 驗證只應讀 target 附近的 window，讀取量必須遠小於整份
// audit 檔案大小、且不超過 maxLineWindow。若日後有人把 lineEventIDEndingAt
// 改回「從 offset 0 逐行掃到 target」的寫法，這則測試會直接爆掉：那正是
// replayindex 存在的理由本身（讓重啟不必全掃 events.jsonl）。
func TestCheckpointVerificationDoesNotScanWholeFile(t *testing.T) {
	dir := t.TempDir()
	auditPath := filepath.Join(dir, "events.jsonl")
	lastEnd, lastID := writeRawAuditEvents(t, auditPath, 30000)
	writeCheckpointFileForTest(t, dir, lastEnd, lastID, nil)

	total := auditSize(t, auditPath)
	if total <= maxLineWindow {
		t.Fatalf("測試前提不成立：audit 檔（%d bytes）必須大於 maxLineWindow（%d bytes）才能證明 O(1)", total, maxLineWindow)
	}

	i, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	var opened *countingAuditFile
	orig := openAuditFile
	openAuditFile = func(path string) (auditFile, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		opened = &countingAuditFile{f: f}
		return opened, nil
	}
	t.Cleanup(func() { openAuditFile = orig })

	if err := i.VerifyOrRebuild(auditPath); err != nil {
		t.Fatal(err)
	}

	if opened == nil {
		t.Fatal("checkpoint 驗證應透過 openAuditFile 開檔（測試前提未成立）")
	}
	if opened.bytesRead >= total {
		t.Fatalf("checkpoint 驗證讀取量應遠小於檔案大小（O(1) 相對檔案大小），實際讀了 %d／總共 %d bytes", opened.bytesRead, total)
	}
	if opened.bytesRead > maxLineWindow {
		t.Fatalf("checkpoint 驗證讀取量不應超過 maxLineWindow：%d > %d", opened.bytesRead, int64(maxLineWindow))
	}
}

func TestCheckpointPastOpenTurnRebuilds(t *testing.T) {
	dir, audit := seedAuditWithOpenTurn(t)
	i, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := i.VerifyOrRebuild(audit); err != nil {
		t.Fatal(err)
	}
	off, ok := i.OpenTurnStart("w1")
	if !ok {
		t.Fatal("checkpoint 越過未完成 turn 時必須能重建其起點（§3.5.5）")
	}
	if got := firstEventIDAt(t, audit, off); got != "user-msg-1" {
		t.Fatalf("open turn 起點錯：%s", got)
	}
}
