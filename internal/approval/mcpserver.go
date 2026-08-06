package approval

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// BrokerTimeout 讀取 WORKBENCH_APPROVAL_TIMEOUT（Go duration），預設 120s。
func BrokerTimeout() time.Duration {
	if v := os.Getenv("WORKBENCH_APPROVAL_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return 120 * time.Second
}

func newULID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// RunMCPServer 以 stdio MCP server 提供 approval_prompt tool；決定經 unix socket
// 轉送 broker，socket 不可用／逾時（broker timeout + 5s）一律 deny（fail closed）。
func RunMCPServer(socketPath string, stdin io.Reader, stdout io.Writer) error {
	timeout := BrokerTimeout() + 5*time.Second
	srv := mcp.NewServer(&mcp.Implementation{Name: "workbench", Version: "0.0.1"}, nil)
	srv.AddTool(&mcp.Tool{
		Name:        "approval_prompt",
		Description: "Route a permission request to the workbench operator.",
		InputSchema: json.RawMessage(`{"type":"object","additionalProperties":true}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		rawParams, _ := json.Marshal(struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments,omitempty"`
		}{req.Params.Name, req.Params.Arguments})
		text := handleApproval(socketPath, timeout, rawParams)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil
	})
	return srv.Run(context.Background(), &mcp.IOTransport{
		Reader: io.NopCloser(stdin),
		Writer: nopWriteCloser{stdout},
	})
}

func handleApproval(sock string, timeout time.Duration, rawParams json.RawMessage) string {
	var p struct {
		Arguments struct {
			ToolName string          `json:"tool_name"`
			Input    json.RawMessage `json:"input"`
		} `json:"arguments"`
	}
	_ = json.Unmarshal(rawParams, &p) // best-effort；真實欄位名以 A2 錄流為準
	d := forwardToSocket(sock, Request{ID: newULID(), ToolName: p.Arguments.ToolName,
		Input: p.Arguments.Input, RawParams: rawParams}, timeout)
	reply := struct {
		Behavior     string          `json:"behavior"`
		Message      string          `json:"message,omitempty"`
		UpdatedInput json.RawMessage `json:"updatedInput,omitempty"`
	}{d.Behavior, d.Message, d.UpdatedInput}
	out, _ := json.Marshal(reply)
	return string(out)
}

func forwardToSocket(sock string, req Request, timeout time.Duration) Decision {
	deny := func(msg string) Decision { return Decision{Behavior: "deny", Message: msg + " (fail closed)"} }
	conn, err := net.DialTimeout("unix", sock, timeout)
	if err != nil {
		return deny("approval broker unavailable")
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	b, _ := json.Marshal(req)
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return deny("approval broker write failed")
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return deny("approval broker read failed")
	}
	var d Decision
	if json.Unmarshal(line, &d) != nil {
		return deny("approval broker bad reply")
	}
	return d
}
