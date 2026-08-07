package approval

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

type rpcFrame struct {
	ID     any             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func lineChan(r io.Reader) <-chan []byte {
	ch := make(chan []byte, 16)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			ch <- append([]byte(nil), sc.Bytes()...)
		}
	}()
	return ch
}

// readResult 以 channel + timeout 讀指定 id 的 result；逾時或串流關閉都會 fail 而非阻塞。
func readResult(t *testing.T, lines <-chan []byte, wantID float64) json.RawMessage {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("stream closed before result")
			}
			var f rpcFrame
			if json.Unmarshal(line, &f) != nil || f.ID == nil {
				continue
			}
			if id, ok := f.ID.(float64); ok && id == wantID {
				if f.Error != nil {
					t.Fatalf("rpc error: %s", f.Error)
				}
				return f.Result
			}
		case <-deadline:
			t.Fatal("timeout waiting rpc result")
		}
	}
}

func strconvID(id float64) string { return strconv.FormatFloat(id, 'f', -1, 64) }

func runServer(t *testing.T, sock string) (io.Writer, <-chan []byte) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() { _ = RunMCPServer(sock, inR, outW) }()
	t.Cleanup(func() { inW.Close() })
	return inW, lineChan(outR)
}

func handshake(t *testing.T, in io.Writer, out <-chan []byte) {
	t.Helper()
	io.WriteString(in, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`+"\n")
	readResult(t, out, 1)
	io.WriteString(in, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n")
}

func callApproval(t *testing.T, in io.Writer, out <-chan []byte, id float64) (behavior, message string, updated json.RawMessage) {
	t.Helper()
	io.WriteString(in, `{"jsonrpc":"2.0","id":`+strconvID(id)+`,"method":"tools/call","params":{"name":"approval_prompt","arguments":{"tool_name":"Bash","input":{"command":"touch x"}}}}`+"\n")
	res := readResult(t, out, id)
	var call struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(res, &call); err != nil || len(call.Content) == 0 {
		t.Fatalf("bad tools/call result: %s", res)
	}
	var reply struct {
		Behavior     string          `json:"behavior"`
		Message      string          `json:"message"`
		UpdatedInput json.RawMessage `json:"updatedInput"`
		ID           string          `json:"id"`
	}
	if err := json.Unmarshal([]byte(call.Content[0].Text), &reply); err != nil {
		t.Fatalf("reply not json: %s", call.Content[0].Text)
	}
	if reply.ID != "" {
		t.Fatal("reply must not leak internal id")
	}
	return reply.Behavior, reply.Message, reply.UpdatedInput
}

func TestE2EAllow(t *testing.T) {
	br, sock, _ := newTestBroker(t, 2*time.Second)
	go func() {
		req := <-br.Pending()
		br.Resolve(req.ID, Decision{Behavior: "allow"})
	}()
	in, out := runServer(t, sock)
	handshake(t, in, out)
	io.WriteString(in, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`+"\n")
	if res := readResult(t, out, 2); !strings.Contains(string(res), `"approval_prompt"`) {
		t.Fatalf("tools/list missing approval_prompt: %s", res)
	}
	behavior, _, updated := callApproval(t, in, out, 3)
	if behavior != "allow" || len(updated) == 0 {
		t.Fatalf("allow must carry updatedInput; got %s / %s", behavior, updated)
	}
}

func TestE2EDenyViaBroker(t *testing.T) { // 完整 broker 鏈的 deny（本輪修正）
	br, sock, _ := newTestBroker(t, 2*time.Second)
	go func() {
		req := <-br.Pending()
		br.Resolve(req.ID, Decision{Behavior: "deny", Message: "operator denied"})
	}()
	in, out := runServer(t, sock)
	handshake(t, in, out)
	behavior, message, updated := callApproval(t, in, out, 2)
	if behavior != "deny" || !strings.Contains(message, "operator denied") || len(updated) != 0 {
		t.Fatalf("deny via broker wrong: %s / %s / %s", behavior, message, updated)
	}
}

func TestE2EDenyOnBrokerDown(t *testing.T) {
	in, out := runServer(t, "/nonexistent.sock")
	handshake(t, in, out)
	behavior, message, _ := callApproval(t, in, out, 2)
	if behavior != "deny" || !strings.Contains(message, "fail closed") {
		t.Fatalf("broker down must deny fail-closed: %s / %s", behavior, message)
	}
}

func TestE2ERawParamsFullChain(t *testing.T) { // v1.4：initialize → tools/call（含未知巢狀 sentinel）→ socket → broker audit 的結構等價
	br, sock, audit := newTestBroker(t, 2*time.Second)
	go func() {
		req := <-br.Pending()
		br.Resolve(req.ID, Decision{Behavior: "deny", Message: "n"})
	}()
	in, out := runServer(t, sock)
	handshake(t, in, out)
	sent := `{"tool_name":"Bash","input":{"command":"touch x"},"x_sentinel":{"deep":[1,"two"],"note":"probe"}}`
	io.WriteString(in, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"approval_prompt","arguments":`+sent+`}}`+"\n")
	readResult(t, out, 2)
	var got any
	for _, line := range bytes.Split(audit.Bytes(), []byte("\n")) {
		var rec struct {
			Kind string `json:"kind"`
			Data struct {
				RawParams json.RawMessage `json:"raw_params"`
			} `json:"data"`
		}
		if json.Unmarshal(line, &rec) == nil && rec.Kind == "request" && len(rec.Data.RawParams) > 0 {
			var params struct {
				Arguments any `json:"arguments"`
			}
			if json.Unmarshal(rec.Data.RawParams, &params) == nil {
				got = params.Arguments
			}
		}
	}
	var want any
	_ = json.Unmarshal([]byte(sent), &want)
	if !reflect.DeepEqual(got, want) { // go-sdk 路徑必須保留未知巢狀欄位
		t.Fatalf("raw_params not preserved through full chain:\n got: %v\nwant: %v", got, want)
	}
}
