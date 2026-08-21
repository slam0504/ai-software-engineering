# Codex「accept 後仍 EPERM」相容性調查（rev2）

- 日期：2026-08-21（rev2 同日——rev1 因 execpolicy 污染被 reviewer 打回全面重跑）
- 票源：m3b-results.md §11 開票記錄第 1 項（P1，owner 裁決）
- 工具：`cmd/probe-codex-approval` rev2——**每組隔離 `CODEX_HOME`**（只複製 auth.json、
  跑完即棄）、每組唯一 marker 檔名、規則檔前後 digest、完整 thread/start response、
  turn 結束後解析 rollout jsonl 的 `turn_context`（**該 turn 實際生效 sandbox 的權威證據**）、
  任一組錯誤／timeout 退出非零
- 對照版本：pinned **0.146.1** vs **0.149.0**（binary 版本與 sha256 記於各原始結果）
- 原始證據（去敏：`$HOME`／`$TMP` 化＋ID 一致性假名化）：
  [`evidence/codex-approval-eperm/`](evidence/codex-approval-eperm/)，命名 `<版本>-g<組>.json`
- **本調查未修改任何 production code 與安全模型**

## 0. rev1 的錯誤與更正（Fail Loud）

rev1 共用 `~/.codex` 且各組同名指令：G2 的 `acceptWithExecpolicyAmendment` 把
`prefix_rule(pattern=["touch","marker.txt"], decision="allow")` 持久化進使用者的
`~/.codex/rules/default.rules`，其後所有組（含 0.149.0 全部）都被該規則放行——
rev1「0.149.0 升版即修復」的結論**全屬污染假象，予以撤回**。使用者環境的注入規則
已於 rev2 開始前移除（備份 `/tmp/default.rules.bak-20260821`，僅刪該行）。

## 1. 結果矩陣（rev2，隔離環境）

**兩版本行為完全一致**（表中結果 0.146.1 與 0.149.0 相同）：

| 組 | 設定方式 | approval | 結果 | turn_context（rollout 權威值） |
|---|---|---|---|---|
| G1 | 預設（不指定 sandbox）＋回 `accept` | 有，accept | ❌ 檔案未建立 | `{"type":"read-only"}` |
| G2 | 同上＋回 `acceptWithExecpolicyAmendment` | 有 | ❌ 當輪檔案未建立；**規則已持久化**（隔離 home 內 digest 改變） | `read-only` |
| G3 | **thread/start** 帶 `sandboxPolicy`（字串／kebab／tagged 四種編碼皆試） | 視情況 | ❌ **全部被靜默忽略**，未建立 | `read-only` |
| G6 | 隔離 home 寫 `config.toml`：`sandbox_mode = "workspace-write"` | 有，accept | ✅ 檔案建立 | `{"type":"workspace-write",…}` |
| G7 | **turn/start** 帶 `sandboxPolicy: {"type":"workspaceWrite"}` | 有，accept | ✅ 檔案建立 | `workspace-write` |
| G8 | G2 之後**同 home 同指令第二輪** | 第二輪**無** | ✅ 第二輪檔案建立 | **仍是 `read-only`** |
| G4 | 有效 workspace-write（turn 層）＋ **workspace 外**寫入（/tmp） | **無**（直接拒絕，不上報 client） | ❌ 未建立（「核可政策拒絕了工作目錄外的寫入」） | `workspace-write` |
| G5 | 有效 workspace-write＋網路（curl） | 0.146.1 有（accept 後仍失敗）；0.149.0 無 | ❌ `Could not resolve host`（`network_access:false`） | `workspace-write` |

補充：turn/start 對 `sandboxPolicy` 有 schema 驗證，錯誤訊息列出合法變體
`dangerFullAccess | readOnly | externalSandbox | workspaceWrite`（internally tagged
`{"type":…}`）；kebab（`workspace-write`）在 protocol 層被拒。thread/start 對同欄位
**連無效值都不報錯**——靜默忽略。

## 2. 歸屬結論（取代 rev1）

1. **與版本無關**：0.146.1 與 0.149.0 全矩陣同構；「升版修復」不成立。
2. **root cause**：workbench 未在任何層級設定 sandbox → codex 預設 `read-only`；
   而 **approval 的 `accept` 只允許指令執行、不放寬 sandbox**——read-only 下寫入型
   指令的核可流程必然徒勞（EPERM）。這是 codex 的設計語意（官方文件將 sandbox
   技術邊界與 approval 授權分開描述），不是 bug，也不是 workbench 回覆格式問題。
3. **有效的 sandbox 控制面只有兩個**：`turn/start.sandboxPolicy`（tagged enum，
   turn 級、protocol 內）與 `$CODEX_HOME/config.toml` 的 `sandbox_mode`（home 級）。
   `thread/start.sandboxPolicy` 在兩個版本都被靜默忽略——對 client 是個陷阱。
4. **G8 的不對稱性（安全含意最重）**：`acceptWithExecpolicyAmendment` 持久化的
   allow 規則，會讓後續同 prefix 指令**跳過核可、且在 sandbox 之外執行**
   （turn_context 仍 read-only 但寫入成功）。interactive accept 被 sandbox 約束、
   persisted rule 不受——兩條路徑的權限語意不同。workbench 目前從不送 amendment，
   這條路徑不可達；**任何未來修法都不應引入 amendment 回覆**，除非把「持久化全域
   規則＋sandbox 逃逸」明確納入安全模型。

## 3. 邊界行為（workspace-write 生效時）

- **workspace 外寫入**：不會產生 approval 請求，直接被政策拒絕（fail-closed，
  agent 收到拒絕並回報）。
- **網路**：`network_access:false`，approval accept 也不放行（DNS 全阻）。
  即 workspace-write 僅擴大「工作目錄內寫入」，其他邊界維持。

## 4. 選項（供裁決；rev1 的「升版」選項已因歸因不成立而移除）

| 選項 | 內容 | 效果與配套 |
|---|---|---|
| B1 | workbench 在 **turn/start** 帶 `sandboxPolicy:{"type":"workspaceWrite"}` | turn 級、可逐 session／逐 turn 控制；workspace 內寫入**仍會出 approval**（untrusted 下），accept 後真的能寫。改動點：`internal/codex/turns.go` StartTurn 參數＋政策決定由誰控制（固定值或 per-session 設定） |
| B2 | 由 workbench 管理的 `CODEX_HOME` 寫 `config.toml` `sandbox_mode="workspace-write"` | home 級全域；workbench 目前直接用使用者的 `~/.codex`（無隔離 home），走這條需先隔離 CODEX_HOME，影響面大 |
| C | 現狀＋UI 揭露「read-only 下寫入指令的核可必然失敗」 | 最小變更；approval 對寫入指令維持誤導性（按允許也不會成功） |

共通約束（自 G8）：**不引入 `acceptWithExecpolicyAmendment`**。若選 B1，approval
語意變成「sandbox 內放行」（寫入仍被詢問、accept 有效），比 rev1 誤判的「approval
消失」溫和——實測 G6／G7 下 `touch` 仍出 approval。

## 5. 重現方式

```
go build -o /tmp/probe-codex-approval ./cmd/probe-codex-approval
/tmp/probe-codex-approval -codex-bin tools/codex-cli/node_modules/.bin/codex \
  -out /tmp/out -groups 1,2,3,6,8
/tmp/probe-codex-approval -codex-bin <bin> -out /tmp/out7 -groups 7,4,5 \
  -sandbox '{"type":"workspaceWrite"}'
```

每組輸出（JSON）：binary 版本／sha256、thread/start 參數與完整 response、規則檔
前後 digest（變動時附內容）、approval method 與原始 params、實送 decision、turn
結局、marker 是否落地、rollout `turn_context`、agent 回覆尾段。
