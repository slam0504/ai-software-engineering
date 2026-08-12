# m2-gate-v1.jsonl — 凍結的真實 M2 v1 journal fixture

M3a gate 引擎泛化前的相容性基準：`internal/gate` 現行（v1）`OpenJournal`／
`Project` 必須能持續解析這份 fixture。任何未來變更若破壞這個測試，即為 v1
replay 相容性回歸。

## 內容與每行出處

檔案共 6 行（6 個 `GateOp`），對應 3 個 `approval_id`：

| 行 | op_id | approval_id | _type | 出處 |
|---|---|---|---|---|
| 1 | `01KZSQN1N80005BABES0CCKB47` | `01KZSQN1N7000AJNEECCGH8M3C` | gate_request | 真實 M2 E2E 驗收 workspace |
| 2 | `01KZSQPKPR0001Y3GMC6GV5ES1` | `01KZSQN1N7000AJNEECCGH8M3C` | approval_record（approved） | 真實 M2 E2E 驗收 workspace |
| 3 | `01KZSQXAMX000FNHF8DF9J2GER` | `01KZSQXAMW0006RERF0874JVD3` | gate_request | 真實 M2 E2E 驗收 workspace |
| 4 | `01KZSR4ZD20004B987Y5PWR054` | `01KZSQN1N7000AJNEECCGH8M3C` | transition（→ stale） | 真實 M2 E2E 驗收 workspace |
| 5 | `01KZTQ6NTY000PDB7H5HKGD0RN` | `01KZTQ6NTY000E5GEPJH4875A3` | gate_request | Task 0 補產（見下） |
| 6 | `01KZTQ6NVB000DXJFKSTW11YJQ` | `01KZTQ6NTY000E5GEPJH4875A3` | approval_record（rejected） | Task 0 補產（見下） |

**第 1–4 行**：逐位元組取自本機 `~/m2-accept/.workbench/gate.jsonl`（M2
驗收使用過的 workspace，4 筆記錄，涵蓋 gate_request → approved → 第二次
gate_request（spec_manifest 變更）→ 對第一筆 approval 的 stale transition，
是兩份候補 journal 中最完整的一份）。已用 `diff` 核對與原始檔前 4 行逐位元組
相同，未重排、未重新序列化。

未採用：`~/m2-accept2/.workbench/gate.jsonl`（3 筆：gate_request → approved
→ stale）——涵蓋範圍是第一份的子集（少了促成 stale 的第二次
gate_request），且無 rejected 記錄，故不合併，避免堆疊近乎重複的敘事而不
增加覆蓋率。

**第 5–6 行**：兩份候補 journal 皆無 `decision:"rejected"` 記錄，依 Task 0
brief step 2 以**現行未改動的 code path**補產——拋棄式程式（`go run` 後即刪，
未留在 repo）呼叫 `gate.OpenJournal` → `gate.NewService` → `Service.Submit`
→ `Service.Decide(id, "rejected", "spec 尚未定案，退回補充", …)`，ulid／時間
函式與 `app.go` 正式路徑相同（`contract.NewULID(time.Now())` ／
`time.Now().UTC().Format(time.RFC3339Nano)`）。輸出的兩行是這次執行產生的
真實 v1 code 輸出，非手工合成 JSON；`approval_id`、digest、commit hash 等
皆為本次執行新產生的合成值（非取自任何既有 workspace）。

## 去敏

- **絕對路徑**：兩份來源 journal 內容中皆未出現任何絕對檔案系統路徑（已用
  `grep -o '/Users/[^"]*'` 核對 fixture 全檔，無命中）。無需去敏。
- **reason 文字**：來源第 2 行 reason 為 `"approve測試"`，未包含機敏資訊，
  原樣保留。第 5–6 行的 reason（`"spec 尚未定案，退回補充"`）為本次補產時
  自行給定的中性文字，非取自真實使用者輸入。
- **approver.id**：第 2 行 `approver.id` 為使用者本人（repo 擁有者）在自己
  private repo 中的 git identity，屬自我揭露而非第三方 PII，原樣保留。第
  5–6 行的 `approver.id`（`"reviewer-1"`）為補產時的合成值。
- **base_commit / digest 值**：皆為既有 workspace 或本次補產時的
  hash／digest 值，非可回溯機敏資訊之欄位，原樣保留。

整體判斷：**無需對既有值做文字替換去敏**（無絕對路徑、reason 與
approver.id 皆非機敏或屬自我揭露）；步驟一僅為逐位元組複製 + 追加，未做任何
JSON 重排或重新序列化。

## 煙霧測試

`internal/gate/fixture_test.go`：`TestM2GateV1FixtureSmoke`

- fixture 複製到 `t.TempDir()` 再 `OpenJournal`（避免 append 手把測到
  `testdata/` 本身）
- `len(ops) == 6`
- `Project(ops)` 無錯，`len(entries) == 3`（`01KZSQN1N7000AJNEECCGH8M3C` →
  stale；`01KZSQXAMW0006RERF0874JVD3` → pending；
  `01KZTQ6NTY000E5GEPJH4875A3` → rejected 終態——M3a Task 3 已補上此相容性
  缺口，見 `TestProjectNormalizesV1AndRejectedTerminal`）

驗證指令：`go test -race ./internal/gate/ -count=1`
