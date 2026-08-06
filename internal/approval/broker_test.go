package approval

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func newTestBroker(t *testing.T, timeout time.Duration) (*Broker, string, *bytes.Buffer) {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "a.sock")
	audit := &bytes.Buffer{}
	br, err := NewBroker(sock, timeout, audit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { br.Close() })
	return br, sock, audit
}

func dialAndAsk(t *testing.T, sock string, req Request) Decision {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	b, _ := json.Marshal(req)
	conn.Write(append(b, '\n'))
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second)) // 測試永不無限阻塞
	var d Decision
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestAllowRoundTrip(t *testing.T) {
	br, sock, audit := newTestBroker(t, time.Second)
	go func() {
		req := <-br.Pending()
		br.Resolve(req.ID, Decision{Behavior: "allow"})
	}()
	d := dialAndAsk(t, sock, Request{ID: "r1", ToolName: "Bash", Input: json.RawMessage(`{"command":"ls"}`)})
	if d.Behavior != "allow" {
		t.Fatalf("behavior = %s", d.Behavior)
	}
	if !bytes.Contains(audit.Bytes(), []byte(`"r1"`)) {
		t.Fatal("audit missing request id")
	}
}

func TestAllowEchoesUpdatedInput(t *testing.T) {
	br, sock, _ := newTestBroker(t, time.Second)
	go func() {
		req := <-br.Pending()
		br.Resolve(req.ID, Decision{Behavior: "allow"}) // 未帶 UpdatedInput
	}()
	d := dialAndAsk(t, sock, Request{ID: "r2", ToolName: "Bash", Input: json.RawMessage(`{"command":"touch x"}`)})
	if string(d.UpdatedInput) != `{"command":"touch x"}` {
		t.Fatalf("updatedInput not echoed: %s", d.UpdatedInput)
	}
}

func TestDenyCarriesMessage(t *testing.T) {
	br, sock, _ := newTestBroker(t, time.Second)
	go func() {
		req := <-br.Pending()
		br.Resolve(req.ID, Decision{Behavior: "deny", Message: "operator denied"})
	}()
	d := dialAndAsk(t, sock, Request{ID: "r3", ToolName: "Bash"})
	if d.Behavior != "deny" || d.Message != "operator denied" {
		t.Fatalf("decision = %+v", d)
	}
}

func TestTimeoutDeniesFailClosed(t *testing.T) {
	_, sock, audit := newTestBroker(t, 50*time.Millisecond)
	d := dialAndAsk(t, sock, Request{ID: "r4", ToolName: "Bash"}) // 無人 Resolve
	if d.Behavior != "deny" {
		t.Fatalf("timeout must deny, got %s", d.Behavior)
	}
	if !bytes.Contains(audit.Bytes(), []byte("timeout")) {
		t.Fatal("audit missing timeout cause")
	}
}

func TestAuditKeepsRawParams(t *testing.T) {
	br, sock, audit := newTestBroker(t, time.Second)
	go func() {
		req := <-br.Pending()
		br.Resolve(req.ID, Decision{Behavior: "deny", Message: "n"})
	}()
	dialAndAsk(t, sock, Request{ID: "r5", ToolName: "Bash",
		RawParams: json.RawMessage(`{"name":"approval_prompt","arguments":{"k":"v"}}`)})
	if !bytes.Contains(audit.Bytes(), []byte(`"arguments"`)) {
		t.Fatal("audit must keep raw params verbatim (contract probe)")
	}
}
