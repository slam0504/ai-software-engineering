package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestThreadRunnerWireLifecycle(t *testing.T) { // response 立即回 inProgress、completed 解鎖
	p := newFakePair(t)
	p.fake.onReq = func(fr Frame) {
		switch fr.Method {
		case MethodThreadStart:
			p.fake.send(map[string]any{"id": *fr.ID, "result": map[string]any{"thread": map[string]any{"id": "t1"}}})
		case MethodTurnStart:
			p.fake.send(map[string]any{"id": *fr.ID, "result": map[string]any{
				"turn": map[string]any{"id": "turn-A", "status": "inProgress"}}})
		}
	}
	doHandshake(t, p.conn)
	r := NewThreadRunner(p.conn)
	ctx := context.Background()
	if _, err := r.EnsureThread(ctx, "", "untrusted"); err != nil {
		t.Fatal(err)
	}
	if id2, _ := r.EnsureThread(ctx, "", "untrusted"); id2 != "t1" { // 冪等
		t.Fatal("EnsureThread must be idempotent")
	}
	turnID, ended, err := r.StartTurn(ctx, "one")
	if err != nil || turnID != "turn-A" || ended || r.ActiveTurnID() != "turn-A" {
		t.Fatalf("start: %s %v %v", turnID, ended, err)
	}
	if _, _, err := r.StartTurn(ctx, "two"); err != ErrTurnActive {
		t.Fatalf("busy must reject, got %v", err)
	}
	if r.NoteTurnEnded("turn-OTHER") {
		t.Fatal("mismatched turn id must be ignored")
	}
	if !r.NoteTurnEnded("turn-A") {
		t.Fatal("matching turn id must unlock")
	}
	if _, _, err := r.StartTurn(ctx, "two"); err != nil {
		t.Fatal(err)
	}
}

func TestThreadRunnerEarlyCompletedLatch(t *testing.T) { // completed 先到不得永久 busy
	p := newFakePair(t)
	r := NewThreadRunner(p.conn)
	ended := make(chan string, 2)
	p.conn.OnNotification(func(m string, params json.RawMessage) {
		if m == MethodTurnCompleted { // notification handler 真正呼叫 NoteTurnEnded
			var tp struct {
				Turn struct {
					ID string `json:"id"`
				} `json:"turn"`
			}
			_ = json.Unmarshal(params, &tp)
			if r.NoteTurnEnded(tp.Turn.ID) {
				ended <- tp.Turn.ID
			}
		}
	})
	p.fake.onReq = func(fr Frame) {
		switch fr.Method {
		case MethodThreadStart:
			p.fake.send(map[string]any{"id": *fr.ID, "result": map[string]any{"thread": map[string]any{"id": "t1"}}})
		case MethodTurnStart: // 惡意順序：completed 先送、response 後送
			p.fake.send(map[string]any{"method": MethodTurnCompleted,
				"params": map[string]any{"threadId": "t1", "turn": map[string]any{"id": "turn-A", "status": "completed"}}})
			p.fake.send(map[string]any{"id": *fr.ID, "result": map[string]any{
				"turn": map[string]any{"id": "turn-A", "status": "inProgress"}}})
		}
	}
	doHandshake(t, p.conn)
	if _, err := r.EnsureThread(context.Background(), "", "untrusted"); err != nil {
		t.Fatal(err)
	}
	turnID, alreadyEnded, err := r.StartTurn(context.Background(), "x")
	if err != nil || turnID != "turn-A" || !alreadyEnded {
		t.Fatalf("latch must reconcile: %s %v %v", turnID, alreadyEnded, err)
	}
	if r.ActiveTurnID() != "" {
		t.Fatal("runner must not stay busy")
	}
	if _, _, err := r.StartTurn(context.Background(), "y"); err != nil {
		t.Fatal(err)
	}
}

func TestThreadRunnerEmptyTurnIDIsError(t *testing.T) {
	p := newFakePair(t)
	p.fake.onReq = func(fr Frame) {
		switch fr.Method {
		case MethodThreadStart:
			p.fake.send(map[string]any{"id": *fr.ID, "result": map[string]any{"thread": map[string]any{"id": "t1"}}})
		case MethodTurnStart:
			p.fake.send(map[string]any{"id": *fr.ID, "result": map[string]any{}}) // 缺 turn
		}
	}
	doHandshake(t, p.conn)
	r := NewThreadRunner(p.conn)
	_, _ = r.EnsureThread(context.Background(), "", "untrusted")
	if _, _, err := r.StartTurn(context.Background(), "x"); err == nil {
		t.Fatal("missing turn id must be an error")
	}
	if _, _, err := r.StartTurn(context.Background(), "x"); err == nil { // 占位已清、可重試
		t.Fatal("pending must be cleared after error")
	}
}

func TestThreadRunnerBarrierOnlyOneWireRequest(t *testing.T) { // 並行 ownership 證明
	p := newFakePair(t)
	var turnStarts atomic.Int32
	p.fake.onReq = func(fr Frame) {
		switch fr.Method {
		case MethodThreadStart:
			p.fake.send(map[string]any{"id": *fr.ID, "result": map[string]any{"thread": map[string]any{"id": "t1"}}})
		case MethodTurnStart:
			turnStarts.Add(1)
			p.fake.send(map[string]any{"id": *fr.ID, "result": map[string]any{
				"turn": map[string]any{"id": "turn-A", "status": "inProgress"}}})
		}
	}
	doHandshake(t, p.conn)
	r := NewThreadRunner(p.conn)
	if _, err := r.EnsureThread(context.Background(), "", "untrusted"); err != nil {
		t.Fatal(err)
	}
	begin := make(chan struct{})
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			<-begin
			_, _, err := r.StartTurn(context.Background(), "x")
			errs <- err
		}()
	}
	close(begin)
	e1, e2 := <-errs, <-errs
	if !((e1 == nil && e2 == ErrTurnActive) || (e2 == nil && e1 == ErrTurnActive)) {
		t.Fatalf("exactly one must win: %v / %v", e1, e2)
	}
	waitFor(t, func() bool { return turnStarts.Load() == 1 }, "exactly one wire turn/start")
}

func TestThreadRunnerResume(t *testing.T) {
	p := newFakePair(t)
	var resumed struct {
		ThreadID       string `json:"threadId"`
		ApprovalPolicy string `json:"approvalPolicy"`
	}
	p.fake.onReq = func(fr Frame) {
		if fr.Method == MethodThreadResume {
			_ = json.Unmarshal(fr.Params, &resumed)
			p.fake.send(map[string]any{"id": *fr.ID, "result": map[string]any{"thread": map[string]any{"id": "t9"}}})
		}
	}
	doHandshake(t, p.conn)
	r := NewThreadRunner(p.conn)
	id, err := r.EnsureThread(context.Background(), "t9", "on-request")
	if err != nil || id != "t9" || resumed.ThreadID != "t9" || resumed.ApprovalPolicy != "on-request" {
		t.Fatalf("resume wiring: %v %s %+v", err, id, resumed)
	}
}

func TestRecordingSpansMultipleTurns(t *testing.T) { // session-scoped 錄流：跨輪 attach、Stop 一次
	p := newFakePair(t)
	var mu sync.Mutex
	var lines [][]byte
	if err := p.conn.BeginRecording(func(b []byte) error {
		mu.Lock()
		lines = append(lines, append([]byte(nil), b...))
		mu.Unlock()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	turnN := atomic.Int32{}
	p.fake.onReq = func(fr Frame) {
		switch fr.Method {
		case MethodThreadStart:
			p.fake.send(map[string]any{"id": *fr.ID, "result": map[string]any{"thread": map[string]any{"id": "t1"}}})
		case MethodTurnStart:
			n := turnN.Add(1)
			id := fmt.Sprintf("turn-%d", n)
			p.fake.send(map[string]any{"id": *fr.ID, "result": map[string]any{
				"turn": map[string]any{"id": id, "status": "inProgress"}}})
			p.fake.send(map[string]any{"method": MethodTurnCompleted,
				"params": map[string]any{"threadId": "t1", "turn": map[string]any{"id": id, "status": "completed"}}})
		}
	}
	done := make(chan string, 2)
	r := NewThreadRunner(p.conn)
	p.conn.OnNotification(func(m string, params json.RawMessage) {
		if m == MethodTurnCompleted {
			var tp struct {
				Turn struct {
					ID string `json:"id"`
				} `json:"turn"`
			}
			_ = json.Unmarshal(params, &tp)
			if r.NoteTurnEnded(tp.Turn.ID) {
				done <- tp.Turn.ID
			}
		}
	})
	doHandshake(t, p.conn)
	_, _ = r.EnsureThread(context.Background(), "", "untrusted")
	runTurn := func(prompt string) {
		_, ended, err := r.StartTurn(context.Background(), prompt)
		if err != nil {
			t.Fatal(err)
		}
		if !ended {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("turn not completed")
			}
		}
	}
	runTurn("one")
	runTurn("two") // 錄流全程 attach（turn 完成不 detach）
	if err := p.conn.StopRecording(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	var starts, completes int
	for _, b := range lines {
		if bytes.Contains(b, []byte(`"turn/start"`)) {
			starts++
		}
		if bytes.Contains(b, []byte(`"turn/completed"`)) {
			completes++
		}
	}
	if starts != 2 || completes != 2 {
		t.Fatalf("recording must span both turns: starts=%d completes=%d", starts, completes)
	}
}
