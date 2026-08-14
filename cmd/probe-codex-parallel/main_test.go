package main

import (
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/codex"
)

// 鎖住 (a) 的自動判準：串行化情境必須回 no、forced 收尾必須回 inconclusive，
// 且結果只由 turn/started 與 turn/completed 的先後決定，不受 thread 級 frame 影響。
//
// 註：舊指標（A/B frame 的 index 區間相交）的恆真性來自 trace 裡混入 thread 級 frame
// （thread/started 等依建立順序必落在最前面）。單看下面的 serialized case 並不足以
// 反證舊指標——該 case 每筆都有 TurnID，舊式在它上面同樣回 false；真正的反例是
// serialized-with-thread-frames case（trace 含 TurnID 為空的 thread 級 frame）。
func TestOverlapVerdict(t *testing.T) {
	const thA, thB = "thread-A", "thread-B"
	f := func(seq int, thread, method string) frameRec {
		return frameRec{Seq: seq, Dir: "s2c", Kind: "notification", Method: method,
			ThreadID: thread, TurnID: "turn-of-" + thread}
	}
	// threadFrame：thread 級 frame（TurnID 空），即舊指標恆真的來源。
	threadFrame := func(seq int, thread, method string) frameRec {
		return frameRec{Seq: seq, Dir: "s2c", Kind: "notification", Method: method, ThreadID: thread}
	}

	cases := []struct {
		name  string
		trace []frameRec
		want  string
	}{
		{
			// 真並行：B 的 turn 在 A 的 turn 生命期內開始並結束
			name: "concurrent",
			trace: []frameRec{
				f(10, thA, codex.MethodTurnStarted),
				f(11, thB, codex.MethodTurnStarted),
				f(12, thB, codex.MethodAgentMessageDelta),
				f(13, thA, codex.MethodAgentMessageDelta),
				f(14, thB, codex.MethodTurnCompleted),
				f(15, thA, codex.MethodTurnCompleted),
			},
			want: "yes",
		},
		{
			// 串行化：A 整輪跑完 server 才受理 B → 新判準必須回 no
			name: "serialized",
			trace: []frameRec{
				f(10, thA, codex.MethodTurnStarted),
				f(11, thA, codex.MethodAgentMessageDelta),
				f(12, thA, codex.MethodTurnCompleted),
				f(13, thB, codex.MethodTurnStarted),
				f(14, thB, codex.MethodAgentMessageDelta),
				f(15, thB, codex.MethodTurnCompleted),
			},
			want: "no",
		},
		{
			// **舊指標的真正反例**：trace 含 thread 級 frame（TurnID 空）時，
			// A/B 的 frame index 區間互相包含（A=[0,4]、B=[1,6]），舊式
			// `firstB < lastA && firstA < lastB` → 1<4 && 0<6 = true（謊報並行）。
			// 新判準只看 turn/started 與 turn/completed 的先後，必須回 no。
			name: "serialized-with-thread-frames",
			trace: []frameRec{
				threadFrame(1, thA, codex.MethodThreadStarted),
				threadFrame(2, thB, codex.MethodThreadStarted),
				f(10, thA, codex.MethodTurnStarted),
				f(12, thA, codex.MethodTurnCompleted),
				threadFrame(13, thA, codex.MethodThreadStatusChanged),
				f(14, thB, codex.MethodTurnStarted),
				f(16, thB, codex.MethodTurnCompleted),
			},
			want: "no",
		},
		{
			// forced 收尾：兩個 turn 都沒有 turn/completed → 不可謊報通過
			name: "forced-no-completion",
			trace: []frameRec{
				f(10, thA, codex.MethodTurnStarted),
				f(11, thB, codex.MethodTurnStarted),
			},
			want: "inconclusive",
		},
		{
			name: "only-one-completed",
			trace: []frameRec{
				f(10, thA, codex.MethodTurnStarted),
				f(11, thB, codex.MethodTurnStarted),
				f(12, thA, codex.MethodTurnCompleted),
			},
			want: "inconclusive",
		},
		{
			name:  "no-turn-started",
			trace: []frameRec{f(10, thA, codex.MethodItemStarted)},
			want:  "inconclusive",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, why := overlapVerdict(tc.trace, thA, thB)
			if got != tc.want {
				t.Fatalf("overlapVerdict = %q (%s), want %q", got, why, tc.want)
			}
		})
	}
}
