package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/recorder"
	"github.com/slam0504/sdlc-workbench/internal/wirelog"
)

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
type GenerationOwner struct {
	Server     probeTarget
	Generation *wirelog.Generation

	mu       sync.Mutex
	attached bool // 本 owner 自己成功掛上錄流 sink，收尾時才有資格 detach
	done     bool
	err      error
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
// （§3.4.2；與 cmd/probe-codex-parallel/main.go:220-223 的收尾同序）。
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
// stop，meta 不帶 exit_code——比照 RunHandshakeProbe 的 start 失敗分支。
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

// RunOwnedHandshake 是 owner 版的 app-server 建立／替換編排（§3.4.2／§3.4.7）。
// 與 RunHandshakeProbe 的差別是 handoff 語意內建：成功後**不** StopRecording、
// 不 finalize generation——錄流持續掛在發布出去的 server 上，直到它死亡或被替換。
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
	newGen func() (*wirelog.Generation, error), start func() (probeTarget, error),
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
			fin := (&GenerationOwner{Generation: gen}).FinalizeWith(err)
			return nil, false, errors.Join(err, fin, oldErr)
		}
		o := &GenerationOwner{Server: srv, Generation: gen}
		if err := srv.BeginRecording(generationSink(gen)); err != nil { // 4. 不留未 handshake 的 server
			// attached 仍為 false：BeginRecording 可能是因「已有別人在錄流」而失
			// 敗，此時收尾若 StopRecording 會摘掉別人的 sink。
			return nil, false, errors.Join(err, o.FinalizeWith(err), oldErr)
		}
		o.attached = true
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
