package assist

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/codex"
	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// ---- preflight 單元（fake bin script；§3.4）----

// writeFakeVersionBin：只回應 `--version` 的 fake CLI script。
func writeFakeVersionBin(t *testing.T, dir, name, versionLine string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	script := "#!/bin/sh\necho \"" + versionLine + "\"\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPreflightClaudeOKOnPinAndFrozenArgv(t *testing.T) {
	bin := writeFakeVersionBin(t, t.TempDir(), "claude", "2.1.223 (Claude Code)")
	res, err := PreflightClaude(bin, ClaudePlannerArgs())
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Fatalf("pin version + frozen argv must pass preflight, reason: %s", res.Reason)
	}
	if res.Version != "2.1.223" {
		t.Fatalf("parsed version = %q, want 2.1.223", res.Version)
	}
	content, rerr := os.ReadFile(bin)
	if rerr != nil {
		t.Fatal(rerr)
	}
	sum := sha256.Sum256(content)
	if want := hex.EncodeToString(sum[:]); res.BinaryDigest != want {
		t.Fatalf("BinaryDigest must be the full untruncated SHA-256 hex: got %q want %q", res.BinaryDigest, want)
	}
}

func TestPreflightClaudeRejectsWrongVersion(t *testing.T) {
	bin := writeFakeVersionBin(t, t.TempDir(), "claude", "2.1.222 (Claude Code)")
	res, err := PreflightClaude(bin, ClaudePlannerArgs())
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatal("non-pin claude version must fail preflight")
	}
	if !strings.Contains(res.Reason, "2.1.222") || !strings.Contains(res.Reason, "2.1.223") {
		t.Fatalf("reason must name got/pin versions, got: %s", res.Reason)
	}
}

func TestPreflightClaudeRejectsArgvDrift(t *testing.T) {
	bin := writeFakeVersionBin(t, t.TempDir(), "claude", "2.1.223 (Claude Code)")
	for name, args := range map[string][]string{
		"extra flag":              append(slices.Clone(ClaudePlannerArgs()), "--extra"),
		"spec-assist argv":        ClaudeAssistArgs(),
		"widened tool whitelist":  {"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--setting-sources", "", "--tools", "Read,Glob,Grep,Write"},
		"missing setting-sources": {"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--tools", "Read,Glob,Grep"},
	} {
		res, err := PreflightClaude(bin, args)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if res.OK {
			t.Fatalf("%s: argv drift from the frozen probe baseline must fail preflight", name)
		}
		if !strings.Contains(res.Reason, "argv") {
			t.Fatalf("%s: reason must point at the argv baseline, got: %s", name, res.Reason)
		}
	}
}

// 凍結基準與 production argv 必須逐字一致——任何一邊單獨改動即紅（drift 守門）。
func TestClaudePlannerArgsMatchesFrozenBaseline(t *testing.T) {
	if !slices.Equal(ClaudePlannerArgs(), probeApprovedClaudeArgs) {
		t.Fatalf("ClaudePlannerArgs() %q must equal the frozen probe baseline %q",
			ClaudePlannerArgs(), probeApprovedClaudeArgs)
	}
}

func TestPreflightCodexVersionPin(t *testing.T) {
	dir := t.TempDir()
	ok := writeFakeVersionBin(t, dir, "codex-ok", "codex-cli 0.146.1")
	res, err := PreflightCodex(ok)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Version != "0.146.1" {
		t.Fatalf("pin codex version must pass, got OK=%v version=%q reason=%s", res.OK, res.Version, res.Reason)
	}
	if len(res.BinaryDigest) != 64 {
		t.Fatalf("codex BinaryDigest must be full sha256 hex, got %q", res.BinaryDigest)
	}
	bad := writeFakeVersionBin(t, dir, "codex-bad", "codex-cli 0.150.0")
	res, err = PreflightCodex(bad)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK || !strings.Contains(res.Reason, "0.150.0") {
		t.Fatalf("non-pin codex version must fail with the version in the reason, got OK=%v reason=%s", res.OK, res.Reason)
	}
}

func TestPreflightMissingBinaryErrors(t *testing.T) {
	if _, err := PreflightClaude(filepath.Join(t.TempDir(), "absent"), ClaudePlannerArgs()); err == nil {
		t.Fatal("missing claude binary must surface an error")
	}
	if _, err := PreflightCodex(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("missing codex binary must surface an error")
	}
}

// ---- Codex production-path enforcement（Step 2b：fake app-server、真 Run）----

// writeFakeCodexAppServer：stdio JSONL fake app-server。所有 client→server
// frame 逐行落到 $FAKE_WIRE_LOG（wire 截取）；FAKE_APPROVAL=1 時在 turn/start
// 後注入 approval request（驗 fail-closed typed violation），否則送
// turn/completed 正常收尾。
func writeFakeCodexAppServer(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "fake-codex")
	script := `#!/usr/bin/env bash
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$FAKE_WIRE_LOG"
  id=$(printf '%s' "$line" | grep -oE '"id":[0-9]+' | head -1 | cut -d: -f2)
  case "$line" in
    *'"method":"initialize"'*) printf '{"id":%s,"result":{}}\n' "$id" ;;
    *'"method":"thread/start"'*) printf '{"id":%s,"result":{"thread":{"id":"t1"}}}\n' "$id" ;;
    *'"method":"turn/start"'*)
      printf '{"id":%s,"result":{}}\n' "$id"
      if [ -n "${FAKE_APPROVAL:-}" ]; then
        printf '{"id":9001,"method":"item/commandExecution/requestApproval","params":{"threadId":"t1","itemId":"i1"}}\n'
      else
        printf '{"method":"turn/completed","params":{"threadId":"t1","turn":{"id":"turn-1"}}}\n'
      fi ;;
  esac
done
`
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// capturedWireParams：從 wire log 找指定 method 的 request params。
func capturedWireParams(t *testing.T, logPath, method string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var f struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal([]byte(line), &f) != nil || f.Method != method {
			continue
		}
		var params map[string]any
		if err := json.Unmarshal(f.Params, &params); err != nil {
			t.Fatalf("captured %s params unmarshal: %v", method, err)
		}
		return params
	}
	t.Fatalf("wire log has no %s frame; log:\n%s", method, data)
	return nil
}

func assertReadOnlyNever(t *testing.T, params map[string]any, method string, wantNetworkKey bool) {
	t.Helper()
	if params["approvalPolicy"] != "never" {
		t.Fatalf("%s wire must carry approvalPolicy=never, got: %v", method, params["approvalPolicy"])
	}
	sp, ok := params["sandboxPolicy"].(map[string]any)
	if !ok || sp["type"] != "readOnly" {
		t.Fatalf("%s wire must carry sandboxPolicy.type=readOnly, got: %v", method, params["sandboxPolicy"])
	}
	if wantNetworkKey && sp["networkAccess"] != false {
		t.Fatalf("%s wire must carry sandboxPolicy.networkAccess=false, got: %v", method, sp["networkAccess"])
	}
}

// (i) production codexAssist.Run 送出的實際 wire 帶 readOnly＋never（builder
// 繞過即紅）；(iii) 事件僅進呼叫端 sink（assist lane），Run 不觸碰任何 session
// store——app 層 lane 隔離由 app_assist_test.go 的 provider-view 斷言接手。
func TestCodexAssistProductionWireEnforcesReadOnlyNever(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeCodexAppServer(t, dir)
	logPath := filepath.Join(dir, "wire.jsonl")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var sunk []contract.Envelope
	err := NewCodexPlanner(bin, dir, []string{"FAKE_WIRE_LOG=" + logPath}).
		Run(ctx, "draft the plan", func(env contract.Envelope) { sunk = append(sunk, env) })
	if err != nil {
		t.Fatalf("fake app-server happy path must complete: %v", err)
	}
	assertReadOnlyNever(t, capturedWireParams(t, logPath, codex.MethodThreadStart), codex.MethodThreadStart, false)
	turn := capturedWireParams(t, logPath, codex.MethodTurnStart)
	assertReadOnlyNever(t, turn, codex.MethodTurnStart, true)
	if turn["threadId"] != "t1" {
		t.Fatalf("turn/start must target the ephemeral thread, got: %v", turn["threadId"])
	}
	if len(sunk) == 0 {
		t.Fatal("turn/completed must reach the assist sink")
	}
}

// (ii) fake server 注入 approval request → 真 Run 回 typed *EnforcementViolation
// 且 runner 終止（Run 返回即 defer Terminate＋Wait 收整組）。
func TestCodexAssistProductionApprovalFailsClosedTyped(t *testing.T) {
	dir := t.TempDir()
	bin := writeFakeCodexAppServer(t, dir)
	logPath := filepath.Join(dir, "wire.jsonl")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := NewCodexPlanner(bin, dir, []string{"FAKE_WIRE_LOG=" + logPath, "FAKE_APPROVAL=1"}).
		Run(ctx, "draft the plan", func(contract.Envelope) {})
	var viol *EnforcementViolation
	if !errors.As(err, &viol) {
		t.Fatalf("injected approval request must surface a typed *EnforcementViolation, got: %v", err)
	}
	if viol.Provider != "codex" || viol.Detail != codex.MethodCmdExecRequestApproval {
		t.Fatalf("violation must carry provider/method, got: %+v", viol)
	}
	if !strings.Contains(viol.Error(), "fail closed") {
		t.Fatalf("violation Error() must keep the fail-closed wording, got: %s", viol.Error())
	}
}
