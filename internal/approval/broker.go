package approval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

type Request struct {
	ID        string          `json:"id"`
	ToolName  string          `json:"tool_name"`
	Input     json.RawMessage `json:"input,omitempty"`
	RawParams json.RawMessage `json:"raw_params,omitempty"`
}

type Decision struct {
	ID           string          `json:"id"`
	Behavior     string          `json:"behavior"` // allow | deny
	Message      string          `json:"message,omitempty"`
	UpdatedInput json.RawMessage `json:"updatedInput,omitempty"`
}

type Broker struct {
	ln      net.Listener
	timeout time.Duration
	audit   io.Writer
	pending chan Request
	mu      sync.Mutex
	waiters map[string]chan Decision
}

func NewBroker(socketPath string, timeout time.Duration, audit io.Writer) (*Broker, error) {
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	b := &Broker{ln: ln, timeout: timeout, audit: audit,
		pending: make(chan Request, 16), waiters: map[string]chan Decision{}}
	go b.acceptLoop()
	return b, nil
}

func (b *Broker) Pending() <-chan Request { return b.pending }

func (b *Broker) Resolve(id string, d Decision) error {
	b.mu.Lock()
	ch, ok := b.waiters[id]
	b.mu.Unlock()
	if !ok {
		return fmt.Errorf("no pending request %s", id)
	}
	d.ID = id
	ch <- d
	return nil
}

func (b *Broker) Close() error { return b.ln.Close() }

func (b *Broker) log(kind string, v any) {
	rec, _ := json.Marshal(map[string]any{"ts": time.Now().UTC().Format(time.RFC3339Nano), "kind": kind, "data": v})
	fmt.Fprintf(b.audit, "%s\n", rec)
}

func (b *Broker) acceptLoop() {
	for {
		conn, err := b.ln.Accept()
		if err != nil {
			return
		}
		go b.serve(conn)
	}
}

func (b *Broker) serve(conn net.Conn) {
	defer conn.Close()
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var req Request
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			b.log("malformed_request", string(sc.Bytes()))
			continue
		}
		b.log("request", req) // 含 RawParams 原文（contract probe）
		ch := make(chan Decision, 1)
		b.mu.Lock()
		b.waiters[req.ID] = ch
		b.mu.Unlock()
		b.pending <- req

		var d Decision
		select {
		case d = <-ch:
		case <-time.After(b.timeout):
			d = Decision{ID: req.ID, Behavior: "deny", Message: "approval timeout (fail closed)"}
			b.log("timeout", req.ID)
		}
		if d.Behavior == "allow" && len(d.UpdatedInput) == 0 {
			d.UpdatedInput = req.Input // 官方 allow 回覆必須含 updatedInput
		}
		b.mu.Lock()
		delete(b.waiters, req.ID)
		b.mu.Unlock()
		b.log("decision", d)
		out, _ := json.Marshal(d)
		conn.Write(append(out, '\n'))
	}
}
