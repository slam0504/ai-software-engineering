package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/claude"
	"github.com/slam0504/sdlc-workbench/internal/singleinstance"
)

// ---- single-instance guard：跨 process 守門 ----
//
// 為什麼一定要 spawn 真的 OS process：flock(2) 的持有者是 kernel 裡的 open
// file description，而「crash／SIGKILL 之後鎖自然釋放」這件事只有 kernel 在
// process 死亡時才會做。同一個 process 裡放兩個 App 實例假裝多開，量到的是
// 別的東西——這條就是這個里程碑的失效形狀 (E)（測試環境本身量不出那個效果）
// 與 (F)（跨 process 那一維沒守）。
//
// 這裡的每一條守門都刻意**不以受測程式自陳的欄位為 oracle**：
//   - 「誰贏了」看 OS 退出碼與 audit.jsonl 裡 startup 紀錄的**筆數**；
//   - 「輸家沒動任何 state」看 state directory 的遞迴磁碟快照（路徑集合、
//     大小、mode、mtime、內容雜湊），不看任何「有沒有呼叫過 X」的探針；
//   - 「鎖還被持有」看第三個真 process 的 Acquire 結果。
//
// 同步一律用 pipe barrier（父行程寫一行才放行 / 關 pipe 才收尾），沒有任何
// time.Sleep。程式碼裡的 time.After 只有一個用途：卡死時的 watchdog，讓失敗
// 是「超時 + 已收到的輸出」而不是整個 go test 逾時。

const (
	// siHelperEnv：把這個 test binary 的 re-exec 進入點從一般 go test（直接
	// return）切成 helper 模式。
	siHelperEnv = "WB_SINGLE_INSTANCE_HELPER"
	// siBarrierEnv："1" = 搶鎖之前先等父行程放行一行（起跑閘門）。
	siBarrierEnv = "WB_SINGLE_INSTANCE_BARRIER"
)

const (
	// siModeHold：走 production runApp，但 UI 那一步只回報「已持鎖」然後阻塞。
	// **不呼叫 App.startup**——所以 state directory 上除了 resolveWorkspace 建
	// 的目錄與 lock file 之外不會有任何 writer 足跡，這正是守門 3 的量測基準：
	// 輸家只要開過任何一個 writer（sessions.json／events.jsonl／replay-index
	// ／wire-segments.jsonl／evidence/…）都會多出檔案。
	siModeHold = "hold"
	// siModeFull：走 production runApp ＋完整 App.startup／shutdown。
	siModeFull = "full"
	// siModePauseShutdown：同 full，但在 shutdown 總序的 siPauseStep 停住——
	// 這一刻 manager／sink／registry／wire segments 都還開著。
	siModePauseShutdown = "full-pause-shutdown"
)

// siPauseStep：§3.6.5 shutdown 總序中，**所有 writer 都還沒關**的一個節點
// （snapshot 在 manager_close／index_flush_close／registry_sync 之前）。
const siPauseStep = "snapshot"

const (
	siMarkerAcquired = "SI-ACQUIRED"
	siMarkerStarted  = "SI-STARTED"
	siMarkerPaused   = "SI-SHUTDOWN-PAUSED"
)

// siWatchdog：卡死時的失敗上限，不是同步手段。
const siWatchdog = 90 * time.Second

// siStdin：helper 的閘門來源。父行程寫一行＝放行；關 pipe（EOF）也放行，讓
// 「父行程結束」等同收尾信號。
var siStdin = bufio.NewReader(os.Stdin)

func siAwait() { _, _ = siStdin.ReadString('\n') }

func siSay(marker string) { fmt.Println(marker) }

// TestHelperProcessSingleInstance 是這個 test binary 的 re-exec 進入點：一般
// go test 直接 return，帶 siHelperEnv 執行時改為呼叫 production 的 runApp。
//
// 被替換掉的**只有 uiRunner**（開視窗那一步，無 GUI 環境跑不了）。workspace
// 解析、single-instance 鎖的取得位置、App 的建立、以及鎖釋放的時機全部留在
// runApp 裡，測試繞不過去——所以「鎖太晚取」「鎖太早放」這兩個 mutation 改的
// 是 helper 也一起走的那條 production 路徑。
func TestHelperProcessSingleInstance(t *testing.T) {
	mode := os.Getenv(siHelperEnv)
	if mode == "" {
		return
	}
	if os.Getenv(siBarrierEnv) == "1" {
		siAwait()
	}
	os.Exit(runApp(siHelperUI(mode), os.Stderr))
}

func siHelperUI(mode string) uiRunner {
	return func(app *App) error {
		// 走到這裡代表 runApp 已經取得鎖（失敗的話 runApp 根本不會呼叫 uiRunner）。
		siSay(siMarkerAcquired)
		if mode == siModeHold {
			siAwait()
			return nil
		}
		app.emitUI = func(string, any) {} // 不碰 wails runtime
		if mode == siModePauseShutdown {
			app.hookShutdownStep = func(step string) {
				if step == siPauseStep {
					siSay(siMarkerPaused)
					siAwait()
				}
			}
		}
		ctx := context.Background()
		app.startup(ctx)
		siSay(siMarkerStarted)
		siAwait()
		app.shutdown(ctx)
		return nil
	}
}

// ---- 父行程側 ----

type siBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *siBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *siBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type siProc struct {
	name   string
	cmd    *exec.Cmd
	stdin  *os.File
	lines  chan string
	exited chan struct{}
	code   int
	errBuf *siBuf
}

// siStart 起一個 helper process。刻意用 os.Pipe 而不是 cmd.StdoutPipe：後者
// 要求「所有讀取都完成後才能 Wait」，而這裡必須能在還在讀 marker 的同時等
// 待 process 退出（輸家就是還沒印任何 marker 就退出的那個）。
func siStart(t *testing.T, name, ws, mode string, barrier bool) *siProc {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcessSingleInstance$")
	cmd.Env = append(os.Environ(), siHelperEnv+"="+mode, "WORKBENCH_WORKSPACE="+ws)
	if barrier {
		cmd.Env = append(cmd.Env, siBarrierEnv+"=1")
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatalf("%s: stdin pipe: %v", name, err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("%s: stdout pipe: %v", name, err)
	}
	errBuf := &siBuf{}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errBuf
	if err := cmd.Start(); err != nil {
		t.Fatalf("%s: start: %v", name, err)
	}
	_ = inR.Close()
	_ = outW.Close()

	p := &siProc{name: name, cmd: cmd, stdin: inW,
		lines: make(chan string, 16), exited: make(chan struct{})}
	p.errBuf = errBuf
	go func() {
		sc := bufio.NewScanner(outR)
		for sc.Scan() {
			p.lines <- sc.Text()
		}
		close(p.lines)
		_ = outR.Close()
	}()
	go func() {
		p.code = siExitCode(cmd.Wait())
		close(p.exited)
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		select {
		case <-p.exited:
		default:
			_ = cmd.Process.Kill()
			<-p.exited
		}
	})
	return p
}

// siExitCode：ExitCode() 對被訊號終止的 process 回 -1，SIGKILL 守門正是靠這
// 個值與正常退出分得開。
func siExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// release 放行 helper 的下一個閘門。
func (p *siProc) release(t *testing.T) {
	t.Helper()
	if _, err := io.WriteString(p.stdin, "go\n"); err != nil {
		t.Fatalf("%s: 放行失敗：%v（stderr=%s）", p.name, err, p.errBuf.String())
	}
}

func (p *siProc) awaitMarker(t *testing.T, want string) {
	t.Helper()
	for {
		select {
		case line, ok := <-p.lines:
			if !ok {
				<-p.exited
				t.Fatalf("%s: stdout 收束仍未看到 %s（exit=%d stderr=%s）",
					p.name, want, p.code, p.errBuf.String())
			}
			if strings.TrimSpace(line) == want {
				return
			}
		case <-time.After(siWatchdog):
			t.Fatalf("%s: 等 %s 逾時（stderr=%s）", p.name, want, p.errBuf.String())
		}
	}
}

// awaitAcquiredOrExit：等這個 process 分出勝負——看到 SI-ACQUIRED（拿到鎖）
// 或先退出（被拒）。輸家不會印任何 marker，因為 runApp 在取鎖失敗時根本不會
// 呼叫 uiRunner。
func (p *siProc) awaitAcquiredOrExit(t *testing.T) (acquired bool, code int) {
	t.Helper()
	for {
		select {
		case line, ok := <-p.lines:
			if !ok {
				<-p.exited
				return false, p.code
			}
			if strings.TrimSpace(line) == siMarkerAcquired {
				return true, 0
			}
		case <-p.exited:
			return false, p.code
		case <-time.After(siWatchdog):
			t.Fatalf("%s: 等勝負逾時（stderr=%s）", p.name, p.errBuf.String())
		}
	}
}

func (p *siProc) awaitExit(t *testing.T) int {
	t.Helper()
	select {
	case <-p.exited:
		return p.code
	case <-time.After(siWatchdog):
		t.Fatalf("%s: 等退出逾時（stderr=%s）", p.name, p.errBuf.String())
		return -1
	}
}

// siRunRejected：起一個完整啟動的 process，期待它被拒絕並回傳它的 stderr。
//
// 看到 SI-ACQUIRED 就立刻判失敗，不等它退出：鎖沒守住時那個 process 會一路
// 啟動並停在自己的閘門上，若在這裡等退出，紅的會是 watchdog 逾時而不是「它
// 竟然拿到了鎖」這條正題斷言。
func siRunRejected(t *testing.T, name, ws string) string {
	t.Helper()
	p := siStart(t, name, ws, siModeFull, false)
	acquired, code := p.awaitAcquiredOrExit(t)
	if acquired {
		t.Fatalf("%s: 鎖仍被持有時，第二個 process 不得取得鎖並進入 writer 初始化", name)
	}
	if code != exitAlreadyRunning {
		t.Fatalf("%s: 鎖被持有時必須以 exitAlreadyRunning(%d) 拒絕，實得 %d（stderr=%s）",
			name, exitAlreadyRunning, code, p.errBuf.String())
	}
	return p.errBuf.String()
}

func siWorkspace(t *testing.T) (ws, stateDir string) {
	t.Helper()
	ws, err := claude.NormalizeCWD(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return ws, filepath.Join(ws, ".workbench")
}

// ---- 磁碟快照（守門 3 的 oracle）----

type siEntry struct {
	Dir   bool
	Size  int64
	Mode  fs.FileMode
	MTime time.Time
	Sum   string
}

// siSnapshot：state directory 的遞迴磁碟事實。含目錄 mtime——temp file 建了
// 又刪也會動到父目錄的 mtime，所以「除 lock file 外檔案內容、大小與 temp
// files 均不變」這條連短命的 temp write 都涵蓋。
func siSnapshot(t *testing.T, root string) map[string]siEntry {
	t.Helper()
	out := map[string]siEntry{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		e := siEntry{Dir: d.IsDir(), Size: info.Size(), Mode: info.Mode(), MTime: info.ModTime()}
		if !d.IsDir() {
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			sum := sha256.Sum256(b)
			e.Sum = hex.EncodeToString(sum[:])
		}
		out[rel] = e
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return out
}

func siDiff(before, after map[string]siEntry) []string {
	var diffs []string
	for k, a := range after {
		b, ok := before[k]
		if !ok {
			diffs = append(diffs, "新增："+k)
			continue
		}
		if !reflect.DeepEqual(a, b) {
			diffs = append(diffs, fmt.Sprintf("變更：%s\n  before=%+v\n  after =%+v", k, b, a))
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			diffs = append(diffs, "消失："+k)
		}
	}
	sort.Strings(diffs)
	return diffs
}

// siCountAuditKind：audit.jsonl 中某個 kind 的紀錄筆數。守門 1 的獨立 oracle
// ——每一次成功的 App.startup 恰好寫一筆 "startup"，所以筆數就是「幾個
// process 真的進入了 writer 初始化」，不必相信任何一方的自陳。
func siCountAuditKind(t *testing.T, stateDir, kind string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(stateDir, "audit.jsonl"))
	if err != nil {
		t.Fatalf("讀 audit.jsonl：%v", err)
	}
	return strings.Count(string(b), `"kind":"`+kind+`"`)
}

// ---- 守門 1／2：兩個 process 同時啟動 ----

// TestSingleInstanceRaceExactlyOneWinner
//
// 守門 1（恰好一個進入 writer initialization）：兩個 process 卡在同一個起跑
// 閘門，父行程同時放行。正題斷言有兩條、互相獨立——OS 退出碼一贏一拒，且
// audit.jsonl 的 startup 紀錄**恰好一筆**（磁碟事實，不是自陳）。
//
// 守門 2（輸家收到可辨識拒絕與使用者可見訊息）：exitAlreadyRunning ＋ stderr
// 上「已在執行中」的主文。
func TestSingleInstanceRaceExactlyOneWinner(t *testing.T) {
	ws, stateDir := siWorkspace(t)
	p1 := siStart(t, "p1", ws, siModeFull, true)
	p2 := siStart(t, "p2", ws, siModeFull, true)

	// 起跑閘門：兩邊都已經 exec 完、卡在 stdin 讀取上，寫入即同時放行。
	p1.release(t)
	p2.release(t)

	ok1, code1 := p1.awaitAcquiredOrExit(t)
	ok2, code2 := p2.awaitAcquiredOrExit(t)

	if ok1 == ok2 {
		t.Fatalf("恰好一個 process 必須取得鎖：p1 acquired=%v(exit=%d) p2 acquired=%v(exit=%d)\n"+
			"p1 stderr=%s\np2 stderr=%s", ok1, code1, ok2, code2, p1.errBuf.String(), p2.errBuf.String())
	}
	winner, loser := p1, p2
	loserCode := code2
	if ok2 {
		winner, loser = p2, p1
		loserCode = code1
	}
	if loserCode != exitAlreadyRunning {
		t.Fatalf("輸家必須以 exitAlreadyRunning(%d) 退出，實得 %d（stderr=%s）",
			exitAlreadyRunning, loserCode, loser.errBuf.String())
	}
	if msg := loser.errBuf.String(); !strings.Contains(msg, "已在執行中") {
		t.Fatalf("輸家必須收到使用者看得懂的拒絕訊息，實得：%q", msg)
	}

	winner.awaitMarker(t, siMarkerStarted)
	winner.release(t) // 收尾
	if code := winner.awaitExit(t); code != 0 {
		t.Fatalf("贏家必須正常退出，實得 %d（stderr=%s）", code, winner.errBuf.String())
	}

	// 獨立 oracle：只有一個 process 進入過 writer 初始化。
	if n := siCountAuditKind(t, stateDir, "startup"); n != 1 {
		t.Fatalf("audit.jsonl 必須恰好一筆 startup（＝只有一個 process 開過 writer），實得 %d", n)
	}
}

// ---- 範圍第 2 條：鎖必須早於任何 writer ----

// TestNoWriterOpensBeforeLockIsAcquired
//
// hold 模式的 process 走的是 production runApp，但 uiRunner 在拿到鎖之後就停
// 住、不呼叫 App.startup。所以這一刻 state directory 上應該**只有**
// resolveWorkspace 建的目錄骨架與 instance.lock——任何多出來的東西，都代表
// 有 writer 排在取鎖之前（registry／audit／events sink／replay index／wire
// log／SegmentSet 全都落在這個目錄底下）。
//
// 正題斷言：快照的路徑集合 == {".", "probe", "recordings", "instance.lock"}。
// 這是磁碟事實，不是「有沒有呼叫過什麼」的探針，也不採信 helper 自己印的
// SI-ACQUIRED（鎖被拿掉時那個 marker 照樣會印）。
func TestNoWriterOpensBeforeLockIsAcquired(t *testing.T) {
	ws, stateDir := siWorkspace(t)
	holder := siStart(t, "holder", ws, siModeHold, false)
	holder.awaitMarker(t, siMarkerAcquired)

	snap := siSnapshot(t, stateDir)
	got := make([]string, 0, len(snap))
	for k := range snap {
		got = append(got, k)
	}
	sort.Strings(got)
	want := []string{".", "probe", "recordings", singleinstance.LockFileName}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("取得鎖之前不得開啟任何 writer——state directory 應只有目錄骨架與鎖檔\nwant %v\ngot  %v",
			want, got)
	}

	holder.release(t)
	if code := holder.awaitExit(t); code != 0 {
		t.Fatalf("持鎖方必須正常退出，實得 %d", code)
	}
}

// ---- 守門 3：輸家不得動到磁碟上任何 state ----

// TestRejectedInstanceMutatesNothingOnDisk
//
// 持鎖方用 hold 模式，所以基線是「目錄骨架 ＋ instance.lock」而已（那條基線
// 本身由 TestNoWriterOpensBeforeLockIsAcquired 守）。接著讓一個**完整啟動**
// 的 process 撞上鎖：只要它在被拒之前開過任何 writer，sessions.json／
// events.jsonl／audit.jsonl／replay-index／wire-segments.jsonl／evidence 之類
// 就會出現在後快照裡。
//
// 正題斷言：siDiff(before, after) 為空——含目錄 mtime，所以建了又刪的 temp
// file 也涵蓋。
func TestRejectedInstanceMutatesNothingOnDisk(t *testing.T) {
	ws, stateDir := siWorkspace(t)
	holder := siStart(t, "holder", ws, siModeHold, false)
	holder.awaitMarker(t, siMarkerAcquired)

	before := siSnapshot(t, stateDir)
	_ = siRunRejected(t, "loser", ws)
	after := siSnapshot(t, stateDir)

	if diffs := siDiff(before, after); len(diffs) != 0 {
		t.Fatalf("被拒絕的 process 不得變更 state directory 的任何磁碟事實，實得：\n%s",
			strings.Join(diffs, "\n"))
	}

	holder.release(t)
	if code := holder.awaitExit(t); code != 0 {
		t.Fatalf("持鎖方必須正常退出，實得 %d", code)
	}
}

// ---- 守門 4：第一個還活著時，第三個仍被拒 ----

func TestThirdInstanceStillRejectedWhileFirstAlive(t *testing.T) {
	ws, _ := siWorkspace(t)
	holder := siStart(t, "holder", ws, siModeHold, false)
	holder.awaitMarker(t, siMarkerAcquired)

	for _, name := range []string{"second", "third"} {
		msg := siRunRejected(t, name, ws)
		if !strings.Contains(msg, "已在執行中") {
			t.Fatalf("%s: 拒絕訊息不可辨識：%q", name, msg)
		}
	}

	holder.release(t)
	if code := holder.awaitExit(t); code != 0 {
		t.Fatalf("持鎖方必須正常退出，實得 %d", code)
	}
}

// TestLockHeldUntilWritersClosed
//
// 守門 4 的嚴格版，守的是範圍第 3 條「鎖持有到所有 writer 完成 shutdown 並
// 關閉後才釋放」。第一個 process 停在 shutdown 總序的 snapshot 步驟——manager
// ／sink／registry／wire segments 都還開著——這一刻第三個 process 必須仍被
// 拒絕。放行之後 shutdown 跑完、process 退出，新的 process 才拿得到鎖。
//
// 正題斷言（依序）：暫停期間的 process 被拒 → 收尾後的 process 取得鎖。
func TestLockHeldUntilWritersClosed(t *testing.T) {
	ws, _ := siWorkspace(t)
	first := siStart(t, "first", ws, siModePauseShutdown, false)
	first.awaitMarker(t, siMarkerStarted)

	first.release(t) // 進入 shutdown
	first.awaitMarker(t, siMarkerPaused)

	// writer 尚未關閉的這一刻：鎖必須還在。
	msg := siRunRejected(t, "during-shutdown", ws)
	if !strings.Contains(msg, "已在執行中") {
		t.Fatalf("shutdown 中途的拒絕訊息不可辨識：%q", msg)
	}

	first.release(t) // 讓 shutdown 跑完
	if code := first.awaitExit(t); code != 0 {
		t.Fatalf("first 必須正常退出，實得 %d（stderr=%s）", code, first.errBuf.String())
	}

	// 反面：收尾完成之後鎖確實放掉了（證明上面的拒絕不是「永遠拒絕」）。
	next := siStart(t, "after-shutdown", ws, siModeHold, false)
	next.awaitMarker(t, siMarkerAcquired)
	next.release(t)
	if code := next.awaitExit(t); code != 0 {
		t.Fatalf("after-shutdown 必須正常退出，實得 %d", code)
	}
}

// ---- 守門 5：正常退出／SIGKILL 之後都能重新取得 ----

func TestLockReacquirableAfterNormalExit(t *testing.T) {
	ws, _ := siWorkspace(t)
	first := siStart(t, "first", ws, siModeHold, false)
	first.awaitMarker(t, siMarkerAcquired)
	first.release(t)
	if code := first.awaitExit(t); code != 0 {
		t.Fatalf("first exit=%d", code)
	}

	second := siStart(t, "second", ws, siModeHold, false)
	second.awaitMarker(t, siMarkerAcquired) // 正題斷言：拿得到
	second.release(t)
	if code := second.awaitExit(t); code != 0 {
		t.Fatalf("second exit=%d", code)
	}
}

// TestLockReacquirableAfterSIGKILL：SIGKILL 不跑 defer，Release 永遠不會被呼
// 叫——鎖必須由 kernel 在 process 死亡時釋放。lock file 本身仍留在磁碟上（這
// 正是 crash 之後的樣子），下一個 process 依然要能取得，不需要任何人先去刪它。
func TestLockReacquirableAfterSIGKILL(t *testing.T) {
	ws, stateDir := siWorkspace(t)
	first := siStart(t, "first", ws, siModeHold, false)
	first.awaitMarker(t, siMarkerAcquired)

	if err := first.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("SIGKILL：%v", err)
	}
	if code := first.awaitExit(t); code != -1 {
		t.Fatalf("SIGKILL 的 process 不該是正常退出碼，實得 %d", code)
	}

	lockPath := filepath.Join(stateDir, singleinstance.LockFileName)
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("crash 後 lock file 本來就會留著，測試前提不成立：%v", err)
	}

	second := siStart(t, "second", ws, siModeFull, false)
	second.awaitMarker(t, siMarkerStarted) // 正題斷言：stale lock file 不擋人
	second.release(t)
	if code := second.awaitExit(t); code != 0 {
		t.Fatalf("second exit=%d（stderr=%s）", code, second.errBuf.String())
	}
}

// ---- 範圍第 6 條：鎖取不到一律 fail closed ----

// TestLockOpenFailureFailsClosed
//
// state directory 目錄骨架先建好（讓 resolveWorkspace 不會 fallback 到別的候
// 選），再把 .workbench 設成不可寫：instance.lock 尚未存在，Acquire 的開檔因
// 此以 EACCES 失敗。這種失敗**不是**「目前沒人持鎖」，必須拒絕啟動。
//
// 正題斷言三條：exitLockUnavailable 退出碼、與「已在執行中」分得開的訊息、
// 以及 state directory 的磁碟事實不變（fail open 的話 App 會整個啟動起來，
// 雖然那些寫入多半也會因權限失敗，但快照與退出碼都會不同）。
func TestLockOpenFailureFailsClosed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 會繞過目錄權限檢查，這條量不出來")
	}
	ws, stateDir := siWorkspace(t)
	// 先建出 resolveWorkspace 會建的骨架，讓它在唯讀目錄下仍選中這個 workspace。
	for _, d := range []string{"recordings", "probe"} {
		if err := os.MkdirAll(filepath.Join(stateDir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(stateDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(stateDir, 0o755) })

	before := siSnapshot(t, stateDir)
	p := siStart(t, "no-lock", ws, siModeFull, false)
	acquired, code := p.awaitAcquiredOrExit(t)
	if acquired {
		t.Fatal("鎖取不到時不得繼續啟動（fail closed）")
	}
	if code != exitLockUnavailable {
		t.Fatalf("必須以 exitLockUnavailable(%d) 拒絕，實得 %d（stderr=%s）",
			exitLockUnavailable, code, p.errBuf.String())
	}
	msg := p.errBuf.String()
	if !strings.Contains(msg, "無法取得單一實例鎖") || strings.Contains(msg, "已在執行中") {
		t.Fatalf("鎖取不到與已在執行中必須是分得開的訊息，實得：%q", msg)
	}
	if diffs := siDiff(before, siSnapshot(t, stateDir)); len(diffs) != 0 {
		t.Fatalf("鎖取不到時不得動任何 state，實得：\n%s", strings.Join(diffs, "\n"))
	}
}

// ---- mutation 4 的守門：鎖必須綁在 state directory ----

// TestLockIsScopedToStateDirectory
//
// 兩條正題斷言分別打不同的走鐘方式：
//   - 鎖檔路徑必須是 <workspace>/.workbench/instance.lock；鎖到 workspace 根
//     目錄（或別的層）會在這裡紅。
//   - 兩個**不同** workspace 的實例必須都能啟動；鎖到某個全域固定路徑
//     （tmp／home）時第二個會被拒，也在這裡紅。
func TestLockIsScopedToStateDirectory(t *testing.T) {
	wsA, stateA := siWorkspace(t)
	wsB, stateB := siWorkspace(t)

	a := siStart(t, "wsA", wsA, siModeHold, false)
	a.awaitMarker(t, siMarkerAcquired)
	b := siStart(t, "wsB", wsB, siModeHold, false)
	b.awaitMarker(t, siMarkerAcquired) // 不同 workspace 不得互相排斥

	for _, c := range []struct{ ws, state string }{{wsA, stateA}, {wsB, stateB}} {
		if _, err := os.Stat(filepath.Join(c.state, singleinstance.LockFileName)); err != nil {
			t.Fatalf("鎖檔必須落在 %s：%v", c.state, err)
		}
		if _, err := os.Stat(filepath.Join(c.ws, singleinstance.LockFileName)); err == nil {
			t.Fatalf("鎖檔不得落在 workspace 根目錄 %s", c.ws)
		}
	}

	a.release(t)
	b.release(t)
	if code := a.awaitExit(t); code != 0 {
		t.Fatalf("wsA exit=%d", code)
	}
	if code := b.awaitExit(t); code != 0 {
		t.Fatalf("wsB exit=%d", code)
	}
}

// ---- 拒絕 UX 的純函式部分 ----

func TestAlreadyRunningMessageIsActionable(t *testing.T) {
	msg := alreadyRunningMessage("/tmp/ws/.workbench")
	for _, want := range []string{"已在執行中", "沒有讀取或寫入任何 session 狀態", "/tmp/ws/.workbench"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("拒絕訊息缺少 %q：%s", want, msg)
		}
	}
}

func TestLockUnavailableMessageIsDistinct(t *testing.T) {
	msg := lockUnavailableMessage("/tmp/ws/.workbench", fmt.Errorf("permission denied"))
	if strings.Contains(msg, "已在執行中") {
		t.Fatalf("鎖取不到與已在執行中必須分得開：%s", msg)
	}
	if !strings.Contains(msg, "permission denied") {
		t.Fatalf("原因必須 fail loud：%s", msg)
	}
}

// TestBundledExecutableDetection：對話框只在 .app bundle 啟動時彈——這個判準
// 必須讓 go test 產出的執行檔落在 false 那一側，否則 barrier 測試會被
// osascript 的 modal 卡住。
func TestBundledExecutableDetection(t *testing.T) {
	if !bundledExecutable("/Applications/sdlc-workbench.app/Contents/MacOS/sdlc-workbench") {
		t.Fatal("bundle 內的執行檔必須判為 true")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if bundledExecutable(exe) {
		t.Fatalf("test binary 不得被判為 bundle 啟動：%s", exe)
	}
}

func TestAppleScriptStringEscapes(t *testing.T) {
	got := appleScriptString("a\"b\nc")
	if strings.Contains(got, "\n") {
		t.Fatalf("換行必須換成 AppleScript 的 return：%q", got)
	}
	if !strings.Contains(got, `\"`) {
		t.Fatalf("雙引號必須跳脫：%q", got)
	}
}
