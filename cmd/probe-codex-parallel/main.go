// probe-codex-parallel：M3b Task 0 的 live probe（NO-GO gate）。
//
// 驗證前提：單一 codex app-server 能同時承載多個 thread 的並行 turn。
// 判定範圍（凍結）：
//   (a) 兩 thread 並行 turn 是否真並行（wire frame 時間上交錯，非串行化）
//   (b) notification 與 approval request 是否帶足以歸屬的 thread／turn identity
//   (c) 自然與強制（-force）兩種收尾是否 bounded 收斂且錄到最後一筆 frame
//
// completed-before-response 不列入本 probe（改由 Task 9 fake-wire 測試鎖住）。
//
// 全程走 production API（codex.StartAppServer / ThreadRunner / Conn），
// 不另建 wire 路徑；wire log 即證據。
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
	viaBcast bool // 無 threadId 可歸屬，只能廣播
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

func main() {
	flag.Parse()
	if *codexBin == "" {
		fatal("必須以 -codex-bin 指定 bundled binary")
	}
	mode := "natural"
	if *force {
		mode = "forced"
	}

	tmp, err := os.MkdirTemp("", "probe-codex-*")
	must(err)
	defer os.RemoveAll(tmp)

	// wire log 落在 tmp 之外——tmp 於離開時整個刪除，證據必須留存。
	wirePath := filepath.Join(os.TempDir(), "probe-codex-parallel-"+mode+".jsonl")
	wireLog, err := os.Create(wirePath)
	must(err)

	p := &probe{waiters: map[string]chan turnEnd{}, wireLog: wireLog}

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	srv, err := codex.StartAppServer(ctx, codex.Config{
		Binary: *codexBin, CWD: tmp, TermGrace: 5 * time.Second,
	})
	must(err)
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
		fatal("NO-GO: approval thread 無法建立: %v", err)
	}
	appr := p.runTurnDenyingApprovals(ctx, srv, apprRunner, "APPROVAL", promptApproval)
	if _, serr := os.Stat(filepath.Join(tmp, "probe-approval.txt")); serr == nil {
		fatal("NO-GO: 核可被拒仍寫入檔案")
	}

	// (a) 兩 thread 並行送 turn
	runnerA, errA := mustStartThread(ctx, srv)
	runnerB, errB := mustStartThread(ctx, srv)
	if errA != nil {
		fatal("NO-GO: thread A 無法建立: %v", errA)
	}
	if errB != nil {
		fatal("NO-GO: 第二個 thread 被拒（單 app-server 無法承載多 thread）: %v", errB)
	}
	thA, thB := runnerA.ThreadID(), runnerB.ThreadID()
	fmt.Printf("threads approval=%s A=%s B=%s\n", apprRunner.ThreadID(), thA, thB)

	var wg sync.WaitGroup
	res := make([]turnResult, 2)
	type pair struct {
		label  string
		runner *codex.ThreadRunner
		prompt string
	}
	for i, pr := range []pair{{"A", runnerA, promptA}, {"B", runnerB, promptB}} {
		wg.Add(1)
		go func(i int, pr pair) {
			defer wg.Done()
			res[i] = p.runTurn(ctx, srv, pr.runner, pr.label, pr.prompt)
		}(i, pr)
	}
	if *force { // 強制收尾：不等 turn 完成（只影響 (a) 與 (c)，approval 已驗完）
		time.Sleep(2 * time.Second) // 讓兩個 turn 真的送上 wire，否則測不到「turn 進行中終止」
		_ = srv.Terminate()
	}
	wg.Wait()

	p.report(mode, res, appr, thA, thB, wirePath)

	// (c) 收尾：Terminate → Wait → StopRecording → Close
	termStart := time.Now()
	_ = srv.Terminate()
	exit := srv.Wait()
	shutdown := time.Since(termStart)
	recErr := srv.StopRecording()
	p.mu.Lock()
	lastSeq, lastAt := p.seq, time.Time{}
	if n := len(p.frames); n > 0 {
		lastAt = p.frames[n-1].At
	}
	p.mu.Unlock()
	must(wireLog.Close())

	fmt.Printf("exit_code=%d stderr_tail=%q\n", exit.Code, tail(exit.StderrTail, 400))
	fmt.Printf("SHUTDOWN mode=%s bounded=%v elapsed=%s record_err=%v last_frame_seq=%d last_frame_at=%s\n",
		mode, shutdown < 10*time.Second, shutdown.Round(time.Millisecond), recErr, lastSeq, ts(lastAt))
	fmt.Printf("WIRE_LOG %s\n", wirePath)
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
			out.status, out.ended = te.status, time.Now()
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
	seq := rec.Seq
	p.mu.Unlock()

	_, err := fmt.Fprintf(p.wireLog, "{\"seq\":%d,\"ts\":%q,\"dir\":%q,\"frame\":%s}\n",
		seq, now.UTC().Format(time.RFC3339Nano), e.Dir, e.Frame)
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
		} `json:"turn"`
	}
	_ = json.Unmarshal(params, &q)
	status := q.Turn.Status
	if status == "" {
		status = "completed"
	}
	te := turnEnd{turnID: q.Turn.ID, status: status}

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

func (p *probe) report(mode string, res []turnResult, appr turnResult, thA, thB, wirePath string) {
	p.mu.Lock()
	frames := append([]frameRec(nil), p.frames...)
	approvals := append([]apprRec(nil), p.appr...)
	notif := p.notifIDs
	noIdent := p.noIdent
	p.mu.Unlock()

	fmt.Printf("MODE %s wire=%s frames=%d\n", mode, wirePath, len(frames))

	for _, r := range append([]turnResult{appr}, res...) {
		fmt.Printf("TURN label=%s thread=%s turn=%s status=%s dur=%s err=%q note=%q\n",
			r.label, r.threadID, r.turnID, r.status,
			r.ended.Sub(r.started).Round(time.Millisecond), r.errText, r.note)
	}

	// (a) 並行交錯：只看 thA / thB 的 s2c frame
	var trace []frameRec
	for _, f := range frames {
		if f.Dir != "s2c" {
			continue
		}
		if f.ThreadID == thA || f.ThreadID == thB {
			trace = append(trace, f)
		}
	}
	sort.SliceStable(trace, func(i, j int) bool { return trace[i].Seq < trace[j].Seq })
	firstA, lastA, firstB, lastB := -1, -1, -1, -1
	nA, nB := 0, 0
	for i, f := range trace {
		if f.ThreadID == thA {
			nA++
			if firstA < 0 {
				firstA = i
			}
			lastA = i
		} else {
			nB++
			if firstB < 0 {
				firstB = i
			}
			lastB = i
		}
	}
	interleaved := firstA >= 0 && firstB >= 0 && firstB < lastA && firstA < lastB
	fmt.Printf("VERDICT_A interleaved=%v framesA=%d framesB=%d windowA=[%d,%d] windowB=[%d,%d]\n",
		interleaved, nA, nB, firstA, lastA, firstB, lastB)
	fmt.Println("--- interleave trace (s2c frames of thread A/B) ---")
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
	fmt.Printf("VERDICT_B approvals=%d\n", len(approvals))
	for _, a := range approvals {
		fmt.Printf("  APPROVAL at=%s method=%s threadId=%q turnId=%q itemId=%q decision=%s\n",
			ts(a.At), a.Method, a.ThreadID, a.TurnID, a.ItemID, a.Decision)
		fmt.Printf("    raw=%s\n", a.Raw)
	}
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

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "PROBE FATAL: "+format+"\n", args...)
	os.Exit(1)
}
