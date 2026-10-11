# B4 Workspace 確認閘與高風險警示 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.
>
> **版本：** rev3（2026-10-11），依 Codex plan review002 修正一項：定義 Wails 產物 `frontend/wailsjs/go/models.ts` 中 `WorkspaceConfirmationInfo` 類別兩行 tab 空行的**狹窄例外**——保留產生器原樣輸出、不做空白正規化（比照 A5），Task 2 明寫不得手動修改，Task 8 的 `git diff --check` 收尾改為「原始輸出與 rc 存檔，再對原始輸出做精確比對」兩步（其餘任何警告必須為 0），Global Constraints 第 22 條同步。前版：rev2（commit `8690d7a`）。
>
> rev2（2026-10-11），依 Codex plan review001（對 rev1 commit `25cfe11` 的 CHANGES_REQUIRED）修正五項：（1）前端 `parseStatus` 改為完整 DTO 形狀驗證、fail closed，刪除「null 可容忍」並改加 refresh 層級的負例；（2）Task 4 `specAssist` 在 purpose pin 之後、確認閘之前加 `defer cancel()`（inline `cancel()` 保留），cancel mutant 改為拿掉所有取消保護；（3）Task 3 自動修補只限 `newTestAppIn` 那一行，其餘失敗先追責任歸屬並回報 owner，`TestAcquireStateLeaseResolvesWorkspaceConfirmation`／`TestProductionStartupPath_*` 失敗視為可能的 production 接線回歸；（4）所有 focused 測試、mutation、完整 gate 的指令改為先存完整輸出與 rc 再讀 log，每個 mutant 獨立 log／rc／備份／cmp 並確認紅在目標測試，Task 8 改用 Task 0 的 base 做三點比較，Self-review 驗證表改為逐次紀錄；（5）Task 8 的 Gherkin 不再寫成會持久保存的稽核保證。前版：rev1（commit `25cfe11`）。
>
> **審閱狀態：** owner 已於 2026-10-11 14:53 審閱本計畫，並選擇 subagent-driven 執行方式；「給審查者的待決事項」已全部裁定（見文末）。
>
> **專案紀律（優先於一般慣例）：** 實作者就是最終執行者，不得再轉派 subagent 做驗證；每個 task 完成後先回報 owner 再進下一個 task（review 檢查點先告知）；回報只引用本次實際執行過的 tool result，沒跑過的標成未驗證。

**Goal:** 在高風險 workspace（家目錄、tmp、env 無效退回、家目錄身分無法判定）上，使用者明確確認之前，不得開始任何 AI 工作（新 session 首輪、resume、後續訊息、Spec／Plan Assist）。

**Architecture:** `resolveWorkspace` 回傳帶 provenance 的結構（環境輸入走小型 seam），純函式 `workspacePolicy` 算出是否需要確認與原因；確認狀態放在 App 上獨立欄位 `workspaceConfirm`（自己的 mutex，不進 `startupState`），由 `WorkspaceConfirmationStatus`／`ConfirmWorkspace` 兩個薄包裝 binding 讀寫；四個 AI 工作入口共用 `requireWorkspaceConfirmed` 閘門；前端用獨立 Pinia store 加頂部列警示元件，fail closed（尚未取得、查詢失敗、形狀不對都視為封鎖）。

**Tech Stack:** Go 1.26.5（`GOTOOLCHAIN=local`）、Wails v2.13.0、Vue 3＋Pinia＋vue-i18n、vitest、vue-tsc。

**Spec:** `docs/superpowers/specs/2026-10-11-b4-workspace-confirmation-design.md`（rev2，commit `6d8d063`，已通過 design gate：`b3a-evidence/b4/codex-spec-review-002/REVIEW.md` ACCEPT）。本計畫逐條實現 spec，不擴大也不縮小範圍；spec 與程式碼不符之處列在文末「給審查者的待決事項」，**不自行改 spec**。

**基準：** main `facf5df`（tree `6fee1676…`）。下文 `檔案:行號` 一律指這個 tree（Task 1 之後 `app.go` 行號會位移，編輯時以文字錨點為準）。

---

## Global Constraints

（一行一條，值逐字取自 spec；每個 task 的要求都隱含這一節。）

1. 「AI 工作入口」＝ `startSession`、`sendMessage`、`specAssist`、`planAssist` 這四個內部實作（spec §2.1）。
2. R1：下列任一成立時 `required=true`，`reasons` 列出所有成立的項目：(a) `source ∈ {home, tmp}`；(b) `envSet && envRejected`；(c) `isHome`：實際 workspace 與 `os.UserHomeDir()` 經 `os.Stat` 後 `os.SameFile` 成立，不論 source；(d) `!homeIdentityKnown`：家目錄或 workspace 的身分無法判定。
3. R1 Invariant：家目錄底下的子目錄不會因為路徑字串的前綴相同而被判成 (c)，判定一律以目錄身分為準。
4. `isHome` 的測試一律對隔離的**真實目錄**呼叫 `os.Stat` 取得 FileInfo，**不得**使用自製的 FileInfo 假物件。
5. resolver 的環境輸入（env、cwd、home、tmp，以及建立目錄的函式）透過一個小型 seam 注入；測試使用隔離目錄，不碰真實的家目錄，也不依賴 chmod。
6. provenance 的判定只做 `os.Stat`，**不新增任何寫入**；「取鎖前只允許空的 state dir 加上鎖檔」的契約維持不變。
7. 不改 fallback 的順序與目錄：tmp fallback 的 workspace 仍是 `os.TempDir()`，state 仍是 `<tmp>/sdlc-workbench/.workbench`。
8. 確認狀態**不放進** `startupState`（避免動到 `CLIInfo` 形狀與欄位枚舉守門）；兩個新 binding 都不列入 startup-safe 白名單、都必須是 `beginTxn → defer endTxn → impl` 薄包裝。
9. R2 拒絕時：error 以 `workspace_confirmation_required` 開頭；不呼叫 `BeginNewSessionSubmit`／`BeginSubmit`；不 spawn CLI 或 app-server；不建立 broker、socket、MCP config、recorder、host；不送 `thread/start`、`turn/start`；Manager 的 submit 狀態不變；允許的副作用只有一筆 `workspace_confirmation_blocked` audit、交易計數，以及 Assist 入口在閘門之前必須做的 root context 惰性建立（`procRoot`）與這次呼叫自己的 context 的取消。
10. R2：不論 claude 或 codex、首輪或 resume、`approvalPolicy` 是 `untrusted` 還是 `never`，都要被拒；各入口既有的檢查與錯誤優先順序不變，`registryUncertain` 只屬於 `startSession`，**不**加到其他三個入口。
11. R2 Invariant：閘門不改變 App 的 lifecycle phase，所以未確認時 `WorkspaceConfirmationStatus` 與 `ConfirmWorkspace` 仍然可以呼叫。
12. §5.4 Assist 固定順序：外層交易（薄包裝的 `beginTxn`）→ `knownProvider` → 取得 root context → **purpose pin**（`purpose = "spec_assist"`）→ 拒絕時一定會執行的 cancel → **確認閘** → gen／`assistActive`／runner；root context 之後的下一個敘述必須是 purpose pin；**不新增內層 `beginTxn`**；`planAssist` 在取得 root context 與既有的 `defer cancel()` 之後、`ensureGate()` 之前檢查。
13. R3：**先核對路徑，再依狀態決定結果**；`expectedPath` 與後端快照的 workspace **字串不完全相同**時，不論狀態是 `Pending`、`Confirmed` 或 `NotRequired`，一律回傳 error，狀態不變，也不寫成功 audit；後端不接受 canonical 別名；`Pending` → `Confirmed` 寫一筆 `workspace_confirmed`，`Confirmed`／`NotRequired` 冪等成功、不再寫 audit。
14. R3 Invariant：`ConfirmWorkspace` 不能設定或更改 workspace，也不接受由前端提供的 confirmed、reason 等欄位；前端必須傳入 `status.workspace` 的原始字串，不可傳縮短的路徑、改寫成 `~` 的路徑，或任何顯示用的字串。
15. R4：確認只存在於這一次 App instance 的記憶體中；重啟後依 R1 重新判定，不從 audit、registry、resume 或任何檔案恢復；同一個 App 內的所有 session 與 Assist 共用同一個確認。
16. audit 在**不持有** `workspaceConfirm` mutex 的情況下寫入；「是否為第一次確認」由一次不可分割的狀態轉換決定；沿用 `a.audit`（best-effort），audit 寫入成功不是放行的條件；查詢 status 不寫 audit。
17. `workspace_confirmed` 記錄 `{workspace, source, reasons}`；`workspace_confirmation_blocked` 記錄 `{workspace, source, reasons, op, provider?, wsid?}`，不包含 prompt；既有 startup audit 不變。
18. 「確認的狀態轉換完成之後，啟動才可能通過」必須以行為斷言驗證，不能只靠 `-race`。
19. R5：`required && !confirmed` 時，頂部列顯示高風險警示——使用 `--err` 樣式並設 `role="alert"`，內容包含實際目錄、source 與原因，並提供「確認在此目錄進行 AI 工作」按鈕；送出訊息與 Assist 的操作在此期間停用；**尚未取得 status、status 查詢失敗**時也不可送出，不能只判斷 `required && !confirmed`。
20. R5 Invariant：元件重新掛載時不會自動確認；晚到的舊回應不會覆蓋較新的狀態（沿用 `refreshCliInfo` 的 request sequencing）；「取消」只關閉這次確認操作，不改變確認狀態；頂部列的 `ws:` 顯示維持不變；資料取得順序為先等 CLIInfo ready，再查詢 `WorkspaceConfirmationStatus`。
21. i18n：`zh-TW.ts` 與 `en.ts` 同時新增 key，並通過 parity、keyRefs 等守門測試。
22. `frontend/wailsjs` 的產生檔以 `wails generate module` 重新產生後提交，做法比照 A5：只提交內容差異，三個 runtime 檔（`runtime/package.json`、`runtime.d.ts`、`runtime.js`）的權限副作用要還原成 644。**唯一的空白例外**：Wails 產生的 `frontend/wailsjs/go/models.ts` 中，新類別 `WorkspaceConfirmationInfo` 內有兩行只含一個 tab 的空行（`git diff --check` 會報 trailing whitespace）——這是產生器的原樣輸出，**原樣提交、不得手動修改**，未來重新產生才不會出現差異；收尾的 `git diff --check` 只容許這兩項（見 Task 8 Step 3），其餘任何空白警告（手寫程式、其他檔案、`models.ts` 的其他位置）必須為 0。不得用 `.gitattributes`、`core.whitespace` 或 `-c` 參數整檔忽略空白，也不得格式化整份 `models.ts`。
23. 既有測試 `TestProductionResolverCreatesOnlyEmptyStateDirBeforeLease`、`TestNoWriterOpensBeforeLeaseIsAcquired`、`CLIInfo` 系列測試、Assist 的 root＋purpose 形狀測試（`TestAssistImplementationsTakeRootContextFirst`），**都必須不修改就能通過**；若必須修改，先停下來回報。
24. 非目標：不改 App 自己對 workspace 的寫入時機、不做啟動時 picker、不允許執行期更換 workspace、不建立檔案沙箱、不持久化確認、Codex 登入與帳號狀態查詢（`AuthStatus`、`StartLogin`）不在閘門範圍內；owner 裁定（2026-10-11）`Logout`、`RestartCodexServerRecorded`、`RecoverCodexRecording` 同樣**不加閘門**，但須與登入一樣以 fake app-server 驗證不會送出 `thread/start`、`turn/start`。
25. 驗證指令：Go 用 `GOTOOLCHAIN=local PATH=$HOME/sdk/go1.26.5/bin:$PATH go test -race ./... -count=1`，加上 `gofmt -l .` 與 `go vet ./...`（一律以 `run_logged` 保存完整輸出與 rc）；前端在 `frontend` 目錄用 `npm run build`（含 vue-tsc）與 vitest，node 路徑 `export PATH=$HOME/.nvm/versions/node/v26.8.1/bin:$PATH`。
26. 首次紅燈要保留，不得重跑到綠燈後掩蓋；失敗要先分類（真實缺陷／fixture 前提／環境）再處理——**分類要追責任歸屬，不能只憑錯誤字串**；每個 focused 測試、mutation 與完整 gate 都先把完整輸出與測試行程本身的 rc 存檔（`run_logged`），之後的 grep 只讀已存好的 log；不使用全域 `ps`，不寫 `/tmp`，不碰 owner 的主工作區，不推送。

---

## Review Focus

spec 隱含、但 spec 自己的測試清單沒有明列、又最可能出錯的 5 項（依可能性排序；每一項都已指派到負責 task 並附測試）：

1. **從沒解析過 workspace 的 App（零值）必須 fail closed，而且 `ConfirmWorkspace("")` 不得把它確認掉。** 空字串的 `expectedPath` 恰好「等於」空的快照；若零值被當成 `NotRequired`，任何沒走 `acquireStateLease` 的啟動路徑都會靜默放行。→ Task 2 `TestWorkspaceConfirmation_UnresolvedFailsClosed`；production 接線由 Task 2 `TestAcquireStateLeaseResolvesWorkspaceConfirmation` 與 Task 3 `TestProductionStartupPath_*` 守住。
2. **`expectedPath` 的字串比對陷阱**：尾端斜線、大小寫（macOS 預設不分大小寫的檔案系統）、`~` 簡寫、symlink 別名、NFC／NFD 組合字元，都必須被拒；含空白、中日文與 NFD 的原始字串則必須原樣通過。→ Task 2 `TestConfirmWorkspace_PathMismatchRejectedInEveryState`、`TestConfirmWorkspace_RawStringRoundTripWithUnicodePath`；前端原樣轉發由 Task 7 `workspaceConfirmation.test.ts`、`bindings.test.ts` 守住。
3. **既有檢查的錯誤優先順序不得改變**：未知 wsid 仍回 `ErrSessionNotFound`；`startSession` 的 `registryUncertain` 仍先於確認閘，且沒有被加到 `sendMessage`；未知 provider 仍回 `unknown provider`；`planAssist` 的確認閘早於 `ensureGate`。→ Task 3 `TestGate_PreservesExistingErrorPrecedence`、Task 4 兩個 Assist 測試。
4. **前端 fail closed 的邊角**：壞掉的回覆（`{}`、缺 `required`／`source`、`reasons` 缺少／`null`／非陣列／含非字串元素）不得解除封鎖，**也不得被補成空值或用 `String()` 轉成合法資料**——parser 驗證完整 DTO 形狀，錯了就丟錯，由 refresh 轉成 failed；從已放行狀態收到壞回覆要回到 failed 且 `blocked=true`；先前已確認、之後查詢失敗要重新封鎖；確認成功但重新查詢失敗仍封鎖；連點確認只送一次；被封鎖時按 Enter 不得清掉輸入框的草稿；`runAssist` 自己也要擋（不只靠 disabled 按鈕）。→ Task 7 `workspaceConfirmation.test.ts`（`BAD` 表的 15 種壞回覆各有 parser、尚未取得、已放行三個層級的負例）、`workspaceGate.test.ts`。
5. **被拒絕的請求不得污染之後的啟動**：拒絕後確認，同一個 session 的啟動必須成功（Manager 沒有洩漏 `ErrSessionActive`／`ErrSubmitActive`）；resume 案例中，拒絕不得把已結束 session 的 reducer 從 `done` 重設成 `idle`（偷呼叫 `BeginNewSessionSubmit` 再 `RejectSubmit` 的實作會被抓到）；Assist 拒絕後同一 provider 不得出現 `ErrAssistActive`，且本次 context 必須已被取消。→ Task 3 `TestStartSessionGate_*`、Task 4 `TestSpecAssistGate_*`／`TestPlanAssistGate_*`。

---

## 任務切分與估點

| Task | 內容 | spec 對應 | 估時 |
|---|---|---|---|
| 0 | 建立隔離 worktree、證據目錄、`frontend/dist` 佔位、基準測量 | §6、紀律 | （不計工時） |
| 1 | resolver provenance＋seam＋純函式 policy | §5.1、R1 | 2.0 hr |
| 2 | 確認狀態＋兩個 binding＋surface／lifecycle 測試＋wailsjs 產生檔 | §5.2、§5.3、R3、R4 | 1.7 hr |
| 3 | 閘門函式＋`startSession`／`sendMessage` | §5.4、R2 | 1.3 hr |
| 4 | `specAssist`／`planAssist` 閘門 | §5.4 固定順序 | 1.3 hr |
| 5 | audit 事件＋並行行為測試 | §5.5、§6.1 | 0.7 hr |
| 6 | Codex 登入邊界驗證（H3）＋`Logout`／`RestartCodexServerRecorded`／`RecoverCodexRecording`（owner 裁定加入） | §5.4 末段、owner 裁定 | 0.6 hr |
| 7 | 前端：store、警示元件、停用條件、i18n、adapter／mock | §5.6、R5、§6.2 | 3.0 hr |
| 8 | README、feature 檔、backlog 收尾、E2E 決定、完整 gate | §5.7、§6.3 | 0.9 hr |
| **合計** | | | **11.5 hr（約 1.15 pt）** |

**與 spec §8 的核對**：spec 合計約 9.5 hr（resolver 2.0／狀態＋binding 1.5／閘門 2.5／audit＋登入 1.0／前端 2.0／文件 0.5）。本計畫 11.5 hr，多 2.0 hr，**+21.1%，超過 20% 門檻**，原因如下：（a）前端 +1.0 hr——spec 的 2.0 hr 沒有計入「需要讓既有 5 個測試檔補確認狀態前提、4 個測試檔補新 binding 的 mock、新增 store 與獨立警示元件、約 99 個新測試」；（b）狀態＋binding +0.2 hr——production 接線測試與 Unicode／別名路徑測試；（c）文件 +0.4 hr——新增 `workspace-confirmation.feature` 與 E2E 取捨決定；（d）閘門＋audit＋登入 +0.1 hr；（e）**+0.2 hr 來自 owner 在審閱計畫時追加的範圍**（2026-10-11 14:53：`Logout`、`RestartCodexServerRecorded`、`RecoverCodexRecording` 納入 Task 6 的 fake app-server 驗證），這一項不在 spec §8 的原估內。若扣除 (e)，是 11.3 hr、+18.9%，在門檻內。完整 Go gate（`-race`，實測約 4–7 分鐘）與 vitest（約 2 分鐘）屬於等候時間，不計工時。

---

## 檔案結構

| 檔案 | 動作 | 責任 |
|---|---|---|
| `workspace_confirmation.go` | 新增 | provenance／resolution 型別、`workspacePolicy`、確認狀態 `workspaceConfirmation`、`WorkspaceConfirmationInfo`、`requireWorkspaceConfirmed`（刻意獨立成檔，不再加大 9248 行的 `app.go`；兩個 exported binding 留在 `app.go`，沿用「Wails binding 留在 app.go」的既有約定） |
| `app.go` | 修改 | resolver seam（取代 `resolveWorkspace`）、`App.workspaceConfirm` 欄位、`acquireStateLease` 接線、兩個 binding、四個入口的閘門呼叫、`hookAssistGateRejected` |
| `app_test.go` | 修改（+1 行程式、+4 行註解） | `newTestAppIn` 夾具把 workspace 放進「已解析且不需確認」（見 Task 3） |
| `app_state_binding_lifecycle_test.go` | 修改 | 把兩個新 binding 加進「收尾之後一律被拒」的逐一列舉 |
| `workspace_*_test.go`（7 個） | 新增 | 本票的 Go 測試 |
| `frontend/wailsjs/go/main/App.{d.ts,js}`、`go/models.ts` | 重新產生 | 只提交內容差異 |
| `frontend/src/stores/workspaceConfirmation.ts` | 新增 | 確認狀態 store（request sequencing、fail closed） |
| `frontend/src/components/WorkspaceConfirmBanner.vue` | 新增 | 頂部列警示＋確認／取消流程 |
| `frontend/src/{App.vue,components/PaneView.vue,components/SpecWorkspace.vue,components/PlanWorkspace.vue,stores/session.ts,types.ts,lib/bindings.ts,i18n/*}` | 修改 | 接線與停用條件 |
| `docs/architecture/features/workspace-confirmation.feature`、`README.md`、backlog | 新增／修改 | 文件（Task 8） |

---

## 環境區塊 E（每個 Bash 呼叫開頭先執行；shell 狀態不會跨呼叫保留）

```bash
export WT=/Users/eason_tseng/b3a-evidence/b4/impl-wt
export EVID=/Users/eason_tseng/b3a-evidence/b4/impl-evidence
export GOTOOLCHAIN=local
export PATH=$HOME/sdk/go1.26.5/bin:$HOME/go/bin:$HOME/.nvm/versions/node/v26.8.1/bin:$PATH
cd $WT

# run_logged <name> <命令...>：完整輸出（stdout＋stderr）存進 $EVID/<name>.log，並「立即」把測試行程本身的
# 結束碼存進 $EVID/<name>.rc——中間不經管線，所以 rc 是測試行程的，不是 grep／head 的。
run_logged() { local n=$1; shift; "$@" > "$EVID/$n.log" 2>&1; local rc=$?; echo $rc > "$EVID/$n.rc"; return $rc; }
# show <name> [grep -E 樣式] [行數]：只讀已存好的 log／rc，不重跑。
show() { echo "[$1] rc=$(cat "$EVID/$1.rc")"; if [ -n "$2" ]; then grep -E -e "$2" "$EVID/$1.log" | head -${3:-20}; else tail -${3:-20} "$EVID/$1.log"; fi; }
# mutant_check <name> <目標失敗樣式> <命令...>：跑 mutant、完整輸出與 rc 先存檔（$EVID/<name>.log／.rc），
# 再確認 (1) 不是 build failed／timeout／解析失敗等非目標失敗，(2) rc≠0，(3) log 裡有預期的目標測試名稱或斷言。
mutant_check() {
  local n=$1 pat=$2; shift 2
  run_logged "$n" "$@"; local rc; rc=$(cat "$EVID/$n.rc")
  if grep -qE '\[build failed\]|panic: test timed out|Failed to resolve import|SyntaxError|Transform failed' "$EVID/$n.log"; then
    echo "$n: 非目標失敗（build／timeout／解析）rc=$rc"; return 2; fi
  if [ "$rc" != 0 ] && grep -qE -e "$pat" "$EVID/$n.log"; then echo "$n: 紅在目標（$pat）rc=$rc"; else echo "$n: 沒有紅在目標 rc=$rc"; return 1; fi
}
# restore_check <備份> <目標>：還原並 cmp。
restore_check() { cp "$1" "$2" && cmp "$1" "$2" && echo "restored: $2"; }
```

zsh 提醒（`zsh-shell-gotchas`）：不要用名為 `path` 的變數（會清掉 `$PATH`）；rc 一律用 `run_logged`（`cmd > log 2>&1; echo $? > rc`，不經管線），不要用 `PIPESTATUS`；變數後接冒號要寫 `${VAR}:`。所有證據 log 寫進 `$EVID`（非揮發目錄），不寫 `/tmp`。

---

## Task 0：隔離 worktree 與基準（不計工時）

**Files:** 無程式碼變更。

- [ ] **Step 1：建立實作 worktree 與證據目錄**

前提：本計畫檔已 commit 在 `docs/b4-workspace-confirmation-spec` 上（由 owner／主審決定 commit 時機）。基底 commit 記錄在 `$EVID/base.txt`。

```bash
mkdir -p /Users/eason_tseng/b3a-evidence/b4/impl-evidence
git -C /Users/eason_tseng/b3a-evidence/b4/spec-wt worktree add -b feat/b4-workspace-confirmation /Users/eason_tseng/b3a-evidence/b4/impl-wt docs/b4-workspace-confirmation-spec
# 之後每個 Bash 呼叫先執行環境區塊 E
git rev-parse HEAD > $EVID/base.txt && git status --short | head
```

Expected：`base.txt` 有一個 commit；`git status --short` 無輸出。

- [ ] **Step 2：`frontend/dist` 佔位與前端依賴**

`main.go:22` 的 `//go:embed all:frontend/dist` 要求 `frontend/dist` 存在，否則任何 `go build`／`go vet`／`go test`／`wails generate module` 都會以 `pattern all:frontend/dist: no matches found` 失敗（A5 驗證紀錄首次嘗試就是這樣失敗）。`frontend/dist/` 在 `.gitignore` 內，佔位檔不會被提交。

```bash
mkdir -p frontend/dist && printf '<!doctype html>\n' > frontend/dist/index.html
git status --short            # Expected：無輸出（dist 被 gitignore）
npm --prefix frontend ci > $EVID/npm-ci.log 2>&1; echo "rc=$?" >> $EVID/npm-ci.log; tail -3 $EVID/npm-ci.log
```

Expected：rc=0。（`npm ci` 只寫 `frontend/node_modules`，同樣被 gitignore。）

- [ ] **Step 3：基準測量（未改任何程式碼）**

每個指令都用 `run_logged`：完整輸出先存進 `$EVID/<name>.log`、測試行程本身的 rc 立即存進 `$EVID/<name>.rc`，之後的 `show` 只讀已存好的檔。

```bash
run_logged baseline-gofmt gofmt -l . ; wc -c < $EVID/baseline-gofmt.log                  # Expected：0
run_logged baseline-vet go vet ./... ; show baseline-vet                                  # Expected：rc=0
run_logged baseline-go go test -race ./... -count=1 -timeout 30m
show baseline-go '^(--- FAIL|FAIL|panic|WARNING: DATA RACE)'; grep -cE '^ok' $EVID/baseline-go.log
(cd frontend && run_logged baseline-vitest npx vitest run); show baseline-vitest 'Test Files|Tests '
```

Expected（計畫撰寫時在相同原始碼的複本上量到）：vet rc=0；根 package `ok`，約 4–7 分鐘；vitest `44 passed`／`605 passed`。**若基準就有紅燈**：先對照 `docs/architecture/wall-clock-test-register.md`（例如 `internal/codex` 的 `TestAppServerMidStreamDeath` 是登記在案的牆鐘測試，計畫撰寫時的全套 `-race` 在同時有其他負載時曾失敗一次，單獨重跑 `-count=5` 全過）分類，**不得重跑到綠燈後掩蓋**，把分類與 log 寫進 `$EVID/baseline-notes.txt`。

---

## Task 1：resolver provenance＋seam＋純函式 policy

**為什麼這樣切：** 這一塊是純計算（只多 `os.Stat`），沒有任何依賴，可以單獨交付、單獨 review；後面所有 task 都消費它的型別。

**Files:**
- Create: `workspace_confirmation.go`（本 task 只放 provenance、resolution 型別與 `workspacePolicy`）
- Modify: `app.go:3235-3278`（`resolveWorkspace` 換成帶 seam 的版本）、`app.go:2108-2110`（`acquireStateLease` 的解構）
- Test: `workspace_policy_test.go`、`workspace_resolver_test.go`（新增）

**Interfaces:**
- Consumes: `claude.NormalizeCWD`（`internal/claude/session.go:51`，內含 `filepath.Abs` 與 `filepath.EvalSymlinks`）。
- Produces（Task 2 起依賴）：
  - `type workspaceEnv struct{ Getenv func(string) string; Getwd func() (string, error); HomeDir func() (string, error); TempDir func() string; Normalize func(string) (string, error); MkdirAll func(string, os.FileMode) error; Stat func(string) (os.FileInfo, error) }`
  - `func productionWorkspaceEnv() workspaceEnv`
  - `type workspaceProvenance struct{ EnvSet, EnvRejected bool; EnvRejectReason string; IsHome, HomeIdentityKnown bool }`
  - `type workspaceResolution struct{ Workspace, State, Source string; Err error; Provenance workspaceProvenance }`
  - `func resolveWorkspace() workspaceResolution`、`func resolveWorkspaceWith(e workspaceEnv) workspaceResolution`
  - `func workspacePolicy(source string, p workspaceProvenance) (required bool, reasons []string)`
  - 原因代碼常數 `wsReasonSourceHome`／`wsReasonSourceTmp`／`wsReasonEnvRejected`／`wsReasonEnvRejectedNormalize`／`wsReasonEnvRejectedMkdir`／`wsReasonIsHome`／`wsReasonHomeUnknown`；`envRejectNormalizeFailed`／`envRejectMkdirFailed`

- [ ] **Step 1：寫失敗測試**

新增 `workspace_policy_test.go`（R1 純函式；(a)～(d) 各一個正例、家目錄子目錄反例、多原因順序）：

```go
package main

import (
	"reflect"
	"testing"
)

// ---- B4 R1：workspacePolicy（純函式）----

func knownHome() workspaceProvenance { return workspaceProvenance{HomeIdentityKnown: true} }

func TestWorkspacePolicy(t *testing.T) {
	cases := []struct {
		name     string
		source   string
		prov     workspaceProvenance
		required bool
		reasons  []string
	}{
		{"env 正常、身分可判定：不需確認", "env", knownHome(), false, []string{}},
		{"cwd 正常：不需確認（wails dev、從專案目錄啟動）", "cwd", knownHome(), false, []string{}},
		{"(a) source=home", "home", knownHome(), true, []string{wsReasonSourceHome}},
		{"(a) source=tmp", "tmp", knownHome(), true, []string{wsReasonSourceTmp}},
		{"(b) env 無效退回 cwd（normalize 失敗）", "cwd",
			workspaceProvenance{EnvSet: true, EnvRejected: true, EnvRejectReason: envRejectNormalizeFailed, HomeIdentityKnown: true},
			true, []string{wsReasonEnvRejectedNormalize}},
		{"(b) env 無效退回 cwd（mkdir 失敗）", "cwd",
			workspaceProvenance{EnvSet: true, EnvRejected: true, EnvRejectReason: envRejectMkdirFailed, HomeIdentityKnown: true},
			true, []string{wsReasonEnvRejectedMkdir}},
		{"(b) 未知的拒絕原因仍算被拒", "cwd",
			workspaceProvenance{EnvSet: true, EnvRejected: true, HomeIdentityKnown: true},
			true, []string{wsReasonEnvRejected}},
		{"(b) 必須 envSet 與 envRejected 同時成立", "cwd",
			workspaceProvenance{EnvRejected: true, EnvRejectReason: envRejectMkdirFailed, HomeIdentityKnown: true},
			false, []string{}},
		{"(c) isHome 與 source 無關（env=$HOME）", "env",
			workspaceProvenance{EnvSet: true, IsHome: true, HomeIdentityKnown: true},
			true, []string{wsReasonIsHome}},
		{"(c) cd ~ 後啟動（source=cwd）", "cwd",
			workspaceProvenance{IsHome: true, HomeIdentityKnown: true},
			true, []string{wsReasonIsHome}},
		{"(d) 家目錄身分無法判定：即使 env 正常也要確認", "env",
			workspaceProvenance{EnvSet: true}, true, []string{wsReasonHomeUnknown}},
		{"家目錄底下的專案子目錄：isHome=false、身分可判定 → 不需確認（Invariant）", "env",
			workspaceProvenance{EnvSet: true, IsHome: false, HomeIdentityKnown: true}, false, []string{}},
		{"多個原因同時成立：全部列出、順序固定 a→b→c→d", "home",
			workspaceProvenance{EnvSet: true, EnvRejected: true, EnvRejectReason: envRejectNormalizeFailed, IsHome: true},
			true, []string{wsReasonSourceHome, wsReasonEnvRejectedNormalize, wsReasonIsHome, wsReasonHomeUnknown}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			required, reasons := workspacePolicy(c.source, c.prov)
			if required != c.required {
				t.Fatalf("required=%v，want %v（reasons=%v）", required, c.required, reasons)
			}
			if !reflect.DeepEqual(reasons, c.reasons) {
				t.Fatalf("reasons=%#v，want %#v", reasons, c.reasons)
			}
		})
	}
}
```

新增 `workspace_resolver_test.go`（隔離目錄＋真實 `os.Stat`；涵蓋 spec §6.1 的 resolver 清單與 H1 symlink、(d) 三種無法判定、取鎖前只建空 state dir）：

```go
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
```

- [ ] **Step 2：跑一次確認失敗（保留首次紅燈）**

```bash
run_logged t1-red go test -run 'TestWorkspacePolicy|TestResolveWorkspaceWith' -count=1 .
show t1-red '(\[build failed\]|undefined)' 12
```

Expected：rc=1，`[build failed]`，錯誤為 `undefined: workspaceProvenance`／`workspacePolicy`／`workspaceEnv`／`resolveWorkspaceWith`。這是「符號尚未存在」的紅燈（預期的紅，不是環境問題）。

- [ ] **Step 3：最小實作**

(3a) 新增 `workspace_confirmation.go`（本 task 的內容；此時沒有任何 import）：

```go
package main

// ---- B4：workspace 確認閘（spec docs/superpowers/specs/2026-10-11-b4-workspace-confirmation-design.md）----

// env 候選被拒的有限原因分類（spec §5.1）。
const (
	envRejectNormalizeFailed = "normalize_failed"
	envRejectMkdirFailed     = "mkdir_failed"
)

// workspaceProvenance：解析過程的附帶事實（spec §2.1）。
type workspaceProvenance struct {
	EnvSet            bool   // WORKBENCH_WORKSPACE 非空
	EnvRejected       bool   // env 候選被拒絕而退回其他候選
	EnvRejectReason   string // envReject*；EnvRejected 為 false 時為空
	IsHome            bool   // 實際 workspace 與家目錄是同一個目錄（os.SameFile）
	HomeIdentityKnown bool   // 家目錄與 workspace 的身分都能判定
}

// noteRejected：只有 env 候選被拒才記錄（其他來源被拒是既有的正常退回）。
func (p *workspaceProvenance) noteRejected(source, reason string) {
	if source != "env" {
		return
	}
	p.EnvRejected = true
	p.EnvRejectReason = reason
}

// workspaceResolution：resolveWorkspace 的結果。前四個欄位與 B4 之前的四個回傳值一一
// 對應；Provenance 是 B4 新增。
type workspaceResolution struct {
	Workspace  string
	State      string
	Source     string
	Err        error
	Provenance workspaceProvenance
}

// 確認原因代碼（spec R1 (a)–(d)）。前端以這些字串查 i18n，新增或改名要同步
// frontend/src/i18n/stateKeys.ts 的 workspaceReasonKeys。
const (
	wsReasonSourceHome           = "source_home"
	wsReasonSourceTmp            = "source_tmp"
	wsReasonEnvRejected          = "env_rejected"
	wsReasonEnvRejectedNormalize = "env_rejected_normalize_failed"
	wsReasonEnvRejectedMkdir     = "env_rejected_mkdir_failed"
	wsReasonIsHome               = "is_home"
	wsReasonHomeUnknown          = "home_identity_unknown"
)

// workspacePolicy：R1 的純函式——輸入 source 與 provenance，輸出是否需要確認與所有成立
// 的原因（固定順序 a→b→c→d）。不碰檔案系統、不讀全域狀態，可單獨測試。
func workspacePolicy(source string, p workspaceProvenance) (required bool, reasons []string) {
	reasons = []string{}
	switch source {
	case "home":
		reasons = append(reasons, wsReasonSourceHome)
	case "tmp":
		reasons = append(reasons, wsReasonSourceTmp)
	}
	if p.EnvSet && p.EnvRejected {
		switch p.EnvRejectReason {
		case envRejectNormalizeFailed:
			reasons = append(reasons, wsReasonEnvRejectedNormalize)
		case envRejectMkdirFailed:
			reasons = append(reasons, wsReasonEnvRejectedMkdir)
		default:
			reasons = append(reasons, wsReasonEnvRejected)
		}
	}
	if p.IsHome {
		reasons = append(reasons, wsReasonIsHome)
	}
	if !p.HomeIdentityKnown {
		reasons = append(reasons, wsReasonHomeUnknown)
	}
	return len(reasons) > 0, reasons
}
```

(3b) `app.go`：把 `resolveWorkspace` 整個函式（從 `func resolveWorkspace() (workspace, state, source string, err error) {` 到緊接 `// stateSubdirs：…` 之前的結尾大括號，base 行 3246–3278）換成下列內容；函式上方既有的 doc 注解（3235–3245）保留。成功候選不回傳先前候選的錯誤、tmp fallback 仍回 `os.TempDir()` 本身，這兩點與舊版逐字相同：

```go
func resolveWorkspace() workspaceResolution {
	return resolveWorkspaceWith(productionWorkspaceEnv())
}

// workspaceEnv：resolveWorkspace 的環境輸入 seam（B4 spec §5.1）。production 用
// productionWorkspaceEnv() 的真實實作；測試注入隔離目錄與故障，不碰真實的家目錄、
// 也不依賴 chmod。Stat 必須回傳 os.Stat 的真實 FileInfo（os.SameFile 只認得真實
// 檔案系統的 FileInfo，自製假物件只會回 false）。
type workspaceEnv struct {
	Getenv    func(key string) string
	Getwd     func() (string, error)
	HomeDir   func() (string, error)
	TempDir   func() string
	Normalize func(dir string) (string, error)
	MkdirAll  func(path string, perm os.FileMode) error
	Stat      func(path string) (os.FileInfo, error)
}

func productionWorkspaceEnv() workspaceEnv {
	return workspaceEnv{
		Getenv: os.Getenv, Getwd: os.Getwd, HomeDir: os.UserHomeDir, TempDir: os.TempDir,
		Normalize: claude.NormalizeCWD, MkdirAll: os.MkdirAll, Stat: os.Stat,
	}
}

// resolveWorkspaceWith：resolveWorkspace 的本體。候選順序、判準與回傳的 workspace／
// state／source／err 與 B4 之前逐字相同（成功候選不回傳先前候選的錯誤；tmp fallback
// 的 workspace 仍是 TempDir 本身），新增的只有 provenance——而且 provenance 的判定只做
// Stat，不新增任何寫入。
func resolveWorkspaceWith(e workspaceEnv) workspaceResolution {
	type cand struct{ dir, src string }
	var cands []cand
	var prov workspaceProvenance
	if d := e.Getenv("WORKBENCH_WORKSPACE"); d != "" {
		prov.EnvSet = true
		cands = append(cands, cand{d, "env"})
	}
	if cwd, cerr := e.Getwd(); cerr == nil && cwd != "/" {
		cands = append(cands, cand{cwd, "cwd"})
	}
	if home, herr := e.HomeDir(); herr == nil {
		cands = append(cands, cand{home, "home"})
	}
	var lastErr error
	for _, c := range cands {
		n, nerr := e.Normalize(c.dir)
		if nerr != nil {
			lastErr = nerr
			prov.noteRejected(c.src, envRejectNormalizeFailed)
			continue
		}
		st := filepath.Join(n, ".workbench")
		if merr := e.MkdirAll(st, 0o755); merr != nil {
			lastErr = merr
			prov.noteRejected(c.src, envRejectMkdirFailed)
			continue
		}
		prov.IsHome, prov.HomeIdentityKnown = workspaceIsHome(e, n)
		return workspaceResolution{Workspace: n, State: st, Source: c.src, Provenance: prov}
	}
	tmp := e.TempDir()
	st := filepath.Join(tmp, "sdlc-workbench", ".workbench")
	prov.IsHome, prov.HomeIdentityKnown = workspaceIsHome(e, tmp)
	if merr := e.MkdirAll(st, 0o755); merr != nil {
		return workspaceResolution{Workspace: tmp, State: st, Source: "tmp",
			Err: errors.Join(lastErr, merr), Provenance: prov}
	}
	return workspaceResolution{Workspace: tmp, State: st, Source: "tmp", Err: lastErr, Provenance: prov}
}

// workspaceIsHome：實際 workspace 是否就是家目錄（B4 spec §5.1）。對家目錄與 workspace
// 各做一次 Stat（會跟隨 symlink），再用 os.SameFile 比目錄身分——不比路徑字串，所以家目錄
// 底下的子目錄不會因為字串前綴相同被誤判。任何一步失敗都回 known=false（R1 (d)）。
func workspaceIsHome(e workspaceEnv, workspace string) (isHome, known bool) {
	home, herr := e.HomeDir()
	if herr != nil || home == "" {
		return false, false
	}
	homeInfo, serr := e.Stat(home)
	if serr != nil {
		return false, false
	}
	wsInfo, serr := e.Stat(workspace)
	if serr != nil {
		return false, false
	}
	return os.SameFile(homeInfo, wsInfo), true
}
```

(3c) `app.go` `acquireStateLease`（base 行 2108–2110）：舊版

```go
	if a.stateDir == "" {
		a.workspaceDir, a.stateDir, a.workspaceSrc, a.workspaceErr = resolveWorkspace()
	}
```

改成

```go
	if a.stateDir == "" {
		r := resolveWorkspace()
		a.workspaceDir, a.stateDir, a.workspaceSrc, a.workspaceErr = r.Workspace, r.State, r.Source, r.Err
	}
```

- [ ] **Step 3.5：確認沒有其他呼叫端**

```bash
grep -rn "resolveWorkspace(" --include='*.go' . | grep -v "^./app.go"      # Expected：只剩註解，沒有程式呼叫
go build ./... && gofmt -l .                                              # Expected：無輸出
```

- [ ] **Step 4：跑測試確認通過，並確認受保護的既有測試不修改就通過**

```bash
run_logged t1-green go test -run 'TestWorkspacePolicy|TestResolveWorkspaceWith' -count=1 -v .
show t1-green '^(--- |ok|FAIL)' 40
run_logged t1-protected go test -run 'TestProductionResolverCreatesOnlyEmptyStateDirBeforeLease|TestNoWriterOpensBeforeLeaseIsAcquired|CLIInfo|TestEveryBindingIsClassified|TestOnlyThinWrappersAdmit|TestAssistImplementationsTakeRootContextFirst' -count=1 -v .
show t1-protected '^(--- |ok|FAIL)' 40
run_logged t1-vet go vet . ; show t1-vet
git diff --stat -- '*_test.go'   # Expected：空（測試檔沒有被修改；新增的 workspace_*_test.go 尚未追蹤）
```

Expected：兩組 rc=0、全部 PASS（計畫撰寫時在複本上，受保護測試那一組有 10 條 `--- PASS`）。

- [ ] **Step 5：Mutation 證據（測試要紅在正題）**

每個 mutant 都有自己的來源備份（`$EVID/t1-mN.orig`）、log（`$EVID/t1-mN.log`）、rc（`$EVID/t1-mN.rc`），以及還原後的 `cmp`。`mutant_check` 會確認失敗的是**目標測試**，並排除 build failed／timeout 等非目標失敗；非目標失敗要先處理，不得進下一個 mutant。

```bash
# M1：(d) 反向——身分無法判定時當成已知
cp workspace_confirmation.go $EVID/t1-m1.orig
sed -i '' 's/if !p.HomeIdentityKnown {/if false {/' workspace_confirmation.go
mutant_check t1-m1 '^--- FAIL: (TestWorkspacePolicy|TestResolveWorkspaceWith_HomeIdentityUnknown)' go test -run 'TestWorkspacePolicy|TestResolveWorkspaceWith' -count=1 .
restore_check $EVID/t1-m1.orig workspace_confirmation.go

# M2：isHome 改用路徑前綴比對（Invariant 的反例）
cp app.go $EVID/t1-m2.orig
python3 - <<'EOF'
s = open('app.go', encoding='utf-8').read()
old = "return os.SameFile(homeInfo, wsInfo), true"
assert old in s
open('app.go', 'w', encoding='utf-8').write(s.replace(old, "return strings.HasPrefix(workspace, home) && homeInfo != nil && wsInfo != nil, true", 1))
EOF
mutant_check t1-m2 '^--- FAIL: TestResolveWorkspaceWith_ProjectUnderHomeIsNotHome' go test -run 'TestResolveWorkspaceWith' -count=1 .
restore_check $EVID/t1-m2.orig app.go
```

Expected：兩個 `mutant_check` 都印出「紅在目標」。M1 → `TestWorkspacePolicy`（`(d)` 與多原因案例）與 `HomeIdentityUnknown`；M2 → `ProjectUnderHomeIsNotHome`（路徑前綴相同但不是同一個目錄）。印出「非目標失敗」或「沒有紅在目標」就停下來回報（測試失效或 mutant 寫壞）。

- [ ] **Step 6：Commit**

```bash
git add workspace_confirmation.go workspace_policy_test.go workspace_resolver_test.go app.go
git commit -m "$(cat <<'EOF'
feat(b4): resolver provenance＋環境 seam＋R1 純函式 policy（Task 1）

resolveWorkspace 改回傳 workspaceResolution，附帶 provenance（envSet／envRejected／
isHome／homeIdentityKnown）；環境輸入走 workspaceEnv seam，isHome 以真實 os.Stat＋os.SameFile
判定目錄身分。候選順序、tmp fallback 與取鎖前只建空 state dir 的契約不變。

Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2：確認狀態＋兩個 binding＋wailsjs 產生檔

**為什麼這樣切：** 狀態機與 binding 是閘門與前端共同的依賴；先讓「狀態可以讀、可以確認」成立並通過 binding surface 守門，Task 3 才接閘門。

**Files:**
- Modify: `workspace_confirmation.go`（加 import 與確認狀態）
- Modify: `app.go:62`（`App` 加欄位）、`app.go:2108-2111`（`acquireStateLease` 接線）、`app.go:3507`（`CLIInfo` 之後、`// ---- workspace 檔案（canonical 邊界）----` 之前加兩個 binding）
- Modify: `app_state_binding_lifecycle_test.go`（`calls` 清單 +2 項）
- Test: `workspace_confirm_test.go`（新增）
- Regenerate: `frontend/wailsjs/go/main/App.d.ts`、`App.js`、`go/models.ts`

**Interfaces:**
- Consumes（Task 1）：`workspaceResolution`、`workspacePolicy`、`wsReason*`。
- Produces（Task 3 起依賴）：
  - `type workspaceConfirmState int`，常數 `wcUnresolved`（零值，fail closed）、`wcNotRequired`、`wcPending`、`wcConfirmed`
  - `type workspaceConfirmation struct{ mu sync.Mutex; state workspaceConfirmState; workspace, source string; reasons []string }`
  - `func (c *workspaceConfirmation) resolve(r workspaceResolution)`、`snapshot() workspaceConfirmSnapshot`、`confirm(expectedPath string) (transitioned bool, snap workspaceConfirmSnapshot, err error)`
  - `type workspaceConfirmSnapshot struct{ state workspaceConfirmState; workspace, source string; reasons []string }` 與 `func (s workspaceConfirmSnapshot) info() WorkspaceConfirmationInfo`
  - `type WorkspaceConfirmationInfo struct{ Required, Confirmed bool; Workspace, Source string; Reasons []string }`（json：`required`／`confirmed`／`workspace`／`source`／`reasons`）
  - 錯誤：`errWorkspaceConfirmationRequired`、`errWorkspaceConfirmPathMismatch`、`errWorkspaceConfirmUnavailable`；常數 `wsReasonUnresolved`
  - `App.workspaceConfirm workspaceConfirmation`；`func (a *App) workspaceConfirmationStatus() (WorkspaceConfirmationInfo, error)`、`confirmWorkspace(expectedPath string) error`
  - binding：`func (a *App) WorkspaceConfirmationStatus() (WorkspaceConfirmationInfo, error)`、`func (a *App) ConfirmWorkspace(expectedPath string) error`（薄包裝要求最後一個回傳值是 `error`，所以 status binding 回 `(T, error)`；lifecycle 拒絕時 TS 端會 reject）
  - 測試輔助：`pendingResolution(ws)`、`notRequiredResolution(ws)`、`makePending(a)`、`makeNotRequired(a)`、`mustStatus(t, a)`

- [ ] **Step 1：寫失敗測試**

新增 `workspace_confirm_test.go`（零值 fail closed、路徑不符在三種狀態都被拒、冪等、Unicode 原樣往返、新 instance 回 Pending、lifecycle、binding surface／非 startup-safe、`CLIInfo` 形狀不變、production 接線）：

```go
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
```

並修改 `app_state_binding_lifecycle_test.go`（把兩個新 binding 加進「收尾之後一律被拒」的逐一列舉，該測試註解要求新增 binding 時必須補上）：

```diff
--- a/app_state_binding_lifecycle_test.go
+++ b/app_state_binding_lifecycle_test.go
@@ -85,6 +85,9 @@
 		{"PreviewAnalysisBaseBump", func() error { _, err := a.PreviewAnalysisBaseBump("p.md", ""); return err }},
 		{"ConfirmAnalysisBaseBump", func() error { _, err := a.ConfirmAnalysisBaseBump(BumpToken{}, "p.md", ""); return err }},
 		{"PlanAssist", func() error { _, err := a.PlanAssist("claude", "x"); return err }},
+		// B4：確認狀態雖然只在記憶體，兩個 binding 仍是 state binding——收尾之後一律被拒。
+		{"WorkspaceConfirmationStatus", func() error { _, err := a.WorkspaceConfirmationStatus(); return err }},
+		{"ConfirmWorkspace", func() error { return a.ConfirmWorkspace("whatever") }},
 	}
 	evidenceBefore := ""
 	if _, serr := os.Stat(filepath.Join(a.stateDir, "evidence", "evidence.jsonl")); serr == nil {
```

- [ ] **Step 2：跑一次確認失敗（保留首次紅燈）**

```bash
run_logged t2-red go test -run 'TestWorkspaceConfirmation|TestConfirmWorkspace|TestWorkspaceBindings|TestCLIInfoShapeUnchanged|TestAcquireStateLeaseResolves|TestStateBindingsRejectedAfterShutdown' -count=1 .
show t2-red '(\[build failed\]|undefined)' 12
```

Expected：rc=1，`[build failed]`：`a.workspaceConfirm undefined`、`a.WorkspaceConfirmationStatus undefined`、`undefined: wsReasonUnresolved` 等。

- [ ] **Step 3：最小實作**

(3a) `workspace_confirmation.go`：在 `package main` 與第一段注解之間加入 import，並在檔尾附加下列「確認狀態」區段：

```go
import (
	"errors"
	"fmt"
	"sync"
)
```

```go
// ---- 確認狀態（spec §3.1、§5.2）----

type workspaceConfirmState int

const (
	// wcUnresolved：零值——workspace 還沒解析過。fail closed：閘門拒絕、ConfirmWorkspace
	// 也不接受（否則空字串的 expectedPath 會「符合」空的快照）。
	wcUnresolved workspaceConfirmState = iota
	wcNotRequired
	wcPending
	wcConfirmed
)

const wsReasonUnresolved = "workspace_unresolved"

// 三種拒絕原因都以固定前綴開頭，呼叫端（前端、audit、測試）可以用前綴辨識。
var (
	errWorkspaceConfirmationRequired = errors.New("workspace_confirmation_required")
	errWorkspaceConfirmPathMismatch  = errors.New("workspace_confirmation_path_mismatch")
	errWorkspaceConfirmUnavailable   = errors.New("workspace_confirmation_unavailable")
)

// workspaceConfirmation：確認狀態＋對應的 workspace 快照，受自己的 mutex 保護。
// 鎖內只有欄位讀寫與字串／slice 複製，不做 I/O、不呼叫 a.audit（audit 一律在鎖外寫，
// 避免鎖序問題，也讓「是否為第一次確認」只由 confirm 的單一臨界區決定）。
type workspaceConfirmation struct {
	mu        sync.Mutex
	state     workspaceConfirmState
	workspace string
	source    string
	reasons   []string
}

type workspaceConfirmSnapshot struct {
	state     workspaceConfirmState
	workspace string
	source    string
	reasons   []string
}

// resolve：workspace 決定的當下計算 policy 並寫入（與 publishWorkspace 同一個時機）。
func (c *workspaceConfirmation) resolve(r workspaceResolution) {
	required, reasons := workspacePolicy(r.Source, r.Provenance)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.workspace, c.source, c.reasons = r.Workspace, r.Source, reasons
	if required {
		c.state = wcPending
	} else {
		c.state = wcNotRequired
	}
}

func (c *workspaceConfirmation) snapshot() workspaceConfirmSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return workspaceConfirmSnapshot{state: c.state, workspace: c.workspace, source: c.source,
		reasons: append([]string{}, c.reasons...)}
}

// confirm：R3。路徑核對與狀態轉換在同一個臨界區內完成：
//  1. 尚未解析 → 拒絕；
//  2. expectedPath 與快照的 workspace 字串不完全相同 → 拒絕（不論目前狀態），狀態不變；
//  3. 路徑相同：Pending → Confirmed（transitioned=true，只有這一次呼叫會拿到 true）；
//     Confirmed／NotRequired → 成功，狀態不變（transitioned=false）。
func (c *workspaceConfirmation) confirm(expectedPath string) (transitioned bool, snap workspaceConfirmSnapshot, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	snap = workspaceConfirmSnapshot{state: c.state, workspace: c.workspace, source: c.source,
		reasons: append([]string{}, c.reasons...)}
	if c.state == wcUnresolved {
		return false, snap, fmt.Errorf("%w：workspace 尚未解析完成", errWorkspaceConfirmUnavailable)
	}
	if expectedPath != c.workspace {
		return false, snap, fmt.Errorf("%w：畫面上的目錄與後端實際的 workspace（%s）不一致",
			errWorkspaceConfirmPathMismatch, c.workspace)
	}
	if c.state == wcPending {
		c.state = wcConfirmed
		snap.state = wcConfirmed
		return true, snap, nil
	}
	return false, snap, nil
}

// WorkspaceConfirmationInfo：WorkspaceConfirmationStatus 的回傳（Wails 會產生對應的 TS 型別）。
// NotRequired：required=false、confirmed=false；Pending：required=true、confirmed=false；
// Confirmed：required=true、confirmed=true。前端的停用條件是 required && !confirmed。
type WorkspaceConfirmationInfo struct {
	Required  bool     `json:"required"`
	Confirmed bool     `json:"confirmed"`
	Workspace string   `json:"workspace"`
	Source    string   `json:"source"`
	Reasons   []string `json:"reasons"`
}

func (s workspaceConfirmSnapshot) info() WorkspaceConfirmationInfo {
	reasons := s.reasons
	if s.state == wcUnresolved {
		reasons = []string{wsReasonUnresolved}
	}
	return WorkspaceConfirmationInfo{
		Required:  s.state != wcNotRequired,
		Confirmed: s.state == wcConfirmed,
		Workspace: s.workspace,
		Source:    s.source,
		Reasons:   append([]string{}, reasons...),
	}
}

func (a *App) workspaceConfirmationStatus() (WorkspaceConfirmationInfo, error) {
	return a.workspaceConfirm.snapshot().info(), nil
}

func (a *App) confirmWorkspace(expectedPath string) error {
	_, _, err := a.workspaceConfirm.confirm(expectedPath)
	return err
}
```

(3b) `app.go` `App` struct（base 行 62，`workspaceSrc string` 之後、`// startupMu 保護…` 之前）加入欄位：

```go
	// workspaceConfirm：B4 的 workspace 確認狀態（spec §5.2）。獨立欄位＋自己的 mutex，
	// 刻意不放進 startupState——那會動到 CLIInfo 的形狀與 startupInfo 的欄位枚舉守門。
	// 零值＝尚未解析＝fail closed（閘門拒絕），production 在 acquireStateLease 解析完
	// workspace 的當下寫入。
	workspaceConfirm workspaceConfirmation
```

(3c) `app.go` `acquireStateLease`：在 Task 1 改好的 `r := resolveWorkspace()` 解構之後加一行（寫入時機與 `publishWorkspace` 相同：workspace 決定的當下；只在真的解析時寫入一次）：

```go
		a.workspaceConfirm.resolve(r) // B4：與 workspace 一起決定，閘門與 status binding 都讀這一份
```

(3d) `app.go`：在 `CLIInfo` 函式之後、`// ---- workspace 檔案（canonical 邊界）----` 之前加入兩個 binding（形狀逐項符合 `app_binding_surface_test.go` 的薄包裝契約：三個敘述、`:=` 的 err、零值字面量、原樣轉發參數、實作名為小寫開頭同名）：

```go
// WorkspaceConfirmationStatus：B4 的唯讀 binding（spec §5.3）——回報實際 workspace、
// 來源、是否需要確認與是否已確認。Wails binding 的固定形狀；刻意**不**列入
// startupSafeBindings（只在 ready 之後可呼叫，啟動期間被 beginTxn 拒絕），也不寫 audit。
func (a *App) WorkspaceConfirmationStatus() (WorkspaceConfirmationInfo, error) {
	if err := a.beginTxn(); err != nil {
		return WorkspaceConfirmationInfo{}, err
	}
	defer a.endTxn()
	return a.workspaceConfirmationStatus()
}

// ConfirmWorkspace：B4 的確認 binding（spec §5.3／R3）。expectedPath 必須是前端從
// WorkspaceConfirmationStatus 拿到的 workspace 原始字串；後端只接受完全相同的字串，
// 不接受縮短、`~` 改寫或 canonical 別名。Wails binding 的固定形狀。
func (a *App) ConfirmWorkspace(expectedPath string) error {
	if err := a.beginTxn(); err != nil {
		return err
	}
	defer a.endTxn()
	return a.confirmWorkspace(expectedPath)
}
```

- [ ] **Step 4：跑測試確認通過（含 binding surface 與受保護測試）**

```bash
run_logged t2-gofmt gofmt -l . ; wc -c < $EVID/t2-gofmt.log                                # Expected：0
run_logged t2-vet go vet . ; show t2-vet                                                    # Expected：rc=0
run_logged t2-green go test -run 'TestWorkspaceConfirmation|TestConfirmWorkspace|TestWorkspaceBindings|TestCLIInfoShapeUnchanged|TestAcquireStateLeaseResolves|TestStateBindingsRejectedAfterShutdown|TestEveryBindingIsClassified|TestOnlyThinWrappersAdmit|TestProtectedImplementations|TestStartupSafeBindingHasFixedShape|TestStartupStateIsTheOnlyAccessPath|TestProductionResolverCreatesOnlyEmptyStateDirBeforeLease|TestNoWriterOpensBeforeLeaseIsAcquired|CLIInfo' -count=1 -v .
show t2-green '^(--- |ok|FAIL)' 60
```

Expected：rc=0、全部 PASS。`TestEveryBindingIsClassifiedAndHonoursItsShape` 會自動把兩個新 binding 算進薄包裝；若它紅，代表形狀不合，不得改測試。

- [ ] **Step 5：Mutation 證據**

```bash
# M1：ConfirmWorkspace 不拒絕未解析的零值狀態（空字串 expectedPath 恰好等於空快照）
cp workspace_confirmation.go $EVID/t2-m1.orig
python3 - <<'EOF'
s = open('workspace_confirmation.go', encoding='utf-8').read()
old = """	if c.state == wcUnresolved {
		return false, snap, fmt.Errorf("%w：workspace 尚未解析完成", errWorkspaceConfirmUnavailable)
	}
"""
assert old in s
open('workspace_confirmation.go', 'w', encoding='utf-8').write(s.replace(old, '', 1))
EOF
mutant_check t2-m1 '^--- FAIL: TestWorkspaceConfirmation_UnresolvedFailsClosed' go test -run 'TestWorkspaceConfirmation_UnresolvedFailsClosed' -count=1 .
restore_check $EVID/t2-m1.orig workspace_confirmation.go

# M2：路徑比對改成忽略尾端斜線與大小寫
cp workspace_confirmation.go $EVID/t2-m2.orig
python3 - <<'EOF'
s = open('workspace_confirmation.go', encoding='utf-8').read()
old = "if expectedPath != c.workspace {"
assert old in s
s = s.replace(old, 'if strings.ToLower(strings.TrimRight(expectedPath, "/")) != strings.ToLower(c.workspace) {', 1)
if '\t"strings"\n' not in s:  # Task 2 時還沒有 strings import；Task 3 之後已有，不能重複
    s = s.replace('import (\n\t"errors"', 'import (\n\t"strings"\n\t"errors"', 1)
open('workspace_confirmation.go', 'w', encoding='utf-8').write(s)
EOF
mutant_check t2-m2 '^--- FAIL: TestConfirmWorkspace_(PathMismatchRejectedInEveryState|RawStringRoundTripWithUnicodePath)' go test -run 'TestConfirmWorkspace_PathMismatch|TestConfirmWorkspace_RawString' -count=1 .
restore_check $EVID/t2-m2.orig workspace_confirmation.go

# M3：拿掉 acquireStateLease 的接線
cp app.go $EVID/t2-m3.orig
python3 - <<'EOF'
s = open('app.go', encoding='utf-8').read()
old = "		a.workspaceConfirm.resolve(r) // B4：與 workspace 一起決定，閘門與 status binding 都讀這一份\n"
assert old in s
open('app.go', 'w', encoding='utf-8').write(s.replace(old, '', 1))
EOF
mutant_check t2-m3 '^--- FAIL: TestAcquireStateLeaseResolvesWorkspaceConfirmation' go test -run 'TestAcquireStateLeaseResolvesWorkspaceConfirmation' -count=1 .
restore_check $EVID/t2-m3.orig app.go
```

Expected：三個 `mutant_check` 都印出「紅在目標」：M1 → `未解析時 ConfirmWorkspace("") 必須回 unavailable，實得 <nil>`；M2 → 尾端斜線、大小寫與 NFC／NFD 紅；M3 → `AcquireStateLeaseResolvesWorkspaceConfirmation` 紅。

- [ ] **Step 6：重新產生 wailsjs（比照 A5，只提交內容差異）**

`wails generate module` 需要 `frontend/dist` 存在（Task 0 已建佔位檔）。若仍失敗，保留 log 並依 A5 先例補佔位後重跑，不得只留成功那份。

```bash
run_logged t2-wails wails generate module ; show t2-wails '' 5                              # Expected：rc=0
# Wails CLI 會把 runtime 三個檔的權限改成 755（A5 記錄的副作用，內容沒有差異）——還原成 644
chmod 644 frontend/wailsjs/runtime/package.json frontend/wailsjs/runtime/runtime.d.ts frontend/wailsjs/runtime/runtime.js
run_logged t2-wails-summary git diff --summary -- frontend/wailsjs ; wc -c < $EVID/t2-wails-summary.log   # Expected：0（沒有任何權限變化；go/ 底下三檔本來就以 755 提交）
run_logged t2-wails-stat git diff --stat -- frontend/wailsjs ; cat $EVID/t2-wails-stat.log              # Expected：App.d.ts、App.js、models.ts 三個檔，只有新增行
git status --short frontend | grep -v wailsjs                                                           # Expected：空
```

**已知特性（狹窄例外，不得手動修改）：** `models.ts` 的新類別 `WorkspaceConfirmationInfo` 內有**兩行只含一個 tab 的空行**（`static createFrom` 之前、`constructor` 之前各一行，下方 diff 中顯示為 `+<tab>`）。這是 Wails 產生器的原樣輸出；它們會讓 `git diff --check` 報 `trailing whitespace`。**原樣保留，不得刪除、改成空行或對整份 `models.ts` 做格式化**——提交產生器原樣的輸出，未來重新產生時才不會出現差異（比照 A5）。收尾的 `git diff --check` 在 Task 8 Step 3 只對這兩項訂例外。確認產物裡剛好是這兩行：

```bash
run_logged t2-wails-tabs git diff -U0 -- frontend/wailsjs/go/models.ts
grep -cE '^\+[[:space:]]+$' $EVID/t2-wails-tabs.log        # Expected：2（新增的空白行只有這兩行）
grep -nE '^\+[[:space:]]+$' $EVID/t2-wails-tabs.log | cat -A | head -4   # 內容應為 +^I（一個 tab）
```

預期的內容差異（計畫撰寫時在複本上重新產生的結果；你的產生結果必須逐位元組相同，否則停下來回報）：

```diff
--- a/frontend/wailsjs/go/main/App.d.ts
+++ b/frontend/wailsjs/go/main/App.d.ts
@@ -19,6 +19,8 @@

 export function ConfirmSpecCommit(arg1:spec.CommitToken,arg2:string):Promise<void>;

+export function ConfirmWorkspace(arg1:string):Promise<void>;
+
 export function CreateSession(arg1:string,arg2:string):Promise<string>;

 export function EndSession(arg1:string):Promise<void>;
@@ -110,3 +112,5 @@
 export function TerminateSession(arg1:string):Promise<void>;

 export function ValidateTestCommit(arg1:string,arg2:string,arg3:string):Promise<void>;
+
+export function WorkspaceConfirmationStatus():Promise<main.WorkspaceConfirmationInfo>;
```

```diff
--- a/frontend/wailsjs/go/main/App.js
+++ b/frontend/wailsjs/go/main/App.js
@@ -26,6 +26,10 @@
   return window['go']['main']['App']['ConfirmSpecCommit'](arg1, arg2);
 }

+export function ConfirmWorkspace(arg1) {
+  return window['go']['main']['App']['ConfirmWorkspace'](arg1);
+}
+
 export function CreateSession(arg1, arg2) {
   return window['go']['main']['App']['CreateSession'](arg1, arg2);
 }
@@ -209,3 +213,7 @@
 export function ValidateTestCommit(arg1, arg2, arg3) {
   return window['go']['main']['App']['ValidateTestCommit'](arg1, arg2, arg3);
 }
+
+export function WorkspaceConfirmationStatus() {
+  return window['go']['main']['App']['WorkspaceConfirmationStatus']();
+}
```

```diff
--- a/frontend/wailsjs/go/models.ts
+++ b/frontend/wailsjs/go/models.ts
@@ -575,7 +575,27 @@
 	        if ('string' === typeof source) source = JSON.parse(source);
 	        this.content = source["content"];
 	        this.digest = source["digest"];
+	    }
+	}
+	export class WorkspaceConfirmationInfo {
+	    required: boolean;
+	    confirmed: boolean;
+	    workspace: string;
+	    source: string;
+	    reasons: string[];
+	
+	    static createFrom(source: any = {}) {
+	        return new WorkspaceConfirmationInfo(source);
 	    }
+	
+	    constructor(source: any = {}) {
+	        if ('string' === typeof source) source = JSON.parse(source);
+	        this.required = source["required"];
+	        this.confirmed = source["confirmed"];
+	        this.workspace = source["workspace"];
+	        this.source = source["source"];
+	        this.reasons = source["reasons"];
+	    }
 	}

 }
```

若 `git diff --summary` 顯示 `go/main/App.d.ts`、`App.js`、`go/models.ts` 有 mode 變化，依 `git ls-files -s frontend/wailsjs` 的已提交 mode（100755）還原，不要全部改 755 或 644。

- [ ] **Step 7：Commit**

```bash
git add workspace_confirmation.go workspace_confirm_test.go app.go app_state_binding_lifecycle_test.go frontend/wailsjs
git commit -m "$(cat <<'EOF'
feat(b4): 確認狀態＋WorkspaceConfirmationStatus／ConfirmWorkspace binding（Task 2）

確認狀態放在 App 的獨立欄位 workspaceConfirm（自己的 mutex，不進 startupState）；零值＝
尚未解析＝fail closed。ConfirmWorkspace 先核對路徑字串再依狀態決定結果，Pending→Confirmed
由單一臨界區決定。兩個 binding 為薄包裝、不列入 startup-safe。wailsjs 以 wails generate
module 重新產生，只提交內容差異。

Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3：閘門函式＋`startSession`／`sendMessage`

**為什麼這樣切：** 這是第一個會改變使用者可見行為的 task（零值 fail closed 會讓許多既有測試的夾具失去前提），獨立 commit 才能清楚區分「閘門行為」與「夾具前提」兩類紅燈。

**Files:**
- Modify: `workspace_confirmation.go`（加 `strings` import 與 `requireWorkspaceConfirmed`）
- Modify: `app.go:6956-6962`（`startSession`：`registryUncertain` 之後、`BeginNewSessionSubmit` 之前）、`app.go:7027-7031`（`sendMessage`：`resolveWSID` 之後、`hostFor` 與兩個 `BeginSubmit` 之前）
- Modify: `app_test.go:149-155`（`newTestAppIn` 夾具加 1 行；**不在 spec §6.1 的受保護清單內，且是共用夾具而非個別測試**）
- Test: `workspace_gate_test.go`（新增）

**Interfaces:**
- Consumes（Task 2）：`a.workspaceConfirm.snapshot()`、`workspaceConfirmSnapshot.info()`、`errWorkspaceConfirmationRequired`、`makePending`／`mustStatus`／`pendingResolution`／`notRequiredResolution`。
- Produces（Task 4／5 依賴）：`func (a *App) requireWorkspaceConfirmed(op, provider, wsid string) error`——op 取值固定為 `"start_session"`、`"send_message"`、`"spec_assist"`、`"plan_assist"`；測試輔助 `requireConfirmationError(t, err)`、`writeCountingClaude(t, a) (spawnLog, inputLog string)`、`armFakeCodex(t, a) *fakeCodexWire`、`countMethod(w, method) int`、`lineCount(t, path) int`、`fileSizeOrZero`、`startArtifacts`。

- [ ] **Step 1：寫失敗測試**

新增 `workspace_gate_test.go`。涵蓋：claude 首輪／resume（含「先前跑完的 session」讓 reducer 停在 `done`）、codex 首輪 `untrusted`／`never`／resume、一個確認涵蓋所有 session、`sendMessage` 的防禦閘門（claude／codex）、錯誤優先順序、以及整條 production 啟動路徑（`HOME` 與 `WORKBENCH_WORKSPACE` 都指向同一個隔離目錄＝家目錄替身，spec §6.3 的反例；對照組為家目錄底下的專案）：

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/appcore"
	"github.com/slam0504/sdlc-workbench/internal/codex"
	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// ---- B4 R2：閘門（startSession／sendMessage）----
//
// 每個拒絕案例都斷言「沒有任何 session／provider 啟動副作用」，而且拒絕之後確認、再啟動
// 必須成功——後者證明 Manager 的 submit 狀態沒有被拒絕的那一次洩漏（ErrSessionActive／
// ErrSubmitActive）。

func requireConfirmationError(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, errWorkspaceConfirmationRequired) {
		t.Fatalf("必須回 workspace_confirmation_required，實得 %v", err)
	}
	if !strings.HasPrefix(err.Error(), "workspace_confirmation_required") {
		t.Fatalf("錯誤訊息必須以 workspace_confirmation_required 開頭：%q", err.Error())
	}
}

func fileSizeOrZero(t *testing.T, p string) int64 {
	t.Helper()
	st, err := os.Stat(p)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}

// writeCountingClaude：fake claude CLI——每次被 exec 記一行 spawn.log，每收到一行 stdin 記一行
// input.log。拒絕案例用「spawn.log 不存在」證明沒有 exec，用 input.log 的行數證明沒有送出 turn。
func writeCountingClaude(t *testing.T, a *App) (spawnLog, inputLog string) {
	t.Helper()
	bin := a.claudeCLIPath()
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	spawnLog = filepath.Join(a.stateDir, "spawn.log")
	inputLog = filepath.Join(a.stateDir, "input.log")
	script := "#!/bin/sh\necho spawn >> " + spawnLog + "\nwhile read -r line; do\n" +
		"echo \"$line\" >> " + inputLog + "\n" +
		"printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false}'\ndone\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return spawnLog, inputLog
}

func lineCount(t *testing.T, p string) int {
	t.Helper()
	b, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "\n")
}

// startArtifacts：session 啟動會留下的檔案——broker socket、MCP config、recorder 檔案。拒絕前後
// 各取一次比對（resume 案例先前的執行留下的檔案不算；只有「拒絕的那一次」不得新增）。
func startArtifacts(t *testing.T, a *App) []string {
	t.Helper()
	var out []string
	for _, pattern := range []string{"mcp-*.json", "approval-*.sock", "recordings/*"} {
		m, _ := filepath.Glob(filepath.Join(a.stateDir, pattern))
		out = append(out, m...)
	}
	sort.Strings(out)
	return out
}

// armFakeCodex：與 startCodexForTest 相同的 fake app-server 回應器＋握手，但**不**呼叫
// StartSession——拒絕案例要在 wire 已就緒的情況下呼叫，這樣任何漏過閘門的 thread/start、
// turn/start 都看得見。
func armFakeCodex(t *testing.T, a *App) *fakeCodexWire {
	t.Helper()
	conn, wire := newFakeCodexConn(t)
	a.wireCodexConn(conn) // 通知（turn/completed 等）才會路由到 session；與既有 fake wire 測試相同
	var turnSeq atomic.Int32
	wire.setOnReq(func(f codex.Frame) {
		switch f.Method {
		case codex.MethodInitialize:
			wire.send(map[string]any{"id": *f.ID, "result": map[string]any{}})
		case codex.MethodThreadStart, codex.MethodThreadResume:
			wire.send(map[string]any{"id": *f.ID, "result": map[string]any{"thread": map[string]any{"id": "t1"}}})
		case codex.MethodTurnStart:
			turnID := fmt.Sprintf("turn-%d", turnSeq.Add(1))
			wire.send(map[string]any{"id": *f.ID, "result": map[string]any{
				"turn": map[string]any{"id": turnID, "status": "inProgress"}}})
			wire.send(map[string]any{"method": codex.MethodTurnCompleted,
				"params": map[string]any{"threadId": "t1", "turn": map[string]any{"id": turnID, "status": "completed"}}})
		}
	})
	hctx, hcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer hcancel()
	if err := conn.Handshake(hctx, clientInfo()); err != nil {
		t.Fatal(err)
	}
	a.codexHostOverride = fakeCodexHost{conn}
	return wire
}

func countMethod(w *fakeCodexWire, method string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, f := range w.seen {
		if f.Method == method {
			n++
		}
	}
	return n
}

func TestStartSessionGate_ClaudeRejectedWithoutSideEffects(t *testing.T) {
	for _, tc := range []struct {
		name, resume string
		priorRun     bool // resume 的真實形狀：這個 session 先前跑完並結束過（reducer 停在 done）
	}{{"首輪", "", false}, {"resume", "sess-old", true}} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newTestApp(t)
			a.wsReg = &stubRegistry{}
			spawnLog, _ := writeCountingClaude(t, a)
			w := mustCreate(t, a, "claude")
			if tc.priorRun {
				// 先前的執行發生在確認之前不需要的 workspace 狀態（NotRequired）。結束之後 reducer 停在
				// done；若拒絕的啟動偷偷呼叫過 BeginNewSessionSubmit（即使事後 RejectSubmit），
				// newSessionLocked 會把 reducer 重設成 idle——下面的狀態比對因此抓得到。
				if err := a.StartSession(string(w), "prior", "", "", "t", ""); err != nil {
					t.Fatal(err)
				}
				waitTurnSettled(t, a, w)
				if err := a.EndSession(string(w)); err != nil {
					t.Fatal(err)
				}
				spawnsBefore := lineCount(t, spawnLog)
				if spawnsBefore != 1 {
					t.Fatalf("前提：先前的執行 exec 過一次 CLI，實得 %d", spawnsBefore)
				}
			}
			makePending(a)
			providerStarts := 0
			a.hookBeforeProviderStart = func() { providerStarts++ }
			stateBefore, _ := a.manager.State(w)
			eventsBefore := fileSizeOrZero(t, a.eventsPath())
			artifactsBefore := startArtifacts(t, a)

			err := a.StartSession(string(w), "hi", tc.resume, "rec-case", "task", "")
			requireConfirmationError(t, err)
			if providerStarts != 0 {
				t.Fatal("拒絕時不得走到 BeginNewSessionSubmit 之後（hookBeforeProviderStart 被呼叫）")
			}
			if a.hostFor(w) != nil {
				t.Fatal("拒絕時不得建立 host")
			}
			wantSpawns := 0
			if tc.priorRun {
				wantSpawns = 1 // 只有先前那一次
			}
			if got := lineCount(t, spawnLog); got != wantSpawns {
				t.Fatalf("拒絕時不得 exec claude CLI（spawn.log %d 行，want %d）", got, wantSpawns)
			}
			if got := startArtifacts(t, a); !reflect.DeepEqual(got, artifactsBefore) {
				t.Fatalf("拒絕時不得新增 broker socket／MCP config／recorder 檔案：%v → %v", artifactsBefore, got)
			}
			if got, _ := a.manager.State(w); got != stateBefore {
				t.Fatalf("Manager 狀態不得改變：%v → %v", stateBefore, got)
			}
			if after := fileSizeOrZero(t, a.eventsPath()); after != eventsBefore {
				t.Fatalf("拒絕時不得寫入 events.jsonl（%d → %d）", eventsBefore, after)
			}

			if err := a.ConfirmWorkspace(a.workspaceDir); err != nil {
				t.Fatal(err)
			}
			if err := a.StartSession(string(w), "hi", "", "", "task", ""); err != nil {
				t.Fatalf("確認之後必須可以啟動（證明 submit 狀態沒有被拒絕的那一次洩漏）：%v", err)
			}
			t.Cleanup(func() { _ = a.EndSession(string(w)) })
		})
	}
}

func TestStartSessionGate_CodexRejectedWithoutThreadOrTurn(t *testing.T) {
	for _, tc := range []struct{ name, resume, policy string }{
		{"首輪 untrusted", "", "untrusted"},
		{"首輪 approvalPolicy=never", "", "never"},
		{"resume never", "thread-old", "never"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newTestApp(t)
			wire := armFakeCodex(t, a)
			w := wsidFor(t, a, contract.ProviderCodex)
			makePending(a)
			providerStarts := 0
			a.hookBeforeProviderStart = func() { providerStarts++ }
			stateBefore, _ := a.manager.State(w)

			err := a.StartSession(string(w), "hi", tc.resume, "rec-case", "task", tc.policy)
			requireConfirmationError(t, err)
			if providerStarts != 0 {
				t.Fatal("拒絕時不得走到 BeginNewSessionSubmit 之後")
			}
			for _, m := range []string{codex.MethodThreadStart, codex.MethodThreadResume, codex.MethodTurnStart} {
				if n := countMethod(wire, m); n != 0 {
					t.Fatalf("拒絕時不得送出 %s（%d 次）", m, n)
				}
			}
			if a.hostFor(w) != nil {
				t.Fatal("拒絕時不得建立 host")
			}
			if got, _ := a.manager.State(w); got != stateBefore {
				t.Fatalf("Manager 狀態不得改變：%v → %v", stateBefore, got)
			}

			if err := a.ConfirmWorkspace(a.workspaceDir); err != nil {
				t.Fatal(err)
			}
			if err := a.StartSession(string(w), "hi", "", "", "task", tc.policy); err != nil {
				t.Fatalf("確認之後必須可以啟動：%v", err)
			}
			t.Cleanup(func() { _ = a.EndSession(string(w)) })
			if countMethod(wire, codex.MethodThreadStart) != 1 || countMethod(wire, codex.MethodTurnStart) != 1 {
				t.Fatal("確認之後的啟動必須送出 thread/start 與 turn/start 各一次")
			}
		})
	}
}

// 一個確認，所有 session 共用（R4）。
func TestStartSessionGate_OneConfirmationCoversEverySession(t *testing.T) {
	a, _ := newTestApp(t)
	a.wsReg = &stubRegistry{}
	writeCountingClaude(t, a)
	w1 := mustCreate(t, a, "claude")
	w2 := mustCreate(t, a, "claude")
	makePending(a)
	for _, w := range []appcore.WSID{w1, w2} {
		requireConfirmationError(t, a.StartSession(string(w), "hi", "", "", "t", ""))
	}
	if err := a.ConfirmWorkspace(a.workspaceDir); err != nil {
		t.Fatal(err)
	}
	for _, w := range []appcore.WSID{w1, w2} {
		if err := a.StartSession(string(w), "hi", "", "", "t", ""); err != nil {
			t.Fatalf("同一個確認必須涵蓋所有 session：%v", err)
		}
		w := w
		t.Cleanup(func() { _ = a.EndSession(string(w)) })
	}
}

// 防禦性的 sendMessage 閘門：先在 NotRequired 下啟動 session，再把狀態翻成 Pending
// （模擬「未來有 restore 自動啟動 host」之類讓 active session 早於確認存在的情況）。
func TestSendMessageGate_ClaudeRejectedWithoutSendingTurn(t *testing.T) {
	a, _ := newTestApp(t)
	a.wsReg = &stubRegistry{}
	_, inputLog := writeCountingClaude(t, a)
	w := mustCreate(t, a, "claude")
	if err := a.StartSession(string(w), "first", "", "", "t", ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.EndSession(string(w)) })
	waitTurnSettled(t, a, w)
	if got := lineCount(t, inputLog); got != 1 {
		t.Fatalf("前提：首輪送出 1 行，實得 %d", got)
	}

	makePending(a)
	requireConfirmationError(t, a.SendMessage(string(w), "second"))
	if got := lineCount(t, inputLog); got != 1 {
		t.Fatalf("拒絕時不得把訊息送進 CLI（input.log %d 行）", got)
	}
	if st, _ := a.manager.State(w); st != contract.StateDone {
		t.Fatalf("拒絕時 Manager 狀態不得改變（BeginSubmit 不得被呼叫）：%v", st)
	}

	if err := a.ConfirmWorkspace(a.workspaceDir); err != nil {
		t.Fatal(err)
	}
	if err := a.SendMessage(string(w), "second"); err != nil {
		t.Fatalf("確認之後必須可送出（證明 submit 狀態沒有洩漏）：%v", err)
	}
	waitFor(t, "第二輪送進 CLI", func() bool { return lineCount(t, inputLog) == 2 })
}

func TestSendMessageGate_CodexRejectedWithoutTurnStart(t *testing.T) {
	a, _ := newTestApp(t)
	wire := armFakeCodex(t, a)
	w := wsidFor(t, a, contract.ProviderCodex)
	if err := a.StartSession(string(w), "first", "", "", "t", "never"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.EndSession(string(w)) })
	waitTurnSettled(t, a, w)
	if got := countMethod(wire, codex.MethodTurnStart); got != 1 {
		t.Fatalf("前提：首輪 1 次 turn/start，實得 %d", got)
	}

	makePending(a)
	requireConfirmationError(t, a.SendMessage(string(w), "second"))
	if got := countMethod(wire, codex.MethodTurnStart); got != 1 {
		t.Fatalf("拒絕時不得送出 turn/start（%d 次）", got)
	}

	if err := a.ConfirmWorkspace(a.workspaceDir); err != nil {
		t.Fatal(err)
	}
	if err := a.SendMessage(string(w), "second"); err != nil {
		t.Fatalf("確認之後必須可送出：%v", err)
	}
	if got := countMethod(wire, codex.MethodTurnStart); got != 2 {
		t.Fatalf("確認之後第二輪必須送出 turn/start：%d", got)
	}
}

// 不改變各入口既有檢查的錯誤優先順序（spec R2 Pre；Review Focus 3）。
func TestGate_PreservesExistingErrorPrecedence(t *testing.T) {
	a, _ := newTestApp(t)
	reg := &stubRegistry{}
	a.wsReg = reg
	writeCountingClaude(t, a)
	w := mustCreate(t, a, "claude")
	makePending(a)

	// 未知的 wsid：resolveWSID 的 ErrSessionNotFound 先於確認閘（startSession／sendMessage 皆然）。
	if err := a.StartSession("no-such-wsid", "hi", "", "", "t", ""); !errors.Is(err, appcore.ErrSessionNotFound) {
		t.Fatalf("StartSession 未知 wsid 必須維持 ErrSessionNotFound：%v", err)
	}
	if err := a.SendMessage("no-such-wsid", "hi"); !errors.Is(err, appcore.ErrSessionNotFound) {
		t.Fatalf("SendMessage 未知 wsid 必須維持 ErrSessionNotFound：%v", err)
	}

	// startSession 原有的 registryUncertain 檢查先於確認閘。
	reg.mu.Lock()
	reg.uncertain = true
	reg.mu.Unlock()
	if err := a.StartSession(string(w), "hi", "", "", "t", ""); !errors.Is(err, errRegistryUncertain) {
		t.Fatalf("registryUncertain 必須先於確認閘：%v", err)
	}
	// 確認閘不會把 registryUncertain 加到 sendMessage：沒有 active session 時，
	// 既有行為是 BeginSubmit 的錯誤，而不是 registryUncertain；Pending 時則是確認閘。
	if err := a.SendMessage(string(w), "hi"); errors.Is(err, errRegistryUncertain) {
		t.Fatalf("不得把 registryUncertain 加到 sendMessage：%v", err)
	}
}

// 整條 production 啟動路徑（acquireStateLease → resolveWorkspace → 確認狀態 → 閘門）：
// 其餘測試用 resolve() 直接把 App 放進指定狀態，只有這一條驗證「production 真的接了線」。
// 反例（spec §6.3）：env 指向「家目錄的隔離替身目錄」——HOME 與 WORKBENCH_WORKSPACE 都設成
// 同一個隔離目錄——必須要求確認，送出訊息必須被拒。不放 Playwright E2E 的理由見計畫 Task 8。
func TestProductionStartupPath_EnvEqualsHomeRequiresConfirmation(t *testing.T) {
	home := realDir(t, filepath.Join(t.TempDir(), "home"))
	t.Setenv("HOME", home)
	t.Setenv("WORKBENCH_WORKSPACE", home)

	a := NewApp()
	a.emitUI = func(string, any) {} // 不碰 wails runtime
	a.startup(context.Background())
	t.Cleanup(func() { a.shutdown(context.Background()) })
	if a.startupErrText() != "" {
		t.Fatalf("前提：乾淨的隔離目錄不應有 startup error：%s", a.startupErrText())
	}

	st := mustStatus(t, a)
	if !st.Required || st.Confirmed || st.Workspace != home || st.Source != "env" ||
		!reflect.DeepEqual(st.Reasons, []string{wsReasonIsHome}) {
		t.Fatalf("env=$HOME 必須要求確認，且原因是 is_home：%+v", st)
	}
	w, err := a.CreateSession("claude", "t") // 建立 session 不是 AI 工作，Pending 時仍可進行
	if err != nil {
		t.Fatalf("Pending 時 CreateSession 必須可用：%v", err)
	}
	requireConfirmationError(t, a.StartSession(w, "hi", "", "", "t", ""))
	requireConfirmationError(t, a.SendMessage(w, "hi"))

	if err := a.ConfirmWorkspace(st.Workspace); err != nil {
		t.Fatal(err)
	}
	if st := mustStatus(t, a); !st.Confirmed {
		t.Fatalf("確認之後：%+v", st)
	}
}

// 同一條路徑的對照組：env 指向家目錄「底下」的專案（HOME 與 workspace 不同）→ 不需確認，
// 行為與 B4 之前相同（E2E／CI 的 env 注入形狀）。
func TestProductionStartupPath_EnvProjectNeedsNoConfirmation(t *testing.T) {
	root := realDir(t, t.TempDir())
	home := realDir(t, filepath.Join(root, "home"))
	project := realDir(t, filepath.Join(home, "projects", "foo"))
	t.Setenv("HOME", home)
	t.Setenv("WORKBENCH_WORKSPACE", project)

	a := NewApp()
	a.emitUI = func(string, any) {}
	a.startup(context.Background())
	t.Cleanup(func() { a.shutdown(context.Background()) })

	st := mustStatus(t, a)
	if st.Required || st.Confirmed || st.Workspace != project || len(st.Reasons) != 0 {
		t.Fatalf("家目錄底下的專案不需確認：%+v", st)
	}
}
```

- [ ] **Step 2：跑一次確認失敗（保留首次紅燈）**

```bash
run_logged t3-red go test -run 'TestStartSessionGate|TestSendMessageGate|TestGate_|TestProductionStartupPath' -count=1 -v .
show t3-red '^(--- |ok|FAIL)' 30
```

Expected（計畫撰寫時在複本上、閘門尚未接上時量到）：`TestStartSessionGate_ClaudeRejectedWithoutSideEffects`、`TestStartSessionGate_CodexRejectedWithoutThreadOrTurn`、`TestStartSessionGate_OneConfirmationCoversEverySession`、`TestSendMessageGate_ClaudeRejectedWithoutSendingTurn`、`TestSendMessageGate_CodexRejectedWithoutTurnStart`、`TestProductionStartupPath_EnvEqualsHomeRequiresConfirmation` 紅（錯誤是「必須回 workspace_confirmation_required，實得 <nil>」——被拒的呼叫真的啟動了）；`TestGate_PreservesExistingErrorPrecedence` 與 `TestProductionStartupPath_EnvProjectNeedsNoConfirmation` 此時就是綠（它們守的是「不退步」）。

- [ ] **Step 3：最小實作**

(3a) `workspace_confirmation.go`：import 加 `"strings"`，檔尾附加：

```go
// ---- 閘門（spec R2、§5.4）----

// requireWorkspaceConfirmed：四個 AI 工作入口（startSession／sendMessage／specAssist／
// planAssist）共用的政策檢查。只有 NotRequired 與 Confirmed 通過；其餘一律回
// 「workspace_confirmation_required…」開頭的錯誤。
//
// 呼叫端必須把它放在任何 session／provider 啟動副作用**之前**（位置見 spec §5.4）。本函式
// 自己不做 I/O、不改變 lifecycle phase。op／provider／wsid 供 Task 5 的稽核使用。
func (a *App) requireWorkspaceConfirmed(op, provider, wsid string) error {
	snap := a.workspaceConfirm.snapshot()
	if snap.state == wcNotRequired || snap.state == wcConfirmed {
		return nil
	}
	info := snap.info()
	return fmt.Errorf("%w：目前的 workspace（%s，來源 %s）需要先在畫面頂端確認，確認前不會開始任何 AI 工作（原因：%s）",
		errWorkspaceConfirmationRequired, info.Workspace, info.Source, strings.Join(info.Reasons, ","))
}
```

(3b) `app.go` `startSession`（`registryUncertain` 檢查之後、`resume == ""` 那段之前；位置在 `BeginNewSessionSubmit` 之前，所以失敗時不需要 `RejectSubmit`）：

```go
	if a.registryUncertain() {
		return errRegistryUncertain
	}
	// B4 確認閘（spec §5.4）：registryUncertain 之後、BeginNewSessionSubmit 之前。拒絕時
	// 尚未取得 submit ownership，所以不需要 RejectSubmit。
	if err := a.requireWorkspaceConfirmed("start_session", string(prov), wsid); err != nil {
		return err
	}
	if resume == "" { // 第三輪 P1-3：view 未被 New 清除時自動接續（plan D6 resume 意圖）
```

（只新增中間那個 `if` 區塊，其餘為既有行的錨點。）

(3c) `app.go` `sendMessage`（`resolveWSID` 之後、`h := a.hostFor(w)` 之前）：

```go
	// B4 確認閘（防禦性，spec §5.4）：一般情況下未確認時不會有 active session；若未來支援
	// restore 時自動啟動 host，這裡就是最後一道保護。必須早於兩個 BeginSubmit。
	if err := a.requireWorkspaceConfirmed("send_message", string(pv), wsid); err != nil {
		return err
	}
```

- [ ] **Step 4：先跑整個根 package（不加 `-race`），保留紅燈並逐一追查責任歸屬——此時還不要補夾具**

零值 fail closed 會讓所有「經 `newTestAppIn` 建 App、卻沒有解析 workspace」的既有測試在 `StartSession`／`SendMessage` 被拒；本 task 自己的測試裡「先在不需確認狀態下啟動、再 `makePending`」的幾處（`workspace_gate_test.go` 的 prior run 與 `SendMessage` 前置啟動）也一樣。

```bash
run_logged t3-full-red go test -count=1 -timeout 20m .
show t3-full-red '^--- FAIL' 5
grep -c '^--- FAIL' $EVID/t3-full-red.log                                                                         # 含子測試的 FAIL 行數
grep -E '^\s+[A-Za-z0-9_]+_test.go:[0-9]+:' $EVID/t3-full-red.log | grep -vc 'workspace_unresolved'              # 不含 workspace_unresolved 的失敗行數（Expected：0）
grep -E '^\s+[A-Za-z0-9_]+_test.go:[0-9]+:' $EVID/t3-full-red.log | sed -E 's/^\s+([A-Za-z0-9_]+_test.go):.*/\1/' | sort | uniq -c
grep -E '^(--- )?FAIL: (TestAcquireStateLeaseResolvesWorkspaceConfirmation|TestProductionStartupPath_)' $EVID/t3-full-red.log   # Expected：空
```

Expected（計畫撰寫時在「Task 3 完成、Task 4／5 尚未實作」的複本上量到，約 2.5 分鐘）：`--- FAIL` 87 行（含子測試）；每一處錯誤訊息都含 `workspace_unresolved`；失敗分散在 `app_claude_multi`、`app_codex_dispatch`、`app_invariants`、`app_lease_boundary`、`app_per_wsid_writer`、`app_registry_uncertain`、`app_remove`、`app_test`、`app_wire_frame_jobs`、`app_wire_frames`、`app_wire_segments`、`app_wirelog_latch`、`app_wsid` 等測試檔，以及本 task 的 `workspace_gate_test.go` 三處。

**分類規則（不能只憑錯誤字串 `workspace_unresolved` 就補 seed）：** 這個字串也可能是 production 接線漏掉 `workspaceConfirm.resolve` 的症狀，所以先分流，再決定能不能動夾具：

1. **允許自動修補的只有一個地方：`newTestAppIn` 那一行**（下一步）。它代表「已完成 startup 的正常專案夾具」；零值 App 的獨立測試仍用 `NewApp()`，production 啟動測試也自行走 `startup`，兩者都不經過這行。
2. 對每個失敗的測試，**先追它的 App 從哪個建構函式來**：`grep -nE 'NewApp\(\)|&App\{|newTestApp[A-Za-z]*\(' <失敗的測試檔>`。經 `newTestApp`／`newTestAppIn` 建立者屬第 1 類；經 `newTestAppAt` 或其他 `NewApp()` 直建 helper、或其他任何不是 `newTestAppIn` 的路徑建立者，**不得補 seed**——停下來，追 caller 與解析／接線責任，分類（夾具前提／production 接線回歸／其他）後回報 owner。
3. `TestAcquireStateLeaseResolvesWorkspaceConfirmation`、`TestProductionStartupPath_*` 失敗（上面最後一條 grep 非空）一律視為**可能的 production 接線回歸**：先檢查 `acquireStateLease` 的 `a.workspaceConfirm.resolve(r)` 是否還在、`resolveWorkspace` 的回傳是否被正確解構，修的是 production，不是夾具。
4. 受保護的 lease、CLIInfo、Assist 形狀測試即使因此失敗也不得修改。
5. 任何失敗訊息**不含** `workspace_unresolved` 的，是真實缺陷或環境問題，另行處理。

（反面教材：若在 Task 4 之後才補夾具，未補前提的 Assist 測試會在 `waitAssistActive` 逾時後卡住整個 package——計畫撰寫時一次未補夾具的全套跑到 30 分鐘 `-timeout` 才中止。所以夾具前提必須在 Task 3 解決。）

- [ ] **Step 5：夾具補上前提（一行，在共用夾具，不改任何個別測試）**

`newTestAppIn` 代表「startup 已完成、workspace 已解析且不需確認」（與 E2E／CI 的 env 注入同形）：

```diff
--- a/app_test.go
+++ b/app_test.go
@@ -152,6 +152,10 @@
 	a.ctx = context.Background()
 	a.workspaceDir = ws
 	a.stateDir = stateDir
+	// B4：測試夾具代表「workspace 已解析且不需確認」（env 注入的正常專案目錄，與 E2E／CI
+	// 相同的形狀）。不設的話 workspaceConfirm 是零值＝fail closed，所有 AI 工作入口都會被
+	// 閘門拒絕。要測 Pending 的測試呼叫 makePending(a)。
+	a.workspaceConfirm.resolve(notRequiredResolution(ws))
 	// 測試基盤明確出示 ownership capability。沒有這一行，openStateWriters 與
 	// 所有 writer 入口一律拒絕（owner 2026-08-18：production 的空 lease 一律
 	// fail closed，不得恢復成「空值就跳過檢查」）。
```

- [ ] **Step 6：新測試轉綠，整個根 package 轉綠**

```bash
run_logged t3-gofmt gofmt -l . ; wc -c < $EVID/t3-gofmt.log                                # Expected：0
run_logged t3-vet go vet . ; show t3-vet                                                    # Expected：rc=0
run_logged t3-green go test -race -run 'TestStartSessionGate|TestSendMessageGate|TestGate_|TestProductionStartupPath' -count=1 -v .
show t3-green '^(--- |ok|FAIL)' 40
run_logged t3-full-green go test -count=1 -timeout 20m .
show t3-full-green '' 5
```

Expected：兩組 rc=0；新測試全部 PASS；根 package `ok`（計畫撰寫時不加 `-race` 約 3 分鐘）。若仍有失敗：依 Step 4 的分類規則處理——**只有**經 `newTestAppIn` 建立、且錯誤含 `workspace_unresolved` 的才屬於這一行夾具的責任；其餘（含 `NewApp()` 直建的 helper）先追責任歸屬並回報 owner，不得自行補 seed。

- [ ] **Step 7：Mutation 證據**

```bash
# M1：閘門挪到 BeginNewSessionSubmit 之後（事後 RejectSubmit）——抓「偷呼叫 Begin」
cp app.go $EVID/t3-m1.orig
python3 - <<'EOF'
s = open('app.go', encoding='utf-8').read()
g = '''	if err := a.requireWorkspaceConfirmed("start_session", string(prov), wsid); err != nil {
		return err
	}
'''
anchor = '''	id, err := a.manager.BeginNewSessionSubmit(w, taskLabel)
	if err != nil {
		return err // ErrSessionActive／ErrSubmitActive 原樣回 UI
	}
'''
assert g in s and anchor in s
s = s.replace(g, '', 1)
s = s.replace(anchor, anchor + '''	if err := a.requireWorkspaceConfirmed("start_session", string(prov), wsid); err != nil {
		_ = a.manager.RejectSubmit(w, id)
		return err
	}
''', 1)
open('app.go', 'w', encoding='utf-8').write(s)
EOF
mutant_check t3-m1 'Manager 狀態不得改變：done → idle' go test -run 'TestStartSessionGate_Claude' -count=1 .
restore_check $EVID/t3-m1.orig app.go

# M2：閘門恆放行
cp workspace_confirmation.go $EVID/t3-m2.orig
sed -i '' 's/if snap.state == wcNotRequired || snap.state == wcConfirmed {/if snap.state != wcUnresolved {/' workspace_confirmation.go
mutant_check t3-m2 '^--- FAIL: (TestStartSessionGate_|TestSendMessageGate_)' go test -run 'TestStartSessionGate|TestSendMessageGate' -count=1 .
restore_check $EVID/t3-m2.orig workspace_confirmation.go
```

Expected：兩個 `mutant_check` 都印出「紅在目標」：M1 → `ClaudeRejectedWithoutSideEffects/resume`，訊息 `Manager 狀態不得改變：done → idle`；M2 → Start／Send 兩組全紅。

- [ ] **Step 8：Commit**

```bash
git add workspace_confirmation.go workspace_gate_test.go app.go app_test.go
git commit -m "$(cat <<'EOF'
feat(b4): requireWorkspaceConfirmed 閘門＋startSession／sendMessage（Task 3）

startSession 的閘門在 registryUncertain 之後、BeginNewSessionSubmit 之前；sendMessage 的防禦性
閘門在 resolveWSID 之後、BeginSubmit 之前。未解析的零值狀態 fail closed，共用夾具
newTestAppIn 補上「已解析且不需確認」前提。

Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 4：`specAssist`／`planAssist` 閘門

**為什麼這樣切：** Assist 入口有 binding surface 的形狀約束與 context 取消責任（spec §5.4、H2），和 Task 3 的失敗模式不同，獨立 review 較安全。

**Files:**
- Modify: `app.go:546`（`hookAssistBeforeTxn` 之後加一個測試 hook 欄位）、`app.go:6331-6332`（`specAssist`：purpose pin 之後、`gen := &assistGen{` 之前）、`app.go:6516-6518`（`planAssist`：既有 `defer cancel()` 之後、`ensureGate()` 之前）
- Test: `workspace_assist_gate_test.go`（新增）；`TestAssistImplementationsTakeRootContextFirst` **不修改**

**Interfaces:**
- Consumes（Task 3）：`requireWorkspaceConfirmed`、`requireConfirmationError`、`makePending`；既有測試輔助 `newTestAppAssist`、`newTestAppGitAssist`、`plantPlannerBin`、`approveGate1Spec`、`runnerFunc`、`runnerMustNotBeCalled`。
- Produces：`App.hookAssistGateRejected func(ctx context.Context)`（測試注入；production 恆為 nil）、測試輔助 `countingRunner(*atomic.Int32) assist.Runner`、`assistActiveCount(a) int`。

**精確位置（逐行標出，spec H2 要求）：**

| 入口 | 位置 | 說明 |
|---|---|---|
| `specAssist` | base 行 6324 `ctx, cancel := context.WithTimeout(a.procRoot(), assistTimeout)` → 行 6331 `purpose = "spec_assist"` → **（新增閘門）** → 行 6332 `gen := &assistGen{…}` | 第一個敘述（`knownProvider` 守門之後）是 root context、緊接 purpose pin，閘門在兩者**之後**；拒絕時 gen 尚未建立，所以在 purpose pin 之後、閘門**之前**先 `defer cancel()`（spec §5.4 要求的「拒絕時一定會執行的 cancel 收尾」），拒絕分支再保留 inline `cancel()`（與既有 `ErrAssistActive` 分支，行 6339–6343，同一個慣例；`CancelFunc` 可重複呼叫，之後既有 `teardown` 再取消也無害，不需要條件式解除），不依賴 gen 建立之後才安排的 `teardown` |
| `planAssist` | base 行 6515 root context → 行 6516 `defer cancel()` → **（新增閘門）** → 行 6518 `ensureGate()` | 拒絕時由行 6516 的既有 `defer cancel()` 取消，不需更動 |

- [ ] **Step 1：寫失敗測試**

```go
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/assist"
	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// ---- B4 R2／§5.4：SpecAssist／PlanAssist 的閘門 ----
//
// 拒絕時要同時證明四件事：回 workspace_confirmation_required；本次的 context 已被取消
// （不依賴 gen 建立之後才安排的 teardown）；gen 沒有登記、assistActive 沒有新增項目；
// runner／factory／preflight／gate journal 一個都沒碰。既有的 root＋purpose 形狀測試
// （TestAssistImplementationsTakeRootContextFirst）不修改，也必須維持通過。

func assistActiveCount(a *App) int {
	a.assistMu.Lock()
	defer a.assistMu.Unlock()
	return len(a.assistActive)
}

func countingRunner(calls *atomic.Int32) assist.Runner {
	return runnerFunc(func(_ context.Context, _ string, sink func(contract.Envelope)) error {
		calls.Add(1)
		sink(contract.Wrap(contract.Event{Provider: contract.ProviderClaude,
			Kind: contract.KindDelta, Text: "draft", Raw: []byte(`{}`)}, ""))
		return nil
	})
}

func TestSpecAssistGate_RejectedCancelsContextAndRegistersNothing(t *testing.T) {
	var runs atomic.Int32
	runner := countingRunner(&runs)
	a := newTestAppAssist(t, runner)
	var factoryCalls atomic.Int32
	a.assistRunnerFactory = func(string) (assist.Runner, error) { factoryCalls.Add(1); return runner, nil }
	makePending(a)
	var rejectedCtx context.Context
	a.hookAssistGateRejected = func(ctx context.Context) { rejectedCtx = ctx }

	for _, provider := range []string{"claude", "codex"} {
		rejectedCtx = nil
		corr, err := a.SpecAssist(provider, "spec_assist", "draft")
		requireConfirmationError(t, err)
		if corr != "" {
			t.Fatalf("%s：拒絕時不得有 correlation id（gen 未建立）：%q", provider, corr)
		}
		if rejectedCtx == nil {
			t.Fatalf("%s：拒絕分支必須走到 hookAssistGateRejected", provider)
		}
		if !errors.Is(rejectedCtx.Err(), context.Canceled) {
			t.Fatalf("%s：本次呼叫的 context 必須已被取消（Err=%v）", provider, rejectedCtx.Err())
		}
		if n := assistActiveCount(a); n != 0 {
			t.Fatalf("%s：拒絕時 assistActive 不得新增項目（%d）", provider, n)
		}
	}
	if runs.Load() != 0 || factoryCalls.Load() != 0 {
		t.Fatalf("拒絕時不得建立或執行 runner（runs=%d factory=%d）", runs.Load(), factoryCalls.Load())
	}

	// 既有檢查的優先順序不變：未知 provider 仍先回 unknown provider。
	if _, err := a.SpecAssist("bogus", "spec_assist", "draft"); err == nil ||
		!strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("未知 provider 必須維持 unknown provider：%v", err)
	}

	// 拒絕沒有洩漏任何獨佔性狀態：確認之後同一個 provider 的 Assist 可以正常執行。
	if err := a.ConfirmWorkspace(a.workspaceDir); err != nil {
		t.Fatal(err)
	}
	if _, err := a.SpecAssist("claude", "spec_assist", "draft"); err != nil {
		t.Fatalf("確認之後 SpecAssist 必須通過（不得有 ErrAssistActive 洩漏）：%v", err)
	}
	if runs.Load() != 1 {
		t.Fatalf("確認之後 runner 應被執行一次：%d", runs.Load())
	}
}

func TestPlanAssistGate_RejectedBeforeEnsureGateAndCancelsContext(t *testing.T) {
	var runs atomic.Int32
	a, _ := newTestAppGitAssist(t, countingRunner(&runs))
	plantPlannerBin(t, a, "claude", "2.1.223 (Claude Code)")
	makePending(a)
	var rejectedCtx context.Context
	a.hookAssistGateRejected = func(ctx context.Context) { rejectedCtx = ctx }

	// 沒有 active Gate 1：若確認閘排在 ensureGate／gateList 之後，這裡會先回
	// 「無生效規格核可」。回的是確認錯誤，就證明閘門在 ensureGate 之前。
	corr, err := a.PlanAssist("claude", "draft plan")
	requireConfirmationError(t, err)
	if corr != "" {
		t.Fatalf("拒絕時不得有 correlation id：%q", corr)
	}
	if rejectedCtx == nil || !errors.Is(rejectedCtx.Err(), context.Canceled) {
		t.Fatalf("本次呼叫的 context 必須已被取消（由既有的 defer cancel）：%v", rejectedCtx)
	}
	if n := assistActiveCount(a); n != 0 {
		t.Fatalf("拒絕時 assistActive 不得新增項目：%d", n)
	}
	if _, serr := os.Stat(filepath.Join(a.stateDir, "gate.jsonl")); !os.IsNotExist(serr) {
		t.Fatal("拒絕時不得觸發 ensureGate（gate.jsonl 不應被建立）")
	}
	if runs.Load() != 0 {
		t.Fatal("拒絕時不得執行 runner")
	}

	if _, err := a.PlanAssist("bogus", "draft plan"); err == nil || !strings.Contains(err.Error(), "unknown provider") {
		t.Fatalf("未知 provider 必須維持 unknown provider：%v", err)
	}

	// 確認之後：既有的前置檢查照舊（沒有 Gate 1 → 原本的錯誤），核可 Gate 1 後完整通過。
	if err := a.ConfirmWorkspace(a.workspaceDir); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PlanAssist("claude", "draft plan"); err == nil || !strings.Contains(err.Error(), "Gate 1") {
		t.Fatalf("確認之後必須回到既有的前置檢查（無 Gate 1）：%v", err)
	}
	approveGate1Spec(t, a)
	if _, err := a.PlanAssist("claude", "draft plan"); err != nil {
		t.Fatalf("確認且前置條件齊備後 PlanAssist 必須通過：%v", err)
	}
	if runs.Load() != 1 {
		t.Fatalf("runner 應被執行一次：%d", runs.Load())
	}
}
```

- [ ] **Step 2：跑一次確認失敗（保留首次紅燈）**

```bash
run_logged t4-red go test -run 'TestSpecAssistGate|TestPlanAssistGate' -count=1 .
show t4-red '(\[build failed\]|undefined)' 8
```

Expected：rc=1，`[build failed]`：`a.hookAssistGateRejected undefined`。

- [ ] **Step 3：最小實作**

(3a) `app.go` `App` struct，`hookAssistBeforeTxn` 欄位（base 行 546）之後加入：

```go
	// hookAssistGateRejected：測試注入——Assist 被 B4 確認閘拒絕時，在 return 之前以**本次
	// 呼叫自己的 context** 呼叫（specAssist 在拒絕分支的 inline cancel() 之前、planAssist 在既有
	// defer cancel() 之前）。測試在呼叫返回後檢查該 context 已被取消。production 恆為 nil。
	hookAssistGateRejected func(ctx context.Context)
```

(3b) `specAssist`：緊接 `purpose = "spec_assist"` 之後、`gen := &assistGen{` 之前。**依 Codex plan review001，採 `defer cancel()`＋拒絕分支 inline `cancel()` 雙重保護**（符合 spec §5.4「確認閘之前先安排一定會執行的 cancel 收尾」）：

```go
	// B4 確認閘（spec §5.4 固定順序）：外層交易 → knownProvider → root context → purpose pin
	// → 拒絕時一定會執行的 cancel 收尾 → 確認閘 → gen／assistActive／runner。閘門不得插在
	// root context 與 purpose pin 之間（TestAssistImplementationsTakeRootContextFirst）。
	// 拒絕時 gen 尚未建立，所以取消不能依賴 gen 建立之後才安排的 teardown：這裡先 defer cancel()，
	// 拒絕分支再 inline cancel() 一次（CancelFunc 可重複呼叫；通過閘門之後既有的 teardown 再取消
	// 也無害，不需要條件式解除）。拒絕時不登記 gen、不進 assistActive、不執行 runner。
	// 不新增內層 beginTxn。
	defer cancel()
	if gerr := a.requireWorkspaceConfirmed("spec_assist", provider, ""); gerr != nil {
		if h := a.hookAssistGateRejected; h != nil {
			h(ctx)
		}
		cancel()
		return "", gerr
	}
```

(3c) `planAssist` **不需更動取消保護**：它本來就有緊接 root context 的既有 `defer cancel()`。緊接該行與其後空行之後、`if _, err := a.ensureGate(); err != nil {` 之前加入：

```go
	// B4 確認閘（spec §5.4）：root context 與既有的 defer cancel() 之後、ensureGate() 之前。
	// 拒絕時由上面的 defer cancel() 取消本次 context；不登記 gen、不跑 gate／git／preflight／
	// runner。不新增內層 beginTxn。
	if gerr := a.requireWorkspaceConfirmed("plan_assist", provider, ""); gerr != nil {
		if h := a.hookAssistGateRejected; h != nil {
			h(ctx)
		}
		return "", gerr
	}

```

- [ ] **Step 4：跑測試確認通過，並確認形狀測試與所有既有 Assist 測試不修改就通過**

```bash
run_logged t4-gofmt gofmt -l . ; wc -c < $EVID/t4-gofmt.log                                # Expected：0
run_logged t4-vet go vet . ; show t4-vet                                                    # Expected：rc=0（lostcancel 不得報）
run_logged t4-green go test -race -run 'TestSpecAssistGate|TestPlanAssistGate|TestAssistImplementationsTakeRootContextFirst|TestEveryBindingIsClassified|TestOnlyThinWrappersAdmit|TestProtectedImplementations|Assist' -count=1 -v .
show t4-green '^(--- |ok|FAIL)' 60
git diff --stat -- app_binding_surface_test.go                                              # Expected：空
```

Expected：rc=0、全部 PASS；`TestAssistImplementationsTakeRootContextFirst` 沒有被修改。

- [ ] **Step 5：Mutation 證據**

```bash
# M1：specAssist 拒絕分支拿掉**所有**取消保護（新加的 defer cancel() 與 inline cancel() 都拿掉）
#     ——只拿掉其中一個不是有效的 mutant：另一個仍會取消，測試正確地維持綠燈
cp app.go $EVID/t4-m1.orig
python3 - <<'EOF'
s = open('app.go', encoding='utf-8').read()
d = '''	defer cancel()
	if gerr := a.requireWorkspaceConfirmed("spec_assist", provider, ""); gerr != nil {'''
i = '''			h(ctx)
		}
		cancel()
		return "", gerr'''
assert d in s and i in s
s = s.replace(d, '''	if gerr := a.requireWorkspaceConfirmed("spec_assist", provider, ""); gerr != nil {''', 1)
s = s.replace(i, '''			h(ctx)
		}
		return "", gerr''', 1)
open('app.go', 'w', encoding='utf-8').write(s)
EOF
mutant_check t4-m1 'context 必須已被取消' go test -run 'TestSpecAssistGate' -count=1 .
restore_check $EVID/t4-m1.orig app.go

# M2：planAssist 的閘門挪到 ensureGate 之後
cp app.go $EVID/t4-m2.orig
python3 - <<'EOF'
s = open('app.go', encoding='utf-8').read()
blk = '''	if gerr := a.requireWorkspaceConfirmed("plan_assist", provider, ""); gerr != nil {
		if h := a.hookAssistGateRejected; h != nil {
			h(ctx)
		}
		return "", gerr
	}

'''
assert blk in s
s = s.replace(blk, '', 1)
anchor = '''	if _, err := a.ensureGate(); err != nil {
		return "", err
	}
	entries, err := a.gateList()'''
assert anchor in s
s = s.replace(anchor, '''	if _, err := a.ensureGate(); err != nil {
		return "", err
	}
''' + blk + '''	entries, err := a.gateList()''', 1)
open('app.go', 'w', encoding='utf-8').write(s)
EOF
mutant_check t4-m2 'gate.jsonl 不應被建立' go test -run 'TestPlanAssistGate' -count=1 .
restore_check $EVID/t4-m2.orig app.go

# M3：specAssist 的閘門（連同 defer）插在 root context 與 purpose pin 之間（違反 spec 固定順序）
cp app.go $EVID/t4-m3.orig
python3 - <<'EOF'
s = open('app.go', encoding='utf-8').read()
a0 = s.index('	// B4 確認閘（spec §5.4 固定順序）')
a1 = s.index('	gen := &assistGen{', a0)
blk = s[a0:a1]
s = s[:a0] + s[a1:]
anchor = '	ctx, cancel := context.WithTimeout(a.procRoot(), assistTimeout)\n	// Pin the assist purpose'
assert anchor in s
s = s.replace(anchor, '	ctx, cancel := context.WithTimeout(a.procRoot(), assistTimeout)\n' + blk + '	// Pin the assist purpose', 1)
open('app.go', 'w', encoding='utf-8').write(s)
EOF
mutant_check t4-m3 '^--- FAIL: TestAssistImplementationsTakeRootContextFirst' go test -run 'TestAssistImplementationsTakeRootContextFirst' -count=1 .
restore_check $EVID/t4-m3.orig app.go
```

Expected：三個 `mutant_check` 都印出「紅在目標」：M1 → `本次呼叫的 context 必須已被取消（Err=<nil>）`；M2 → `拒絕時不得觸發 ensureGate（gate.jsonl 不應被建立）`；M3 → `TestAssistImplementationsTakeRootContextFirst`（既有守門有效）。

- [ ] **Step 6：Commit**

```bash
git add workspace_assist_gate_test.go app.go
git commit -m "$(cat <<'EOF'
feat(b4): specAssist／planAssist 的確認閘（Task 4）

specAssist：purpose pin 之後先 defer cancel()、再確認閘（拒絕分支保留 inline cancel()），gen 之前；
planAssist：既有 defer cancel() 之後、ensureGate 之前。拒絕時不登記 gen、不進 assistActive、不跑 runner／
preflight。不新增內層 beginTxn；root＋purpose 形狀測試不修改即通過。

Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 5：audit 事件＋並行行為測試

**Files:**
- Modify: `workspace_confirmation.go`（`confirmWorkspace` 與 `requireWorkspaceConfirmed` 加 audit）
- Test: `workspace_audit_test.go`（新增）

**Interfaces:**
- Consumes：`a.audit(kind string, v any)`（`app.go:3448`，忽略寫入錯誤＝best-effort）、`a.hookAudit`（測試注入，在 `a.audit` 內、呼叫端 goroutine 上、取 `auditMu` 之前執行）、`enableAudit`／`auditRecordsOfKind`（既有測試輔助）、`workspaceConfirmation.confirm` 回傳的 `transitioned`。
- Produces：audit kind `workspace_confirmed`（`{workspace, source, reasons}`）、`workspace_confirmation_blocked`（`{workspace, source, reasons, op, provider?, wsid?}`）；測試輔助 `readAuditRaw`。

- [ ] **Step 1：寫失敗測試**

涵蓋：`workspace_confirmed` 每次狀態轉換只寫一筆（路徑不符、重複確認、NotRequired、查詢 status 都不寫）、blocked 的欄位與不含 prompt、audit 不持有 mutex（`hookAudit` 內 `TryLock`）、**確認的狀態轉換完成之後啟動才可能通過**（讓 Confirm 停在「狀態已轉換、audit 尚未寫完」，此時啟動必須已通過）、併發壓力（16 個 Confirm＋16 個閘門＋status，搭配 `-race`）：

```go
package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slam0504/sdlc-workbench/internal/assist"
)

// ---- B4 §5.5：audit 事件；§6.1：確認與啟動的並行行為 ----

func TestWorkspaceConfirmedAudit_OncePerTransitionWithFields(t *testing.T) {
	a, _ := newTestApp(t)
	enableAudit(t, a)
	makePending(a)

	_ = a.ConfirmWorkspace(a.workspaceDir + "/wrong")          // 路徑不符：不得有成功 audit
	if _, err := a.WorkspaceConfirmationStatus(); err != nil { // 查詢 status 不寫 audit
		t.Fatal(err)
	}
	if n := len(auditRecordsOfKind(t, a, "workspace_confirmed")); n != 0 {
		t.Fatalf("確認之前（含失敗的確認、查詢 status）不得有 workspace_confirmed：%d", n)
	}

	if err := a.ConfirmWorkspace(a.workspaceDir); err != nil {
		t.Fatal(err)
	}
	if err := a.ConfirmWorkspace(a.workspaceDir); err != nil { // 重複確認：冪等，不重寫
		t.Fatal(err)
	}
	recs := auditRecordsOfKind(t, a, "workspace_confirmed")
	if len(recs) != 1 {
		t.Fatalf("workspace_confirmed 只在 Pending→Confirmed 寫一次，實得 %d 筆", len(recs))
	}
	if recs[0]["workspace"] != a.workspaceDir || recs[0]["source"] != "home" {
		t.Fatalf("欄位不符：%v", recs[0])
	}
	if !reflect.DeepEqual(recs[0]["reasons"], []any{wsReasonSourceHome, wsReasonIsHome}) {
		t.Fatalf("reasons 不符：%v", recs[0]["reasons"])
	}

	b, _ := newTestApp(t) // NotRequired：冪等成功，不寫 audit
	enableAudit(t, b)
	if err := b.ConfirmWorkspace(b.workspaceDir); err != nil {
		t.Fatal(err)
	}
	if n := len(auditRecordsOfKind(t, b, "workspace_confirmed")); n != 0 {
		t.Fatalf("NotRequired 的 ConfirmWorkspace 不寫 audit：%d", n)
	}
}

func TestWorkspaceBlockedAudit_FieldsAndNoPrompt(t *testing.T) {
	a := newTestAppAssist(t, runnerMustNotBeCalled(t))
	a.wsReg = &stubRegistry{}
	enableAudit(t, a)
	w := mustCreate(t, a, "claude")
	makePending(a)

	const secret = "TOP-SECRET-PROMPT-TEXT"
	requireConfirmationError(t, a.StartSession(string(w), secret, "", "", "t", ""))
	requireConfirmationError(t, a.SendMessage(string(w), secret))
	_, err := a.SpecAssist("codex", "spec_assist", secret)
	requireConfirmationError(t, err)
	_, err = a.PlanAssist("claude", secret)
	requireConfirmationError(t, err)

	recs := auditRecordsOfKind(t, a, "workspace_confirmation_blocked")
	if len(recs) != 4 {
		t.Fatalf("每一次被拒絕的請求寫一筆，期望 4 筆，實得 %d：%v", len(recs), recs)
	}
	want := []struct {
		op, provider string
		hasWSID      bool
	}{
		{"start_session", "claude", true}, {"send_message", "claude", true},
		{"spec_assist", "codex", false}, {"plan_assist", "claude", false},
	}
	for i, wnt := range want {
		r := recs[i]
		if r["op"] != wnt.op || r["provider"] != wnt.provider {
			t.Fatalf("第 %d 筆 op／provider 不符：%v", i, r)
		}
		if _, has := r["wsid"]; has != wnt.hasWSID {
			t.Fatalf("第 %d 筆 wsid 欄位的有無不符（want %v）：%v", i, wnt.hasWSID, r)
		}
		if r["workspace"] != a.workspaceDir || r["source"] != "home" ||
			!reflect.DeepEqual(r["reasons"], []any{wsReasonSourceHome, wsReasonIsHome}) {
			t.Fatalf("第 %d 筆 workspace／source／reasons 不符：%v", i, r)
		}
	}
	if raw := readAuditRaw(t, a); strings.Contains(raw, secret) {
		t.Fatal("audit 不得包含 prompt")
	}

	if err := a.ConfirmWorkspace(a.workspaceDir); err != nil {
		t.Fatal(err)
	}
	var runs atomic.Int32
	a.assistRunnerFactory = func(string) (assist.Runner, error) { return countingRunner(&runs), nil }
	if _, err := a.SpecAssist("codex", "spec_assist", "ok"); err != nil || runs.Load() != 1 {
		t.Fatalf("確認之後 SpecAssist 必須通過閘門並執行 runner：err=%v runs=%d", err, runs.Load())
	}
	if n := len(auditRecordsOfKind(t, a, "workspace_confirmation_blocked")); n != 4 {
		t.Fatalf("確認之後通過閘門的請求不得再寫 blocked：%d", n)
	}
}

// H4／spec §6.1：audit 必須在不持有 workspaceConfirm mutex 的情況下寫入。hookAudit 在
// a.audit 內、呼叫端的 goroutine 上執行——此刻若鎖還被持有，TryLock 會失敗。
func TestWorkspaceConfirmationAudit_WrittenOutsideMutex(t *testing.T) {
	a, _ := newTestApp(t)
	a.wsReg = &stubRegistry{}
	enableAudit(t, a)
	w := mustCreate(t, a, "claude")
	makePending(a)
	var checked atomic.Int32
	a.hookAudit = func(kind string) {
		if kind != "workspace_confirmed" && kind != "workspace_confirmation_blocked" {
			return
		}
		checked.Add(1)
		if !a.workspaceConfirm.mu.TryLock() {
			t.Errorf("%s 的 audit 在持有 workspaceConfirm mutex 時被寫入", kind)
			return
		}
		a.workspaceConfirm.mu.Unlock()
	}
	requireConfirmationError(t, a.StartSession(string(w), "hi", "", "", "t", ""))
	if err := a.ConfirmWorkspace(a.workspaceDir); err != nil {
		t.Fatal(err)
	}
	if checked.Load() != 2 {
		t.Fatalf("兩種 audit 都必須被檢查到：%d", checked.Load())
	}
}

// 「只有在確認的狀態轉換完成之後，啟動才可能通過」——行為斷言，不只靠 -race。
// 做法：轉換之前先確認啟動被拒絕；再讓 Confirm 停在「狀態已轉換、audit 尚未寫完」這個點
// （hookAudit 阻塞），此時啟動必須已經可以通過——轉換已完成，而且閘門沒有持有鎖或等 audit。
func TestConfirmTransitionCompletesBeforeStartCanPass(t *testing.T) {
	a, _ := newTestApp(t)
	a.wsReg = &stubRegistry{}
	enableAudit(t, a)
	writeCountingClaude(t, a)
	w := mustCreate(t, a, "claude")
	makePending(a)

	requireConfirmationError(t, a.StartSession(string(w), "hi", "", "", "t", "")) // 轉換之前：拒絕

	inAudit, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	doRelease := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(doRelease) // 失敗路徑也要放行被擋住的 goroutine，否則 fixture 收尾會等不到 inflight 交易
	a.hookAudit = func(kind string) {
		if kind == "workspace_confirmed" {
			close(inAudit)
			<-release
		}
	}
	confirmDone := make(chan error, 1)
	go func() { confirmDone <- a.ConfirmWorkspace(a.workspaceDir) }()
	select {
	case <-inAudit:
	case <-time.After(5 * time.Second):
		t.Fatal("Confirm 沒有走到 audit")
	}
	// 此刻：狀態已轉換、audit 被擋住。啟動必須通過，而且不能被 audit 擋住。
	// （刻意不在這裡直接讀 snapshot()：若實作錯把 audit 放進鎖內，讀 snapshot 會跟著死鎖，
	// 測試就卡住而不是失敗。）
	startDone := make(chan error, 1)
	go func() { startDone <- a.StartSession(string(w), "hi", "", "", "t", "") }()
	select {
	case err := <-startDone:
		if err != nil {
			t.Fatalf("狀態轉換完成之後啟動必須通過：%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("啟動被確認的 audit 擋住了（閘門不得等 audit，也不得持有鎖等 audit）")
	}
	t.Cleanup(func() { _ = a.EndSession(string(w)) })
	doRelease()
	if err := <-confirmDone; err != nil {
		t.Fatal(err)
	}
}

// 併發壓力（搭配 -race）：多個 Confirm 同時進來只會有一筆 workspace_confirmed；閘門的結果
// 只可能是「通過」或「確認錯誤」，而且在同一條 goroutine 上一旦通過就不會再被拒絕。
func TestConcurrentConfirmAndGate_SingleAuditAndMonotonic(t *testing.T) {
	a, _ := newTestApp(t)
	enableAudit(t, a)
	makePending(a)
	const confirmers, gaters, rounds = 16, 16, 200
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < confirmers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := a.ConfirmWorkspace(a.workspaceDir); err != nil {
				t.Errorf("ConfirmWorkspace: %v", err)
			}
		}()
	}
	for i := 0; i < gaters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			passed := false
			for r := 0; r < rounds; r++ {
				err := a.requireWorkspaceConfirmed("test", "claude", "")
				switch {
				case err == nil:
					passed = true
				case passed:
					t.Errorf("閘門通過之後又被拒絕（狀態倒退）：%v", err)
					return
				case !strings.HasPrefix(err.Error(), "workspace_confirmation_required"):
					t.Errorf("閘門只能回通過或確認錯誤：%v", err)
					return
				}
				if _, serr := a.WorkspaceConfirmationStatus(); serr != nil {
					t.Errorf("status: %v", serr)
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	if n := len(auditRecordsOfKind(t, a, "workspace_confirmed")); n != 1 {
		t.Fatalf("併發確認必須只寫一筆 workspace_confirmed，實得 %d", n)
	}
	if err := a.requireWorkspaceConfirmed("test", "claude", ""); err != nil {
		t.Fatalf("全部完成後閘門必須通過：%v", err)
	}
}

// readAuditRaw：audit.jsonl 全文（找不到回空字串）。
func readAuditRaw(t *testing.T, a *App) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(a.stateDir, "audit.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	return string(b)
}
```

- [ ] **Step 2：跑一次確認失敗（保留首次紅燈）**

```bash
run_logged t5-red go test -race -run 'TestWorkspaceConfirmedAudit|TestWorkspaceBlockedAudit|TestWorkspaceConfirmationAudit|TestConfirmTransition|TestConcurrentConfirm' -count=1 -timeout 120s .
show t5-red '^(--- FAIL|FAIL)|workspace_audit_test' 20
```

Expected：rc=1；五條測試因「還沒有 audit」而紅（count 為 0、`兩種 audit 都必須被檢查到：0`、`Confirm 沒有走到 audit`）。

- [ ] **Step 3：最小實作**

`workspace_confirmation.go`：

(3a) `confirmWorkspace` 換成（audit 在 `confirm` 的鎖之外寫；「第一次」由 `transitioned` 決定）。舊版：

```go
func (a *App) confirmWorkspace(expectedPath string) error {
	_, _, err := a.workspaceConfirm.confirm(expectedPath)
	return err
}
```

新版：

```go
// confirmWorkspace：R3 + §5.5。「是否為第一次確認」由 confirm 的單一臨界區決定
// （transitioned），audit 在鎖外寫——所以 workspace_confirmed 只會有一筆，也沒有鎖序問題。
// audit 沿用 a.audit 的 best-effort：寫入失敗不影響確認，也不是放行的條件。
func (a *App) confirmWorkspace(expectedPath string) error {
	transitioned, snap, err := a.workspaceConfirm.confirm(expectedPath)
	if err != nil {
		return err
	}
	if transitioned {
		info := snap.info()
		a.audit("workspace_confirmed", map[string]any{
			"workspace": info.Workspace, "source": info.Source, "reasons": info.Reasons})
	}
	return nil
}
```

(3b) `requireWorkspaceConfirmed` 在拒絕分支加 audit（`snapshot()` 已釋放鎖）。舊版：

```go
func (a *App) requireWorkspaceConfirmed(op, provider, wsid string) error {
	snap := a.workspaceConfirm.snapshot()
	if snap.state == wcNotRequired || snap.state == wcConfirmed {
		return nil
	}
	info := snap.info()
	return fmt.Errorf("%w：目前的 workspace（%s，來源 %s）需要先在畫面頂端確認，確認前不會開始任何 AI 工作（原因：%s）",
		errWorkspaceConfirmationRequired, info.Workspace, info.Source, strings.Join(info.Reasons, ","))
}
```

新版（doc 注解中「自己不做 I/O…」那句同步改成「拒絕時寫一筆 best-effort 的 workspace_confirmation_blocked audit（不含 prompt）」）：

```go
func (a *App) requireWorkspaceConfirmed(op, provider, wsid string) error {
	snap := a.workspaceConfirm.snapshot()
	if snap.state == wcNotRequired || snap.state == wcConfirmed {
		return nil
	}
	info := snap.info()
	// 每一次真正被拒絕的 AI 工作請求寫一筆（§5.5）；不含 prompt。鎖已在 snapshot() 內釋放。
	data := map[string]any{"workspace": info.Workspace, "source": info.Source,
		"reasons": info.Reasons, "op": op}
	if provider != "" {
		data["provider"] = provider
	}
	if wsid != "" {
		data["wsid"] = wsid
	}
	a.audit("workspace_confirmation_blocked", data)
	return fmt.Errorf("%w：目前的 workspace（%s，來源 %s）需要先在畫面頂端確認，確認前不會開始任何 AI 工作（原因：%s）",
		errWorkspaceConfirmationRequired, info.Workspace, info.Source, strings.Join(info.Reasons, ","))
}
```

- [ ] **Step 4：跑測試（含 `-race`、重複執行抓間歇性）**

```bash
run_logged t5-gofmt gofmt -l . ; wc -c < $EVID/t5-gofmt.log                                # Expected：0
run_logged t5-vet go vet . ; show t5-vet                                                    # Expected：rc=0
run_logged t5-green go test -race -run 'TestWorkspaceConfirmedAudit|TestWorkspaceBlockedAudit|TestWorkspaceConfirmationAudit|TestConfirmTransition|TestConcurrentConfirm' -count=5 -timeout 300s -v .
show t5-green '^(--- FAIL|FAIL|ok|WARNING: DATA RACE)' 20
run_logged t5-prev go test -race -run 'TestStartSessionGate|TestSendMessageGate|TestSpecAssistGate|TestPlanAssistGate|TestProductionStartupPath' -count=1 .
show t5-prev '' 5
```

Expected：兩組 rc=0，沒有 `WARNING: DATA RACE`；後者確認 blocked audit 沒有破壞 Task 3／4 的測試。

- [ ] **Step 5：Mutation 證據**

```bash
# M1：audit 放進鎖內
cp workspace_confirmation.go $EVID/t5-m1.orig
python3 - <<'EOF'
s = open('workspace_confirmation.go', encoding='utf-8').read()
old = '''		info := snap.info()
		a.audit("workspace_confirmed", map[string]any{
			"workspace": info.Workspace, "source": info.Source, "reasons": info.Reasons})'''
assert old in s
open('workspace_confirmation.go', 'w', encoding='utf-8').write(s.replace(old, '''		info := snap.info()
		a.workspaceConfirm.mu.Lock()
		a.audit("workspace_confirmed", map[string]any{
			"workspace": info.Workspace, "source": info.Source, "reasons": info.Reasons})
		a.workspaceConfirm.mu.Unlock()''', 1))
EOF
mutant_check t5-m1 '^--- FAIL: (TestWorkspaceConfirmationAudit_WrittenOutsideMutex|TestConfirmTransitionCompletesBeforeStartCanPass)' go test -run 'TestWorkspaceConfirmationAudit_WrittenOutsideMutex|TestConfirmTransitionCompletesBeforeStartCanPass' -count=1 -timeout 90s .
restore_check $EVID/t5-m1.orig workspace_confirmation.go

# M2：每次 Confirm 都寫 audit（不看 transitioned）
cp workspace_confirmation.go $EVID/t5-m2.orig
sed -i '' 's/	if transitioned {/	if transitioned || true {/' workspace_confirmation.go
mutant_check t5-m2 '^--- FAIL: (TestWorkspaceConfirmedAudit_OncePerTransitionWithFields|TestConcurrentConfirmAndGate_SingleAuditAndMonotonic)' go test -run 'TestWorkspaceConfirmedAudit|TestConcurrentConfirm' -count=1 -timeout 90s .
restore_check $EVID/t5-m2.orig workspace_confirmation.go
```

Expected：兩個 `mutant_check` 都印出「紅在目標」，**而且不會卡住整個 run**（測試已用 `t.Cleanup` 放行被擋住的 goroutine，且不在鎖內直接讀 `snapshot()`；若出現 `panic: test timed out`，`mutant_check` 會判為非目標失敗）。M1 → `WrittenOutsideMutex` 紅、`ConfirmTransitionCompletesBeforeStartCanPass` 在 5 秒後紅（`啟動被確認的 audit 擋住了`）；M2 → `OncePerTransition…` 紅（2 筆）、`ConcurrentConfirm…` 紅（16 筆）。

- [ ] **Step 6：Commit**

```bash
git add workspace_confirmation.go workspace_audit_test.go
git commit -m "$(cat <<'EOF'
feat(b4): workspace_confirmed／workspace_confirmation_blocked audit＋並行行為測試（Task 5）

audit 在 workspaceConfirm mutex 之外寫入，「是否第一次確認」由單一狀態轉換決定。以 hookAudit
讓 Confirm 停在轉換完成之後，斷言此時啟動已可通過；另有 16×16 併發壓力測試（搭配 -race）。

Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 6：Codex 登入邊界驗證（H3）＋同類入口（owner 裁定加入）

**Files:**
- Test: `workspace_login_boundary_test.go`（新增；**無 production 變更**）

**Interfaces:**
- Consumes：`a.codexServerFactory`（測試注入的 app-server 工廠）、`fakeCodexServer`／`fakeCodexWire`（既有 fake）、`codex.Method*` 常數。
- Produces：`methodLogWire`（記錄每個 client request 的 method 並回應 `initialize`／`account/read`／`account/login/start`／`account/logout`／`thread/start`／`turn/start`）。

**四個「不加閘門」入口實際送出什麼（先讀過 `app.go` 再寫測試；行號為 base）：**

| 入口 | 實作路徑 | 送出的 method |
|---|---|---|
| `AuthStatus`（8890） | codex：`ensureAppServer` → `account/read`；claude：`exec claude auth status`（無 `Dir`） | `initialize`（握手）、`account/read` |
| `StartLogin`（8923） | codex：`ensureAppServer` → `account/login/start` | `initialize`、`account/login/start` |
| `Logout`（9028；實作 9036） | codex：`ensureAppServer` → `account/logout`；claude：`exec claude auth logout` | `initialize`、`account/logout` |
| `RestartCodexServerRecorded`（8846；實作 8861）／`RecoverCodexRecording`（8150；實作 8170，live host 檢查 8134） | `codexServerMu` → `refuseIfCodexLive`（有 live host 就拒絕）→ `replaceCodexGeneration`：`codex.StartAppServer`（工廠，cwd＝`a.workspaceDir`，`app.go:8086`）→ `RunOwnedHandshake` | 只有 `initialize` 握手；沒有任何 thread／turn／account method |

`ensureAppServer` 與 `replaceCodexGeneration` 都會以 workspace 為 cwd 啟動（或替換）app-server——這是 owner 已知並接受的邊界；CLI 啟動時自身的初始化寫入行為仍是**未驗證**。

此 task 沒有可寫的「先紅後綠」production 變更——它驗證的是**既有行為**（spec §5.4 末段：登入與狀態查詢只送 `account/read`、`account/login/start`；owner 裁定追加的 `Logout`、`RestartCodexServerRecorded`、`RecoverCodexRecording` 同樣不送 `thread/start`、`turn/start`）。紅燈證據改以 mutation 取得，並以「對照組」證明記錄器看得到 `thread/start`／`turn/start`（oracle 不是恆真）。

- [ ] **Step 1：寫測試**

```go
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/slam0504/sdlc-workbench/internal/codex"
	"github.com/slam0504/sdlc-workbench/internal/contract"
)

// ---- B4 H3／§5.4：Codex 登入與狀態查詢不在閘門範圍內，且不會建立 thread 或 turn ----
//
// spec 的理由：AuthStatus／StartLogin 只送 account/read、account/login/start，沒有 prompt、
// 沒有 turn/start。這裡用 fake app-server 把「登入流程實際送出的每一個 method」記下來驗證，
// 而不是只相信原碼閱讀。對照組：確認之後的 StartSession 走同一個 fake server，必須看得到
// thread/start、turn/start——證明這個記錄器看得到它該看的東西（oracle 不是恆真）。
//
// 範圍之外（spec 明確不做）：app-server 仍以 workspace 為 cwd 被啟動，CLI 自身初始化時的
// 寫入行為**未驗證**；若 owner 要求連這種啟動也禁止，那是另一個邊界。

type methodLogWire struct {
	mu      sync.Mutex
	methods []string
	turnSeq atomic.Int32
}

func (c *methodLogWire) record(m string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.methods = append(c.methods, m)
}

func (c *methodLogWire) seen() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.methods...)
}

func (c *methodLogWire) respond(w *fakeCodexWire, f codex.Frame) {
	var p struct {
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(f.Params, &p)
	switch f.Method {
	case codex.MethodInitialize:
		w.send(map[string]any{"id": *f.ID, "result": map[string]any{}})
	case codex.MethodAccountRead:
		w.send(map[string]any{"id": *f.ID, "result": map[string]any{"account": nil, "requiresOpenaiAuth": true}})
	case codex.MethodAccountLoginStart:
		w.send(map[string]any{"id": *f.ID, "result": map[string]any{"loginId": "login-1", "authUrl": ""}})
	case codex.MethodAccountLogout:
		w.send(map[string]any{"id": *f.ID, "result": map[string]any{}})
	case codex.MethodThreadStart:
		w.send(map[string]any{"id": *f.ID, "result": map[string]any{"thread": map[string]any{"id": "t-login"}}})
	case codex.MethodTurnStart:
		turnID := fmt.Sprintf("turn-%d", c.turnSeq.Add(1))
		w.send(map[string]any{"id": *f.ID, "result": map[string]any{
			"turn": map[string]any{"id": turnID, "status": "inProgress"}}})
		w.send(map[string]any{"method": codex.MethodTurnCompleted, "params": map[string]any{
			"threadId": p.ThreadID, "turn": map[string]any{"id": turnID, "status": "completed"}}})
	}
}

func (c *methodLogWire) newServer(t *testing.T) (codex.ProbeTarget, error) {
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	w := &fakeCodexWire{t: t, out: s2cW}
	s := &fakeCodexServer{
		conn:      codex.NewConn(c2sW, s2cR),
		s2c:       s2cW,
		closeWire: func() { _ = s2cW.Close(); _ = c2sW.Close(); _ = c2sR.Close() },
		steps:     func(string) {},
		done:      make(chan struct{}),
	}
	s.hsFn = func(ctx context.Context, ci codex.ClientInfo) error { return s.conn.Handshake(ctx, ci) }
	go func() {
		sc := bufio.NewScanner(c2sR)
		for sc.Scan() {
			var f codex.Frame
			if json.Unmarshal(sc.Bytes(), &f) != nil || f.Method == "" || f.ID == nil {
				continue // 只處理 client request
			}
			c.record(f.Method)
			c.respond(w, f)
		}
	}()
	t.Cleanup(s.die)
	return s, nil
}

func TestCodexLoginAndStatusSendNoThreadOrTurn(t *testing.T) {
	a, _ := newTestApp(t)
	ctl := &methodLogWire{}
	a.codexServerFactory = func() (codex.ProbeTarget, error) { return ctl.newServer(t) }
	enableAudit(t, a)
	makePending(a) // 未確認：登入與狀態查詢仍須可用（不在閘門範圍內）

	if _, err := a.AuthStatus("codex"); err != nil {
		t.Fatalf("Pending 時 AuthStatus 不得被閘門擋下：%v", err)
	}
	if err := a.StartLogin("codex"); err != nil {
		t.Fatalf("Pending 時 StartLogin 不得被閘門擋下：%v", err)
	}
	got := ctl.seen()
	for _, want := range []string{codex.MethodAccountRead, codex.MethodAccountLoginStart} {
		if !slices.Contains(got, want) {
			t.Fatalf("登入流程應送出 %s（記錄器必須看得到它）：%v", want, got)
		}
	}
	for _, forbidden := range []string{codex.MethodThreadStart, codex.MethodThreadResume, codex.MethodTurnStart} {
		if slices.Contains(got, forbidden) {
			t.Fatalf("登入與狀態查詢不得送出 %s：%v", forbidden, got)
		}
	}
	if n := len(auditRecordsOfKind(t, a, "workspace_confirmation_blocked")); n != 0 {
		t.Fatalf("登入與狀態查詢不被閘門攔截，也就不該寫 blocked audit：%d", n)
	}

	// 對照組：確認之後的 StartSession 走同一個 fake server，記錄器必須看得到 thread/start、turn/start。
	if err := a.ConfirmWorkspace(a.workspaceDir); err != nil {
		t.Fatal(err)
	}
	w := wsidFor(t, a, contract.ProviderCodex)
	if err := a.StartSession(string(w), "hi", "", "", "t", "untrusted"); err != nil {
		t.Fatalf("對照組 StartSession：%v", err)
	}
	t.Cleanup(func() { _ = a.EndSession(string(w)) })
	got = ctl.seen()
	for _, want := range []string{codex.MethodThreadStart, codex.MethodTurnStart} {
		if !slices.Contains(got, want) {
			t.Fatalf("對照組必須看得到 %s，否則這個記錄器驗不到任何東西：%v", want, got)
		}
	}
}

// ---- 同類入口：Logout、RestartCodexServerRecorded、RecoverCodexRecording ----
//
// owner 裁定（2026-10-11）：這三個入口維持 spec 範圍、不加閘門；但要用同一個 fake app-server
// 驗證它們一樣不會送出 thread/start、turn/start。
//   - Logout："codex" 分支走 ensureAppServer，再送 account/logout。
//   - RestartCodexServerRecorded／RecoverCodexRecording：沒有 live host 時走 replaceCodexGeneration
//     （工廠起一個新 server＋initialize 握手），不送任何 thread／turn 相關 method。
// 同樣以確認後的 StartSession 當對照組，證明記錄器看得到 thread/start、turn/start。

func TestCodexServerEntryPointsSendNoThreadOrTurn(t *testing.T) {
	cases := []struct {
		name string
		run  func(a *App) error
		want string // 該入口必須實際送出（或握手）的 method，證明記錄器看得到它
	}{
		{"Logout", func(a *App) error { return a.Logout("codex") }, codex.MethodAccountLogout},
		{"RestartCodexServerRecorded", func(a *App) error { return a.RestartCodexServerRecorded("b4-case") }, codex.MethodInitialize},
		{"RecoverCodexRecording", func(a *App) error { return a.RecoverCodexRecording() }, codex.MethodInitialize},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, _ := newTestApp(t)
			ctl := &methodLogWire{}
			a.codexServerFactory = func() (codex.ProbeTarget, error) { return ctl.newServer(t) }
			enableAudit(t, a)
			makePending(a) // 未確認：這些入口不在閘門範圍內，必須照常可用

			if err := c.run(a); err != nil {
				t.Fatalf("Pending 時 %s 不得被閘門擋下：%v", c.name, err)
			}
			got := ctl.seen()
			if !slices.Contains(got, c.want) {
				t.Fatalf("%s 應送出 %s（記錄器必須看得到它）：%v", c.name, c.want, got)
			}
			for _, forbidden := range []string{codex.MethodThreadStart, codex.MethodThreadResume, codex.MethodTurnStart} {
				if slices.Contains(got, forbidden) {
					t.Fatalf("%s 不得送出 %s：%v", c.name, forbidden, got)
				}
			}
			if n := len(auditRecordsOfKind(t, a, "workspace_confirmation_blocked")); n != 0 {
				t.Fatalf("%s 不被閘門攔截，也就不該寫 blocked audit：%d", c.name, n)
			}

			// 對照組：確認之後的 StartSession 走同一個 fake server。
			if err := a.ConfirmWorkspace(a.workspaceDir); err != nil {
				t.Fatal(err)
			}
			w := wsidFor(t, a, contract.ProviderCodex)
			if err := a.StartSession(string(w), "hi", "", "", "t", "untrusted"); err != nil {
				t.Fatalf("對照組 StartSession：%v", err)
			}
			t.Cleanup(func() { _ = a.EndSession(string(w)) })
			got = ctl.seen()
			for _, want := range []string{codex.MethodThreadStart, codex.MethodTurnStart} {
				if !slices.Contains(got, want) {
					t.Fatalf("對照組必須看得到 %s：%v", want, got)
				}
			}
		})
	}
}
```

- [ ] **Step 2：跑測試（預期直接通過，明確標示「非先紅後綠」）**

```bash
run_logged t6-green go test -race -run 'TestCodexLoginAndStatusSendNoThreadOrTurn|TestCodexServerEntryPointsSendNoThreadOrTurn' -count=3 -v .
show t6-green '^(\s*--- |ok|FAIL|WARNING: DATA RACE)' 40
```

Expected：rc=0、全部 PASS。本測試驗證既有行為，首次執行即綠；紅燈證據見 Step 3 的 mutation。

- [ ] **Step 3：Mutation 證據（測試要紅在正題）——四個入口各一個 mutant**

每個 mutant 都是在該入口的 production 路徑多送一個 `thread/start`；每個 mutant 有自己的備份、log、rc，並在還原後 `cmp`：

```bash
for M in auth logout restart recover; do
cp app.go $EVID/t6-$M.orig
python3 - "$M" <<'EOF'
import sys
m = sys.argv[1]
s = open('app.go', encoding='utf-8').read()
inj = """	if o, ok := a.codexSingle.Current(); ok {
		_, _ = o.Server.Conn().Call(context.Background(), codex.MethodThreadStart, map[string]any{})
	}
"""
if m == 'auth':
    old = "		res, err := srv.Conn().Call(ctx, codex.MethodAccountRead, map[string]any{})"
    assert old in s
    s = s.replace(old, "		_, _ = srv.Conn().Call(ctx, codex.MethodThreadStart, map[string]any{})\n" + old, 1)
elif m == 'logout':
    old = "		_, err = srv.Conn().Call(ctx, codex.MethodAccountLogout, map[string]any{})"
    assert old in s
    s = s.replace(old, "		_, _ = srv.Conn().Call(ctx, codex.MethodThreadStart, map[string]any{})\n" + old, 1)
elif m == 'restart':
    old = '	a.audit("codex_probe_ok", map[string]any{"case": recordCase})'
    assert old in s
    s = s.replace(old, inj + old, 1)
else:
    old = '	a.audit("codex_wire_recovery_ok", map[string]any{})'
    assert old in s
    s = s.replace(old, inj + old, 1)
open('app.go', 'w', encoding='utf-8').write(s)
EOF
case $M in
  auth)    PAT='^--- FAIL: TestCodexLoginAndStatusSendNoThreadOrTurn' ;;
  logout)  PAT='--- FAIL: TestCodexServerEntryPointsSendNoThreadOrTurn/Logout' ;;
  restart) PAT='--- FAIL: TestCodexServerEntryPointsSendNoThreadOrTurn/RestartCodexServerRecorded' ;;
  recover) PAT='--- FAIL: TestCodexServerEntryPointsSendNoThreadOrTurn/RecoverCodexRecording' ;;
esac
mutant_check t6-$M "$PAT" go test -run 'TestCodexLoginAndStatusSendNoThreadOrTurn|TestCodexServerEntryPointsSendNoThreadOrTurn' -count=1 -v .
restore_check $EVID/t6-$M.orig app.go
done
```

Expected：四個 `mutant_check` 都印出「紅在目標」，每個 mutant 只讓對應的那一個入口變紅：`auth` → `登入與狀態查詢不得送出 thread/start：[initialize thread/start account/read account/login/start]`；`logout` → `Logout 不得送出 thread/start：[initialize thread/start account/logout]`；`restart`／`recover` → `…不得送出 thread/start：[initialize thread/start]`。

- [ ] **Step 4：未驗證範圍照實記錄（Fail Loud）**

在 commit message 與回報中寫明：本測試**沒有**驗證 codex CLI 以 workspace 為 cwd 啟動時自身的初始化寫入行為（spec 明列為未驗證）；`Logout`、`RestartCodexServerRecorded`、`RecoverCodexRecording` 依 owner 裁定（2026-10-11 14:53）不加閘門，只驗證不送 `thread/start`、`turn/start`。claude 分支的 `exec claude auth status／logout` 沒有設定 `Dir`，不在這個 fake app-server 的觀察範圍內，**未驗證**。

- [ ] **Step 5：Commit**

```bash
git add workspace_login_boundary_test.go
git commit -m "$(cat <<'EOF'
test(b4): Codex 登入、狀態查詢與同類入口不送 thread/start／turn/start（Task 6，H3）

以記錄每個 client request 的 fake app-server 驗證 AuthStatus／StartLogin 只送 account/read、
account/login/start，Logout／RestartCodexServerRecorded／RecoverCodexRecording（owner 裁定不加閘門）
也不送 thread/start、turn/start；對照組（確認後的 StartSession）證明記錄器看得到它們。
未驗證：codex CLI 啟動時自身的初始化寫入行為（spec 明列為未驗證）。

Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 7：前端

**為什麼一個 task：** store、警示元件、停用條件與 i18n 互相依賴，且共享同一批既有測試的前提調整；拆開會讓中間狀態無法通過 vitest。

**Files:**
- Create: `frontend/src/stores/workspaceConfirmation.ts`、`frontend/src/components/WorkspaceConfirmBanner.vue`、`frontend/src/test/workspaceConfirmation.ts`（測試輔助）
- Create（測試）: `frontend/src/stores/workspaceConfirmation.test.ts`、`frontend/src/components/WorkspaceConfirmBanner.test.ts`、`frontend/src/components/workspaceGate.test.ts`、`frontend/src/workspaceConfirmationApp.test.ts`、`frontend/src/i18n/workspaceReasonKeys.test.ts`
- Modify: `frontend/src/App.vue:317-323`（`refreshCliInfo`）與模板 `.meta` 之後、`frontend/src/components/PaneView.vue:62-70,103-107`、`frontend/src/components/SpecWorkspace.vue:397-410,753-758`、`frontend/src/components/PlanWorkspace.vue:513-526,793,849`、`frontend/src/stores/session.ts:599-621`、`frontend/src/types.ts`（檔尾）、`frontend/src/lib/bindings.ts`、`frontend/src/i18n/stateKeys.ts`、`frontend/src/i18n/locales/{zh-TW,en}.ts`
- Modify（既有測試，只補前提）: `lib/bindings.test.ts`、`i18n/keyRefs.test.ts`、`App.test.ts`、`cliInfoReady.test.ts`、`paneLayout.test.ts`、`registryUncertain.test.ts`（補新 binding 的 mock，依 `App.test.ts` 內「mock 這個 module 的測試檔有跟著補的義務」的註解）；`stores/session.test.ts`、`components/DualPane.test.ts`、`components/PlanWorkspace.test.ts`、`components/SpecWorkspace.test.ts`、`registryUncertain.test.ts`（補「不需確認」的 seed）

**Interfaces:**
- Consumes（Task 2）：`wailsjs` 產生的 `main.WorkspaceConfirmationInfo`、`WorkspaceConfirmationStatus()`、`ConfirmWorkspace(arg1:string)`。
- Produces：
  - `useWorkspaceConfirmation()` store：state `status: WorkspaceStatus | null`、`queryError: string`、`seq: number`、`confirming: boolean`、`confirmError: string`；getters `blockKind: 'ok'|'loading'|'failed'|'pending'`、`blocked: boolean`；actions `refresh(fetchStatus)`、`confirm(confirmFn, refreshFn): Promise<boolean>`；`parseStatus(raw)`（形狀不對丟錯）
  - `WorkspaceConfirmBanner`（props：`confirm: (expectedPath: string) => Promise<void>`、`refresh: () => Promise<void>`）
  - `WorkspaceConfirmationBindings`（`types.ts`）；`makeBindings()` 多兩個欄位
  - `workspaceReasonKeys`、`workspaceBlockKeys`（`i18n/stateKeys.ts`）
  - 測試輔助 `seedWorkspaceConfirmation(over?)`（`src/test/workspaceConfirmation.ts`）
  - i18n key 群組 `workspaceConfirm.*`

- [ ] **Step 1：寫失敗測試（新檔案）**

(1a) store 測試——封鎖判定三態、壞回覆、晚到舊回應（成功與失敗兩種）、原始字串原樣轉發、確認失敗／重新查詢失敗／連點：

```ts
import { setActivePinia, createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { parseStatus, useWorkspaceConfirmation } from './workspaceConfirmation'

// ---- B4 R5：workspace 確認 store ----
// fail closed：只有「已取得 status，且不是 required && !confirmed」才放行。

const PENDING = { required: true, confirmed: false, workspace: '/Users/me', source: 'home', reasons: ['source_home', 'is_home'] }
const CONFIRMED = { ...PENDING, confirmed: true }
const NOT_REQUIRED = { required: false, confirmed: false, workspace: '/work/repo', source: 'env', reasons: [] }

function deferred<T>() {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}

describe('workspace confirmation store：封鎖判定', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('尚未取得 status：封鎖（loading），不能只判 required && !confirmed', () => {
    const wsc = useWorkspaceConfirmation()
    expect(wsc.status).toBeNull()
    expect(wsc.blockKind).toBe('loading')
    expect(wsc.blocked).toBe(true)
  })

  it('查詢失敗：封鎖（failed）並保留錯誤原文', async () => {
    const wsc = useWorkspaceConfirmation()
    await wsc.refresh(async () => { throw new Error('app 尚未完成啟動') })
    expect(wsc.blockKind).toBe('failed')
    expect(wsc.blocked).toBe(true)
    expect(wsc.queryError).toContain('app 尚未完成啟動')
  })

  it('required && !confirmed：封鎖（pending）；已確認或不需確認：放行', async () => {
    const wsc = useWorkspaceConfirmation()
    await wsc.refresh(async () => PENDING)
    expect(wsc.blockKind).toBe('pending')
    expect(wsc.blocked).toBe(true)
    await wsc.refresh(async () => CONFIRMED)
    expect(wsc.blockKind).toBe('ok')
    expect(wsc.blocked).toBe(false)
    await wsc.refresh(async () => NOT_REQUIRED)
    expect(wsc.blocked).toBe(false)
  })

  // Review Focus 4：壞掉的回覆不得解除封鎖，也不得被補成合法資料。
  const BAD: Array<[string, unknown]> = [
    ['空物件', {}],
    ['缺 required', { confirmed: false, workspace: '/w', source: 'env', reasons: [] }],
    ['required 不是 boolean', { required: 'false', confirmed: false, workspace: '/w', source: 'env', reasons: [] }],
    ['缺 confirmed', { required: false, workspace: '/w', source: 'env', reasons: [] }],
    ['缺 workspace', { required: false, confirmed: false, source: 'env', reasons: [] }],
    ['缺 source', { required: false, confirmed: false, workspace: '/w', reasons: [] }],
    ['source 不是字串', { required: false, confirmed: false, workspace: '/w', source: 1, reasons: [] }],
    ['缺 reasons', { required: false, confirmed: false, workspace: '/w', source: 'env' }],
    ['reasons 是 null', { required: false, confirmed: false, workspace: '/w', source: 'env', reasons: null }],
    ['reasons 不是陣列', { required: false, confirmed: false, workspace: '/w', source: 'env', reasons: 'is_home' }],
    ['reasons 含數字', { required: false, confirmed: false, workspace: '/w', source: 'env', reasons: ['is_home', 1] }],
    ['reasons 含 null', { required: false, confirmed: false, workspace: '/w', source: 'env', reasons: [null] }],
    ['null', null],
    ['undefined', undefined],
    ['字串', 'ok'],
  ]

  it.each(BAD)('parseStatus 拒絕形狀不對的回覆（%s）：丟錯，不補空值', (_name, raw) => {
    expect(() => parseStatus(raw)).toThrow('malformed')
  })

  it('parseStatus 接受完整且正確的 DTO，並回傳 reasons 的複本', () => {
    const reasons = ['source_home', 'is_home']
    const st = parseStatus({ required: true, confirmed: false, workspace: '/w', source: 'home', reasons })
    expect(st).toEqual({ required: true, confirmed: false, workspace: '/w', source: 'home', reasons })
    expect(st.reasons).not.toBe(reasons)
  })

  it.each(BAD)('refresh：尚未取得時收到壞回覆（%s）→ failed，blocked=true', async (_name, raw) => {
    const wsc = useWorkspaceConfirmation()
    await wsc.refresh(async () => raw)
    expect(wsc.status).toBeNull()
    expect(wsc.blockKind).toBe('failed')
    expect(wsc.blocked).toBe(true)
  })

  // 最重要的一格：已經放行（confirmed／notRequired）之後收到壞回覆，必須回到 failed 並重新封鎖，
  // 不能沿用舊的放行狀態，也不能因為壞回覆而「看起來沒事」。
  it.each(BAD)('refresh：已放行後收到壞回覆（%s）→ 回到 failed，blocked=true', async (_name, raw) => {
    const wsc = useWorkspaceConfirmation()
    await wsc.refresh(async () => CONFIRMED)
    expect(wsc.blocked).toBe(false)
    await wsc.refresh(async () => raw)
    expect(wsc.status).toBeNull()
    expect(wsc.blockKind).toBe('failed')
    expect(wsc.blocked).toBe(true)
    await wsc.refresh(async () => NOT_REQUIRED)
    expect(wsc.blocked).toBe(false)
    await wsc.refresh(async () => raw)
    expect(wsc.blockKind).toBe('failed')
    expect(wsc.blocked).toBe(true)
  })

  // 先前已確認、之後查詢失敗要重新封鎖（fail closed），不沿用舊的 confirmed。
  it('先前 confirmed、之後查詢失敗 → 重新封鎖', async () => {
    const wsc = useWorkspaceConfirmation()
    await wsc.refresh(async () => CONFIRMED)
    expect(wsc.blocked).toBe(false)
    await wsc.refresh(async () => { throw new Error('boom') })
    expect(wsc.blocked).toBe(true)
    expect(wsc.status).toBeNull()
  })
})

describe('workspace confirmation store：晚到的舊回應', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('較舊的 request 較晚完成：不得覆蓋較新的狀態（成功回覆）', async () => {
    const wsc = useWorkspaceConfirmation()
    const first = deferred<unknown>()
    const second = deferred<unknown>()
    const p1 = wsc.refresh(() => first.promise)
    const p2 = wsc.refresh(() => second.promise)
    second.resolve(CONFIRMED)
    await p2
    expect(wsc.blockKind).toBe('ok')
    first.resolve(PENDING) // 舊回應（確認之前查到的）較晚抵達
    await p1
    expect(wsc.blockKind).toBe('ok')
    expect(wsc.status?.confirmed).toBe(true)
  })

  it('較舊的 request 較晚失敗：也不得覆蓋較新的狀態', async () => {
    const wsc = useWorkspaceConfirmation()
    const first = deferred<unknown>()
    const p1 = wsc.refresh(() => first.promise)
    const p2 = wsc.refresh(async () => CONFIRMED)
    await p2
    first.reject(new Error('stale failure'))
    await p1
    expect(wsc.blockKind).toBe('ok')
    expect(wsc.queryError).toBe('')
  })
})

describe('workspace confirmation store：確認流程', () => {
  beforeEach(() => setActivePinia(createPinia()))

  async function pendingStore(workspace = PENDING.workspace) {
    const wsc = useWorkspaceConfirmation()
    await wsc.refresh(async () => ({ ...PENDING, workspace }))
    return wsc
  }

  // R3：前端必須傳 status.workspace 的原始字串——不縮短、不改寫成 ~、不 trim。
  it('ConfirmWorkspace 收到的是 status.workspace 原樣（含空白、中文、NFD 組合字元）', async () => {
    const raw = '/Users/測試 user/café 專案 '
    const wsc = await pendingStore(raw)
    const confirmFn = vi.fn(async (_p: string) => {})
    await wsc.confirm(confirmFn, async () => { await wsc.refresh(async () => ({ ...CONFIRMED, workspace: raw })) })
    expect(confirmFn).toHaveBeenCalledTimes(1)
    expect(confirmFn.mock.calls[0][0]).toBe(raw)
  })

  it('成功後重新查詢；放行只看後端回覆，store 不自己把 confirmed 設成 true', async () => {
    const wsc = await pendingStore()
    const refreshFn = vi.fn(async () => {}) // 後端回覆沒變（假設仍是 Pending）
    const ok = await wsc.confirm(async () => {}, refreshFn)
    expect(ok).toBe(true)
    expect(refreshFn).toHaveBeenCalledTimes(1)
    expect(wsc.blockKind).toBe('pending') // 沒有後端的 confirmed 回覆就不放行
  })

  it('確認失敗：狀態不變、保留錯誤、不重新查詢', async () => {
    const wsc = await pendingStore()
    const refreshFn = vi.fn(async () => {})
    const ok = await wsc.confirm(async () => { throw new Error('workspace_confirmation_path_mismatch') }, refreshFn)
    expect(ok).toBe(false)
    expect(wsc.confirmError).toContain('path_mismatch')
    expect(wsc.blockKind).toBe('pending')
    expect(refreshFn).not.toHaveBeenCalled()
  })

  // Review Focus 4：確認成功，但之後的重新查詢失敗 → 仍封鎖（不因為「確認成功」就放行）。
  it('確認成功但重新查詢失敗 → 封鎖', async () => {
    const wsc = await pendingStore()
    await wsc.confirm(async () => {}, async () => { await wsc.refresh(async () => { throw new Error('query down') }) })
    expect(wsc.blocked).toBe(true)
    expect(wsc.blockKind).toBe('failed')
  })

  // Review Focus 4：連點兩次只送一次 ConfirmWorkspace。
  it('確認進行中再次呼叫 confirm 會被忽略（防連點）', async () => {
    const wsc = await pendingStore()
    const gate = deferred<void>()
    const confirmFn = vi.fn(() => gate.promise)
    const p1 = wsc.confirm(confirmFn, async () => {})
    expect(wsc.confirming).toBe(true)
    const second = await wsc.confirm(confirmFn, async () => {})
    expect(second).toBe(false)
    gate.resolve()
    await p1
    expect(confirmFn).toHaveBeenCalledTimes(1)
    expect(wsc.confirming).toBe(false)
  })

  it('沒有 status（尚未取得／查詢失敗）時不送出確認', async () => {
    const wsc = useWorkspaceConfirmation()
    const confirmFn = vi.fn(async () => {})
    expect(await wsc.confirm(confirmFn, async () => {})).toBe(false)
    expect(confirmFn).not.toHaveBeenCalled()
  })
})
```

(1b) 警示元件——`role="alert"`、實際目錄／來源／原因、zh-TW 與 en、loading／failed、確認成功／失敗／取消、重新掛載不自動確認：

```ts
import { setActivePinia, createPinia } from 'pinia'
import { flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import WorkspaceConfirmBanner from './WorkspaceConfirmBanner.vue'
import { useWorkspaceConfirmation } from '../stores/workspaceConfirmation'
import { mountWithI18n } from '../test/i18n'
import zhTW from '../i18n/locales/zh-TW'

// ---- B4 R5／§6.2：高風險警示與確認流程 ----

const PENDING = {
  required: true, confirmed: false, workspace: '/Users/me', source: 'home',
  reasons: ['source_home', 'is_home'],
}

function mountBanner(over: { confirm?: (p: string) => Promise<void>; refresh?: () => Promise<void> } = {}, locale: 'zh-TW' | 'en' = 'zh-TW') {
  const confirm = over.confirm ?? vi.fn(async () => {})
  const refresh = over.refresh ?? vi.fn(async () => {})
  const w = mountWithI18n(WorkspaceConfirmBanner, { props: { confirm, refresh } }, locale)
  return { w, confirm, refresh }
}

describe('WorkspaceConfirmBanner', () => {
  beforeEach(() => setActivePinia(createPinia()))

  it('required && !confirmed：role="alert"、--err 樣式，顯示實際目錄、來源與原因', async () => {
    const wsc = useWorkspaceConfirmation()
    await wsc.refresh(async () => PENDING)
    const { w } = mountBanner()
    const alert = w.find('[role="alert"]')
    expect(alert.exists()).toBe(true)
    expect(alert.attributes('data-test')).toBe('ws-alert')
    expect(w.find('[data-test=ws-path]').text()).toBe('/Users/me')
    expect(w.find('[data-test=ws-source]').text()).toBe('home')
    const reasons = w.findAll('[data-test=ws-reason]').map(r => r.text())
    expect(reasons).toEqual([zhTW.workspaceConfirm.reason.sourceHome, zhTW.workspaceConfirm.reason.isHome])
    expect(w.find('[data-test=ws-open-confirm]').text()).toBe('確認在此目錄進行 AI 工作')
    // 樣式必須使用 --err（高風險警示，不是普通狀態文字）
    expect(w.html()).toContain('ws-alert')
  })

  it('en locale 顯示英文，未知的原因代碼原樣顯示（不洩漏缺漏 key）', async () => {
    const wsc = useWorkspaceConfirmation()
    await wsc.refresh(async () => ({ ...PENDING, reasons: ['is_home', 'brand_new_reason'] }))
    const { w } = mountBanner({}, 'en')
    expect(w.text()).toContain('High-risk workspace')
    const reasons = w.findAll('[data-test=ws-reason]').map(r => r.text())
    expect(reasons[0]).toContain('home directory')
    expect(reasons[1]).toBe('brand_new_reason')
  })

  it('不需確認或已確認：不渲染警示', async () => {
    const wsc = useWorkspaceConfirmation()
    await wsc.refresh(async () => ({ ...PENDING, confirmed: true }))
    const { w } = mountBanner()
    expect(w.find('[role="alert"]').exists()).toBe(false)
    expect(w.find('[role="status"]').exists()).toBe(false)
    await wsc.refresh(async () => ({ ...PENDING, required: false }))
    expect(w.find('[data-test=ws-alert]').exists()).toBe(false)
  })

  it('尚未取得 status：顯示 loading（role="status"），不是警示', () => {
    const { w } = mountBanner()
    expect(w.find('[data-test=ws-loading]').attributes('role')).toBe('status')
    expect(w.find('[role="alert"]').exists()).toBe(false)
  })

  it('查詢失敗：role="alert" 顯示原因，並可重新查詢', async () => {
    const wsc = useWorkspaceConfirmation()
    await wsc.refresh(async () => { throw new Error('app 尚未完成啟動') })
    const { w, refresh } = mountBanner()
    const failed = w.find('[data-test=ws-query-failed]')
    expect(failed.attributes('role')).toBe('alert')
    expect(failed.text()).toContain('app 尚未完成啟動')
    await w.find('[data-test=ws-retry]').trigger('click')
    expect(refresh).toHaveBeenCalledTimes(1)
  })

  it('確認成功：點擊 → 對話 → 確認 → 以 status.workspace 原樣呼叫 ConfirmWorkspace，並重新查詢', async () => {
    const wsc = useWorkspaceConfirmation()
    await wsc.refresh(async () => PENDING)
    const confirm = vi.fn(async (_p: string) => {})
    const refresh = vi.fn(async () => { await wsc.refresh(async () => ({ ...PENDING, confirmed: true })) })
    const { w } = mountBanner({ confirm, refresh })
    expect(w.find('[data-test=ws-confirm-dialog]').exists()).toBe(false) // 點擊前沒有對話
    await w.find('[data-test=ws-open-confirm]').trigger('click')
    expect(confirm).not.toHaveBeenCalled() // 只是打開確認對話，還沒確認
    await w.find('[data-test=ws-confirm]').trigger('click')
    await flushPromises()
    expect(confirm).toHaveBeenCalledWith('/Users/me')
    expect(refresh).toHaveBeenCalledTimes(1)
    expect(wsc.blocked).toBe(false)
    expect(w.find('[data-test=ws-alert]').exists()).toBe(false)
  })

  it('確認失敗：保留警示、顯示錯誤、狀態不變', async () => {
    const wsc = useWorkspaceConfirmation()
    await wsc.refresh(async () => PENDING)
    const { w } = mountBanner({ confirm: async () => { throw new Error('workspace_confirmation_path_mismatch') } })
    await w.find('[data-test=ws-open-confirm]').trigger('click')
    await w.find('[data-test=ws-confirm]').trigger('click')
    await flushPromises()
    expect(w.find('[data-test=ws-confirm-error]').text()).toContain('path_mismatch')
    expect(w.find('[data-test=ws-alert]').exists()).toBe(true)
    expect(wsc.blockKind).toBe('pending')
  })

  it('取消：只關閉這次確認操作——不呼叫 binding、不改變狀態', async () => {
    const wsc = useWorkspaceConfirmation()
    await wsc.refresh(async () => PENDING)
    const { w, confirm, refresh } = mountBanner()
    await w.find('[data-test=ws-open-confirm]').trigger('click')
    expect(w.find('[data-test=ws-confirm-dialog]').exists()).toBe(true)
    await w.find('[data-test=ws-cancel]').trigger('click')
    expect(w.find('[data-test=ws-confirm-dialog]').exists()).toBe(false)
    expect(confirm).not.toHaveBeenCalled()
    expect(refresh).not.toHaveBeenCalled()
    expect(wsc.blockKind).toBe('pending')
    expect(w.find('[data-test=ws-alert]').exists()).toBe(true)
  })

  it('元件重新掛載不會自動確認', async () => {
    const wsc = useWorkspaceConfirmation()
    await wsc.refresh(async () => PENDING)
    const first = mountBanner()
    first.w.unmount()
    const second = mountBanner()
    await flushPromises()
    expect(first.confirm).not.toHaveBeenCalled()
    expect(second.confirm).not.toHaveBeenCalled()
    expect(second.w.find('[data-test=ws-alert]').exists()).toBe(true)
  })
})
```

(1c) 停用條件——composer（PaneView）與 `s.submit`、Spec 三個 AI 按鈕、Plan 生成按鈕（含 `runAssist` 自己的守門）；三種封鎖（pending／loading／failed）與兩種放行（confirmed／notRequired）：

```ts
import { flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import PaneView from './PaneView.vue'
import SpecWorkspace from './SpecWorkspace.vue'
import PlanWorkspace from './PlanWorkspace.vue'
import { useSession } from '../stores/session'
import { useWorkspaceConfirmation } from '../stores/workspaceConfirmation'
import { mountWithI18n } from '../test/i18n'
import type { Bindings } from '../types'

// ---- B4 R5／§5.6：送出訊息與 Spec／Plan Assist 的停用條件 ----
// 三種封鎖都必須停用：required && !confirmed（pending）、尚未取得 status（loading）、
// 查詢失敗（failed）。放行：不需確認、或已確認。

const mocks = vi.hoisted(() => ({
  SpecAssist: vi.fn(), SpecList: vi.fn(), SpecRead: vi.fn(), SpecWrite: vi.fn(),
  SubmitForApproval: vi.fn(), PreviewSpecCommit: vi.fn(), ConfirmSpecCommit: vi.fn(),
  PlanAssist: vi.fn(), PlanList: vi.fn(), PlanRead: vi.fn(), PlanWrite: vi.fn(),
  PreviewPlanCommit: vi.fn(), ConfirmPlanCommit: vi.fn(), SubmitPlanForApproval: vi.fn(),
  PreviewAnalysisBaseBump: vi.fn(), ConfirmAnalysisBaseBump: vi.fn(),
}))
vi.mock('../../wailsjs/go/main/App', () => mocks)

type Mode = 'pending' | 'loading' | 'failed' | 'confirmed' | 'notRequired'

async function putIntoMode(mode: Mode) {
  const wsc = useWorkspaceConfirmation()
  const base = { required: true, confirmed: false, workspace: '/Users/me', source: 'home', reasons: ['source_home'] }
  if (mode === 'pending') await wsc.refresh(async () => base)
  if (mode === 'failed') await wsc.refresh(async () => { throw new Error('down') })
  if (mode === 'confirmed') await wsc.refresh(async () => ({ ...base, confirmed: true }))
  if (mode === 'notRequired') await wsc.refresh(async () => ({ ...base, required: false }))
  // loading：什麼都不做（status 為 null、沒有錯誤）
  return wsc
}

const BLOCKED: Mode[] = ['pending', 'loading', 'failed']
const OPEN: Mode[] = ['confirmed', 'notRequired']

beforeAll(() => { Element.prototype.scrollTo = Element.prototype.scrollTo ?? (() => {}) })

describe('composer（PaneView）與 session.submit', () => {
  let bindings: Bindings
  beforeEach(() => {
    setActivePinia(createPinia())
    bindings = { StartSession: vi.fn(async () => {}), SendMessage: vi.fn(async () => {}) }
    const s = useSession()
    s.setBindings(bindings)
    s.registerSession({ wsid: 'w1', provider: 'claude', taskLabel: '' })
    s.pin(0, 'w1')
    s.setFocus(0)
  })

  it.each(BLOCKED)('%s：composer 的輸入框與送出鈕停用，並顯示原因', async (mode) => {
    await putIntoMode(mode)
    const w = mountWithI18n(PaneView, { props: { idx: 0 } })
    expect(w.find('[data-test=composer-textarea]').attributes('disabled')).toBeDefined()
    expect(w.find('[data-test=composer-send]').attributes('disabled')).toBeDefined()
    expect(w.find('[data-test=composer-blocked]').exists()).toBe(true)
  })

  it.each(OPEN)('%s：composer 不停用、沒有封鎖提示', async (mode) => {
    await putIntoMode(mode)
    const w = mountWithI18n(PaneView, { props: { idx: 0 } })
    await w.find('[data-test=composer-textarea]').setValue('hi')
    expect(w.find('[data-test=composer-textarea]').attributes('disabled')).toBeUndefined()
    expect(w.find('[data-test=composer-send]').attributes('disabled')).toBeUndefined()
    expect(w.find('[data-test=composer-blocked]').exists()).toBe(false)
  })

  it.each(BLOCKED)('%s：s.submit 不呼叫 StartSession／SendMessage，推一則 notice', async (mode) => {
    await putIntoMode(mode)
    const s = useSession()
    const errorsBefore = s.errorSeq
    await s.submit('go')
    expect(bindings.StartSession).not.toHaveBeenCalled()
    expect(bindings.SendMessage).not.toHaveBeenCalled()
    expect(s.errorSeq).toBe(errorsBefore + 1)
    expect(s.sessions['w1'].busy).toBe(false)
  })

  // disabled 的 textarea／button 收不到使用者輸入與事件，所以：先在放行狀態下輸入草稿，再把 store
  // 翻成封鎖，然後直接呼叫元件的 send()（Enter 與送出鈕共用的函式）。「被擋下的草稿要留在
  // 輸入框」靠的就是 send() 在清空 draft 之前先檢查封鎖。
  it('輸入草稿後才被封鎖：send() 不會清掉草稿，也不會送出', async () => {
    const wsc = await putIntoMode('confirmed')
    const w = mountWithI18n(PaneView, { props: { idx: 0 } })
    const ta = w.find('[data-test=composer-textarea]')
    await ta.setValue('keep me')
    await wsc.refresh(async () => { throw new Error('status query down') }) // 之後查詢失敗 → 封鎖
    expect(wsc.blocked).toBe(true)
    await (w.vm as unknown as { send: () => Promise<void> }).send()
    await flushPromises()
    expect((ta.element as HTMLTextAreaElement).value).toBe('keep me')
    expect(bindings.StartSession).not.toHaveBeenCalled()
  })

  it('解除封鎖後可正常送出（第一輪走 StartSession）', async () => {
    await putIntoMode('confirmed')
    await useSession().submit('go')
    expect(bindings.StartSession).toHaveBeenCalledTimes(1)
  })
})

// CodeMirror 首次建構的一次性成本（沿用 SpecWorkspace.test.ts／PlanWorkspace.test.ts 的預熱）。
beforeAll(async () => {
  const [{ EditorView, basicSetup }, { EditorState }] = await Promise.all([
    import('codemirror'), import('@codemirror/state'),
  ])
  const host = document.createElement('div')
  document.body.appendChild(host)
  new EditorView({ state: EditorState.create({ doc: '', extensions: [basicSetup] }), parent: host }).destroy()
  host.remove()
}, 30_000)

describe('Spec／Plan Assist 觸發點', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    for (const fn of Object.values(mocks)) fn.mockReset()
    mocks.SpecList.mockResolvedValue([])
    mocks.SpecRead.mockResolvedValue({ content: '', digest: 'sha256:stub' })
    mocks.PlanList.mockResolvedValue([])
    mocks.PlanRead.mockResolvedValue({ content: '', digest: 'sha256:stub' })
    mocks.SpecAssist.mockResolvedValue('corr-1')
    mocks.PlanAssist.mockResolvedValue('corr-1')
  })

  const SPEC_BUTTONS = ['assist-draft', 'assist-ambiguity', 'assist-oracle']

  it.each(BLOCKED)('SpecWorkspace %s：三個 AI 輔助按鈕都停用並顯示原因', async (mode) => {
    await putIntoMode(mode)
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature' } })
    await flushPromises()
    for (const id of SPEC_BUTTONS) expect(w.find(`[data-test=${id}]`).attributes('disabled'), id).toBeDefined()
    expect(w.find('[data-test=assist-blocked]').exists()).toBe(true)
    // disabled 的按鈕收不到 click；runAssist 本身的守門（深度防禦）要直接呼叫才量得到。
    await (w.vm as unknown as { runAssist: (p: string) => Promise<void> }).runAssist('prompt')
    await flushPromises()
    expect(mocks.SpecAssist).not.toHaveBeenCalled()
    expect(w.find('.err').exists()).toBe(true)
  })

  it.each(OPEN)('SpecWorkspace %s：按鈕可用且會呼叫 SpecAssist', async (mode) => {
    await putIntoMode(mode)
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature' } })
    await flushPromises()
    expect(w.find('[data-test=assist-draft]').attributes('disabled')).toBeUndefined()
    expect(w.find('[data-test=assist-blocked]').exists()).toBe(false)
    await w.find('[data-test=assist-draft]').trigger('click')
    await flushPromises()
    expect(mocks.SpecAssist).toHaveBeenCalledTimes(1)
  })

  it.each(BLOCKED)('PlanWorkspace %s：生成草稿按鈕停用並顯示原因', async (mode) => {
    await putIntoMode(mode)
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml' } })
    await flushPromises()
    expect(w.find('[data-test=generate-draft]').attributes('disabled')).toBeDefined()
    expect(w.find('[data-test=assist-blocked]').exists()).toBe(true)
    await (w.vm as unknown as { runAssist: () => Promise<void> }).runAssist() // bump 的重新執行也走這裡
    await flushPromises()
    expect(mocks.PlanAssist).not.toHaveBeenCalled()
    expect(w.find('.err').exists()).toBe(true)
  })

  it.each(OPEN)('PlanWorkspace %s：生成草稿會呼叫 PlanAssist', async (mode) => {
    await putIntoMode(mode)
    const w = mountWithI18n(PlanWorkspace, { props: { path: 'plan/a.yaml' } })
    await flushPromises()
    expect(w.find('[data-test=generate-draft]').attributes('disabled')).toBeUndefined()
    await w.find('[data-test=generate-draft]').trigger('click')
    await flushPromises()
    expect(mocks.PlanAssist).toHaveBeenCalledTimes(1)
  })

  it('解除封鎖後同一個元件的按鈕立即恢復（響應式，不需重新掛載）', async () => {
    const wsc = await putIntoMode('pending')
    const w = mountWithI18n(SpecWorkspace, { props: { path: 'spec/a.feature' } })
    await flushPromises()
    expect(w.find('[data-test=assist-draft]').attributes('disabled')).toBeDefined()
    await wsc.refresh(async () => ({ required: true, confirmed: true, workspace: '/Users/me', source: 'home', reasons: [] }))
    await flushPromises()
    expect(w.find('[data-test=assist-draft]').attributes('disabled')).toBeUndefined()
  })
})
```

(1d) App 接線——先等 ready 再查詢、查詢失敗、晚到舊回應、按下確認的完整路徑（原始字串）、重新掛載不自動確認、`ws:` 顯示不變：

```ts
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import { useWorkspaceConfirmation } from './stores/workspaceConfirmation'

// ---- B4：App.vue 的接線（spec §5.6 資料取得順序、R5 Invariant）----
// 抓 App.vue 自己註冊的 EventsOn handler 觸發 refresh（同 cliInfoReady.test.ts：不重寫
// production 邏輯）。

const runtimeMocks = vi.hoisted(() => {
  const handlers: Record<string, (d?: any) => void> = {}
  return {
    handlers,
    EventsOn: (name: string, cb: (d?: any) => void) => { handlers[name] = cb; return vi.fn() },
  }
})
vi.mock('../wailsjs/runtime/runtime', () => runtimeMocks)

const appMocks = vi.hoisted(() => ({
  CLIInfo: vi.fn(async () => ({}) as Record<string, string>),
  WorkspaceConfirmationStatus: vi.fn(),
  ConfirmWorkspace: vi.fn(async (_p: string) => {}),
  GateDecide: vi.fn(), GateDecisionContext: vi.fn(),
  SubmitForApproval: vi.fn(), SubmitPlanForApproval: vi.fn(), PlanAssist: vi.fn(),
  GateList: vi.fn(async () => [] as any[]),
  ListSessions: vi.fn(async () => [] as any[]),
  SpecList: vi.fn(async () => [] as any[]),
  StartSession: vi.fn(), SendMessage: vi.fn(), EndSession: vi.fn(), NewSession: vi.fn(),
  TerminateSession: vi.fn(), CreateSession: vi.fn(), RemoveSession: vi.fn(),
  RecoverCodexRecording: vi.fn(), LoadTurnsBefore: vi.fn(),
  PaneLayout: vi.fn(async () => ({ pins: ['', ''], focused: '' })),
  SetPaneLayout: vi.fn(async () => undefined),
  RegisterMutation: vi.fn(), RunEvidence: vi.fn(), EvidenceGet: vi.fn(),
  SubmitTestContract: vi.fn(), ValidateTestCommit: vi.fn(), EvidenceCommitCandidates: vi.fn(),
  EscalationList: vi.fn(async () => [] as any[]), EscalationCreate: vi.fn(),
  EscalationAck: vi.fn(), EscalationResolve: vi.fn(),
}))
vi.mock('../wailsjs/go/main/App', () => appMocks)

import App from './App.vue'
import { makeI18n } from './test/i18n'

const READY = { ready: 'true', workspace: '/Users/me', workspaceSource: 'home', startupError: '' }
const PENDING = { required: true, confirmed: false, workspace: '/Users/me', source: 'home', reasons: ['source_home', 'is_home'] }

function deferred<T>() {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}

function mountApp() {
  const pinia = createPinia()
  setActivePinia(pinia)
  // 警示元件保持真實（shallowMount 預設會 stub 掉子元件）。
  return shallowMount(App, { global: { plugins: [pinia, makeI18n()], stubs: { WorkspaceConfirmBanner: false } } })
}

beforeEach(() => {
  for (const k of Object.keys(runtimeMocks.handlers)) delete runtimeMocks.handlers[k]
  for (const fn of Object.values(appMocks)) (fn as any).mockClear?.()
  appMocks.CLIInfo.mockImplementation(async () => READY)
  appMocks.WorkspaceConfirmationStatus.mockReset()
  appMocks.WorkspaceConfirmationStatus.mockImplementation(async () => PENDING)
  appMocks.ConfirmWorkspace.mockReset()
  appMocks.ConfirmWorkspace.mockImplementation(async () => {})
})

describe('App：workspace 確認狀態的取得順序', () => {
  it('CLIInfo 尚未 ready 時不查詢（維持封鎖）；cli-ready 事件之後才查', async () => {
    appMocks.CLIInfo.mockImplementationOnce(async () => ({ ready: 'false' }))
    const w = mountApp()
    await flushPromises()
    expect(appMocks.WorkspaceConfirmationStatus).not.toHaveBeenCalled()
    expect(useWorkspaceConfirmation().blockKind).toBe('loading')
    expect(w.find('[data-test=ws-loading]').exists()).toBe(true)

    appMocks.CLIInfo.mockImplementationOnce(async () => READY)
    runtimeMocks.handlers['workbench:cli-ready']!()
    await flushPromises()
    expect(appMocks.WorkspaceConfirmationStatus).toHaveBeenCalledTimes(1)
    expect(useWorkspaceConfirmation().blockKind).toBe('pending')
    expect(w.find('[role="alert"][data-test=ws-alert]').exists()).toBe(true)
  })

  it('晚連線（mount 時已 ready）：首查之後立刻查 status', async () => {
    const w = mountApp()
    await flushPromises()
    expect(appMocks.WorkspaceConfirmationStatus).toHaveBeenCalledTimes(1)
    expect(w.find('[data-test=ws-path]').text()).toBe('/Users/me')
  })

  it('status 查詢失敗：維持封鎖並顯示警示（不放行）', async () => {
    appMocks.WorkspaceConfirmationStatus.mockImplementation(async () => { throw new Error('app 尚未完成啟動') })
    const w = mountApp()
    await flushPromises()
    expect(useWorkspaceConfirmation().blocked).toBe(true)
    expect(w.find('[data-test=ws-query-failed]').text()).toContain('app 尚未完成啟動')
  })
})

describe('App：晚到的舊回應、確認流程與重新掛載', () => {
  it('較舊的 status 回覆較晚抵達：不得覆蓋較新的（已確認）狀態', async () => {
    const first = deferred<unknown>()
    appMocks.WorkspaceConfirmationStatus.mockImplementationOnce(() => first.promise as Promise<any>)
    const w = mountApp()
    await flushPromises() // 首查（已 ready）→ 第一次 status 查詢卡住

    appMocks.WorkspaceConfirmationStatus.mockImplementationOnce(async () => ({ ...PENDING, confirmed: true }))
    runtimeMocks.handlers['workbench:cli-ready']!() // 第二次 refresh → 第二次 status 查詢
    await flushPromises()
    expect(useWorkspaceConfirmation().blockKind).toBe('ok')

    first.resolve(PENDING) // 舊回應（確認之前查到的）較晚抵達
    await flushPromises()
    expect(useWorkspaceConfirmation().blockKind).toBe('ok')
    expect(w.find('[data-test=ws-alert]').exists()).toBe(false)
  })

  it('按下確認：以 status.workspace 原樣呼叫 ConfirmWorkspace，成功後重新查詢並解除警示', async () => {
    appMocks.WorkspaceConfirmationStatus
      .mockImplementationOnce(async () => PENDING)
      .mockImplementationOnce(async () => ({ ...PENDING, confirmed: true }))
    const w = mountApp()
    await flushPromises()
    await w.find('[data-test=ws-open-confirm]').trigger('click')
    await w.find('[data-test=ws-confirm]').trigger('click')
    await flushPromises()
    expect(appMocks.ConfirmWorkspace).toHaveBeenCalledTimes(1)
    expect(appMocks.ConfirmWorkspace).toHaveBeenCalledWith('/Users/me')
    expect(appMocks.WorkspaceConfirmationStatus).toHaveBeenCalledTimes(2)
    expect(w.find('[data-test=ws-alert]').exists()).toBe(false)
  })

  it('元件重新掛載不會自動確認：每次掛載只查詢，ConfirmWorkspace 從未被呼叫', async () => {
    const first = mountApp()
    await flushPromises()
    first.unmount()
    const second = mountApp()
    await flushPromises()
    expect(appMocks.WorkspaceConfirmationStatus).toHaveBeenCalledTimes(2)
    expect(appMocks.ConfirmWorkspace).not.toHaveBeenCalled()
    expect(second.find('[data-test=ws-alert]').exists()).toBe(true)
  })

  it('頂部列的 ws: 顯示維持不變', async () => {
    const w = mountApp()
    await flushPromises()
    expect(w.find('.meta').text()).toContain('ws: home @ /Users/me')
  })
})
```

(1e) drift guard——Go 端每個 `wsReason*` 代碼都有 i18n key：

```ts
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { workspaceBlockKeys, workspaceReasonKeys } from './stateKeys'

// B4 drift guard：Go 端 workspace_confirmation.go 的每一個 wsReason* 代碼都必須有對應的 i18n key。
// 少了的話，UI 會把原始代碼（例如 "is_home"）直接顯示給使用者。

describe('workspace 確認的原因代碼', () => {
  it('Go 的每個 wsReason* 常數都在 workspaceReasonKeys 裡', () => {
    const src = readFileSync(join(__dirname, '../../../workspace_confirmation.go'), 'utf8')
    const codes = [...src.matchAll(/wsReason\w+\s*=\s*"([a-z_]+)"/g)].map(m => m[1])
    expect(codes.length).toBeGreaterThanOrEqual(8) // 守門本身有效：抓得到常數
    expect(codes.filter(c => !(c in workspaceReasonKeys))).toEqual([])
  })

  it('封鎖種類 loading／failed／pending 都有 key（ok 不需要）', () => {
    expect(Object.keys(workspaceBlockKeys).sort()).toEqual(['failed', 'loading', 'pending'])
  })
})
```

(1f) 測試輔助：

```ts
import { useWorkspaceConfirmation, type WorkspaceStatus } from '../stores/workspaceConfirmation'

// 測試輔助：把 workspace 確認 store 放進指定狀態（預設＝不需確認，與 E2E／CI 的 env 注入同形）。
// 必須在 setActivePinia() 之後呼叫；不需要確認的既有元件測試在 beforeEach 呼叫它，讓 composer、
// Spec／Plan Assist 的行為維持原樣。
export function seedWorkspaceConfirmation(over: Partial<WorkspaceStatus> = {}) {
  const wsc = useWorkspaceConfirmation()
  wsc.$patch({
    status: { required: false, confirmed: false, workspace: '/w', source: 'env', reasons: [], ...over },
    queryError: '',
  })
  return wsc
}
```

(1g) 既有測試只補新 binding 的 mock 與 keyRefs 的 map 清單。

`lib/bindings.test.ts`（兩個新 binding 的逐參數轉發）：

```diff
--- a/frontend/src/lib/bindings.test.ts
+++ b/frontend/src/lib/bindings.test.ts
@@ -23,6 +23,8 @@
   EscalationCreate: vi.fn(async () => 'esc-id'),
   EscalationAck: vi.fn(async () => {}),
   EscalationResolve: vi.fn(async () => {}),
+  WorkspaceConfirmationStatus: vi.fn(async () => ({ required: false, confirmed: false, workspace: '/w', source: 'env', reasons: [] })),
+  ConfirmWorkspace: vi.fn(async (..._a: string[]) => {}),
 }))
 vi.mock('../../wailsjs/go/main/App', () => h)

@@ -183,4 +185,18 @@
     await b.EscalationResolve('esc-1', 'fixed', 'because reasons')
     expect(h.EscalationResolve).toHaveBeenCalledWith('esc-1', 'fixed', 'because reasons')
   })
+
+  // B4：ConfirmWorkspace 的唯一參數是 status.workspace 的原始字串——原樣轉發，不得改寫。
+  it('ConfirmWorkspace 原樣轉發 expectedPath（含空白與中文）', async () => {
+    const b = makeBindings()
+    await b.ConfirmWorkspace('/Users/測試 user/proj ')
+    expect(h.ConfirmWorkspace).toHaveBeenCalledWith('/Users/測試 user/proj ')
+  })
+
+  it('WorkspaceConfirmationStatus 轉發至 Go 綁定（無參數）並回傳其結果', async () => {
+    const b = makeBindings()
+    const st = await b.WorkspaceConfirmationStatus()
+    expect(h.WorkspaceConfirmationStatus).toHaveBeenCalledWith()
+    expect(st.required).toBe(false)
+  })
 })
```

`i18n/keyRefs.test.ts`（把兩個新 map 納入「map 值必須是 locale leaf」）：

```diff
--- a/frontend/src/i18n/keyRefs.test.ts
+++ b/frontend/src/i18n/keyRefs.test.ts
@@ -19,7 +19,7 @@
   }
   return acc
 }
-import { sessionStateKeys, gateStateKeys, codexToolStatusKeys, evidenceResultKeys, riskTierKeys } from './stateKeys'
+import { sessionStateKeys, gateStateKeys, codexToolStatusKeys, evidenceResultKeys, riskTierKeys, workspaceReasonKeys, workspaceBlockKeys } from './stateKeys'
 describe('t() key references exist', () => {
   it('every literal t(\'...\') key is a locale leaf', () => {
     const keys = leafSet(zhTW)
@@ -37,6 +37,7 @@
     const mapVals = [
       ...Object.values(sessionStateKeys), ...Object.values(gateStateKeys), ...Object.values(codexToolStatusKeys),
       ...Object.values(evidenceResultKeys), ...Object.values(riskTierKeys),
+      ...Object.values(workspaceReasonKeys), ...Object.values(workspaceBlockKeys),
     ]
     expect(mapVals.filter(v => !keys.has(v))).toEqual([])
   })
```

四個會 mount `App.vue` 的測試檔補 mock（`App.test.ts`、`cliInfoReady.test.ts`、`paneLayout.test.ts`、`registryUncertain.test.ts`；少了它們測試仍會綠，但綠得沒有意義——見 `App.test.ts` 內 PaneLayout 那段註解）。`registryUncertain.test.ts` 的 mock 與 seed 合在 Step 6 的同一份 diff；其餘三個：

```diff
--- a/frontend/src/App.test.ts
+++ b/frontend/src/App.test.ts
@@ -32,6 +32,8 @@
   RegisterMutation: vi.fn(), RunEvidence: vi.fn(), EvidenceGet: vi.fn(),
   SubmitTestContract: vi.fn(), ValidateTestCommit: vi.fn(), EvidenceCommitCandidates: vi.fn(),
   EscalationList: vi.fn(async () => [] as any[]), EscalationCreate: vi.fn(), EscalationAck: vi.fn(), EscalationResolve: vi.fn(),
+  WorkspaceConfirmationStatus: vi.fn(async () => ({ required: false, confirmed: false, workspace: '/w', source: 'env', reasons: [] as string[] })),
+  ConfirmWorkspace: vi.fn(async (_p: string) => {}),
   // A2-1：Spec／Plan 送核與 Plan assist——App.vue 用 withEscalationReload 包裝
   // 這三個綁定，未 mock 時 props('submit')／props('assist') 呼叫會撞 window.go。
   SubmitForApproval: vi.fn(), SubmitPlanForApproval: vi.fn(), PlanAssist: vi.fn(),
```

```diff
--- a/frontend/src/cliInfoReady.test.ts
+++ b/frontend/src/cliInfoReady.test.ts
@@ -47,6 +47,8 @@
   SetPaneLayout: vi.fn(async () => undefined),
   RegisterMutation: vi.fn(), RunEvidence: vi.fn(), EvidenceGet: vi.fn(),
   SubmitTestContract: vi.fn(), ValidateTestCommit: vi.fn(), EvidenceCommitCandidates: vi.fn(),
+  WorkspaceConfirmationStatus: vi.fn(async () => ({ required: false, confirmed: false, workspace: '/w', source: 'env', reasons: [] as string[] })),
+  ConfirmWorkspace: vi.fn(async (_p: string) => {}),
   EscalationList: vi.fn(async () => [] as any[]), EscalationCreate: vi.fn(),
   EscalationAck: vi.fn(), EscalationResolve: vi.fn(),
 }))
```

```diff
--- a/frontend/src/paneLayout.test.ts
+++ b/frontend/src/paneLayout.test.ts
@@ -54,6 +54,8 @@
     SubmitTestContract: vi.fn(), ValidateTestCommit: vi.fn(), EvidenceCommitCandidates: vi.fn(),
     AuthStatus: vi.fn(), StartLogin: vi.fn(), CancelLogin: vi.fn(), Logout: vi.fn(),
     RestartCodexServerRecorded: vi.fn(),
+    WorkspaceConfirmationStatus: vi.fn(async () => ({ required: false, confirmed: false, workspace: '/w', source: 'env', reasons: [] as string[] })),
+    ConfirmWorkspace: vi.fn(async (_p: string) => {}),
     EscalationList: vi.fn(async () => [] as any[]), EscalationAck: vi.fn(),
     EscalationCreate: vi.fn(), EscalationResolve: vi.fn(),
   }
```

- [ ] **Step 2：跑一次確認失敗（保留首次紅燈）**

```bash
cd frontend
run_logged t7-red npx vitest run src/stores/workspaceConfirmation.test.ts src/components/WorkspaceConfirmBanner.test.ts src/components/workspaceGate.test.ts src/workspaceConfirmationApp.test.ts src/i18n/workspaceReasonKeys.test.ts src/lib/bindings.test.ts
show t7-red 'Test Files|Tests |Failed to resolve|Error:' 12
cd ..
```

Expected：rc=1；新檔案因 `Failed to resolve import "./workspaceConfirmation"`／`WorkspaceConfirmBanner.vue` 等而整檔失敗；`bindings.test.ts` 的新測試因 `b.ConfirmWorkspace is not a function` 紅。

- [ ] **Step 3：實作（store、i18n、型別、adapter）**

(3a) store：

```ts
import { defineStore } from 'pinia'

// B4 workspace 確認狀態（spec §5.6）：App 頂部列警示、composer、Spec／Plan Assist 共用的
// 單一來源。沿用 escalation store 的慣例——不在 store 內 import wailsjs，真正的 binding 由
// App.vue 注入（測試因此不必 mock 整個 App module）。
//
// fail closed：只有「已取得 status，且不是 required && !confirmed」才放行。尚未取得、查詢失敗、
// 回覆形狀不對，一律視為封鎖。

export interface WorkspaceStatus {
  required: boolean
  confirmed: boolean
  workspace: string
  source: string
  reasons: string[]
}

export type BlockKind = 'ok' | 'loading' | 'failed' | 'pending'

interface State {
  status: WorkspaceStatus | null // null＝尚未取得或上一次查詢失敗
  queryError: string // 查詢失敗的原文；空字串＝沒有失敗
  seq: number // request sequencing：回覆到達時序號已變＝舊回應，整筆丟棄
  confirming: boolean // 確認進行中（擋重複點擊）
  confirmError: string // 最近一次確認失敗的原文
}

// parseStatus：驗證完整 DTO 形狀，不補空值、不用 String() 把錯誤資料轉成合法資料。
// Go 端保證 required／confirmed 是 boolean、workspace／source 是 string、reasons 是**非 nil** 的
// string 陣列（見 WorkspaceConfirmationInfo.info()）——所以任何缺少、null、型別不符的欄位都是
// 壞回覆，一律丟錯，由 refresh 轉成封鎖（failed）。缺 required 若被當成 false、reasons 缺少
// 若被補成 []，一個壞掉的回覆就會解除封鎖。
export function parseStatus(raw: unknown): WorkspaceStatus {
  const r = raw as Partial<Record<keyof WorkspaceStatus, unknown>> | null | undefined
  if (
    !r || typeof r !== 'object' ||
    typeof r.required !== 'boolean' || typeof r.confirmed !== 'boolean' ||
    typeof r.workspace !== 'string' || typeof r.source !== 'string' ||
    !Array.isArray(r.reasons) || !r.reasons.every((x): x is string => typeof x === 'string')
  ) {
    throw new Error('WorkspaceConfirmationStatus: malformed response')
  }
  return {
    required: r.required,
    confirmed: r.confirmed,
    workspace: r.workspace,
    source: r.source,
    reasons: [...r.reasons],
  }
}

export const useWorkspaceConfirmation = defineStore('workspaceConfirmation', {
  state: (): State => ({ status: null, queryError: '', seq: 0, confirming: false, confirmError: '' }),

  getters: {
    blockKind: (s): BlockKind => {
      if (s.status === null) return s.queryError === '' ? 'loading' : 'failed'
      return s.status.required && !s.status.confirmed ? 'pending' : 'ok'
    },
    blocked(): boolean { return this.blockKind !== 'ok' },
  },

  actions: {
    async refresh(fetchStatus: () => Promise<unknown>) {
      const token = ++this.seq
      try {
        const st = parseStatus(await fetchStatus())
        if (token !== this.seq) return // 較新的 refresh 已發出：這筆是舊回應，丟棄
        this.status = st
        this.queryError = ''
      } catch (e) {
        if (token !== this.seq) return // 舊呼叫的失敗不得覆蓋新狀態
        this.status = null // 查詢失敗＝封鎖，不沿用舊的 confirmed
        this.queryError = String((e as Error)?.message ?? e)
      }
    },

    // confirm：把後端回報的 workspace **原始字串**原樣傳回（不縮短、不改寫成 ~、不 trim）。
    // 成功後一律重新查詢 status——前端不自己把 confirmed 設成 true，放行與否只看後端回覆。
    async confirm(confirmFn: (expectedPath: string) => Promise<void>, refreshFn: () => Promise<void>): Promise<boolean> {
      if (this.confirming || this.status === null) return false
      this.confirming = true
      this.confirmError = ''
      try {
        try {
          await confirmFn(this.status.workspace)
        } catch (e) {
          this.confirmError = String((e as Error)?.message ?? e)
          return false
        }
        await refreshFn()
        return true
      } finally {
        this.confirming = false
      }
    },
  },
})
```

(3b) `types.ts`（檔尾附加；並把檔頭 import 改成 `import type { evidence, escalation, main } from '../wailsjs/go/models'`）、`lib/bindings.ts`：

```diff
--- a/frontend/src/types.ts
+++ b/frontend/src/types.ts
@@ -1,4 +1,4 @@
-import type { evidence, escalation } from '../wailsjs/go/models'
+import type { evidence, escalation, main } from '../wailsjs/go/models'

 // Envelope v1（M1 凍結契約）：欄位名對齊 Go contract.Envelope 的 json tag。
 export interface Usage { input_tokens: number; output_tokens: number; cached_input_tokens?: number }
@@ -151,3 +151,9 @@
   EscalationAck(id: string): Promise<void>
   EscalationResolve(id: string, resolution: string, reason: string): Promise<void>
 }
+// WorkspaceConfirmationBindings：B4 的兩個綁定。ConfirmWorkspace 的唯一參數必須是
+// WorkspaceConfirmationStatus 回傳的 workspace 原始字串（逐參數轉發鎖在 bindings.test.ts）。
+export interface WorkspaceConfirmationBindings {
+  WorkspaceConfirmationStatus(): Promise<main.WorkspaceConfirmationInfo>
+  ConfirmWorkspace(expectedPath: string): Promise<void>
+}
```

```diff
--- a/frontend/src/lib/bindings.ts
+++ b/frontend/src/lib/bindings.ts
@@ -4,10 +4,11 @@
   PaneLayout, SetPaneLayout,
   RegisterMutation, RunEvidence, EvidenceGet, SubmitTestContract, ValidateTestCommit, EvidenceCommitCandidates,
   EscalationList, EscalationCreate, EscalationAck, EscalationResolve,
+  WorkspaceConfirmationStatus, ConfirmWorkspace,
 } from '../../wailsjs/go/main/App'
 import type {
   Bindings, SessionLifecycleBindings, WorkspaceSessionBindings, CodexRecordingBindings,
-  TurnWindowBindings, PaneLayoutBindings, EvidenceBindings, EscalationBindings,
+  TurnWindowBindings, PaneLayoutBindings, EvidenceBindings, EscalationBindings, WorkspaceConfirmationBindings,
 } from '../types'

 // production bindings adapter（M1.5 第三輪 review P1-1：SendMessage 必須
@@ -29,7 +30,7 @@
 // 綁定個別當 prop 傳給 TcaWorkspace／EscalationInbox 等元件。
 export function makeBindings(): Bindings & SessionLifecycleBindings & WorkspaceSessionBindings
   & CodexRecordingBindings & TurnWindowBindings & PaneLayoutBindings & EvidenceBindings
-  & EscalationBindings {
+  & EscalationBindings & WorkspaceConfirmationBindings {
   return {
     StartSession: (wsid, prompt, resume, rc, task, policy) => StartSession(wsid, prompt, resume, rc, task, policy),
     SendMessage: (wsid, text) => SendMessage(wsid, text),
@@ -55,5 +56,7 @@
     EscalationCreate: (sourceRef, blockScope, summary) => EscalationCreate(sourceRef, blockScope, summary),
     EscalationAck: (id) => EscalationAck(id),
     EscalationResolve: (id, resolution, reason) => EscalationResolve(id, resolution, reason),
+    WorkspaceConfirmationStatus: () => WorkspaceConfirmationStatus(),
+    ConfirmWorkspace: (expectedPath) => ConfirmWorkspace(expectedPath),
   }
 }
```

(3c) `i18n/stateKeys.ts`（原因代碼與封鎖種類的 key map；未知代碼沿用 `resolveState` 的「原樣顯示」）與兩個 locale（zh-TW 用台灣慣用語；en 同結構，parity 測試會逐 leaf 比對）：

```diff
--- a/frontend/src/i18n/stateKeys.ts
+++ b/frontend/src/i18n/stateKeys.ts
@@ -19,6 +19,23 @@
 export const evidenceResultKeys: Record<string, string> = {
   passed: 'evidence.result.passed', failed: 'evidence.result.failed', error: 'evidence.result.error',
 }
+// B4：workspace 確認的原因代碼（Go 端 workspacePolicy 的輸出）與封鎖種類。未知代碼原樣顯示
+// （resolveState 的既有行為），新增或改名要和 workspace_confirmation.go 的 wsReason* 同步。
+export const workspaceReasonKeys: Record<string, string> = {
+  source_home: 'workspaceConfirm.reason.sourceHome',
+  source_tmp: 'workspaceConfirm.reason.sourceTmp',
+  env_rejected: 'workspaceConfirm.reason.envRejected',
+  env_rejected_normalize_failed: 'workspaceConfirm.reason.envRejectedNormalize',
+  env_rejected_mkdir_failed: 'workspaceConfirm.reason.envRejectedMkdir',
+  is_home: 'workspaceConfirm.reason.isHome',
+  home_identity_unknown: 'workspaceConfirm.reason.homeIdentityUnknown',
+  workspace_unresolved: 'workspaceConfirm.reason.unresolved',
+}
+export const workspaceBlockKeys: Record<string, string> = {
+  loading: 'workspaceConfirm.blocked.loading',
+  failed: 'workspaceConfirm.blocked.failed',
+  pending: 'workspaceConfirm.blocked.pending',
+}
 export function resolveState(map: Record<string, string>, raw: string, translate: (k: string) => string): string {
   const key = map[raw]
   return key ? translate(key) : raw // unknown → 原樣，不洩漏缺漏 key
```

```diff
--- a/frontend/src/i18n/locales/zh-TW.ts
+++ b/frontend/src/i18n/locales/zh-TW.ts
@@ -29,6 +29,43 @@
       restoreFailed: '無法還原釘選的 pane——兩個 pane 以空白啟動，請重新釘選需要的 session：{error}',
     },
   },
+  workspaceConfirm: {
+    title: '高風險 workspace：AI 工作已停用',
+    intro: 'AI 可以在這個目錄執行工具並修改檔案。確認之前，送出訊息與 Spec／Plan 的 AI 輔助都會停用。',
+    field: {
+      path: '實際目錄',
+      source: '來源',
+      reasons: '原因',
+    },
+    reason: {
+      sourceHome: '退回使用者家目錄（從 Finder 開啟時工作目錄是 /）',
+      sourceTmp: '所有候選目錄都無法使用，退回系統暫存目錄',
+      envRejected: 'WORKBENCH_WORKSPACE 被拒絕，已退回其他候選目錄',
+      envRejectedNormalize: 'WORKBENCH_WORKSPACE 指向的路徑無法解析（不存在或無法存取），已退回其他候選目錄',
+      envRejectedMkdir: 'WORKBENCH_WORKSPACE 指向的目錄無法建立 .workbench，已退回其他候選目錄',
+      isHome: '實際目錄就是使用者家目錄',
+      homeIdentityUnknown: '無法判定這個目錄是不是使用者家目錄',
+      unresolved: 'workspace 尚未解析完成',
+    },
+    action: {
+      open: '確認在此目錄進行 AI 工作',
+      confirm: '確認',
+      cancel: '取消',
+      retry: '重新查詢',
+    },
+    dialog: {
+      prompt: '你要讓 AI 在這個目錄進行工作嗎？{path}',
+    },
+    confirming: '確認中…',
+    confirmFailed: '確認失敗：{error}',
+    loading: '正在取得 workspace 確認狀態…',
+    queryFailed: '無法取得 workspace 確認狀態，AI 工作已停用：{error}',
+    blocked: {
+      loading: '正在取得 workspace 確認狀態，AI 工作暫時停用',
+      failed: '無法取得 workspace 確認狀態，AI 工作已停用',
+      pending: '尚未確認 workspace，AI 工作已停用——請先在畫面頂端確認',
+    },
+  },
   settings: {
     action: {
       new: '開新對話',
```

```diff
--- a/frontend/src/i18n/locales/en.ts
+++ b/frontend/src/i18n/locales/en.ts
@@ -29,6 +29,43 @@
       restoreFailed: 'Pinned panes could not be restored — both panes start empty; pin the sessions you want again: {error}',
     },
   },
+  workspaceConfirm: {
+    title: 'High-risk workspace: AI work is disabled',
+    intro: 'The AI can run tools and modify files in this directory. Until you confirm, sending messages and Spec/Plan AI assist stay disabled.',
+    field: {
+      path: 'Actual directory',
+      source: 'Source',
+      reasons: 'Reasons',
+    },
+    reason: {
+      sourceHome: 'Fell back to the home directory (a Finder launch starts in /)',
+      sourceTmp: 'Every candidate directory was unusable; fell back to the system temp directory',
+      envRejected: 'WORKBENCH_WORKSPACE was rejected; fell back to another candidate',
+      envRejectedNormalize: 'The path in WORKBENCH_WORKSPACE could not be resolved (missing or inaccessible); fell back to another candidate',
+      envRejectedMkdir: 'WORKBENCH_WORKSPACE points to a directory where .workbench cannot be created; fell back to another candidate',
+      isHome: 'The actual directory is your home directory',
+      homeIdentityUnknown: 'Could not determine whether this directory is your home directory',
+      unresolved: 'The workspace has not been resolved yet',
+    },
+    action: {
+      open: 'Confirm AI work in this directory',
+      confirm: 'Confirm',
+      cancel: 'Cancel',
+      retry: 'Retry',
+    },
+    dialog: {
+      prompt: 'Let the AI work in this directory? {path}',
+    },
+    confirming: 'Confirming…',
+    confirmFailed: 'Confirmation failed: {error}',
+    loading: 'Fetching the workspace confirmation status…',
+    queryFailed: 'Could not fetch the workspace confirmation status; AI work is disabled: {error}',
+    blocked: {
+      loading: 'Fetching the workspace confirmation status; AI work is temporarily disabled',
+      failed: 'Could not fetch the workspace confirmation status; AI work is disabled',
+      pending: 'The workspace is not confirmed; AI work is disabled — confirm it at the top of the window first',
+    },
+  },
   settings: {
     action: {
       new: 'New',
```

- [ ] **Step 4：實作（元件與接線）**

(4a) 警示元件：

```vue
<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useWorkspaceConfirmation } from '../stores/workspaceConfirmation'
import { resolveState, workspaceReasonKeys } from '../i18n/stateKeys'

// B4 高風險 workspace 警示（spec §5.6、R5）。三種狀態：
//   loading → 尚未取得 status：一行說明（role="status"），AI 工作已停用；
//   failed  → 查詢失敗：role="alert"＋重新查詢，AI 工作已停用；
//   pending → required && !confirmed：高風險警示（role="alert"、--err 樣式），顯示實際目錄、
//             來源與原因，並提供「確認在此目錄進行 AI 工作」→ 確認／取消 兩步流程。
// ok 時不渲染任何東西。元件重新掛載不會自動確認：確認只由使用者按下「確認」觸發。
const props = defineProps<{
  confirm: (expectedPath: string) => Promise<void> // ConfirmWorkspace 綁定（由 App.vue 注入）
  refresh: () => Promise<void> // 重新查詢 WorkspaceConfirmationStatus
}>()
const { t } = useI18n()
const wsc = useWorkspaceConfirmation()
const dialogOpen = ref(false)
const reasons = computed(() => (wsc.status?.reasons ?? []).map(r => resolveState(workspaceReasonKeys, r, t)))

async function doConfirm() {
  const ok = await wsc.confirm(props.confirm, props.refresh)
  if (ok) dialogOpen.value = false
}
// 取消只關閉這次確認操作：不呼叫任何 binding、不改變確認狀態。
function cancel() {
  dialogOpen.value = false
  wsc.confirmError = ''
}
</script>

<template>
  <div v-if="wsc.blockKind === 'loading'" class="ws-status" role="status" data-test="ws-loading">
    {{ t('workspaceConfirm.loading') }}
  </div>
  <div v-else-if="wsc.blockKind === 'failed'" class="ws-alert" role="alert" data-test="ws-query-failed">
    <span>{{ t('workspaceConfirm.queryFailed', { error: wsc.queryError }) }}</span>
    <button type="button" data-test="ws-retry" @click="props.refresh()">{{ t('workspaceConfirm.action.retry') }}</button>
  </div>
  <div v-else-if="wsc.blockKind === 'pending' && wsc.status" class="ws-alert" role="alert" data-test="ws-alert">
    <strong>{{ t('workspaceConfirm.title') }}</strong>
    <p>{{ t('workspaceConfirm.intro') }}</p>
    <dl>
      <dt>{{ t('workspaceConfirm.field.path') }}</dt>
      <dd data-test="ws-path">{{ wsc.status.workspace }}</dd>
      <dt>{{ t('workspaceConfirm.field.source') }}</dt>
      <dd data-test="ws-source">{{ wsc.status.source }}</dd>
      <dt>{{ t('workspaceConfirm.field.reasons') }}</dt>
      <dd>
        <ul>
          <li v-for="(r, i) in reasons" :key="i" data-test="ws-reason">{{ r }}</li>
        </ul>
      </dd>
    </dl>
    <button v-if="!dialogOpen" type="button" data-test="ws-open-confirm" @click="dialogOpen = true">
      {{ t('workspaceConfirm.action.open') }}
    </button>
    <div v-else class="ws-dialog" data-test="ws-confirm-dialog">
      <p>{{ t('workspaceConfirm.dialog.prompt', { path: wsc.status.workspace }) }}</p>
      <button type="button" :disabled="wsc.confirming" data-test="ws-confirm" @click="doConfirm">
        {{ wsc.confirming ? t('workspaceConfirm.confirming') : t('workspaceConfirm.action.confirm') }}
      </button>
      <button type="button" :disabled="wsc.confirming" data-test="ws-cancel" @click="cancel">
        {{ t('workspaceConfirm.action.cancel') }}
      </button>
    </div>
    <p v-if="wsc.confirmError" class="ws-error" data-test="ws-confirm-error">
      {{ t('workspaceConfirm.confirmFailed', { error: wsc.confirmError }) }}
    </p>
  </div>
</template>

<style scoped>
.ws-status { font-size: 11px; color: var(--text-faint); padding: 0 10px 4px; }
.ws-alert { margin: 0 10px 6px; padding: 6px 10px; border: 1px solid var(--err); border-left-width: 4px; border-radius: 4px; color: var(--err); font-size: 12px; text-align: left; }
.ws-alert p { margin: 2px 0; }
.ws-alert dl { display: grid; grid-template-columns: max-content 1fr; gap: 0 8px; margin: 4px 0; }
.ws-alert dt { font-weight: 600; }
.ws-alert dd { margin: 0; word-break: break-all; }
.ws-alert ul { margin: 0; padding-left: 16px; }
.ws-dialog { margin-top: 4px; }
.ws-error { font-weight: 600; }
</style>
```

(4b) `App.vue`（`refreshCliInfo` 在套用 CLIInfo 之後、`ready === 'true'` 才查詢；模板在 `.meta` 與 `.body` 之間加入警示元件）：

```diff
--- a/frontend/src/App.vue
+++ b/frontend/src/App.vue
@@ -11,6 +11,7 @@
 import { usePlan } from './stores/plan'
 import { useEvidence } from './stores/evidence'
 import { useEscalation } from './stores/escalation'
+import { useWorkspaceConfirmation } from './stores/workspaceConfirmation'
 import { routeEnvelope } from './lib/gateRouting'
 import { load, save } from './lib/persist'
 import { escalationBadge } from './lib/escalationBadge'
@@ -21,6 +22,7 @@
 import SessionList from './components/SessionList.vue'
 import Timeline from './components/Timeline.vue'
 import StatusBar from './components/StatusBar.vue'
+import WorkspaceConfirmBanner from './components/WorkspaceConfirmBanner.vue'
 import FileTree from './components/FileTree.vue'
 import PreviewPane from './components/PreviewPane.vue'
 import ApprovalDialog from './components/ApprovalDialog.vue'
@@ -40,6 +42,7 @@
 const plan = usePlan()
 const evidence = useEvidence()
 const escalation = useEscalation()
+const wsc = useWorkspaceConfirmation()
 const wailsBindings = makeBindings() // Task 22：TCA workspace 六個綁定＋Task 25：escalation 收件匣四個綁定的唯一 production 來源（見 bindings.test.ts）
 const selectedEvidenceId = ref('')
 const gateDegraded = ref(false) // GateList().journal_degraded（任一筆為 true）→ 停用核可／駁回（spec §3.2）
@@ -318,10 +321,22 @@
   const token = ++cliInfoReq
   try {
     const info = await CLIInfo()
-    if (token === cliInfoReq) cliInfo.value = info
+    if (token === cliInfoReq) {
+      cliInfo.value = info
+      // B4（spec §5.6）：先等 CLIInfo ready，再查 workspace 確認狀態。未 ready 時後端的
+      // WorkspaceConfirmationStatus 會被 lifecycle 拒絕，store 維持封鎖；cli-ready 事件
+      // 到達時 refreshCliInfo 會再走一次。
+      if (info.ready === 'true') await refreshWorkspaceConfirmation()
+    }
   } catch { /* dev 無綁定時忽略 */ }
 }

+// refreshWorkspaceConfirmation：store 自己做 request sequencing（晚到的舊回應整筆丟棄）、
+// 失敗時維持封鎖（fail closed）。重新掛載只會重新查詢，不會自動確認。
+async function refreshWorkspaceConfirmation() {
+  await wsc.refresh(wailsBindings.WorkspaceConfirmationStatus)
+}
+
 // wailsDisposers：onMounted 註冊的 Wails listener 全部收在這裡，unmount 一併清
 // ——不清的 handler 會在 HMR／重掛後對著舊的 store instance 繼續收事件。
 // undefined guard：測試 mock 的 EventsOn 可能不回傳 disposer。
@@ -378,6 +393,7 @@
       ws: {{ cliInfo.workspaceSource }} @ {{ cliInfo.workspace }} | tools: {{ cliInfo.toolsSource }} | node {{ cliInfo.node }}
       <span v-if="cliInfo.startupError" class="err">{{ t('app.startupError', { error: cliInfo.startupError }) }}</span>
     </div>
+    <WorkspaceConfirmBanner :confirm="wailsBindings.ConfirmWorkspace" :refresh="refreshWorkspaceConfirmation" />
     <div class="body">
       <aside class="side">
         <div class="side-sessions"><SessionList /></div>
```

(4c) `PaneView.vue`（composer 停用＋原因；`send()` 在清空草稿之前先檢查）、`stores/session.ts`（`submit` 的前端守門；後端 R2 仍是最後防線）：

```diff
--- a/frontend/src/components/PaneView.vue
+++ b/frontend/src/components/PaneView.vue
@@ -2,7 +2,8 @@
 import { computed, nextTick, ref, watch } from 'vue'
 import { useI18n } from 'vue-i18n'
 import { useSession } from '../stores/session'
-import { resolveState, sessionStateKeys } from '../i18n/stateKeys'
+import { useWorkspaceConfirmation } from '../stores/workspaceConfirmation'
+import { resolveState, sessionStateKeys, workspaceBlockKeys } from '../i18n/stateKeys'
 import { isAtBottom, isAtTop } from '../lib/scroll'

 // PaneView（Task 28，spec §3.7）：單一 pane，綁定 DualPane 分配給它的 WSID
@@ -19,6 +20,7 @@
 const props = defineProps<{ idx: 0 | 1 }>()
 const { t } = useI18n()
 const s = useSession()
+const wsc = useWorkspaceConfirmation() // B4：required && !confirmed、尚未取得、查詢失敗 → composer 停用

 const wsid = computed(() => s.pins[props.idx] ?? '')
 const meta = computed(() => (wsid.value ? s.sessions[wsid.value] : undefined))
@@ -62,6 +64,7 @@
 async function send() {
   const text = draft.value.trim()
   if (!text || !meta.value || meta.value.busy) return
+  if (wsc.blocked) return // 早於清空 draft：被擋下的草稿要留在輸入框
   draft.value = ''
   follow.value = true
   await s.submit(text) // submit() 作用於 focused pane，composer 只在 focused 時渲染，故一致
@@ -100,11 +103,12 @@
           <div class="text">{{ m.text }}<span v-if="m.streaming" class="cursor">▌</span></div>
         </div>
       </div>
+      <p v-if="focused && wsc.blocked" class="blocked-hint" data-test="composer-blocked">{{ t(workspaceBlockKeys[wsc.blockKind]) }}</p>
       <div v-if="focused" class="composer" data-test="composer">
-        <textarea v-model="draft" rows="2" :disabled="meta?.busy" data-test="composer-textarea"
+        <textarea v-model="draft" rows="2" :disabled="meta?.busy || wsc.blocked" data-test="composer-textarea"
           :placeholder="t('chat.input.placeholder')"
           @keydown.enter.exact.prevent="send" @click.stop />
-        <button :disabled="meta?.busy || !draft.trim()" data-test="composer-send" @click.stop="send">{{ t('chat.action.send') }}</button>
+        <button :disabled="meta?.busy || wsc.blocked || !draft.trim()" data-test="composer-send" @click.stop="send">{{ t('chat.action.send') }}</button>
       </div>
     </template>
   </div>
@@ -127,6 +131,7 @@
 .thinking pre { font-size: 12px; color: var(--text-muted); white-space: pre-wrap; }
 .cursor { animation: blink 1s step-start infinite; }
 @keyframes blink { 50% { opacity: 0; } }
+.blocked-hint { margin: 0; padding: 4px 8px; font-size: var(--fs-s); color: var(--err); text-align: left; }
 .composer { display: flex; gap: 8px; padding: 8px; border-top: 1px solid var(--border); cursor: default; }
 .composer textarea { flex: 1; resize: none; padding: 8px; }
 </style>
```

```diff
--- a/frontend/src/stores/session.ts
+++ b/frontend/src/stores/session.ts
@@ -1,5 +1,7 @@
 import { defineStore } from 'pinia'
 import { t } from '../i18n'
+import { workspaceBlockKeys } from '../i18n/stateKeys'
+import { useWorkspaceConfirmation } from './workspaceConfirmation'
 import type { Bindings, ChatItem, Envelope, PaneLayoutInfo, SessionInfo, TimelineItem } from '../types'

 const NOISE_KINDS = new Set(['system_other', 'unknown'])
@@ -612,6 +614,13 @@
         this.pushNotice(t('store.bindingsNotReady'))
         return
       }
+      // B4：required && !confirmed、尚未取得 status、查詢失敗，一律不送出（後端的 R2 閘門仍是
+      // 最後防線；這裡讓使用者立刻看到原因，而不是等一次被拒絕的呼叫）。
+      const wsc = useWorkspaceConfirmation()
+      if (wsc.blocked) {
+        this.pushNotice(t(workspaceBlockKeys[wsc.blockKind]))
+        return
+      }
       m.busy = true
       try {
         if (!m.active) {
```

(4d) `SpecWorkspace.vue`、`PlanWorkspace.vue`（三個 Spec 按鈕、Plan 生成按鈕與 bump 的重新執行按鈕停用；`runAssist` 自己也擋）：

```diff
--- a/frontend/src/components/SpecWorkspace.vue
+++ b/frontend/src/components/SpecWorkspace.vue
@@ -7,6 +7,8 @@
 import type { main, spec } from '../../wailsjs/go/models'
 import { useSession } from '../stores/session'
 import { useAssist } from '../stores/assist'
+import { useWorkspaceConfirmation } from '../stores/workspaceConfirmation'
+import { workspaceBlockKeys } from '../i18n/stateKeys'
 import { extractGherkin } from '../lib/gherkin'
 import { templateFor, inScope, SPEC_SCOPE_PATTERNS } from '../lib/planTemplates'
 import { isWriteConflict } from '../lib/writeConflict'
@@ -37,6 +39,7 @@

 const s = useSession()
 const assist = useAssist()
+const wsc = useWorkspaceConfirmation() // B4：未確認／尚未取得／查詢失敗時停用三個 AI 輔助按鈕

 const files = ref<main.FileNode[]>([])
 // selectedPath 是唯一權威來源（A1a-1 缺口 3 修正）：props.path 只在宣告當下 seed
@@ -396,6 +399,10 @@
 // 為操作者已經放棄這次結果，草稿留空。
 async function runAssist(prompt: string) {
   assistError.value = ''
+  if (wsc.blocked) { // B4：後端 R2 閘門是最後防線，這裡先擋下並說明原因
+    assistError.value = t(workspaceBlockKeys[wsc.blockKind])
+    return
+  }
   assistBusy.value = true
   const startedForPath = effectivePath.value
   try {
@@ -751,10 +758,11 @@
     <p v-if="saveError" class="err" data-test="save-error" :data-conflict="saveConflict ? 'true' : undefined">{{ saveError }}</p>

     <div class="assist-buttons">
-      <button data-test="assist-draft" :disabled="assistBusy" @click="draftGherkin">{{ t('spec.action.draftGherkin') }}</button>
-      <button data-test="assist-ambiguity" :disabled="assistBusy" @click="detectAmbiguity">{{ t('spec.action.detectAmbiguity') }}</button>
-      <button data-test="assist-oracle" :disabled="assistBusy" @click="checkOracleCoverage">{{ t('spec.action.checkOracle') }}</button>
+      <button data-test="assist-draft" :disabled="assistBusy || wsc.blocked" @click="draftGherkin">{{ t('spec.action.draftGherkin') }}</button>
+      <button data-test="assist-ambiguity" :disabled="assistBusy || wsc.blocked" @click="detectAmbiguity">{{ t('spec.action.detectAmbiguity') }}</button>
+      <button data-test="assist-oracle" :disabled="assistBusy || wsc.blocked" @click="checkOracleCoverage">{{ t('spec.action.checkOracle') }}</button>
     </div>
+    <p v-if="wsc.blocked" class="err" data-test="assist-blocked">{{ t(workspaceBlockKeys[wsc.blockKind]) }}</p>
     <p v-if="assistError" class="err">{{ assistError }}</p>

     <div class="draft-area">
```

```diff
--- a/frontend/src/components/PlanWorkspace.vue
+++ b/frontend/src/components/PlanWorkspace.vue
@@ -7,6 +7,8 @@
 } from '../../wailsjs/go/main/App'
 import type { main, spec } from '../../wailsjs/go/models'
 import { usePlan } from '../stores/plan'
+import { useWorkspaceConfirmation } from '../stores/workspaceConfirmation'
+import { workspaceBlockKeys } from '../i18n/stateKeys'
 // extractGherkin 只在 info tag 為 gherkin/feature 時才特殊處理，其餘（含無 tag／yaml
 // tag）一律走通用 fence 擷取路徑——對 plan YAML 草稿一樣適用，別名匯入避免誤讀成
 // domain 耦合（沿用既有、已測試涵蓋的 fence 擷取邏輯，不重複實作）。
@@ -21,6 +23,7 @@
 import ExternalChangeCompare from './ExternalChangeCompare.vue'

 const { t } = useI18n()
+const wsc = useWorkspaceConfirmation() // B4：未確認／尚未取得／查詢失敗時停用 AI 草稿（含 bump 的重新執行）

 // PlanWorkspace（Task 13，spec §7 Stage B）：鏡射 SpecWorkspace.vue 的資料流與
 // props 慣例（`path`/`draft`/`write` 皆可注入，測試走這條路徑，避開 CM6 DOM／
@@ -512,6 +515,10 @@
 // await 期間操作者可能已切到另一個檔案，只在 effectivePath 沒變時才採用回傳的 id。
 async function runAssist() {
   assistError.value = ''
+  if (wsc.blocked) { // B4：onBumpRerunAssist 也走這裡；後端 R2 閘門是最後防線
+    assistError.value = t(workspaceBlockKeys[wsc.blockKind])
+    return
+  }
   assistBusy.value = true
   const startedForPath = effectivePath.value
   try {
@@ -790,8 +797,9 @@
         <option value="codex">codex</option>
       </select>
       <textarea v-model="promptInput" data-test="prompt-input" :placeholder="t('planWorkspace.prompt.placeholder')" />
-      <button data-test="generate-draft" :disabled="assistBusy" @click="runAssist">{{ t('planWorkspace.action.generateDraft') }}</button>
+      <button data-test="generate-draft" :disabled="assistBusy || wsc.blocked" @click="runAssist">{{ t('planWorkspace.action.generateDraft') }}</button>
     </div>
+    <p v-if="wsc.blocked" class="err" data-test="assist-blocked">{{ t(workspaceBlockKeys[wsc.blockKind]) }}</p>
     <p v-if="assistError" class="err">{{ assistError }}</p>

     <div class="draft-area">
@@ -846,7 +854,7 @@
           <li v-for="f in bumpPreview.touched_files" :key="f">{{ f }}</li>
         </ul>
         <p class="bump-warning" data-test="bump-warning">{{ t('bump.warning') }}</p>
-        <button type="button" data-test="bump-rerun-assist" @click="onBumpRerunAssist">{{ t('bump.action.rerunAssist') }}</button>
+        <button type="button" data-test="bump-rerun-assist" :disabled="wsc.blocked" @click="onBumpRerunAssist">{{ t('bump.action.rerunAssist') }}</button>
         <button type="button" data-test="bump-confirm" :disabled="busyReason !== ''" @click="confirmBump">{{ t('bump.action.confirm') }}</button>
       </div>
       <!-- bumpConfirmError 獨立於面板外層——Confirm 失敗後觸發的重新預覽
```

- [ ] **Step 5：新測試轉綠，並保留「既有測試前提」的第二類紅燈**

```bash
cd frontend
run_logged t7-tsc npx vue-tsc --noEmit ; show t7-tsc                                       # Expected：rc=0、無輸出
run_logged t7-full-red npx vitest run
show t7-full-red 'Test Files|Tests '
grep -E '^ FAIL' $EVID/t7-full-red.log | sed 's/ > .*//' | sort | uniq -c
cd ..
```

Expected（計畫撰寫時在複本上量到）：新檔案全綠；既有測試 **5 個檔案、12 條**紅：`stores/session.test.ts`（5）、`components/DualPane.test.ts`（1）、`components/PlanWorkspace.test.ts`（2）、`components/SpecWorkspace.test.ts`（2）、`registryUncertain.test.ts`（2）。**分類**：這些測試驗的是「不需確認時」的既有行為，它們的 store 從沒被餵過確認狀態，所以 fail closed 的 `loading` 封鎖了 composer／Assist——是夾具前提，不是回歸。**若失敗的檔案或數量與上面不同**（例如新測試檔紅、或 App 層級測試紅），不得套用 Step 6 的 seed，先追責任歸屬並回報 owner。

- [ ] **Step 6：補夾具前提（只加 seed，不改任何斷言）**

下列五個檔案各在對應的 `beforeEach`（或 `mountApp`）加一行 `seedWorkspaceConfirmation()`，把 store 放進「不需確認」：

```diff
--- a/frontend/src/stores/session.test.ts
+++ b/frontend/src/stores/session.test.ts
@@ -1,6 +1,7 @@
 import { setActivePinia, createPinia } from 'pinia'
 import { beforeEach, describe, expect, it, vi } from 'vitest'
 import { useSession } from './session'
+import { seedWorkspaceConfirmation } from '../test/workspaceConfirmation'
 import type { Envelope } from '../types'

 // env：所有 conversation lane 事件都帶 workspace_session_id（§3.1.5 之後為必填）。
@@ -158,7 +159,10 @@
 })

 describe('session store：focused pane 操作（§3.7）', () => {
-  beforeEach(() => setActivePinia(createPinia()))
+  beforeEach(() => {
+    setActivePinia(createPinia())
+    seedWorkspaceConfirmation() // B4：submit 在 workspace 需確認時不送出；這組驗的是不需確認時的既有行為
+  })

   // pin() 的 action 契約（owner 2026-08-17 追加裁決）：「釘進第 N 格」＝同時明確
   // 選了 session 與 pane，所以第 N 格必須成為作用中的那一格。
```

```diff
--- a/frontend/src/components/DualPane.test.ts
+++ b/frontend/src/components/DualPane.test.ts
@@ -6,6 +6,7 @@
 import SettingsBar from './SettingsBar.vue'
 import { useSession } from '../stores/session'
 import { makeI18n } from '../test/i18n'
+import { seedWorkspaceConfirmation } from '../test/workspaceConfirmation'
 import type { Bindings } from '../types'

 // DualPane（Task 28，spec §3.7）：兩個 pane 都持續接收串流（不管是否
@@ -31,6 +32,7 @@
   beforeEach(() => {
     pinia = createPinia()
     setActivePinia(pinia)
+    seedWorkspaceConfirmation() // B4：不需確認的 workspace（既有行為）
     // jsdom 未實作 scrollTo（PaneView 的 follow-tail watch 會呼叫）
     Element.prototype.scrollTo = Element.prototype.scrollTo ?? (() => {})
     const s = useSession()
```

```diff
--- a/frontend/src/components/PlanWorkspace.test.ts
+++ b/frontend/src/components/PlanWorkspace.test.ts
@@ -6,6 +6,7 @@
 import PlanWorkspace from './PlanWorkspace.vue'
 import { usePlan } from '../stores/plan'
 import { mountWithI18n } from '../test/i18n'
+import { seedWorkspaceConfirmation } from '../test/workspaceConfirmation'

 // wailsjs 綁定 mock：控制 PlanAssist 的 resolve 時機（同 SpecWorkspace.test.ts 慣例）。
 // PlanList/PlanRead 給穩定預設值，避免 onMounted 的載入路徑撞到未定義行為。
@@ -25,6 +26,7 @@
 describe('PlanWorkspace', () => {
   beforeEach(() => {
     setActivePinia(createPinia())
+    seedWorkspaceConfirmation() // B4：這些測試驗的是 Assist 本身；不需確認的 workspace 與既有行為相同
     for (const fn of Object.values(mocks)) fn.mockReset()
     mocks.PlanList.mockResolvedValue([])
     mocks.PlanRead.mockResolvedValue({ content: '', digest: 'sha256:stub' })
```

```diff
--- a/frontend/src/components/SpecWorkspace.test.ts
+++ b/frontend/src/components/SpecWorkspace.test.ts
@@ -6,6 +6,7 @@
 import SpecWorkspace from './SpecWorkspace.vue'
 import { useAssist } from '../stores/assist'
 import { mountWithI18n } from '../test/i18n'
+import { seedWorkspaceConfirmation } from '../test/workspaceConfirmation'
 import zhTW from '../i18n/locales/zh-TW'

 // wailsjs 綁定 mock：控制 SpecAssist 的 resolve 時機，模擬「assist 進行中操作者
@@ -25,6 +26,7 @@
 describe('SpecWorkspace draft accept', () => {
   beforeEach(() => {
     setActivePinia(createPinia())
+    seedWorkspaceConfirmation() // B4：這些測試驗的是 Assist 本身；不需確認的 workspace 與既有行為相同
     for (const fn of Object.values(mocks)) fn.mockReset()
     mocks.SpecList.mockResolvedValue([])
     mocks.SpecRead.mockResolvedValue({ content: '', digest: 'sha256:stub' })
```

```diff
--- a/frontend/src/registryUncertain.test.ts
+++ b/frontend/src/registryUncertain.test.ts
@@ -52,6 +52,8 @@
   SubmitTestContract: vi.fn(), ValidateTestCommit: vi.fn(), EvidenceCommitCandidates: vi.fn(),
   AuthStatus: vi.fn(), StartLogin: vi.fn(), CancelLogin: vi.fn(), Logout: vi.fn(),
   RestartCodexServerRecorded: vi.fn(),
+  WorkspaceConfirmationStatus: vi.fn(async () => ({ required: false, confirmed: false, workspace: '/w', source: 'env', reasons: [] as string[] })),
+  ConfirmWorkspace: vi.fn(async (_p: string) => {}),
   EscalationList: vi.fn(async () => [] as any[]), EscalationAck: vi.fn(),
   EscalationCreate: vi.fn(), EscalationResolve: vi.fn(),
 }))
@@ -61,6 +63,7 @@
 import { useSession } from './stores/session'
 import type { Envelope } from './types'
 import { makeI18n } from './test/i18n'
+import { seedWorkspaceConfirmation } from './test/workspaceConfirmation'

 // memStorage：這個 jsdom 環境沒有 localStorage（vitest 啟動時就印
 // 「localStorage is not available」），而 lib/persist 的 load/save 對缺席是靜默
@@ -123,6 +126,7 @@
 async function mountApp() {
   const pinia = createPinia()
   setActivePinia(pinia)
+  seedWorkspaceConfirmation() // B4：本檔的 CLIInfo mock 沒有 ready，App 不會查詢；先放進不需確認的狀態
   const w = mount(App, { global: { plugins: [pinia, makeI18n()], stubs: STUBS }, attachTo: document.body })
   await flushPromises()
   return w
```

```bash
cd frontend
run_logged t7-full-green npx vitest run ; show t7-full-green 'Test Files|Tests '
cd ..
```

Expected：rc=0，`49 passed`／`704 passed`（605 基準＋99 新測試）。若數字不同，**當場數過再寫**，並說明差異來源。

- [ ] **Step 7：Mutation 證據（測試要紅在正題）**

每個 mutant 有自己的備份、log、rc，並在還原後 `cmp`；`mutant_check` 會確認失敗的是目標測試，並排除 `Failed to resolve import`／`SyntaxError` 等非目標失敗。每個 mutant 只跑本 task 的新測試檔：

```bash
cd frontend
T=(src/stores/workspaceConfirmation.test.ts src/components/WorkspaceConfirmBanner.test.ts src/components/workspaceGate.test.ts src/workspaceConfirmationApp.test.ts)  # 用陣列：zsh 不會對未加引號的變數做 word splitting
run_logged t7-base-focused npx vitest run "${T[@]}" ; show t7-base-focused 'Test Files|Tests '        # Expected：rc=0、全綠（mutation 的基準）

# M1：尚未取得／查詢失敗時視為放行
cp src/stores/workspaceConfirmation.ts $EVID/t7-m1.orig
python3 - <<'EOF'
p = 'src/stores/workspaceConfirmation.ts'
s = open(p, encoding='utf-8').read()
old = "if (s.status === null) return s.queryError === '' ? 'loading' : 'failed'"
assert old in s
open(p, 'w', encoding='utf-8').write(s.replace(old, "if (s.status === null) return 'ok'", 1))
EOF
mutant_check t7-m1 'FAIL .*(workspaceConfirmation|WorkspaceConfirmBanner|workspaceGate|workspaceConfirmationApp)\.test\.ts' npx vitest run "${T[@]}"
restore_check $EVID/t7-m1.orig src/stores/workspaceConfirmation.ts

# M2：壞回覆被補成合法資料（parser 補空值、用 String() 轉型）
cp src/stores/workspaceConfirmation.ts $EVID/t7-m2.orig
python3 - <<'EOF'
import re
p = 'src/stores/workspaceConfirmation.ts'
s = open(p, encoding='utf-8').read()
g0 = s.index('  if (\n    !r ||')
g1 = s.index('    throw new Error(\'WorkspaceConfirmationStatus: malformed response\')\n  }\n') + len('    throw new Error(\'WorkspaceConfirmationStatus: malformed response\')\n  }\n')
s = s[:g0] + "  if (!r) throw new Error('WorkspaceConfirmationStatus: malformed response')\n" + s[g1:]
r0 = s.index('  return {\n    required: r.required,')
r1 = s.index('  }\n}', r0) + len('  }\n')
s = s[:r0] + '''  return {
    required: r.required === true,
    confirmed: r.confirmed === true,
    workspace: String(r.workspace ?? ''),
    source: String(r.source ?? ''),
    reasons: Array.isArray(r.reasons) ? r.reasons.map(String) : [],
  }
''' + s[r1:]
open(p, 'w', encoding='utf-8').write(s)
EOF
mutant_check t7-m2 'FAIL .*workspaceConfirmation\.test\.ts.*(parseStatus|refresh)' npx vitest run "${T[@]}"
restore_check $EVID/t7-m2.orig src/stores/workspaceConfirmation.ts

# M3：拿掉 request sequencing
cp src/stores/workspaceConfirmation.ts $EVID/t7-m3.orig
python3 - <<'EOF'
p = 'src/stores/workspaceConfirmation.ts'
s = open(p, encoding='utf-8').read()
a = "if (token !== this.seq) return // 較新的 refresh 已發出：這筆是舊回應，丟棄"
b = "if (token !== this.seq) return // 舊呼叫的失敗不得覆蓋新狀態"
assert a in s and b in s
open(p, 'w', encoding='utf-8').write(s.replace(a, '', 1).replace(b, '', 1))
EOF
mutant_check t7-m3 'FAIL .*(workspaceConfirmation|workspaceConfirmationApp)\.test\.ts.*(舊|晚)' npx vitest run "${T[@]}"
restore_check $EVID/t7-m3.orig src/stores/workspaceConfirmation.ts

# M4：PaneView.send() 沒有先檢查封鎖（會清掉被擋下的草稿）
cp src/components/PaneView.vue $EVID/t7-m4.orig
python3 - <<'EOF'
p = 'src/components/PaneView.vue'
s = open(p, encoding='utf-8').read()
old = "  if (wsc.blocked) return // 早於清空 draft：被擋下的草稿要留在輸入框\n"
assert old in s
open(p, 'w', encoding='utf-8').write(s.replace(old, '', 1))
EOF
mutant_check t7-m4 'FAIL .*workspaceGate\.test\.ts.*send\(\)' npx vitest run "${T[@]}"
restore_check $EVID/t7-m4.orig src/components/PaneView.vue

# M5：Spec／Plan 的 runAssist 沒有自己的守門
cp src/components/SpecWorkspace.vue $EVID/t7-m5-spec.orig; cp src/components/PlanWorkspace.vue $EVID/t7-m5-plan.orig
python3 - <<'EOF'
import re
for p in ('src/components/SpecWorkspace.vue', 'src/components/PlanWorkspace.vue'):
    s = open(p, encoding='utf-8').read()
    s2 = re.sub(r"  if \(wsc\.blocked\) \{[^\n]*\n    assistError\.value = t\(workspaceBlockKeys\[wsc\.blockKind\]\)\n    return\n  \}\n", "", s, count=1)
    assert s2 != s, p
    open(p, 'w', encoding='utf-8').write(s2)
EOF
mutant_check t7-m5 'FAIL .*workspaceGate\.test\.ts.*(SpecWorkspace|PlanWorkspace)' npx vitest run "${T[@]}"
restore_check $EVID/t7-m5-spec.orig src/components/SpecWorkspace.vue; restore_check $EVID/t7-m5-plan.orig src/components/PlanWorkspace.vue
cd ..
```

Expected：五個 `mutant_check` 都印出「紅在目標」（計畫撰寫時在複本上的紅燈數量見 Self-review 的驗證紀錄；數字以你當場數到的為準）。**每個 mutant 都必須變紅**；沒變紅就是測試失效（例如 disabled 的元素收不到事件，所以 M4／M5 的測試改為直接呼叫元件的 `send()`／`runAssist()`）。

- [ ] **Step 8：build 與完整前端 gate**

```bash
cd frontend
run_logged t7-build npm run build ; show t7-build '' 4
run_logged t7-final-vitest npx vitest run ; show t7-final-vitest 'Test Files|Tests '
cd ..
git status --short | grep -v "^[AM?]" ; git status --short frontend/dist      # Expected：dist 不出現（gitignore）
```

Expected：兩組 rc=0（`vue-tsc --noEmit` 與 `vite build` 皆過；chunk size 警告是既有的；vitest 數字見 Step 6）。

- [ ] **Step 9：Commit**

```bash
git add frontend/src
git commit -m "$(cat <<'EOF'
feat(b4): 前端高風險 workspace 警示、確認流程與停用條件（Task 7）

獨立 store（request sequencing、fail closed：尚未取得／查詢失敗／形狀不對皆封鎖）、
WorkspaceConfirmBanner（role="alert"＋--err、確認／取消兩步流程、原樣傳回 status.workspace）、
composer 與 Spec／Plan Assist 的停用條件與原因、i18n 兩語系與 drift guard。既有測試只補
前提（新 binding 的 mock、不需確認的 seed），不改斷言。

Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 8：文件、E2E 決定與收尾 gate

**Files:**
- Create: `docs/architecture/features/workspace-confirmation.feature`
- Modify: `README.md:117-126`（第一次使用）、`README.md:530`（環境變數表）
- Modify（收尾步驟）: `docs/architecture/pre-m4-readiness-backlog.md`（B4 狀態）

**Interfaces:** 無（文件）。

**E2E 反例的決定（spec §6.3 交由本計畫決定）：放單元／整合測試，不放 Playwright E2E。** 理由：（1）需要的前提是「`WORKBENCH_WORKSPACE` 指向家目錄的隔離替身」，而 `os.UserHomeDir()` 讀的是 `$HOME`，E2E harness 目前以真實 `HOME` 啟動 `wails dev`（`preflight.ts:50-52` 只檢查 fixture 不等於 `HOME`）；要在 E2E 讓兩者相等，必須改 harness 的環境隔離與 `evaluate-e2e-evidence.mjs`／B3b L1 的 `workspace_source=env` 契約，違反 spec「既有 E2E 與 B3b 證據契約都不需修改」；（2）Task 3 的 `TestProductionStartupPath_EnvEqualsHomeRequiresConfirmation` 已走**整條 production 啟動路徑**（`startup → acquireStateLease → resolveWorkspace → 確認狀態 → 閘門`），用 `HOME`＝`WORKBENCH_WORKSPACE`＝同一個隔離目錄，直接斷言警示所需的狀態（`required`、`is_home`）與送出被拒；前端警示與停用則由 Task 7 的 `workspaceConfirmationApp.test.ts`／`workspaceGate.test.ts` 在 App 層級涵蓋；（3）既有 E2E 在 env 有效、非家目錄、身分可判定時狀態為 `NotRequired`，composer 行為不變——**實際跑 E2E 會驗證這點，但本計畫無法在本機宣稱它已通過**，列為未驗證，交由 CI 的 `b3a-e2e-ci` 在 PR 上確認。

- [ ] **Step 1：README**

`README.md:117-126`（第一次使用第 1 點）。在「第一次送出訊息前，先確認畫面頂端的 `ws: <source> @ <path>` 是你要的目錄——否則 AI 會在家目錄而不是你的專案內操作。」之後（同一個列表項內，縮排 3 空格）加入：

```markdown
   **高風險 workspace 需要明確確認**：實際 workspace 是家目錄（含 `cd ~` 後啟動、`WORKBENCH_WORKSPACE`
   指向家目錄或指向家目錄的 symlink）、退回系統暫存目錄、`WORKBENCH_WORKSPACE` 無效而退回其他候選，
   或 App 無法判定它是不是家目錄時，畫面頂端會出現紅色警示，列出實際目錄、來源與原因。按下「確認在此
   目錄進行 AI 工作」並再確認一次之前，送出訊息與 Spec／Plan 的 AI 輔助都會停用（後端同樣會拒絕）。
   確認只在這次執行有效，重啟後要重新確認；它不是檔案沙箱，也不取代逐工具的核可。
```

`README.md:530`（環境變數表）。舊版：

```markdown
| `WORKBENCH_WORKSPACE` | 覆寫 workspace 根目錄（預設：目前工作目錄；不可寫時改用使用者家目錄） |
```

新版：

```markdown
| `WORKBENCH_WORKSPACE` | 覆寫 workspace 根目錄（預設：目前工作目錄；不可寫時改用使用者家目錄；都不可用時退回系統暫存目錄）。路徑無效或無法建立 `.workbench` 時**會靜默退回下一個候選，並觸發高風險確認**（見「第一次使用」） |
```

```bash
git diff -- README.md | head -40
```

注意：owner 的主工作區在本次 session 開始時顯示 `M README.md`（未提交變更）；你的 worktree 從已提交內容建立，不含那份變更。若 owner 之後把那份變更合入，這兩處編輯可能衝突——合併時以文字錨點重新套用，不要覆蓋對方的變更。

- [ ] **Step 2：新增 `docs/architecture/features/workspace-confirmation.feature`**

格式比照既有 feature 檔（`Feature:` 一行、`Scenario:` 區塊、Given／When／Then，繁體中文）。內容依 spec §4 的 R1～R5 與 §2.2 的例子：

```gherkin
Feature: 高風險 workspace 必須先經使用者確認，才能開始 AI 工作

  Scenario: 從 Finder 開啟退回家目錄，確認前送出訊息被拒（R1 (a)、R2）
    Given 我從 Finder 開啟 app，workspace 退回使用者家目錄
    Then 頂部列出現高風險警示，列出實際目錄、來源 home 與原因
    And 送出訊息與 Spec／Plan 的 AI 輔助都是停用的
    When 前端被繞過而直接呼叫 StartSession
    Then 後端回 workspace_confirmation_required，沒有任何 provider 行程、broker、MCP config、recorder 被建立
    And 後端 best-effort 嘗試寫入一筆 workspace_confirmation_blocked 稽核（不含 prompt）；稽核寫入失敗不影響拒絕本身

  Scenario: 明確確認之後可以開始，且同一次執行內所有 session 共用（R3、R4）
    Given workspace 處於待確認，且 audit writer 運作正常
    When 我按下「確認在此目錄進行 AI 工作」並在對話中再確認一次
    Then 後端把狀態轉為已確認，並 best-effort 嘗試寫入一筆 workspace_confirmed 稽核（audit writer 正常時恰好留下一筆）
    And 稽核寫入失敗不阻擋確認與後續啟動
    And 所有 session 與 Spec／Plan 的 AI 輔助都可以開始
    When 我重複確認
    Then 結果成功，狀態不變，不再嘗試寫稽核

  Scenario: 取消確認不改變狀態（R5）
    Given 警示的確認對話已開啟
    When 我按下「取消」
    Then 對話關閉，沒有呼叫 ConfirmWorkspace，AI 工作仍是停用的

  Scenario: 重啟後回到未確認（R4）
    Given 我已確認過 workspace
    When 我重新開啟 app
    Then 依 R1 重新判定，仍是高風險時再次要求確認，不從稽核、registry 或 resume 恢復

  Scenario: WORKBENCH_WORKSPACE 有效的專案目錄不需確認（R1）
    Given WORKBENCH_WORKSPACE 指向家目錄底下的專案目錄，且家目錄身分可判定
    Then 不顯示警示，行為與沒有 B4 之前相同

  Scenario: WORKBENCH_WORKSPACE 無效而靜默退回其他候選，仍要確認（R1 (b)）
    Given WORKBENCH_WORKSPACE 指向不存在的路徑，app 退回目前工作目錄
    Then 警示顯示退回的原因（路徑無法解析或無法建立 .workbench）

  Scenario Outline: 實際目錄就是家目錄，不論來源，都要確認（R1 (c)）
    Given workspace 的來源是 <來源>，且實際目錄與使用者家目錄是同一個目錄
    Then 警示出現，原因包含「實際目錄就是使用者家目錄」

    Examples:
      | 來源                                       |
      | WORKBENCH_WORKSPACE 設成家目錄             |
      | 在終端機 cd ~ 後啟動（來源 cwd）            |
      | WORKBENCH_WORKSPACE 指向家目錄的 symlink    |

  Scenario: 退回系統暫存目錄要確認（R1 (a)）
    Given 所有候選目錄都無法使用，app 退回系統暫存目錄
    Then 警示出現，原因包含「退回系統暫存目錄」

  Scenario: 家目錄身分無法判定時要確認（R1 (d)）
    Given 無法取得使用者家目錄，或無法對家目錄或 workspace 取得檔案資訊
    Then 即使 WORKBENCH_WORKSPACE 本身正常也要確認，警示顯示可辨認的原因

  Scenario: 前端顯示的目錄與後端快照不一致時，確認被拒（R3）
    Given 畫面上的目錄與後端實際的 workspace 字串不完全相同
    When 呼叫 ConfirmWorkspace
    Then 回傳錯誤，狀態維持原樣，不嘗試寫成功稽核

  Scenario: 查詢失敗或尚未取得狀態時，前端不放行（R5）
    Given WorkspaceConfirmationStatus 尚未回覆、查詢失敗，或回覆形狀不對
    Then 送出訊息與 Spec／Plan 的 AI 輔助都是停用的
    And 晚到的舊回應不會覆蓋較新的狀態
```

- [ ] **Step 3：完整 gate（收尾驗證）**

每個指令都用 `run_logged`（完整輸出先存檔、rc 立即存檔），`show` 只讀已存好的檔。`git diff --check` 使用 Task 0 記錄的 base 做**三點比較**（`${BASE}...HEAD`），**不得**在一個範圍失敗後改用另一個範圍；失敗就照實回報 rc 與內容，不以第二次的 rc 代表整個檢查通過。

```bash
BASE=$(cat $EVID/base.txt)
run_logged final-gofmt gofmt -l . ; wc -c < $EVID/final-gofmt.log                          # Expected：0
run_logged final-vet go vet ./... ; show final-vet                                         # Expected：rc=0
run_logged final-go go test -race ./... -count=1 -timeout 30m
show final-go '^(--- FAIL|FAIL|panic|WARNING: DATA RACE)'; grep -cE '^ok' $EVID/final-go.log
(cd frontend && run_logged final-build npm run build && run_logged final-vitest npx vitest run)
show final-build '' 3; show final-vitest 'Test Files|Tests '
```

Expected：gofmt 0 位元組、vet rc=0、`go test -race` rc=0 且無 `DATA RACE`；vitest 全綠；build rc=0。

**`git diff --check` 分兩步（Wails 產物有兩行 tab 空行的狹窄例外，見 Global Constraints 第 22 條與 Task 2 Step 6）：**

*步驟 1：完整檢查，原始輸出與 rc 存檔；`${BASE}...HEAD` 三點比較，不得改用其他範圍。* **預期 rc 不是 0**（`models.ts` 的兩行已知警告，git 以 rc=2 結束）——**不得把這個非零 rc 寫成 clean**，回報時照實寫出原始 rc 與原始輸出：

```bash
BASE=$(cat $EVID/base.txt)
run_logged final-diffcheck git diff --check ${BASE}...HEAD
show final-diffcheck '' 20                                  # 照實顯示：rc 與完整原始輸出
```

*步驟 2：對步驟 1 已存好的原始輸出做精確比對。* 允許的只有 `frontend/wailsjs/go/models.ts` 中、屬於 `WorkspaceConfirmationInfo` 類別範圍內的兩行 `+<tab>` trailing whitespace；其餘任何警告（含手寫程式、其他檔案、`models.ts` 的其他位置）都不允許。比對腳本只讀 `$EVID/final-diffcheck.log` 與 `HEAD:models.ts`，過濾後剩下的必須為空，且被過濾的剛好是 2 項、行號都落在該類別範圍內、行內容恰為一個 tab：

```bash
python3 - "$EVID/final-diffcheck.log" <<'EOF'
import re, subprocess, sys
MODELS = 'frontend/wailsjs/go/models.ts'
log = open(sys.argv[1], encoding='utf-8').read().split('\n')
if log and log[-1] == '':
    log.pop()
if len(log) % 2:
    print('FAIL: 輸出不是「檔名:行號: 訊息 / +內容」成對的格式，行數 =', len(log)); sys.exit(1)
head = subprocess.check_output(['git', 'show', 'HEAD:' + MODELS], text=True).split('\n')
start = next(i for i, l in enumerate(head, 1) if l == '\texport class WorkspaceConfirmationInfo {')
end = next(i for i in range(start, len(head) + 1) if head[i - 1] == '\t}')   # 類別結尾（縮排恰為一個 tab）
allowed, rest = [], []
for i in range(0, len(log), 2):
    m = re.match(r'^(.*):(\d+): (.*)$', log[i])
    ok = (m and m.group(1) == MODELS and m.group(3) == 'trailing whitespace.' and log[i + 1] == '+\t'
          and start < int(m.group(2)) < end and head[int(m.group(2)) - 1] == '\t')
    (allowed if ok else rest).append((log[i], log[i + 1]))
print('類別範圍 %d-%d；允許的警告 %d 項；其餘 %d 項' % (start, end, len(allowed), len(rest)))
for r in rest:
    print('不允許：', r)
sys.exit(0 if (len(allowed) == 2 and not rest) else 1)
EOF
echo "filter rc=$?"                                         # Expected：允許的警告 2 項、其餘 0 項、filter rc=0
```

任何一項不符（允許的不是剛好 2 項、或有任何「不允許」的警告）就**停下來回報**，不得放寬腳本、不得改用其他範圍、不得用 `.gitattributes`／`core.whitespace`／`-c` 忽略空白。**任何紅燈先分類**（真實缺陷／夾具前提／已登記的牆鐘測試，見 `docs/architecture/wall-clock-test-register.md`），不得重跑到綠燈後掩蓋；已登記牆鐘測試的紅燈要單獨重跑並把「首次紅燈＋重跑結果」都寫進回報。再確認受保護的既有測試沒有被修改：

```bash
run_logged final-protected git diff ${BASE}...HEAD --stat -- app_binding_surface_test.go app_startup_state_test.go main_singleinstance_test.go app_cliinfo_ready_test.go ; wc -c < $EVID/final-protected.log
# Expected：0（四個檔案都沒有變更）
```

- [ ] **Step 4：backlog 的 B4 狀態（收尾步驟，不在本 task 預先寫死數字）**

沿用 A4／A5 關票先例（commit `8671551`、`787612d`）的形狀更新 `docs/architecture/pre-m4-readiness-backlog.md`：（a）檔頭標題與「版本」行 rev83 → rev84，並把 rev83 那段移到「前版」；（b）`### B4 Workspace 明確確認` 段落（base 行 299）加一條 `**狀態（rev84）**` 摘要，寫明驗收三項各自對應的證據（驗收 (1)：確認閘；(2)：高風險警示；(3)：fallback 路徑的確認閘測試——`workspace_resolver_test.go`、`workspace_gate_test.go`）、實際使用的 commit 範圍、測試數量（**當場數過再寫**）、以及保留的限制（codex CLI 自身的初始化寫入未驗證；啟動後外部檔案系統變動與 `cmd/probe-*` 不受保護；E2E 未在本機跑）；（c）估點表 B4 列（base 行 520）標註狀態，**點數不改**（原估 0.6 pt 與本票估點的差異屬於估點檢討，不在此回填）；（d）「修訂記錄」補一條 rev84。證據引用 `$EVID` 內的 log 檔名與 SHA256（`shasum -a 256`）。本步驟在 PR 的最後一個 commit 做，並等 owner 確認關票條件後才執行。

- [ ] **Step 5：Commit（文件）**

```bash
git add docs/architecture/features/workspace-confirmation.feature README.md
git commit -m "$(cat <<'EOF'
docs(b4): README 高風險 workspace 確認說明＋workspace-confirmation.feature（Task 8）

README 第一次使用與環境變數表補上確認流程、tmp fallback 與「env 無效會靜默退回並觸發確認」。
新增 Gherkin 行為規格。E2E 反例改由 Go 整合測試（整條 production 啟動路徑）與前端 App 層級
測試涵蓋，理由見實作計畫 Task 8。

Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>
EOF
)"
```

---

## Self-review

### 1. Spec 涵蓋度（逐項對照）

| spec 項目 | 對應 task／測試 |
|---|---|
| §1.2 目標：四類 AI 工作都被擋 | Task 3（startSession 首輪／resume、sendMessage）、Task 4（Spec／Plan Assist） |
| §1.3 非目標 | Global Constraints 24；沒有任何 task 變更 App 寫入時機、fallback 順序、picker |
| §2.2 例子 1 | Task 3 `TestProductionStartupPath_EnvEqualsHomeRequiresConfirmation`、Task 7 警示測試 |
| 例子 2、7 | Task 1 `ValidEnvOutsideHomeNeedsNoConfirmation`、`ProjectUnderHomeIsNotHome`；Task 3 `…EnvProjectNeedsNoConfirmation` |
| 例子 3 | Task 1 `InvalidEnvFallsBackToCwd`、`EnvMkdirFailureIsClassified`、`InvalidEnvFallsBackToHome` |
| 例子 4、5、6 | Task 1 `EnvEqualsHomeIsHome`、`CwdIsHome`、`SymlinkToHomeIsHome`（H1） |
| 例子 8 | Task 1 `AllCandidatesFailFallsBackToTmp`、`HomeCandidateMkdirFailsThenTmp` |
| 例子 9 | Task 3 `TestSendMessageGate_*` |
| 例子 10 | Task 4 `TestSpecAssistGate_*`、`TestPlanAssistGate_*` |
| 例子 11 | Task 2 `TestConfirmWorkspace_PathMismatchRejectedInEveryState` |
| 例子 12 | Task 2 `TestWorkspaceConfirmation_NewAppInstanceStartsPendingAgain` |
| 例子 13 | Task 1 `HomeIdentityUnknown`（三種）、Task 1 policy (d) |
| R1 (a)–(d)、Invariant、多原因 | Task 1 `TestWorkspacePolicy` 全表、resolver 測試 |
| R2 全部 Post（含 claude／codex、首輪／resume、`approvalPolicy=never`） | Task 3 |
| R2 Pre（`registryUncertain` 只屬 startSession、錯誤優先順序） | Task 3 `TestGate_PreservesExistingErrorPrecedence`；Task 4 unknown provider |
| R2 Assist 允許的 context 管理、cancel、gen／assistActive 未登記 | Task 4（`hookAssistGateRejected`、`assistActiveCount`） |
| R2 Invariant（不改 phase） | Task 2 `TestWorkspaceConfirmationBindings_FollowLifecycle`（Pending 時仍可呼叫） |
| R3 處理順序、三狀態、冪等 | Task 2 |
| R3 比對對象／Invariant | Task 2（Unicode、別名）、Task 7（原始字串轉發） |
| R4 | Task 2、Task 3（一個確認涵蓋所有 session） |
| R5 全部 | Task 7 |
| §5.1 resolver／seam | Task 1 |
| §5.2 狀態＋lifecycle 測試 | Task 2（含 `TestStateBindingsRejectedAfterShutdown` 加項） |
| §5.3 binding／wailsjs | Task 2 |
| §5.4 閘門位置（五個入口） | Task 3、Task 4（逐行標示） |
| §5.4 末段：Codex 登入（H3）；owner 裁定追加的 `Logout`／`RestartCodexServerRecorded`／`RecoverCodexRecording` | Task 6 |
| §5.5 audit | Task 5 |
| §5.6 前端 | Task 7 |
| §5.7 README／backlog | Task 8 |
| §6.1 Go 測試全部 | Task 1–6；「並行 Confirm 與 StartSession」＝Task 5 行為斷言＋併發壓力；「audit 在鎖外」＝Task 5 |
| §6.2 前端測試全部 | Task 7 |
| §6.3 E2E | Task 8 的決定（單元／整合測試，不放 Playwright），反例由 Task 3 涵蓋 |
| §7 相容性（adapter、mock、既有 binding 不變） | Task 7 |
| §9 H1、H3、H4 | Task 1（H1）、Task 6（H3）、Task 5（H4：`WrittenOutsideMutex`，無鎖序問題）；H2 已由 spec §5.4 取代，Task 4 逐行標位置 |

**spec 中沒有對應任務的項目（明列）：**
- §7 剩餘風險（啟動後外部檔案系統變動、`cmd/probe-*`）：spec 明確不處理，只需記錄；本計畫在 Task 8 backlog 步驟要求寫入限制，沒有測試。
- §7 「與 B3b-ready 的衝突」「行為改變（使用者可見）」：屬排程與溝通，沒有程式工作。
- §8 估點：已在文首核對。

### 2. 佔位掃描

已逐項檢查：計畫內沒有 TBD／TODO／「適當的錯誤處理」／「類似 Task N」；所有程式步驟都附實際程式碼。**例外（照實標示，不是佔位）**：Task 8 Step 4（backlog 收尾）的數字與 commit 範圍必須由實際執行結果填入，spec §5.7 明定「關票時更新」，因此寫成收尾步驟並列出每個欄位的來源。

### 3. 型別與名稱一致性

- `workspaceResolution`／`workspaceProvenance` 欄位名（`Workspace`、`State`、`Source`、`Err`、`Provenance`；`EnvSet`、`EnvRejected`、`EnvRejectReason`、`IsHome`、`HomeIdentityKnown`）在 Task 1 定義，Task 2 的 `resolve(r workspaceResolution)`、`notRequiredResolution`／`pendingResolution` 與所有測試一致。
- `requireWorkspaceConfirmed(op, provider, wsid string) error` 在 Task 3 定義、Task 3／4／5 的呼叫點與 op 字串（`start_session`、`send_message`、`spec_assist`、`plan_assist`）與 Task 5 audit 測試的期望值一致。
- 錯誤變數 `errWorkspaceConfirmationRequired`／`errWorkspaceConfirmPathMismatch`／`errWorkspaceConfirmUnavailable` 在 Task 2 定義，Task 2–6 測試一致。
- 前端 `blockKind` 的值（`ok`／`loading`／`failed`／`pending`）與 `workspaceBlockKeys` 的 key（`loading`／`failed`／`pending`）一致；`workspaceReasonKeys` 的 key 與 Go `wsReason*` 的值一致，由 `workspaceReasonKeys.test.ts` drift guard 機械檢查。
- binding 簽章：Go `WorkspaceConfirmationStatus() (WorkspaceConfirmationInfo, error)`／`ConfirmWorkspace(expectedPath string) error` ↔ wailsjs `WorkspaceConfirmationStatus():Promise<main.WorkspaceConfirmationInfo>`／`ConfirmWorkspace(arg1:string):Promise<void>` ↔ `types.ts` `WorkspaceConfirmationBindings`，由 `vue-tsc` 與 `bindings.test.ts` 鎖住。

### 4. 驗證紀錄（rev2；逐次列出，**全部是起草者在複本上的回報，未經獨立驗證**）

複本：`/Users/eason_tseng/b3a-evidence/b4/plan-scratch`（由 spec-wt 複製、不含 `.git`；`frontend/node_modules` 是指向另一個 worktree `b3a-evidence/a4/wt` 的唯讀 symlink，`package-lock.json` 逐位元組相同）。**檔案版本**：以複本內所有非忽略檔案（排除 `.git`、`node_modules`、`dist`、`.workbench`、`*.orig`）的 `find | sort | shasum -a 256 | shasum -a 256` 摘要識別：rev2 驗證前 `299a334dc1081e0291c17e2a3092ff5fa2e95761f2a819324c7ef3b3b8689bdf`，驗證全部結束後 `299a334dc1081e0291c17e2a3092ff5fa2e95761f2a819324c7ef3b3b8689bdf`（相同，確認所有 mutant 都已還原）。計畫內的程式碼區塊取自這個版本。原始 log 與 rc：`/Users/eason_tseng/b3a-evidence/b4/plan-rev2-verify/evid/<name>.log`／`<name>.rc`（計畫內各 Step 逐字抽出後以 `source` 執行，所以這些指令本身也被執行過）。

| # | 時間（2026-10-11） | 範圍 | 項目 | 結果（log 名稱） |
|---|---|---|---|---|
| 1 | 15:47:10–15:52:20 | **完整** | `go vet ./...`；`go test -race ./... -count=1 -timeout 30m` | 兩者 rc=0；23 個 package `ok`；根 package 302 秒；無 `FAIL`／`panic`／`DATA RACE`（`v-vet`、`v-go-race`） |
| 2 | 約 15:43–15:47 | 完整（僅根 package，不加 `-race`） | `go test -count=1 -timeout 20m .`（Task 3 Step 6） | rc=0，250.6 秒（`t3-full-green`） |
| 3 | 15:39–15:42 | **完整**（前端） | `npm run build`（`vue-tsc --noEmit` ＋ `vite build`）；`vitest run` | 兩者 rc=0；49 檔／704 條全綠（基準 44 檔／605 條，新增 5 檔、99 條）（`t7-build`、`t7-final-vitest`） |
| 4 | 約 15:42 | 完整（產生檔） | `wails generate module`（Wails v2.13.0）後 runtime 三檔 `chmod 644` | rc=0；產出與複本內 `frontend/wailsjs` 逐檔相同，`diff -r` 無差異（`v-wails`） |
| 5 | 15:52:30–15:56:32 | focused | Task 1 Step 4／5、Task 2 Step 4／5、Task 3 Step 7、Task 4 Step 4／5、Task 5 Step 4／5、Task 6 Step 2 | 各 `run_logged` rc=0（正向）；Go mutant 12 個（T1×2、T2×3、T3×2、T4×3、T5×2），`mutant_check` 全數印出「紅在目標」，還原後 `cmp` 相同 |
| 6 | 約 15:37 | focused | Task 6 Step 3（4 個 mutant） | 全部「紅在目標」，還原 `cmp` 相同（`t6-auth`／`t6-logout`／`t6-restart`／`t6-recover`） |
| 7 | 約 15:36–15:38 | focused | Task 7 Step 7：基準 95 條全綠（4 個測試檔），5 個 mutant | 全部「紅在目標」，紅燈數 47／39／3／1／6（共 95 條），還原 `cmp` 相同（`t7-base-focused`、`t7-m1`…`t7-m5`） |

**這次 rev2 修改後有重跑完整 gate**：第 1–3 列是在含 Task 6 追加測試（`TestCodexServerEntryPointsSendNoThreadOrTurn`）、Task 4 的 `defer cancel()`、嚴格 `parseStatus` 的最終版本上執行的；rev1 時 Task 6 追加的測試只跑過 focused 與 4 個 mutant，那個缺口在 rev2 已補上（第 1、2、5、6 列）。

**rev3 補充驗證（Task 8 Step 3 的 `git diff --check` 過濾，起草者在複本上的回報，未經獨立驗證）**：在 `/Users/eason_tseng/b3a-evidence/b4/plan-rev3-verify/repo`（小型 git repo：base＝spec-wt 的 `models.ts`，HEAD＝原型產物 `plan-scratch/frontend/wailsjs/go/models.ts`）以計畫內逐字抽出的 Step 1／Step 2 指令執行；各情境的原始 log、rc 在 `plan-rev3-verify/evid-<情境>/`（`final-diffcheck.log|.rc`、`step2.out`、`filter.rc`）。結果：**正例**（原型產物）原始 `git diff --check` rc=2、4 行輸出，過濾後「允許 2 項、其餘 0 項」，filter rc=0；**反例**全部判為失敗（filter rc=1）：其他檔案多一項 trailing whitespace（`foo.go`）、`models.ts` 其他位置多一項、類別範圍內多一行 tab 空行（變成 3 項）、少一行（只剩 1 項）、其中一行內容改成兩個空格。

**尚未重跑，或無法在最終版本上重現**：
- 各 Task 的 Step 2「首次紅燈」（Task 1、2、4、5、7）：是起草過程中逐步實作時觀察到的；最終版本的符號已存在，無法重現，也沒有保存完整原始 log。計畫內寫的 Expected 是當時觀察，不是 rev2 的驗證結果。
- Task 3 Step 4 的「未補夾具的整包紅燈」（87 行 `--- FAIL`、全部含 `workspace_unresolved`）與 Task 7 Step 5 的「5 個檔案、12 條既有測試紅燈」：起草 rev1 時在「該 task 剛完成」的複本上量到（Task 3 那份 log 為 `/Users/eason_tseng/b3a-evidence/b4/measure-t3-red2.log`），rev2 的修改（parser、`defer cancel()`、Task 6 追加測試）之後**未重跑**。
- Task 0、Task 8 的指令（`git worktree add`、`git commit`、三點 `git diff --check`、backlog、README 與 feature 檔文字）：未執行，也無法機械驗證。
- Playwright E2E、`wails build`、GUI 實機、CI 上的行為（`HOME` 環境、`b3a-e2e-ci`）。

**撰寫過程中發現並修正的計畫問題**（保留以供審查者了解測試設計演進）：（a）`hookAudit` 阻塞測試在 mutant 下會死鎖整個 package——已改成不在鎖內讀 `snapshot()` 並用 `t.Cleanup` 放行；（b）disabled 的 `textarea`／`button` 收不到 VTU 事件，使兩個前端守門測試一度是空測試——已改為先在放行狀態輸入、再翻成封鎖，並直接呼叫 `send()`／`runAssist()`；（c）閘門若挪到 `BeginNewSessionSubmit` 之後並 `RejectSubmit`，原測試抓不到——已加入「先前跑完的 session 不得被重設成 idle」的比對；（d）`T=(…)` 必須用陣列，zsh 不對未加引號的變數做 word splitting；（e）rev2 抽出計畫內指令實際執行時發現：`mutant_check` 的目標樣式以 `-` 開頭時 `grep` 會當成選項（已改 `grep -e`）、Task 2 的 M2 在 Task 3 之後會重複加 `strings` import 而 build failed（已改為先檢查再加）——這正是「排除非目標失敗」要擋的情形。

---

## 給審查者的待決事項（spec 與程式碼不符、或 spec 沒有涵蓋；**未自行改 spec**）

**owner 裁定（2026-10-11 14:53）：全部照主審的建議處理。** 下列每項標註裁定與日期；計畫內容已依裁定修訂。

1. **`WorkspaceConfirmationStatus` 必須回傳 `(T, error)`。** spec §5.3 表格只寫回傳 `{required, confirmed, workspace, source, reasons[]}`；但 binding surface 的薄包裝守門（`thinWrapperViolationTyped`）要求最後一個回傳值是 `beginTxn` 的 `err`。本計畫採 `(WorkspaceConfirmationInfo, error)`（前端 Promise 在 lifecycle 拒絕時 reject，fail closed）。屬於 spec 未寫明的簽章細節，不違反任何 spec 條文。→ **裁定（2026-10-11）：照主審建議，維持 `(T, error)`。**
2. **零值（從未解析 workspace）的行為，spec 沒有規定。** 本計畫選 fail closed（`wcUnresolved`：閘門拒絕、`ConfirmWorkspace` 也拒絕），代價是共用夾具 `newTestAppIn`（`app_test.go`，不在 §6.1 受保護清單）要補 1 行把 workspace 放進「已解析且不需確認」。複本上驗證：只補這 1 行，整個根 package 測試即全數恢復（無需改任何個別測試）。替代方案「零值＝`NotRequired`」不需改夾具，但任何沒走 `acquireStateLease` 的路徑會靜默放行。→ **裁定（2026-10-11 14:53）：同意零值 fail closed，在 `newTestAppIn` 補 1 行的做法照計畫進行。**
3. **§5.4 對 `specAssist` 寫「安排拒絕時一定會執行的 cancel 收尾」。** rev1 在拒絕分支只用 inline `cancel()`（沿用既有 `ErrAssistActive` 分支的慣例）；Codex plan review001 指出那是在確認閘與測試 hook 執行**之後**才取消，與 spec §5.4 的固定順序（閘門之前先安排取消）不一致。→ **裁定／修訂（2026-10-11，依 Codex plan review001）：改為 `defer cancel()`（purpose pin 之後、確認閘之前）＋拒絕分支 inline `cancel()` 雙重保護，符合 spec §5.4。** （owner 於 14:53 對 rev1 的「同意 inline `cancel()`」被本次修訂取代；`CancelFunc` 可重複呼叫，不需要旗標。）送 Codex plan gate 時主審仍會特別標出這一點。
4. **為了斷言「本次 context 已被取消」新增了 `hookAssistGateRejected` 測試 hook。** context 在函式內部建立，沒有其他可觀察點。B6a 驗收 (4) 曾要求「不新增 ad-hoc test hook 於 App aggregate」，但那是 B6a 的範圍；B4 沿用 `App` 上既有約 15 個 `hook*` 欄位的慣例。替代：不加 hook，改靠 `go vet` 的 `lostcancel` 靜態檢查——但那不是單元測試。→ **裁定（2026-10-11 14:53）：同意新增 `hookAssistGateRejected`。**
5. **spec 只討論 `AuthStatus`／`StartLogin`（H3），但 `Logout`、`RestartCodexServerRecorded`、`RecoverCodexRecording` 同樣會以 workspace 為 cwd 啟動（或替換）app-server**（`app.go:8086`），且 claude 的 `auth status`／`logout` 以 `exec.Command` 啟動、沒有設定 `Dir`。→ **裁定（2026-10-11 14:53）：這三個入口不加閘門，維持 spec 範圍；但要加進 Task 6 的 fake app-server 驗證，斷言不會送出 `thread/start`、`turn/start`，做法與 `AuthStatus`／`StartLogin` 相同。** 計畫已依此修訂 Task 6（新增 `TestCodexServerEntryPointsSendNoThreadOrTurn` 與三個 mutant，估時 +0.2 hr）。claude 分支的 `exec` 不在 fake app-server 的觀察範圍內，列為未驗證。
6. **`isHome`／`homeIdentityKnown` 在 CI 或 E2E 的環境風險。** 若 CI runner 的 `HOME` 未設或指向不存在的目錄，R1 (d) 會讓 env 注入的 E2E 也要求確認，composer 會被停用。spec §6.3 已承認這個前提；本計畫無法在本機驗證 CI 環境，已列為未驗證。建議 PR 開出後由 `b3a-e2e-ci` 的實跑確認。→ **已告知 owner（2026-10-11 14:53），屬於風險，不需裁定。**
7. **README 衝突風險。** session 開始時 owner 的主工作區顯示 `M README.md`（未提交變更），本計畫對 README 的兩處編輯（117–126、530）可能與那份變更衝突；實作 worktree 從已提交內容建立，看不到那份變更。→ **已告知 owner（2026-10-11 14:53），屬於風險，不需裁定。**
8. **根 package 每次 `go test` 會在 repo 根目錄留下被 `.gitignore` 的 `.workbench/`**（既有測試在沒有 env 時走 cwd 候選）。這是既有行為，與本票無關，但會出現在 `git status --ignored`。→ 屬既有行為的觀察，不需裁定。
9. **登記在案的牆鐘測試。** 計畫撰寫時的第一次全套 `-race` 中，`internal/codex` 的 `TestAppServerMidStreamDeath`（`wall-clock-test-register.md` #5）失敗一次（`handshake: context deadline exceeded`），當時同一台機器正在同時跑 vitest 與 `wails generate module`；單獨重跑 `-count=5` 全過。本票沒有修改 `internal/codex`。實作時若再遇到，請依該登記表分類，不要靜默重跑。（維持原樣。）
