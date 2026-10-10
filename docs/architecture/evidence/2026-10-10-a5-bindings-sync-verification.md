# A5 bindings 產生檔同步：收尾驗證紀錄

> 2026-10-10：A5 的處置已由 `9cb2e67`（2026-09-08，owner 裁定採方案 A）完成。本紀錄在 main 現況上重新產生 bindings 驗證，結果內容差異為零。本次**沒有提交產生檔內容變更**；生成器在隔離 worktree 重寫了檔案，另有三個 runtime 檔案的權限變更，只新增本紀錄，並在 backlog 標註 A5 完成。

## 0. 範圍

- backlog A5 的範圍：先唯讀核對產生方式（何時、由誰、是否應進版控）、完整差異與使用端影響；處置方式與是否更新產生檔，另案授權。
- owner 已於 2026-09-08 裁定處置採方案 A，由 `9cb2e67` 實施。本紀錄只驗證那次處置在現在的 main 上仍然成立。

## 1. 先前的處置（`9cb2e67`，2026-09-08）

依該 commit 訊息：

- **成因**：產生檔自 `cb477bb`（2026-08-17）之後就沒有再產生；`TerminalCause` 則在 `53c94f0`（B6 Task 6b）加入 `app.go`，因此 `frontend/wailsjs` 落後於 Go 端。
- **處置（方案 A）**：以 `wails generate module` 重新產生，只保留 `models.ts` 中 `GateEntryDTO.terminal_cause` 的兩行差異。Wails CLI 會把 runtime 三個檔的權限改成 755，這是產生時的副作用（內容沒有差異），當時已用 chmod 644 恢復。`App.d.ts`／`App.js` 沒有差異。
- **使用端影響**：`App.vue` 的 `refreshGate()` 是逐欄挑選欄位，`types.ts` 自訂的 `GateEntry` 也沒有這個欄位，所以沒有發現會改變目前畫面行為；以後若要顯示 Gate 的終態原因，仍需另外修改資料映射與畫面型別。
- **當時的驗證**：`npm run build`（vue-tsc --noEmit）exit 0、vitest 466/466、`git diff --check` 乾淨。

## 2. 本次驗證（2026-10-10）

- **基準**：`04ad111`，tree `a60492ae…`，與當時的 main `367526b` 相同。`9cb2e67` 已在 main 的歷史中（`git merge-base --is-ancestor` 成立）。這之後 main 只有文件變更（rev81），不影響 Go 與產生檔。
- **環境**：go1.27.1 darwin/amd64；Wails CLI v2.13.0，與 `go.mod` 釘選的 `github.com/wailsapp/wails/v2 v2.13.0` 相同。
- **指令**：在 repo 外的隔離 worktree 執行 `wails generate module`。因為 `main.go` 會 embed `frontend/dist`，只在該 worktree 內放了一個佔位檔；這不影響 bindings 的內容。
- **首次嘗試**：`generate.log` 記錄 rc=1，錯誤為 `main.go:22:12: pattern all:frontend/dist: no matching files found`。之後僅在隔離 worktree 補上佔位 dist，再執行生成；首次失敗保留，不稱整個批次首次通過。
- **修正 fixture 後的結果**：
  - rc 0。`models.ts`、`App.js`、`App.d.ts` 都在本次被重新寫入（mtime 為 22:11:19）。
  - `git -c core.fileMode=false diff -- frontend/wailsjs`：**0 行**，內容與 committed 版本完全相同。
  - `git diff --summary`：只有 runtime 三個檔 `100644 => 100755` 的權限變化，與 `9cb2e67` 記錄的副作用相同。
- **結論**：committed 的產生檔與 Go 端目前同步，A5 的處置仍然成立。

## 3. 觀察（不在 A5 範圍內，未處理）

- CI 的 `wails-build` job 中，Wails 建置步驟是 `wails build -s`；該 job 沒有在產生後比對產生檔是否與 committed 版本一致。所以未來 Go 端的 binding 型別一旦變更，同樣的落後還可能再發生。是否加入漂移檢查（例如在 CI 重新產生後，以 `git diff --exit-code` 比對，並忽略權限差異）需要 owner 另外決定。
- 是否應進版控：目前產生檔是 committed 的，前端 build 與 vitest 都直接引用；本紀錄不改變這個做法。

## 4. 限制

- 只驗證 bindings 的內容是否同步，沒有跑 Go 測試、前端測試、`wails build` 或 GUI，本次未提交任何程式碼或產生檔內容變更；這不是完整建置或 GUI 驗證。內容同步的結論限於上述 source 與 Wails v2.13.0／Go1.27.1 驗證環境，不外推為 CI Go1.26.5 的獨立再生證明。
- 原始輸出保存在 repo 外：`~/b3a-evidence/a5/verification.txt`、`generate.log`（首次失敗）、`generate2.log`（修正 fixture 後成功）。
