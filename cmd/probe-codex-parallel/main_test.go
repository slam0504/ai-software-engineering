package main

import (
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/codex"
)

// 鎖住 (a) 的自動判準必須能**區分**並行與串行化——舊指標（A/B frame 的 index 區間相交）
// 在串行化情境下恆真，這裡的 serialized case 就是它的反例。
func TestOverlapVerdict(t *testing.T) {
	const thA, thB = "thread-A", "thread-B"
	f := func(seq int, thread, method string) frameRec {
		return frameRec{Seq: seq, Dir: "s2c", Kind: "notification", Method: method,
			ThreadID: thread, TurnID: "turn-of-" + thread}
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
			// 串行化：A 整輪跑完 server 才受理 B。舊指標會回 true（thread 級 frame
			// 使兩區間互相包含），新指標必須回 no。
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
