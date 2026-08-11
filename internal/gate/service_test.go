package gate

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeEmitter records emitted gate events; safe for concurrent use.
type fakeEmitter struct {
	mu     sync.Mutex
	events []fakeEvent
}

type fakeEvent struct {
	kind     string
	bindings []Binding
	payload  any
}

func (f *fakeEmitter) EmitGateEvent(kind string, bindings []Binding, payload any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, fakeEvent{kind: kind, bindings: bindings, payload: payload})
}

// opsForTest exposes the service's journal ops for test-only projection.
func (s *Service) opsForTest() []GateOp { return s.j.Ops() }

// counterULID returns a monotonically increasing, unique-per-call id
// ("u1", "u2", ...); safe under concurrent use.
func counterULID() func() string {
	var n int64
	return func() string {
		return fmt.Sprintf("u%d", atomic.AddInt64(&n, 1))
	}
}

func newTestServiceWithCurrent(t *testing.T, currentFn ManifestFn) (*Service, *fakeEmitter) {
	t.Helper()
	j, err := OpenJournal(filepath.Join(t.TempDir(), "gate.jsonl"))
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	em := &fakeEmitter{}
	now := func() string { return "t" }
	s := NewService(j, currentFn, counterULID(), now, em)
	return s, em
}

func newTestService(t *testing.T) (*Service, *fakeEmitter) {
	t.Helper()
	return newTestServiceWithCurrent(t, func() (string, error) { return "sha256:" + hex64(), nil })
}

func mustProjectOps(t *testing.T, s *Service) []GateEntry {
	t.Helper()
	es, err := Project(s.opsForTest())
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	return es
}

// hex64b returns a 64-hex string that differs from hex64().
func hex64b() string { return strings.Repeat("b", 64) }

// gate1BWith returns valid gate1 bindings with spec_manifest digest m and a
// fixed valid base_commit.
func gate1BWith(m string) []Binding {
	return gate1B(m, "git:sha1:"+hex40())
}

func TestDecideOnlyOnceUnderConcurrency(t *testing.T) {
	s, _ := newTestService(t)
	id, _ := s.Submit("sha256:"+hex64(), "git:sha1:"+hex40(), gate1B("sha256:"+hex64(), "git:sha1:"+hex40()))
	var ok int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Decide(id, "approved", "", Approver{ID: "u", Method: "app-local"},
				gate1B("sha256:"+hex64(), "git:sha1:"+hex40())); err == nil {
				atomic.AddInt32(&ok, 1)
			}
		}()
	}
	wg.Wait()
	if ok != 1 {
		t.Fatalf("exactly one Decide must succeed, got %d", ok)
	}
}

func TestReconcileAppendsSingleStaleThenNoRevive(t *testing.T) {
	digest := "sha256:" + hex64()
	changed := "sha256:" + hex64b()
	cur := digest
	s, _ := newTestServiceWithCurrent(t, func() (string, error) { return cur, nil })
	id, _ := s.Submit(digest, "git:sha1:"+hex40(), gate1BWith(digest))
	_ = s.Decide(id, "approved", "", Approver{ID: "u", Method: "app-local"}, gate1BWith(digest))
	cur = changed // spec changed
	if err := s.ReconcileGate1(); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileGate1(); err != nil { // concurrent/re-entrant call must still yield only one transition
		t.Fatal(err)
	}
	e := entryByID(mustProjectOps(t, s), id)
	if e.State != Stale {
		t.Fatalf("want stale, got %s", e.State)
	}
	cur = digest // reverting must not revive
	_ = s.ReconcileGate1()
	if entryByID(mustProjectOps(t, s), id).State != Stale {
		t.Fatal("stale must not revive")
	}
}

// TestReconcileSingleStaleUnderConcurrency is the spec §7 barrier: a watcher
// (ReconcileGate1) and GateList (List, which also reconciles) racing against
// each other must produce exactly ONE stale transition for the affected
// approval id — never a duplicate. Unlike
// TestReconcileAppendsSingleStaleThenNoRevive (which calls ReconcileGate1
// sequentially and only checks the projected terminal state), this test
// fires 8 goroutines concurrently and counts raw "stale" transition records
// in the journal, which would catch a bug that appends a redundant stale
// transition under a genuine race.
func TestReconcileSingleStaleUnderConcurrency(t *testing.T) {
	digest := "sha256:" + hex64()
	changed := "sha256:" + hex64b()
	var curMu sync.Mutex
	cur := digest
	s, _ := newTestServiceWithCurrent(t, func() (string, error) {
		curMu.Lock()
		defer curMu.Unlock()
		return cur, nil
	})
	id, _ := s.Submit(digest, "git:sha1:"+hex40(), gate1BWith(digest))
	if err := s.Decide(id, "approved", "", Approver{ID: "u", Method: "app-local"}, gate1BWith(digest)); err != nil {
		t.Fatal(err)
	}

	curMu.Lock()
	cur = changed // spec changed, before any concurrent reconcile starts
	curMu.Unlock()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%3 == 0 {
				if _, err := s.List(); err != nil {
					t.Error(err)
				}
			} else if err := s.ReconcileGate1(); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()

	staleCount := 0
	for _, op := range s.opsForTest() {
		for _, raw := range op.Records {
			var probe struct {
				Type       string `json:"_type"`
				ApprovalID string `json:"approval_id"`
				To         string `json:"to"`
			}
			if err := json.Unmarshal(raw, &probe); err != nil {
				t.Fatalf("unmarshal record: %v", err)
			}
			if probe.Type == "transition" && probe.ApprovalID == id && probe.To == "stale" {
				staleCount++
			}
		}
	}
	if staleCount != 1 {
		t.Fatalf("want exactly 1 stale transition under concurrency, got %d", staleCount)
	}
}

func TestRejectNeedsReason(t *testing.T) {
	s, _ := newTestService(t)
	id, _ := s.Submit("sha256:"+hex64(), "git:sha1:"+hex40(), gate1BWith("sha256:"+hex64()))
	if err := s.Decide(id, "rejected", "", Approver{ID: "u", Method: "app-local"}, nil); err == nil {
		t.Fatal("rejected must require reason")
	}
}
