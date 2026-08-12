package assist

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// enforcement 證據（argv／wire 建構斷言，非 behavioral）：Claude one-shot argv 含
// `--tools ""`、Codex turn wire 帶 sandboxPolicy={readOnly,networkAccess:false}
// ＋ approvalPolicy="never"。zero workspace mutation 的強制點在此，非 prompt。

func TestClaudeAssistArgvDisablesTools(t *testing.T) {
	args := ClaudeAssistArgs()
	idx := -1
	for i, s := range args {
		if s == "--tools" {
			idx = i
			break
		}
	}
	if idx < 0 || idx+1 >= len(args) || args[idx+1] != "" {
		t.Fatalf("claude assist argv must carry --tools \"\" (empty allowed-tools): %#v", args)
	}
	// 確認 NewClaudeAssist 產出實作 Runner 的實例（同一 argv 建構路徑）。
	if NewClaudeAssist("claude", "/tmp", nil) == nil {
		t.Fatal("NewClaudeAssist must return a Runner")
	}
}

func TestCodexAssistTurnParamsEnforceReadOnlyNoApproval(t *testing.T) {
	b, _ := json.Marshal(CodexAssistTurnParams("t1", "draft"))
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	sp, ok := got["sandboxPolicy"].(map[string]any)
	if !ok || sp["type"] != "readOnly" || sp["networkAccess"] != false {
		t.Fatalf("turn wire must enforce readOnly sandbox with network off, got: %s", b)
	}
	if got["approvalPolicy"] != "never" {
		t.Fatalf("turn wire must set approvalPolicy=never, got: %s", b)
	}
	if got["threadId"] != "t1" {
		t.Fatalf("turn wire must target the ephemeral thread, got: %s", b)
	}
	if NewCodexAssist("codex", "/tmp", nil) == nil {
		t.Fatal("NewCodexAssist must return a Runner")
	}
}

// fail loud：Claude stdout 出現 >16MB 行（scanner ErrTooLong）時，必須 surface
// 一個 stream_error（不可無聲以 clean-EOF 收場）——對齊 session.go 的驗收慣例。
func TestClaudeAssistFailsLoudOnOversizedLine(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fakeclaude.sh")
	// 讀掉 prompt 行後吐一條 >16MB 的行（超過 scanner 上限）。
	script := "#!/bin/sh\nread -r _line\nhead -c 17000000 /dev/zero | tr '\\0' 'a'\necho\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var sawStreamErr bool
	// sink 由 Run 序列呼叫（單一 goroutine），無需額外同步。
	err := NewClaudeAssist(bin, dir, nil).Run(ctx, "draft", func(env contract.Envelope) {
		if env.Kind == string(contract.KindStreamError) {
			sawStreamErr = true
		}
	})
	if !sawStreamErr {
		t.Fatalf("oversized line must surface a stream_error (fail loud); run err=%v", err)
	}
}

func TestCodexAssistThreadParamsEnforceNever(t *testing.T) {
	b, _ := json.Marshal(CodexAssistThreadParams(""))
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["approvalPolicy"] != "never" {
		t.Fatalf("thread/start must set approvalPolicy=never, got: %s", b)
	}
	sp, ok := got["sandboxPolicy"].(map[string]any)
	if !ok || sp["type"] != "readOnly" {
		t.Fatalf("thread/start must set readOnly sandbox, got: %s", b)
	}
	if _, hasThread := got["threadId"]; hasThread {
		t.Fatalf("fresh ephemeral thread must not carry threadId, got: %s", b)
	}
}
