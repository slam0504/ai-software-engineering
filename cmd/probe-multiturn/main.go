// probe-multiturn：M1 Task 4 的多輪 live probe。
// 嚴格序：send#1 → 讀到第一個 result（stdin 全程開啟）→ send#2（同 process）
// → 讀到第二個 result → 驗 session_id 一致 + 上下文保持。任一步失敗 exit 非 0。
// 另輸出 usage VERDICT（per-turn｜cumulative｜inconclusive）供 Task 6 定案。
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/claude"
	"github.com/slam0504/sdlc-workbench/internal/contract"
	"github.com/slam0504/sdlc-workbench/internal/ports"
	"github.com/slam0504/sdlc-workbench/internal/singleinstance"
)

type turnInfo struct {
	sessionID string
	text      strings.Builder
	usage     *contract.Usage
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "PROBE FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("PROBE PASS: in-process multi-turn confirmed")
}

// run 集中資源管理：任何失敗路徑都先清資源再返回。
func run() (retErr error) {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	stateDir := filepath.Join(root, ".workbench")
	recDir := filepath.Join(stateDir, "recordings")
	if err := os.MkdirAll(recDir, 0o755); err != nil {
		return fmt.Errorf("mkdir recordings: %w", err)
	}
	// 這個 probe 是**第三個**會寫進 workbench state directory 的進入點（app 與
	// mcp-approval 子命令是另外兩個）。下面的 os.Create 帶 O_TRUNC，若 app 正
	// 開著就會直接截斷它的錄流檔，所以這裡照樣取 single-instance 鎖：跟 app
	// 同一把、同一個目錄，取不到就拒絕執行（fail closed），不去猜「應該沒關係」。
	lock, err := singleinstance.Acquire(stateDir)
	if err != nil {
		return fmt.Errorf("probe 需要獨佔 workspace state（%s）：%w", stateDir, err)
	}
	defer func() { _ = lock.Release() }()

	out, err := os.Create(filepath.Join(recDir, "claude-multiturn.ndjson"))
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); cerr != nil && retErr == nil {
			retErr = fmt.Errorf("recording close: %w", cerr)
		}
	}()

	s, err := claude.Start(context.Background(), claude.Config{
		Binary: filepath.Join(root, "tools/claude-cli/node_modules/.bin/claude"),
		CWD:    root, MultiTurn: true,
		Prompt: "記住暗號：鳳梨酥。然後用大約兩百字介紹 TCP 三次握手", // turn1 刻意長回答（VERDICT 用）
	})
	if err != nil {
		return fmt.Errorf("start: %w", err)
	}
	defer func() { // 正常路徑等自然收尾（Close 錯誤不吞；Close 後持續 drain 防 scanner 阻塞）
		if cerr := s.Close(); cerr != nil && retErr == nil {
			retErr = fmt.Errorf("stdin close: %w", cerr)
		}
		go func() {
			for range s.Events() {
			}
		}()
		exit := make(chan ports.Exit, 1)
		go func() { exit <- s.Wait() }()
		select {
		case ex := <-exit:
			if retErr == nil && ex.Code != 0 {
				retErr = fmt.Errorf("CLI exit %d after close (stderr: %s)", ex.Code, ex.StderrTail)
			}
		case <-time.After(30 * time.Second):
			_ = s.Terminate()
			<-exit
			if retErr == nil {
				retErr = errors.New("CLI did not exit naturally within 30s after stdin close")
			}
		}
	}()

	waitResult := func(step string, d time.Duration) (turnInfo, error) {
		var tu turnInfo
		timer := time.After(d)
		for {
			select {
			case ev, ok := <-s.Events():
				if !ok {
					return tu, fmt.Errorf("%s: stream closed before result (no in-process multi-turn)", step)
				}
				if _, werr := fmt.Fprintf(out, "%s\n", ev.Raw); werr != nil {
					return tu, fmt.Errorf("%s: recording write: %w", step, werr)
				}
				if ev.SessionID != "" {
					tu.sessionID = ev.SessionID
				}
				if ev.Kind == contract.KindDelta || (ev.Kind == contract.KindMessage && ev.Role == "assistant") {
					tu.text.WriteString(ev.Text)
				}
				if ev.Kind == contract.KindResult {
					tu.usage = ev.Usage
					return tu, nil
				}
			case <-timer:
				return tu, fmt.Errorf("%s: timeout", step)
			}
		}
	}

	t1, err := waitResult("turn1", 180*time.Second)
	if err != nil {
		return err
	}
	// 嚴格序：第一個 result 已讀到、stdin 仍開啟，才送第二則
	if err := s.Send("暗號是什麼？只回覆暗號本身"); err != nil { // turn2 刻意極短回答
		return fmt.Errorf("send2: %w", err)
	}
	t2, err := waitResult("turn2", 120*time.Second)
	if err != nil {
		return err
	}

	if t1.sessionID == "" || t1.sessionID != t2.sessionID {
		return fmt.Errorf("session id mismatch: %q vs %q", t1.sessionID, t2.sessionID)
	}
	if !strings.Contains(t2.text.String(), "鳳梨酥") {
		return fmt.Errorf("context lost: %q", t2.text.String())
	}

	// usage VERDICT（可判定規則 + inconclusive fallback）
	verdict := "inconclusive"
	if t1.usage != nil && t2.usage != nil && t1.usage.OutputTokens > 0 {
		o1, o2 := t1.usage.OutputTokens, t2.usage.OutputTokens
		switch {
		case o2 < o1/2:
			verdict = "per-turn"
		case o2 >= o1:
			verdict = "cumulative"
		}
	}
	u1, _ := json.Marshal(t1.usage)
	u2, _ := json.Marshal(t2.usage)
	fmt.Printf("USAGE turn1=%s turn2=%s VERDICT=%s\n", u1, u2, verdict)
	return nil
}
