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
	os.Exit(runWailsUI(NewApp()))
}

// uiRunner：「把 App 交給視窗層，跑到使用者關閉為止」這一步。production 恆為
// wailsUI；barrier 測試在無 GUI 環境下替換的**只有這一步**。
//
// 這個 seam 現在只影響拒絕 UX（第二個實例會不會開視窗），**不影響資料正確
// 性**——ownership lease 已經下沉到 App.startup（見 app.go 的 stateLease），
// 任何入口（含 `runWailsUI(NewApp())` 這種完全不經 runInstance 的寫法）都無法
// 在沒有 lease 的情況下開啟 writer。
type uiRunner func(*App) error

// runInstance：production 的實例啟動骨架。
//
//	（開視窗之前）取得 ownership lease → UI 層（OnStartup 的 App.startup 會
//	自行再取得／驗證同一份 lease；OnShutdown 的 App.shutdown 在全部 writer
//	關閉之後才釋放）
//
// 這裡先取一次 lease **純粹是拒絕 UX**：讓第二個實例連視窗都不開。它不是安全
// 邊界——安全邊界在 App.startup 裡。
//
// 拒絕路徑刻意直接 os.Exit 而不是把退出碼交還給 caller：這條路徑的正確性不能
// 取決於「呼叫端記得處理回傳值」，否則 `func main() { runWailsUI(NewApp()) }`
// 這種寫法會讓被拒的 process 以退出碼 0 靜默結束。
func runInstance(app *App, run uiRunner, msg io.Writer) int {
	if _, err := app.acquireStateLease(); err != nil {
		if errors.Is(err, singleinstance.ErrAlreadyRunning) {
			notifyRejected(msg, alreadyRunningMessage(app.stateDir))
			os.Exit(exitAlreadyRunning)
		}
		// fail closed：開檔／權限／檔案系統不支援 flock 等任何異常，都**不**
		// 當成「目前沒人持鎖」而繼續啟動。
		notifyRejected(msg, lockUnavailableMessage(app.stateDir, err))
		os.Exit(exitLockUnavailable)
	}
	if err := run(app); err != nil {
		fmt.Fprintln(msg, "Error:", err.Error())
		return 1
	}
	return 0
}

func runWailsUI(app *App) int { return runInstance(app, wailsUI, os.Stderr) }

func wailsUI(app *App) error {
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
