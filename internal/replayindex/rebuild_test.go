package replayindex

import (
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

// seedAuditWithTurns：建出一組 (index dir, audit path)，audit 含 wsid "w1"
// 依序 n 個完整 turn（user → delta → done），dir 是完全跟上 audit 的 index
// （checkpoint＋w1.turns.jsonl 皆反映全部 n 個 turn）。
func seedAuditWithTurns(t *testing.T, n int) (dir, auditPath string) {
	t.Helper()
	tmp := t.TempDir()
	dir = filepath.Join(tmp, "index")
	auditPath = filepath.Join(tmp, "events.jsonl")
	r := newSeedRig(t, dir, auditPath)
	for k := 0; k < n; k++ {
		base := fmt.Sprintf("t%d", k)
		r.userMsg("w1", "user-"+base)
		r.delta("w1", "delta-"+base)
		r.done("w1", "done-"+base)
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

func TestIndexBehindIsCaughtUp(t *testing.T) {
	dir, audit := seedAuditWithTurns(t, 3)
	i, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	truncateIndexTo(t, dir, "w1", 1)
	if err := i.VerifyOrRebuild(audit); err != nil {
		t.Fatal(err)
	}
	if turns, _ := i.RecentTurns("w1", 10); len(turns) != 3 {
		t.Fatalf("落後必須補掃：%d", len(turns))
	}
}

func TestIndexAheadIsRepaired(t *testing.T) {
	dir, audit := seedAuditWithTurns(t, 2)
	i, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	writeBogusCheckpoint(t, dir, 1<<30, "e-nonexistent")
	if err := i.VerifyOrRebuild(audit); err != nil {
		t.Fatal(err)
	}
	off, last := i.Checkpoint()
	if off > auditSize(t, audit) || last == "e-nonexistent" {
		t.Fatalf("超前的不可信 checkpoint 未修復：%d %s", off, last)
	}
	// 修復後仍應保有兩個完整 turn（不可信重建須以「丟棄舊快取＋全量重掃」
	// 完成，不能留下 0 筆或重複筆數）。
	if turns, _ := i.RecentTurns("w1", 10); len(turns) != 2 {
		t.Fatalf("超前修復後應精確重建 2 個 turn、不重複不遺漏：%d", len(turns))
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
