//go:build diag_orphan

// B2c-2 Task 2（layer 3——full path）暫時性診斷測試。
//
// **暫時性：本檔不合併進 main，B2c-2 結案後刪除。**
//
// 目的：在 internal/claude（真實 claude.Session）以既有測試
// TestOrphanDoesNotHangNormalExit 的實際順序重演，取代 layer 2b（自製 proc
// 探針＋真正 fixture）與原 layer 2（自製 proc 探針＋自製 fixture）之間仍未定位
// 的差異——本層涵蓋 claude.Session 所增加的完整路徑（stdin JSON 寫法、pump／
// Decode／channel、Wait 包裝），與真實測試控制流程順序相同的收斂形狀。本檔只忠實
// 記錄原始時序與快照，不下判斷；歸因由 plan 的解析器對 iter-records.jsonl 逐輪
// 套用。
//
// Env：
//   - DIAG_ITER（預設 3）：迭代次數。
//   - DIAG_OUT（預設 t.TempDir()）：輸出目錄，寫 iter-N-final.json／
//     iter-N-partial.json／iter-N-ps-<offset>.txt（raw ps 輸出）／
//     iter-records.jsonl／summary.json。
//   - DIAG_RUNNER（預設 "local"）：runner 標籤，寫入每筆 record。
//   - DIAG_REPORT_DEADLINE（預設 60s；僅本機控制用）：彙報階段整批共用的絕對
//     期限，只建立一次。
//   - DIAG_PS_TIMEOUT（預設 5s；僅本機控制用）：每次 ps 取樣的 CommandContext
//     期限，逾時保留起訖時刻並計入 invalidEvidence。
//   - DIAG_FAKE_HANG=1（正式 CI 不設）：多加 FAKE_HANG=1 進 fixture Env，讓
//     leader 收尾前 sleep 30——唯一用途是製造確定性的 pending 控制樣本。
//
// 排序契約（與 TestOrphanDoesNotHangNormalExit 控制流程順序相同）：
//   - 同一 worker goroutine 先 `for ev := range s.Events()` drain 至關閉，
//     再呼叫 `s.Wait()`；worker 不持有、不修改 record，也不設任何自己的期限，
//     只透過 cap-1 channel（eventsCh／waitCh）各送一筆不可變結果。
//   - 主 goroutine 保留既有測試的 5 秒 combined-completion checkpoint；到
//     checkpoint 只取證（checkpointHit、非阻塞讀 eventsCh／waitCh、
//     kill(-pgid,0) 原始 error、ps 快照），絕不 t.Fatal，worker 繼續自然收斂。
//   - oracle 取樣（kill0／ps 各自獨立 goroutine，互不等待）以「doneCh 關閉
//     時刻（收斂輪）或 checkpoint 時刻（逾時輪）」為同一個 monotonic base，
//     偏移各自獨立排程。
//   - record 只由主 goroutine 持有並序列化；worker 與 kill0／ps goroutine
//     都只透過 channel 回傳不可變結果。
//
// pending 與 invalidEvidence 分開計數，皆令測試以非零結束：pending＝彙報階段
// 整批共用的 reportDeadline 到期時該輪仍未同時觀察到 Events() 關閉與
// s.Wait() 返回——這是有效診斷結果，不是取證失敗；invalidEvidence 只計取證
// 失敗（Start／ps／JSON 編碼／artifact 寫入等）。
//
// 詮釋守則（供 plan 解析器使用，本檔本身不下判斷）：
//   - kill0 == nil（群組存在）只證明 PGID 仍存在，不證明其中程序狀態。
//   - ps 快照中的 Z 狀態只是支持性證據，不單獨證明任何結論。
//   - 本層看不到 proc.onSignal（cleanup KILL 的 callback），因此 Session
//     完成後仍觀察到 live group member，只能記「Session 完成後仍有 live
//     group member」，不得歸為 (b1)，也不得與 layer 2b 的 cleanup event 合併
//     歸因（不同程序、不同 iteration，沒有一對一同輪關係）。
//
// 本檔的 diagPs／diagEnvDur／invalid／mustWrite 等 helper 鏡自
// internal/proc/orphan_diag_test.go（B2c-2 Task 1），因套件不同（package
// claude 不可 import proc 套件的 unexported helper）而在此重新實作最小版本。
package claude

import (
	"context"
	"encoding/json"
	"errors"
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

	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// ---- helpers（鏡自 proc 探針，套件不同故重新實作最小版本） ----

func diagEnvDur(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

func diagMs(d time.Duration) *float64 { v := float64(d.Microseconds()) / 1000.0; return &v }

func diagMsSince(t0, t time.Time) *float64 {
	if t.IsZero() {
		return nil
	}
	return diagMs(t.Sub(t0))
}

var diagPsTimeout = diagEnvDur("DIAG_PS_TIMEOUT", 5*time.Second)

var diagKill0Offsets = []time.Duration{0, time.Millisecond, 10 * time.Millisecond, 100 * time.Millisecond, time.Second}
var diagPsOffsets = []time.Duration{0, 10 * time.Millisecond, 100 * time.Millisecond, time.Second}

// diagBoundedRecv 以單一 timer（不空轉）等待 ch 送出一筆值，逾時回傳 zero,false。
func diagBoundedRecv[T any](ch <-chan T, deadline time.Time) (T, bool) {
	var zero T
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case v := <-ch:
		return v, true
	case <-timer.C:
		return zero, false
	}
}

// diagPsRaw 執行 `ps -eo pgid,pid,ppid,stat,etime,command`；逾時由 DIAG_PS_TIMEOUT
// 控制（CommandContext），逾時保留起訖時刻並回傳 error。鏡自 proc 探針的 diagPs。
func diagPsRaw() (out []byte, start, end time.Time, err error) {
	start = time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), diagPsTimeout)
	defer cancel()
	out, cmdErr := exec.CommandContext(ctx, "ps", "-eo", "pgid,pid,ppid,stat,etime,command").CombinedOutput()
	if ctx.Err() != nil {
		cmdErr = fmt.Errorf("ps timeout after %s: %w", diagPsTimeout, ctx.Err())
	}
	end = time.Now()
	return out, start, end, cmdErr
}

type diagPsMember struct {
	PID     int    `json:"pid"`
	PPID    int    `json:"ppid"`
	Stat    string `json:"stat"`
	Command string `json:"command"`
}

// diagParsePsRows 在 Go 端依 pgid 過濾（不依賴 `ps -g`，`-eo` 已知在 macOS／Linux
// 皆可用，維持可攜）。
func diagParsePsRows(out []byte, pgid int) (rows []diagPsMember) {
	for i, line := range strings.Split(string(out), "\n") {
		if i == 0 || strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 6 {
			continue
		}
		gp, e1 := strconv.Atoi(f[0])
		pid, e2 := strconv.Atoi(f[1])
		ppid, e3 := strconv.Atoi(f[2])
		if e1 != nil || e2 != nil || e3 != nil || gp != pgid {
			continue
		}
		rows = append(rows, diagPsMember{PID: pid, PPID: ppid, Stat: f[3], Command: strings.Join(f[5:], " ")})
	}
	return rows
}

type diagPsSample struct {
	Offset          string         `json:"offset"`
	PsStart         string         `json:"psStart"`
	PsEnd           string         `json:"psEnd"`
	Members         []diagPsMember `json:"members,omitempty"`
	GroupStatZ      int            `json:"groupStatZ"`
	OrphanLikeCount int            `json:"orphanLikeCount"` // 命令列含 "sleep 30"（僅觀察）
	LiveMemberCount int            `json:"liveMemberCount"` // stat 不以 Z 開頭
	File            string         `json:"file,omitempty"`
	Err             string         `json:"err,omitempty"`
}

// diagPsSampleAt 取一次 ps 快照，寫 raw 輸出到 path，並依 pgid 過濾統計。
func diagPsSampleAt(pgid int, offset time.Duration, path string) diagPsSample {
	out, start, end, err := diagPsRaw()
	s := diagPsSample{Offset: offset.String(), PsStart: start.Format(time.RFC3339Nano), PsEnd: end.Format(time.RFC3339Nano)}
	if path != "" {
		if werr := os.WriteFile(path, out, 0o644); werr != nil && err == nil {
			err = fmt.Errorf("write %s: %w", path, werr)
		} else if werr == nil {
			s.File = filepath.Base(path)
		}
	}
	if err != nil {
		s.Err = err.Error()
		return s
	}
	for _, r := range diagParsePsRows(out, pgid) {
		s.Members = append(s.Members, r)
		if strings.HasPrefix(r.Stat, "Z") {
			s.GroupStatZ++
		} else {
			s.LiveMemberCount++
		}
		if strings.Contains(r.Command, "sleep 30") {
			s.OrphanLikeCount++
		}
	}
	return s
}

type diagKill0Sample struct {
	Offset string `json:"offset"`
	At     string `json:"at"`
	Err    string `json:"err"` // "nil" | "ESRCH" | 其他 errno 文字
}

// diagSampleKill0：對每個偏移以同一 monotonic base 排程，各自 time.Sleep(time.Until(...))
// 後立刻 kill(-pgid,0)，保存原始 error（nil／ESRCH／其他）。
func diagSampleKill0(pgid int, base time.Time) []diagKill0Sample {
	samples := make([]diagKill0Sample, 0, len(diagKill0Offsets))
	for _, off := range diagKill0Offsets {
		time.Sleep(time.Until(base.Add(off)))
		at := time.Now()
		err := syscall.Kill(-pgid, 0)
		s := diagKill0Sample{Offset: off.String(), At: at.Format(time.RFC3339Nano)}
		switch {
		case err == nil:
			s.Err = "nil"
		case errors.Is(err, syscall.ESRCH):
			s.Err = "ESRCH"
		default:
			s.Err = err.Error()
		}
		samples = append(samples, s)
	}
	return samples
}

// diagSamplePs：獨立於 kill0 之外排程，偏移較少（0／10ms／100ms／1s）。
func diagSamplePs(pgid int, base time.Time, outDir string, iter int) []diagPsSample {
	samples := make([]diagPsSample, 0, len(diagPsOffsets))
	for _, off := range diagPsOffsets {
		time.Sleep(time.Until(base.Add(off)))
		path := filepath.Join(outDir, fmt.Sprintf("iter-%d-ps-%s.txt", iter, off.String()))
		samples = append(samples, diagPsSampleAt(pgid, off, path))
	}
	return samples
}

// ---- worker 回傳的不可變結果（worker 不持有、不修改 record） ----

type diagEventsResult struct {
	tFirstEvent   time.Time
	tLastResult   time.Time
	tEventsClosed time.Time
	streamErrors  []string // KindStreamError 的錯誤字串（scanner／rpc）；非空＝該輪取證失敗（invalidEvidence）
}

type diagWaitResult struct {
	tWaitReturn time.Time
	exitCode    int
}

// diagReportAggregate 是彙報階段專用 aggregator：接受既有 eventsSeen／waitSeen
// 狀態，只對未 seen 的 channel 等待（單一 timer，不空轉）；兩者皆已 seen 立即
// 返回、不理會（甚至不建立）deadline timer。收到的值視為該筆之後唯一一次讀取。
func diagReportAggregate(deadline time.Time, eventsCh <-chan *diagEventsResult, waitCh <-chan *diagWaitResult, eventsSeen, waitSeen bool) (finalEventsSeen, finalWaitSeen, pending bool, eventsResult *diagEventsResult, waitResult *diagWaitResult) {
	finalEventsSeen, finalWaitSeen = eventsSeen, waitSeen
	if finalEventsSeen {
		eventsCh = nil
	}
	if finalWaitSeen {
		waitCh = nil
	}
	if eventsCh == nil && waitCh == nil {
		return finalEventsSeen, finalWaitSeen, false, nil, nil
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	for eventsCh != nil || waitCh != nil {
		select {
		case v := <-eventsCh:
			eventsResult = v
			finalEventsSeen = true
			eventsCh = nil
		case v := <-waitCh:
			waitResult = v
			finalWaitSeen = true
			waitCh = nil
		case <-timer.C:
			pending = !(finalEventsSeen && finalWaitSeen)
			return
		}
	}
	pending = !(finalEventsSeen && finalWaitSeen)
	return
}

// ---- record（只由主 goroutine 持有與序列化） ----

type diagFullRecord struct {
	Iter                   int               `json:"iter"`
	Runner                 string            `json:"runner"`
	GOOS                   string            `json:"goos"`
	Fixture                string            `json:"fixture"`
	FakeHang               bool              `json:"fakeHang"`
	AmbientFakeVars        []string          `json:"ambientFakeVars,omitempty"`
	PGID                   int               `json:"pgid"`
	T0                     string            `json:"t0"`
	TFirstEventMs          *float64          `json:"tFirstEventMs,omitempty"`
	TLastResultMs          *float64          `json:"tLastResultMs,omitempty"`
	TEventsClosedMs        *float64          `json:"tEventsClosedMs,omitempty"`
	TWaitReturnMs          *float64          `json:"tWaitReturnMs,omitempty"`
	ExitCode               *int              `json:"exitCode,omitempty"`
	CheckpointHit          bool              `json:"checkpointHit"`
	EventsSeenAtCheckpoint bool              `json:"eventsSeenAtCheckpoint"`
	WaitSeenAtCheckpoint   bool              `json:"waitSeenAtCheckpoint"`
	Kill0                  []diagKill0Sample `json:"kill0"`
	Ps                     []diagPsSample    `json:"ps"`
	StreamErrorCount       int               `json:"streamErrorCount"`
	StreamErrors           []string          `json:"streamErrors,omitempty"` // KindStreamError 內容；計入 invalidEvidence，不得算有效收斂
	FinalStatus            string            `json:"finalStatus"`            // converged | pending | finalConverged | aborted
	FinalWaitMs            *float64          `json:"finalWaitMs,omitempty"`
	Errors                 []string          `json:"errors,omitempty"`

	t0 time.Time // 不序列化：主 goroutine 內部計算 msSince 用
}

func (rec *diagFullRecord) applyEvents(er *diagEventsResult) {
	if er == nil {
		return
	}
	rec.TFirstEventMs = diagMsSince(rec.t0, er.tFirstEvent)
	rec.TLastResultMs = diagMsSince(rec.t0, er.tLastResult)
	rec.TEventsClosedMs = diagMsSince(rec.t0, er.tEventsClosed)
	rec.StreamErrorCount = len(er.streamErrors)
	rec.StreamErrors = append([]string(nil), er.streamErrors...)
}

func (rec *diagFullRecord) applyWait(wr *diagWaitResult) {
	if wr == nil {
		return
	}
	rec.TWaitReturnMs = diagMsSince(rec.t0, wr.tWaitReturn)
	code := wr.exitCode
	rec.ExitCode = &code
}

// TestDiagOrphanFullPath：layer 3——full path（claude.Start → drain → Wait），
// 與 TestOrphanDoesNotHangNormalExit 控制流程順序相同的收斂順序。只做取證，不以
// 時間作為通過條件（除既有測試本身的 5 秒 checkpoint 只取證不 t.Fatal）。
func TestDiagOrphanFullPath(t *testing.T) {
	n := 3
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
	fakeHang := os.Getenv("DIAG_FAKE_HANG") == "1"
	fakeBadLine := os.Getenv("DIAG_FAKE_BADLINE") == "1" // 診斷專用控制：驗證 KindStreamError → invalidEvidence 路徑；正式 CI 不設
	reportDeadlineDur := diagEnvDur("DIAG_REPORT_DEADLINE", 60*time.Second)

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

	// 父環境原有的 FAKE_* 名稱只揭露，不驅動任何判定（六個空值遮蔽同一批名稱）。
	var ambientFakeVars []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "FAKE_") {
			if k, _, ok := strings.Cut(kv, "="); ok {
				ambientFakeVars = append(ambientFakeVars, k)
			}
		}
	}

	t.Logf("diag(full-path): iterations=%d outDir=%s runner=%s goos=%s fakeHang=%v fakeBadLine=%v reportDeadline=%s ambientFakeVars=%v",
		n, outDir, runner, runtime.GOOS, fakeHang, fakeBadLine, reportDeadlineDur, ambientFakeVars)

	type bgItem struct {
		rec        *diagFullRecord
		eventsCh   chan *diagEventsResult
		waitCh     chan *diagWaitResult
		eventsSeen bool
		waitSeen   bool
	}

	records := make([]*diagFullRecord, 0, n)
	var bg []bgItem

	writeFinal := func(rec *diagFullRecord) {
		if b, jerr := json.Marshal(rec); jerr == nil {
			mustWrite(filepath.Join(outDir, fmt.Sprintf("iter-%d-final.json", rec.Iter)), b)
		} else {
			invalid("iter %d marshal final: %v", rec.Iter, jerr)
		}
	}
	writePartial := func(rec *diagFullRecord) {
		if b, jerr := json.Marshal(rec); jerr == nil {
			mustWrite(filepath.Join(outDir, fmt.Sprintf("iter-%d-partial.json", rec.Iter)), b)
		} else {
			invalid("iter %d marshal partial: %v", rec.Iter, jerr)
		}
	}
	fmtMs := func(v *float64) string {
		if v == nil {
			return "-"
		}
		return fmt.Sprintf("%.2f", *v)
	}

	for i := 0; i < n; i++ {
		rec := &diagFullRecord{Iter: i, Runner: runner, GOOS: runtime.GOOS, Fixture: "fake-claude(full-path)",
			FakeHang: fakeHang, AmbientFakeVars: ambientFakeVars, FinalStatus: "pending"}
		records = append(records, rec)

		// 有效的非空 fixture 開關僅 FAKE_ORPHAN=1；六個空值遮蔽 ambient FAKE_*
		// （proc.Start 以 append(os.Environ(), cfg.Env...) 傳環境，os/exec 去重保留
		// 最後一筆）。只有 DIAG_FAKE_HANG=1 才再覆寫 FAKE_HANG=1。
		env := []string{"FAKE_MULTI=", "FAKE_STDERR=", "FAKE_DIE=", "FAKE_BADLINE=", "FAKE_HANG=", "FAKE_EXIT=", "FAKE_ORPHAN=1"}
		if fakeHang {
			env = append(env, "FAKE_HANG=1")
		}
		if fakeBadLine {
			env = append(env, "FAKE_BADLINE=1")
		}
		cfg := fakeCfg(t, env...)
		if fakeBadLine {
			cfg.MaxLineBytes = 1024 // 與 TestScannerErrorSurfaced 相同：4096 位元組行 → scanner error → KindStreamError
		}

		// 1. Start；失敗即 invalid + aborted，不進入取樣。
		s, err := Start(context.Background(), cfg)
		if err != nil {
			rec.Errors = append(rec.Errors, "start: "+err.Error())
			rec.FinalStatus = "aborted"
			invalid("iter %d Start failed: %v", i, err)
			writeFinal(rec)
			t.Logf("iter %d: aborted (start failed): %v", i, err)
			continue
		}
		t0 := time.Now()
		rec.t0 = t0
		rec.T0 = t0.Format(time.RFC3339Nano)
		pgid := s.PGID()
		rec.PGID = pgid

		eventsCh := make(chan *diagEventsResult, 1)
		waitCh := make(chan *diagWaitResult, 1)
		doneCh := make(chan struct{})

		// 2. worker goroutine：與既有測試相同的一條——先 drain 至 Events() 關閉，
		// 再 Wait()。worker 不持有、不修改 record，也不設任何自己的期限。
		go func() {
			var er diagEventsResult
			first := true
			for ev := range s.Events() {
				now := time.Now()
				if first {
					er.tFirstEvent = now
					first = false
				}
				if ev.Kind == contract.KindResult {
					er.tLastResult = now
				}
				if ev.Kind == contract.KindStreamError { // 傳輸層錯誤：帶回主 goroutine 計入 invalidEvidence，worker 繼續 drain
					msg := string(ev.Raw)
					if ev.Err != nil {
						msg = ev.Err.Error()
					}
					er.streamErrors = append(er.streamErrors, msg)
				}
			}
			er.tEventsClosed = time.Now()
			eventsCh <- &er

			ex := s.Wait()
			wr := diagWaitResult{tWaitReturn: time.Now(), exitCode: ex.Code}
			waitCh <- &wr

			close(doneCh)
		}()

		// 3. 5 秒 combined-completion checkpoint——與既有測試相同的形狀，到時只
		// 取證、絕不 t.Fatal。base 是 doneCh 關閉時刻（收斂輪）或 checkpoint
		// 時刻（逾時輪），供下面 oracle 取樣（item 4）使用同一個 monotonic base。
		var base time.Time
		var eventsSeen, waitSeen bool
		var er *diagEventsResult
		var wr *diagWaitResult
		select {
		case <-doneCh:
			base = time.Now()
			// doneCh 關閉前兩筆已送出（cap-1 buffered），保證非阻塞可讀。
			er = <-eventsCh
			wr = <-waitCh
			eventsSeen, waitSeen = true, true
		case <-time.After(5 * time.Second):
			base = time.Now()
			rec.CheckpointHit = true
			select {
			case v := <-eventsCh:
				er = v
				eventsSeen = true
				rec.EventsSeenAtCheckpoint = true
			default:
			}
			select {
			case v := <-waitCh:
				wr = v
				waitSeen = true
				rec.WaitSeenAtCheckpoint = true
			default:
			}
		}
		rec.applyEvents(er)
		rec.applyWait(wr)
		for _, se := range rec.StreamErrors {
			invalid("iter %d stream error: %s", i, se)
		}

		// 4. oracle 取樣：kill0 與 ps 各自獨立 goroutine，互不等待，結果經 channel
		// 回主 goroutine（唯一寫入 record 者）。以報告期限風格的單一 timer 界定
		// 上限（兩者實際約 1 秒內完成，界定只為避免真正卡死時無界等待）。
		kill0Ch := make(chan []diagKill0Sample, 1)
		psCh := make(chan []diagPsSample, 1)
		go func() { kill0Ch <- diagSampleKill0(pgid, base) }()
		go func() { psCh <- diagSamplePs(pgid, base, outDir, i) }()
		kill0Deadline := time.Now().Add(3 * time.Second)
		psDeadline := time.Now().Add(time.Second + time.Duration(len(diagPsOffsets))*diagPsTimeout + 3*time.Second)
		if v, ok := diagBoundedRecv(kill0Ch, kill0Deadline); ok {
			rec.Kill0 = v
		} else {
			invalid("iter %d kill0 sampling goroutine did not return within bound", i)
		}
		if v, ok := diagBoundedRecv(psCh, psDeadline); ok {
			rec.Ps = v
			for _, p := range v {
				if p.Err != "" {
					invalid("iter %d ps@%s: %s", i, p.Offset, p.Err)
				}
			}
		} else {
			invalid("iter %d ps sampling goroutine did not return within bound", i)
		}

		if !rec.CheckpointHit {
			// 收斂輪：eventsSeen／waitSeen 必為 true（doneCh 關閉前兩筆已送出）。
			rec.FinalStatus = "converged"
			writeFinal(rec)
		} else {
			// 5. 逾時輪：記錄後立即進入下一輪；未收斂的 channel／worker 交背景，
			// 統一由彙報階段（item 6）的共用 reportDeadline 收斂或標記 pending。
			writePartial(rec)
			bg = append(bg, bgItem{rec: rec, eventsCh: eventsCh, waitCh: waitCh, eventsSeen: eventsSeen, waitSeen: waitSeen})
		}

		kill0At0 := "-"
		if len(rec.Kill0) > 0 {
			kill0At0 = rec.Kill0[0].Err
		}
		psLive0, psZ0 := 0, 0
		if len(rec.Ps) > 0 {
			psLive0 = rec.Ps[0].LiveMemberCount
			psZ0 = rec.Ps[0].GroupStatZ
		}
		t.Logf("iter %d: converged=%v checkpointHit=%v eventsClosedMs=%s waitReturnMs=%s kill0@0=%s ps@0 live=%d Z=%d",
			i, rec.FinalStatus == "converged", rec.CheckpointHit, fmtMs(rec.TEventsClosedMs), fmtMs(rec.TWaitReturnMs), kill0At0, psLive0, psZ0)
	}

	// 6. 彙報：整批共用單一絕對期限，只建立一次；逐輪把既有 eventsSeen／waitSeen
	// 狀態交給 aggregator，只對未 seen 的 channel 等待；序列化後不再讀任何 channel。
	reportDeadline := time.Now().Add(reportDeadlineDur)
	for _, w := range bg {
		start := time.Now()
		_, _, pending, er, wr := diagReportAggregate(reportDeadline, w.eventsCh, w.waitCh, w.eventsSeen, w.waitSeen)
		w.rec.applyEvents(er)
		w.rec.applyWait(wr)
		for _, se := range w.rec.StreamErrors {
			invalid("iter %d stream error(late): %s", w.rec.Iter, se)
		}
		if !pending {
			w.rec.FinalStatus = "finalConverged"
		} else {
			w.rec.FinalStatus = "pending"
			w.rec.Errors = append(w.rec.Errors, "pending at report: reportDeadline exceeded before Events()-close and Wait()-return both observed")
		}
		w.rec.FinalWaitMs = diagMs(time.Since(start))
		writeFinal(w.rec)
	}

	// 每輪只序列化一次進 JSONL；任何寫入／編碼錯誤都計入 invalidEvidence。
	f, err := os.Create(filepath.Join(outDir, "iter-records.jsonl"))
	if err != nil {
		t.Fatalf("create iter-records.jsonl: %v", err)
	}
	enc := json.NewEncoder(f)
	counts := map[string]int{}
	kill0At0Counts := map[string]int{"nil": 0, "ESRCH": 0, "other": 0}
	anyZIters := 0
	anyLiveAfterCompletionIters := 0
	for _, rec := range records {
		if err := enc.Encode(rec); err != nil {
			invalid("encode iter %d: %v", rec.Iter, err)
		}
		counts[rec.FinalStatus]++
		if rec.CheckpointHit {
			counts["checkpointHit"]++
		}
		if len(rec.Kill0) > 0 {
			switch rec.Kill0[0].Err {
			case "nil":
				kill0At0Counts["nil"]++
			case "ESRCH":
				kill0At0Counts["ESRCH"]++
			default:
				kill0At0Counts["other"]++
			}
		}
		sawZ := false
		for _, p := range rec.Ps {
			if p.GroupStatZ > 0 {
				sawZ = true
			}
		}
		if sawZ {
			anyZIters++
		}
		// 只有「收斂輪」的 base 保證在 Session 完成（Events() 關閉且 Wait()
		// 返回）之後；checkpoint 輪的取樣時刻早於完成，不計入本統計（見檔頭
		// 詮釋守則：不得跨 layer／跨時序合併歸因）。
		if rec.FinalStatus == "converged" {
			for _, p := range rec.Ps {
				if p.LiveMemberCount > 0 {
					anyLiveAfterCompletionIters++
					break
				}
			}
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
		"iterations":                        n,
		"runner":                            runner,
		"goos":                              runtime.GOOS,
		"fixture":                           "fake-claude(full-path)",
		"fakeHang":                          fakeHang,
		"fakeBadLine":                       fakeBadLine,
		"ambientFakeVars":                   ambientFakeVars,
		"reportDeadline":                    reportDeadlineDur.String(),
		"counts":                            counts,
		"kill0At0":                          kill0At0Counts,
		"anyZSeenIters":                     anyZIters,
		"anyLiveMemberAfterCompletionIters": anyLiveAfterCompletionIters,
		"pending":                           pending,
		"invalidEvidenceCount":              len(inv),
		"invalidEvidence":                   inv,
	}
	if b, merr := json.MarshalIndent(summary, "", "  "); merr == nil {
		mustWrite(filepath.Join(outDir, "summary.json"), b)
	} else {
		invalid("marshal summary: %v", merr)
	}

	evMu.Lock()
	inv = append([]string(nil), invalidEvidence...)
	evMu.Unlock()
	t.Logf("diag(full-path) summary: %v pending=%d invalidEvidence=%d", counts, pending, len(inv))
	if len(inv) > 0 || pending > 0 {
		t.Fatalf("evidence incomplete or pending (invalidEvidence=%d pending=%d); artifacts kept in %s: invalidEvidence=%v", len(inv), pending, outDir, inv)
	}
}

// TestDiagFullPathReportAggregator：diagReportAggregate 的 helper control。
func TestDiagFullPathReportAggregator(t *testing.T) {
	t.Run("never delivers, deadline pending, no spin", func(t *testing.T) {
		eventsCh := make(chan *diagEventsResult)
		waitCh := make(chan *diagWaitResult)
		deadline := time.Now().Add(300 * time.Millisecond)
		start := time.Now()
		eventsSeen, waitSeen, pending, er, wr := diagReportAggregate(deadline, eventsCh, waitCh, false, false)
		elapsed := time.Since(start)
		if eventsSeen || waitSeen || !pending || er != nil || wr != nil {
			t.Fatalf("expected eventsSeen=false waitSeen=false pending=true nil results, got eventsSeen=%v waitSeen=%v pending=%v er=%v wr=%v",
				eventsSeen, waitSeen, pending, er, wr)
		}
		if elapsed < 250*time.Millisecond || elapsed > time.Second {
			t.Fatalf("did not return near deadline: elapsed=%s", elapsed)
		}
	})

	t.Run("eventsSeen already true waits only for waitCh", func(t *testing.T) {
		eventsCh := make(chan *diagEventsResult)
		waitCh := make(chan *diagWaitResult)
		deadline := time.Now().Add(300 * time.Millisecond)
		start := time.Now()
		eventsSeen, waitSeen, pending, _, wr := diagReportAggregate(deadline, eventsCh, waitCh, true, false)
		elapsed := time.Since(start)
		if !eventsSeen || waitSeen || !pending || wr != nil {
			t.Fatalf("expected eventsSeen=true waitSeen=false pending=true wr=nil, got eventsSeen=%v waitSeen=%v pending=%v wr=%v",
				eventsSeen, waitSeen, pending, wr)
		}
		if elapsed < 250*time.Millisecond || elapsed > time.Second {
			t.Fatalf("did not return near deadline: elapsed=%s", elapsed)
		}
	})

	t.Run("both already seen returns immediately regardless of deadline", func(t *testing.T) {
		eventsCh := make(chan *diagEventsResult)
		waitCh := make(chan *diagWaitResult)
		start := time.Now()
		eventsSeen, waitSeen, pending, er, wr := diagReportAggregate(time.Now().Add(24*time.Hour), eventsCh, waitCh, true, true)
		elapsed := time.Since(start)
		if !eventsSeen || !waitSeen || pending || er != nil || wr != nil {
			t.Fatalf("expected eventsSeen=true waitSeen=true pending=false nil results, got eventsSeen=%v waitSeen=%v pending=%v",
				eventsSeen, waitSeen, pending)
		}
		if elapsed > 50*time.Millisecond {
			t.Fatalf("did not return immediately: elapsed=%s", elapsed)
		}
	})

	t.Run("value arrives on waitCh before deadline", func(t *testing.T) {
		eventsCh := make(chan *diagEventsResult)
		waitCh := make(chan *diagWaitResult, 1)
		want := &diagWaitResult{tWaitReturn: time.Now(), exitCode: 0}
		go func() {
			time.Sleep(50 * time.Millisecond)
			waitCh <- want
		}()
		deadline := time.Now().Add(300 * time.Millisecond)
		eventsSeen, waitSeen, pending, _, wr := diagReportAggregate(deadline, eventsCh, waitCh, true, false)
		if !eventsSeen || !waitSeen || pending || wr != want {
			t.Fatalf("expected eventsSeen=true waitSeen=true pending=false wr=want, got eventsSeen=%v waitSeen=%v pending=%v wr=%v",
				eventsSeen, waitSeen, pending, wr)
		}
	})
}
