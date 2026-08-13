package assist

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
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

// probeApprovedClaudeArgs：Claude PlannerAssist 的凍結 argv 基準。前 8 個 token
// 逐字抄自 Task 0 live probe（docs/spikes/m3a1-planner-probe.md §2(c)——probe
// 與當時的 ClaudePlannerArgs() 完全一致），另加 `--setting-sources ""`
// （Task 7 實測：pin 2.1.223 支援該旗標；空來源下 SessionStart hook 0 筆、無
// .remember/ 檔案副作用——隔離使用者全域／project／local settings 的 hook，
// 堵住非白名單路徑寫入）。白名單 enforcement 在 `--tools`（probe 的 GO 判定
// 依據），不受新旗標影響。ClaudePlannerArgs() 改動而未同步此基準（或反之）
// 即 preflight 失敗——argv 分岔 fail closed，不無聲放行。
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
func BinarySHA256(bin string) (string, error) {
	f, err := os.Open(bin)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// binaryVersionOutput 執行 `<bin> --version` 並回傳 trim 後的 stdout。
func binaryVersionOutput(bin string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("assist: %s --version: %w", bin, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// PreflightClaude 驗證 (1) binary 版本 == pin 2.1.223（`--version` 輸出格式
// "2.1.223 (Claude Code)"，取第一個 field）；(2) plannerArgs 與凍結 probe 基準
// probeApprovedClaudeArgs 逐字相等。error 只在環境層失敗（binary 缺失／不可
// 執行／不可讀）回；檢驗不符走 OK=false＋Reason。兩者在 app 層皆 fail closed。
func PreflightClaude(bin string, plannerArgs []string) (PreflightResult, error) {
	res := PreflightResult{Provider: "claude", BinaryPath: bin}
	digest, err := BinarySHA256(bin)
	if err != nil {
		return res, err
	}
	res.BinaryDigest = digest
	raw, err := binaryVersionOutput(bin)
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
func PreflightCodex(bin string) (PreflightResult, error) {
	res := PreflightResult{Provider: "codex", BinaryPath: bin}
	digest, err := BinarySHA256(bin)
	if err != nil {
		return res, err
	}
	res.BinaryDigest = digest
	raw, err := binaryVersionOutput(bin)
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
