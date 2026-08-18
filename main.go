package main

import (
	"embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"

	"github.com/slam0504/sdlc-workbench/internal/approval"
	"github.com/slam0504/sdlc-workbench/internal/singleinstance"
)

//go:embed all:frontend/dist
var assets embed.FS

// 拒絕啟動的兩個可辨識退出碼。分開是因為使用者的處置不同：4 是「切回已開著的
// 視窗」，5 是「state directory 本身有問題（權限／檔案系統），去排除環境」。
// 兩者都 fail closed——都在開任何 writer 之前原地退出。
const (
	exitAlreadyRunning  = 4
	exitLockUnavailable = 5
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "mcp-approval" {
		// mcp-approval 是 app 自己 spawn 的 MCP stdio 子行程，**刻意不取單一
		// 實例鎖**：它必然與持鎖的父行程同時存在，取鎖只會讓它必然失敗。這樣
		// 做安全是因為它一個 state writer 都不開——只透過 unix socket 與父行程
		// 的 approval broker 對話（見 approval.RunMCPServer）。
		fs := flag.NewFlagSet("mcp-approval", flag.ExitOnError)
		sock := fs.String("socket", "", "broker unix socket path")
		_ = fs.Parse(os.Args[2:])
		if err := approval.RunMCPServer(*sock, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	os.Exit(runApp(runWailsUI, os.Stderr))
}

// uiRunner：「把 App 交給視窗層，跑到使用者關閉為止」這一步。production 恆為
// runWailsUI。barrier 測試在無 GUI 環境下替換的**只有這一步**——workspace 解
// 析、鎖的取得位置、App 的建立、以及鎖釋放的時機全部留在 runApp 自己手上，
// 所以「鎖是否早於 writer 初始化」「是否撐到 writer 全關才放」這兩件事測試無
// 法從外面繞過。
type uiRunner func(*App) error

// runApp：production 啟動總序。
//
//	resolve workspace → 取得 single-instance 鎖 → 建立 App → UI 層
//	（OnStartup 開 registry／audit／events sink／replay index／wire log／
//	SegmentSet 並跑 registry migration；OnShutdown 收尾並關閉全部 writer）
//	→ UI 回來（＝ shutdown 已完成）之後才釋放鎖。
//
// 鎖排在**第一步**不是為了 UX：AppendReceipt 的 offset 與 registry 的
// temp/rename 都以單一 writer 為前提（見 internal/singleinstance 的檔頭），
// 第二個 process 只要開過任何一個 writer、跑過任何一次 migration，那個前提就
// 已經破了。所以取鎖失敗時直接退出，一個 byte 的 state 都不寫。
func runApp(run uiRunner, msg io.Writer) int {
	// resolveWorkspace 的錯誤在這裡刻意忽略：它在失敗時仍回傳可用的 tmp
	// fallback 路徑，而錯誤本身由 App.startup 再解析一次時記進 startupErr／
	// UI 橫幅（原本的 fail loud 路徑不變）。真正不可用的環境會在下一行的
	// Acquire 開檔失敗，走 exitLockUnavailable。
	_, stateDir, _, _ := resolveWorkspace()
	lock, err := singleinstance.Acquire(stateDir)
	if err != nil {
		if errors.Is(err, singleinstance.ErrAlreadyRunning) {
			notifyRejected(msg, alreadyRunningMessage(stateDir))
			return exitAlreadyRunning
		}
		// fail closed：開檔／權限／檔案系統不支援 flock 等任何異常，都**不**
		// 當成「目前沒人持鎖」而繼續啟動。
		notifyRejected(msg, lockUnavailableMessage(stateDir, err))
		return exitLockUnavailable
	}
	defer func() { _ = lock.Release() }()

	app := NewApp()
	app.lockedStateDir = stateDir
	if err := run(app); err != nil {
		fmt.Fprintln(msg, "Error:", err.Error())
		return 1
	}
	return 0
}

func runWailsUI(app *App) error {
	return wails.Run(&options.App{
		Title:  "sdlc-workbench",
		Width:  1024,
		Height: 768,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind: []interface{}{
			app,
		},
	})
}

// alreadyRunningMessage：拒絕 UX 的主文。要能讓使用者一眼知道三件事——已經有
// 一個在跑、這次啟動什麼都沒動、以及是哪個 workspace。
// bring-to-front 不在本次範圍（owner 凍結）。
func alreadyRunningMessage(stateDir string) string {
	return "sdlc-workbench 已在執行中。\n" +
		"另一個 Workbench 實例正持有這個 workspace 的單一實例鎖，本次啟動已中止，" +
		"沒有讀取或寫入任何 session 狀態。請切換到已經開著的 Workbench 視窗。\n" +
		"workspace state：" + stateDir + "\n"
}

// lockUnavailableMessage：鎖本身取不到（開檔失敗、權限不足、檔案系統不支援
// flock）。刻意與「已在執行中」分開：這一類要使用者去修環境，不是去找視窗。
func lockUnavailableMessage(stateDir string, err error) string {
	return "sdlc-workbench 無法取得單一實例鎖，本次啟動已中止，沒有讀取或寫入任何 session 狀態。\n" +
		"為了不讓兩個實例同時寫同一份稽核與 registry，取不到鎖一律視為拒絕啟動。\n" +
		"workspace state：" + stateDir + "\n" +
		"原因：" + err.Error() + "\n"
}

// notifyRejected：拒絕訊息的出口。一律寫 stderr；若這次是從 .app bundle
// （Finder／Dock）啟動，stderr 沒有任何人看得到，額外彈一個原生對話框。
func notifyRejected(msg io.Writer, text string) {
	fmt.Fprint(msg, text)
	exe, err := os.Executable()
	if err != nil || !bundledExecutable(exe) {
		return
	}
	showBundleAlert(text)
}

// bundledExecutable：執行檔是否位於 macOS .app bundle 內。
//
// 這是「有沒有終端機可以看 stderr」的判準，**不是**測試旗標：go test 產出的
// 執行檔不在 bundle 裡，所以 barrier 測試不會被對話框卡住；反過來說真正的
// Finder 啟動一定命中。
func bundledExecutable(exe string) bool {
	return strings.Contains(exe, ".app/Contents/MacOS/")
}

// showBundleAlert：best effort 的原生對話框。失敗只是少一個視窗——拒絕本身與
// 退出碼都已經發生，不受影響。
func showBundleAlert(text string) {
	script := "display alert " + appleScriptString("sdlc-workbench 已在執行中") +
		" message " + appleScriptString(text) + " as critical"
	_ = exec.Command("osascript", "-e", script).Run()
}

// appleScriptString：AppleScript 字串常值。strconv.Quote 的跳脫規則（\" 與 \\）
// 對 AppleScript 字串同樣成立，換行則必須換成 AppleScript 的 return 常數。
func appleScriptString(s string) string {
	return strings.ReplaceAll(strconv.Quote(s), `\n`, `" & return & "`)
}
