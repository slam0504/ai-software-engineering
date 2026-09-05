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
// 歸因統計由 plan 的解析器對 iter-records.jsonl 逐輪套用；本檔只忠實記錄原始時序與快照。
//
// 契約（plan rev6 之後、owner 第二輪 P1）：
//   - 所有等待皆有界：exitedCh（DIAG_EXITED_TIMEOUT，預設 10s）、EOF（D1=tExited+10s）、Done（5s）、guard 2（單一絕對 deadline 迴圈，5s）。
//   - onSignal 依既有白箱測試的鎖定契約於 p.mu 內寫入。
//   - 單一 stdout reader，同一 goroutine 記 tLastLine 與 tEOF（mutex 保護）。
//   - 兩階段身分：先以 DIAG_PIDFILE 的 PID＋命令列含 token 驗明父 bash，再接受同一份快照中 ppid==父 PID 且 PID==DIAG_CHILDPIDFILE 的 sleep。
//   - rescue 只對已驗明目標：父與子皆在 p.PGID() → group；否則只對已驗明 PID 個別 kill → targeted-pid；無目標 → skipped-no-target。
//   - 證據完整性：任何 artifact 寫入／編碼／Start／stdin／scanner 錯誤都累積到 invalidEvidence，最後讓測試非零結束。
//   - 強制控制（只用於本機驗證分支，CI 不設）：DIAG_MODE=hang（exitedTimeout 路徑）、DIAG_MODE=escape（eofTimeout→targeted-pid→guard 2）、
//     DIAG_FORCE_EOF_TIMEOUT=1（跳過 guard 1 等待→skipped-no-target 路徑）。
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
	OrphanPresent     bool   `json:"orphanPresent"` // 父 bash：PID==DIAG_PIDFILE 且命令列含 token
	OrphanPgid        int    `json:"orphanPgid"`
	OrphanPgidMatches bool   `json:"orphanPgidMatches"`
	OrphanZombie      bool   `json:"orphanZombie"`
	ChildPresent      bool   `json:"childPresent"` // sleep：PID==DIAG_CHILDPIDFILE 且 ppid==已驗明父 PID
	ChildPgid         int    `json:"childPgid"`
	ChildPgidMatches  bool   `json:"childPgidMatches"`
	LeaderPresent     bool   `json:"leaderPresent"` // leader（pid==p.pgid）仍在：hang 模式／exitedTimeout 診斷用
	File              string `json:"file"`
	Err               string `json:"err,omitempty"`
}

type diagRescue struct {
	Mode     string            `json:"mode"` // none | group | targeted-pid | skipped-no-target
	Reason   string            `json:"reason,omitempty"`
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
	Mode                string          `json:"mode"`
	Token               string          `json:"token"`
	OrphanPid           int             `json:"orphanPid"`
	ChildPid            int             `json:"childPid"`
	PGID                int             `json:"pgid"`
	StartToExitedMs     *float64        `json:"startToExitedMs"`
	LastLineToExitedMs  *float64        `json:"lastLineToExitedMs"`
	ExitedToCleanupKill *float64        `json:"exitedToCleanupKillMs"`
	ExitedToEOFMs       *float64        `json:"exitedToEOFMs"`
	ExitedToDoneMs      *float64        `json:"exitedToDoneMs"`
	ExitedTimeout       bool            `json:"exitedTimeout"`
	EOFTimeout          bool            `json:"eofTimeout"`
	DoneTimeout         bool            `json:"doneTimeout"`
	ForcedEOFTimeout    bool            `json:"forcedEofTimeout"`
	AfterRescue         bool            `json:"afterRescue"`
	Converged           bool            `json:"converged"`
	FinalStatus         string          `json:"finalConverged"` // 彙報後填入：converged | finalConverged | pending | aborted
	FinalWaitMs         *float64        `json:"finalWaitMs,omitempty"`
	Anomaly             bool            `json:"anomaly"`
	Snapshots           []diagPsSummary `json:"ps"`
	Rescue              diagRescue      `json:"rescue"`
	NewPidsAfterIter    int             `json:"newPidsAfterIter"`
	ExitCode            *int            `json:"exitCode,omitempty"`
	Errors              []string        `json:"errors,omitempty"`
}

func diagMs(d time.Duration) *float64 { v := float64(d.Microseconds()) / 1000.0; return &v }

func diagPs(path string) ([]diagPsRow, error) {
	out, err := exec.Command("ps", "-eo", "pgid,pid,ppid,stat,etime,command").CombinedOutput()
	if path != "" {
		if werr := os.WriteFile(path, out, 0o644); werr != nil {
			return nil, fmt.Errorf("write %s: %w", path, werr)
		}
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

// diagIdentity 兩階段身分：先驗父（PID＋token），再驗子（PID＋ppid==父）。回傳已驗明的 rows。
func diagIdentity(rows []diagPsRow, orphanPid, childPid int, token string) (parent, child *diagPsRow) {
	for i := range rows {
		r := &rows[i]
		if orphanPid > 0 && r.PID == orphanPid && strings.Contains(r.Command, token) {
			parent = r
			break
		}
	}
	if parent != nil {
		for i := range rows {
			r := &rows[i]
			if childPid > 0 && r.PID == childPid && r.PPID == parent.PID && strings.Contains(r.Command, "sleep") {
				child = r
				break
			}
		}
	}
	return parent, child
}

func diagSummarize(rows []diagPsRow, groupPgid, orphanPid, childPid int, token string) diagPsSummary {
	s := diagPsSummary{}
	for _, r := range rows {
		if r.PGID == groupPgid {
			s.GroupMembers++
		}
		if r.PID == groupPgid {
			s.LeaderPresent = true
		}
	}
	parent, child := diagIdentity(rows, orphanPid, childPid, token)
	if parent != nil {
		s.OrphanPresent = true
		s.OrphanPgid = parent.PGID
		s.OrphanPgidMatches = parent.PGID == groupPgid
		s.OrphanZombie = strings.HasPrefix(parent.Stat, "Z")
	}
	if child != nil {
		s.ChildPresent = true
		s.ChildPgid = child.PGID
		s.ChildPgidMatches = child.PGID == groupPgid
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

func diagEnvDur(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

// diagWaitConverge：單一絕對 deadline timer；同時追蹤 EOF 與 Done。已觀察到的 channel 設為 nil（已關閉的
// channel 永遠 ready，不設 nil 會讓迴圈空轉），因此每個 channel 最多被選中一次；回傳 select 次數供 helper control 檢查不空轉。
func diagWaitConverge(deadline time.Time, eofCh <-chan struct{}, doneCh <-chan struct{}, eofSeen, doneSeen bool) (bool, bool, time.Time, time.Time, int) {
	var tEOFSeen, tDoneSeen time.Time
	if eofSeen {
		eofCh = nil
	}
	if doneSeen {
		doneCh = nil
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	loops := 0
	for eofCh != nil || doneCh != nil {
		loops++
		select {
		case <-eofCh:
			eofSeen = true
			tEOFSeen = time.Now()
			eofCh = nil
		case <-doneCh:
			doneSeen = true
			tDoneSeen = time.Now()
			doneCh = nil
		case <-timer.C:
			return eofSeen, doneSeen, tEOFSeen, tDoneSeen, loops
		}
	}
	return eofSeen, doneSeen, tEOFSeen, tDoneSeen, loops
}

// TestDiagWaitConvergeDoneBeforeEOF：helper control——Done 立即關閉、EOF 永不到達，必須在 deadline 準時回傳
// (eof=false, done=true)，且不空轉（select 次數 ≤ 2：一次選中 done、一次選中 timer）。
func TestDiagWaitConvergeDoneBeforeEOF(t *testing.T) {
	eofCh := make(chan struct{})
	doneCh := make(chan struct{})
	close(doneCh)
	deadline := time.Now().Add(300 * time.Millisecond)
	start := time.Now()
	eof, done, _, tD, loops := diagWaitConverge(deadline, eofCh, doneCh, false, false)
	elapsed := time.Since(start)
	if eof || !done || tD.IsZero() {
		t.Fatalf("expected eof=false done=true, got eof=%v done=%v", eof, done)
	}
	if loops > 2 {
		t.Fatalf("busy loop detected: %d selects", loops)
	}
	if elapsed < 250*time.Millisecond || elapsed > 600*time.Millisecond {
		t.Fatalf("did not return at deadline: elapsed=%s", elapsed)
	}
	// 對照：兩者皆已 seen 時不得阻塞。
	e2, d2, _, _, l2 := diagWaitConverge(time.Now().Add(time.Second), eofCh, doneCh, true, true)
	if !(e2 && d2) || l2 != 0 {
		t.Fatalf("pre-seen path wrong: eof=%v done=%v loops=%d", e2, d2, l2)
	}
}

// TestDiagOrphanTimeline 只做取證，不以時間作為通過條件；iteration 內的異常只記錄。
// 測試以非零結束的唯一原因是「證據不完整」（invalidEvidence > 0）。
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
	mode := os.Getenv("DIAG_MODE")
	if mode == "" {
		mode = "normal"
	}
	forceEOFTimeout := os.Getenv("DIAG_FORCE_EOF_TIMEOUT") == "1"
	exitedTimeout := diagEnvDur("DIAG_EXITED_TIMEOUT", 10*time.Second)
	t.Logf("diag: iterations=%d outDir=%s runner=%s goos=%s mode=%s forceEOFTimeout=%v exitedTimeout=%s", n, outDir, runner, runtime.GOOS, mode, forceEOFTimeout, exitedTimeout)

	var evMu sync.Mutex
	var invalidEvidence []string
	invalid := func(format string, a ...any) {
		evMu.Lock()
		invalidEvidence = append(invalidEvidence, fmt.Sprintf(format, a...))
		evMu.Unlock()
	}
	mustWrite := func(path string, b []byte) {
		if err := os.WriteFile(path, b, 0o644); err != nil {
			invalid("write %s: %v", filepath.Base(path), err)
		}
	}
	writePartial := func(rec *diagRecord) {
		if b, jerr := json.Marshal(rec); jerr == nil {
			mustWrite(filepath.Join(outDir, fmt.Sprintf("iter-%d-partial.json", rec.Iter)), b)
		} else {
			invalid("iter %d marshal partial: %v", rec.Iter, jerr)
		}
	}

	// finalizeIter：正常與逾時路徑共用的收尾證據檢查——scanner error、逐輪 after 快照、partial 寫檔。
	finalizeIter := func(rec *diagRecord, i int, getScanErr func() error, beforePids map[int]bool) {
		if getScanErr != nil {
			if serr := getScanErr(); serr != nil {
				rec.Errors = append(rec.Errors, "scanner: "+serr.Error())
				invalid("iter %d scanner: %v", i, serr)
			}
		}
		after, perr := diagPs(filepath.Join(outDir, fmt.Sprintf("iter-%d-after.txt", i)))
		if perr != nil {
			invalid("iter %d after ps: %v", i, perr)
		}
		for _, r := range after {
			if !beforePids[r.PID] {
				rec.NewPidsAfterIter++
			}
		}
		writePartial(rec)
	}

	records := make([]*diagRecord, 0, n)
	type bgWait struct {
		rec     *diagRecord
		eofCh   <-chan struct{}
		doneCh  <-chan struct{}
		scanErr func() error
	}
	var bg []bgWait
	offsets := []time.Duration{0, 50 * time.Millisecond, 200 * time.Millisecond, time.Second, 5 * time.Second}

	// rescueDecide：以當下快照做兩階段身分驗證後決定 rescue 模式；只對已驗明目標送訊號。
	rescueDecide := func(rec *diagRecord, p *Proc, orphanPid, childPid int, token, reason string, i int, leaderRow *diagPsRow) {
		rows, perr := diagPs(filepath.Join(outDir, fmt.Sprintf("iter-%d-rescue-before.txt", i)))
		if perr != nil {
			invalid("iter %d rescue-before ps: %v", i, perr)
		}
		sb := diagSummarize(rows, p.PGID(), orphanPid, childPid, token)
		sb.Offset = "rescue-before"
		sb.At = time.Now().UnixNano()
		rec.Rescue.PsBefore = &sb
		rec.Rescue.Reason = reason
		parent, child := diagIdentity(rows, orphanPid, childPid, token)
		var targets []diagPsRow
		if parent != nil {
			targets = append(targets, *parent)
		}
		if child != nil {
			targets = append(targets, *child)
		}
		// exitedTimeout 路徑：leader 也是已驗明目標（pid==p.PGID() 且命令列含 fixture 名）。
		if leaderRow != nil {
			for _, r := range rows {
				if r.PID == leaderRow.PID && strings.Contains(r.Command, "diag-orphan.sh") {
					targets = append(targets, r)
				}
			}
		}
		allInGroup := true
		for _, tr := range targets {
			if tr.PGID != p.PGID() {
				allInGroup = false
			}
		}
		switch {
		case len(targets) == 0:
			rec.Rescue.Mode = "skipped-no-target"
		case allInGroup:
			// 所有已驗明目標都在預期群組：對群組送 KILL（唯一允許使用 p.PGID() 的情況）。
			rec.Rescue.Mode = "group"
			rec.Rescue.AtNs = time.Now().UnixNano()
			if err := p.SignalGroup(syscall.SIGKILL); err != nil {
				rec.Rescue.Err = err.Error()
			}
		default:
			// 有已驗明目標不在預期群組：先固定 psBefore 的目標清單，再逐一送訊號並記錄每個結果。
			rec.Rescue.Mode = "targeted-pid"
			rec.Rescue.AtNs = time.Now().UnixNano()
			rec.Rescue.PerPID = map[string]string{}
			for _, tr := range targets {
				if err := syscall.Kill(tr.PID, syscall.SIGKILL); err != nil {
					rec.Rescue.PerPID[strconv.Itoa(tr.PID)] = err.Error()
				} else {
					rec.Rescue.PerPID[strconv.Itoa(tr.PID)] = "ok"
				}
			}
		}
		rows2, perr2 := diagPs(filepath.Join(outDir, fmt.Sprintf("iter-%d-rescue-after.txt", i)))
		if perr2 != nil {
			invalid("iter %d rescue-after ps: %v", i, perr2)
		}
		sa := diagSummarize(rows2, p.PGID(), orphanPid, childPid, token)
		sa.Offset = "rescue-after"
		sa.At = time.Now().UnixNano()
		rec.Rescue.PsAfter = &sa
	}

	for i := 0; i < n; i++ {
		rec := &diagRecord{Iter: i, Runner: runner, GOOS: runtime.GOOS, Mode: mode, FinalStatus: "pending", ForcedEOFTimeout: forceEOFTimeout}
		rec.Rescue.Mode = "none"
		records = append(records, rec)
		token := diagToken(i)
		rec.Token = token
		pidfile := filepath.Join(outDir, fmt.Sprintf("iter-%d-orphan.pid", i))
		childfile := filepath.Join(outDir, fmt.Sprintf("iter-%d-child.pid", i))
		before, perr := diagPs(filepath.Join(outDir, fmt.Sprintf("iter-%d-before.txt", i)))
		if perr != nil {
			invalid("iter %d before ps: %v", i, perr)
		}
		beforePids := map[int]bool{}
		for _, r := range before {
			beforePids[r.PID] = true
		}

		p, err := Start(context.Background(), Config{Binary: fixture, Env: []string{"DIAG_TOKEN=" + token, "DIAG_PIDFILE=" + pidfile, "DIAG_CHILDPIDFILE=" + childfile, "DIAG_MODE=" + mode}, TermGrace: 200 * time.Millisecond})
		if err != nil {
			rec.Errors = append(rec.Errors, "start: "+err.Error())
			rec.FinalStatus = "aborted"
			invalid("iter %d Start failed: %v", i, err)
			writePartial(rec)
			continue
		}
		rec.PGID = p.PGID()
		t0 := time.Now()

		// seam：依既有白箱測試的鎖定契約，於 p.mu 內寫入；在送 prompt 前完成，leader 收到 prompt 前不會退出。
		killCh := make(chan time.Time, 1)
		p.mu.Lock()
		p.onSignal = func(ev signalEvent) {
			if ev == sigEventSupervisorCleanupKill {
				select {
				case killCh <- time.Now():
				default:
				}
			}
		}
		p.mu.Unlock()

		// 單一 stdout reader：同一 goroutine 記 tLastLine 與 tEOF（mutex 保護）。
		var tsMu sync.Mutex
		var tLastLine, tEOF time.Time
		var scanErr error
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
			scanErr = sc.Err()
			tEOF = time.Now()
			tsMu.Unlock()
			close(eofCh)
		}()
		if _, werr := p.Stdin.Write([]byte("x\n")); werr != nil {
			rec.Errors = append(rec.Errors, "stdin write: "+werr.Error())
			invalid("iter %d stdin write: %v", i, werr)
		}
		if cerr := p.Stdin.Close(); cerr != nil {
			rec.Errors = append(rec.Errors, "stdin close: "+cerr.Error())
			invalid("iter %d stdin close: %v", i, cerr)
		}

		// 有界等待 exitedCh（候選機制 (a) 若發生，這裡是第一個會逾時的點）。
		var tExited time.Time
		select {
		case <-p.exitedCh:
			tExited = time.Now() // 觀察時刻：supervisor 的 cleanup KILL 事件可早數微秒，故 exitedToCleanupKillMs 可為極小負值。
		case <-time.After(exitedTimeout):
			rec.ExitedTimeout = true
		}
		orphanPid := diagReadPid(pidfile)
		childPid := diagReadPid(childfile) // optional：normal 模式下孫程序常在寫出前即被清掉
		rec.OrphanPid, rec.ChildPid = orphanPid, childPid
		if orphanPid <= 0 {
			// 父 PID 檔是必要證據（fixture 在印出任何 JSON 行前就寫出）；缺失或格式錯誤即證據不完整。
			rec.Errors = append(rec.Errors, "orphan pid file missing or invalid")
			invalid("iter %d orphan pid file missing/invalid: %s", i, filepath.Base(pidfile))
		}

		if rec.ExitedTimeout {
			// (a) 代理命中：leader 在 exitedTimeout 內未被 supervisor 記錄退出。取快照、身分驗證後 cleanup，有界等收斂，記錄並結束本輪。
			snapAt := time.Now()
			file := fmt.Sprintf("iter-%d-ps-exited-timeout.txt", i)
			rows, perr := diagPs(filepath.Join(outDir, file))
			if perr != nil {
				invalid("iter %d exited-timeout ps: %v", i, perr)
			}
			sum := diagSummarize(rows, p.PGID(), orphanPid, childPid, token)
			sum.Offset = "exited-timeout"
			sum.At = snapAt.UnixNano()
			sum.File = file
			rec.Snapshots = append(rec.Snapshots, sum)
			if ll := getLastLine(); !ll.IsZero() {
				v := float64(snapAt.Sub(ll).Microseconds()) / 1000.0
				rec.LastLineToExitedMs = &v // 下界：至 exitedTimeout 觀察時刻仍未記錄退出
			}
			var leaderRow *diagPsRow
			for k := range rows {
				if rows[k].PID == p.PGID() {
					leaderRow = &rows[k]
				}
			}
			rescueDecide(rec, p, orphanPid, childPid, token, "exitedTimeout", i, leaderRow)
			eofSeen, doneSeen, _, _, _ := diagWaitConverge(time.Now().Add(5*time.Second), eofCh, p.Done(), false, false)
			rec.Converged = eofSeen && doneSeen // 無 tExited 基準，不填 exitedToEOF／exitedToDone
			rec.AfterRescue = rec.Converged
			if rec.Converged {
				ex := p.Wait()
				code := ex.Code
				rec.ExitCode = &code
				rec.FinalStatus = "converged"
			} else {
				bg = append(bg, bgWait{rec: rec, eofCh: eofCh, doneCh: p.Done(), scanErr: func() error { tsMu.Lock(); defer tsMu.Unlock(); return scanErr }})
			}
			finalizeIter(rec, i, func() error { tsMu.Lock(); defer tsMu.Unlock(); return scanErr }, beforePids)
			t.Logf("iter %d: exitedTimeout rescue=%s converged=%v", i, rec.Rescue.Mode, rec.Converged)
			continue
		}

		d1 := tExited.Add(10 * time.Second)
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
				sum := diagSummarize(rows, p.PGID(), orphanPid, childPid, token)
				sum.Offset = off.String()
				sum.At = time.Now().UnixNano()
				sum.File = filepath.Base(file)
				if perr != nil {
					sum.Err = perr.Error()
					invalid("iter %d ps@%s: %v", i, off, perr)
				}
				snapMu.Lock()
				rec.Snapshots = append(rec.Snapshots, sum)
				snapMu.Unlock()
			}
		}()

		// guard 1：EOF 以 D1 為絕對 deadline（DIAG_FORCE_EOF_TIMEOUT=1 時直接視為逾時，用於強制 skipped-no-target 路徑）。
		var tDone time.Time
		if forceEOFTimeout {
			rec.EOFTimeout = true
		} else {
			select {
			case <-eofCh:
			case <-time.After(time.Until(d1)):
				rec.EOFTimeout = true
			}
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
		reason := ""
		switch {
		case rec.EOFTimeout:
			reason = "eofTimeout"
		case rec.DoneTimeout:
			reason = "doneTimeout"
		}
		if !needRescue {
			snapWg.Wait()
			snapMu.Lock()
			for _, s := range rec.Snapshots {
				if s.Offset == "5s" && (s.OrphanPresent || s.ChildPresent) {
					rec.Anomaly = true
					needRescue = true
					reason = "anomaly@5s"
				}
			}
			snapMu.Unlock()
		}
		if needRescue {
			rescueDecide(rec, p, orphanPid, childPid, token, reason, i, nil)
			eofSeen, doneSeen, _, tD, _ := diagWaitConverge(time.Now().Add(5*time.Second), eofCh, p.Done(), !getEOF().IsZero(), !tDone.IsZero())
			if doneSeen && tDone.IsZero() && !tD.IsZero() {
				tDone = tD
			}
			if (eofSeen && rec.EOFTimeout) || (doneSeen && (rec.DoneTimeout || rec.EOFTimeout)) {
				rec.AfterRescue = true
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
			bg = append(bg, bgWait{rec: rec, eofCh: eofCh, doneCh: p.Done(), scanErr: func() error { tsMu.Lock(); defer tsMu.Unlock(); return scanErr }})
		}

		finalizeIter(rec, i, func() error { tsMu.Lock(); defer tsMu.Unlock(); return scanErr }, beforePids)
		if rec.EOFTimeout || rec.DoneTimeout || rec.Anomaly || !rec.Converged {
			t.Logf("iter %d: eofTimeout=%v doneTimeout=%v anomaly=%v converged=%v rescue=%s", i, rec.EOFTimeout, rec.DoneTimeout, rec.Anomaly, rec.Converged, rec.Rescue.Mode)
		}
	}

	// 彙報階段：整體最多 30s（絕對期限）；EOF 與 Done 兩者皆到才是 finalConverged；稍後出現的 scanner error 也計入。
	deadline := time.Now().Add(30 * time.Second)
	for _, w := range bg {
		start := time.Now()
		eofSeen, doneSeen, _, _, _ := diagWaitConverge(deadline, w.eofCh, w.doneCh, false, false)
		if eofSeen && doneSeen {
			w.rec.FinalStatus = "finalConverged"
			w.rec.FinalWaitMs = diagMs(time.Since(start))
		} else {
			w.rec.FinalStatus = "pending"
			w.rec.Errors = append(w.rec.Errors, fmt.Sprintf("pending at report: eof=%v done=%v", eofSeen, doneSeen))
		}
		if w.scanErr != nil {
			if serr := w.scanErr(); serr != nil {
				w.rec.Errors = append(w.rec.Errors, "scanner(late): "+serr.Error())
				invalid("iter %d scanner(late): %v", w.rec.Iter, serr)
			}
		}
		if b, jerr := json.Marshal(w.rec); jerr == nil {
			mustWrite(filepath.Join(outDir, fmt.Sprintf("iter-%d-final.json", w.rec.Iter)), b)
		}
	}

	// 每輪只序列化一次；任何寫入／編碼錯誤都計入 invalidEvidence。
	f, err := os.Create(filepath.Join(outDir, "iter-records.jsonl"))
	if err != nil {
		t.Fatalf("create iter-records.jsonl: %v", err)
	}
	enc := json.NewEncoder(f)
	counts := map[string]int{}
	for _, rec := range records {
		if err := enc.Encode(rec); err != nil {
			invalid("encode iter %d: %v", rec.Iter, err)
		}
		counts[rec.FinalStatus]++
		if rec.ExitedTimeout {
			counts["exitedTimeout"]++
		}
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
	if err := f.Close(); err != nil {
		invalid("close iter-records.jsonl: %v", err)
	}
	evMu.Lock()
	inv := append([]string(nil), invalidEvidence...)
	evMu.Unlock()
	summary := map[string]any{"iterations": n, "runner": runner, "goos": runtime.GOOS, "mode": mode, "forceEOFTimeout": forceEOFTimeout, "exitedTimeout": exitedTimeout.String(), "counts": counts, "invalidEvidence": inv}
	if b, err := json.MarshalIndent(summary, "", "  "); err == nil {
		mustWrite(filepath.Join(outDir, "summary.json"), b)
	} else {
		invalid("marshal summary: %v", err)
	}
	evMu.Lock()
	inv = append([]string(nil), invalidEvidence...)
	evMu.Unlock()
	t.Logf("diag summary: %v invalidEvidence=%d", counts, len(inv))
	if len(inv) > 0 {
		t.Fatalf("evidence incomplete (%d issues); artifacts kept in %s: %v", len(inv), outDir, inv)
	}
}
