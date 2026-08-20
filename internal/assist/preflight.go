package assist

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/proc"
)

// provider capability preflight（spec §3.4）：PlanAssist spawn 前驗證「pin 版本
// ＋argv 凍結基準」，證明 enforcement 前提（Task 0 probe 驗過的白名單行為）在
// 當下 binary 上仍成立。驗不過 → fail closed（app 層拒啟動 runner）。
//
// 誤分類邊界：preflight 只回答「enforcement 前提是否已證明」。preflight 通過後
// 的 runner 啟動失敗／逾時是一般錯誤，不是 enforcement 失敗——那條線由 app 層
// 守（不建 planner-enforcement 項）。runtime 違規（Codex 在 readOnly+never 下
// 仍送 approval/escalation request）以 typed *EnforcementViolation 回報。

// pin 版本（spec §3.4；probe 記錄 docs/spikes/m3a1-planner-probe.md Step 1、
// Task 7 實測 `codex --version` → "codex-cli 0.146.1"）。
const (
	claudePinVersion = "2.1.223"
	codexPinVersion  = "0.146.1"
)

// probeApprovedClaudeArgs：Claude PlannerAssist 的凍結 argv 基準——**整組
// 10 token 已以最終形狀完整重跑 live probe**（docs/spikes/m3a1-planner-probe.md
// Addendum 2026-08-13：written.txt 拒寫＋Write 不在 tool schema＋hook_started
// 0 筆＋無 .remember/ 皆實測通過）。組成：Task 0 probe 形狀的前 6 token
// （`-p` … `--verbose`）＋Task 7 新增的 `--setting-sources ""` 2 token（隔離
// user／project／local settings 的 hook 副作用）＋Task 0 形狀的末 2 token
// （`--tools` 唯讀白名單，enforcement 落點）。ClaudePlannerArgs() 改動而未
// 同步此基準（或反之）即 preflight 失敗——argv 分岔 fail closed，不無聲放行；
// 任何 argv 變更需先重跑 probe addendum 等值驗證再更新此基準。
var probeApprovedClaudeArgs = []string{
	"-p",
	"--input-format", "stream-json",
	"--output-format", "stream-json",
	"--verbose",
	"--setting-sources", "", // 空 setting sources：隔離使用者全域 setting/hook（見上）
	"--tools", "Read,Glob,Grep",
}

// ErrEnforcementUnproven：preflight 未能證明 provider enforcement 前提
// （版本非 pin／argv 偏離凍結基準／binary 無法檢驗）。呼叫端以 errors.Is 判定。
var ErrEnforcementUnproven = errors.New("assist: provider enforcement unproven")

// EnforcementViolation：runtime enforcement 違規——Codex 在 readOnly+never 下
// 仍送出 escalation／approval request。typed，供 app 層 errors.As 判定後建
// planner-enforcement hard 項（§3.4／§3.8(9)）；一般 runner 失敗不得用此型別。
type EnforcementViolation struct {
	Provider string // "codex"
	Detail   string // 違規的 wire method（如 item/commandExecution/requestApproval）
}

func (e *EnforcementViolation) Error() string {
	return fmt.Sprintf("assist: %s escalation/approval refused (fail closed): %s", e.Provider, e.Detail)
}

// PreflightResult 是單次 preflight 的檢驗結果。OK=false 時 Reason 說明缺口；
// BinaryDigest 一律為 binary 內容的完整 SHA-256 hex（app 層快取 key 的一部分，
// 不截斷——binary 變更即 cache miss 重驗）。
type PreflightResult struct {
	Provider     string
	BinaryPath   string
	BinaryDigest string
	Version      string
	OK           bool
	Reason       string
}

// BinarySHA256 回傳 bin 檔案內容的完整 SHA-256 hex。
//
// **兩個防線**（reviewer 2026-08-20）：
//
//	(1) 目標必須是 regular file。以 FIFO／device 取代 binary 之後，io.Copy 會
//	    無限等寫入端，preflight 的 30 秒 deadline 也救不了——因為那個 deadline
//	    只掛在子行程上，讀檔完全不受它管。symlink 先解析到真正目標再判。
//	(2) 讀取期間逐塊檢查 ctx。就算檔案是 regular 但落在一個卡住的網路檔案系統
//	    上，取消也要能收斂。
func BinarySHA256(ctx context.Context, bin string) (string, error) {
	if ctx == nil { // nil context 不 panic：明確退化成 Background（API 契約）
		ctx = context.Background()
	}
	target, err := filepath.EvalSymlinks(bin)
	if err != nil {
		return "", fmt.Errorf("assist: 解析 %s: %w", bin, err)
	}
	// **O_NONBLOCK 開檔，再對同一個 descriptor 做 Stat**（reviewer 2026-08-20）：
	// 先 Stat 再 Open 是 TOCTOU——兩者之間把 regular file 換成 FIFO，open 就會
	// 卡在等寫入端，deadline 也救不了（open 本身不吃 context）。O_NONBLOCK 讓
	// FIFO 的唯讀 open 立即返回，對 regular file 則是 no-op；拿到 fd 之後用
	// f.Stat() 判型別，判的就是**實際打開的那個東西**。
	f, err := os.OpenFile(target, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("assist: %s 不是一般檔案（mode=%s）——拒絕對 FIFO／device 計算 digest", target, st.Mode())
	}
	h := sha256.New()
	buf := make([]byte, 64*1024)
	for {
		if cerr := ctx.Err(); cerr != nil {
			return "", cerr
		}
		n, rerr := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", rerr
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// **已知界限**（不宣稱完整 bounded）：取消只在**兩次 read 之間**生效。目標若落在
// 卡住的遠端檔案系統上，單一次 read syscall 在 Go 裡沒有中斷手段——那要靠掛載層
// 的 timeout（例如 NFS soft mount）處理，不是這裡能保證的。本地磁碟與 FIFO／
// device 這兩種可預期的無界等待則已經擋掉。

// binaryVersionOutput 執行 `<bin> --version` 並回傳 trim 後的 stdout。
func binaryVersionOutput(parent context.Context, bin string) (string, error) {
	// 30s 是上界，**不是**唯一的取消來源：parent 由呼叫端傳入，shutdown 一
	// cancel 就立刻收斂，不必等滿 30 秒（reviewer 2026-08-20）。
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	// process group 收尾（同 spec.GitRepo 的理由）：CommandContext 只殺直接
	// child，忽略 TERM 的孫程序會讓取消不收斂（reviewer 2026-08-20）。
	out, ex, err := proc.Output(ctx, proc.Config{Binary: bin, Args: []string{"--version"}})
	if err != nil {
		return "", fmt.Errorf("assist: %s --version: %w", bin, err) // 保留 ctx error identity
	}
	if ex.Err != nil {
		// %w 保留 *exec.ExitError（同 appGitRunner／spec.GitRepo 的處置）：呼叫端
		// 現在沒有依 exit type 分流，但攤平成字串會讓之後想分流的人無從下手。
		return "", fmt.Errorf("assist: %s --version: %s: %w", bin, strings.TrimSpace(ex.StderrTail), ex.Err)
	}
	if ex.Code != 0 {
		return "", fmt.Errorf("assist: %s --version: exit %d: %s", bin, ex.Code, strings.TrimSpace(ex.StderrTail))
	}
	return strings.TrimSpace(string(out)), nil
}

// PreflightClaude 驗證 (1) binary 版本 == pin 2.1.223（`--version` 輸出格式
// "2.1.223 (Claude Code)"，取第一個 field）；(2) plannerArgs 與凍結 probe 基準
// probeApprovedClaudeArgs 逐字相等。error 只在環境層失敗（binary 缺失／不可
// 執行／不可讀）回；檢驗不符走 OK=false＋Reason。兩者在 app 層皆 fail closed。
func PreflightClaude(ctx context.Context, bin string, plannerArgs []string) (PreflightResult, error) {
	res := PreflightResult{Provider: "claude", BinaryPath: bin}
	digest, err := BinarySHA256(ctx, bin)
	if err != nil {
		return res, err
	}
	res.BinaryDigest = digest
	raw, err := binaryVersionOutput(ctx, bin)
	if err != nil {
		return res, err
	}
	res.Version = raw
	if f := strings.Fields(raw); len(f) > 0 {
		res.Version = f[0]
	}
	if res.Version != claudePinVersion {
		res.Reason = fmt.Sprintf("claude binary 版本 %q ≠ pin %q", res.Version, claudePinVersion)
		return res, nil
	}
	if !slices.Equal(plannerArgs, probeApprovedClaudeArgs) {
		res.Reason = fmt.Sprintf("planner argv 偏離凍結 probe 基準：got %q want %q",
			plannerArgs, probeApprovedClaudeArgs)
		return res, nil
	}
	res.OK = true
	return res, nil
}

// PreflightCodex 驗證 binary 版本 == pin 0.146.1（`--version` 輸出格式
// "codex-cli 0.146.1"，取最後一個 field）。wire 與 handler 的 enforcement
// 證明由 production-path 測試提供（oneshot_test.go 的 fake app-server 走真
// codexAssist.Run），preflight runtime 檢查限版本＋binary digest。
func PreflightCodex(ctx context.Context, bin string) (PreflightResult, error) {
	res := PreflightResult{Provider: "codex", BinaryPath: bin}
	digest, err := BinarySHA256(ctx, bin)
	if err != nil {
		return res, err
	}
	res.BinaryDigest = digest
	raw, err := binaryVersionOutput(ctx, bin)
	if err != nil {
		return res, err
	}
	res.Version = raw
	if f := strings.Fields(raw); len(f) > 0 {
		res.Version = f[len(f)-1]
	}
	if res.Version != codexPinVersion {
		res.Reason = fmt.Sprintf("codex binary 版本 %q ≠ pin %q", res.Version, codexPinVersion)
		return res, nil
	}
	res.OK = true
	return res, nil
}
