# B3a-2a Gate 1／Gate 2／STALE runtime 驗收與交付紀錄

> 建立：2026-09-25（台北時間）。範圍：B3a-2a 的 Gate 1／Gate 2／STALE browser 驗證，不經 provider；包含首跑失敗、診斷、修正與限定驗收。
> codex-reviewer 於 mailroom #459 裁定本票在約定範圍內技術驗收完成。Source PR [#23](https://github.com/slam0504/ai-software-engineering/pull/23) 的四項 PR CI 成功，已於 2026-09-25T09:06:29Z 合併為 `41257e0d86a0fb6dddef9d38ec3809f305f97d02`；本文件記錄的 browser 證據與一般 CI 分開判讀。

## 0. 為什麼要留這份紀錄

B3a-2a 的 runtime 驗收不是一次到位：中途歷經一次啟動前環境失敗、一次 Gate2 功能性首跑失敗、一次僅供診斷用的隔離重現、等待上限、證據判定與 Vite dev watch 等修正、多輪 source 複核，最後才在同一份修正後的 source 上完整跑過 Gate2 與 STALE。**Gate1 沒有跟著 Gate2／STALE 一起在最新 source 快照上重跑**——這是 reviewer 依變更影響範圍做的裁定（見 §4），不是疏漏，本文件把這個裁定與其依據寫清楚，避免日後誤讀成「三條流程都在同一快照跑過」。

## 1. 受測 snapshot

正式驗收 run 使用下列 worktree／branch，受測 base 不變；source 隨測試修正而演進。診斷 run 另用隔離的 `b3a-2a-diag` worktree，不在此工作樹內執行。

| 項目 | 值 |
|---|---|
| worktree | `/Users/eason_tseng/playground/project/ai-se-worktrees/b3a-2a-gates` |
| branch | `b3a-2a/gates-e2e` |
| base commit（全程未變） | `be099dfa1f35c0a5ccbe36b35123bb4c3f41645c`（未提交狀態下的 working tree 修改） |

Source 隨修正演進，本文件引用兩個經 reviewer 核定的 manifest：

| 代號 | 檔案數 | manifest 檔案 | manifest SHA256（自身） | 涵蓋範圍 | 對應 flow |
|---|---|---|---|---|---|
| r4 | 28 | `SOURCE-SHA256SUMS-r4-reviewer`（`/tmp/codex-review434-6ve78ch5/`） | `3e65b5f854e2312225bd9316371961d322f02739fc25876d38d618ed6edb1340` | 修正 gate1／journal 相關檔案後的快照，**尚未含** Gate2 等待邏輯修正與 Vite patch | Gate1（唯一一次 run 用此快照） |
| r5 | 31 | `SOURCE-SHA256SUMS-r5-reviewer`（`/tmp/codex-review456-nleo6i8c/`） | `d90afedb5022805b0d9d4cac9bb37e41075e8e5de8602666ea1a3474ec0c19a6` | r4 加上 `gateSubmitWait.ts`／selftest、Gate2／STALE 等待與 body 上限、package scripts／md5，以及 Vite artifacts 排除；`gate2Fixture.ts`／selftest 在 r4→r5 間未變 | Gate2、STALE（正式執行皆用此快照） |

兩份 manifest 皆由 reviewer 核對過 `shasum -c` 全部 OK，且 `git status` 的本次變更路徑（含尚未受版控的新檔）與 manifest 路徑集合一致（r5：31/31，見 §3 preflight 欄位）。

## 2. 啟動前失敗（未進入 test 流程）

| 項目 | 值 |
|---|---|
| 時間 | 2026-09-25T06:52:00Z |
| 指令 | `env -u E2E_SCENARIO -u E2E_OFFLINE_SANDBOX E2E_KEEP_ARTIFACTS=1 E2E_GATE=gate1 caffeinate -i npm run test:e2e:gates` |
| rc | **127** |
| 輸出 | `npm: No such file or directory` |
| 原因 | 當次 shell 的 `PATH` 未含 nvm 管理的 npm 路徑；後續各次啟動改用可解析到 `npm 11.19.0`（node v26.8.1）的環境，未再重現 |

這是純環境問題，**與 harness 或受測程式碼無關**，未進入任何 Playwright test、未產生 run-id artifacts；啟動命令與錯誤 log 仍有保存。紀錄於 `/tmp/b3a2a-browser/gate1.rc`、`gate1-run.log`、`gate1-cmd.txt`、`preflight-gate1.log`。

## 3. Run／manifest 矩陣

以下列出 wails dev＋browser 的實際執行；Gate2 首跑失敗與 diagnostic-only run 不計入驗收。

| Flow | Run-id | Source 快照 | rc | Artifacts manifest（自身 SHA256，22/22 或 26/26） | 收尾結果 | tripwire（claude／codex／違規） | network 違規 | vite reload | 備註 |
|---|---|---|---|---|---|---|---|---|---|
| Gate1（唯一一次） | `20260925T065454Z-924661` | r4（28 檔） | 0 | `df4f59942eefd90438f2703dea3a97a6166b0c598efb4e475f8c9132ef530575`（22/22） | `clean=true portsReleased=true`，TERM 2342ms／KILL 0ms | 3／3／0 | 0（0 bytes 違規檔；`networkViolations=0`） | **2**（patch 前，見下方說明） | PASSED，測試本身 54.8s、總 run 7.6m |
| Gate2（首跑，**歷史失敗**） | `20260925T071019Z-146267` | r4（28 檔，Gate2 等待邏輯修正前） | 91（inner test rc=1） | `df798c0d1982cb695853ca3598730f5e37f071e57eee8dfea6ac4377ccfd73e3`（26/26） | `clean=false`，`abandonedPids=95805`（身分不符不送信號）、當時保留 `.active-run.json`、`TEST_FAILED` marker | 4／4／0 | 0 | **2**（patch 前，見下方說明） | **FAILED**：`gate2.spec.ts:226` 15 秒內未觀察到 gate2 `gate_request`；trace 顯示 P1 呼叫已送出但無回覆；不計入驗收（見 §5） |
| 診斷 run（**僅供診斷，不計入驗收**） | `20260925T075938Z-b6d61a` | 隔離診斷副本（非 r4／r5、非本 worktree） | 0（PASSED） | `23b1e49b0e19c68c95efb31535b362224a3dff2965a252887313c788bbc1f2e8`（22/22；diag-out 4/4） | clean | 未列為驗收指標 | 未列為驗收指標 | **2**（診斷副本，未套 Vite patch） | P1 native 耗時 13.886791775s，其中 git wrapper 呼叫 29 筆合計 13.778574172s（占比 99.2207%，平均 475ms／筆）；原始首跑超過 15 秒的原因未完全定位（見 §5） |
| Gate2（正式重跑） | `20260925T083946Z-b4abcb` | r5（31 檔，含等待邏輯修正＋Vite patch） | 0 | `4bb361d7fc6c3792a5b87e9f3e0677d624f9a3c9f23180df22d571cc99053f2b`（22/22） | `clean=true portsReleased=true`，TERM 1291ms／KILL 0ms | 4／4／0 | 0 | **0** | PASSED，測試本身 1.5m、總 run 3.5m |
| STALE（正式首跑） | `20260925T084336Z-1d7c3f` | r5（同上） | 0 | `489a5083f22b4b2bd185ea2b746e29ab75e158a2b7a6d06a880d9e4a411b5235`（22/22） | `clean=true portsReleased=true`，TERM 1292ms／KILL 0ms | 6／6／0 | 0 | **0** | PASSED，測試本身 1.1m、總 run 2.5m |

正式 run 與 Gate2 首跑的 manifest 自身 hash 及全檔校驗均經 reviewer 核對；診斷另核對 artifacts 22/22 與 diag-out 4/4。矩陣中的 hash 與各 driver 紀錄一致。

**vite reload 計數方式**：對每個 run 保留下來的 `frontend/e2e/.artifacts/<run-id>/wails-dev.log` 執行 `grep -c 'page reload' wails-dev.log`，本次覆核重新算過一次：

```
Gate1        20260925T065454Z-924661  → 2
Gate2 首跑    20260925T071019Z-146267  → 2
診斷         20260925T075938Z-b6d61a  → 2
Gate2 正式    20260925T083946Z-b4abcb  → 0
STALE 正式    20260925T084336Z-1d7c3f  → 0
```

Gate1 與 Gate2 首跑的兩筆 reload，內容都是 `[vite] (client) page reload e2e/.artifacts/<run-id>/playwright/.playwright-artifacts-0/traces/resources/<hash>.html`——即 harness 自己把 trace 資源檔寫進 `e2e/.artifacts/**`，觸發 Vite dev watch 誤判成原始碼變更。這兩次都發生在 §6 的 Vite patch（`server.watch.ignored` 排除 `e2e/.artifacts/**`）**套用之前**；**依 `review458 decision.md`，Gate1 快照差異裁定本就是依變更影響比對接受、不要求在 r5 重跑**，這兩筆 reload 不改變、也不影響該裁定。正式 Gate2 重跑與 STALE 首跑都在 patch 之後，reload 皆為 0。

**source 一致性**：正式 Gate2／STALE 執行前後，31 個 r5 manifest 路徑的校驗皆 rc=0，本次變更路徑集合相符；受測 worktree HEAD 均為 `be099dfa1f35c0a5ccbe36b35123bb4c3f41645c`。

## 4. Gate1 快照差異的裁定

**Gate1 沒有在 r5（31 檔）快照上重跑**——唯一一次 Gate1 run 用的是 r4（28 檔）快照。這不是遺漏，而是 codex-reviewer 在 `review458 decision.md` 中依變更影響範圍做出的明確裁定：

> 「Gate1 `20260925T065454Z-924661` 是 r4-reviewer 樣本，沒有在 r5 重跑。比對其 source-only 快照與目前工作區，Gate1 spec、App、SpecWorkspace/GateConsole、共用 setup/teardown/env/executionMode/artifactIntegrity 及 gate evidence/journal helpers 均逐位元組相同。r5 新增的 wait helper 只供 Gate2/STALE 使用；package 差異是測試 script/metadata。」

reviewer 進一步核對 Gate1 唯一相關的執行環境差異（Vite 排除 harness artifacts 的 matcher）已有專項檢查，且兩條正式 run（Gate2／STALE）本身就是在該設定下成功啟動且沒有 artifacts 觸發的 reload，因此**依變更影響比對，不要求再跑 Gate1**。

**這是本次（2026-09-25）明確做出的驗收裁定，不是歷史上早已存在的規則，也不能被寫成或理解為「三條流程都在同一個 r5 快照重跑過」**——Gate1 的正式證據仍然只來自 r4 快照那一次 run。

## 5. 歷史失敗與僅供診斷的界線

### 5.1 啟動前失敗（rc=127）

見 §2。純環境問題（PATH 未解析到 npm），未進入任何測試邏輯，不影響後續任何裁定。

### 5.2 Gate2 首跑失敗（`20260925T071019Z-146267`）

- 在 `gate2.spec.ts:226`，`.poll(() => latestGateRequestApprovalId(gatePath, 'gate2'), { timeout: 15_000 })` 逾時——15 秒內沒有從 journal 觀察到 gate2 的 `gate_request` 紀錄。
- 直接解析當次 trace.zip 的 WebSocketRoute 記錄：G2-P1（`main.App.SubmitPlanForApproval`）已送出，但 trace 範圍內沒有匹配回覆；N2/N3/N4/N5 的送出至回覆分別耗時 5301.941／8069.510／6436.269／7087.503 ms（見 `/tmp/codex-review442-v5qir4wz/gate2-wire-calls.json`）。
- 收尾不完整：`abandonedPids=95805`——該 PID 記錄的 command 是 `node .../npm run dev`，當時 live 的 command 是 `npm run dev`，兩者不逐字相符，harness 依既有「身分不符不送信號」規則未直接對該 PID 送信號，並將它列為 `abandonedPids`。當時保留 `.active-run.json` 與 `TEST_FAILED`。reviewer 後來確認 45 個追蹤 PID 全部不存在，先備份指標，再由既有 `handleStaleRun` 以 `stale-pointer-only` 清除過期指標；原 failed run-state、TEST_FAILED 與 artifacts 均未改寫（mailroom #443）。
- reviewer 當場親跑既有 Go 單元測試 `TestSubmitPlanForApprovalSucceeds`（無 N1..N5＋三 task fixture 序列）PASS，**不能排除本次問題**——即該次快速通過不構成反證。
- **這次失敗本身沒有被追出單一根因**；15 秒內沒有 request 不能直接推論成永久死鎖，因為缺少當時的 goroutine stack 或 Go 內部階段時間。

### 5.3 診斷 run（`20260925T075938Z-b6d61a`，僅供診斷）

- 在**獨立隔離診斷副本**（非受審 worktree、非 r4／r5 manifest）加入 timing instrumentation 後跑一次 N1..N5→三 task P1 序列。
- 診斷只觀察到 journal request，沒有驗證 WebSocket callback、risk 選擇、approve 或完整 bindings；它的 `1 passed` 只代表診斷流程結束。
- P1（Go 端）耗時 13.886791775 秒，**低於 15 秒門檻**，未重現原始 >15 秒逾時；同批呼叫中 29 次 git wrapper 呼叫合計 13.778574172 秒，占 P1 總耗時 99.2207%，平均每次 475.123ms（區間 396–603ms）。wrapper 計時含 instrumentation、spawn／執行／讀取／回收，不能當成純 spawn 耗時。
- 與先前單元環境的 wrapper 耗時（約 30–40ms／次）相比高出一個量級；watchdog（15s／60s goroutine dump）全程未觸發，因為 P1 在門檻前就已返回。
- **明確保留的界線**：這次結果只證明「本次診斷 run 的 P1 剛好在 13.9 秒完成」，**不代表原始 gate2 逾時問題已解決，也不構成 gate2 驗收證據**——診斷 run 使用不同的 source 副本、不同的 manifest，且未拆解 spawn／exec／read／reap 各階段，因此「為什麼原始失敗 run 會超過 15 秒、而本次只到 13.9 秒」仍未有直接證據解釋，只能假設是同一種「環境相依、隨機器負載波動」機制。

## 6. 修正與 source 複核

依 mailroom #445／#451／#457 的裁定（對應 review444／review448／review456 紀錄），做了以下修正（皆落在 r5 manifest 內）：

1. **等待上限調整（測試側，非 production）**：Gate2 送核等待上限自 15 秒改為 60 秒（15 秒仍保留為觀測欄位、不重送請求，60 秒仍無 request 才判失敗）；Gate2／STALE test body 上限設為 240 秒；STALE 的 S-N1 匹配 probe 等待設為 15 秒（原本非只有 200ms debounce，Gate1/Gate2 的 durable reconcile 會做多次 git 查詢）。
2. **Vite dev watch 排除 harness 產物目錄**：`frontend/vite.config.ts` 的 `server.watch.ignored` 加入 `**/e2e/.artifacts/**`，消除已觀察到的「harness 寫入 trace/log 觸發非預期 page reload」現象；**其他一般 Vue/TS 原始碼仍受監看**，不動 Wails 自身 watcher。正式 Gate2／STALE 兩次 run 的 `vite page reload lines`（observation only）皆為 0。
3. **r5 source 複核**（mailroom #457）：三個新反例（慢 poll 超過 60 秒、sleep 恢復時已過 deadline、16 秒才觀察到卻標成 15 秒已觀察到）先在修正前的 worker r5 版本失敗、修正後 10/10 通過；`typecheck:e2e` rc=0；新 source-only 副本的 `selftest:all` 首跑一次 rc=0：**32 suites／702 checks／0 failures**，未重試。

一項**未被核准落地**的候選：共用 harness 程序身分觀測設計（pending 二次確認、append-only 觀測序列等，v1/v2 見 `/tmp/b3a2a-bc/harness-identity-design.md`、`harness-identity-design-v2.md`）——mailroom #445 裁定 C 明確**不核准落地**，理由包含既有兩次採樣提案存在邏輯矛盾、`descendantsOf` 消失不等於死亡、pending 不可授予較弱 signal 權限等。此設計**維持獨立候選，未核點、未施工、未改 `stopProcedure.ts`／`runState.ts`／`processTree.ts`／`psUtil.ts`**。

## 7. 交付狀態

| 項目 | 值 |
|---|---|
| commit | `e66045d34f59373e023821079156b981277aa7ce` |
| tree | `09d70242011fe161af8c6fe227339402450e0b5b` |
| parent | `be099dfa1f35c0a5ccbe36b35123bb4c3f41645c` |
| 分支 | `b3a-2a/gates-e2e` |
| PR | [#23](https://github.com/slam0504/ai-software-engineering/pull/23)，開往 `main`，31 個檔（全部 ADDED／MODIFIED 於 `frontend/e2e/gates/`、`frontend/e2e/support/gates/`、`frontend/e2e/global-setup.gates.ts`、`frontend/e2e/playwright.config.ts`／`playwright.gates.config.ts`、`frontend/package.json`／`package.json.md5`、`frontend/vite.config.ts`） |
| PR CI | [run 36115456033](https://github.com/slam0504/ai-software-engineering/actions/runs/36115456033)，attempt=1，event=pull_request，head=`e66045d34f59373e023821079156b981277aa7ce`；checksums／frontend／go／wails-build 全部 success，未重跑 |
| merge | rebase merge，`41257e0d86a0fb6dddef9d38ec3809f305f97d02`，2026-09-25T09:06:29Z；tree 與受審 commit 相同，parent=`be099dfa1f35c0a5ccbe36b35123bb4c3f41645c` |

Source 已合併；本文件與 backlog 的 docs 交付另行處理。一般 CI 不執行 browser E2E，PR CI 成功不取代 §3 的本機 runtime 證據。

## 8. 保留限制

以下全部照 `review458 decision.md` 原文精神列出，本文件不放寬也不加強措辭：

1. 各條 runtime 為**單次樣本**，不宣稱長期穩定、最壞時間或效能 SLO。
2. 13.3 秒／11.5 秒是**兩次不同正式 run 的送核等待耗時**，不是 S-N1 相關耗時：13.3 秒（精確 13317.537ms）是**正式 Gate2 run**（`20260925T083946Z-b4abcb`）`gate-evidence.json` 裡 `G2-P1-submit-wait.observedDurationMs`——即 G2-P1 送核後觀察到 gate2 request 的耗時；11.5 秒（精確 11528.761ms）是**正式 STALE run**（`20260925T084336Z-1d7c3f`）同一份 evidence 裡 `Gate2-precondition-submit-wait.observedDurationMs`——即 STALE 流程裡 Gate2 前置送核的耗時。兩者都是從 click 之後的 pollStart 開始、**journal 觀察到的耗時，不是 callback 延遲**。
3. **正式 STALE run**（`20260925T084336Z-1d7c3f`，非診斷 run）`gate-evidence.json` 裡 `S-N1-probe-events.probeWaitElapsedMs=3414`（3414ms）**高於舊名目 3 秒**，只代表「舊估計偏緊、留有餘裕」；**沒有實跑舊 3 秒版本**、且舊 probe helper 可能在 deadline 後才做最後讀取，**不能斷言同一樣本在舊版必定失敗**。**診斷 run 只跑到 gate2 的 P1，沒有跑 S-N1**，3414ms 與診斷 run 無關。
4. **Gate2 原本逾時的原因未被完整因果定位**；本輪兩次正式 run（Gate2／STALE）沒有出現 identity mismatch，**不代表共用 harness 的身分觀測問題已經修好**——身分設計仍是獨立未核准的候選（見 §6）。
5. `CLIInfo` 只斷言 `startupError`，**原始回傳值未保存**；成功的 run **沒有保留 trace／screenshots**；raw journal／steps 與測試斷言的保存界線分開。
6. 網路證據是**取樣加 browser network guard**，不是封包層全程證明。
7. **S-N3 只由既有 Go policy 測試覆蓋**，沒有 browser 讀取故障或 Service 整輪回退的驗證。
8. **一般 CI 不跑 browser E2E**，checksums／frontend／go／wails-build 四項 CI job 通過不能取代本機 browser 證據。
9. **正式 CI 整合、cold-start、真實 provider、共用身分設計**皆為另案，不因本次驗收自動納入 B3a-2a 範圍——正式 CI 整合屬 B3a-CI 票，不由本票承擔。
10. active effort（實際工時）**未量測**，不以牆鐘回填、不回填任何未核定點數。

## 9. 與 B3a aggregate 的關係

B3a-2a 完成的是「Gate 1、Gate 2、STALE 三條流程，不經 provider」這個限定範圍（見 backlog 估點表 B3a-2a 列原文）。各項狀態分開記錄，B3a aggregate 尚未完成：

- B3a-2b（session recovery、approval，以 replay/fake provider 驅動）另票列管：限定技術驗收已完成，結案 docs PR #22 已於 2026-09-24 合併（`be099dfa1f35c0a5ccbe36b35123bb4c3f41645c`）。
- B3a-CI（正式 CI 整合）——**未由本票承擔**，一般 PR CI 不含 browser E2E。

## 10. 證據保存界線與文件交付

- 驗收裁定與獨立重算記錄見 `/tmp/codex-review458-h6t6sjzn/decision.md`、`verification.json`；mailroom 的對外裁定訊息為 #459。PR #23 的 live head／CI／merge 由 reviewer 在 #460 複核期間重新查證。
- `/tmp` 與 worktree 路徑非跨機器可取得的證據；不宣稱原始 artifacts 已上傳到 GitHub。
- 原始診斷 binary 目前已不在保存的原路徑，僅有當時記錄的 SHA256 與 build-info，未另重建。
- 本文件與 backlog 以獨立 docs 分支／PR 交付，source PR #23 不含這兩份文件。未量測工時、未核定點數與先前歷史修訂均保留原意。
