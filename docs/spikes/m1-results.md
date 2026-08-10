# M1 驗收結果（進行中）

- 執行依據：`docs/architecture/sdlc-workbench-m1-plan.md`（v13 APPROVED，SHA256 `c2c1f237464e7b415e86ce3df4513ef1a0e53bda41955d1f62f31c30b6f142ac`）
- 基線：main @ `05415b9`；branch `m1-mvp`（`9564727`…`91fba7e`，Task 1–11 全數 commit）
- 狀態：**自動 gate 全 PASS；V0–V6 live 驗收待 owner 操作**（見「待驗收」節）

## 版本基線（CLI pin 不變聲明）

| 項目 | 版本 | 備註 |
|---|---|---|
| claude CLI | 2.1.223 | 沿 M0 pin，M1 未變更 |
| codex CLI | 0.146.1 | 沿 M0 pin，M1 未變更 |
| Go | 1.26.5 darwin/amd64 | |
| BAT 參考 | commit `72dc4ba`（non-normative 參考） | 未檢出漂移 |

## Task 4 多輪判定與 usage VERDICT（已完成，commit `1c94a1a`）

- 多輪判定：**Plan A GO**——pinned claude 2.1.223 in-process 多輪成立（同 session_id、前後文保留、stdin 關閉後自然 exit 0）。Task 4b fallback 未觸發，`internal/claude/multiturn*.go` 不存在。
- usage VERDICT：**per-turn**（turn1 output=642、turn2 output=9 → o2 < o1/2）→ Manager `ClaudeUsageCumulative=false`（累加制，UI 標示 `session_total`、claude tokens 無 `*`）。
- 證據：`testdata/fixtures/claude-multiturn-shape.ndjson`（已消毒）、`cmd/probe-multiturn` 輸出（M1-T4 記錄）。

## 自動驗證（已執行，2026-08-10）

| Gate | 結果 | 證據 |
|---|---|---|
| `go vet ./...` | PASS | 無輸出 |
| `go test -race ./... -count=1` | PASS | appcore/approval/claude/codex/contract/proc/recorder/main 全 ok |
| V0.9 replay 迴歸 | PASS | `TestContractReplay`（claude）、`TestReplay`（codex）各 ok |
| `npm --prefix frontend run test` | PASS | 20/20（store 13 + scroll 3 + sanitizer 4） |
| `npm --prefix frontend run build` | PASS | vue-tsc + vite build 成功 |
| `wails build` | PASS | `build/bin/sdlc-workbench.app` 產出 |
| `scripts/bundle-clis.sh` | PASS | bundled: 873M |
| grep gate `ClaudeTurns` | PASS | internal/ cmd/ app.go 均 0 hit |
| workspace 安全 6 例 | PASS | `TestWorkspaceReadSecurity`（dot-dot／symlink 檔／symlink 目錄／>1MB／list escape／正常讀） |
| clean working tree | PASS（本檔 commit 後複驗） | |

單元測試涵蓋（依 plan 驗證策略總表）：
- appcore：RecordingLease barrier 恰一次／首錯保留／stop-err join；coordinator ownership barrier／stale-accept／approval 入列順序／reject；StartSession 原子交易；Submit×End lifecycle barrier（`TestSendThenEndBarrier`、`TestEndThenSendRejected`、`TestEndDuringStartingIsRejected`、`TestCancelEndSessionRestoresActive`、stale token）；EndSessionFlow busy／teardown-error／冪等；Pump quiesce／timeout；CloseSequence 三路徑（正常／逾時升級／卡死＋`Exited=false`）；Close×Emit barrier＋fail-loud ID 遞增；usage 語意雙分支＋semantics 欄位；M0 turn 追蹤遷入（turntrack）。
- codex：wire lifecycle、early-completed latch、空 turn id、barrier 單 wire request、resume、session-scoped 錄流雙輪。
- contract：ULID 單調、Wrap role precedence、raw fallback、reducer 全轉移。
- 前端：user-envelope 氣泡、tool-echo 路由、delta 累積、usage 覆寫、costDisplay、busy、submit 分流／失敗、雜訊分組、note／applyDone／resumeInput、sanitizer 4 例、isAtBottom 3 例。

## 待驗收（需 owner 操作 live app）

以下項目需啟動 `build/bin/sdlc-workbench.app`（或 `wails dev`）與已登入的雙 CLI：

- [ ] V0.1 claude approval allow（touch probe → 彈窗 → Allow → 檔案存在 + audit）
- [ ] V0.2 claude approval deny（檔案不存在、turn 正常收尾）
- [ ] V0.3 claude approval 逾時自動 deny（`WORKBENCH_APPROVAL_TIMEOUT=5s`）
- [ ] V0.4 claude resume（跨 app 重啟引用前文；異 cwd refused）
- [ ] V0.5 claude Terminate（5s 內收尾、exit 143）
- [ ] V0.6 Recorder 失敗可見（chmod 500 recordings → recorderError）
- [ ] V0.7 codex approval allow/deny（untrusted）＋ thread/resume
- [ ] V0.8 codex AuthStatus（logout/login 循環僅在 owner 事前明示同意時執行；未同意記「未執行（沿 M0 waiver）」）
- [ ] V1 多輪：claude 3 輪 + codex 3 輪（同 thread、server 不重啟；codex 三輪單一 `codex-m1-chat.jsonl`、meta 恰一份）
- [ ] V2 SC2 四問（StatusBar 截圖；claude tokens 無 `*`、codex 恆 `*`＋tooltip）
- [ ] V3 streaming／thinking 摺疊／follow-tail 上捲停跟隨／tool 卡片（claude `Bash(touch …)`、codex per-type＋status）／Timeline 摺疊 toggle
- [ ] V4 檔案樹＋預覽（`.md` 含 mermaid 區塊、`.mmd` 存檔 1 秒內重渲染、symlink 拒絕一例實測）
- [ ] V5 approvalPolicy（untrusted 彈核可；never 不彈——行為與風險如實記錄）
- [ ] V6 稽核 `events.jsonl`（event_id 單調、抽 3 筆對照 UI、user message 先於該輪首個 provider event、無 stream_error）
- [ ] 封裝 smoke（bundle 後雙 provider 各 1 輪）

## 偏差記錄

1. **MermaidPane.vue 刪除時點**：plan 排在 Task 10 commit；實際移至 Task 11 commit（App.vue 重寫同時），避免中間 commit 引用已刪檔案。內容無偏差。
2. **SettingsBar resume 輸入**：plan 樣張為 `v-model="s.resumeInput"`；Pinia options-store getter 唯讀，實作為元件內 writable computed 委派 store `resumeInput` getter ＋ `setResumeInput` action。行為等價（per-provider 記憶），有 vitest 覆蓋。
3. **FileTree 實作形狀**：plan 描述「懶載入樹（約 60 行）」未附完整碼；實作採 Vue 3 檔名自遞迴 SFC，每層展開時 mount 子層才呼叫 `ListWorkspace`（懶載入語意一致）。
4. **`ListWorkspace("..")` 拒絕**：`filepath.Clean("/"+rel)` 會把 `..` 中和成 root（不逸出但也不報錯），與 plan 測試「必須拒絕」不符；實作補 `..` 成分顯式檢查（拒絕、不無聲重導）。
5. **PreviewPane mermaid 錯誤顯示**：錯誤文字以 `textContent` 寫入（不進 HTML sink），較 plan 樣張更嚴格（XSS 防護）。

## 殘餘風險

1. V0–V6 live 驗收未執行（上表）；coordinator 的「app.go 忘記呼叫」殘餘情境依 plan 由 V1／V6 live 把關。
2. claude 自然結束路徑（pump 收乾 → EndSessionFlow）在單元層以 CloseSequence 測試覆蓋，但與 live CLI 的整合行為待 V1／V0.5 驗證。
3. 測試穩定性：appcore barrier 測試依 channel 同步（deterministic），無 sleep 競態；M0 已知 race-test 穩定性註記沿用。
4. bundle 873M 未瘦身（plan 明列 M1 不處理）。

## SC2 達成聲明

待 V2 截圖存證後補記。

## 錄流 digest

待 V1／V0 錄流產出後補 SHA-256 清單。
