package codex

import "sync"

// Alive 是 Single 管理對象的最小介面（*Server 滿足；測試用 stub）。
type Alive interface{ Done() <-chan struct{} }

// Single 序列化「單一長駐 instance」的取得與重建（v1.8）。
//
// epoch 是單調遞增的發布序號（M3b §3.4.2）：每次成功發布（Ensure 建立、
// WithExclusive／WithExclusiveEpoch 回 keep=true）遞增一次，用來辨識「現在持有的
// 還是不是當時那一個 instance」。刻意不用值比較——T 沒有 comparable 約束（加上去
// 會牽動既有的 Single[*Server]／Single[*stubAlive] 實例化），泛型值不能直接用 ==。
type Single[T Alive] struct {
	mu    sync.Mutex
	cur   T
	ok    bool
	epoch uint64
}

// Ensure：既有 instance 存在且未死（Done 未關閉）→ 直接回傳；否則呼叫 start 重建。
// start 失敗 → 不保留任何 instance；start 內部必須自行清理其失敗的中間產物。
func (s *Single[T]) Ensure(start func() (T, error)) (T, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ok {
		select {
		case <-s.cur.Done(): // 已死：重建
		default:
			return s.cur, nil
		}
	}
	t, err := start()
	if err != nil {
		var zero T
		s.cur, s.ok = zero, false
		return zero, err
	}
	s.cur, s.ok, s.epoch = t, true, s.epoch+1
	return t, nil
}

// Take 取出並清空 ownership（僅供 app 關閉：取出後立即 Terminate+Wait，無後續回填）；
// 無 instance 時 ok=false。
func (s *Single[T]) Take() (T, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.cur, s.ok
	var zero T
	s.cur, s.ok = zero, false
	return t, ok
}

// WithExclusive 在同一把 mutex 下執行整段 replacement（v1.9，第九輪 P0）：
// fn 收到目前 instance（可能為空），負責 dispose 舊 instance 與建立新 instance；
// 回傳 (新 instance, keep, err)——keep=true 則回填（err 非 nil 亦保留，供「成功但
// stop/close 有錯」情境）、keep=false 則 ownership 留空。fn 執行期間 Ensure／Take
// 一律阻塞，因此不存在「probe 空窗內另建 server」的競態；fn 內不得呼叫 Single
// 的其他方法（同鎖，會死鎖）。
func (s *Single[T]) WithExclusive(fn func(cur T, ok bool) (T, bool, error)) error {
	_, err := s.WithExclusiveEpoch(fn)
	return err
}

// WithExclusiveEpoch 與 WithExclusive 同語意，但**在同一把鎖內**回傳本次發布取得
// 的 epoch（keep=false 時回 0）。
//
// 需要 epoch 的呼叫端一律用它，不得解鎖後再呼叫 Epoch()——那有 TOCTOU：期間若已有
// 第二次 replacement 發布，第一個 watcher 會拿到第二個 owner 的 epoch，之後在舊
// instance 死亡時錯誤取走新的那一個。
func (s *Single[T]) WithExclusiveEpoch(fn func(cur T, ok bool) (T, bool, error)) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, keep, err := fn(s.cur, s.ok)
	if !keep {
		var zero T
		s.cur, s.ok = zero, false
		return 0, err
	}
	s.cur, s.ok, s.epoch = t, true, s.epoch+1
	return s.epoch, err
}

// Epoch 回傳目前持有者的 epoch；無持有者回 0。
// 僅供觀測用；要辨識「自己是不是仍為持有者」請用發布當下取得的 epoch 搭配
// CompareAndTakeEpoch，不要事後再呼叫本方法（見 WithExclusiveEpoch 的 TOCTOU）。
func (s *Single[T]) Epoch() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ok {
		return 0
	}
	return s.epoch
}

// CompareAndTakeEpoch 僅當目前持有者正是 epoch e 那一次發布的 instance 時才取出並
// 清空 ownership；已被後續 replacement 取代（或已被取走）時回 ok=false 且不動狀態。
// e 為 0（未成功發布）一律回 false。
func (s *Single[T]) CompareAndTakeEpoch(e uint64) (T, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var zero T
	if e == 0 || !s.ok || s.epoch != e {
		return zero, false
	}
	t := s.cur
	s.cur, s.ok = zero, false
	return t, true
}
