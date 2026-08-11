package assist

import (
	"encoding/json"
	"testing"
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
