# Codex「accept 後仍 EPERM」相容性調查（rev4）

- 日期：2026-08-21（rev1→rev2→rev3→rev4 同日連續修正）
- 票源：m3b-results.md §11 開票記錄第 1 項（P1，owner 裁決）
- **裁決：B1**（見 §4）——owner 2026-08-21。
- 工具：`cmd/probe-codex-approval`——每組隔離 `CODEX_HOME`（只帶 auth、即棄）、
  每組唯一 marker、規則檔前後 digest、**完整 thread/start 與 turn/start 參數**、
  **逐事件 approval（approval_events[]＋approvals_turn1/turn2）**、rollout `turn_context`
  權威值、launcher/native binary sha256（直指 native 時 native=launcher）、fail-loud 退出碼
- 對照版本：pinned **0.146.1**（launcher `134063e1…`）vs **0.149.0**（native binary
  sha 各記於證據）
- 原始證據（去敏：`$HOME`／`$TMP` 化＋ID 一致性假名化）：
  [`evidence/codex-approval-eperm/`](evidence/codex-approval-eperm/)，40 檔（含 G1／G5
  各版 3 次重跑、G4 0146 3 次、0149 兩態、G1 的 approval-0 觀察樣本）
- **probe 本身不修改 production code 與安全模型；B1 由本 commit 另行實作**（見 §4a）

## 0. 修正史（Fail Loud）

- **rev1 撤回**：共用 `~/.codex`＋同名指令，G2 的 execpolicy 規則污染後續全部組別，
  「0.149.0 升版即修復」是污染假象。注入使用者環境的規則已移除（僅刪該行、留備份）。
- **rev2 兩處錯誤（本 rev 修正）**：
  1. committed 的 0.146.1 G7 送的是**無效編碼** `workspace-write`（被 schema 拒），
     卻在矩陣寫成成功——B1 機制的關鍵證據是錯的。rev3 用正確的
     `{"type":"workspaceWrite"}` 重跑，G7 兩版皆成立。
  2. 只存 thread_start_params、不存 turn_start_params，所以錯誤的 G7 參數當下沒被看出。
- **rev2 過度斷言「兩版全矩陣同構」**：實際有版本差異（見 §1 註記），本 rev 縮小措辭。
- **rev3 兩處被 reviewer 抓到、本 rev（rev4）修正**：
  1. **approval 出現次數有執行間非確定性**——G1／G5 的 committed 證據與矩陣曾互相矛盾
     （0146-g1 記 0、矩陣寫 1）。rev4 各版重跑並全部收入證據：G1 兩版各 3 次皆 1
     （`0146/0149-g1-run1..3`），另存早期 approval-0 觀察樣本（`0146-g1-approval0-observed`，
     events=0）；G5 兩版各 3 次皆 1（`0146/0149-g5-run1..3`）。**approval 出現與否不是
     穩定判準**，穩定的是「檔案寫入＋sandbox 結局」。
  2. **G5 撤回為版本差異**：rev3 憑一次 0 觀察稱「0.149.0 無 approval」；rev4 兩版各
     3 次都是 approval 1＋DNS 阻——G5 無版本差異。**唯一的版本差異是 G4**（見 §1）。

## 1. 結果矩陣（rev4，隔離環境；判準是「檔案寫入＋sandbox 結局」——穩定；approval 出現次數有執行間非確定性，另列）

| 組 | sandbox 設定 | 0.146.1 寫入 | 0.149.0 寫入 | turn_context |
|---|---|---|---|---|
| G1 | 預設，回 `accept` | ❌ 未建立 | ❌ 未建立 | `read-only` |
| G2 | 預設，回 amendment | ❌ 未建立，規則持久化 | ❌ 同 | `read-only` |
| G3 | **thread/start** `sandboxPolicy`（四編碼：str/tag × camel/kebab；兩版各四份） | ❌ **全被靜默忽略** | ❌ 同 | `read-only` |
| G6 | config.toml `sandbox_mode="workspace-write"` | ✅ 建立 | ✅ 建立 | `workspace-write` |
| G7 | **turn/start** `{"type":"workspaceWrite"}` | ✅ 建立 | ✅ 建立 | `workspace-write` |
| G8 | G2 後同 home 同指令第二輪 | ✅ 第二輪建立 | ✅ 同 | `read-only`（兩輪皆） |
| G4 | workspace-write ＋ workspace 外寫入 | ❌ 未建立（穩定） | **⚠ 執行間差異** | `workspace-write` |
| G5 | workspace-write ＋ 網路（curl） | ❌ DNS 阻 | ❌ DNS 阻 | `workspace-write` |

**穩定核心結論（不受 approval 非確定性影響）**：read-only 下寫入必敗（G1/G3）；
workspace-write（turn/start tag 或 config.toml）下 workspace 內寫入成功（G6/G7/G9）；
thread/start sandboxPolicy 兩版四編碼全被忽略（G3）。

**版本差異只有一處**：
- **G4**（workspace 外寫入）：0.146.1 三次皆直接拒絕、不上報 approval（`0146-g4-run1..3`，
  events=0、未建立，穩定）；**0.149.0 執行間非確定**——`0149-g4-refused`（拒絕、未建立）
  與 `0149-g4-approved`（上報 approval、accept 後 /tmp 成功建立）各一份。即 0.149.0
  的 workspace 外寫入授權在此版本已放寬但非確定性觸發。

**approval 出現次數（非穩定判準，執行間差異，另記）**：
- G1／G5 各版 3 次重跑（`*-g1-run1..3`／`*-g5-run1..3`）皆 approval 1；G1 另有一份
  早期 approval-0 觀察（`0146-g1-approval0-observed`）——故 approval 出現非確定。
  `accept` 對這些 read-only／網路情境不改變最終寫入／連線結果。
- **G8 turn2**：`approvals_turn2 == 0`（逐事件計數，穩定）——amendment 落規則後第二輪
  同指令**跳過核可**且於 `read-only` turn_context 下寫入成功（sandbox 外執行）。
- G4 的版本差異（上一段）伴隨 approval：0.146.1 不上報、0.149.0 可能上報。

## 2. 歸屬結論

1. **與 workbench 無關**：probe 不經 workbench，G1 仍重現 EPERM。
2. **root cause**：workbench 未設 sandbox → codex 預設 `read-only`；approval 的
   `accept` 只允許執行、不放寬 sandbox——read-only 下寫入型指令的核可必然徒勞。
3. **有效控制面只有兩個**：`turn/start.sandboxPolicy`（tagged enum `{"type":…}`，
   turn 級、有 schema 驗證，錯誤訊息列出 `dangerFullAccess|readOnly|externalSandbox|
   workspaceWrite`）與 `config.toml.sandbox_mode`（home 級）。
   `thread/start.sandboxPolicy` 在**兩版、四種編碼**下都被靜默忽略（client 陷阱）。
4. **禁止項（G8）**：`acceptWithExecpolicyAmendment` 持久化的 allow 規則會讓後續同
   prefix 指令跳過核可且於 sandbox 外執行——任何修法不得引入。

## 3. 邊界行為（workspace-write 生效時，pinned 0.146.1）

- workspace 內寫入：**仍出 approval**（untrusted），accept 後真的能寫（G6/G7）。
- workspace 外寫入：直接拒絕、不上報（G4，fail-closed）。
- 網路：`network_access:false`，DNS 全阻（G5）。

## 4. 裁決：B1（owner 2026-08-21）

**在正常 turn/start 明確送 `{"sandboxPolicy":{"type":"workspaceWrite"}}`。**

理由：turn 級控制（範圍小於 B2 的全域 CODEX_HOME）；untrusted 下仍保留逐指令
approval（G6/G7 實測 workspace 內寫入仍被詢問）；accept 後 workspace 內寫入真能執行；
workspace 外與網路仍受 sandbox 邊界（0.146.1；0.149.0 的 workspace 外經 approval 可寫，
見 §1 註記——升版前需重評）。不選 C（保留無效且誤導的寫入核可）、不選 B2（引入 auth／
rules／session home 的生命週期與遷移負擔）。

**實作約束（owner 指定）**：
- 首輪與 SendMessage 後續**每一輪**都要帶入 `sandboxPolicy`。
- **不得引入** `acceptWithExecpolicyAmendment`（§1 G8）。
- SpecAssist／preflight 既有的 `readOnly + never` 路徑**不得改動**。
- 補測三種 approval policy（untrusted／on-request／never）× 兩種寫入路徑
  （command execution／file change）。

## 4a. B1 實作與 approval-policy × 寫入路徑矩陣（owner 指定驗收）

**實作**：`internal/codex` `ThreadRunner.SetTurnSandbox("workspaceWrite")`，每一輪
`StartTurn` 帶 `sandboxPolicy={"type":"workspaceWrite"}`；由 `app.go` 的
`startCodexHost` 對每個 codex session 設定。守門：
`internal/codex` `TestStartTurnCarriesSandboxPolicyEveryTurn`（兩輪逐一驗，含
未設不帶的反向）＋`TestCodexTurnStartCarriesWorkspaceWriteSandbox`（app 層：
StartSession 首輪與後續輪皆帶）。assist 的 oneshot 走自己的 readOnly params、
不經 ThreadRunner，未受影響（`internal/assist` 測試全綠佐證）。

**矩陣**（workspace-write 生效、pinned 0.146.1；證據 `evidence/…/matrix-*.json`）：

| approval policy | command-execution（touch） | file-change（apply_patch） |
|---|---|---|
| **untrusted**（workbench 預設） | approval 1 次 → accept → ✅ 寫入 | approval 1 次 → accept → ✅ 寫入 |
| on-request | 無 approval → ✅ 寫入 | 無 approval → ✅ 寫入 |
| never | 無 approval → ✅ 寫入 | 無 approval → ✅ 寫入 |

結論：**只有 untrusted 對每個寫入動作出 approval**（兩種寫入路徑皆然）——正是
workbench 用的 policy，逐指令核可在 B1 下保留且 accept 後真的生效。on-request／
never 下 workspace 內寫入不再逐一詢問（policy 語意，非 B1 引入）。

## 5. 重現方式

```
go build -o /tmp/probe ./cmd/probe-codex-approval
/tmp/probe -codex-bin tools/codex-cli/node_modules/.bin/codex -out /tmp/o -groups 1,2,6,8
/tmp/probe -codex-bin <bin> -out /tmp/o2 -groups 7,4,5 -sandbox '{"type":"workspaceWrite"}'
/tmp/probe -codex-bin <bin> -out /tmp/o3 -groups 3 -sandbox '{"type":"workspaceWrite"}'  # thread 層被忽略
```
