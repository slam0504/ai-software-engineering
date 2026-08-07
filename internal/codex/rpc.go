package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
)

// RequestID 保留 wire 原始 ID 型別——pinned schema 的 RequestId 是
// string | int64 union（RequestId.json），不得假設整數。
type RequestID struct{ raw json.RawMessage }

func NewIntID(n int64) *RequestID {
	return &RequestID{raw: json.RawMessage(strconv.FormatInt(n, 10))}
}

func (r *RequestID) UnmarshalJSON(b []byte) error {
	t := bytes.TrimSpace(b)
	if len(t) == 0 {
		return errors.New("codex: empty request id")
	}
	switch t[0] { // 只接受 string 或 number（schema union）
	case '"':
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
	default:
		return fmt.Errorf("codex: invalid request id %s", b)
	}
	r.raw = append(json.RawMessage(nil), t...)
	return nil
}

func (r RequestID) MarshalJSON() ([]byte, error) {
	if len(r.raw) == 0 {
		return nil, errors.New("codex: empty request id")
	}
	return r.raw, nil
}

// Key 回傳 pending map 用的正規化鍵（原始 JSON 文字，string 與 number 不會相撞）。
func (r *RequestID) Key() string { return string(r.raw) }

func (r *RequestID) String() string { return string(r.raw) }

// Frame 是 app-server wire 的單一 JSONL frame：JSON-RPC 2.0 語意但省略 jsonrpc 欄位。
type Frame struct {
	ID     *RequestID      `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Conn struct {
	stdin   io.Writer
	writeMu sync.Mutex

	nextID int64 // atomic

	pendMu   sync.Mutex
	pending  map[string]chan Frame
	closed   bool
	closeErr error

	hsDone atomic.Bool

	hMu             sync.Mutex
	onServerRequest func(method string, params json.RawMessage) (any, error)
	onNotification  func(method string, params json.RawMessage)
	onUnknown       func(raw []byte)

	// session-scoped 錄流（v1.6）：sink mutex + in-flight WaitGroup，Stop 原子 detach。
	recMu  sync.Mutex
	sink   func([]byte) error
	recWG  *sync.WaitGroup
	recErr error

	doneCh chan struct{} // 讀迴圈結束
}

func NewConn(stdin io.Writer, stdout io.Reader) *Conn {
	c := &Conn{stdin: stdin, pending: map[string]chan Frame{}, doneCh: make(chan struct{})}
	go c.readLoop(stdout)
	return c
}

func (c *Conn) OnServerRequest(h func(method string, params json.RawMessage) (any, error)) {
	c.hMu.Lock()
	defer c.hMu.Unlock()
	c.onServerRequest = h
}

func (c *Conn) OnNotification(h func(method string, params json.RawMessage)) {
	c.hMu.Lock()
	defer c.hMu.Unlock()
	c.onNotification = h
}

func (c *Conn) OnUnknown(h func(raw []byte)) {
	c.hMu.Lock()
	defer c.hMu.Unlock()
	c.onUnknown = h
}

func (c *Conn) serverRequestHandler() func(string, json.RawMessage) (any, error) {
	c.hMu.Lock()
	defer c.hMu.Unlock()
	return c.onServerRequest
}

func (c *Conn) notificationHandler() func(string, json.RawMessage) {
	c.hMu.Lock()
	defer c.hMu.Unlock()
	return c.onNotification
}

func (c *Conn) unknownHandler() func([]byte) {
	c.hMu.Lock()
	defer c.hMu.Unlock()
	return c.onUnknown
}

// BeginRecording 安裝 session-scoped sink；已在錄流中回 error。
func (c *Conn) BeginRecording(sink func([]byte) error) error {
	c.recMu.Lock()
	defer c.recMu.Unlock()
	if c.sink != nil {
		return errors.New("codex: recording already in progress")
	}
	c.sink, c.recWG, c.recErr = sink, &sync.WaitGroup{}, nil
	return nil
}

// StopRecording 原子 detach：摘除 sink、等待 in-flight callback 完成、
// 回傳本次錄流 latch 的首個錯誤並重設狀態。
func (c *Conn) StopRecording() error {
	c.recMu.Lock()
	wg := c.recWG
	c.sink = nil
	c.recMu.Unlock()
	if wg != nil {
		wg.Wait()
	}
	c.recMu.Lock()
	err := c.recErr
	c.recErr, c.recWG = nil, nil
	c.recMu.Unlock()
	return err
}

func (c *Conn) record(dir string, frame []byte) {
	c.recMu.Lock()
	sink, wg := c.sink, c.recWG
	if sink == nil {
		c.recMu.Unlock()
		return
	}
	wg.Add(1) // 在鎖內登記 in-flight，Stop 的 Wait 才能涵蓋本次 callback
	c.recMu.Unlock()
	defer wg.Done()
	env, _ := json.Marshal(struct {
		Dir   string          `json:"dir"`
		Frame json.RawMessage `json:"frame"`
	}{dir, frame})
	if err := sink(env); err != nil {
		c.recMu.Lock()
		if c.recErr == nil {
			c.recErr = err
		}
		c.recMu.Unlock()
	}
}

func (c *Conn) send(f Frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	c.record("c2s", b)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.stdin.Write(append(b, '\n'))
	return err
}

// Handshake：initialize（帶 clientInfo）→ initialized 通知；完成前 Call 其他方法回錯誤。
func (c *Conn) Handshake(ctx context.Context, ci ClientInfo) error {
	if c.hsDone.Load() {
		return errors.New("codex: already initialized")
	}
	if _, err := c.call(ctx, MethodInitialize, map[string]any{"clientInfo": ci}); err != nil {
		return err
	}
	if err := c.send(Frame{Method: MethodInitialized}); err != nil {
		return err
	}
	c.hsDone.Store(true)
	return nil
}

// Call 送出 request 並等待 response；ctx select，不阻塞。
func (c *Conn) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if method != MethodInitialize && !c.hsDone.Load() {
		return nil, errors.New("codex: connection not initialized (run Handshake first)")
	}
	return c.call(ctx, method, params)
}

func (c *Conn) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	rid := NewIntID(atomic.AddInt64(&c.nextID, 1))
	raw := json.RawMessage(`{}`)
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		raw = b
	}
	ch := make(chan Frame, 1)
	c.pendMu.Lock()
	if c.closed {
		err := c.closeErr
		c.pendMu.Unlock()
		return nil, fmt.Errorf("codex: connection closed: %w", err)
	}
	c.pending[rid.Key()] = ch
	c.pendMu.Unlock()
	defer func() {
		c.pendMu.Lock()
		delete(c.pending, rid.Key())
		c.pendMu.Unlock()
	}()
	if err := c.send(Frame{ID: rid, Method: method, Params: raw}); err != nil {
		return nil, err
	}
	select {
	case f := <-ch:
		if len(f.Error) > 0 {
			var e struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			}
			_ = json.Unmarshal(f.Error, &e)
			return nil, fmt.Errorf("codex rpc %s: code %d: %s", method, e.Code, e.Message)
		}
		return f.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.doneCh:
		return nil, fmt.Errorf("codex: connection closed: %v", c.closeErr)
	}
}

// Done 在讀迴圈結束（連線不再可用）後關閉。
func (c *Conn) Done() <-chan struct{} { return c.doneCh }

func (c *Conn) readLoop(stdout io.Reader) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := append([]byte(nil), sc.Bytes()...)
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		c.record("s2c", line)
		var f Frame
		if err := json.Unmarshal(line, &f); err != nil {
			if h := c.unknownHandler(); h != nil {
				h(line)
			}
			continue
		}
		switch {
		case f.ID != nil && f.Method == "": // response
			c.pendMu.Lock()
			ch, ok := c.pending[f.ID.Key()]
			c.pendMu.Unlock()
			if ok {
				ch <- f
			}
		case f.ID != nil && f.Method != "": // server → client request
			go c.handleServerRequest(f)
		case f.Method != "": // notification
			if ServerNotifications[f.Method] {
				if h := c.notificationHandler(); h != nil {
					h(f.Method, f.Params)
				}
			} else if h := c.unknownHandler(); h != nil {
				h(line)
			}
		default:
			if h := c.unknownHandler(); h != nil {
				h(line)
			}
		}
	}
	err := sc.Err()
	if err == nil {
		err = io.EOF
	}
	c.pendMu.Lock()
	c.closed, c.closeErr = true, err
	c.pendMu.Unlock()
	close(c.doneCh) // 喚醒所有 in-flight Call（select doneCh 分支）
}

func (c *Conn) handleServerRequest(f Frame) {
	h := c.serverRequestHandler()
	if h == nil {
		eb, _ := json.Marshal(map[string]any{"code": -32601, "message": "no server request handler installed"})
		_ = c.send(Frame{ID: f.ID, Error: eb})
		return
	}
	res, err := h(f.Method, f.Params)
	if err != nil {
		eb, _ := json.Marshal(map[string]any{"code": -32000, "message": err.Error()})
		_ = c.send(Frame{ID: f.ID, Error: eb})
		return
	}
	rb, err := json.Marshal(res)
	if err != nil {
		eb, _ := json.Marshal(map[string]any{"code": -32000, "message": err.Error()})
		_ = c.send(Frame{ID: f.ID, Error: eb})
		return
	}
	_ = c.send(Frame{ID: f.ID, Result: rb})
}
