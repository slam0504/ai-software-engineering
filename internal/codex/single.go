package codex

import "sync"

// Alive 是 Single 管理對象的最小介面（*Server 滿足；測試用 stub）。
type Alive interface{ Done() <-chan struct{} }

// Single 序列化「單一長駐 instance」的取得與重建（v1.8）。
type Single[T Alive] struct {
	mu  sync.Mutex
	cur T
	ok  bool
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
	s.cur, s.ok = t, true
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
	s.mu.Lock()
	defer s.mu.Unlock()
	t, keep, err := fn(s.cur, s.ok)
	if keep {
		s.cur, s.ok = t, true
	} else {
		var zero T
		s.cur, s.ok = zero, false
	}
	return err
}
