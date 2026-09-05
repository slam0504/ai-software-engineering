//go:build diag_orphan

// B2c 暫時性診斷測試——不得合併進 main，B2c 結案後刪除。
//
// 目的：對 internal/claude/TestOrphanDoesNotHangNormalExit 在 CI 上的 5 秒逾時取證，
// 以套件內既有 seam（exitedCh／onSignal／Done）直接量 supervisor 收尾窗口，區分：
//
//	(b1) cleanup KILL 已成功送往正確群組但程序未及時消失（唯一支持 production 收尾缺陷）
//	(b2) 本輪孫程序不在預期 PGID（fixture／PGID 契約問題）
//	(c)  孫程序已消失但 stdout EOF 延遲
//	(a′) 最後輸出至 supervisor 記錄退出的延遲——只是 (a) cmd.Wait 延遲的代理，不確認也不排除 (a)
//
// 歸因統計由 plan 的 Python 解析對 iter-records.jsonl 逐輪套用；本檔只忠實記錄原始時序與快照。
//
// 契約（plan rev5）：
//   - 單一 stdout reader，同一 goroutine 記 tLastLine 與 tEOF。
//   - tExited 當下建立絕對 deadline D1 = tExited+10s；ps 快照 goroutine 與 EOF 等待並行。
//   - EOF 到達後的 Done() 等待有界（5s）。任一逾時 → rescue 判定 → guard 2（5s）。
//   - rescue 前先取當下 ps：本輪孫程序（DIAG_PIDFILE 的 PID，命令列含 token）或其 sleep 子程序
//     仍在且 pgid==p.PGID() 才對群組 SIGKILL（mode=group）；pgid 不符只對已驗明 PID 個別 kill
//     （mode=targeted-pid）；找不到即 skipped-no-target，不送任何訊號。
//   - 未收斂的 iteration 不呼叫 p.Wait()，交背景；測試結束前整體最多等 30s，逾時記 pending。
//   - record 於彙報完成後每輪只序列化一次到 iter-records.jsonl；逐輪另寫 partial 檔保底。
package proc

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type diagPsRow struct {
	PGID, PID, PPID int
	Stat, Etime     string
	Command         string
}

type diagPsSummary struct {
	Offset            string `json:"offset"`
	At                int64  `json:"atNs"`
	GroupMembers      int    `json:"groupMembers"`
	OrphanPresent     bool   `json:"orphanPresent"`
	OrphanPgid        int    `json:"orphanPgid"`
	OrphanPgidMatches bool   `json:"orphanPgidMatches"`
	OrphanZombie      bool   `json:"orphanZombie"`
	SleepChildPresent bool   `json:"sleepChildPresent"`
	SleepChildPgid    int    `json:"sleepChildPgid"`
	File              string `json:"file"`
	Err               string `json:"err,omitempty"`
}

type diagRescue struct {
	Mode     string            `json:"mode"` // none | group | targeted-pid | skipped-no-target
	Err      string            `json:"err,omitempty"`
	AtNs     int64             `json:"atNs,omitempty"`
	PerPID   map[string]string `json:"perPid,omitempty"`
	PsBefore *diagPsSummary    `json:"psBefore,omitempty"`
	PsAfter  *diagPsSummary    `json:"psAfter,omitempty"`
}

type diagRecord struct {
	Iter                int             `json:"iter"`
	Runner              string          `json:"runner"`
	GOOS                string          `json:"goos"`
	Token               string          `json:"token"`
	OrphanPid           int             `json:"orphanPid"`
	PGID                int             `json:"pgid"`
	StartToExitedMs     *float64        `json:"startToExitedMs"`
	LastLineToExitedMs  *float64        `json:"lastLineToExitedMs"`
	ExitedToCleanupKill *float64        `json:"exitedToCleanupKillMs"`
	ExitedToEOFMs       *float64        `json:"exitedToEOFMs"`
	ExitedToDoneMs      *float64        `json:"exitedToDoneMs"`
	EOFTimeout          bool            `json:"eofTimeout"`
	DoneTimeout         bool            `json:"doneTimeout"`
	AfterRescue         bool            `json:"afterRescue"`
	Converged           bool            `json:"converged"`
	FinalStatus         string          `json:"finalConverged"` // 彙報後填入：converged | finalConverged | pending
	FinalWaitMs         *float64        `json:"finalWaitMs,omitempty"`
	Anomaly             bool            `json:"anomaly"`
	Snapshots           []diagPsSummary `json:"ps"`
	Rescue              diagRescue      `json:"rescue"`
	NewPidsAfterIter    int             `json:"newPidsAfterIter"`
	ExitCode            *int            `json:"exitCode,omitempty"`
	Note                string          `json:"note,omitempty"`
}

func diagMs(d time.Duration) *float64 { v := float64(d.Microseconds()) / 1000.0; return &v }

func diagPs(path string) ([]diagPsRow, error) {
	out, err := exec.Command("ps", "-eo", "pgid,pid,ppid,stat,etime,command").CombinedOutput()
	if path != "" {
		_ = os.WriteFile(path, out, 0o644)
	}
	if err != nil {
		return nil, err
	}
	var rows []diagPsRow
	for i, line := range strings.Split(string(out), "\n") {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 6 {
			continue
		}
		pgid, e1 := strconv.Atoi(f[0])
		pid, e2 := strconv.Atoi(f[1])
		ppid, e3 := strconv.Atoi(f[2])
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		rows = append(rows, diagPsRow{PGID: pgid, PID: pid, PPID: ppid, Stat: f[3], Etime: f[4], Command: strings.Join(f[5:], " ")})
	}
	return rows, nil
}

func diagSummarize(rows []diagPsRow, groupPgid, orphanPid int, token string) diagPsSummary {
	s := diagPsSummary{}
	for _, r := range rows {
		if r.PGID == groupPgid {
			s.GroupMembers++
		}
		if orphanPid > 0 && r.PID == orphanPid && strings.Contains(r.Command, token) {
			s.OrphanPresent = true
			s.OrphanPgid = r.PGID
			s.OrphanPgidMatches = r.PGID == groupPgid
			s.OrphanZombie = strings.HasPrefix(r.Stat, "Z")
		}
		if orphanPid > 0 && r.PPID == orphanPid && strings.Contains(r.Command, "sleep") {
			s.SleepChildPresent = true
			s.SleepChildPgid = r.PGID
		}
	}
	return s
}

func diagReadPid(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return n
}

func diagToken(i int) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("iter-%d-%s", i, hex.EncodeToString(b))
}

// TestDiagOrphanTimeline 只做取證，不以時間作為通過條件；任何 iteration 的異常都記錄而不 t.Fatal。
func TestDiagOrphanTimeline(t *testing.T) {
	fixture, err := filepath.Abs("testdata/diag-orphan.sh")
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(fixture); err != nil || fi.Mode()&0o111 == 0 {
		t.Fatalf("fixture missing or not executable: %v", err)
	}
	n := 100
	if v := os.Getenv("DIAG_ITER"); v != "" {
		if k, err := strconv.Atoi(v); err == nil && k > 0 {
			n = k
		}
	}
	outDir := os.Getenv("DIAG_OUT")
	if outDir == "" {
		outDir = t.TempDir()
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := os.Getenv("DIAG_RUNNER")
	if runner == "" {
		runner = "local"
	}
	t.Logf("diag: iterations=%d outDir=%s runner=%s goos=%s", n, outDir, runner, runtime.GOOS)

	records := make([]*diagRecord, 0, n)
	type bgWait struct {
		rec  *diagRecord
		done chan time.Time
	}
	var bg []bgWait
	offsets := []time.Duration{0, 50 * time.Millisecond, 200 * time.Millisecond, time.Second, 5 * time.Second}

	for i := 0; i < n; i++ {
		rec := &diagRecord{Iter: i, Runner: runner, GOOS: runtime.GOOS, FinalStatus: "pending"}
		records = append(records, rec)
		token := diagToken(i)
		rec.Token = token
		pidfile := filepath.Join(outDir, fmt.Sprintf("iter-%d-orphan.pid", i))
		before, _ := diagPs(filepath.Join(outDir, fmt.Sprintf("iter-%d-before.txt", i)))
		beforePids := map[int]bool{}
		for _, r := range before {
			beforePids[r.PID] = true
		}

		ctx := context.Background()
		p, err := Start(ctx, Config{Binary: fixture, Env: []string{"DIAG_TOKEN=" + token, "DIAG_PIDFILE=" + pidfile}, TermGrace: 200 * time.Millisecond})
		if err != nil {
			rec.Note = "start failed: " + err.Error()
			continue
		}
		rec.PGID = p.PGID()
		t0 := time.Now()

		// seam：Start 返回後、送 prompt 前設定；leader 收到 prompt 前不會退出，事件不會漏。
		killCh := make(chan time.Time, 1)
		p.onSignal = func(ev signalEvent) {
			if ev == sigEventSupervisorCleanupKill {
				select {
				case killCh <- time.Now():
				default:
				}
			}
		}

		// 單一 stdout reader：同一 goroutine 記 tLastLine 與 tEOF；兩個時刻以 mutex 保護，
		// 主 goroutine 只透過 getters 讀取（race detector 要求明確的 happens-before）。
		var tsMu sync.Mutex
		var tLastLine, tEOF time.Time
		getLastLine := func() time.Time { tsMu.Lock(); defer tsMu.Unlock(); return tLastLine }
		getEOF := func() time.Time { tsMu.Lock(); defer tsMu.Unlock(); return tEOF }
		eofCh := make(chan struct{})
		go func() {
			sc := bufio.NewScanner(p.Stdout)
			sc.Buffer(make([]byte, 64*1024), 1024*1024)
			for sc.Scan() {
				if strings.Contains(sc.Text(), `"type":"result"`) {
					tsMu.Lock()
					tLastLine = time.Now()
					tsMu.Unlock()
				}
			}
			tsMu.Lock()
			tEOF = time.Now()
			tsMu.Unlock()
			close(eofCh)
		}()
		_, _ = p.Stdin.Write([]byte("x\n"))
		_ = p.Stdin.Close()

		<-p.exitedCh
		tExited := time.Now() // 觀察時刻：主 goroutine 自 exitedCh 醒來；supervisor 的 cleanup KILL 事件可能早它數微秒，故 exitedToCleanupKillMs 可為極小負值。
		d1 := tExited.Add(10 * time.Second)
		orphanPid := diagReadPid(pidfile)
		rec.OrphanPid = orphanPid
		rec.StartToExitedMs = diagMs(tExited.Sub(t0))
		if ll := getLastLine(); !ll.IsZero() {
			rec.LastLineToExitedMs = diagMs(tExited.Sub(ll))
		}

		// 快照 goroutine：與 EOF 等待並行。
		var snapMu sync.Mutex
		var snapWg sync.WaitGroup
		snapWg.Add(1)
		go func() {
			defer snapWg.Done()
			for _, off := range offsets {
				time.Sleep(time.Until(tExited.Add(off)))
				file := filepath.Join(outDir, fmt.Sprintf("iter-%d-ps-%s.txt", i, off.String()))
				rows, perr := diagPs(file)
				sum := diagSummarize(rows, p.PGID(), orphanPid, token)
				sum.Offset = off.String()
				sum.At = time.Now().UnixNano()
				sum.File = filepath.Base(file)
				if perr != nil {
					sum.Err = perr.Error()
				}
				snapMu.Lock()
				rec.Snapshots = append(rec.Snapshots, sum)
				snapMu.Unlock()
			}
		}()

		// guard 1：EOF 以 D1 為絕對 deadline。
		var tDone time.Time
		select {
		case <-eofCh:
		case <-time.After(time.Until(d1)):
			rec.EOFTimeout = true
		}
		if !rec.EOFTimeout {
			select {
			case <-p.Done():
				tDone = time.Now()
			case <-time.After(5 * time.Second):
				rec.DoneTimeout = true
			}
		}
		select {
		case tk := <-killCh:
			rec.ExitedToCleanupKill = diagMs(tk.Sub(tExited))
		default:
		}

		needRescue := rec.EOFTimeout || rec.DoneTimeout
		if !needRescue {
			// anomaly：EOF 已到但 5s 快照仍見本輪孫程序。
			snapWg.Wait()
			for _, s := range rec.Snapshots {
				if s.Offset == "5s" && (s.OrphanPresent || s.SleepChildPresent) {
					rec.Anomaly = true
					needRescue = true
				}
			}
		}
		rec.Rescue.Mode = "none"
		if needRescue {
			rows, _ := diagPs(filepath.Join(outDir, fmt.Sprintf("iter-%d-rescue-before.txt", i)))
			sb := diagSummarize(rows, p.PGID(), orphanPid, token)
			sb.Offset = "rescue-before"
			sb.At = time.Now().UnixNano()
			rec.Rescue.PsBefore = &sb
			switch {
			case (sb.OrphanPresent && sb.OrphanPgidMatches) || (sb.SleepChildPresent && sb.SleepChildPgid == p.PGID()):
				rec.Rescue.Mode = "group"
				rec.Rescue.AtNs = time.Now().UnixNano()
				if err := p.SignalGroup(syscall.SIGKILL); err != nil {
					rec.Rescue.Err = err.Error()
				}
			case sb.OrphanPresent || sb.SleepChildPresent:
				rec.Rescue.Mode = "targeted-pid"
				rec.Rescue.AtNs = time.Now().UnixNano()
				// 先固定 psBefore 中已驗明的 PID 清單，再逐一送訊號並記錄每個結果。
				targets := []int{}
				for _, r := range rows {
					if (r.PID == orphanPid && strings.Contains(r.Command, token)) || (r.PPID == orphanPid && strings.Contains(r.Command, "sleep")) {
						targets = append(targets, r.PID)
					}
				}
				rec.Rescue.PerPID = map[string]string{}
				for _, pid := range targets {
					if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
						rec.Rescue.PerPID[strconv.Itoa(pid)] = err.Error()
					} else {
						rec.Rescue.PerPID[strconv.Itoa(pid)] = "ok"
					}
				}
			default:
				rec.Rescue.Mode = "skipped-no-target"
			}
			rows2, _ := diagPs(filepath.Join(outDir, fmt.Sprintf("iter-%d-rescue-after.txt", i)))
			sa := diagSummarize(rows2, p.PGID(), orphanPid, token)
			sa.Offset = "rescue-after"
			sa.At = time.Now().UnixNano()
			rec.Rescue.PsAfter = &sa
			// guard 2：5s 內並行等 EOF 與 Done。
			g2 := time.After(5 * time.Second)
			if rec.EOFTimeout {
				select {
				case <-eofCh:
					rec.AfterRescue = true
				case <-g2:
				}
			}
			if !getEOF().IsZero() || !rec.EOFTimeout {
				select {
				case <-p.Done():
					if tDone.IsZero() {
						tDone = time.Now()
						rec.AfterRescue = true
					}
				case <-g2:
				}
			}
		}
		snapWg.Wait()

		eofAt := getEOF()
		if !eofAt.IsZero() {
			rec.ExitedToEOFMs = diagMs(eofAt.Sub(tExited))
		}
		if !tDone.IsZero() {
			rec.ExitedToDoneMs = diagMs(tDone.Sub(tExited))
		}
		rec.Converged = !eofAt.IsZero() && !tDone.IsZero()
		if rec.Converged {
			ex := p.Wait()
			code := ex.Code
			rec.ExitCode = &code
			rec.FinalStatus = "converged"
		} else {
			ch := make(chan time.Time, 1)
			go func(pp *Proc) { pp.Wait(); ch <- time.Now() }(p)
			bg = append(bg, bgWait{rec: rec, done: ch})
		}

		after, _ := diagPs(filepath.Join(outDir, fmt.Sprintf("iter-%d-after.txt", i)))
		for _, r := range after {
			if !beforePids[r.PID] {
				rec.NewPidsAfterIter++
			}
		}
		if b, err := json.Marshal(rec); err == nil {
			_ = os.WriteFile(filepath.Join(outDir, fmt.Sprintf("iter-%d-partial.json", i)), b, 0o644)
		}
		if rec.EOFTimeout || rec.DoneTimeout || rec.Anomaly || !rec.Converged {
			t.Logf("iter %d: eofTimeout=%v doneTimeout=%v anomaly=%v converged=%v rescue=%s", i, rec.EOFTimeout, rec.DoneTimeout, rec.Anomaly, rec.Converged, rec.Rescue.Mode)
		}
	}

	// 彙報階段：整體最多 30s。
	deadline := time.Now().Add(30 * time.Second)
	for _, w := range bg {
		start := time.Now()
		select {
		case at := <-w.done:
			w.rec.FinalStatus = "finalConverged"
			w.rec.FinalWaitMs = diagMs(at.Sub(start))
		case <-time.After(time.Until(deadline)):
			w.rec.FinalStatus = "pending"
		}
	}

	// 每輪只序列化一次。
	f, err := os.Create(filepath.Join(outDir, "iter-records.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	enc := json.NewEncoder(f)
	counts := map[string]int{}
	for _, rec := range records {
		_ = enc.Encode(rec)
		counts[rec.FinalStatus]++
		if rec.EOFTimeout {
			counts["eofTimeout"]++
		}
		if rec.DoneTimeout {
			counts["doneTimeout"]++
		}
		if rec.Rescue.Mode != "none" {
			counts["rescue:"+rec.Rescue.Mode]++
		}
		if rec.ExitedToCleanupKill != nil {
			counts["cleanupKillObserved"]++
		}
	}
	_ = f.Close()
	if b, err := json.MarshalIndent(map[string]any{"iterations": n, "runner": runner, "goos": runtime.GOOS, "counts": counts}, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(outDir, "summary.json"), b, 0o644)
	}
	t.Logf("diag summary: %v", counts)
}
