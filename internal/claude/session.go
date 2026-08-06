package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"syscall"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/contract"
	"github.com/slam0504/sdlc-workbench/internal/proc"
)

type Config struct {
	Binary               string // 必填：managed CLI 路徑
	CWD                  string // canonical cwd：resume 必須與首次相同
	Prompt               string
	Resume               string
	MCPConfigPath        string
	PermissionPromptTool string
	SettingsJSON         string // --settings inline JSON（probe ask rule 用）
	Env                  []string
	TermGrace            time.Duration // 預設 5s
	MaxLineBytes         int           // scanner 行上限，預設 16MB；超過 → KindStreamError
}

func (c Config) args() []string {
	a := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json",
		"--verbose", "--include-partial-messages"}
	if c.SettingsJSON != "" {
		a = append(a, "--settings", c.SettingsJSON)
	}
	if c.PermissionPromptTool != "" {
		a = append(a, "--permission-prompt-tool", c.PermissionPromptTool,
			"--mcp-config", c.MCPConfigPath, "--strict-mcp-config")
	}
	if c.Resume != "" {
		a = append(a, "--resume", c.Resume)
	}
	return a
}

func NormalizeCWD(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

type Session struct {
	p      *proc.Proc
	argv   []string
	events chan contract.Event
}

func Start(ctx context.Context, cfg Config) (*Session, error) {
	cwd, err := NormalizeCWD(cfg.CWD)
	if err != nil {
		return nil, err
	}
	p, err := proc.Start(ctx, proc.Config{Binary: cfg.Binary, Args: cfg.args(),
		Dir: cwd, Env: cfg.Env, TermGrace: cfg.TermGrace})
	if err != nil { // binary 不存在等啟動失敗在這裡浮現
		return nil, err
	}
	msg, _ := json.Marshal(map[string]any{"type": "user",
		"message": map[string]any{"role": "user", "content": []map[string]string{{"type": "text", "text": cfg.Prompt}}}})
	if _, err := fmt.Fprintf(p.Stdin, "%s\n", msg); err != nil {
		_ = p.SignalGroup(syscall.SIGKILL) // v1.5：stdin 失敗也殺整組，不是只殺直接子程序
		p.Wait()
		return nil, err
	}
	_ = p.Stdin.Close() // M0 單回合：送完 prompt 即關

	maxLine := cfg.MaxLineBytes
	if maxLine == 0 {
		maxLine = 16 * 1024 * 1024
	}
	s := &Session{p: p, argv: append([]string{cfg.Binary}, cfg.args()...),
		events: make(chan contract.Event, 64)}
	go func() {
		defer close(s.events)
		sc := bufio.NewScanner(p.Stdout)
		// bufio.Scanner 的 max token = max(maxLine, cap(buf))：初始 cap 必須
		// 不大於 maxLine，否則小的 MaxLineBytes 會被 64KB 初始容量蓋掉。
		initCap := 64 * 1024
		if maxLine < initCap {
			initCap = maxLine
		}
		sc.Buffer(make([]byte, 0, initCap), maxLine)
		for sc.Scan() {
			s.events <- Decode(sc.Bytes())
		}
		if err := sc.Err(); err != nil { // 傳輸層錯誤是驗收證據，不可吞
			s.events <- contract.Event{Provider: contract.ProviderClaude, Kind: contract.KindStreamError,
				Raw: []byte(err.Error()), Err: err}
			_ = p.Terminate() // stream 已不可信，收掉整組
		}
	}()
	return s, nil
}

func (s *Session) Events() <-chan contract.Event { return s.events }
func (s *Session) Argv() []string                { return append([]string(nil), s.argv...) }
func (s *Session) Terminate() error              { return s.p.Terminate() }
func (s *Session) Wait() proc.Exit               { return s.p.Wait() } // supervisor 快取，任意時點可呼叫
func (s *Session) PGID() int                     { return s.p.PGID() }
