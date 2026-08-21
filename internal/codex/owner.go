package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/proc"
	"github.com/slam0504/sdlc-workbench/internal/recorder"
	"github.com/slam0504/sdlc-workbench/internal/wirelog"
)

// ProbeTarget 是一個 app-server generation 對外的最小介面（*Server 以薄委派滿足；
// 測試用 stub）。GenerationOwner 只持有它、不直接持有 *Server，呼叫端（App 的
// 受控 restart／錄流復原路徑）因此可以注入 fake server 走同一段 production 編排。
//
// 匯出的理由：`RunOwnedHandshake` 的 start 參數型別是 `func() (ProbeTarget, error)`，
// package 外的呼叫端必須能寫出這個 closure 型別（Go 沒有 function 型別的協變）。
type ProbeTarget interface {
	Alive
	// Conn 是 GenerationOwner 的呼叫端（interrupt／fake-wire 路徑）取得底層連線的
	// 入口——owner 只持有 ProbeTarget，不再直接持有 *Server。
	Conn() *Conn
	BeginRecording(sink func([]byte) error) error
	StopRecording() error
	Handshake(ctx context.Context, ci ClientInfo) error
	Terminate() error
	Wait() proc.Exit
	StderrSnapshot() string
	Argv() []string
}

// errServerDied 是死亡 reaper 收尾 generation 時記錄的 stage 原因（§3.4.2）。
var errServerDied = errors.New("codex: app-server exited")

// errWireDrainTimeout：等 stdout 汲取完成逾時。孫程序若以 setsid 脫離 process
// group，stdout 的寫入端可能永遠不關閉、EOF 不會來，收尾不能因此無限期卡住；
// 逾時代表錄流檔尾可能不完整，故 fail loud（回錯＋寫進 meta 的 finalize_cause）。
var errWireDrainTimeout = errors.New("codex: wire log drain timed out before stdout EOF")

// wireDrainTimeout 是上述等待的上界（測試可覆寫）。
var wireDrainTimeout = 5 * time.Second

// closedCh 供無 Server 的暫時 owner 當作「已結束」的 Done()。
var closedCh = func() chan struct{} { c := make(chan struct{}); close(c); return c }()

// hookAfterPublishBeforeWatch 是「owner 已發布、watcher 尚未註冊」之間的測試
// barrier；production 恆為 nil（唯一設值入口是 owner_test.go）。
var hookAfterPublishBeforeWatch func()

// GenerationOwner 把「app-server instance」與「該 generation 的 always-on 錄流」
// 綁成單一 ownership 單位（§3.4.2）。
//
// 為什麼要綁：錄流的 Generation 若只留在呼叫端 closure 裡，server 意外死亡時沒有
// 任何路徑拿得到它去 finalize——錄流 meta 就此漏掉；而 Single.Ensure 遇到已關閉的
// Done() 會直接建立並覆寫新 instance，舊 generation 連被察覺的機會都沒有。綁進
// Single 持有的值之後，reaper／受控 restart／shutdown 總序三條路徑都拿得到它。
//
// **一律用 NewGenerationOwner 建構，不要直接寫 struct literal**：未匯出的 attached
// 記錄「錄流 sink 是不是本 owner 自己掛上的」，直接建構會讓它恆為 false，收尾時就
// **靜默**跳過 detach（不回錯、不計數）——generation 關檔後仍掛著的 sink 會把後續
// frame 寫進已關閉的檔案，錯誤只 latch 進 writeErr，而 meta 早就寫完了。
type GenerationOwner struct {
	Server     ProbeTarget
	Generation *wirelog.Generation

	mu       sync.Mutex
	attached bool // 本 owner 自己成功掛上錄流 sink，收尾時才有資格 detach
	done     bool
	err      error
}

// NewGenerationOwner 建立 owner 並**同時**把 gen 掛成 srv 的錄流 sink——attach 這件
// 事只有這一條路徑能做，attached 因此不可能與現實不符。
//
// 回傳的 owner **一律非 nil**，即使 attach 失敗也要拿它收尾（Terminate → Wait →
// finalize）；attach 失敗時 attached 維持 false，收尾不會 detach 別人的 sink——
// BeginRecording 可能正是因為「已有別人在錄流」而失敗的。
func NewGenerationOwner(srv ProbeTarget, gen *wirelog.Generation) (*GenerationOwner, error) {
	o := &GenerationOwner{Server: srv, Generation: gen}
	if err := srv.BeginRecording(generationSink(gen)); err != nil {
		return o, err
	}
	o.attached = true
	return o, nil
}

// newUnstartedOwner 是 start 階段就失敗（還沒有 server）時的收尾載體：只帶
// generation，收尾時跳過 terminate／wait／drain／detach，但 wire_log_id 與失敗
// 原因照樣落進 meta。
func newUnstartedOwner(gen *wirelog.Generation) *GenerationOwner {
	return &GenerationOwner{Generation: gen}
}

// Done 讓 GenerationOwner 滿足 Alive（Single 的型別約束）。
// Server 為 nil（start 階段就失敗的暫時 owner）時回傳已關閉的 channel——「沒有
// server」等同「已結束」，不 panic 也不讓呼叫端永久阻塞。
func (o *GenerationOwner) Done() <-chan struct{} {
	if o.Server == nil {
		return closedCh
	}
	return o.Server.Done()
}

// FinalizeWith 收尾本 owner，順序凍結為
// **Terminate → Wait → stdout 汲取完成 → StopRecording → Generation.Finalize**
// （§3.4.2）。前後段沿用 repo 既有慣例——cmd/probe-codex-parallel/main.go:220-223
// 是 Terminate → Wait → StopRecording；本函式在 Wait 與 Stop **之間多一步**等
// Conn.Done（stdout 汲取完成），那個 probe driver 沒有這一步。差別在於 probe 是
// 一次性程序、Stop 之後隨即退出，長駐 server 的受控 replacement 與死亡 reaper 則
// 會續用同一條連線，尾端 frame 漏掉就永遠補不回來。
//
// 為什麼 detach 必須排在 server 死透之後：`Conn.record` 看到 sink == nil 會直接
// return——無錯誤、無計數——所以任何提前 detach 都是**靜默漏錄**。提前 detach 會吃
// 掉三段 frame：(a) SIGTERM 到真正退出之間的 in-flight response／shutdown
// notification／error frame；(b) 已進 OS pipe 但 readLoop 的 scanner 還沒掃到的
// 行；(c) 死亡 reaper 場景最嚴重——`Server.Done()` 是 proc 的 doneCh（cmd.Wait ＋
// stderr 收完就關），與 stdout 汲取「完成」沒有順序依賴，reaper 醒來時 stdout 幾乎
// 必然還有殘餘。`Conn.Done()`（readLoop 讀到 EOF 才關，rpc.go:315）才是汲取完成的
// 真正訊號，故等它、不等 proc 的 Done。
//
// 為什麼 detach 仍必須排在 Finalize 之前：`Generation.Line` 沒有 finalized
// short-circuit，Finalize 之後才拿到鎖的 Line 會寫進已 Close 的 file，錯誤只會
// latch 進 writeErr，而 meta 早就寫完——那個錯誤永遠進不了 meta。StopRecording 會
// 等 in-flight callback 跑完，正好關掉這個窗口。
//
// stage 是觸發收尾的原因（死亡＝errServerDied、受控 replacement＝nil、三階段失敗
// ＝該階段的錯誤），寫進 meta.FinalizeCause；stderr_tail 維持只放子程序 stderr 原文。
// 失敗的 generation 同樣保留 wire_log_id 與收尾證據，不因未發布而丟棄。
//
// 冪等：死亡 reaper、受控 restart 與 shutdown 總序都會呼叫，重複呼叫只回傳第一次
// 的結果，不重複 Terminate／Wait／寫 meta。
//
// Server 為 nil（start 階段就失敗、還沒有 server）時跳過 terminate／wait／drain／
// stop，meta 不帶 exit_code——沒有子行程就沒有 exit 證據，不得捏造。
func (o *GenerationOwner) FinalizeWith(stage error) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.done {
		return o.err
	}
	o.done = true

	meta := recorder.Meta{Provider: "codex", RecordedAt: time.Now().UTC().Format(time.RFC3339)}
	if stage != nil {
		meta.FinalizeCause = stage.Error()
	}
	var stopErr, drainErr error
	if o.Server != nil {
		_ = o.Server.Terminate()
		ex := o.Server.Wait()
		drainErr = o.drainStdout()
		if o.attached { // 只 detach 自己掛上的 sink，不摘別人的
			stopErr = o.Server.StopRecording()
		}
		meta.Argv = o.Server.Argv()
		meta.ExitCode = &ex.Code
		meta.StderrTail = ex.StderrTail
	}
	if drainErr != nil {
		meta.FinalizeCause = appendCause(meta.FinalizeCause, drainErr)
	}

	var finErr error
	if o.Generation != nil {
		finErr = o.Generation.Finalize(meta)
	}
	o.err = errors.Join(drainErr, stopErr, finErr)
	return o.err
}

// drainStdout 等 readLoop 讀到 EOF（stdout 汲取完成），上界 wireDrainTimeout。
// Conn 為 nil（測試 stub／尚未建立連線）時無事可等。
func (o *GenerationOwner) drainStdout() error {
	c := o.Server.Conn()
	if c == nil {
		return nil
	}
	t := time.NewTimer(wireDrainTimeout)
	defer t.Stop()
	select {
	case <-c.Done():
		return nil
	case <-t.C:
		return errWireDrainTimeout
	}
}

// Finalized 回報本 owner 是否已收尾過一次。
func (o *GenerationOwner) Finalized() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.done
}

// appendCause 併接多個收尾原因（stage ＋ drain 逾時）到 finalize_cause。
func appendCause(cause string, extra error) string {
	if cause == "" {
		return extra.Error()
	}
	return cause + "; " + extra.Error()
}

// WatchGeneration 是 production 的死亡收尾 reaper（§3.4.2）：owner 發布後立即啟動，
// Done() 關閉即以「發布當下取得的 epoch」把自己從 Single 取回並 finalize。
//
// epoch 不符（已被 replacement 取代）時**不動 Single**，但仍 finalize 自己的
// generation——否則舊那份錄流的 meta 會漏；且兩種分支都會呼叫 onFinalized
// （wasActive 標示死亡當下是否仍是持有者），呼叫端才不會在 stale 分支空等。
//
// **onFinalized 的契約（呼叫端務必看）**：`wasActive == false` **不代表異常**——每
// 一次受控 replacement 之後都會補發一次：舊 owner 的 watcher 仍掛在舊 process 上，
// 該 process 退出時就會以 wasActive=false 回報。所以只有 `wasActive == true` 才是
// 「現役 server 意外死亡」；把 false 也當死亡處理（發事件、re-latch、觸發重啟）會在
// 每次正常 restart 後誤觸。
func WatchGeneration(s *Single[*GenerationOwner], o *GenerationOwner, epoch uint64,
	onFinalized func(err error, wasActive bool)) {
	go func() {
		<-o.Done()
		_, wasActive := s.CompareAndTakeEpoch(epoch)
		err := o.FinalizeWith(errServerDied)
		if onFinalized != nil {
			onFinalized(err, wasActive)
		}
	}()
}

// RunOwnedHandshake 是 app-server 建立／替換的唯一編排（§3.4.2／§3.4.7）：B1 受控
// restart、錄流受控復原與 ensureAppServer 三條路徑共用它。handoff 語意內建：成功後
// **不** StopRecording、不 finalize generation——錄流持續掛在發布出去的 server 上，
// 直到它死亡或被替換（M3b 之前的 probe-scoped 入口 RunHandshakeProbe 在成功發布前
// 會 Stop＋Close 錄流，與 §3.4.1 的 always-on wire log 不相容，已於 Task 13 刪除）。
//
// 全段在 single.WithExclusiveEpoch 的單一互斥交易內完成：鎖內先收尾舊 owner
// （terminate → wait → finalize_old）→ 建新 generation（wire_log_id 在掛 recorder
// **之前**配置）→ start → attach → handshake → 發布，並由該呼叫直接回傳本次發布的
// epoch。epoch 必須這樣取得，不能解鎖後再呼叫 Epoch()——期間若已有第二次
// replacement 發布，第一個 watcher 會拿到第二個 owner 的 epoch，之後在舊 server
// 死亡時錯誤取走新 generation。
//
// 呼叫端不得在外層再包一次 single.WithExclusive（同一把 mutex，巢狀必死鎖）。
//
// **回傳值契約**：`err != nil` **不代表沒有發布新 server**——新 server handshake 成功
// 但舊 owner 收尾失敗時，ownership 已換成新的，錯誤照樣往上回（沿用
// WithExclusive 的 `keep=true && err != nil` 語意）。呼叫端不得從 err 推論
// 「Single 是空的」，要判斷請看 Single 本身。
//
// **只有經本函式發布的 owner 才有死亡 reaper**；`Single.Ensure` 不回傳 epoch，
// 經它建立的 instance 沒有 watcher（見 Single.Ensure 的註解）。
func RunOwnedHandshake(ctx context.Context, single *Single[*GenerationOwner],
	newGen func() (*wirelog.Generation, error), start func() (ProbeTarget, error),
	ci ClientInfo, onFinalized func(err error, wasActive bool)) error {

	var published *GenerationOwner
	epoch, err := single.WithExclusiveEpoch(func(cur *GenerationOwner, ok bool) (*GenerationOwner, bool, error) {
		var oldErr error
		if ok { // 1. 被替換的 owner：terminate → wait → 舊 generation finalize
			oldErr = cur.FinalizeWith(nil)
		}
		gen, err := newGen() // 2. wire_log_id 前置配置（always-on，早於 attach）
		if err != nil {
			return nil, false, errors.Join(err, oldErr)
		}
		srv, err := start()
		if err != nil { // 3. 尚無 server：generation 仍要 finalize，證據不丟
			fin := newUnstartedOwner(gen).FinalizeWith(err)
			return nil, false, errors.Join(err, fin, oldErr)
		}
		o, err := NewGenerationOwner(srv, gen) // 4. attach；失敗也不留未 handshake 的 server
		if err != nil {
			return nil, false, errors.Join(err, o.FinalizeWith(err), oldErr)
		}
		if err := srv.Handshake(ctx, ci); err != nil { // 5. 同上
			return nil, false, errors.Join(err, o.FinalizeWith(err), oldErr)
		}
		// 6. 成功：handoff——錄流留著，交給 WatchGeneration 與後續 replacement 收尾。
		published = o
		return o, true, oldErr
	})
	if published != nil {
		if hookAfterPublishBeforeWatch != nil {
			hookAfterPublishBeforeWatch()
		}
		WatchGeneration(single, published, epoch, onFinalized)
	}
	return err
}

// generationSink 把 codex.Conn 的錄流 envelope（{"dir":...,"frame":...}，見
// Conn.record）轉成 wirelog.Generation 的 (direction, raw) 介面。
// envelope 由本套件自己產生，形狀不合就是內部錯誤——fail loud，不靜默丟 frame。
func generationSink(gen *wirelog.Generation) func([]byte) error {
	return func(b []byte) error {
		var env struct {
			Dir   string          `json:"dir"`
			Frame json.RawMessage `json:"frame"`
		}
		if err := json.Unmarshal(b, &env); err != nil {
			return fmt.Errorf("codex: malformed wire envelope: %w", err)
		}
		return gen.Line(wirelog.Direction(env.Dir), env.Frame)
	}
}
