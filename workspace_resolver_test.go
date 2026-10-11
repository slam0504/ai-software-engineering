package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// ---- B4 §5.1／§6.1：resolveWorkspaceWith（隔離目錄、真實 os.Stat）----
//
// 全部用 t.TempDir() 底下的隔離目錄；不碰真實的家目錄，也不依賴 chmod（讓 mkdir 失敗改走
// seam 的 MkdirAll）。Stat 一律是真實的 os.Stat——os.SameFile 對自製的 FileInfo 只會回
// false，用假物件的話「isHome」的測試等於沒有驗證任何東西（spec §6.1）。

type wsFixture struct {
	root, home, cwd, tmp string
	envDir               string // WORKBENCH_WORKSPACE 的值（空字串＝未設）
	env                  workspaceEnv
}

func realDir(t *testing.T, p string) string {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func newWsFixture(t *testing.T) *wsFixture {
	t.Helper()
	root := realDir(t, t.TempDir())
	f := &wsFixture{
		root: root,
		home: realDir(t, filepath.Join(root, "home")),
		cwd:  realDir(t, filepath.Join(root, "work", "repo")),
		tmp:  realDir(t, filepath.Join(root, "tmp")),
	}
	f.env = workspaceEnv{
		Getenv: func(k string) string {
			if k == "WORKBENCH_WORKSPACE" {
				return f.envDir
			}
			return ""
		},
		Getwd:     func() (string, error) { return f.cwd, nil },
		HomeDir:   func() (string, error) { return f.home, nil },
		TempDir:   func() string { return f.tmp },
		Normalize: productionWorkspaceEnv().Normalize,
		MkdirAll:  os.MkdirAll,
		Stat:      os.Stat,
	}
	return f
}

func (f *wsFixture) resolve() (workspaceResolution, bool, []string) {
	r := resolveWorkspaceWith(f.env)
	required, reasons := workspacePolicy(r.Source, r.Provenance)
	return r, required, reasons
}

func TestResolveWorkspaceWith_ValidEnvOutsideHomeNeedsNoConfirmation(t *testing.T) {
	f := newWsFixture(t)
	f.envDir = realDir(t, filepath.Join(f.root, "proj"))
	r, required, reasons := f.resolve()
	if r.Source != "env" || r.Workspace != f.envDir || r.Err != nil {
		t.Fatalf("env 有效時應解析到 env：%+v", r)
	}
	if r.Provenance.EnvRejected || r.Provenance.IsHome || !r.Provenance.HomeIdentityKnown {
		t.Fatalf("provenance 不符：%+v", r.Provenance)
	}
	if required {
		t.Fatalf("E2E／CI 的 env 注入路徑不得需要確認，reasons=%v", reasons)
	}
	if r.State != filepath.Join(f.envDir, ".workbench") {
		t.Fatalf("state 應是 <workspace>/.workbench，實得 %s", r.State)
	}
}

func TestResolveWorkspaceWith_EnvEqualsHomeIsHome(t *testing.T) {
	f := newWsFixture(t)
	f.envDir = f.home
	r, required, reasons := f.resolve()
	if r.Source != "env" || !r.Provenance.IsHome {
		t.Fatalf("env=$HOME 必須記為 isHome：%+v", r)
	}
	if !required || !reflect.DeepEqual(reasons, []string{wsReasonIsHome}) {
		t.Fatalf("required=%v reasons=%v", required, reasons)
	}
}

// H1：os.Stat 會跟隨 symlink，所以指向家目錄的 symlink 也是 isHome。
func TestResolveWorkspaceWith_SymlinkToHomeIsHome(t *testing.T) {
	f := newWsFixture(t)
	link := filepath.Join(f.root, "link-to-home")
	if err := os.Symlink(f.home, link); err != nil {
		t.Fatal(err)
	}
	f.envDir = link
	r, required, _ := f.resolve()
	if r.Workspace != f.home {
		t.Fatalf("Normalize 應把 symlink 解成實際目錄 %s，實得 %s", f.home, r.Workspace)
	}
	if !r.Provenance.IsHome || !required {
		t.Fatalf("指向家目錄的 symlink 必須是 isHome：%+v required=%v", r.Provenance, required)
	}
}

// Invariant：家目錄底下的子目錄以目錄身分判定，不因路徑前綴相同而被判成 isHome。
func TestResolveWorkspaceWith_ProjectUnderHomeIsNotHome(t *testing.T) {
	f := newWsFixture(t)
	f.envDir = realDir(t, filepath.Join(f.home, "projects", "foo"))
	r, required, reasons := f.resolve()
	if r.Provenance.IsHome || !r.Provenance.HomeIdentityKnown {
		t.Fatalf("家目錄底下的專案不是 isHome：%+v", r.Provenance)
	}
	if required {
		t.Fatalf("家目錄底下的專案不需確認，reasons=%v", reasons)
	}
}

func TestResolveWorkspaceWith_CwdIsHome(t *testing.T) {
	f := newWsFixture(t)
	f.cwd = f.home // 在終端機 cd ~ 後啟動
	r, required, reasons := f.resolve()
	if r.Source != "cwd" || !r.Provenance.IsHome {
		t.Fatalf("source=cwd 且 isHome：%+v", r)
	}
	if !required || !reflect.DeepEqual(reasons, []string{wsReasonIsHome}) {
		t.Fatalf("required=%v reasons=%v", required, reasons)
	}
}

func TestResolveWorkspaceWith_CwdProjectNeedsNoConfirmation(t *testing.T) {
	f := newWsFixture(t) // 沒有 env，cwd 是專案目錄（wails dev）
	r, required, reasons := f.resolve()
	if r.Source != "cwd" || required {
		t.Fatalf("cwd 專案目錄不需確認：%+v required=%v reasons=%v", r, required, reasons)
	}
}

func TestResolveWorkspaceWith_InvalidEnvFallsBackToCwd(t *testing.T) {
	f := newWsFixture(t)
	f.envDir = filepath.Join(f.root, "does-not-exist")
	r, required, reasons := f.resolve()
	if r.Source != "cwd" || r.Workspace != f.cwd {
		t.Fatalf("env 無效應退回 cwd：%+v", r)
	}
	if r.Err != nil {
		t.Fatalf("成功候選不得回傳先前候選的錯誤（維持 B4 之前的行為）：%v", r.Err)
	}
	p := r.Provenance
	if !p.EnvSet || !p.EnvRejected || p.EnvRejectReason != envRejectNormalizeFailed {
		t.Fatalf("provenance 必須記下 env 被拒（normalize_failed）：%+v", p)
	}
	if !required || !reflect.DeepEqual(reasons, []string{wsReasonEnvRejectedNormalize}) {
		t.Fatalf("required=%v reasons=%v", required, reasons)
	}
}

func TestResolveWorkspaceWith_EnvMkdirFailureIsClassified(t *testing.T) {
	f := newWsFixture(t)
	f.envDir = realDir(t, filepath.Join(f.root, "readonly-proj"))
	envState := filepath.Join(f.envDir, ".workbench")
	mkErr := errors.New("injected mkdir failure")
	f.env.MkdirAll = func(path string, perm os.FileMode) error {
		if path == envState {
			return mkErr
		}
		return os.MkdirAll(path, perm)
	}
	r, _, reasons := f.resolve()
	if r.Source != "cwd" {
		t.Fatalf("env 的 .workbench 建不出來應退回 cwd：%+v", r)
	}
	if r.Provenance.EnvRejectReason != envRejectMkdirFailed ||
		!reflect.DeepEqual(reasons, []string{wsReasonEnvRejectedMkdir}) {
		t.Fatalf("應分類為 mkdir_failed：%+v reasons=%v", r.Provenance, reasons)
	}
}

func TestResolveWorkspaceWith_InvalidEnvFallsBackToHome(t *testing.T) {
	f := newWsFixture(t)
	f.envDir = filepath.Join(f.root, "nope")
	f.env.Getwd = func() (string, error) { return "/", nil } // Finder 啟動：cwd 是 /
	r, required, reasons := f.resolve()
	if r.Source != "home" || r.Workspace != f.home {
		t.Fatalf("應退回 home：%+v", r)
	}
	want := []string{wsReasonSourceHome, wsReasonEnvRejectedNormalize, wsReasonIsHome}
	if !required || !reflect.DeepEqual(reasons, want) {
		t.Fatalf("required=%v reasons=%v，want %v", required, reasons, want)
	}
}

func TestResolveWorkspaceWith_FinderLaunchFallsBackToHome(t *testing.T) {
	f := newWsFixture(t) // 沒有 env
	f.env.Getwd = func() (string, error) { return "/", nil }
	r, required, reasons := f.resolve()
	want := []string{wsReasonSourceHome, wsReasonIsHome}
	if r.Source != "home" || !required || !reflect.DeepEqual(reasons, want) {
		t.Fatalf("source=%s required=%v reasons=%v，want %v", r.Source, required, reasons, want)
	}
}

func TestResolveWorkspaceWith_AllCandidatesFailFallsBackToTmp(t *testing.T) {
	f := newWsFixture(t)
	f.envDir = filepath.Join(f.root, "nope")
	f.env.Getwd = func() (string, error) { return "/", nil }
	f.env.HomeDir = func() (string, error) { return "", errors.New("no home") }
	r, required, reasons := f.resolve()
	if r.Source != "tmp" || r.Workspace != f.tmp {
		t.Fatalf("tmp fallback 的 workspace 仍是 TempDir 本身：%+v", r)
	}
	if r.State != filepath.Join(f.tmp, "sdlc-workbench", ".workbench") {
		t.Fatalf("tmp fallback 的 state 仍是 <tmp>/sdlc-workbench/.workbench：%s", r.State)
	}
	if r.Err == nil {
		t.Fatal("tmp fallback 成功時仍回傳先前候選的錯誤（維持 B4 之前的行為）")
	}
	want := []string{wsReasonSourceTmp, wsReasonEnvRejectedNormalize, wsReasonHomeUnknown}
	if !required || !reflect.DeepEqual(reasons, want) {
		t.Fatalf("required=%v reasons=%v，want %v", required, reasons, want)
	}
}

func TestResolveWorkspaceWith_HomeCandidateMkdirFailsThenTmp(t *testing.T) {
	f := newWsFixture(t) // 沒有 env
	f.env.Getwd = func() (string, error) { return "/", nil }
	homeState := filepath.Join(f.home, ".workbench")
	f.env.MkdirAll = func(path string, perm os.FileMode) error {
		if path == homeState {
			return errors.New("injected")
		}
		return os.MkdirAll(path, perm)
	}
	r, _, reasons := f.resolve()
	if r.Source != "tmp" || r.Provenance.EnvRejected {
		t.Fatalf("home 建不出 .workbench 應退回 tmp，且不是 env 被拒：%+v", r)
	}
	if !reflect.DeepEqual(reasons, []string{wsReasonSourceTmp}) {
		t.Fatalf("reasons=%v", reasons)
	}
}

// R1 (d)：家目錄身分無法判定——HomeDir 失敗、或 Stat 家目錄／workspace 失敗——都要記成 unknown，
// 即使 env 本身完全正常。
func TestResolveWorkspaceWith_HomeIdentityUnknown(t *testing.T) {
	cases := []struct {
		name string
		tune func(f *wsFixture)
	}{
		{"UserHomeDir 失敗", func(f *wsFixture) {
			f.env.HomeDir = func() (string, error) { return "", errors.New("no home") }
		}},
		{"家目錄 Stat 失敗", func(f *wsFixture) {
			home := f.home
			f.env.Stat = func(p string) (os.FileInfo, error) {
				if p == home {
					return nil, errors.New("injected stat failure")
				}
				return os.Stat(p)
			}
		}},
		{"workspace Stat 失敗", func(f *wsFixture) {
			ws := f.envDir
			f.env.Stat = func(p string) (os.FileInfo, error) {
				if p == ws {
					return nil, errors.New("injected stat failure")
				}
				return os.Stat(p)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newWsFixture(t)
			f.envDir = realDir(t, filepath.Join(f.root, "proj"))
			c.tune(f)
			r, required, reasons := f.resolve()
			if r.Source != "env" || r.Provenance.HomeIdentityKnown || r.Provenance.IsHome {
				t.Fatalf("身分無法判定：%+v", r)
			}
			if !required || !reflect.DeepEqual(reasons, []string{wsReasonHomeUnknown}) {
				t.Fatalf("必須要求確認（fail-closed）：required=%v reasons=%v", required, reasons)
			}
		})
	}
}

// 取鎖之前只允許建立空的 state directory（既有契約）：resolver 不得多寫任何東西。
func TestResolveWorkspaceWith_OnlyCreatesEmptyStateDir(t *testing.T) {
	f := newWsFixture(t)
	f.envDir = realDir(t, filepath.Join(f.root, "proj"))
	r, _, _ := f.resolve()
	ents, err := os.ReadDir(r.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 || ents[0].Name() != ".workbench" {
		t.Fatalf("workspace 底下只允許 .workbench：%v", ents)
	}
	inner, err := os.ReadDir(r.State)
	if err != nil {
		t.Fatal(err)
	}
	if len(inner) != 0 {
		t.Fatalf("state directory 必須是空的：%v", inner)
	}
}
