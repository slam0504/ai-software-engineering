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
//   - 兩階段身分（僅 DIAG_FIXTURE=diag）：先以 DIAG_PIDFILE 的 PID＋命令列含 token 驗明父 bash，再接受同一份快照中 ppid==父 PID 且 PID==DIAG_CHILDPIDFILE 的 sleep。
//   - rescue 只對已驗明目標：父與子皆在 p.PGID() → group；否則只對已驗明 PID 個別 kill → targeted-pid；無目標 → skipped-no-target。
//   - 證據完整性：任何 artifact 寫入／編碼／Start／stdin／scanner 錯誤都累積到 invalidEvidence，最後讓測試非零結束。
//   - 強制控制（只用於本機驗證分支，CI 不設）：DIAG_MODE=hang（exitedTimeout 路徑）、DIAG_MODE=escape（eofTimeout→targeted-pid→guard 2）、
//     DIAG_FORCE_EOF_TIMEOUT=1（跳過 guard 1 等待→skipped-no-target 路徑）。
//
// B2c-2 Task 1（fixture-only differential 參數化，本檔新增）：
//   - DIAG_FIXTURE=diag|fake-claude（預設 diag）：diag＝上述既有行為，語意與先前完全相同；
//     fake-claude＝Binary 換成 repo-root testdata/fake-claude.sh；proc.Start 會把 os.Environ()
//     整份傳給子程序（proc.go:133），因此 fake 路徑以尾端覆寫明確清空所有 FAKE_*（FAKE_MULTI／
//     FAKE_STDERR／FAKE_DIE／FAKE_BADLINE／FAKE_HANG／FAKE_EXIT；os/exec 去重保留最後一筆），
//     再設 FAKE_ORPHAN=1，父環境原有的 FAKE_* 名稱記入 record.ambientFakeVars 揭露。所有身分
//     欄位不可用，rescueDecide 恆為 skipped-no-target（不讀 PID 檔、不送任何訊號）；DIAG_MODE
//     對 fake-claude 無意義（fixture 不使用；環境變數仍會被子程序繼承），記 modeIgnored=true。
//   - DIAG_PS_TIMEOUT（預設 5s；僅本機控制用）：每次 ps 取樣的 CommandContext 期限，逾時保留
//     起訖時刻並計入 invalidEvidence（測試非零），避免 ps 卡住讓 iteration 無界。
//   - DIAG_FAKE_HANG=1（僅 DIAG_FIXTURE=fake-claude 生效；正式 CI 不設）：多加 FAKE_HANG=1 進
//     fixture Env，讓 leader 收尾前 sleep 30——唯一用途是製造確定性的 pending 控制樣本。
//   - pending 與 invalidEvidence 分開計數且都會讓測試非零結束：pending＝彙報階段（整批共用
//     一個 reportDeadline，預設 60s、DIAG_REPORT_DEADLINE 可覆寫，僅本機控制用）到期時該輪仍
//     未同時觀察到 EOF 與 Done——這是有效診斷結果，不是取證失敗；invalidEvidence 只計取證失
//     敗（artifact／Start／stdin／scanner／JSON 編碼／fixture 缺失等）。兩者皆分別列於
//     summary.json，任一 >0 都讓測試以非零結束。
//   - 逾時輪（exitedTimeout／eofTimeout／doneTimeout）一律記錄後立即進入下一輪，未收斂的
//     Proc／goroutine 交背景，統一由彙報階段的 reportDeadline 收斂或標記 pending，不在主迴圈
//     內等待孫程序的 sleep 30。
//   - 每筆 record 另外持有 pgid（供外層 shell 唯讀輪詢）、tCleanupKill（onSignal cleanup-kill
//     callback 的絕對時刻，RFC3339Nano＋相對 t0 的 ms offset）；每筆 ps 快照另外記 psStart／
//     psEnd（exec 前後的絕對時刻）與相對 tCleanupKill 的分類（afterCleanupKill／straddle／
//     before／unknown）、groupStatZ（群組內 Z 狀態成員數）、orphanLikeCount（群組內命令列含
//     "sleep 30" 的成員數）——皆僅供觀察，不驅動任何判定或訊號。
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
	PsStartNs         int64  `json:"psStartNs"`
	PsEndNs           int64  `json:"psEndNs"`
	CleanupRelation   string `json:"cleanupRelation"` // afterCleanupKill | straddle | before | unknown
	GroupMembers      int    `json:"groupMembers"`
	GroupStatZ        int    `json:"groupStatZ"`      // 群組成員中 stat 以 Z 開頭的數量（觀察用）
	OrphanLikeCount   int    `json:"orphanLikeCount"` // 群組成員中命令列含 "sleep 30" 的數量（觀察用）
	OrphanPresent     bool   `json:"orphanPresent"`   // 父 bash：PID==DIAG_PIDFILE 且命令列含 token
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
	Iter                 int             `json:"iter"`
	Runner               string          `json:"runner"`
	GOOS                 string          `json:"goos"`
	Fixture              string          `json:"fixture"` // diag | fake-claude
	FakeHang             bool            `json:"fakeHang"`
	Mode                 string          `json:"mode"`
	ModeIgnored          bool            `json:"modeIgnored,omitempty"`     // fake-claude 不支援 DIAG_MODE，恆為 true
	AmbientFakeVars      []string        `json:"ambientFakeVars,omitempty"` // 父環境原有的 FAKE_* 名稱（已被尾端覆寫清空，僅揭露）
	Token                string          `json:"token"`
	OrphanPid            int             `json:"orphanPid"`
	ChildPid             int             `json:"childPid"`
	PGID                 int             `json:"pgid"`
	StartToExitedMs      *float64        `json:"startToExitedMs"`
	LastLineToExitedMs   *float64        `json:"lastLineToExitedMs"`
	ExitedToCleanupKill  *float64        `json:"exitedToCleanupKillMs"`
	TCleanupKill         string          `json:"tCleanupKill,omitempty"` // RFC3339Nano，onSignal cleanup-kill callback 的絕對時刻
	TCleanupKillOffsetMs *float64        `json:"tCleanupKillOffsetMs,omitempty"`
	ExitedToEOFMs        *float64        `json:"exitedToEOFMs"`
	ExitedToDoneMs       *float64        `json:"exitedToDoneMs"`
	ExitedTimeout        bool            `json:"exitedTimeout"`
	EOFTimeout           bool            `json:"eofTimeout"`
	DoneTimeout          bool            `json:"doneTimeout"`
	ForcedEOFTimeout     bool            `json:"forcedEofTimeout"`
	AfterRescue          bool            `json:"afterRescue"`
	Converged            bool            `json:"converged"`
	FinalStatus          string          `json:"finalConverged"` // 彙報後填入：converged | finalConverged | pending | aborted
	FinalWaitMs          *float64        `json:"finalWaitMs,omitempty"`
	Anomaly              bool            `json:"anomaly"`
	Snapshots            []diagPsSummary `json:"ps"`
	Rescue               diagRescue      `json:"rescue"`
	NewPidsAfterIter     int             `json:"newPidsAfterIter"`
	ExitCode             *int            `json:"exitCode,omitempty"`
	Errors               []string        `json:"errors,omitempty"`

	// 不序列化（小寫）：主 goroutine 內部計算用，僅供 tCleanupKillOffsetMs／cleanupRelation。
	t0             time.Time
	tCleanupKillAt time.Time
}

func diagMs(d time.Duration) *float64 { v := float64(d.Microseconds()) / 1000.0; return &v }

// diagPs 回傳的 start／end 是 exec 前、parse 完成後的絕對時刻，供每筆 ps 快照與
// tCleanupKill 判先後（afterCleanupKill／straddle／before／unknown，見 diagClassifyCleanupRelation）。
// 以 Go 端依 pgid 過濾（見 diagSummarize），不依賴 `ps -g`，`-eo` 已在 macOS／Linux 兩種 ps
// 上驗證可用，維持可攜。
var diagPsTimeout = diagEnvDur("DIAG_PS_TIMEOUT", 5*time.Second)

func diagPs(path string) (rows []diagPsRow, start, end time.Time, err error) {
	start = time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), diagPsTimeout)
	defer cancel()
	out, cmdErr := exec.CommandContext(ctx, "ps", "-eo", "pgid,pid,ppid,stat,etime,command").CombinedOutput()
	if ctx.Err() != nil {
		cmdErr = fmt.Errorf("ps timeout after %s: %w", diagPsTimeout, ctx.Err())
	}
	if path != "" {
		if werr := os.WriteFile(path, out, 0o644); werr != nil {
			end = time.Now()
			return nil, start, end, fmt.Errorf("write %s: %w", path, werr)
		}
	}
	if cmdErr != nil {
		end = time.Now()
		return nil, start, end, cmdErr
	}
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
	end = time.Now()
	return rows, start, end, nil
}

// diagIdentity 兩階段身分：先驗父（PID＋token），再驗子（PID＋ppid==父）。回傳已驗明的 rows。
// fake-claude 路徑恆傳 orphanPid=childPid=0，兩層檢查皆不成立，回傳 nil,nil（skipped-no-target）。
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

func diagSummarize(rows []diagPsRow, groupPgid, orphanPid, childPid int, token string, psStart, psEnd time.Time) diagPsSummary {
	s := diagPsSummary{PsStartNs: psStart.UnixNano(), PsEndNs: psEnd.UnixNano()}
	for _, r := range rows {
		if r.PGID == groupPgid {
			s.GroupMembers++
			if strings.HasPrefix(r.Stat, "Z") {
				s.GroupStatZ++
			}
			if strings.Contains(r.Command, "sleep 30") {
				s.OrphanLikeCount++
			}
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

// diagClassifyCleanupRelation：ps 取樣窗口（psStart／psEnd）與 cleanup KILL 絕對時刻
// （cleanupKillAt）的先後關係。未觀察到 cleanup KILL（cleanupKillAt 為零值）一律回傳
// unknown；只有 ps.start >= cleanupKillAt 才回傳 afterCleanupKill，橫跨者回傳 straddle，
// 較早者回傳 before（owner 裁定 rev5：只有 afterCleanupKill 的樣本才可能支持 (b1)）。
func diagClassifyCleanupRelation(psStart, psEnd, cleanupKillAt time.Time) string {
	if cleanupKillAt.IsZero() {
		return "unknown"
	}
	if !psStart.Before(cleanupKillAt) {
		return "afterCleanupKill"
	}
	if !psEnd.Before(cleanupKillAt) {
		return "straddle"
	}
	return "before"
}

// diagRecordCleanupKill：非阻塞讀 killCh（cap 1，onSignal 於 sigEventSupervisorCleanupKill
// 成功時送出恰一次）；只由主 goroutine 呼叫，可在同一輪的多個檢查點重複呼叫——值只會被
// 讀走一次，讀走後的呼叫落入 default，不會覆寫已寫入的欄位。tExited 為零值時（exitedTimeout
// 路徑無 tExited 基準）不填 ExitedToCleanupKill，語意同既有註解「無 tExited 基準，不填
// exitedToEOF／exitedToDone」。
func diagRecordCleanupKill(rec *diagRecord, killCh <-chan time.Time, tExited time.Time) {
	select {
	case tk := <-killCh:
		rec.tCleanupKillAt = tk
		rec.TCleanupKill = tk.Format(time.RFC3339Nano)
		rec.TCleanupKillOffsetMs = diagMs(tk.Sub(rec.t0))
		if !tExited.IsZero() {
			rec.ExitedToCleanupKill = diagMs(tk.Sub(tExited))
		}
	default:
	}
}

// diagClassifyRecordSnapshots：用 rec 目前已知的 tCleanupKillAt（可能仍是零值＝unknown）
// 回填每筆 ps 快照（含 rescue 前後）的 cleanupRelation；冪等，可重複呼叫。
func diagClassifyRecordSnapshots(rec *diagRecord) {
	for k := range rec.Snapshots {
		rec.Snapshots[k].CleanupRelation = diagClassifyCleanupRelation(
			time.Unix(0, rec.Snapshots[k].PsStartNs), time.Unix(0, rec.Snapshots[k].PsEndNs), rec.tCleanupKillAt)
	}
	if rec.Rescue.PsBefore != nil {
		rec.Rescue.PsBefore.CleanupRelation = diagClassifyCleanupRelation(
			time.Unix(0, rec.Rescue.PsBefore.PsStartNs), time.Unix(0, rec.Rescue.PsBefore.PsEndNs), rec.tCleanupKillAt)
	}
	if rec.Rescue.PsAfter != nil {
		rec.Rescue.PsAfter.CleanupRelation = diagClassifyCleanupRelation(
			time.Unix(0, rec.Rescue.PsAfter.PsStartNs), time.Unix(0, rec.Rescue.PsAfter.PsEndNs), rec.tCleanupKillAt)
	}
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

// diagReportAggregate 是彙報階段專用的 aggregator：包一層 diagWaitConverge，額外算出
// pending（尚未同時收到 EOF 與 Done）。行為完全委派給 diagWaitConverge，因此沿用其
// 「已 seen 的 channel 設 nil、兩者皆已 seen 時不consult deadline 直接返回」的性質。
func diagReportAggregate(deadline time.Time, eofCh, doneCh <-chan struct{}, eofSeen, doneSeen bool) (finalEOFSeen, finalDoneSeen, pending bool, tEOFSeen, tDoneSeen time.Time, loops int) {
	finalEOFSeen, finalDoneSeen, tEOFSeen, tDoneSeen, loops = diagWaitConverge(deadline, eofCh, doneCh, eofSeen, doneSeen)
	pending = !(finalEOFSeen && finalDoneSeen)
	return
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

// TestDiagReportAggregator：彙報階段 aggregator 的 helper control——用永不關閉的 synthetic
// channel 驗證絕對 deadline 準時回 pending、不空轉；驗證已 seen 狀態只等未 seen 的 channel；
// 驗證兩者皆已 seen 時立即返回，不理會（甚至不阻塞在）遙遠的 deadline。
func TestDiagReportAggregator(t *testing.T) {
	t.Run("never closed, deadline pending, no spin", func(t *testing.T) {
		eofCh := make(chan struct{})
		doneCh := make(chan struct{})
		deadline := time.Now().Add(300 * time.Millisecond)
		start := time.Now()
		eof, done, pending, _, _, loops := diagReportAggregate(deadline, eofCh, doneCh, false, false)
		elapsed := time.Since(start)
		if eof || done || !pending {
			t.Fatalf("expected eof=false done=false pending=true, got eof=%v done=%v pending=%v", eof, done, pending)
		}
		if elapsed < 250*time.Millisecond || elapsed > time.Second {
			t.Fatalf("did not return near deadline: elapsed=%s", elapsed)
		}
		if loops > 1 {
			t.Fatalf("busy loop detected: %d selects", loops)
		}
	})

	t.Run("eof already seen waits only for done", func(t *testing.T) {
		eofCh := make(chan struct{})
		doneCh := make(chan struct{})
		deadline := time.Now().Add(300 * time.Millisecond)
		start := time.Now()
		eof, done, pending, tEOF, _, _ := diagReportAggregate(deadline, eofCh, doneCh, true, false)
		elapsed := time.Since(start)
		if !eof || done || !pending {
			t.Fatalf("expected eof=true done=false pending=true, got eof=%v done=%v pending=%v", eof, done, pending)
		}
		if !tEOF.IsZero() {
			t.Fatalf("tEOFSeen must stay zero when eof was already seen before the call, got %v", tEOF)
		}
		if elapsed < 250*time.Millisecond || elapsed > time.Second {
			t.Fatalf("did not return near deadline: elapsed=%s", elapsed)
		}
	})

	t.Run("both already seen returns immediately regardless of deadline", func(t *testing.T) {
		eofCh := make(chan struct{})
		doneCh := make(chan struct{})
		start := time.Now()
		eof, done, pending, _, _, loops := diagReportAggregate(time.Now().Add(24*time.Hour), eofCh, doneCh, true, true)
		elapsed := time.Since(start)
		if !eof || !done || pending {
			t.Fatalf("expected eof=true done=true pending=false, got eof=%v done=%v pending=%v", eof, done, pending)
		}
		if elapsed > 50*time.Millisecond {
			t.Fatalf("did not return immediately: elapsed=%s", elapsed)
		}
		if loops != 0 {
			t.Fatalf("expected 0 loops when both pre-seen, got %d", loops)
		}
	})
}

// TestDiagOrphanTimeline 只做取證，不以時間作為通過條件；iteration 內的異常只記錄。
// 測試以非零結束的原因有二，分開計數且分開列於 summary：invalidEvidence（取證失敗：
// artifact 寫入／Start／stdin／scanner／JSON 編碼／fixture 缺失等）與 pending（彙報階段的
// 共用絕對期限到期時仍未同時觀察到 EOF 與 Done——有效診斷結果，不是取證失敗）。
func TestDiagOrphanTimeline(t *testing.T) {
	fixtureKind := os.Getenv("DIAG_FIXTURE")
	if fixtureKind == "" {
		fixtureKind = "diag"
	}
	if fixtureKind != "diag" && fixtureKind != "fake-claude" {
		t.Fatalf("invalid DIAG_FIXTURE=%q (want diag|fake-claude)", fixtureKind)
	}
	fakeHang := os.Getenv("DIAG_FAKE_HANG") == "1"
	if fakeHang && fixtureKind != "fake-claude" {
		t.Fatalf("DIAG_FAKE_HANG=1 requires DIAG_FIXTURE=fake-claude")
	}

	fixtureRel := "testdata/diag-orphan.sh"
	if fixtureKind == "fake-claude" {
		fixtureRel = "../../testdata/fake-claude.sh"
	}
	fixture, err := filepath.Abs(fixtureRel)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(fixture); err != nil || fi.Mode()&0o111 == 0 {
		t.Fatalf("fixture missing or not executable: %v (%s)", err, fixture)
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
	modeIgnored := fixtureKind == "fake-claude"
	forceEOFTimeout := os.Getenv("DIAG_FORCE_EOF_TIMEOUT") == "1"
	exitedTimeout := diagEnvDur("DIAG_EXITED_TIMEOUT", 10*time.Second)
	reportDeadlineDur := diagEnvDur("DIAG_REPORT_DEADLINE", 60*time.Second)
	t.Logf("diag: iterations=%d outDir=%s runner=%s goos=%s fixture=%s fakeHang=%v mode=%s modeIgnored=%v forceEOFTimeout=%v exitedTimeout=%s reportDeadline=%s",
		n, outDir, runner, runtime.GOOS, fixtureKind, fakeHang, mode, modeIgnored, forceEOFTimeout, exitedTimeout, reportDeadlineDur)

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
		after, _, _, perr := diagPs(filepath.Join(outDir, fmt.Sprintf("iter-%d-after.txt", i)))
		if perr != nil {
			invalid("iter %d after ps: %v", i, perr)
		}
		for _, r := range after {
			if !beforePids[r.PID] {
				rec.NewPidsAfterIter++
			}
		}
		diagClassifyRecordSnapshots(rec)
		writePartial(rec)
	}

	records := make([]*diagRecord, 0, n)
	type bgWait struct {
		rec     *diagRecord
		eofCh   <-chan struct{}
		doneCh  <-chan struct{}
		scanErr func() error
		killCh  <-chan time.Time
		tExited time.Time
	}
	var bg []bgWait
	offsets := []time.Duration{0, 50 * time.Millisecond, 200 * time.Millisecond, time.Second, 5 * time.Second}

	// rescueDecide：以當下快照做兩階段身分驗證後決定 rescue 模式；只對已驗明目標送訊號。
	// 呼叫端對 fake-claude 傳入 orphanPid=childPid=0、leaderRow=nil，兩階段身分皆不成立，
	// 恆得到 skipped-no-target，不讀任何 PID 檔、不送任何訊號。
	rescueDecide := func(rec *diagRecord, p *Proc, orphanPid, childPid int, token, reason string, i int, leaderRow *diagPsRow) {
		rows, rStart, rEnd, perr := diagPs(filepath.Join(outDir, fmt.Sprintf("iter-%d-rescue-before.txt", i)))
		if perr != nil {
			invalid("iter %d rescue-before ps: %v", i, perr)
		}
		sb := diagSummarize(rows, p.PGID(), orphanPid, childPid, token, rStart, rEnd)
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
		// exitedTimeout 路徑：leader 也是已驗明目標（pid==p.PGID() 且命令列含 fixture 名）；
		// 只有 fixtureKind==diag 的呼叫端才會傳非 nil leaderRow（見下方呼叫處）。
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
		rows2, r2Start, r2End, perr2 := diagPs(filepath.Join(outDir, fmt.Sprintf("iter-%d-rescue-after.txt", i)))
		if perr2 != nil {
			invalid("iter %d rescue-after ps: %v", i, perr2)
		}
		sa := diagSummarize(rows2, p.PGID(), orphanPid, childPid, token, r2Start, r2End)
		sa.Offset = "rescue-after"
		sa.At = time.Now().UnixNano()
		rec.Rescue.PsAfter = &sa
	}

	for i := 0; i < n; i++ {
		rec := &diagRecord{Iter: i, Runner: runner, GOOS: runtime.GOOS, Fixture: fixtureKind, FakeHang: fakeHang, Mode: mode, ModeIgnored: modeIgnored, FinalStatus: "pending", ForcedEOFTimeout: forceEOFTimeout}
		rec.Rescue.Mode = "none"
		records = append(records, rec)
		token := diagToken(i)
		rec.Token = token
		pidfile := filepath.Join(outDir, fmt.Sprintf("iter-%d-orphan.pid", i))
		childfile := filepath.Join(outDir, fmt.Sprintf("iter-%d-child.pid", i))
		before, _, _, perr := diagPs(filepath.Join(outDir, fmt.Sprintf("iter-%d-before.txt", i)))
		if perr != nil {
			invalid("iter %d before ps: %v", i, perr)
		}
		beforePids := map[int]bool{}
		for _, r := range before {
			beforePids[r.PID] = true
		}

		// Env 依 fixtureKind 分岔：fake-claude 只給 FAKE_ORPHAN=1（+ 診斷用 FAKE_HANG=1），
		// 沒有 token／PID 檔；diag 維持既有 DIAG_TOKEN／DIAG_PIDFILE／DIAG_CHILDPIDFILE／DIAG_MODE。
		var startEnv []string
		if fixtureKind == "fake-claude" {
			// proc.Start 以 append(os.Environ(), cfg.Env...) 傳環境，os/exec 去重保留最後一筆：
			// 先以尾端覆寫清空所有 FAKE_*（fixture 皆以 -n 判斷，空值＝未設），再設 FAKE_ORPHAN=1，
			// 只有 DIAG_FAKE_HANG=1 才覆寫 FAKE_HANG=1；父環境原有的 FAKE_* 名稱記入 record 揭露。
			startEnv = []string{"FAKE_MULTI=", "FAKE_STDERR=", "FAKE_DIE=", "FAKE_BADLINE=", "FAKE_HANG=", "FAKE_EXIT=", "FAKE_ORPHAN=1"}
			if fakeHang {
				startEnv = append(startEnv, "FAKE_HANG=1")
			}
			for _, kv := range os.Environ() {
				if strings.HasPrefix(kv, "FAKE_") {
					if k, _, ok := strings.Cut(kv, "="); ok {
						rec.AmbientFakeVars = append(rec.AmbientFakeVars, k)
					}
				}
			}
		} else {
			startEnv = []string{"DIAG_TOKEN=" + token, "DIAG_PIDFILE=" + pidfile, "DIAG_CHILDPIDFILE=" + childfile, "DIAG_MODE=" + mode}
		}

		p, err := Start(context.Background(), Config{Binary: fixture, Env: startEnv, TermGrace: 200 * time.Millisecond})
		if err != nil {
			rec.Errors = append(rec.Errors, "start: "+err.Error())
			rec.FinalStatus = "aborted"
			invalid("iter %d Start failed: %v", i, err)
			writePartial(rec)
			continue
		}
		rec.PGID = p.PGID()
		t0 := time.Now()
		rec.t0 = t0

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
		// stdin：無論 fixture 為何，皆讀一行 "x\n"（diag-orphan.sh／fake-claude.sh 皆為 `read -r _prompt || true`）。
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
		var orphanPid, childPid int
		if fixtureKind == "diag" {
			// 父 PID 檔是必要證據（fixture 在印出任何 JSON 行前就寫出）；缺失或格式錯誤即證據不完整。
			// fake-claude 沒有 PID 檔可讀（設計上不提供身分），不觸發 invalidEvidence。
			orphanPid = diagReadPid(pidfile)
			childPid = diagReadPid(childfile) // optional：normal 模式下孫程序常在寫出前即被清掉
			if orphanPid <= 0 {
				rec.Errors = append(rec.Errors, "orphan pid file missing or invalid")
				invalid("iter %d orphan pid file missing/invalid: %s", i, filepath.Base(pidfile))
			}
		}
		rec.OrphanPid, rec.ChildPid = orphanPid, childPid

		if rec.ExitedTimeout {
			// (a) 代理命中：leader 在 exitedTimeout 內未被 supervisor 記錄退出。取快照、身分驗證後 cleanup，有界等收斂，記錄並結束本輪。
			snapAt := time.Now()
			file := fmt.Sprintf("iter-%d-ps-exited-timeout.txt", i)
			rows, rStart, rEnd, perr := diagPs(filepath.Join(outDir, file))
			if perr != nil {
				invalid("iter %d exited-timeout ps: %v", i, perr)
			}
			sum := diagSummarize(rows, p.PGID(), orphanPid, childPid, token, rStart, rEnd)
			sum.Offset = "exited-timeout"
			sum.At = snapAt.UnixNano()
			sum.File = file
			rec.Snapshots = append(rec.Snapshots, sum)
			if ll := getLastLine(); !ll.IsZero() {
				v := float64(snapAt.Sub(ll).Microseconds()) / 1000.0
				rec.LastLineToExitedMs = &v // 下界：至 exitedTimeout 觀察時刻仍未記錄退出
			}
			var leaderRow *diagPsRow
			if fixtureKind == "diag" {
				for k := range rows {
					if rows[k].PID == p.PGID() {
						leaderRow = &rows[k]
					}
				}
			}
			rescueDecide(rec, p, orphanPid, childPid, token, "exitedTimeout", i, leaderRow)
			eofSeen, doneSeen, _, _, _ := diagWaitConverge(time.Now().Add(5*time.Second), eofCh, p.Done(), false, false)
			rec.Converged = eofSeen && doneSeen // 無 tExited 基準，不填 exitedToEOF／exitedToDone
			rec.AfterRescue = rec.Converged
			diagRecordCleanupKill(rec, killCh, time.Time{})
			if rec.Converged {
				ex := p.Wait()
				code := ex.Code
				rec.ExitCode = &code
				rec.FinalStatus = "converged"
			} else {
				// D1：逾時輪不等 sleep 30，記錄後立即交背景，於彙報階段的共用 reportDeadline 收斂或標記 pending。
				bg = append(bg, bgWait{rec: rec, eofCh: eofCh, doneCh: p.Done(), scanErr: func() error { tsMu.Lock(); defer tsMu.Unlock(); return scanErr }, killCh: killCh, tExited: time.Time{}})
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
				rows, pStart, pEnd, perr := diagPs(file)
				sum := diagSummarize(rows, p.PGID(), orphanPid, childPid, token, pStart, pEnd)
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
		diagRecordCleanupKill(rec, killCh, tExited)

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
		diagRecordCleanupKill(rec, killCh, tExited)
		if rec.Converged {
			ex := p.Wait()
			code := ex.Code
			rec.ExitCode = &code
			rec.FinalStatus = "converged"
		} else {
			bg = append(bg, bgWait{rec: rec, eofCh: eofCh, doneCh: p.Done(), scanErr: func() error { tsMu.Lock(); defer tsMu.Unlock(); return scanErr }, killCh: killCh, tExited: tExited})
		}

		finalizeIter(rec, i, func() error { tsMu.Lock(); defer tsMu.Unlock(); return scanErr }, beforePids)
		if rec.EOFTimeout || rec.DoneTimeout || rec.Anomaly || !rec.Converged {
			t.Logf("iter %d: eofTimeout=%v doneTimeout=%v anomaly=%v converged=%v rescue=%s", i, rec.EOFTimeout, rec.DoneTimeout, rec.Anomaly, rec.Converged, rec.Rescue.Mode)
		}
	}

	// 彙報階段：整批共用單一絕對期限 reportDeadline（預設 60s，DIAG_REPORT_DEADLINE 可覆寫，
	// 僅本機控制用；只建立一次，所有輪次共用）。EOF 與 Done 兩者皆到才是 finalConverged，
	// 否則記 pending（獨立計數，見下方 Fatalf 閘）；序列化後不再讀任何 channel。
	reportDeadline := time.Now().Add(reportDeadlineDur)
	for _, w := range bg {
		start := time.Now()
		eofSeen, doneSeen, pending, _, _, _ := diagReportAggregate(reportDeadline, w.eofCh, w.doneCh, false, false)
		if !pending {
			w.rec.FinalStatus = "finalConverged"
			w.rec.FinalWaitMs = diagMs(time.Since(start))
		} else {
			w.rec.FinalStatus = "pending"
			w.rec.Errors = append(w.rec.Errors, fmt.Sprintf("pending at report: eof=%v done=%v", eofSeen, doneSeen))
		}
		diagRecordCleanupKill(w.rec, w.killCh, w.tExited)
		diagClassifyRecordSnapshots(w.rec)
		if w.scanErr != nil {
			if serr := w.scanErr(); serr != nil {
				w.rec.Errors = append(w.rec.Errors, "scanner(late): "+serr.Error())
				invalid("iter %d scanner(late): %v", w.rec.Iter, serr)
			}
		}
		if b, jerr := json.Marshal(w.rec); jerr == nil {
			mustWrite(filepath.Join(outDir, fmt.Sprintf("iter-%d-final.json", w.rec.Iter)), b)
		} else {
			invalid("iter %d marshal final: %v", w.rec.Iter, jerr)
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
		diagClassifyRecordSnapshots(rec)
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
	pending := counts["pending"]
	summary := map[string]any{
		"iterations":           n,
		"runner":               runner,
		"goos":                 runtime.GOOS,
		"fixture":              fixtureKind,
		"fakeHang":             fakeHang,
		"mode":                 mode,
		"modeIgnored":          modeIgnored,
		"forceEOFTimeout":      forceEOFTimeout,
		"exitedTimeout":        exitedTimeout.String(),
		"reportDeadline":       reportDeadlineDur.String(),
		"counts":               counts,
		"pending":              pending,
		"invalidEvidenceCount": len(inv),
		"invalidEvidence":      inv,
	}
	if b, err := json.MarshalIndent(summary, "", "  "); err == nil {
		mustWrite(filepath.Join(outDir, "summary.json"), b)
	} else {
		invalid("marshal summary: %v", err)
	}
	evMu.Lock()
	inv = append([]string(nil), invalidEvidence...)
	evMu.Unlock()
	t.Logf("diag summary: %v pending=%d invalidEvidence=%d", counts, pending, len(inv))
	if len(inv) > 0 || pending > 0 {
		t.Fatalf("evidence incomplete or pending (invalidEvidence=%d pending=%d); artifacts kept in %s: invalidEvidence=%v", len(inv), pending, outDir, inv)
	}
}
