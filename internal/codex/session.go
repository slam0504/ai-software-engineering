package codex

import (
	"context"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/proc"
)

type Config struct {
	Binary    string
	CWD       string
	Env       []string
	TermGrace time.Duration
}

// Server 是長駐 codex app-server 子程序（internal/proc supervisor 語意：
// group 啟動、退出即清整組、Wait 快取）。Conn 的讀迴圈即 stdout 汲取，
// v1.6 汲取契約由建構滿足。
type Server struct {
	p    *proc.Proc
	conn *Conn
	argv []string
}

func StartAppServer(ctx context.Context, cfg Config) (*Server, error) {
	p, err := proc.Start(ctx, proc.Config{Binary: cfg.Binary, Args: []string{"app-server"},
		Dir: cfg.CWD, Env: cfg.Env, TermGrace: cfg.TermGrace})
	if err != nil {
		return nil, err
	}
	return &Server{p: p, conn: NewConn(p.Stdin, p.Stdout), argv: []string{cfg.Binary, "app-server"}}, nil
}

func (s *Server) Conn() *Conn            { return s.conn }
func (s *Server) Argv() []string         { return append([]string(nil), s.argv...) }
func (s *Server) Terminate() error       { return s.p.Terminate() }
func (s *Server) Wait() proc.Exit        { return s.p.Wait() }
func (s *Server) PGID() int              { return s.p.PGID() }
func (s *Server) StderrSnapshot() string { return s.p.StderrSnapshot() } // v1.6：長駐 server live 證據
func (s *Server) Done() <-chan struct{}  { return s.p.Done() }           // v1.7：非阻塞死亡判定

// ProbeTarget 薄委派（Task 8 Step 5b）
func (s *Server) BeginRecording(sink func([]byte) error) error { return s.conn.BeginRecording(sink) }
func (s *Server) StopRecording() error                         { return s.conn.StopRecording() }
func (s *Server) Handshake(ctx context.Context, ci ClientInfo) error {
	return s.conn.Handshake(ctx, ci)
}
