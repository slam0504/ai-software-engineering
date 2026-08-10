// Package ports 定義 provider 中立的應用層契約（go-ddd 對齊：consumer——appcore——
// 依賴此契約，不 import provider adapter；本 package 只依賴 contract，不依賴
// proc 等 infrastructure）。
package ports

import "github.com/slam0504/sdlc-workbench/internal/contract"

// Exit 是 ports 自有的中立收尾值（不引用 infrastructure 型別）。
//
// # Exit 語意（凍結）
//
// Exited=false 表示「未取得 exit」（例如 pump 卡死、supervisor 未回收，或 codex
// 長駐 server 尚在執行）——此時 Code 無意義，稽核端（recorder meta 的 ExitCode）
// 必須維持 nil，不得把未知偽裝成 exit 0。
type Exit struct {
	Exited     bool // true = Code 有效（process 已回收）
	Code       int
	StderrTail string
}

// Turns 是多輪 provider 抽象。
//
// # Turns 語意（凍結）
//
// Send 只在前一輪 result 事件已出現後合法；生命週期實作各自定義（同 process
// 多輪或 resume-per-turn），對呼叫端不可見。Close 冪等、代表「不再有輸入」，
// 之後 Send 必回錯誤；Terminate 強殺（整組）。Wait 回傳快取的 Exit，任意時點
// 可呼叫；Events channel 於 provider 收尾後關閉。argv／錄流等診斷資訊不屬
// turn 行為，拆為選配的 Diagnostics capability。
type Turns interface {
	Events() <-chan contract.Event
	Send(prompt string) error
	Close() error     // 結束輸入（自然收尾）
	Terminate() error // 強殺
	Wait() Exit
}

// Diagnostics 為選配診斷能力（稽核／recorder meta 用）；需要時以型別斷言取得。
type Diagnostics interface {
	Argv() []string
}
