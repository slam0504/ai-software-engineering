// probe-codex-approval：Codex「accept 後仍 EPERM」相容性調查（owner 2026-08-21 開票）。
//
// rev2（reviewer 2026-08-21 CHANGES_REQUIRED 後重寫）：
//   - **每組隔離 CODEX_HOME**：G2 的 acceptWithExecpolicyAmendment 會把 allow 規則
//     持久化到 $CODEX_HOME/rules/default.rules，rev1 用共用家目錄＋同名指令，
//     讓 G2 之後的所有組（含另一版本）都吃到既存規則、版本歸因失效。每組
//     複製 auth.json 到全新暫存 home，跑完即棄；並記錄規則檔前後 digest。
//   - **每組唯一 marker 檔名**（防禦第二層：即使規則洩漏也對不上 prefix）。
//   - **sandbox 證據**：完整保存 thread/start response 原文，並於 turn 結束後
//     解析 $CODEX_HOME/sessions 的 rollout jsonl，抽出 turn_context 的
//     sandbox_policy／approval_policy——rev1 找的 thread.settings 不存在於 schema。
//   - **fail loud**：任一組 Err／timeout → 退出碼 1。
//   - **原始證據**：每組結果以 JSON 落到 -out 目錄，附 binary 版本與 sha256。
//
// rev3（reviewer 2026-08-21 二輪）：
//   - 記錄 **turn_start_params**（G7 收錯證據正是因為只存了 thread 層參數）。
//   - approval 改 **approval_events[] 逐事件**＋approvals_turn1/turn2 計數——
//     G8「第二輪跳過核可」由 approvals_turn2==0 直接證明。
//   - sha256 拆成 launcher_sha256（node wrapper）與 **native_sha256**（vendor
//     native binary，版本身分的真正載體）。
//
// 組別：
//
//	G1: 預設 sandbox ＋ 回 "accept"
//	G2: 預設 sandbox ＋ 回 acceptWithExecpolicyAmendment（echo server 的提案）
//	G3: thread/start 指定 sandboxPolicy（值由 -sandbox 控制）＋ workspace 內寫入
//	G4: 同 G3 ＋ **workspace 外**寫入（/tmp）
//	G5: 同 G3 ＋ 網路存取（curl）
//	G6: 不帶 protocol 參數，改在隔離 home 寫 config.toml `sandbox_mode = "workspace-write"`
//	G7: sandboxPolicy 掛在 **turn/start**（TurnStartParams 也有此欄）
//	G8: 同 G2 後，同 home 同指令**再跑第二輪**——驗證 persisted rule 是否自動放行並於 sandbox 外執行
//	G9: workspace-write ＋ **file-change 寫入路徑**（apply_patch，對照 command-execution）
//
// -approval 旗標控制 approvalPolicy（untrusted｜on-request｜never）；配合 G7/G9
// 可跑「三 policy × 兩寫入路徑」矩陣（B1 驗收）。
//
// **本 probe 不改 production 安全模型**；只在隔離環境做行為對照。
// 退出碼：0 = 全部組別完成且無 probe 自身錯誤；1 = 任一組失敗／timeout／環境錯誤。
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/codex"
)

var (
	codexBin = flag.String("codex-bin", "", "codex binary 路徑（必填）")
	groups   = flag.String("groups", "1,2,3", "逗號分隔的組別（1-8）")
	outDir   = flag.String("out", "", "原始結果輸出目錄（必填）")
	keep     = flag.Bool("keep", false, "保留每組的暫存 home 與 cwd")
	// sandboxPolicy 的 wire 編碼待驗（TS enum 是 camelCase、rollout 內部是
	// {"type":"read-only"} kebab）；G3-G5 用這個旗標逐一對照。
	sandboxForm = flag.String("sandbox", "workspaceWrite",
		`G3-G5 的 sandboxPolicy 值：字串（如 workspaceWrite / workspace-write）或 JSON（如 {"type":"workspaceWrite"}）`)
	approval = flag.String("approval", "untrusted", "approvalPolicy：untrusted | on-request | never")
)

const groupTimeout = 120 * time.Second

type groupResult struct {
	Group             string   `json:"group"`
	BinaryVersion     string   `json:"binary_version"`
	LauncherSHA256    string   `json:"launcher_sha256"` // -codex-bin 解析後實體檔（node wrapper 時是 wrapper 的 hash）
	NativeSHA256      string   `json:"native_sha256"`   // wrapper 之下實際的 vendor native binary
	NativePath        string   `json:"native_path"`
	ThreadStartParams string   `json:"thread_start_params"`
	TurnStartParams   string   `json:"turn_start_params"`
	ThreadStartResp   string   `json:"thread_start_response_raw"`
	RulesBefore       string   `json:"rules_digest_before"`
	RulesAfter        string   `json:"rules_digest_after"`
	RulesContentAfter string   `json:"rules_content_after,omitempty"`
	ApprovalMethod    string   `json:"approval_method"` // 首個 approval（向後相容）
	ApprovalEvents    []string `json:"approval_events"` // 逐事件：method→decision（依序）
	ApprovalsTurn1    int      `json:"approvals_turn1"`
	ApprovalsTurn2    int      `json:"approvals_turn2"` // 僅 G8 有意義
	ApprovalRaw       string   `json:"approval_params_raw,omitempty"`
	DecisionSent      string   `json:"decision_sent"`
	TurnOutcome       string   `json:"turn_outcome"`
	MarkerPath        string   `json:"marker_path"`
	MarkerCreated     bool     `json:"marker_created"`
	TurnContext       string   `json:"rollout_turn_context"`
	AgentTail         string   `json:"agent_tail"`
	Err               string   `json:"err,omitempty"`
}

func main() {
	flag.Parse()
	if *codexBin == "" || *outDir == "" {
		fmt.Fprintln(os.Stderr, "PROBE FATAL: -codex-bin 與 -out 必填")
		os.Exit(1)
	}
	bin, err := filepath.Abs(*codexBin)
	if err != nil {
		fatal("abs: %v", err)
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fatal("mkdir out: %v", err)
	}
	ver, launcherSHA, nativeSHA, nativePath := binaryIdentity(bin)
	fmt.Printf("binary: %s\nversion: %s\nlauncher_sha256: %s\nnative: %s\nnative_sha256: %s\n\n",
		bin, ver, launcherSHA, nativePath, nativeSHA)

	failed := false
	var summaries []string
	for _, g := range strings.Split(*groups, ",") {
		g = strings.TrimSpace(g)
		r := runGroup(bin, g)
		r.BinaryVersion, r.LauncherSHA256, r.NativeSHA256, r.NativePath = ver, launcherSHA, nativeSHA, nativePath
		raw, _ := json.MarshalIndent(r, "", "  ")
		outPath := filepath.Join(*outDir, fmt.Sprintf("g%s.json", g))
		if werr := os.WriteFile(outPath, raw, 0o644); werr != nil {
			fatal("write %s: %v", outPath, werr)
		}
		fmt.Println(format(r))
		verdict := "檔案未建立"
		if r.MarkerCreated {
			verdict = "檔案已建立"
		}
		if r.Err != "" || r.TurnOutcome == "timeout" {
			failed = true
			verdict += "（PROBE ERROR）"
		}
		summaries = append(summaries, fmt.Sprintf("G%s decision=%s → %s（turn=%s, turn_context=%s）",
			g, r.DecisionSent, verdict, r.TurnOutcome, r.TurnContext))
	}
	fmt.Println("---- SUMMARY ----")
	for _, s := range summaries {
		fmt.Println(s)
	}
	if failed {
		fmt.Println("PROBE RESULT: FAILED（存在組別錯誤或 timeout）")
		os.Exit(1)
	}
}

func fatal(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "PROBE FATAL: "+f+"\n", a...)
	os.Exit(1)
}

func runGroup(bin, g string) (res groupResult) {
	res.Group = g
	cwd, err := os.MkdirTemp("", "probe-approval-cwd-g"+g+"-")
	if err != nil {
		res.Err = err.Error()
		return
	}
	home, err := os.MkdirTemp("", "probe-approval-home-g"+g+"-")
	if err != nil {
		res.Err = err.Error()
		return
	}
	if !*keep {
		defer os.RemoveAll(cwd)
		defer os.RemoveAll(home)
	} else {
		fmt.Printf("G%s scratch cwd=%s home=%s\n", g, cwd, home)
	}
	// 隔離 home 只帶 auth（跑完即棄）；使用者的 ~/.codex 全程不被讀寫。
	if err := copyFile(filepath.Join(os.Getenv("HOME"), ".codex", "auth.json"),
		filepath.Join(home, "auth.json")); err != nil {
		res.Err = "copy auth: " + err.Error()
		return
	}
	// G6：sandbox 走 config.toml（codex 的正規設定路徑），不走 protocol 參數。
	if g == "6" {
		if err := os.WriteFile(filepath.Join(home, "config.toml"),
			[]byte("sandbox_mode = \"workspace-write\"\n"), 0o644); err != nil {
			res.Err = "write config.toml: " + err.Error()
			return
		}
	}
	res.RulesBefore = rulesDigest(home)

	marker := fmt.Sprintf("marker-g%s-%d.txt", g, os.Getpid())
	var prompt string
	switch g {
	case "4":
		res.MarkerPath = filepath.Join(os.TempDir(), fmt.Sprintf("probe-outside-g4-%d.txt", os.Getpid()))
		prompt = fmt.Sprintf("請執行 touch %s 建立檔案（注意：路徑在工作目錄之外）。無論成功、失敗或需要核可，都請簡短回報結果，不要嘗試其他替代作法。", res.MarkerPath)
		defer os.Remove(res.MarkerPath)
	case "5":
		res.MarkerPath = "(network)"
		prompt = "請執行 curl --max-time 8 -sS https://example.com -o /dev/null -w '%{http_code}' 並回報輸出。無論成功、失敗或需要核可，都請簡短回報結果，不要嘗試其他替代作法。"
	case "9":
		// file-change 寫入路徑（apply_patch），對照 command-execution。先放一個
		// 種子檔讓 codex 有東西可改。
		seed := filepath.Join(cwd, "edit-target.txt")
		_ = os.WriteFile(seed, []byte("original\n"), 0o644)
		res.MarkerPath = seed
		prompt = "請把工作目錄裡 edit-target.txt 的內容改成 CHANGED（用檔案編輯，不要用 shell 指令）。無論成功、失敗或需要核可，都請簡短回報結果。"
	default:
		res.MarkerPath = filepath.Join(cwd, marker)
		prompt = fmt.Sprintf("請執行 touch %s 建立檔案。無論成功或失敗，都請簡短回報結果，不要嘗試其他替代作法。", marker)
	}

	ctx, cancel := context.WithTimeout(context.Background(), groupTimeout)
	defer cancel()
	srv, err := codex.StartAppServer(ctx, codex.Config{Binary: bin, CWD: cwd,
		Env: []string{"CODEX_HOME=" + home}, TermGrace: 3 * time.Second})
	if err != nil {
		res.Err = "StartAppServer: " + err.Error()
		return
	}
	defer func() {
		_ = srv.Terminate()
		srv.Wait()
		res.RulesAfter = rulesDigest(home)
		if res.RulesAfter != res.RulesBefore {
			res.RulesContentAfter = rulesContent(home)
		}
		res.TurnContext = rolloutTurnContext(home)
	}()

	turnDone := make(chan string, 1)
	var agentText strings.Builder
	conn := srv.Conn()
	conn.OnNotification(func(method string, params json.RawMessage) {
		switch method {
		case "item/agentMessage/delta":
			var p struct {
				Delta string `json:"delta"`
			}
			_ = json.Unmarshal(params, &p)
			agentText.WriteString(p.Delta)
		case "turn/completed", "turn/failed":
			select {
			case turnDone <- strings.TrimPrefix(method, "turn/"):
			default:
			}
		}
	})
	var apprMu sync.Mutex
	conn.OnServerRequest(func(method string, params json.RawMessage) (any, error) {
		if !strings.Contains(method, "requestApproval") {
			return nil, fmt.Errorf("probe: unexpected server request %s", method)
		}
		var p struct {
			ProposedExecpolicyAmendment []string `json:"proposedExecpolicyAmendment"`
		}
		_ = json.Unmarshal(params, &p)
		var decision any = "accept"
		if (g == "2" || g == "8") && len(p.ProposedExecpolicyAmendment) > 0 {
			decision = map[string]any{"acceptWithExecpolicyAmendment": map[string]any{
				"execpolicy_amendment": p.ProposedExecpolicyAmendment}}
		}
		b, _ := json.Marshal(decision)
		apprMu.Lock()
		if res.ApprovalMethod == "" { // 首個（向後相容欄位）
			res.ApprovalMethod = method
			res.ApprovalRaw = compact(params)
			res.DecisionSent = string(b)
		}
		res.ApprovalEvents = append(res.ApprovalEvents, method+"→"+string(b))
		apprMu.Unlock()
		return map[string]any{"decision": decision}, nil
	})

	if err := srv.Handshake(ctx, codex.ClientInfo{Name: "probe-codex-approval"}); err != nil {
		res.Err = "handshake: " + err.Error()
		return
	}

	startParams := map[string]any{"approvalPolicy": *approval}
	if g == "3" { // 只有 G3 留在 thread/start（實測：被靜默忽略——這本身是 finding）
		v, perr := parseSandboxForm(*sandboxForm)
		if perr != nil {
			res.Err = perr.Error()
			return
		}
		startParams["sandboxPolicy"] = v
	}
	pb, _ := json.Marshal(startParams)
	res.ThreadStartParams = string(pb)
	resRaw, err := conn.Call(ctx, "thread/start", startParams)
	if err != nil {
		res.Err = "thread/start: " + err.Error()
		return
	}
	res.ThreadStartResp = compact(resRaw) // 完整 response 原文（含頂層 sandbox 相關欄位）
	var tr struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(resRaw, &tr); err != nil || tr.Thread.ID == "" {
		res.Err = "thread id missing"
		return
	}

	turnParams := map[string]any{
		"threadId": tr.Thread.ID,
		"input":    []map[string]any{{"type": "text", "text": prompt}},
	}
	if g == "4" || g == "5" || g == "7" || g == "9" {
		// turn/start 才是 protocol 上有效的 sandbox 入口（G7 主證；G4/G5 在有效的
		// workspace-write context 下驗 workspace 外寫入與網路邊界；G9 file-change）。
		v, perr := parseSandboxForm(*sandboxForm)
		if perr != nil {
			res.Err = perr.Error()
			return
		}
		turnParams["sandboxPolicy"] = v
	}
	tb, _ := json.Marshal(turnParams)
	res.TurnStartParams = string(tb)
	if _, err := conn.Call(ctx, "turn/start", turnParams); err != nil {
		res.Err = "turn/start: " + err.Error()
		return
	}
	select {
	case res.TurnOutcome = <-turnDone:
	case <-ctx.Done():
		res.TurnOutcome = "timeout"
	}
	apprMu.Lock()
	res.ApprovalsTurn1 = len(res.ApprovalEvents)
	apprMu.Unlock()
	// G8：不對稱性證明——第一輪 amendment 落規則後，同 home 同指令再跑第二輪。
	// 「第二輪跳過核可」由 approvals_turn2 == 0 直接證明（reviewer 2026-08-21：
	// 單一 approval_method 欄位無法分辨第二輪有沒有再問）。
	if g == "8" && res.TurnOutcome == "completed" {
		if _, err := conn.Call(ctx, "turn/start", map[string]any{
			"threadId": tr.Thread.ID,
			"input":    []map[string]any{{"type": "text", "text": prompt}},
		}); err != nil {
			res.Err = "turn/start(第二輪): " + err.Error()
			return
		}
		select {
		case second := <-turnDone:
			res.TurnOutcome = "completed;second=" + second
		case <-ctx.Done():
			res.TurnOutcome = "completed;second=timeout"
		}
		apprMu.Lock()
		res.ApprovalsTurn2 = len(res.ApprovalEvents) - res.ApprovalsTurn1
		apprMu.Unlock()
	}
	switch g {
	case "5": // 網路：無 marker 檔
	case "9": // file-change：看內容是否真的被改（種子檔一開始就存在）
		if b, rerr := os.ReadFile(res.MarkerPath); rerr == nil && strings.Contains(string(b), "CHANGED") {
			res.MarkerCreated = true
		}
	default:
		if _, serr := os.Stat(res.MarkerPath); serr == nil {
			res.MarkerCreated = true
		}
	}
	t := agentText.String()
	if len(t) > 200 {
		t = t[len(t)-200:]
	}
	res.AgentTail = strings.ReplaceAll(t, "\n", " ")
	return
}

// rolloutTurnContext：從隔離 home 的 sessions rollout jsonl 抽出 turn_context 的
// sandbox／approval 設定——這是「該 turn 實際生效的 sandbox」的權威證據。
func rolloutTurnContext(home string) string {
	var hits []string
	_ = filepath.Walk(filepath.Join(home, "sessions"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".jsonl") {
			return nil
		}
		f, ferr := os.Open(p)
		if ferr != nil {
			return nil
		}
		defer f.Close()
		b, _ := io.ReadAll(f)
		for _, line := range bytes.Split(b, []byte("\n")) {
			if bytes.Contains(line, []byte("turn_context")) {
				var e struct {
					Payload struct {
						SandboxPolicy  json.RawMessage `json:"sandbox_policy"`
						ApprovalPolicy json.RawMessage `json:"approval_policy"`
					} `json:"payload"`
				}
				if json.Unmarshal(line, &e) == nil && len(e.Payload.SandboxPolicy) > 0 {
					hits = append(hits, fmt.Sprintf("sandbox=%s approval=%s",
						string(e.Payload.SandboxPolicy), string(e.Payload.ApprovalPolicy)))
				}
			}
		}
		return nil
	})
	if len(hits) == 0 {
		return "(rollout 無 turn_context 紀錄)"
	}
	return strings.Join(dedup(hits), " ; ")
}

func rulesDigest(home string) string {
	p := filepath.Join(home, "rules", "default.rules")
	b, err := os.ReadFile(p)
	if err != nil {
		return "absent"
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))[:16]
}

func rulesContent(home string) string {
	b, err := os.ReadFile(filepath.Join(home, "rules", "default.rules"))
	if err != nil {
		return ""
	}
	return string(b)
}

func binaryIdentity(bin string) (version, launcherSHA, nativeSHA, nativePath string) {
	out, err := exec.Command(bin, "--version").Output()
	if err != nil {
		version = "(version err: " + err.Error() + ")"
	} else {
		version = strings.TrimSpace(string(out))
	}
	real, err := filepath.EvalSymlinks(bin)
	if err != nil {
		real = bin
	}
	launcherSHA = fileSHA(real)
	// node wrapper（bin/codex.js）之下的 vendor native binary 才是版本身分的
	// 真正載體（reviewer 2026-08-21：兩版 wrapper JS 內容相同、hash 相同，
	// 誤稱 binary 身分）。從 wrapper 所在的 node_modules 找 @openai/codex-*/
	// vendor/**/bin/codex。
	nativeSHA, nativePath = "(not found)", "(not found)"
	nm := real
	for i := 0; i < 8 && nm != "/"; i++ {
		nm = filepath.Dir(nm)
		if filepath.Base(nm) == "node_modules" {
			break
		}
	}
	matches, _ := filepath.Glob(filepath.Join(nm, "@openai", "codex-*", "vendor", "*", "bin", "codex"))
	switch len(matches) {
	case 1:
		nativePath = matches[0]
		nativeSHA = fileSHA(matches[0])
	case 0:
		// -codex-bin 直接指向 Mach-O native binary（非 node wrapper）：native
		// identity 就是 launcher 本身（reviewer 2026-08-21）。
		nativePath = real
		nativeSHA = launcherSHA
	default:
		nativePath = strings.Join(matches, ";")
		nativeSHA = "(ambiguous)"
	}
	return
}

func fileSHA(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return "(read err)"
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

// parseSandboxForm：-sandbox 旗標值——`{` 開頭視為 JSON（tagged enum），否則原樣字串。
func parseSandboxForm(s string) (any, error) {
	if strings.HasPrefix(strings.TrimSpace(s), "{") {
		var obj map[string]any
		if err := json.Unmarshal([]byte(s), &obj); err != nil {
			return nil, fmt.Errorf("invalid -sandbox JSON: %w", err)
		}
		return obj, nil
	}
	return s, nil
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o600)
}

func dedup(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func compact(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}

func format(r groupResult) string {
	return fmt.Sprintf(`==== G%s ====
thread/start params : %s
thread/start resp   : %.400s
rules digest        : before=%s after=%s
approval method     : %s
decision sent       : %s
turn outcome        : %s
turn_context        : %s
marker (%s) created : %v
agent tail          : %s
err                 : %s`,
		r.Group, r.ThreadStartParams, r.ThreadStartResp, r.RulesBefore, r.RulesAfter,
		r.ApprovalMethod, r.DecisionSent, r.TurnOutcome, r.TurnContext,
		r.MarkerPath, r.MarkerCreated, r.AgentTail, r.Err)
}
