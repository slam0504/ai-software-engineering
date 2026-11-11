# 外部參考對照

這份文件記錄兩份外部參考的固定版本、可借用的機制、限制，以及對應的票號。backlog 只連到本文件的段落，不重複比較內容。

## 0. 使用規則

- 這裡列的都是**設計時的參考**，不是可以直接移植的實作。對接方式要在各票的設計中另外決定。
- 已凍結的契約優先。新增 schema 或改變判定語意時，必須另外列為契約變更。已凍結的契約包括：
  - B5／B6 的 `TaskRunPayloadV1`；
  - Gate 3 證據契約；
  - C1 的驗收條件；
  - DomainSpec 收斂報告的五項採用前提。
- 本文件不做成本判斷。「便宜」「低成本」「可直接用」這類說法，必須附上依據才能寫。
- 外部專案的數值不沿用，例如期限、預設上限、門檻。

## 1. 來源與固定版本

| 代號 | 來源 | 固定版本 | 取得與核對 |
|---|---|---|---|
| GR | Grounder 知識處理與規則推理系統規劃書 v1.0（2026-11-04） | SHA256 `f8b28d1abcbe5b7d80751edfc0cce7f81e45372c12b35abcf0332bcca674d269`（45,557 bytes） | owner 持有的個人規劃草稿，**尚未實作**；其中的門檻、工時、容量都是規劃假設。**本 repo 不收錄全文**（owner 2026-11-11 決定）：我們只借用它的想法、邏輯與方法，原構想的作者也還在實驗中。SHA256 只用來識別當時參考的是哪一版草稿。本文件引用的 GR 段落編號，是給持有原檔的人對照用的；借用的內容在 §2 已經摘述。 |
| RB | review-bridge（<https://github.com/fortes1219/review-bridge/tree/50185a26b14251969dbe0725fe2decb0e5599fd3>） | commit `50185a26b14251969dbe0725fe2decb0e5599fd3`，版本 0.3.0-rc.3，MIT 授權 | **只審查了選取的 18 個檔案**，清單見 §1.1。這 18 個檔的 git blob 雜湊，已和該 commit 的 tree 逐一比對相符（2026-11-11）。該 commit 共有 315 個檔，其餘 297 個沒有讀。**沒有執行過這個工具。** |

### 1.1 review-bridge 的審查範圍（18 個檔案）

路徑都相對於該 commit 的根目錄。

- 根目錄的文件：`README.md`、`ARCHITECTURE.md`、`AGENTS.md`、`CHANGELOG.md`
- 說明文件：
  - `docs/concepts/index.md`
  - `docs/principles/why.md`
  - `docs/principles/asking.md`
  - `docs/principles/references.md`
  - `docs/guide/results.md`
  - `docs/reference/result-file.md`
  - `docs/reference/integration.md`
- 設定與 schema：
  - `review-bridge/config/default-policy.json`
  - `review-bridge/features/workOrder/schemas/review.result.schema.json`
  - `review-bridge/features/workOrder/schemas/writer.result.schema.json`
- 原始檔與提示詞：
  - `review-bridge/review-prompt.md`
  - `review-bridge/features/workOrder/acceptance.mjs`
  - `review-bridge/features/setupCheck/reviewerProbe.mjs`
  - `review-bridge/features/workOrder/workspaceEvidence.mjs`

## 2. Grounder（GR）

| 機制 | 可借用的部分 | 限制與不可照搬之處 | 對應票 |
|---|---|---|---|
| 規則包發布生命週期（GR §10） | 不可變版本、執行時鎖定快照、回滾時保留歷史，這三項可以當作提案骨架。 | 見 §2.1。 | D1 |
| 判定順序漂移（GR §9、T10） | 用原始檔或 AST 的指紋，觸發「production 相容性未重新確認」這個狀態。 | 見 §2.2。 | D1 |
| 驗收案例的紀律（GR §17） | 補上案例家族、預期值來源、獨立核對方式、邊界覆蓋。 | 既有 44 筆（41 筆 evaluated、3 筆 acquisition_failed）繼續作回歸用途，不當成盲測集。擴大規則面時，要另外準備獨立的驗收案例。 | D1 |
| 維護成本估算（GR §21） | F／s 可以當作估算框架。 | 見 §2.3。 | D1 |
| 事實來源與接受依據（GR §5–6） | 檢查事實的來源、接受依據，以及歷史追溯的需求。 | 見 §2.4。 | C1 |

### 2.1 發布生命週期的限制（D1）

- 發布生命週期和 `spec/rules/**` 的 scope 擴充是兩件事：
  - scope 擴充回答的是「哪些內容會影響核准的有效性」；
  - 發布切換回答的是「哪一版開始被使用」。
  - 兩者可以一起設計，但要分別裁決。
- 以下幾點需要另外定義：
  - 預期版本的核對；
  - 寫入失敗與中斷之後的恢復順序；
  - workbench 的規則失效條件。這一項不能套用 GR 的制度生效日期。
- 「單一寫入者＋journal」不足以證明切換是原子的。

### 2.2 漂移偵測的限制（D1）

- 這一項對應收斂報告 §五第 1 項，以及 M4.5 建議 1 的自動化交叉檢查。
- 指紋不同，不等於判定順序已經改變；指紋相同，也只涵蓋有納入雜湊的範圍。
- 要解除這個狀態，必須由針對 precedence 的案例和 production 重放共同驗證。不得只更新指紋就解除。
- 這一項只影響 shadow 結果是否可信。M4 維持 shadow／explain。

### 2.3 維護成本估算的限制（D1）

- 單位的候選是「一次規則變更」或「一次審核與追查」。
- 要計入的成本包括：雙份規則的維護、漂移檢查、host adapter、測試、遷移。
- shadow 的一致性，不足以推論值得採用。

### 2.4 事實來源與接受依據的限制（C1）

- 沿用已凍結的契約。required checks 的完整性，和 review 集合的完整性（rev7），是兩項不同的驗收責任。
- 工具或 CI 的輸出，先只是觀察證據。核對過執行身分、commit、範圍、完整性與接受規則之後，才能成為特定判斷可用的 fact。
- 是否需要獨立的 Claim／AcceptedFact 實體，留到設計時再判斷。
- 歷史追溯要先確定想回答哪些查詢，再區分三種時間：來源事件發生的時間、系統取得的時間、規則適用的時間。
- 只加時間戳，卻沒有保存對應的版本與事實內容，不足以重現當時的判斷。
- 歷史追溯和 D2a 的防竄改是兩個不同的問題。

### 2.5 語意差異

DomainSpec 的整體 truth 順序是 conflict → deny → unknown → true（`internal/domainspec/eval.go:522`）。GR 的案件結果，則是讓未知或衝突先轉人工。兩者不可互換。

### 2.6 不納入

- AI 起草規則。
- OCR／RAG。
- 多角色權限與多租戶技術棧。

## 3. review-bridge（RB）

| 機制 | 可借用的部分 | 限制與不可照搬之處 | 對應票／用途 |
|---|---|---|---|
| 審查結果的固定格式 | 逐條 verdict（`pass`／`fail`／`cannot_verify`）、finding 欄位、已檢查與未執行的清單、停止原因。 | 見 §3.1。 | [`mailroom 範本`](../collaboration/mailroom-templates.md)；C1c 的呈現結構 |
| 驗收 ID 集合檢查 | 逐條驗收報告的 ID 集合，要等於該版契約的 active ID 集合；每個 ID 恰好有一筆彙總結果。 | 見 §3.2。 | D5 |
| 以 Git 為準的變更紀錄 | 把三類變更分開記錄：Git 觀察到的、實作者宣稱但未核對的、開始前就存在的。 | 見 §3.3。 | M4 的執行／審查證據紀錄 |
| 有界證據包 | 交付時固定提供：由工具算出的 delta、控制方自己跑的測試結果、實作者宣稱與 delta 的不一致清單、初始閱讀範圍。 | 見 §3.4。 | [`mailroom 範本`](../collaboration/mailroom-templates.md) |
| 授權紀錄規則 | 可以當作檢查清單：綁定內容雜湊、紀錄不可變、開始後的修改不影響已核准內容、設定值來源的記錄。 | 見 §3.5。 | C1 |
| 唯讀探測與 MCP 隔離 | 開工前先驗證特定設定是否生效；記錄每種證據的強度差異。 | 見 §3.6。 | provider adapter 檢查 |
| 預算模型 | 絕對期限、以剩餘時間作為每次呼叫的上限、`partial`／unknown 用量標記。 | 見 §3.7。 | D4 |

### 3.1 審查結果格式的限制

- `proven` 仍然依賴 reviewer 的判斷。RB 的程式（`acceptance.mjs`）包含下列檢查：
  - 回報的 ID 是否完整、有沒有重複、有沒有契約裡不存在的 ID；
  - 判定不是 `fail` 時，證據字串不得為空；
  - finding 引用的條件編號，必須是契約裡存在的條件；
  - 整體判 PASS 時，不得有任何條件是 `fail`；
  - writer 宣稱整體完成時，不得有未完成的條件。
- 通過這些檢查之後，程式把 reviewer 的 `pass` 映射成 `proven`、`fail` 映射成 `failed`，其餘是 `unknown`。它**不判斷證據是否真的支持該條件**。RB 的原始碼也自己說明，程式無法知道測試證明了哪些條款。
- 所以這個機制的正確描述是「由程式檢查回報的完整性，並彙總驗收判定」。模型填的 `pass` 加 evidence，不能成為 Gate 3 的權威依據。
- 我們的範本要多做兩件事：
  - 區分證據的取得方式：自行重跑、讀取他人的 log、只檢查原始碼；並依契約的要求限制 `pass` 的適用條件。
  - 保留 `cannot_verify`。「沒有足夠證據提出 FAIL」不等於通過。

### 3.2 驗收 ID 集合檢查的限制

- RB 的 AC1、AC2 是封存契約內的清單編號，不是跨版本穩定的 scenario ID。
- 測試的 metadata 仍然允許一個 scenario 對應多個測試，或一個測試涵蓋多個 scenario。合法的多重覆蓋，不得被判成重複錯誤。
- 這項檢查要對齊 D5 與 D7b 的關係，不另外建立一套平行的追蹤機制。

### 3.3 Git 變更紀錄的限制

- RB 的版本指紋有四個涵蓋不到的地方：
  - 被 ignore 的 untracked 檔不在指紋內；
  - 沒有 Git 的目錄，會回報 unknown；
  - 只在幾個時間點取樣，期間改動後又還原的變更看不到；
  - Git 觀察到變更，不等於證明變更是實作者寫的。
- B3b 那種 repo 外的 candidate，仍然需要逐檔 manifest、外部測試輸入清單和固定複本。版本指紋解決不了它的需求。
- delta 是結束之後才知道的，不寫回 TaskRun snapshot。snapshot 是開工時固定的核准上下文。
- Gate 3 契約使用 commit OID，不採用 diff bytes 的 digest。照搬 RB 的做法會改變已凍結的契約。

### 3.4 證據包的限制

- 不照搬「禁止讀交接文件」。DECISION、核准的規格與相關交接紀錄，要以固定版本放進證據包，並且把「核准決策」和「實作者的結論」分開。
- 閱讀範圍只是初始範圍，必須允許沿著具體問題繼續追查。
- 不照搬「複審的新發現只能落在變更檔」。有些缺陷是改動觸發的，但位在沒有改動的檔案裡。
- finding 不強制要有程式行號。方案矛盾、缺少必要實作、執行期證據這幾類，可以引用契約段落或 artifact。
- 已修正的 finding 要明列關閉依據，不能只是從回報中消失。

### 3.5 授權紀錄規則的限制

- 24 小時期限、單次使用、`open_questions` 的擋關方式，都是 RB 自己的政策選擇，不直接採用。
- 我們的現況（基準同 §4）：
  - pending 的 request 只能成功提交一次（owner 審查時指出；本文件未重新核對）；
  - `Expired` 是 pending 的 Gate 3 request 在決議時重驗不符後的終態（`internal/gate/types.go:13`，本次已讀原始碼），不是固定的時間期限。
- 「一次工作執行的授權」和「可持續引用的 spec／plan 核准」要分開。後者不會因為用過一次就被消耗掉。
- `open_questions` 的處理要分兩種：需要 owner 裁決的問題，要先解決；已經明確接受、而且有驗證計畫的假設，可以保留。

### 3.6 唯讀探測與 MCP 隔離的限制

- 我們的現況見 §4 的「MCP 隔離」列。待辦是查清所有啟動路徑的涵蓋範圍，不是新增同一個旗標。
- MCP 隔離不是跨 provider 的同一個旗標。RB 在 Codex 這條路徑上，另外停用 apps 與 plugins，並逐一設定 MCP server。
- RB 的 reviewer 另外還有它自己的 graph MCP，以及可設定的 allowlist。
- 一次哨兵寫入被拒，不能證明所有路徑和外部 MCP 都沒有副作用。

### 3.7 預算模型的限制

- RB 的 token 上限主要約束 writer。reviewer 的用量只有記錄，不受同一份預算阻擋。
- D4 的範圍比 RB 大：涵蓋所有 session、CPU／RSS、併行數。兩者不等價，RB 的預設值不沿用。
- 把測試移到 agent 的沙箱外執行，不會自動讓測試可信。runner 的權限與資源，仍然要另外界定。

### 3.8 不採用為依賴

- RB 的原生執行只在 Windows x64 驗證過。owner 的開發機是 Intel Mac（x86_64），是否支援沒有確認。
- 就算用拋棄式的專案目錄，也隔離不了 RB 共用的帳號資料與 PostgreSQL。
- 目前不建議試跑。

## 4. 現況對照

- **基準**：本 repo 的 main `d7e2eaa`（commit 日期 2026-11-09），核對日期 2026-11-11。這是當時的狀態，不是即時狀態。
- 以下把「現有實作」「已驗證的能力」「尚未實作的設計」分開列，不做整體嚴格度的比較。
- 標示「本文件未重新核對」的項目，表示這次只確認了檔案或紀錄存在，沒有重新執行測試，也沒有核對它的涵蓋範圍。

| 主題 | 我們的現有實作 | 已驗證的能力 | 尚未實作的設計 |
|---|---|---|---|
| 行程清理與 PID 身分 | `internal/proc` 的有界清理（B2c-5／B2c-6）。 | B2c-7 的 CI 驗證，記錄在 `orphan-timeout-diagnosis-record.md` v3 §8（run `34039387868`）。本文件未重新核對。 | B3b L1 的兩層 custody：方案在 repo 外（owner 持有，2026-11-10 定稿），**沒有實作，也沒有驗收**。backlog 的 B3b 狀態還停在 rev80，尚未反映 2026-11 的進展。 |
| 執行中斷後的狀態 | `internal/singleinstance/lock.go`（single-instance guard）；lease 邊界見 `app_lease_boundary_test.go`；有界清理同上一列。三者各自處理不同的問題。 | 對應的測試檔存在（`internal/singleinstance/lock_test.go`、`app_lease_boundary_test.go`）。本文件未重新核對。 | 沒有主張「程序死了、紀錄仍顯示執行中」的所有情況都已解決。 |
| 規則評估 | `internal/domainspec`：獨立的 `evaluation_error`、mutation、production 重放。 | `docs/superpowers/specs/2026-08-26-domainspec-spike-results.md`：44 筆 corpus（41 筆 evaluated、3 筆 acquisition_failed）零 misalignment，範圍限於 shadow。本文件未重新核對。 | D1 的五項採用前提。 |
| MCP 隔離 | Claude adapter 在設定了 `PermissionPromptTool` 的路徑上傳入 `--mcp-config` 與 `--strict-mcp-config`（`internal/claude/session.go:41-43`，本次已讀原始碼）。 | 本文件未重新核對。 | 其他啟動路徑的涵蓋範圍還沒有查。 |
| 獨立驗證 | 審查方在隔離複本中自己寫 probe 和反例。 | 審查紀錄在 repo 外（mailroom 與 owner 持有的證據目錄）。本文件未重新核對。 | 回報格式見 [`mailroom 範本`](../collaboration/mailroom-templates.md)，剛開始試用。 |

## 5. 修訂記錄

- 2026-11-11：GR 改為不收錄全文，只借用想法（owner 決定）；刪除原 §1.2「收錄 GR 全文時的做法」。
- 2026-11-11：初版。依 owner 對兩份分析與草稿 1 的審查意見整理；GR 全文沒有收錄。
