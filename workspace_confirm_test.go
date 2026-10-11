package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// ---- B4 共用的測試輔助：用 resolution 把 App 放進指定的確認狀態 ----

// pendingResolution：從 Finder 開啟的形狀（source=home、isHome）→ Pending。
func pendingResolution(ws string) workspaceResolution {
	return workspaceResolution{Workspace: ws, Source: "home",
		Provenance: workspaceProvenance{IsHome: true, HomeIdentityKnown: true}}
}

// notRequiredResolution：env 注入的正常專案目錄（E2E／CI 的路徑）→ NotRequired。
func notRequiredResolution(ws string) workspaceResolution {
	return workspaceResolution{Workspace: ws, Source: "env",
		Provenance: workspaceProvenance{EnvSet: true, HomeIdentityKnown: true}}
}

func makePending(a *App)     { a.workspaceConfirm.resolve(pendingResolution(a.workspaceDir)) }
func makeNotRequired(a *App) { a.workspaceConfirm.resolve(notRequiredResolution(a.workspaceDir)) }

func mustStatus(t *testing.T, a *App) WorkspaceConfirmationInfo {
	t.Helper()
	st, err := a.WorkspaceConfirmationStatus()
	if err != nil {
		t.Fatalf("WorkspaceConfirmationStatus: %v", err)
	}
	return st
}

// ---- R3／R4：確認狀態與 binding ----

// 從沒解析過 workspace 的 App（零值）必須 fail closed，而且 ConfirmWorkspace("") 不得把它確認掉
// ——空字串的 expectedPath 恰好「等於」空的快照，這是最容易漏掉的洞（Review Focus 1）。
func TestWorkspaceConfirmation_UnresolvedFailsClosed(t *testing.T) {
	a := NewApp()
	a.setPhase(phaseReady)
	st := mustStatus(t, a)
	if !st.Required || st.Confirmed || !reflect.DeepEqual(st.Reasons, []string{wsReasonUnresolved}) {
		t.Fatalf("未解析的 App 必須回報 required 且帶 unresolved 原因：%+v", st)
	}
	for _, path := range []string{"", "/anything"} {
		err := a.ConfirmWorkspace(path)
		if !errors.Is(err, errWorkspaceConfirmUnavailable) {
			t.Fatalf("未解析時 ConfirmWorkspace(%q) 必須回 unavailable，實得 %v", path, err)
		}
	}
	if st := mustStatus(t, a); st.Confirmed {
		t.Fatalf("未解析的 App 不得被確認：%+v", st)
	}
}

// R3 處理順序：先核對路徑，再依狀態決定結果。三種狀態都要有「路徑不符會被拒」的測試，
// 而且被拒之後狀態不變（Review Focus 2：字串必須完全相同，不接受任何別名）。
func TestConfirmWorkspace_PathMismatchRejectedInEveryState(t *testing.T) {
	setups := map[string]func(a *App){
		"Pending":     func(a *App) { makePending(a) },
		"Confirmed":   func(a *App) { makePending(a); _ = a.ConfirmWorkspace(a.workspaceDir) },
		"NotRequired": func(a *App) { makeNotRequired(a) },
	}
	for name, setup := range setups {
		t.Run(name, func(t *testing.T) {
			a, _ := newTestApp(t)
			setup(a)
			ws := a.workspaceDir
			alias := filepath.Join(filepath.Dir(ws), "alias-of-"+filepath.Base(ws))
			if err := os.Symlink(ws, alias); err != nil {
				t.Fatal(err)
			}
			bad := []string{
				"", ws + "/", ws + " ", strings.ToUpper(ws), strings.ToLower(ws) + "x",
				"~/" + filepath.Base(ws), filepath.Base(ws), alias,
			}
			before := mustStatus(t, a)
			for _, p := range bad {
				if p == ws { // ToUpper 在純小寫數字路徑上可能剛好相同；相同就不是壞輸入
					continue
				}
				if err := a.ConfirmWorkspace(p); !errors.Is(err, errWorkspaceConfirmPathMismatch) {
					t.Fatalf("[%s] ConfirmWorkspace(%q) 必須回 path_mismatch，實得 %v", name, p, err)
				}
			}
			if after := mustStatus(t, a); !reflect.DeepEqual(after, before) {
				t.Fatalf("[%s] 路徑不符不得改變狀態：%+v → %+v", name, before, after)
			}
		})
	}
}

func TestConfirmWorkspace_PendingConfirmedAndIdempotent(t *testing.T) {
	a, _ := newTestApp(t)
	makePending(a)
	st := mustStatus(t, a)
	if !st.Required || st.Confirmed {
		t.Fatalf("Pending：%+v", st)
	}
	if err := a.ConfirmWorkspace(st.Workspace); err != nil {
		t.Fatalf("第一次確認：%v", err)
	}
	if st := mustStatus(t, a); !st.Required || !st.Confirmed {
		t.Fatalf("確認後：%+v", st)
	}
	if err := a.ConfirmWorkspace(st.Workspace); err != nil {
		t.Fatalf("重複確認必須冪等成功：%v", err)
	}
	if st := mustStatus(t, a); !st.Confirmed {
		t.Fatalf("重複確認後仍是 Confirmed：%+v", st)
	}

	b, _ := newTestApp(t)
	makeNotRequired(b)
	nst := mustStatus(t, b)
	if nst.Required || nst.Confirmed {
		t.Fatalf("NotRequired：%+v", nst)
	}
	if err := b.ConfirmWorkspace(nst.Workspace); err != nil {
		t.Fatalf("NotRequired 的 ConfirmWorkspace 必須冪等成功：%v", err)
	}
	if after := mustStatus(t, b); after.Confirmed || after.Required {
		t.Fatalf("NotRequired 呼叫 Confirm 之後狀態不變：%+v", after)
	}
}

// R3 比對對象：前端傳 status.workspace 的原始字串。含空白、中日文與 NFD 組合字元的路徑，
// 原樣回傳就必須通過；NFC／NFD 互換則是不同字串，必須被拒。
func TestConfirmWorkspace_RawStringRoundTripWithUnicodePath(t *testing.T) {
	a, _ := newTestApp(t)
	nfd := "/Users/測試 user/café 專案"
	nfc := "/Users/測試 user/café 專案"
	a.workspaceConfirm.resolve(workspaceResolution{Workspace: nfd, Source: "home",
		Provenance: workspaceProvenance{IsHome: true, HomeIdentityKnown: true}})
	st := mustStatus(t, a)
	if st.Workspace != nfd {
		t.Fatalf("status 必須原樣回報 workspace：%q", st.Workspace)
	}
	if err := a.ConfirmWorkspace(nfc); !errors.Is(err, errWorkspaceConfirmPathMismatch) {
		t.Fatalf("NFC 與 NFD 是不同字串，必須被拒：%v", err)
	}
	if err := a.ConfirmWorkspace(st.Workspace); err != nil {
		t.Fatalf("原樣回傳 status.workspace 必須通過：%v", err)
	}
}

// R4：確認只存在於這一次 App instance 的記憶體；新的 instance 依 R1 重新判定。
func TestWorkspaceConfirmation_NewAppInstanceStartsPendingAgain(t *testing.T) {
	first, _ := newTestApp(t)
	makePending(first)
	if err := first.ConfirmWorkspace(first.workspaceDir); err != nil {
		t.Fatal(err)
	}
	second, _ := newTestAppIn(t, first.workspaceDir, first.stateDir+"-2")
	makePending(second)
	if st := mustStatus(t, second); !st.Required || st.Confirmed {
		t.Fatalf("新的 App instance 必須回到 Pending：%+v", st)
	}
}

// shutdown 或尚未 ready 時，兩個 binding 的行為與既有 lifecycle 規則一致（被 beginTxn 拒絕）；
// 閘門本身不改變 phase，所以 Pending 時兩者仍可呼叫（R2 Invariant）。
func TestWorkspaceConfirmationBindings_FollowLifecycle(t *testing.T) {
	fresh := NewApp() // phaseInitializing
	if _, err := fresh.WorkspaceConfirmationStatus(); err == nil {
		t.Fatal("尚未 ready：WorkspaceConfirmationStatus 必須被 lifecycle 拒絕")
	}
	if err := fresh.ConfirmWorkspace("/x"); err == nil {
		t.Fatal("尚未 ready：ConfirmWorkspace 必須被 lifecycle 拒絕")
	}

	a, _ := newTestApp(t)
	makePending(a) // Pending 不是 phase：兩個 binding 仍可呼叫
	if _, err := a.WorkspaceConfirmationStatus(); err != nil {
		t.Fatalf("Pending 時 status 必須可呼叫：%v", err)
	}
	a.setPhase(phaseShuttingDown)
	if _, err := a.WorkspaceConfirmationStatus(); err == nil {
		t.Fatal("shutdown 之後 WorkspaceConfirmationStatus 必須被拒")
	}
	if err := a.ConfirmWorkspace(a.workspaceDir); err == nil {
		t.Fatal("shutdown 之後 ConfirmWorkspace 必須被拒")
	}
	if got := a.workspaceConfirm.snapshot().state; got != wcPending {
		t.Fatalf("被 lifecycle 拒絕的 ConfirmWorkspace 不得改變狀態：%v", got)
	}
}

// ---- binding surface（spec §5.3、§6.1）----

func TestWorkspaceBindingsAreThinWrappersAndNotStartupSafe(t *testing.T) {
	_, files, info, pkg := typeCheckProductionPackage(t)
	appType := namedType(t, pkg, "App")
	methods := appMethodsByName(files, info, appType)
	for _, name := range []string{"WorkspaceConfirmationStatus", "ConfirmWorkspace"} {
		fd := methods[name]
		if fd == nil {
			t.Fatalf("找不到 binding %s", name)
		}
		if why := thinWrapperViolationTyped(fd, methods, methodSignature(info, fd)); why != "" {
			t.Errorf("%s 不是合格的薄包裝：%s", name, why)
		}
		if startupSafeBindings[name] != "" {
			t.Errorf("%s 不得列入 startup-safe 白名單（只在 ready 之後可呼叫）", name)
		}
	}
	if len(startupSafeBindings) != 1 || startupSafeBindings["CLIInfo"] == "" {
		t.Errorf("startup-safe 白名單必須維持只有 CLIInfo：%v", startupSafeBindings)
	}
}

// CLIInfo 的形狀不變：確認狀態不放進 startupState／CLIInfo（spec §3.3）。
func TestCLIInfoShapeUnchangedByWorkspaceConfirmation(t *testing.T) {
	a, _ := newTestApp(t)
	makePending(a)
	var keys []string
	for k := range a.CLIInfo() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := []string{"claudeVersion", "codexVersion", "node", "ready", "startupError",
		"toolsDir", "toolsSource", "workspace", "workspaceSource"}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("CLIInfo 的 key 集合不得改變：%v", keys)
	}
}

// production 接線：acquireStateLease 解析完 workspace 的當下就寫入確認狀態（與 publishWorkspace
// 同一個時機）。其餘測試用 resolve() 直接放進指定狀態，只有這條與 Task 3 的 TestProductionStartupPath_*
// 驗證「production 真的接了線」——拿掉 acquireStateLease 裡的 resolve 呼叫，這條必須紅。
func TestAcquireStateLeaseResolvesWorkspaceConfirmation(t *testing.T) {
	home := realDir(t, filepath.Join(t.TempDir(), "home"))
	t.Setenv("HOME", home)
	t.Setenv("WORKBENCH_WORKSPACE", home)
	a := NewApp()
	a.ctx = context.Background()
	lease, err := a.acquireStateLease()
	if err != nil {
		t.Fatalf("acquireStateLease: %v", err)
	}
	t.Cleanup(func() { _ = lease.release() })
	snap := a.workspaceConfirm.snapshot()
	if snap.state != wcPending || snap.workspace != home || snap.source != "env" ||
		!reflect.DeepEqual(snap.reasons, []string{wsReasonIsHome}) {
		t.Fatalf("production 解析必須寫入 Pending 狀態：%+v", snap)
	}
}
