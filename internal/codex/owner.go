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

	mu   sync.Mutex
	done bool
	err  error
}

// Done 讓 GenerationOwner 滿足 Alive（Single 的型別約束）。
// 只有發布成功的 owner 才會被 WatchGeneration 監看，故此時 Server 必非 nil；
// start 失敗時建立的暫時 owner（Server == nil）只會走 FinalizeWith，不會被監看。
func (o *GenerationOwner) Done() <-chan struct{} { return o.Server.Done() }

// FinalizeWith 收尾本 owner：detach 錄流 sink（StopRecording 會等 in-flight
// callback 完成，避免寫入已 finalize 的 generation）→ Terminate → Wait →
// 以 Exit 證據 finalize generation。stage 是觸發收尾的原因（死亡＝errServerDied、
// 受控 replacement＝nil、三階段失敗＝該階段的錯誤），會併入 meta 的 stderr_tail
// 一併保留——失敗的 generation 同樣要留下 wire_log_id 與收尾證據，不因未發布而丟棄。
//
// 冪等：死亡 reaper、受控 restart 與 shutdown 總序都會呼叫，重複呼叫只回傳第一次
// 的結果，不重複 Terminate／Wait／寫 meta。
//
// Server 為 nil（start 階段就失敗、還沒有 server）時跳過 stop／terminate／wait，
// meta 不帶 exit_code——比照 RunHandshakeProbe 的 start 失敗分支。
func (o *GenerationOwner) FinalizeWith(stage error) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.done {
		return o.err
	}
	o.done = true

	meta := recorder.Meta{Provider: "codex", RecordedAt: time.Now().UTC().Format(time.RFC3339)}
	var stopErr error
	if o.Server != nil {
		stopErr = o.Server.StopRecording()
		_ = o.Server.Terminate()
		ex := o.Server.Wait()
		meta.Argv = o.Server.Argv()
		meta.ExitCode = &ex.Code
		meta.StderrTail = ex.StderrTail
	}
	if stage != nil {
		meta.StderrTail = appendCause(meta.StderrTail, stage)
	}

	var finErr error
	if o.Generation != nil {
		finErr = o.Generation.Finalize(meta)
	}
	o.err = errors.Join(stopErr, finErr)
	return o.err
}

// Finalized 回報本 owner 是否已收尾過一次。
func (o *GenerationOwner) Finalized() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.done
}

// appendCause 把收尾原因併進 stderr_tail。recorder.Meta 沒有獨立的「收尾原因」欄
// 位，而 recorder_error 由 wirelog.Finalize 保留給 latch 住的寫入錯誤（會被覆寫），
// 不能借用——否則兩種不同的失敗會互相蓋掉。
func appendCause(tail string, cause error) string {
	line := "wire log finalize cause: " + cause.Error()
	if tail == "" {
		return line
	}
	return tail + "\n" + line
}

// WatchGeneration 是 production 的死亡收尾 reaper（§3.4.2）：owner 發布後立即啟動，
// Done() 關閉即以「發布當下取得的 epoch」把自己從 Single 取回並 finalize。
//
// epoch 不符（已被 replacement 取代）時**不動 Single**，但仍 finalize 自己的
// generation——否則舊那份錄流的 meta 會漏；且兩種分支都會呼叫 onFinalized
// （wasActive 標示死亡當下是否仍是持有者），呼叫端才不會在 stale 分支空等。
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
