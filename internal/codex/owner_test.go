package codex

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/proc"
	"github.com/slam0504/sdlc-workbench/internal/recorder"
	"github.com/slam0504/sdlc-workbench/internal/wirelog"
)

func newTestGeneration(t *testing.T) *wirelog.Generation {
	t.Helper()
	g, err := wirelog.NewGeneration(t.TempDir(), "wire-gen")
	if err != nil {
		t.Fatalf("NewGeneration: %v", err)
	}
	return g
}

func TestServerDeathAutoFinalizesGeneration(t *testing.T) {
	stub := newStubServer()
	var single Single[*GenerationOwner]
	gen := newTestGeneration(t)
	finalized := make(chan bool, 1) // wasActive
	if err := RunOwnedHandshake(context.Background(), &single,
		func() (*wirelog.Generation, error) { return gen, nil },
		func() (probeTarget, error) { return stub, nil }, ClientInfo{},
		func(_ error, wasActive bool) { finalized <- wasActive }); err != nil {
		t.Fatal(err)
	}
	stub.die() // 只做這件事——不得由測試呼叫 FinalizeWith
	select {
	case wasActive := <-finalized:
		if !wasActive {
			t.Fatal("死亡時仍是 active 持有者，wasActive 應為 true")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Done() 關閉後必須由 production reaper 自動 finalize（§3.4.2）")
	}
	if !gen.Finalized() {
		t.Fatal("generation 未 finalize，錄流 meta 會漏")
	}
	if m := gen.FinalMeta(); m.ExitCode == nil || *m.ExitCode != 9 {
		t.Fatalf("死亡收尾必須帶 exit 證據：%+v", m)
	}
	if _, ok := single.Take(); ok {
		t.Fatal("死亡的 owner 必須已從 Single 移除")
	}
}

func TestReplacementFinalizesOldGenerationBeforePublishingNew(t *testing.T) {
	old := newStubServer()
	var single Single[*GenerationOwner]
	oldGen := newTestGeneration(t)
	if err := RunOwnedHandshake(context.Background(), &single,
		func() (*wirelog.Generation, error) { return oldGen, nil },
		func() (probeTarget, error) { return old, nil }, ClientInfo{}, nil); err != nil {
		t.Fatal(err)
	}

	var order []string
	newGen := newTestGeneration(t)
	newSrv := newStubServer()
	if err := RunOwnedHandshake(context.Background(), &single,
		func() (*wirelog.Generation, error) {
			if oldGen.Finalized() { // 新 generation 建立前，舊的必須已收尾
				order = append(order, "old_finalized")
			}
			order = append(order, "new_gen_created")
			return newGen, nil
		},
		func() (probeTarget, error) { return newSrv, nil }, ClientInfo{}, nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"old_finalized", "new_gen_created"}) {
		t.Fatalf("replacement 必須等舊 generation finalize 完才建新的並發布：%v", order)
	}
	if _, _, terms, waits := old.callCounts(); terms != 1 || waits != 1 {
		t.Fatalf("被替換的 server 必須 terminate＋wait 各一次：term=%d wait=%d", terms, waits)
	}
	if o, ok := single.Take(); !ok || o.Generation != newGen {
		t.Fatal("ownership 必須是新的 owner")
	}
}

func TestStaleReaperDoesNotClearNewGeneration(t *testing.T) {
	oldSrv := newStubServer()
	var single Single[*GenerationOwner]
	oldGen := newTestGeneration(t)
	staleDone := make(chan bool, 1) // wasActive
	if err := RunOwnedHandshake(context.Background(), &single,
		func() (*wirelog.Generation, error) { return oldGen, nil },
		func() (probeTarget, error) { return oldSrv, nil }, ClientInfo{},
		func(_ error, wasActive bool) { staleDone <- wasActive }); err != nil {
		t.Fatal(err)
	}

	// 先以 replacement 換掉 owner（epoch 遞增），之後才讓舊 server 死亡
	newSrv, newGen := newStubServer(), newTestGeneration(t)
	if err := RunOwnedHandshake(context.Background(), &single,
		func() (*wirelog.Generation, error) { return newGen, nil },
		func() (probeTarget, error) { return newSrv, nil }, ClientInfo{}, nil); err != nil {
		t.Fatal(err)
	}

	oldSrv.die()
	select { // stale 分支同樣會呼叫 onFinalized，測試不會卡住
	case wasActive := <-staleDone:
		if wasActive {
			t.Fatal("已被 replacement 取代，wasActive 應為 false")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stale reaper 也必須呼叫 onFinalized")
	}

	o, ok := single.Take()
	if !ok || o.Generation != newGen {
		t.Fatal("stale reaper 不得清掉新 generation（CompareAndTakeEpoch 必須回 false）")
	}
	if newGen.Finalized() {
		t.Fatal("新 generation 不得被舊 reaper finalize")
	}
}

// epoch 必須在鎖內取得：第一個 owner 發布後、watcher 註冊前停住，
// 讓第二次 replacement 完成，再放行第一個 watcher。
func TestWatcherEpochCapturedUnderLock(t *testing.T) {
	var single Single[*GenerationOwner]
	firstAtBarrier := make(chan struct{})
	releaseFirst := make(chan struct{})
	// 不可用 sync.Once：Once.Do 在第一次 callback 未返回前會**阻塞**第二次呼叫，
	// 於是第二個 replacement 也卡住，而 releaseFirst 又要等它返回才關 → 死鎖。
	var calls atomic.Int32
	hookAfterPublishBeforeWatch = func() {
		if calls.Add(1) == 1 { // 只有第一次停在 barrier，後續呼叫直接通過
			close(firstAtBarrier)
			<-releaseFirst
		}
	}
	t.Cleanup(func() { hookAfterPublishBeforeWatch = nil })

	oldSrv, oldGen := newStubServer(), newTestGeneration(t)
	staleDone := make(chan bool, 1)
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- RunOwnedHandshake(context.Background(), &single,
			func() (*wirelog.Generation, error) { return oldGen, nil },
			func() (probeTarget, error) { return oldSrv, nil }, ClientInfo{},
			func(_ error, wasActive bool) { staleDone <- wasActive })
	}()
	<-firstAtBarrier

	// 第二次 replacement 在第一個 watcher 註冊前就完成發布
	newSrv, newGen := newStubServer(), newTestGeneration(t)
	if err := RunOwnedHandshake(context.Background(), &single,
		func() (*wirelog.Generation, error) { return newGen, nil },
		func() (probeTarget, error) { return newSrv, nil }, ClientInfo{}, nil); err != nil {
		t.Fatal(err)
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}

	oldSrv.die()
	select {
	case wasActive := <-staleDone:
		if wasActive {
			t.Fatal("第一個 watcher 必須帶自己那次發布的 epoch，wasActive 應為 false")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stale watcher 未回報")
	}
	o, ok := single.Take()
	if !ok || o.Generation != newGen {
		t.Fatal("舊 watcher 不得取走新 owner（epoch 必須在鎖內取得）")
	}
	if newGen.Finalized() {
		t.Fatal("新 generation 不得被舊 watcher finalize")
	}
}

// 已被別的路徑（例如 shutdown 的 Take）取走 ownership 之後才死亡：reaper 拿不回
// Single，但那份 generation 仍必須被 finalize，否則錄流 meta 就漏了。
func TestReaperFinalizesEvenWhenOwnershipAlreadyTaken(t *testing.T) {
	stub := newStubServer()
	var single Single[*GenerationOwner]
	gen := newTestGeneration(t)
	done := make(chan bool, 1)
	if err := RunOwnedHandshake(context.Background(), &single,
		func() (*wirelog.Generation, error) { return gen, nil },
		func() (probeTarget, error) { return stub, nil }, ClientInfo{},
		func(_ error, wasActive bool) { done <- wasActive }); err != nil {
		t.Fatal(err)
	}
	if _, ok := single.Take(); !ok { // 模擬 shutdown 先取走、還沒收尾
		t.Fatal("Take 應取得 owner")
	}
	stub.die()
	select {
	case wasActive := <-done:
		if wasActive {
			t.Fatal("ownership 已被取走，wasActive 應為 false")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stale 分支也必須呼叫 onFinalized")
	}
	if !gen.Finalized() {
		t.Fatal("stale 分支仍須 finalize 自己那份 generation")
	}
}

func TestOldProbeEntryPointUnchanged(t *testing.T) {
	// 舊 probe-scoped 入口在 Task 13 之前必須維持原簽名與原行為，
	// 否則 App.codexSingle（Single[*codex.Server]）與 B1 呼叫點會編譯失敗
	stub := newStubServer()
	var single Single[*stubServer]
	newRec, _ := probeRecFactory(t)
	if err := RunHandshakeProbe(context.Background(), &single, newRec,
		func() (*stubServer, error) { return stub, nil }, ClientInfo{}); err != nil {
		t.Fatal(err)
	}
	if _, stops, _, _ := stub.callCounts(); stops != 1 {
		t.Fatalf("舊入口必須維持 probe-scoped（成功時 StopRecording）：%d", stops)
	}
}

func TestThreeStageFailuresDisposeAndKeepEvidence(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(*stubServer)
	}{
		{"start", func(s *stubServer) {}},
		{"attach", func(s *stubServer) { s.beginErr = errors.New("attach failed") }},
		{"handshake", func(s *stubServer) { s.hsErr = errors.New("handshake refused") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			stub := newStubServer()
			stub.exit = proc.Exit{Code: 3, StderrTail: "boom"}
			c.setup(stub)
			startErr := errors.New("spawn failed")
			var single Single[*GenerationOwner]
			gen := newTestGeneration(t)
			err := RunOwnedHandshake(context.Background(), &single,
				func() (*wirelog.Generation, error) { return gen, nil },
				func() (probeTarget, error) {
					if c.name == "start" {
						return nil, startErr
					}
					return stub, nil
				}, ClientInfo{}, nil)
			if err == nil {
				t.Fatal("失敗階段必須回錯")
			}
			if gen.ID() == "" || !gen.Finalized() {
				t.Fatal("失敗的 generation 仍須保留 wire_log_id 並 finalize")
			}
			m := gen.FinalMeta()
			if c.name == "start" {
				if !errors.Is(err, startErr) {
					t.Fatalf("必須回傳原始 start 錯誤：%v", err)
				}
				if m.ExitCode != nil {
					t.Fatalf("尚無 server 時不得捏造 exit_code：%+v", m)
				}
				if !strings.Contains(m.FinalizeCause, "spawn failed") {
					t.Fatalf("必須保留失敗原因：%+v", m)
				}
			} else {
				if m.ExitCode == nil || *m.ExitCode != 3 || m.StderrTail != "boom" {
					t.Fatalf("必須保留收尾證據：%+v", m)
				}
				if !strings.Contains(m.FinalizeCause, "failed") && !strings.Contains(m.FinalizeCause, "refused") {
					t.Fatalf("收尾原因必須寫進 finalize_cause：%+v", m)
				}
				if _, _, terms, waits := stub.callCounts(); terms != 1 || waits != 1 {
					t.Fatalf("失敗階段必須 terminate＋wait：term=%d wait=%d", terms, waits)
				}
			}
			if _, ok := single.Take(); ok {
				t.Fatal("不得留下未發布 server")
			}
		})
	}
}

func TestHandoffKeepsRecorderOpenAndIDBeforeAttach(t *testing.T) {
	stub := newStubServer()
	var order []string
	stub.onBegin = func() { order = append(order, "attach") }
	var single Single[*GenerationOwner]
	gen := newTestGeneration(t)
	if err := RunOwnedHandshake(context.Background(), &single,
		func() (*wirelog.Generation, error) { order = append(order, "gen_id"); return gen, nil },
		func() (probeTarget, error) { order = append(order, "start"); return stub, nil },
		ClientInfo{}, nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"gen_id", "start", "attach"}) {
		t.Fatalf("wire_log_id 必須在掛 recorder 前配置：%v", order)
	}
	if _, stops, terms, _ := stub.callCounts(); stops != 0 || terms != 0 {
		t.Fatalf("handoff 模式不得 Stop／dispose：stop=%d term=%d", stops, terms)
	}
	if gen.Finalized() {
		t.Fatal("handoff 模式不得 Finalize（§3.4.7）")
	}
}

func TestFinalizeWithIsIdempotent(t *testing.T) { // 死亡 reaper／受控 restart／shutdown 多路徑
	stub := newStubServer()
	o := &GenerationOwner{Server: stub, Generation: newTestGeneration(t), attached: true}
	first := o.FinalizeWith(errServerDied)
	if !o.Finalized() {
		t.Fatal("FinalizeWith 後 Finalized 必須為 true")
	}
	if second := o.FinalizeWith(nil); !errors.Is(second, first) && second != first {
		t.Fatalf("重複呼叫必須回傳第一次的結果：%v vs %v", second, first)
	}
	if _, stops, terms, waits := stub.callCounts(); stops != 1 || terms != 1 || waits != 1 {
		t.Fatalf("重複呼叫不得重跑收尾：stop=%d term=%d wait=%d", stops, terms, waits)
	}
}

// §3.4.2 順序：terminate → wait →（stdout 汲取完成）→ stop → finalize。
// detach 一旦排在 terminate 之前，關閉期間的 frame 會被 Conn.record 靜默丟棄
// （sink == nil 直接 return，無錯誤、無計數），錄流檔尾就此被截。
func TestFinalizeWithDetachesOnlyAfterServerExit(t *testing.T) {
	stub := newStubServer()
	gen := newTestGeneration(t)
	var finalizedAtStop bool
	stub.onStop = func() { finalizedAtStop = gen.Finalized() }
	o := &GenerationOwner{Server: stub, Generation: gen, attached: true}
	if err := o.FinalizeWith(errServerDied); err != nil {
		t.Fatal(err)
	}
	if got := stub.callOrder(); !reflect.DeepEqual(got, []string{"terminate", "wait", "stop"}) {
		t.Fatalf("收尾順序必須是 terminate → wait → stop（§3.4.2）：%v", got)
	}
	if finalizedAtStop {
		t.Fatal("StopRecording 必須早於 Generation.Finalize，否則收尾期間的寫入會撞上已關檔")
	}
	if !gen.Finalized() {
		t.Fatal("收尾必須以 Finalize 結束")
	}
}

// stdout 汲取完成的訊號是 Conn.Done（readLoop 讀到 EOF），不是 proc 的 Done
// （後者在 cmd.Wait／stderr 收完就關，與 stdout 汲取無順序依賴）。
func TestFinalizeWithWaitsForStdoutDrainBeforeDetach(t *testing.T) {
	pr, pw := io.Pipe()
	stub := newStubServer()
	stub.conn = NewConn(io.Discard, pr)
	o := &GenerationOwner{Server: stub, Generation: newTestGeneration(t), attached: true}

	done := make(chan error, 1)
	go func() { done <- o.FinalizeWith(errServerDied) }()

	select { // stdout 還沒 EOF：不得 detach
	case <-done:
		t.Fatal("stdout 尚未汲取完成就收尾，關閉期間的 frame 會漏錄")
	case <-time.After(100 * time.Millisecond):
	}
	if _, stops, _, _ := stub.callCounts(); stops != 0 {
		t.Fatal("EOF 之前不得 StopRecording")
	}

	_ = pw.Close() // readLoop 讀到 EOF → Conn.Done 關閉
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("EOF 之後收尾必須完成")
	}
	if got := stub.callOrder(); !reflect.DeepEqual(got, []string{"terminate", "wait", "stop"}) {
		t.Fatalf("順序錯誤：%v", got)
	}
}

// 孫程序 setsid 脫離 process group 時 EOF 可能永遠不來——收尾必須有界，
// 且逾時要 fail loud（回錯＋寫進 finalize_cause），不得假裝錄流完整。
func TestFinalizeWithBoundedDrainTimeout(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	prev := wireDrainTimeout
	wireDrainTimeout = 50 * time.Millisecond
	t.Cleanup(func() { wireDrainTimeout = prev })

	stub := newStubServer()
	stub.conn = NewConn(io.Discard, pr)
	gen := newTestGeneration(t)
	o := &GenerationOwner{Server: stub, Generation: gen, attached: true}

	done := make(chan error, 1)
	go func() { done <- o.FinalizeWith(errServerDied) }()
	select {
	case err := <-done:
		if !errors.Is(err, errWireDrainTimeout) {
			t.Fatalf("逾時必須 fail loud：%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("drain 必須有界，不得無限期等 EOF")
	}
	if m := gen.FinalMeta(); !strings.Contains(m.FinalizeCause, "drain") {
		t.Fatalf("逾時證據必須留在 meta：%+v", m)
	}
}

// BeginRecording 失敗可能是因為「已有別人在錄流」——此時收尾不得 detach 別人的 sink。
func TestFinalizeWithoutOwnAttachDoesNotStopOthersRecording(t *testing.T) {
	stub := newStubServer()
	stub.beginErr = errors.New("recording already in progress")
	var single Single[*GenerationOwner]
	if err := RunOwnedHandshake(context.Background(), &single,
		func() (*wirelog.Generation, error) { return newTestGeneration(t), nil },
		func() (probeTarget, error) { return stub, nil }, ClientInfo{}, nil); err == nil {
		t.Fatal("attach 失敗必須回錯")
	}
	if _, stops, terms, _ := stub.callCounts(); stops != 0 || terms != 1 {
		t.Fatalf("未成功 attach 就不得 StopRecording：stop=%d term=%d", stops, terms)
	}
}

func TestOwnerWithoutServerDoneIsClosed(t *testing.T) { // start 失敗的暫時 owner
	o := &GenerationOwner{Generation: newTestGeneration(t)}
	select {
	case <-o.Done():
	case <-time.After(time.Second):
		t.Fatal("無 server 的 owner，Done 必須視為已結束（且不得 panic）")
	}
}

func TestGenerationSinkRecordsConnEnvelope(t *testing.T) { // stub 的 sink 走的就是 Conn.record 形狀
	gen := newTestGeneration(t)
	stub := newStubServer()
	var single Single[*GenerationOwner]
	if err := RunOwnedHandshake(context.Background(), &single,
		func() (*wirelog.Generation, error) { return gen, nil },
		func() (probeTarget, error) { return stub, nil }, ClientInfo{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := gen.Err(); err != nil {
		t.Fatalf("envelope 必須能寫進 generation：%v", err)
	}
	got := gen.FrameIndex().Lookup(wirelog.FrameKey{
		WireLogID: gen.ID(), Direction: wirelog.DirClientToServer, RequestID: "1"})
	if len(got) != 1 {
		t.Fatalf("dir／raw 必須正確拆封後索引：%v", got)
	}
	if err := gen.Finalize(recorder.Meta{}); err != nil {
		t.Fatal(err)
	}
}

func TestSingleEpochCompareAndTake(t *testing.T) {
	var s Single[*stubAlive]
	a := newStubAlive(false)
	if _, err := s.Ensure(func() (*stubAlive, error) { return a, nil }); err != nil {
		t.Fatal(err)
	}
	e1 := s.Epoch()
	if e1 == 0 {
		t.Fatal("成功發布後 epoch 不得為 0")
	}
	if _, ok := s.CompareAndTakeEpoch(e1 + 1); ok {
		t.Fatal("epoch 不符不得取出")
	}
	b := newStubAlive(false)
	e2, err := s.WithExclusiveEpoch(func(cur *stubAlive, ok bool) (*stubAlive, bool, error) {
		return b, true, nil
	})
	if err != nil || e2 == e1 {
		t.Fatalf("replacement 必須取得新 epoch：e1=%d e2=%d err=%v", e1, e2, err)
	}
	if _, ok := s.CompareAndTakeEpoch(e1); ok {
		t.Fatal("舊 epoch 不得取走新持有者")
	}
	if _, ok := s.CompareAndTakeEpoch(0); ok {
		t.Fatal("epoch 0 一律不得取出")
	}
	if v, ok := s.CompareAndTakeEpoch(e2); !ok || v != b {
		t.Fatal("epoch 相符必須取出目前持有者")
	}
	if _, ok := s.CompareAndTakeEpoch(e2); ok {
		t.Fatal("已取走後不得再取出")
	}
	if _, err := s.WithExclusiveEpoch(func(cur *stubAlive, ok bool) (*stubAlive, bool, error) {
		return nil, false, nil
	}); err != nil {
		t.Fatal(err)
	}
	if e := s.Epoch(); e != 0 {
		t.Fatalf("keep=false 後無持有者，Epoch 必須為 0：%d", e)
	}
}
