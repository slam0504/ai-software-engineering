# M3a.1 — M3a 閉環完整度收尾設計

- 日期：2026-08-13
- 狀態：設計定稿 rev1（owner 設計審閱四項 P1 契約已整合：新檔建立規則／bump preview token／TCA generation 隔離／planner enforcement 接線點）
- 上游依據：`docs/superpowers/specs/2026-08-12-m3a-plan-test-contract-design.md`（rev4＋erratum）；`docs/spikes/m3a-results.md` 已知缺口 1–6 與最終 review triage
- 前置：M3a ✅ merged（`cfa5a20`）＋post-merge P1/P2/i18n 修正 ✅

---

## 1. 目標與定位

讓 M3a 交付的 Stage B→C 閉環達到：**純 UI 可完成**（SC4 方向）、操作陷阱有引導、已確認測試債清零、planner enforcement 真正接線。封閉範圍的修正型里程碑，**不含任何多 session 架構變更**（M3b）；不承諾時程，工程量由 implementation plan 展開後估算。

驗收主軸：實機從零走一輪 Stage B→C **全程不離開 UI、不用 binding 直呼**；A2 情境（HEAD 前移）靠 UI 引導完成 bump→重送核。

## 2. 範圍總表

| # | 項目 | 對應缺口／票 |
|---|---|---|
| A | 新檔建立 UI（含建立規則契約 §3.1） | 缺口 1 |
| B | analysis_base bump 引導（preview token 契約 §3.2） | 缺口 2 |
| C | STALE 重核引導（帶脈絡導航） | 缺口 3 |
| D＋E | TCA generation 隔離（§3.3，涵蓋 reactivity 與 role undefined） | 缺口 4、5 |
| F | Planner enforcement probe＋接線（§3.4） | live probe＋§3.8 (9) |
| G | 已確認測試債（§5） | 最終 review (b)＋缺口 6 |
| H | 四項防禦補強（§4） | 最終 review (b) |

**明確不含**：多 session（M3b）、forge/CI（M4）、`inflight.Wait` timeout（維持既有裁定）、escalation load() 序號化、TCA reconcile 效能（觀察項）。

## 3. 契約（實作前凍結）

### 3.1 新檔建立規則

形態：SpecWorkspace／PlanWorkspace 檔案清單頂部的 **inline 輸入列**（與既有清單輕量操作一致，非 modal）。

1. 建立一律走既有 `SpecWrite`／`PlanWrite(path, content, "")`——**成功後才重新載入清單並選取新檔**；失敗顯示後端錯誤原文、清單不動。
2. **同名檔案由後端 optimistic concurrency 拒絕**（expectedDigest="" 對已存在檔案必失敗），不得覆寫；前端不做「已存在就開啟」的靜默降級。
3. **PlanWorkspace 在已有主要 plan 文件時拒絕再建立另一份 `plan/<id>.yaml`**（前端擋＋訊息說明單一 plan 限制）——`worktreePlanDoc` 對多份可解析 plan fail loud 是既有權威行為，UI 不得讓使用者踩進 ambiguous 狀態。oracle-surface／risk-policy／permissions 不受此限。
4. **模板依路徑決定**（不靠副檔名猜）：`plan/<id>.yaml`（plan 骨架：plan_id 由檔名帶入、analysis_base_commit 留空提示、單一 task 範例）、`plan/risk-policy.yaml`（version＋default_tier 骨架）、`plan/oracle-surface.yaml`（version＋patterns 骨架）、`plan/permissions/<name>.yaml`（空權限清單骨架）；其他合法 `plan/**`／spec scope 路徑建立**空白檔案**。
5. 路徑先經前端 scope pattern 預驗（即時提示），後端 scope 驗證仍是唯一權威。

### 3.2 analysis_base bump（preview token）

新後端綁定，兩段式（防「已檢視新 HEAD、實際套用過期 HEAD」）：

1. `PreviewAnalysisBaseBump(planRel string, bufferDigest string)` 回傳 `{token, old, head, commits[], touchedFiles[]}`——token **至少綁定**：plan 路徑、舊 `analysis_base_commit`、Preview 當下 HEAD、當下 editor buffer digest。
2. UI 顯示：舊值、目前 HEAD、區間變更摘要（commits＋touched files）＋警語「**更新代表你已檢視這段 code 變更，並確認現有計畫仍適用**」；旁附「重新執行 PlannerAssist」建議動作（不強制）。
3. `ConfirmAnalysisBaseBump(token)`：重驗 token——HEAD、plan 路徑、buffer digest 任一改變即拒絕並要求重新預覽。成功後**只精準替換 buffer 內的 `analysis_base_commit` 值**（回傳新 buffer 或替換指令由前端套用）——**不寫檔、不 commit**。
4. 使用者仍須依序「儲存 → 預覽 commit → 建立 commit → 重新送核」——既有 commit token 與 Gate 2 驗證鏈不變，本契約只補 UI 誠實性。
5. 觸發時機：PlanWorkspace 偵測「目前 HEAD ≠ buffer 內 analysis_base_commit 且區間含非 plan/** 變更」（lineage 將擋）時顯示提示與入口。

### 3.3 TCA generation 隔離

以 **gate2 approval id 作為 view generation／run identity 的一部分**：

1. TcaWorkspace 監看 active gate2 的 `approval_id`（非只 planId）；換版時：重載 test_commit 候選與 task context，並**清除**——test commit 輸入與預檢結果、mutation ID、expected-red／negative-control 結果、送核結果與錯誤、尚未完成的舊 async response（generation guard 丟棄晚到回應）。
2. evidence store 的 run key 納入 `gate2_approval_id`（`(approval_id, plan_id, task_id, kind)`）；`RunEvidence` 的 workspace started/finished 事件 **additive 補 `gate2_approval_id` 欄位**——store 對非當前 generation 的晚到事件直接丟棄。舊 approval 的兩筆 PASS 不得在新版畫面顯示或啟用送核（後端 validator 本就會拒，本契約消 UI 假訊號）。
3. GateConsole tca 卡片的 evidence 控制項：**production binding 缺 role 或帶未知 role 時不得組出 `undefined`**——fail loud（顯示資料錯誤標示）或略過該控制項，並修 data-test 根因＋測試。

### 3.4 Planner enforcement probe＋接線

Live probe 文件本身不構成 runtime enforcement——凍結接線點：

1. **Probe＝exact pin 的實作 gate**：對 pin claude `2.1.223` 實機驗證 `--tools "Read,Glob,Grep"`——記錄實際 argv、CLI 版本、唯讀工具可用證據、寫入（Write/Edit/Bash）被拒證據；結果落 `docs/spikes/m3a1-planner-probe.md`（如實記錄，含失敗）。
2. **PlanAssist 啟動前 provider capability preflight**：無法證明 enforcement 時——不啟動 runner、建立 `planner-enforcement:<provider>` **hard** escalation、回傳明確錯誤。preflight 的證明方式依 probe 結果於 implementation plan 定形（原則：argv／wire 級驗證，不依賴 prompt）。
3. Codex 收到 approval／escalation request 時回 **typed enforcement violation**（取代現行字串比對），接到相同 condition key 升級項目。
4. **誤分類禁止**：一般 process 啟動失敗、模型錯誤、逾時不得計入 enforcement failure（各走原本錯誤路徑）。
5. 權威條件恢復（preflight 重新通過）後由系統 `resolveByKey` 解除；使用者不可手動解除（hard）。

## 4. 四項防禦補強

1. **evidence ID 路徑安全**：`NewWorktree`／registry 邊界拒絕含路徑分隔字元、`..`、超長（>64）或非 `[A-Za-z0-9_-]` 的 evidenceID（呼叫端 ULID 天然合規；此為邊界防禦）。
2. **runner 輸出分隔**：stdout 與 stderr 合併給 matcher 前插入明確分隔 byte（`\x00`），消除跨 stream 拼出假 matcher 命中。
3. **gate decision 白名單**：`Service` Decide/PrepareDecision 入口只接受 `approved`｜`rejected`，其他值明確拒絕（消「殭屍 pending」路徑）。
4. **risk classification typed error**：`internal/plan` 對 risk 分類失敗提供 exported typed error（如 `ErrRiskUnclassifiable` 或 error type），`app.go` 的 `hasRiskClassificationError` 改 `errors.Is/As`，移除錯誤文字相依。

## 5. 已確認測試債（全數清零）

gate2 三條拒絕路徑（minimum 重算不符／planner<minimum／未知 tier）；`gate.Service.Lookup` 直接測試；gate_op 原子性直接斷言（record＋superseded transitions 同一 op）；PlanAssist「plan 路徑 dirty 允許」正向測試；permissions_ref 缺檔即拒測試；EvidenceRunDigest 竄改測試補非 string 欄位（ExitCode）；PutCAS 併發回歸測試常駐（-race）；**orphan worktree 慢腳本 E2E**（`sleep 5` 假測試腳本＋精準 kill 命中 worktree 存活窗，實機重現「真正孤兒被 CleanupOrphans 清除」——補齊 A10 的間接驗證）。

## 6. 驗收

1. **實機純 UI 走查**：全新 workspace 從零 Stage B→C 全程（建檔含四種模板→Gate 1→PlannerAssist→plan→Gate 2→test→mutation→evidence→TCA），全程不用 binding 直呼；截圖存證。
2. **A2 引導情境**：HEAD 前移後靠 UI 完成 bump（含 token 過期情境：預覽後再 commit 一筆→確認被拒→重新預覽）→重送核成功。
3. **generation 隔離情境**：gate2 換版後舊 evidence 結果清空、晚到事件丟棄、送核按鈕不誤啟用。
4. **enforcement 情境**：probe 記錄完成；模擬 preflight 失敗→PlanAssist 拒＋hard 收件匣項→恢復後系統解除。
5. 收尾 gate：`go vet`／全 repo `go test -race -count=1`／vitest／frontend build／`wails build`＋新測試全綠。

## 7. 風險與待驗證假設

1. Claude pin CLI 的工具白名單旗標實際行為（probe 才能確認）——若 `--tools` 白名單語意與假設不符，preflight 證明方式需在 implementation plan 調整，fail closed 原則不變。
2. `ConfirmAnalysisBaseBump` 的 buffer 替換以「精準替換單一欄位值」為契約——YAML 註解與排版不得被重排（實作以字串定位而非重新序列化）。
3. evidence store run key 擴充涉及 Task 22 既有測試 baseline 更新——additive 欄位不破壞既有 workspace 事件消費者。
