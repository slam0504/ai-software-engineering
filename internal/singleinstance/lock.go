// Package singleinstance 保證同一個 workbench state directory 同一時間只有一
// 個 OS process 進入 writer 初始化。
//
// 為什麼需要它（不是 UX 問題）：app 的多份磁碟狀態都以「單一 writer」為前提
//   - appcore.JSONLSink 的 offset 是**本機累加值**：開檔時 Seek 到檔尾取初始
//     值，之後只加自己寫出去的 byte 數。events.jsonl 是 O_APPEND，兩個 process
//     的稽核行本身不會互相截斷，但第二個 process 寫進去的 byte 不會反映到第
//     一個 process 的 offset，於是 AppendReceipt 的 StartOffset／EndOffset 從
//     那一刻起全部偏移，而 §3.5.2 凍結 receipt 是 replay index checkpoint 的
//     ground truth → checkpoint 指向錯誤的 byte 位置。
//   - wsregistry／replayindex checkpoint 的 temp write + rename：兩個 process
//     用同一組 temp 檔名互相覆蓋，rename 的 last-writer-wins 會讓其中一邊的
//     commit 靜默消失。
//
// 為什麼是 flock(2) 而不是「建立一個 lock file」：單靠檔案存在無法區分「有人
// 在跑」與「上一次 SIGKILL／panic 留下的殘骸」，crash 後會留下 stale lock 把
// 使用者永久鎖在門外。flock 的持有者是 kernel 裡的 open file description，
// process 一死（含 SIGKILL、panic、OOM kill）kernel 必然釋放，lock file 留在
// 磁碟上也不影響下一次取得——所以這裡**刻意不刪** lock file（見 Release）。
//
// 平台：unix（darwin／linux）。與 internal/proc 的 process group 收尾一樣，
// 本 repo 目前只在 unix 上建置。
package singleinstance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// LockFileName：state directory 內的鎖檔名。
const LockFileName = "instance.lock"

// ErrAlreadyRunning：鎖被另一個活著的 process 持有。這是唯一一種「請使用者切
// 回既有視窗」的拒絕；其餘失敗（開檔、權限、不支援 flock 的檔案系統）都回別
// 的錯誤，呼叫端一律 fail closed，**不得**當成「目前沒人持鎖」。
var ErrAlreadyRunning = errors.New("singleinstance: 另一個 workbench 實例正持有這個 state directory 的鎖")

// Lock：已持有的 state directory 獨佔鎖。
//
// 三個欄位全部 unexported，而且只有 Acquire 設得起來：別的 package 造得出
// `&singleinstance.Lock{}`，但那個零值的 Held() 為 false、StateDir() 為空字
// 串，過不了任何 ownership 檢查。呼叫端可以據此把 *Lock 當成**不可偽造的
// capability**（見 app.go 的 stateLease）。
type Lock struct {
	f        *os.File
	path     string
	stateDir string
	released bool
}

// Acquire 對 <stateDir>/instance.lock 取得非阻塞獨佔 flock。
//
// 非阻塞是刻意的：第二個實例要立刻收到拒絕並退出，不是排隊等第一個關閉。
//
// 失敗一律不回傳 Lock，也**不做任何補救**（不刪檔、不重試、不降級成「檔案存
// 在檢查」）——呼叫端在這之前不能開任何 writer，在這之後失敗就得原地退出。
func Acquire(stateDir string) (*Lock, error) {
	path := filepath.Join(stateDir, LockFileName)
	// O_CREATE|O_RDWR：不帶 O_TRUNC。截斷會動到磁碟事實，而被拒絕的那一方
	// 必須做到「除了開這個 fd 之外什麼都沒動」。
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("singleinstance: 開啟 %s 失敗：%w", path, err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w（%s）", ErrAlreadyRunning, path)
		}
		return nil, fmt.Errorf("singleinstance: flock(%s) 失敗：%w", path, err)
	}
	return &Lock{f: f, path: path, stateDir: stateDir}, nil
}

// Path：鎖檔的絕對路徑（fail closed 的拒絕訊息用——要讓使用者知道是哪一個鎖
// 檔擋住的，尤其是權限類的失敗）。
func (l *Lock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// StateDir：這把鎖保護的 state directory（＝ Acquire 的引數）。
func (l *Lock) StateDir() string {
	if l == nil {
		return ""
	}
	return l.stateDir
}

// Held：這是不是一把真的、由 Acquire 取得且尚未釋放的鎖。
//
// 存在的理由是 capability 語意：`&Lock{}` 這種偽造值必須過不了檢查。fd 在
// Release 之後仍非 nil（os.File 只是被 Close），所以 Held 另外看 released
// 旗標——「釋放過的 lease 不能拿來授權任何寫入」。
func (l *Lock) Held() bool { return l != nil && l.f != nil && !l.released }

// Release 關閉 fd，kernel 隨之釋放 flock。
//
// **不刪除 lock file**：unlink 之後 flock 仍掛在原本的 inode 上，另一個 process
// 會在新建的同名檔案上取得第二把鎖——兩邊都以為自己是唯一 writer。留著空檔案
// 沒有成本（下次 Acquire 直接重用），也正是 crash 之後磁碟上會看到的狀態。
func (l *Lock) Release() error {
	if l == nil || l.released {
		return nil
	}
	l.released = true
	return l.f.Close()
}
