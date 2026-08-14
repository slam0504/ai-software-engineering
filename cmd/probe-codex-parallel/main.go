// probe-codex-parallel：M3b Task 0 的 live probe（NO-GO gate）。
//
// 驗證前提：單一 codex app-server 能同時承載多個 thread 的並行 turn。
// 判定範圍（凍結）：
//   (a) 兩 thread 並行 turn 是否真並行（turn 生命期重疊，非 A 全部完成才出現 B）
//   (b) notification 與 approval request 是否帶足以歸屬的 thread／turn identity
//   (c) 自然與強制（-force）兩種收尾是否 bounded 收斂且錄到最後一筆 frame
//
// completed-before-response 不列入本 probe（改由 Task 9 fake-wire 測試鎖住）。
//
// 全程走 production API（codex.StartAppServer / ThreadRunner / Conn），
// 不另建 wire 路徑；wire log 即證據。
//
// 退出碼：0 = 全部通過；1 = probe 執行失敗（環境／API 錯誤）；2 = probe 跑完但判定 NO-GO。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/codex"
)

var (
	codexBin = flag.String("codex-bin", "", "bundled codex binary 路徑（必填）")
	force    = flag.Bool("force", false, "強制收尾分支：turn 進行中直接 Terminate")
	// 補充 run 專用旗標——不影響凍結參數的兩次主 run。
	longOutput = flag.Bool("long-output", false,
		"補充 run：以長輸出 prompt 取代 promptA/promptB，觀察輸出階段是否交錯（非凍結參數）")
)

// 凍結參數
const (
	probeTimeout   = 90 * time.Second
	turnTimeout    = 60 * time.Second
	approvalPolicy = "untrusted" // 凍結：與 production 預設一致，確保會觸發核可
	promptA        = "請只回覆字串 PROBE_A_DONE，不要使用任何工具。"
	promptB        = "請只回覆字串 PROBE_B_DONE，不要使用任何工具。"
	// 第三個 turn 刻意觸發核可——(b) 的 approval 歸屬必須有實際 frame 才能驗
	promptApproval = "請在目前工作目錄建立檔案 probe-approval.txt，內容為 PROBE。"
)

// 補充 run 的 prompt（**非凍結參數**）：只在 -long-output 時取代 promptA／promptB，
// 用來觀察兩 thread 的模型輸出階段（agentMessage delta）是否真的交錯。
const (
	promptALongOutput = "請從 1 數到 60，每個數字一行，不要使用任何工具。"
	promptBLongOutput = "請從 101 數到 160，每個數字一行，不要使用任何工具。"
)

// frameRec 是 wire log 每筆 frame 的分析用摘要（raw frame 另存 jsonl）。
type frameRec struct {
	Seq      int
	At       time.Time
	Dir      string
	Kind     string // request / response / notification
	Method   string
	ThreadID string
	TurnID   string
}

type turnEnd struct {
	turnID   string
	status   string
	errMsg   string // turn.error.message（server 端失敗，例如 usageLimitExceeded）
	viaBcast bool   // 無 threadId 可歸屬，只能廣播
}

type turnResult struct {
	label    string
	threadID string
	turnID   string
	status   string // completed / failed / timeout / error / server-died
	errText  string
	note     string
	started  time.Time
	ended    time.Time
}

type apprRec struct {
	At       time.Time
	Method   string
	ThreadID string
	TurnID   string
	ItemID   string
	Decision string
	Raw      string
}

// analysis 是 report() 算出的自動指標，供 gate 強制判定使用。
type analysis struct {
	overlap          string // yes / no / inconclusive —— (a) turn 生命期重疊
	overlapWhy       string
	deltaInterleaved string // yes / no / inconclusive —— 輸出階段是否交錯（觀察用，不入 gate）
	approvals        int
	apprMissingID    int
	broadcast        bool
}

type probe struct {
	mu       sync.Mutex
	seq      int
	frames   []frameRec
	appr     []apprRec
	waiters  map[string]chan turnEnd
	noIdent  bool // 曾出現無法歸屬的 turn/completed
	notifIDs struct {
		total       int
		withThread  int
		withTurn    int
		missingBoth []string // method 名（去重前）
	}
	wireLog *os.File
}

// ---- cleanup registry：任何退出路徑（含 NO-GO）都必須收掉子行程與暫存目錄 ----

var (
	cleanupMu   sync.Mutex
	cleanups    []func()
	cleanupDone bool
)

func addCleanup(f func()) {
	cleanupMu.Lock()
	cleanups = append(cleanups, f)
	cleanupMu.Unlock()
}

// runCleanups 反序執行並且只執行一次（正常路徑與 fatal／nogo 路徑共用）。
func runCleanups() {
	cleanupMu.Lock()
	if cleanupDone {
		cleanupMu.Unlock()
		return
	}
	cleanupDone = true
	fs := cleanups
	cleanupMu.Unlock()
	for i := len(fs) - 1; i >= 0; i-- {
		fs[i]()
	}
}

// terminateAt 記錄**第一次** Terminate 的時刻——forced 模式的第一次 Terminate 發生在
// 並行段中途，收尾段的第二次是 no-op，量它等於量錯指標（I4）。
var (
	termOnce sync.Once
	termAt   time.Time
)

func terminate(srv *codex.Server) {
	termOnce.Do(func() { termAt = time.Now() })
	_ = srv.Terminate()
}

func main() {
	flag.Parse()
	if *codexBin == "" {
		fatal("必須以 -codex-bin 指定 bundled binary")
	}
	mode := "natural"
	if *longOutput {
		mode = "natural-long"
	}
	if *force {
		mode = "forced"
		if *longOutput {
			mode = "forced-long"
		}
	}
	pA, pB := promptA, promptB
	if *longOutput {
		pA, pB = promptALongOutput, promptBLongOutput
	}

	tmp, err := os.MkdirTemp("", "probe-codex-*")
	must(err)
	addCleanup(func() { _ = os.RemoveAll(tmp) })

	// wire log 落在 tmp 之外——tmp 於離開時整個刪除，證據必須留存。
	wirePath := filepath.Join(os.TempDir(), "probe-codex-parallel-"+mode+".jsonl")
	wireLog, err := os.Create(wirePath)
	must(err)
	addCleanup(func() { _ = wireLog.Close() })

	p := &probe{waiters: map[string]chan turnEnd{}, wireLog: wireLog}

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	srv, err := codex.StartAppServer(ctx, codex.Config{
		Binary: *codexBin, CWD: tmp, TermGrace: 5 * time.Second,
	})
	must(err)

	// 收尾（Terminate → Wait → StopRecording）只跑一次；正常路徑與 fatal／nogo 共用同一份實作，
	// 因此任何退出路徑都不會留下孤兒 app-server。
	var (
		shutOnce           sync.Once
		exitCode           int
		stderrTail         string
		doneAfterTerminate time.Duration
		recErr             error
		shutdownRan        bool
	)
	shutdown := func() {
		shutOnce.Do(func() {
			terminate(srv)
			ex := srv.Wait() // Wait 回傳時 Done 已關閉（supervisor 收尾完成、Exit 已快取）
			doneAfterTerminate = time.Since(termAt)
			recErr = srv.StopRecording()
			exitCode, stderrTail, shutdownRan = ex.Code, ex.StderrTail, true
		})
	}
	addCleanup(shutdown)

	must(srv.BeginRecording(p.sink)) // 全程錄流即證據

	conn := srv.Conn()
	conn.OnNotification(p.onNotification)
	conn.OnServerRequest(p.onServerRequest) // 一律拒絕核可
	conn.OnUnknown(func([]byte) {})         // raw 已在 sink 錄下

	must(srv.Handshake(ctx, codex.ClientInfo{Name: "probe-codex-parallel"}))

	// (b) 受控 approval turn 先跑——server 必須仍活著才能開 thread。
	//     -force 會在 (a) 途中終止 server，故此項一律排在並行段之前。
	apprRunner, err := mustStartThread(ctx, srv)
	if err != nil {
		nogoExit("approval thread 無法建立: %v", err)
	}
	appr := p.runTurnDenyingApprovals(ctx, srv, apprRunner, "APPROVAL", promptApproval)
	if _, serr := os.Stat(filepath.Join(tmp, "probe-approval.txt")); serr == nil {
		nogoExit("核可被拒仍寫入檔案 probe-approval.txt")
	}

	// (a) 兩 thread 並行送 turn
	runnerA, errA := mustStartThread(ctx, srv)
	runnerB, errB := mustStartThread(ctx, srv)
	if errA != nil {
		nogoExit("thread A 無法建立: %v", errA)
	}
	if errB != nil {
		nogoExit("第二個 thread 被拒（單 app-server 無法承載多 thread）: %v", errB)
	}
	thA, thB := runnerA.ThreadID(), runnerB.ThreadID()
	fmt.Printf("MODE %s promptA=%q promptB=%q\n", mode, pA, pB)
	fmt.Printf("threads approval=%s A=%s B=%s\n", apprRunner.ThreadID(), thA, thB)

	var wg sync.WaitGroup
	res := make([]turnResult, 2)
	type pair struct {
		label  string
		runner *codex.ThreadRunner
		prompt string
	}
	for i, pr := range []pair{{"A", runnerA, pA}, {"B", runnerB, pB}} {
		wg.Add(1)
		go func(i int, pr pair) {
			defer wg.Done()
			res[i] = p.runTurn(ctx, srv, pr.runner, pr.label, pr.prompt)
		}(i, pr)
	}
	if *force { // 強制收尾：不等 turn 完成（只影響 (a) 與 (c)，approval 已驗完）
		time.Sleep(2 * time.Second) // 讓兩個 turn 真的送上 wire，否則測不到「turn 進行中終止」
		terminate(srv)
	}
	wg.Wait()

	an := p.report(mode, res, appr, thA, thB, wirePath)

	// (c) 收尾：Terminate → Wait → StopRecording（doneAfterTerminate 自**第一次** Terminate 起算）
	shutdown()
	p.mu.Lock()
	lastSeq, lastAt := p.seq, time.Time{}
	if n := len(p.frames); n > 0 {
		lastAt = p.frames[n-1].At
	}
	p.mu.Unlock()

	fmt.Printf("exit_code=%d stderr_tail=%q\n", exitCode, tail(stderrTail, 400))
	fmt.Printf("SHUTDOWN mode=%s ran=%v bounded=%v done_after_first_terminate=%s record_err=%v last_frame_seq=%d last_frame_at=%s\n",
		mode, shutdownRan, doneAfterTerminate < 10*time.Second,
		doneAfterTerminate.Round(time.Millisecond), recErr, lastSeq, ts(lastAt))
	fmt.Printf("WIRE_LOG %s\n", wirePath)

	// **先**分出「probe 執行失敗」與「判定 NO-GO」——這是兩件事，不可混為一談。
	// server 端 turn 失敗（usageLimitExceeded、模型錯誤…）是環境問題，不是架構前提不成立，
	// 不得報成 NO-GO，也不得報成 GO。
	var execFail []string
	for _, r := range append([]turnResult{appr}, res...) {
		if r.status != "completed" {
			if *force && (r.label == "A" || r.label == "B") {
				continue // forced 模式刻意中斷並行 turn，其未完成是預期行為
			}
			execFail = append(execFail, fmt.Sprintf("turn %s status=%s err=%q", r.label, r.status, r.errText))
		}
	}
	if len(execFail) > 0 {
		fmt.Println("PROBE EXECUTION FAILED（環境／server 問題，非 GO/NO-GO 判定）")
		for _, e := range execFail {
			fmt.Println("  - " + e)
		}
		runCleanups()
		os.Exit(1)
	}

	// GO 條件由 driver 強制（I2）：作為 gate 工具，最關鍵的 NO-GO 不靠人眼把關。
	var reasons []string
	if an.approvals == 0 {
		reasons = append(reasons, "(b) 未觀察到任何 approval request")
	}
	if an.apprMissingID > 0 {
		reasons = append(reasons, fmt.Sprintf("(b) %d 筆 approval request 缺 threadId／turnId", an.apprMissingID))
	}
	if an.broadcast {
		reasons = append(reasons, "(b) turn/completed 缺 threadId，只能靠廣播歸屬")
	}
	if !*force && an.overlap != "yes" {
		reasons = append(reasons, "(a) 兩 thread 的 turn 生命期未重疊："+an.overlapWhy)
	}
	if recErr != nil {
		reasons = append(reasons, fmt.Sprintf("(c) 錄流回報錯誤: %v", recErr))
	}
	if !(doneAfterTerminate < 10*time.Second) {
		reasons = append(reasons, "(c) 收尾未於 10s 內 bounded 收斂")
	}
	if len(reasons) > 0 {
		fmt.Println("GATE NO-GO")
		for _, r := range reasons {
			fmt.Println("  - " + r)
		}
		runCleanups()
		os.Exit(2)
	}
	fmt.Println("GATE GO")
	runCleanups()
}

// ---- thread / turn helpers（依 internal/codex 真實 API 實作）----

// mustStartThread 在既有 app-server 上開一個新 thread（production 路徑：
// NewThreadRunner + EnsureThread）。第二個以後失敗即代表單 server 撐不住多 thread。
func mustStartThread(ctx context.Context, srv *codex.Server) (*codex.ThreadRunner, error) {
	r := codex.NewThreadRunner(srv.Conn())
	if _, err := r.EnsureThread(ctx, "", approvalPolicy); err != nil {
		return nil, err
	}
	return r, nil
}

// runTurnDenyingApprovals 與 runTurn 同一路徑；核可拒絕由 conn 層 handler
// （p.onServerRequest）統一處理，本 helper 額外標示這一輪預期會收到 approval。
func (p *probe) runTurnDenyingApprovals(ctx context.Context, srv *codex.Server,
	r *codex.ThreadRunner, label, prompt string) turnResult {
	return p.runTurn(ctx, srv, r, label, prompt)
}

func (p *probe) runTurn(ctx context.Context, srv *codex.Server,
	r *codex.ThreadRunner, label, prompt string) turnResult {
	out := turnResult{label: label, threadID: r.ThreadID(), started: time.Now()}
	ch := p.register(out.threadID)
	defer p.unregister(out.threadID)

	tctx, cancel := context.WithTimeout(ctx, turnTimeout)
	defer cancel()

	turnID, alreadyEnded, err := r.StartTurn(tctx, prompt)
	out.turnID = turnID
	if err != nil {
		out.status, out.errText, out.ended = "error", err.Error(), time.Now()
		return out
	}
	if alreadyEnded { // completed 先於 response（earlyEnded latch 已對消）
		out.status, out.ended, out.note = "completed", time.Now(), "completed-before-response"
		return out
	}
	for {
		select {
		case te := <-ch:
			if te.turnID != "" && turnID != "" && te.turnID != turnID {
				continue // 同 thread 的舊 turn 收尾，不是這一輪
			}
			out.status, out.ended, out.errText = te.status, time.Now(), te.errMsg
			if te.viaBcast {
				out.note = "turn/completed 無 threadId，只能靠廣播歸屬"
			}
			return out
		case <-tctx.Done():
			out.status, out.errText, out.ended = "timeout", tctx.Err().Error(), time.Now()
			return out
		case <-srv.Done():
			out.status, out.ended = "server-died", time.Now()
			return out
		}
	}
}

func (p *probe) register(threadID string) chan turnEnd {
	ch := make(chan turnEnd, 8)
	p.mu.Lock()
	p.waiters[threadID] = ch
	p.mu.Unlock()
	return ch
}

func (p *probe) unregister(threadID string) {
	p.mu.Lock()
	delete(p.waiters, threadID)
	p.mu.Unlock()
}

// ---- wire 錄流與 handler ----

// sink 在同一把鎖內配發 seq 並寫檔——read loop（s2c）與並行段兩個送 turn/start 的
// goroutine（c2s）會同時進來，寫入必須與 seq 配發原子化，否則證據檔會交錯損毀（I5）。
func (p *probe) sink(env []byte) error {
	var e struct {
		Dir   string          `json:"dir"`
		Frame json.RawMessage `json:"frame"`
	}
	if err := json.Unmarshal(env, &e); err != nil {
		return err
	}
	now := time.Now()
	rec := parseFrame(e.Frame)
	rec.Dir, rec.At = e.Dir, now

	p.mu.Lock()
	defer p.mu.Unlock()
	p.seq++
	rec.Seq = p.seq
	p.frames = append(p.frames, rec)
	if e.Dir == "s2c" && rec.Kind == "notification" {
		p.notifIDs.total++
		if rec.ThreadID != "" {
			p.notifIDs.withThread++
		}
		if rec.TurnID != "" {
			p.notifIDs.withTurn++
		}
		if rec.ThreadID == "" && rec.TurnID == "" {
			p.notifIDs.missingBoth = append(p.notifIDs.missingBoth, rec.Method)
		}
	}
	_, err := fmt.Fprintf(p.wireLog, "{\"seq\":%d,\"ts\":%q,\"dir\":%q,\"frame\":%s}\n",
		rec.Seq, now.UTC().Format(time.RFC3339Nano), e.Dir, e.Frame)
	return err
}

func (p *probe) onNotification(method string, params json.RawMessage) {
	if method != codex.MethodTurnCompleted {
		return
	}
	var q struct {
		ThreadID string `json:"threadId"`
		Turn     struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Error  struct {
				Message string `json:"message"`
			} `json:"error"`
		} `json:"turn"`
	}
	_ = json.Unmarshal(params, &q)
	status := q.Turn.Status
	if status == "" {
		status = "completed"
	}
	te := turnEnd{turnID: q.Turn.ID, status: status, errMsg: q.Turn.Error.Message}

	p.mu.Lock()
	defer p.mu.Unlock()
	if q.ThreadID != "" {
		if ch, ok := p.waiters[q.ThreadID]; ok {
			select {
			case ch <- te:
			default:
			}
			return
		}
	}
	// 無 threadId 或找不到 waiter：只能廣播 → (b) 歸屬失敗的實據
	p.noIdent = true
	te.viaBcast = true
	for _, ch := range p.waiters {
		select {
		case ch <- te:
		default:
		}
	}
}

// onServerRequest：一律拒絕核可（decision=decline，與 production fail-closed 一致）。
func (p *probe) onServerRequest(method string, params json.RawMessage) (any, error) {
	switch method {
	case codex.MethodCmdExecRequestApproval, codex.MethodFileChangeRequestApproval:
		var q struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			ItemID   string `json:"itemId"`
		}
		_ = json.Unmarshal(params, &q)
		p.mu.Lock()
		p.appr = append(p.appr, apprRec{At: time.Now(), Method: method, ThreadID: q.ThreadID,
			TurnID: q.TurnID, ItemID: q.ItemID, Decision: "decline", Raw: tail(string(params), 600)})
		p.mu.Unlock()
		return map[string]string{"decision": "decline"}, nil
	default:
		p.mu.Lock()
		p.appr = append(p.appr, apprRec{At: time.Now(), Method: method,
			Decision: "unsupported", Raw: tail(string(params), 600)})
		p.mu.Unlock()
		return nil, fmt.Errorf("unsupported server request %s", method)
	}
}

func parseFrame(b []byte) frameRec {
	var f struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return frameRec{Kind: "unparsed"}
	}
	rec := frameRec{Method: f.Method}
	switch {
	case len(f.ID) > 0 && f.Method != "":
		rec.Kind = "request"
	case len(f.ID) > 0:
		rec.Kind = "response"
	default:
		rec.Kind = "notification"
	}
	for _, blob := range []json.RawMessage{f.Params, f.Result} {
		if len(blob) == 0 {
			continue
		}
		var q struct {
			ThreadID string `json:"threadId"`
			TurnID   string `json:"turnId"`
			Thread   struct {
				ID string `json:"id"`
			} `json:"thread"`
			Turn struct {
				ID       string `json:"id"`
				ThreadID string `json:"threadId"`
			} `json:"turn"`
		}
		if json.Unmarshal(blob, &q) != nil {
			continue
		}
		if rec.ThreadID == "" {
			rec.ThreadID = firstNonEmpty(q.ThreadID, q.Thread.ID, q.Turn.ThreadID)
		}
		if rec.TurnID == "" {
			rec.TurnID = firstNonEmpty(q.TurnID, q.Turn.ID)
		}
	}
	return rec
}

// ---- 判定輸出 ----

func (p *probe) report(mode string, res []turnResult, appr turnResult, thA, thB, wirePath string) analysis {
	p.mu.Lock()
	frames := append([]frameRec(nil), p.frames...)
	approvals := append([]apprRec(nil), p.appr...)
	notif := p.notifIDs
	noIdent := p.noIdent
	p.mu.Unlock()

	fmt.Printf("WIRE %s frames=%d\n", wirePath, len(frames))

	for _, r := range append([]turnResult{appr}, res...) {
		fmt.Printf("TURN label=%s thread=%s turn=%s status=%s dur=%s err=%q note=%q\n",
			r.label, r.threadID, r.turnID, r.status,
			r.ended.Sub(r.started).Round(time.Millisecond), r.errText, r.note)
	}

	an := analysis{approvals: len(approvals), broadcast: noIdent}

	// (a) 判準：**turn 生命期重疊**——turn/started(A) < turn/completed(B) 且
	//     turn/started(B) < turn/completed(A)。只取 turn-scoped frame（TurnID 非空），
	//     排除 thread/started 等 thread 級 frame——它們必然依建立順序落在最前面，
	//     用「index 區間相交」會恆真，無法區分並行與串行化。
	var trace []frameRec
	for _, f := range frames {
		if f.Dir != "s2c" || f.TurnID == "" {
			continue
		}
		if f.ThreadID == thA || f.ThreadID == thB {
			trace = append(trace, f)
		}
	}
	sort.SliceStable(trace, func(i, j int) bool { return trace[i].Seq < trace[j].Seq })

	an.overlap, an.overlapWhy = overlapVerdict(trace, thA, thB)
	fmt.Printf("VERDICT_A turn_lifetime_overlap=%s %s\n", an.overlap, an.overlapWhy)

	// 輸出階段是否交錯（觀察指標，**不入 gate**）：兩 thread 的 agentMessage delta
	// 的 seq 區間是否相交。用來區分「並行受理」與「並行產出」。
	dA0, dA1, dB0, dB1, nDA, nDB := -1, -1, -1, -1, 0, 0
	for _, f := range trace {
		if f.Method != codex.MethodAgentMessageDelta {
			continue
		}
		if f.ThreadID == thA {
			nDA++
			if dA0 < 0 {
				dA0 = f.Seq
			}
			dA1 = f.Seq
		} else {
			nDB++
			if dB0 < 0 {
				dB0 = f.Seq
			}
			dB1 = f.Seq
		}
	}
	switch {
	case nDA == 0 || nDB == 0:
		an.deltaInterleaved = "inconclusive"
	case dB0 < dA1 && dA0 < dB1:
		an.deltaInterleaved = "yes"
	default:
		an.deltaInterleaved = "no"
	}
	fmt.Printf("OBSERVE delta_interleaved=%s deltasA=%d[seq%d..%d] deltasB=%d[seq%d..%d]\n",
		an.deltaInterleaved, nDA, dA0, dA1, nDB, dB0, dB1)

	fmt.Println("--- turn-scoped s2c trace (thread A/B) ---")
	for _, f := range trace {
		lbl := "A"
		if f.ThreadID == thB {
			lbl = "B"
		}
		fmt.Printf("  seq=%-4d %s %s %s turn=%s\n", f.Seq, ts(f.At), lbl, f.Method, f.TurnID)
	}
	fmt.Println("--- end trace ---")

	// (b) identity 歸屬
	fmt.Printf("VERDICT_B notifications=%d with_threadId=%d with_turnId=%d missing_both=%d broadcast_fallback=%v\n",
		notif.total, notif.withThread, notif.withTurn, len(notif.missingBoth), noIdent)
	if len(notif.missingBoth) > 0 {
		fmt.Printf("  notif_missing_identity_methods=%v\n", dedup(notif.missingBoth))
	}
	for _, a := range approvals {
		if a.ThreadID == "" || a.TurnID == "" {
			an.apprMissingID++
		}
	}
	fmt.Printf("VERDICT_B approvals=%d missing_identity=%d\n", an.approvals, an.apprMissingID)
	for _, a := range approvals {
		fmt.Printf("  APPROVAL at=%s method=%s threadId=%q turnId=%q itemId=%q decision=%s\n",
			ts(a.At), a.Method, a.ThreadID, a.TurnID, a.ItemID, a.Decision)
		fmt.Printf("    raw=%s\n", a.Raw)
	}
	return an
}

// overlapVerdict 是 (a) 的自動判準：**turn 生命期重疊**——
// turn/started(A) 早於 turn/completed(B) 且 turn/started(B) 早於 turn/completed(A)。
//
// trace 必須只含 turn-scoped s2c frame（TurnID 非空）並依 Seq 遞增。
// 不可改回「A/B frame 的 index 區間相交」：thread 級 frame（thread/started 等）依建立
// 順序必然落在最前面，串行化的 server 也會讓兩區間互相包含 → 該指標恆真。
func overlapVerdict(trace []frameRec, thA, thB string) (verdict, why string) {
	var startedA, startedB, endedA, endedB *frameRec
	pick := func(dst **frameRec, f *frameRec) {
		if *dst == nil {
			*dst = f
		}
	}
	for i := range trace {
		f := &trace[i]
		switch {
		case f.Method == codex.MethodTurnStarted && f.ThreadID == thA:
			pick(&startedA, f)
		case f.Method == codex.MethodTurnStarted && f.ThreadID == thB:
			pick(&startedB, f)
		case f.Method == codex.MethodTurnCompleted && f.ThreadID == thA:
			pick(&endedA, f)
		case f.Method == codex.MethodTurnCompleted && f.ThreadID == thB:
			pick(&endedB, f)
		}
	}
	switch {
	case startedA == nil || startedB == nil:
		return "inconclusive", "缺少 turn/started（A 或 B 的 turn 未開始）"
	case endedA == nil && endedB == nil:
		return "inconclusive", "兩個 turn 都沒有 turn/completed（forced 收尾預期如此）"
	case endedA == nil || endedB == nil:
		return "inconclusive", "只有一個 turn 有 turn/completed，無法判定生命期是否重疊"
	}
	v := "no"
	if startedA.Seq < endedB.Seq && startedB.Seq < endedA.Seq {
		v = "yes"
	}
	return v, fmt.Sprintf("startedA=seq%d(%s) endedA=seq%d(%s) startedB=seq%d(%s) endedB=seq%d(%s)",
		startedA.Seq, ts(startedA.At), endedA.Seq, ts(endedA.At),
		startedB.Seq, ts(startedB.At), endedB.Seq, ts(endedB.At))
}

// ---- 小工具 ----

func dedup(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func ts(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.UTC().Format("15:04:05.000")
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func must(err error) {
	if err != nil {
		fatal("%v", err)
	}
}

// fatal：probe 執行失敗（環境／API 錯誤），exit 1。先 cleanup 再退出。
func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "PROBE FATAL: "+format+"\n", args...)
	runCleanups()
	os.Exit(1)
}

// nogoExit：probe 跑得起來但判定 NO-GO，exit 2。先 cleanup 再退出。
func nogoExit(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "GATE NO-GO: "+format+"\n", args...)
	fmt.Println("GATE NO-GO")
	runCleanups()
	os.Exit(2)
}
