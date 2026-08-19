package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
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

// ---- single-instance ownership lease：跨 process 守門 ----
//
// 為什麼一定要 spawn 真的 OS process：flock(2) 的持有者是 kernel 裡的 open
// file description，而「crash／SIGKILL 之後鎖自然釋放」只有 kernel 在 process
// 死亡時才會做。同一個 process 裡放兩個 App 實例假裝多開，量到的是別的東西
// ——這是失效形狀 (E)（測試環境本身量不出那個效果）與 (F)（跨 process 那一維
// 沒守）。
//
// **入口形狀（owner 2026-08-18 裁決）**：資料正確性的守門一律走 `bare` 模式
// ——`NewApp()` → `App.startup` → `App.shutdown`，**完全不經 runInstance**。
// 那正是 `func main() { runWailsUI(NewApp()) }` 這種「繞過編排」的入口形狀；
// ownership lease 已經下沉到 App.startup，所以這條路徑必須自己守得住。
// runInstance 只負責拒絕 UX（第二個實例連視窗都不開），由 `entry` 模式守。
//
// oracle 一律獨立於受測對象：
//   - 「誰進入了 writer initialization」→ `audit.jsonl` 裡 `"kind":"startup"`
//     的**筆數**（每次成功的 startupAfterWriters 恰好寫一筆）；
//   - 「輸家沒動任何 state」→ state directory 的遞迴磁碟快照；
//   - 「鎖還被持有／已經釋放」→ 另一個真 process（或同 process 的新 fd）實際
//     Acquire 的結果。
//
// 同步一律用 pipe barrier（父行程寫一行才放行 / 關 pipe 才收尾），沒有任何
// time.Sleep。程式碼裡的 time.After 只有一個用途：卡死時的 watchdog。

const (
	// siHelperEnv：把這個 test binary 的 re-exec 進入點從一般 go test（直接
	// return）切成 helper 模式。
	siHelperEnv = "WB_SINGLE_INSTANCE_HELPER"
	// siBarrierEnv："1" = 起跑前先等父行程放行一行（起跑閘門）。
	siBarrierEnv = "WB_SINGLE_INSTANCE_BARRIER"
)

const (
	// siModeHold：走 runInstance 取得 lease 後阻塞，**不呼叫 App.startup**。
	// state directory 因此停在「目錄骨架 ＋ lock file」的 pristine 基線——守門
	// 「取鎖之前不得開任何 writer」與守門 3 的量測基準。
	siModeHold = "hold"
	// siModeEntry：走 runInstance ＋完整生命週期。守拒絕 UX（退出碼、訊息）。
	siModeEntry = "entry"
	// siModeBare：**不經 runInstance** 的 NewApp()+startup/shutdown。
	siModeBare = "bare"
	// siModeBarePause：同 bare，但停在 shutdown 總序的 siPauseStep。
	siModeBarePause = "bare-pause-shutdown"
	// siModeBareRecheck：同 bare，shutdown 之後在**同一個 process** 重新
	// Acquire——flock 是 per open file description，所以這是「shutdown 真的把
	// lease 放掉了」的獨立探針（不是讀 App 的欄位）。
	siModeBareRecheck = "bare-recheck"
)

// siPauseEnv：helper 停在 shutdown 總序的哪一個節點（由父行程指定）。
//
// **必須參數化，不能寫死一個早期節點**（owner 2026-08-19）：只停在 snapshot 時，
// 「把 lease.release() 從總序最後一步搬到 manager_close 之前」這種提早釋放的
// mutation 整包 root package 仍然全綠——暫停點在它前面，跨 process 的觀測窗根本
// 還沒到那一刻（失效形狀 (D)）。所以守門要在**早期與晚期各一個節點**各跑一次。
const siPauseEnv = "SI_PAUSE_STEP"

const (
	// siPauseEarly：所有 writer 都還沒關（在 manager_close／index_flush_close／
	// registry_sync 之前）。守「lease 撐到 writer 開著的時候」。
	siPauseEarly = "snapshot"
	// siPauseLate：§3.6.5 總序中最後一個落盤步驟。守「lease 沒有在落盤與釋放
	// 之間被提早放掉」。
	siPauseLate = "registry_sync"
)

const (
	siMarkerAcquired = "SI-ACQUIRED" // runInstance 取得 lease（hold／entry）
	siMarkerStarted  = "SI-STARTED"  // App.startup 回來（**不代表成功**）
	siMarkerPaused   = "SI-SHUTDOWN-PAUSED"
	siMarkerReleased = "SI-RELEASED"   // shutdown 後同 process 重新 Acquire 成功
	siMarkerHeld     = "SI-STILL-HELD" // 同上但失敗（＝ shutdown 沒放掉）
	siPrefixBlocker  = "SI-BLOCKER:"   // startup 產生的 UI 橫幅內容
)

// siWatchdog：卡死時的失敗上限，不是同步手段。
const siWatchdog = 90 * time.Second

// siStdin：helper 的閘門來源。父行程寫一行＝放行；關 pipe（EOF）也放行。
var siStdin = bufio.NewReader(os.Stdin)

func siAwait() { _, _ = siStdin.ReadString('\n') }

func siSay(line string) { fmt.Println(line) }

// TestHelperProcessSingleInstance 是這個 test binary 的 re-exec 進入點：一般
// go test 直接 return，帶 siHelperEnv 執行時改跑 helper 模式。
func TestHelperProcessSingleInstance(t *testing.T) {
	mode := os.Getenv(siHelperEnv)
	if mode == "" {
		return
	}
	if os.Getenv(siBarrierEnv) == "1" {
		siAwait()
	}
	os.Exit(siHelperRun(mode))
}

func siHelperRun(mode string) int {
	switch mode {
	case siModeHold:
		return runInstance(NewApp(), func(*App) error {
			siSay(siMarkerAcquired)
			siAwait()
			return nil
		}, os.Stderr)
	case siModeEntry:
		return runInstance(NewApp(), func(app *App) error {
			siSay(siMarkerAcquired)
			siLifecycle(app, mode)
			return nil
		}, os.Stderr)
	}
	// bare 家族：owner 指定的「繞過編排」入口形狀。刻意不呼叫 runInstance——
	// 這裡沒有任何取鎖程式碼，全靠 App.startup 自己守。
	app := NewApp()
	siLifecycle(app, mode)
	if mode == siModeBareRecheck {
		if l, err := singleinstance.Acquire(app.stateDir); err == nil {
			_ = l.Release()
			siSay(siMarkerReleased)
		} else {
			siSay(siMarkerHeld)
		}
	}
	return 0
}

func siLifecycle(app *App, mode string) {
	app.emitUI = func(string, any) {} // 不碰 wails runtime
	if mode == siModeBarePause {
		want := os.Getenv(siPauseEnv)
		if want == "" {
			want = siPauseEarly
		}
		app.hookShutdownStep = func(step string) {
			if step == want {
				siSay(siMarkerPaused)
				siAwait()
			}
		}
	}
	ctx := context.Background()
	app.startup(ctx)
	if app.startupErr != "" {
		siSay(siPrefixBlocker + strings.ReplaceAll(app.startupErr, "\n", " "))
	}
	siSay(siMarkerStarted) // 只代表 startup 回來了
	siAwait()
	app.shutdown(ctx)
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
	seen   []string
	exited chan struct{}
	code   int
	errBuf *siBuf
}

// siStart 起一個 helper process。刻意用 os.Pipe 而不是 cmd.StdoutPipe：後者
// 要求「所有讀取都完成後才能 Wait」，而這裡必須能在還在讀 marker 的同時等待
// process 退出。
func siStart(t *testing.T, name, ws, mode string, barrier bool, extraEnv ...string) *siProc {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcessSingleInstance$")
	cmd.Env = append(os.Environ(), siHelperEnv+"="+mode, "WORKBENCH_WORKSPACE="+ws)
	cmd.Env = append(cmd.Env, extraEnv...)
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

	p := &siProc{name: name, cmd: cmd, stdin: inW, errBuf: errBuf,
		lines: make(chan string, 32), exited: make(chan struct{})}
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

// siExitCode：ExitCode() 對被訊號終止的 process 回 -1，SIGKILL 守門靠這個值
// 與正常退出分得開。
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

func (p *siProc) release(t *testing.T) {
	t.Helper()
	if _, err := io.WriteString(p.stdin, "go\n"); err != nil {
		t.Fatalf("%s: 放行失敗：%v（stderr=%s）", p.name, err, p.errBuf.String())
	}
}

// awaitMarker 讀到 want（前綴比對）為止，途中的行都留在 p.seen 供事後斷言。
func (p *siProc) awaitMarker(t *testing.T, want string) string {
	t.Helper()
	for {
		select {
		case line, ok := <-p.lines:
			if !ok {
				<-p.exited
				t.Fatalf("%s: stdout 收束仍未看到 %s（exit=%d seen=%v stderr=%s）",
					p.name, want, p.code, p.seen, p.errBuf.String())
			}
			line = strings.TrimSpace(line)
			p.seen = append(p.seen, line)
			if strings.HasPrefix(line, want) {
				return line
			}
		case <-time.After(siWatchdog):
			t.Fatalf("%s: 等 %s 逾時（seen=%v stderr=%s）", p.name, want, p.seen, p.errBuf.String())
		}
	}
}

// awaitAcquiredOrExit：等 runInstance 模式的 process 分出勝負——看到
// SI-ACQUIRED（取得 lease）或先退出（被拒）。
func (p *siProc) awaitAcquiredOrExit(t *testing.T) (acquired bool, code int) {
	t.Helper()
	for {
		select {
		case line, ok := <-p.lines:
			if !ok {
				<-p.exited
				return false, p.code
			}
			line = strings.TrimSpace(line)
			p.seen = append(p.seen, line)
			if line == siMarkerAcquired {
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
		t.Fatalf("%s: 等退出逾時（seen=%v stderr=%s）", p.name, p.seen, p.errBuf.String())
		return -1
	}
}

func (p *siProc) blockerLine() string {
	for _, l := range p.seen {
		if strings.HasPrefix(l, siPrefixBlocker) {
			return strings.TrimPrefix(l, siPrefixBlocker)
		}
	}
	return ""
}

// siRunBlockedBare：起一個 **bare 入口**（不經 runInstance）的 process，期待
// 它被 App.startup 的 ownership lease 檢查擋下來。
//
// 回傳它的 UI 橫幅內容。橫幅是受測對象自陳，**不作為唯一 oracle**——呼叫端
// 一律另外用 audit startup 筆數或磁碟快照判定它有沒有進入 writer init。
func siRunBlockedBare(t *testing.T, name, ws string) string {
	t.Helper()
	p := siStart(t, name, ws, siModeBare, false)
	p.awaitMarker(t, siMarkerStarted)
	p.release(t)
	if code := p.awaitExit(t); code != 0 {
		t.Fatalf("%s: bare 入口被擋下時仍應乾淨結束，實得 exit=%d（stderr=%s）",
			name, code, p.errBuf.String())
	}
	return p.blockerLine()
}

// siRunRejectedEntry：起一個 **runInstance 入口**的 process，期待拒絕 UX。
func siRunRejectedEntry(t *testing.T, name, ws string, wantCode int) string {
	t.Helper()
	p := siStart(t, name, ws, siModeEntry, false)
	acquired, code := p.awaitAcquiredOrExit(t)
	if acquired {
		t.Fatalf("%s: 鎖仍被持有時，第二個 process 不得取得 lease", name)
	}
	if code != wantCode {
		t.Fatalf("%s: 必須以退出碼 %d 拒絕，實得 %d（stderr=%s）",
			name, wantCode, code, p.errBuf.String())
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

// ---- 磁碟快照 oracle ----

type siEntry struct {
	Dir   bool
	Size  int64
	Mode  fs.FileMode
	MTime time.Time
	Sum   string
}

// siSnapshot：state directory 的遞迴磁碟事實。
//
// **已知邊界**：含目錄 mtime，所以「建了又刪的 temp file」在 APFS（ns 級
// timestamp）上量得到；但在 1 秒粒度的檔案系統（HFS+、部分網路 fs）上，若
// before 快照與 temp write 落在同一秒，這個訊號會靜默消失。路徑集合、size 與
// 內容雜湊不受此限。
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

// siCountAuditKind：audit.jsonl 中某個 kind 的筆數。
//
// 守門「誰進入了 writer initialization」的獨立 oracle——`"kind":"startup"` 由
// startupAfterWriters 寫，而那一段只有 openStateWriters 回 true 才走得到。
// 檔案不存在＝零筆（lease 被擋下時 audit.jsonl 根本不會被建立）。
func siCountAuditKind(t *testing.T, stateDir, kind string) int {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(stateDir, "audit.jsonl"))
	if errors.Is(err, fs.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("讀 audit.jsonl：%v", err)
	}
	return strings.Count(string(b), `"kind":"`+kind+`"`)
}

func siInode(t *testing.T, path string) uint64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("這個平台取不到 inode")
	}
	return uint64(st.Ino)
}

// ---- 守門 1：兩個 process 同時啟動，恰好一個進入 writer initialization ----

// TestBareEntryRaceExactlyOneEntersWriterInit
//
// 兩個 process 都走 **bare 入口**（`NewApp()` → `App.startup`，完全不經
// runInstance＝owner 指定的 `runWailsUI(NewApp())` 形狀），卡在同一個起跑閘門
// 後同時放行。
//
// 正題斷言：`audit.jsonl` 的 `"kind":"startup"` **恰好一筆**。這是磁碟事實，
// 不看任何一方自陳的 marker——鎖被拿掉時兩邊都會照樣印 SI-STARTED。
// 附帶斷言：恰好一邊看到「已在執行中」的 UI 橫幅。
func TestBareEntryRaceExactlyOneEntersWriterInit(t *testing.T) {
	ws, stateDir := siWorkspace(t)
	p1 := siStart(t, "p1", ws, siModeBare, true)
	p2 := siStart(t, "p2", ws, siModeBare, true)

	// 起跑閘門：兩邊都已 exec 完、卡在 stdin 讀取上，寫入即同時放行。
	p1.release(t)
	p2.release(t)

	p1.awaitMarker(t, siMarkerStarted)
	p2.awaitMarker(t, siMarkerStarted)

	if n := siCountAuditKind(t, stateDir, "startup"); n != 1 {
		t.Fatalf("恰好一個 process 可以進入 writer initialization：audit.jsonl 的 startup 筆數應為 1，實得 %d\n"+
			"p1 seen=%v\np2 seen=%v", n, p1.seen, p2.seen)
	}
	blocked := 0
	for _, p := range []*siProc{p1, p2} {
		if strings.Contains(p.blockerLine(), "已在執行中") {
			blocked++
		}
	}
	if blocked != 1 {
		t.Fatalf("恰好一邊要看到「已在執行中」的 UI 橫幅，實得 %d\np1 seen=%v\np2 seen=%v",
			blocked, p1.seen, p2.seen)
	}

	for _, p := range []*siProc{p1, p2} {
		p.release(t)
		if code := p.awaitExit(t); code != 0 {
			t.Fatalf("%s 必須乾淨結束，實得 %d（stderr=%s）", p.name, code, p.errBuf.String())
		}
	}
}

// ---- 守門 2：拒絕 UX（runInstance 入口）----

// TestEntryRejectionUX：第二個實例連視窗都不開——runInstance 在建立視窗之前
// 就拒絕並終止 process。
//
// 正題斷言：退出碼 exitAlreadyRunning ＋ stderr 上「已在執行中」的主文。
// 退出碼由 runInstance 內部的 os.Exit 發出（不是交還給 main），所以
// `func main() { runWailsUI(NewApp()) }` 這種丟掉回傳值的寫法也不會弄丟它。
func TestEntryRejectionUX(t *testing.T) {
	ws, stateDir := siWorkspace(t)
	holder := siStart(t, "holder", ws, siModeHold, false)
	holder.awaitMarker(t, siMarkerAcquired)

	msg := siRunRejectedEntry(t, "second", ws, exitAlreadyRunning)
	for _, want := range []string{"已在執行中", "沒有讀取或寫入任何 session 狀態", stateDir} {
		if !strings.Contains(msg, want) {
			t.Fatalf("拒絕訊息缺少 %q：%s", want, msg)
		}
	}
	// 獨立佐證：被拒的一方沒有進入 writer initialization。
	if n := siCountAuditKind(t, stateDir, "startup"); n != 0 {
		t.Fatalf("被拒的 process 不得寫出 startup 稽核，實得 %d 筆", n)
	}

	holder.release(t)
	if code := holder.awaitExit(t); code != 0 {
		t.Fatalf("持鎖方必須正常退出，實得 %d", code)
	}
}

// ---- 範圍第 2 條：lease 必須早於任何 writer ----

// TestNoWriterOpensBeforeLeaseIsAcquired
//
// hold 模式的 process 取得 lease 之後就停住、不呼叫 App.startup。所以這一刻
// state directory 上應該**只有** resolveWorkspace 建的目錄骨架與 instance.lock
// ——任何多出來的東西都代表有 writer 排在取 lease 之前。
//
// 正題斷言：快照路徑集合 == {".", "probe", "recordings", "instance.lock"}。
func TestNoWriterOpensBeforeLeaseIsAcquired(t *testing.T) {
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
		t.Fatalf("取得 lease 之前不得開啟任何 writer——state directory 應只有目錄骨架與鎖檔\nwant %v\ngot  %v",
			want, got)
	}

	holder.release(t)
	if code := holder.awaitExit(t); code != 0 {
		t.Fatalf("持鎖方必須正常退出，實得 %d", code)
	}
}

// ---- 守門 3：被擋下的 process 不得動到磁碟上任何 state ----

// TestBlockedBareEntryMutatesNothingOnDisk
//
// 持鎖方用 hold 模式，基線是「目錄骨架 ＋ instance.lock」。接著讓一個 **bare
// 入口**的完整啟動撞上 lease：只要它在被擋下之前開過任何 writer 或跑過任何
// migration，sessions.json／events.jsonl／audit.jsonl／replay-index／
// wire-segments.jsonl／evidence 之類就會出現在後快照裡。
//
// 正題斷言：siDiff(before, after) 為空。
func TestBlockedBareEntryMutatesNothingOnDisk(t *testing.T) {
	ws, stateDir := siWorkspace(t)
	holder := siStart(t, "holder", ws, siModeHold, false)
	holder.awaitMarker(t, siMarkerAcquired)

	before := siSnapshot(t, stateDir)
	blocker := siRunBlockedBare(t, "loser", ws)
	after := siSnapshot(t, stateDir)

	if diffs := siDiff(before, after); len(diffs) != 0 {
		t.Fatalf("被擋下的 process 不得變更 state directory 的任何磁碟事實，實得：\n%s",
			strings.Join(diffs, "\n"))
	}
	if !strings.Contains(blocker, "已在執行中") {
		t.Fatalf("被擋下時 UI 橫幅必須說明原因，實得：%q", blocker)
	}

	holder.release(t)
	if code := holder.awaitExit(t); code != 0 {
		t.Fatalf("持鎖方必須正常退出，實得 %d", code)
	}
}

// ---- 守門 4：第一個未結束前，第三個仍被擋 ----

func TestThirdInstanceStillBlockedWhileFirstAlive(t *testing.T) {
	ws, stateDir := siWorkspace(t)
	holder := siStart(t, "holder", ws, siModeHold, false)
	holder.awaitMarker(t, siMarkerAcquired)

	for _, name := range []string{"second", "third"} {
		if blocker := siRunBlockedBare(t, name, ws); !strings.Contains(blocker, "已在執行中") {
			t.Fatalf("%s: 橫幅不可辨識：%q", name, blocker)
		}
	}
	if n := siCountAuditKind(t, stateDir, "startup"); n != 0 {
		t.Fatalf("被擋下的 process 不得進入 writer initialization，audit startup 筆數應為 0，實得 %d", n)
	}

	holder.release(t)
	if code := holder.awaitExit(t); code != 0 {
		t.Fatalf("持鎖方必須正常退出，實得 %d", code)
	}
}

// TestLeaseHeldUntilWritersClosed
//
// 範圍第 3 條：lease 持有到所有 writer 完成 shutdown 並關閉後才釋放。第一個
// process 停在 shutdown 總序的 snapshot 步驟——manager／sink／registry／wire
// segments 都還開著——這一刻第三個真 process 必須仍被擋下。
//
// 正題斷言（依序）：暫停期間的 process 沒有進入 writer init（audit startup
// 筆數不變）＋磁碟快照不變 → 收尾後的 process 拿得到 lease。
func TestLeaseHeldUntilWritersClosed(t *testing.T) {
	// 早期與晚期各跑一次（見 siPauseEnv 的 doc）：只停在 snapshot 時，把
	// lease.release() 搬到 manager_close 之前的 mutation 觀測不到。
	for _, step := range []string{siPauseEarly, siPauseLate} {
		t.Run(step, func(t *testing.T) { leaseHeldUntilWritersClosed(t, step) })
	}
}

func leaseHeldUntilWritersClosed(t *testing.T, pauseStep string) {
	ws, stateDir := siWorkspace(t)
	first := siStart(t, "first", ws, siModeBarePause, false, siPauseEnv+"="+pauseStep)
	first.awaitMarker(t, siMarkerStarted)

	first.release(t) // 進入 shutdown
	first.awaitMarker(t, siMarkerPaused)

	// 暫停在 pauseStep 的這一刻：lease 必須還在。
	beforeCount := siCountAuditKind(t, stateDir, "startup")
	before := siSnapshot(t, stateDir)
	blocker := siRunBlockedBare(t, "during-shutdown", ws)
	if n := siCountAuditKind(t, stateDir, "startup"); n != beforeCount {
		t.Fatalf("shutdown 停在 %s 時，第二個 process 不得進入 writer initialization：startup 筆數 %d → %d",
			pauseStep, beforeCount, n)
	}
	if diffs := siDiff(before, siSnapshot(t, stateDir)); len(diffs) != 0 {
		t.Fatalf("shutdown 停在 %s 時，第二個 process 不得變更磁碟事實：\n%s",
			pauseStep, strings.Join(diffs, "\n"))
	}
	if !strings.Contains(blocker, "已在執行中") {
		t.Fatalf("shutdown 中途的橫幅不可辨識：%q", blocker)
	}

	first.release(t) // 讓 shutdown 跑完
	if code := first.awaitExit(t); code != 0 {
		t.Fatalf("first 必須正常退出，實得 %d（stderr=%s）", code, first.errBuf.String())
	}

	// 反面：收尾完成之後 lease 確實放掉了（證明上面的擋不是「永遠擋」）。
	next := siStart(t, "after-shutdown", ws, siModeHold, false)
	next.awaitMarker(t, siMarkerAcquired)
	next.release(t)
	if code := next.awaitExit(t); code != 0 {
		t.Fatalf("after-shutdown 必須正常退出，實得 %d", code)
	}
}

// TestShutdownReleasesLeaseInSameProcess
//
// 「shutdown 之後 lease 真的放掉了」的獨立探針：flock 是 per open file
// description，所以**同一個 process** 在 shutdown 之後重新 Acquire，成功與否
// 完全由 kernel 決定，跟 App 自己記了什麼無關（process 退出會順便釋放，那條
// 路徑證明不了 shutdown 有沒有做事）。
//
// 正題斷言：helper 印出 SI-RELEASED 而不是 SI-STILL-HELD。
func TestShutdownReleasesLeaseInSameProcess(t *testing.T) {
	ws, _ := siWorkspace(t)
	p := siStart(t, "recheck", ws, siModeBareRecheck, false)
	p.awaitMarker(t, siMarkerStarted)
	p.release(t)
	line := p.awaitMarker(t, "SI-")
	for line != siMarkerReleased && line != siMarkerHeld {
		line = p.awaitMarker(t, "SI-")
	}
	if line != siMarkerReleased {
		t.Fatalf("shutdown 之後同一個 process 必須能重新取得 lease（＝已釋放），實得 %s", line)
	}
	if code := p.awaitExit(t); code != 0 {
		t.Fatalf("exit=%d（stderr=%s）", code, p.errBuf.String())
	}
}

// TestLeaseReleasedWhenStartupFailsMidway
//
// startup 中途失敗（events sink 開檔失敗——events.jsonl 先被做成目錄）時，
// lease 已經取得，shutdown 仍必須正確釋放它。
//
// 正題斷言：helper 回報 startup 有錯（fail loud）**且**印出 SI-RELEASED。
func TestLeaseReleasedWhenStartupFailsMidway(t *testing.T) {
	ws, stateDir := siWorkspace(t)
	// 讓 events sink 的 OpenFile 必然失敗，但 lease 已經先取得。
	if err := os.MkdirAll(filepath.Join(stateDir, "events.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := siStart(t, "midway-fail", ws, siModeBareRecheck, false)
	p.awaitMarker(t, siMarkerStarted)
	if b := p.blockerLine(); !strings.Contains(b, "event sink init failed") {
		t.Fatalf("startup 中途失敗必須 fail loud，實得橫幅：%q", b)
	}
	p.release(t)
	line := p.awaitMarker(t, "SI-")
	for line != siMarkerReleased && line != siMarkerHeld {
		line = p.awaitMarker(t, "SI-")
	}
	if line != siMarkerReleased {
		t.Fatalf("startup 中途失敗後 shutdown 仍必須釋放 lease，實得 %s", line)
	}
	_ = p.awaitExit(t)
}

// ---- 守門 5：正常退出／SIGKILL 之後都能重新取得 ----

func TestLeaseReacquirableAfterNormalExit(t *testing.T) {
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

// TestLeaseReacquirableAfterSIGKILL：SIGKILL 不跑 defer，Release 永遠不會被
// 呼叫——鎖必須由 kernel 在 process 死亡時釋放。lock file 本身仍留在磁碟上
// （crash 之後的樣子），下一個 process 依然要能取得，不需要任何人先刪它。
func TestLeaseReacquirableAfterSIGKILL(t *testing.T) {
	ws, stateDir := siWorkspace(t)
	first := siStart(t, "first", ws, siModeBare, false)
	first.awaitMarker(t, siMarkerStarted)

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

	before := siCountAuditKind(t, stateDir, "startup")
	second := siStart(t, "second", ws, siModeBare, false)
	second.awaitMarker(t, siMarkerStarted)
	// 正題斷言：stale lock file 不擋人——第二個 process 真的進入了 writer init。
	if n := siCountAuditKind(t, stateDir, "startup"); n != before+1 {
		t.Fatalf("SIGKILL 之後新 process 必須能進入 writer initialization：startup 筆數 %d → %d",
			before, n)
	}
	second.release(t)
	if code := second.awaitExit(t); code != 0 {
		t.Fatalf("second exit=%d（stderr=%s）", code, second.errBuf.String())
	}
}

// TestLockFileInodeStableAcrossReacquire
//
// Release 若改成 unlink，跨 process 的實際後果是「兩個 process 在不同 inode
// 上各拿一把鎖」——那個後果本身難以穩定重現，但**成因**可以直接量：正常收尾
// 之後重新取得的必須是同一個 inode。unlink 會讓下一次 Acquire 建出新檔案。
//
// 正題斷言：兩次持有期間 lock file 的 inode 相同。
//
// 兩邊都用 **bare** 模式而不是 hold：hold 不呼叫 App.shutdown，`Release()` 根
// 本不會被執行，unlink mutation 也就打不紅（第一版就踩到這個，改掉）。
func TestLockFileInodeStableAcrossReacquire(t *testing.T) {
	ws, stateDir := siWorkspace(t)
	lockPath := filepath.Join(stateDir, singleinstance.LockFileName)

	first := siStart(t, "first", ws, siModeBare, false)
	first.awaitMarker(t, siMarkerStarted)
	ino1 := siInode(t, lockPath)
	first.release(t) // → App.shutdown → lease.release()
	if code := first.awaitExit(t); code != 0 {
		t.Fatalf("first exit=%d", code)
	}

	second := siStart(t, "second", ws, siModeBare, false)
	second.awaitMarker(t, siMarkerStarted)
	ino2 := siInode(t, lockPath)
	if ino1 != ino2 {
		t.Fatalf("Release 不得 unlink lock file：inode %d → %d（新 inode 代表下一個 process 會鎖到不同的檔案）",
			ino1, ino2)
	}
	second.release(t)
	_ = second.awaitExit(t)
}

// ---- 範圍第 6 條：lease 取不到一律 fail closed ----

// TestLeaseOpenFailureFailsClosed
//
// state directory 骨架先建好（讓 resolveWorkspace 不會 fallback 到別的候選），
// 再把 .workbench 設成不可寫：instance.lock 尚未存在，Acquire 的開檔因此以
// EACCES 失敗。這種失敗**不是**「目前沒人持鎖」，必須拒絕。
//
// 正題斷言：bare 入口不得進入 writer init（磁碟快照不變、橫幅說明原因）；
// runInstance 入口以 exitLockUnavailable 拒絕且訊息與「已在執行中」分得開。
func TestLeaseOpenFailureFailsClosed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 會繞過目錄權限檢查，這條量不出來")
	}
	ws, stateDir := siWorkspace(t)
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
	blocker := siRunBlockedBare(t, "bare-no-lock", ws)
	if diffs := siDiff(before, siSnapshot(t, stateDir)); len(diffs) != 0 {
		t.Fatalf("lease 取不到時不得動任何 state，實得：\n%s", strings.Join(diffs, "\n"))
	}
	if !strings.Contains(blocker, "無法取得 workspace 的單一實例鎖") || strings.Contains(blocker, "已在執行中") {
		t.Fatalf("取不到鎖與已在執行中必須分得開，實得橫幅：%q", blocker)
	}

	msg := siRunRejectedEntry(t, "entry-no-lock", ws, exitLockUnavailable)
	if !strings.Contains(msg, "無法取得單一實例鎖") || strings.Contains(msg, "已在執行中") {
		t.Fatalf("拒絕訊息必須分得開，實得：%q", msg)
	}
}

// ---- lease 必須綁在 state directory ----

// TestLeaseIsScopedToStateDirectory
//
// 兩條正題斷言打不同的走鐘方式：
//   - 鎖檔路徑必須是 <workspace>/.workbench/instance.lock；鎖到 workspace 根
//     目錄（或別層）會在這裡紅。
//   - 兩個**不同** workspace 的實例必須都能啟動；鎖到全域固定路徑（tmp／home）
//     時第二個會被擋，也在這裡紅。
func TestLeaseIsScopedToStateDirectory(t *testing.T) {
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

// ---- lease capability 的不可偽造性（同 process，純型別語意）----

// TestForgedLeaseIsRejected：別的地方造得出 &stateLease{}，但那個零值必須過不
// 了 ownsStateDir——「沒有 lease 就是拒絕」而不是「沒設就當作沒問題」。
func TestForgedLeaseIsRejected(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name  string
		lease *stateLease
	}{
		{"nil", nil},
		{"零值", &stateLease{}},
		{"只有目錄、沒有授權來源", &stateLease{stateDir: dir}},
		{"偽造的 Lock 零值", &stateLease{stateDir: dir, lock: &singleinstance.Lock{}}},
	}
	for _, c := range cases {
		if c.lease.ownsStateDir(dir) {
			t.Fatalf("%s：不可偽造的 lease 檢查被繞過", c.name)
		}
	}
	// 對照組：test-only capability 有效，但只對它自己的目錄有效。
	l := newTestStateLease(dir)
	if !l.ownsStateDir(dir) {
		t.Fatal("test-only lease 必須對自己的 state directory 有效")
	}
	if l.ownsStateDir(filepath.Join(dir, "other")) {
		t.Fatal("lease 不得授權別的 state directory")
	}
}

// ---- test-only capability 的原始碼掃描（型別解析版）----
//
// owner 2026-08-18 範圍：「test-only capability 不得存在於 production 建構路徑，
// 也不能讓 production caller 自行偽造」。
//
// 這條**刻意**是原始碼掃描，因為要守的性質本身就是原始碼層級的——「production
// binary 裡沒有任何地方能把 testOnly 打開」。runtime 量不到「不存在的呼叫」；
// 同 package 的 struct field 在 Go 裡也沒有語言層的可見性可用。與此對照，
// 跨 process 的行為守門（bare 入口、拒絕 UX）一律用真 process ＋ 磁碟證據。
//
// # 為什麼從 go/ast 再進一步到 go/types（owner 2026-08-19）
//
// 這個守門被繞過兩次，每次都是「掃描比它宣稱的弱」（失效形狀 (C)）：
//
//	初版（字串比對）→ 位置式 literal `&stateLease{dir, nil, true}` 不含 `testOnly:`。
//	第二版（純語法）→ 只認 `x.Type` 是識別字 `stateLease` 的 literal，於是
//	                  (1) 省略型別的巢狀 literal `[]stateLease{{…, true}}`（x.Type 為 nil）
//	                  (2) type alias `type l = stateLease` 之後的 `l{testOnly: true}`
//	                  兩種寫法都掃不到。
//
// 語法有無限多種寫法，型別只有一個。所以判定改成「這個 composite literal 的
// **型別**是不是 stateLease」——由 go/types 回答，alias 自動解開、省略型別的巢狀
// literal 也由上下文推得出型別。欄位序不再硬編，改由型別本身查出來。

// forbiddenFieldWrite：掃描結果的一筆（檔案位置 ＋ 原因）。
type forbiddenFieldWrite struct {
	pos token.Position
	why string
}

// scanFieldWrites：在已型別檢查的 files 裡找出所有「寫入 target 型別的 field 欄位」
// 的地方——composite literal（keyed／位置式／省略型別的巢狀）與直接賦值都算。
//
// target 是型別物件而不是名字：alias 與同名的別種型別因此天然分得開。
func scanFieldWrites(fset *token.FileSet, files []*ast.File, info *types.Info,
	target *types.Named, field string) []forbiddenFieldWrite {
	st, ok := target.Underlying().(*types.Struct)
	if !ok {
		return nil
	}
	fieldIdx := -1
	for i := range st.NumFields() {
		if st.Field(i).Name() == field {
			fieldIdx = i
		}
	}
	if fieldIdx < 0 {
		return nil
	}
	// litType：composite literal 的具名型別（`&T{}`／`[]*T{{…}}` 的元素都會落到
	// 這裡，指標先解開）。省略型別的巢狀 literal 沒有 x.Type，型別由 info 提供。
	//
	// **types.Unalias 是必要的，不是保險**：Go 1.23 起 `type l = T` 在型別系統裡
	// 是獨立的 *types.Alias 節點，不解開的話 alias 那條繞過路徑照樣掃不到——本檔
	// 的 fixture 測試就是這樣抓到第一版修法仍然漏掉它的。
	litType := func(x *ast.CompositeLit) *types.Named {
		tv, found := info.Types[x]
		if !found {
			return nil
		}
		typ := types.Unalias(tv.Type)
		if p, isPtr := typ.(*types.Pointer); isPtr {
			typ = types.Unalias(p.Elem())
		}
		n, isNamed := typ.(*types.Named)
		if !isNamed {
			return nil
		}
		return n
	}
	var out []forbiddenFieldWrite
	note := func(pos token.Pos, why string) {
		out = append(out, forbiddenFieldWrite{pos: fset.Position(pos), why: why})
	}
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CompositeLit:
				if lt := litType(x); lt == nil || lt.Obj() != target.Obj() {
					return true
				}
				for i, el := range x.Elts {
					if kv, keyed := el.(*ast.KeyValueExpr); keyed {
						if k, _ := kv.Key.(*ast.Ident); k != nil && k.Name == field {
							note(kv.Pos(), target.Obj().Name()+" composite literal 設定 "+field+"（keyed）")
						}
						continue
					}
					if i == fieldIdx {
						note(el.Pos(), target.Obj().Name()+" composite literal 設定 "+field+"（位置式）")
					}
				}
			case *ast.AssignStmt:
				// `x.field = ...`（含 := 與各種複合賦值）。用 Selections 確認選到的
				// 真的是 target 的那個欄位，不是別的型別上剛好同名的欄位。
				for _, lhs := range x.Lhs {
					sel, isSel := lhs.(*ast.SelectorExpr)
					if !isSel || sel.Sel.Name != field {
						continue
					}
					s := info.Selections[sel]
					if s == nil || s.Obj() != st.Field(fieldIdx) {
						continue
					}
					note(sel.Pos(), "對 "+target.Obj().Name()+"."+field+" 賦值")
				}
			}
			return true
		})
	}
	return out
}

// typeCheckProductionPackage：對 **production package main**（所有非 `_test.go`
// 的 .go 檔）做完整型別檢查。
//
// 依賴的型別資訊來自 `go list -deps -export` 產生的 gc export data——那份在
// `go test` 建置本 package 時已經生成，所以這裡只是查快取（實測約 1 秒），不會
// 重新編譯整棵相依樹。刻意不用 source importer：那會從原始碼重新檢查 wails 等
// 大型相依，慢上兩個數量級。
func typeCheckProductionPackage(t *testing.T) (*token.FileSet, []*ast.File, *types.Info, *types.Package) {
	t.Helper()
	out, err := exec.Command("go", "list", "-deps", "-export", "-json", ".").Output()
	if err != nil {
		t.Fatalf("go list -deps -export：%v", err)
	}
	exports := map[string]string{}
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p struct{ ImportPath, Export string }
		if derr := dec.Decode(&p); derr != nil {
			break
		}
		if p.Export != "" {
			exports[p.ImportPath] = p.Export
		}
	}
	ents, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if perr != nil {
			t.Fatalf("parse %s：%v", name, perr)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		t.Fatal("掃描前提不成立：找不到任何 production .go 檔")
	}
	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	conf := types.Config{Importer: importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		p, found := exports[path]
		if !found {
			return nil, fmt.Errorf("找不到 %s 的 export data", path)
		}
		return os.Open(p)
	})}
	// 型別檢查必須乾淨通過：帶著錯誤的 info 會有一堆 Invalid 型別，掃描就從
	// 「型別解析」退化成「碰巧掃到的那些」——那正是這條守門兩次被繞過的形狀。
	pkg, cerr := conf.Check("github.com/slam0504/sdlc-workbench", fset, files, info)
	if cerr != nil {
		t.Fatalf("production package 型別檢查失敗，掃描結果不可信：%v", cerr)
	}
	return fset, files, info, pkg
}

// namedType：從 package scope 取出具名型別（找不到即測試前提不成立）。
func namedType(t *testing.T, pkg *types.Package, name string) *types.Named {
	t.Helper()
	obj := pkg.Scope().Lookup(name)
	if obj == nil {
		t.Fatalf("掃描前提不成立：package %s 裡找不到型別 %s", pkg.Name(), name)
	}
	n, ok := obj.Type().(*types.Named)
	if !ok {
		t.Fatalf("%s 不是具名型別（實得 %T）", name, obj.Type())
	}
	return n
}

// TestTestOnlyCapabilityHasNoProductionConstructionPath
//
// 正題斷言：production package main 裡沒有任何地方**寫入** stateLease.testOnly
// ——不論用哪種語法。
func TestTestOnlyCapabilityHasNoProductionConstructionPath(t *testing.T) {
	fset, files, info, pkg := typeCheckProductionPackage(t)
	found := scanFieldWrites(fset, files, info, namedType(t, pkg, "stateLease"), "testOnly")
	if len(found) == 0 {
		return
	}
	lines := make([]string, 0, len(found))
	for _, o := range found {
		lines = append(lines, fmt.Sprintf("%s:%d: %s", filepath.Base(o.pos.Filename), o.pos.Line, o.why))
	}
	t.Fatalf("test-only ownership capability 不得有 production 建構路徑，實得：\n%s",
		strings.Join(lines, "\n"))
}

// TestFieldWriteScanCatchesKnownBypasses
//
// 掃描本身的防回歸測試：把 reviewer 用來繞過前一版掃描的**兩個寫法**餵給它，
// 必須全部被抓到。沒有這一條的話，「掃描綠燈」只證明掃描沒掃到，不證明沒有洞。
//
// 用合成 package 而不是在 production 檔裡放違規程式碼：後者會讓正題那條測試
// 永遠是紅的。合成 package 不 import 任何東西，所以型別檢查不需要 export data。
func TestFieldWriteScanCatchesKnownBypasses(t *testing.T) {
	const src = `package fixture

type stateLease struct {
	stateDir string
	lock     *int
	testOnly bool
}

// 繞過案例 1：type alias——語法上的型別名字不是 "stateLease"。
type leaseAlias = stateLease

// 繞過案例 2：省略型別的巢狀 literal——內層 literal 根本沒有 Type 節點。
var viaSlice = []stateLease{{"/d", nil, true}}
var viaMap = map[string]*stateLease{"k": {testOnly: true}}
var viaAlias = leaseAlias{testOnly: true}

// 既有兩種寫法（不得回歸）。
var keyed = stateLease{stateDir: "/d", testOnly: true}
var positional = &stateLease{"/d", nil, true}

func assigns() {
	var l stateLease
	l.testOnly = true
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fixture.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	pkg, err := (&types.Config{}).Check("fixture", fset, []*ast.File{f}, info)
	if err != nil {
		t.Fatalf("fixture 型別檢查失敗：%v", err)
	}
	files := []*ast.File{f}
	target := namedType(t, pkg, "stateLease")

	got := map[int]string{}
	for _, o := range scanFieldWrites(fset, files, info, target, "testOnly") {
		got[o.pos.Line] = o.why
	}
	// 每一個 want 是「該行必須被抓到」——行號寫死是刻意的，這樣改壞掃描時失敗訊息
	// 直接指出是哪一種寫法漏了。
	wants := []struct {
		line int
		what string
	}{
		{13, "省略型別的巢狀 literal（slice、位置式）"},
		{14, "省略型別的巢狀 literal（map、指標元素、keyed）"},
		{15, "type alias"},
		{18, "keyed literal"},
		{19, "位置式 literal"},
		{23, "直接賦值"},
	}
	for _, w := range wants {
		if _, ok := got[w.line]; !ok {
			t.Errorf("掃描漏掉「%s」（fixture.go:%d），實得：%v", w.what, w.line, got)
		}
	}
	// 反向：沒有寫入 testOnly 的 package 必須是乾淨的（掃描不是無條件打紅）。
	const clean = `package fixture

type stateLease struct {
	stateDir string
	testOnly bool
}

var ok1 = stateLease{stateDir: "/d"}

func reads() bool {
	l := stateLease{}
	return l.testOnly
}
`
	cfset := token.NewFileSet()
	cf, err := parser.ParseFile(cfset, "clean.go", clean, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	cinfo := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	cpkg, err := (&types.Config{}).Check("fixture", cfset, []*ast.File{cf}, cinfo)
	if err != nil {
		t.Fatal(err)
	}
	if n := scanFieldWrites(cfset, []*ast.File{cf}, cinfo, namedType(t, cpkg, "stateLease"), "testOnly"); len(n) != 0 {
		t.Fatalf("沒有寫入 testOnly 的 package 不得被判違規，實得 %v", n)
	}
}

// TestReleasedLeaseIsRejected：釋放過的 lease 不能再拿來授權寫入。
func TestReleasedLeaseIsRejected(t *testing.T) {
	dir := t.TempDir()
	lock, err := singleinstance.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	l := &stateLease{stateDir: dir, lock: lock}
	if !l.ownsStateDir(dir) {
		t.Fatal("剛取得的 lease 必須有效")
	}
	if err := l.release(); err != nil {
		t.Fatal(err)
	}
	if l.ownsStateDir(dir) {
		t.Fatal("釋放之後的 lease 必須失效")
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
