# B1b 前端兩條 wall-clock 候選重現與處置 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> 版本：rev11（2026-09-05，rev10 短複審 CHANGES_REQUIRED 後修訂：P2 兩處非歷史狀態的「rev9 通過後」改為不帶版號的「本 plan design gate 通過後」，Task 3 的「不因 rev8 放寬」改為「不因本次 file-confirm pre-merge control 例外放寬」；前版：rev10（rev9 短複審 CHANGES_REQUIRED 後修訂：P1 file-confirm 判準改以「計入的命中子程序」計數（exit 非零＋唯一 FAIL 在還原檔＋指定 timeout 訊息＋`1 failed | 396 passed` 四者同時成立才算命中；單一非相關失敗只揭露不計；任一子程序 ≥2 FAIL 整輪無效停下），launcher 註解同步；P2 尚未完成段 rev 字樣、非歷史段落去版號；前版：rev9（rev8 短複審 CHANGES_REQUIRED 後修訂：P1 Global Constraints 與 launcher 註解仍是 rev7 契約——為 file-confirm 明列狹義例外（至少兩份非零、唯一 timeout 可落在還原檔任一測試、其他檔案／setup／資源失效不計入、環境失效直接停下不降兩份），並重申不放寬 living 契約；P2 四處狀態文字同步；前版：rev8（正式 (a′) 輪 N1／N2 不符 rev7 候選層級判準後修訂：owner 裁定 negative control 改採**檔案層級**判準，但不得以既有 `v4-N1`／`v4-N2` 資料回填 Gate——保留為「candidate-level Gate 失敗、file-level 診斷證據」，rev8 通過後以新 tag `v4-N1-file-confirm`／`v4-N2-file-confirm` 各重跑一輪；P 兩輪與 seq 三輪不重跑；Task 3 把 pre-merge control 判準與 living 契約回歸規則分開；前版：rev7（rev6 短複審 CHANGES_REQUIRED 後修訂：P1 Gate A 的 manifest 驗證改為在 `$B1B` 內執行（manifest 只含 basename），Step 0 開頭補「已存在即 exit 1」讓不得重產 fail loud；P2 Task 1 前置與版本摘要的「rev5 通過後」改 rev7；前版：rev6（rev5 短複審 CHANGES_REQUIRED 後修訂：P1 正式輪開始前先對 83 個既有 artifact 建立 `legacy-artifacts-sha256.txt`（目錄外建立再移入、不得重產），Gate A 改為 `shasum -a 256 -c` 該 manifest；P2 狀態列改「rev5 通過後」、Step 1 的 `shasum -c` 改用 `$B1B` 絕對路徑；前版：rev5（rev4 短複審 CHANGES_REQUIRED 後修訂：P1 正式輪 artifact 全部改 `v4-` 前綴（`v4-single-*`／`v4-P-r1`／`v4-P-r2`／`v4-seq-r1..3`／`v4-N1`／`v4-N2`），既有 `P-r1-*`、`SPIKE-P-*` 不得覆寫；P2 Task 1 殘留的 (a) 契約與「約 50ms」預設值統一為 (a′) 與只記錄；P2 補 `B1B` 沿用 `/tmp/b1b.RHppZ0` 的 rev4 例外；前版：rev4（Task 1 Step 4 依停下條件中止後修訂：(a) 純 import 預熱的 positive control **不成立**（F1 仍 timeout 2/3），owner 裁定選項 1、經未提交 spike 驗證後，模板改為 **(a′) import 預熱＋一次 `EditorView` 建構預熱**；spike 證據只作設計依據、不算 Gate A；正式 P／N1／N2 依新模板重跑；前版：rev3（第二輪 CHANGES_REQUIRED 後修訂：一項 P1——`run3` launcher 改為 fail loud（子 shell 帶 Node PATH 並以 vitest rc 結束、父層逐一 `wait "$pid"` 並比對 `.exit`、artifact 缺件或無重疊即非零返回），Gate A 明列 P 三份 exit 0、N1／N2 非零且只失敗於指定候選；非阻斷：Architecture 與註解模板改為「目前假設」語氣；前版：rev2（第一輪 CHANGES_REQUIRED 後修訂：三項 P1——preflight 表格 F1／F2 三格寫反已更正並補齊 artifact、Gate A 三份併發改用可逐字執行的 launcher 與重疊區間判準、register 規則 7 具名 F1／F2 的 FAIL 分類契約——與一項 P2——Task 2 scope check 時機；D1–D3 裁定回寫；前版：rev1）
> 狀態：**design gate 待審（rev11 短複審）——rev7 已 APPROVED 並提交（`b02d15e`）；正式 (a′) 輪已執行：Step 0 manifest、P 兩輪、seq 三輪全部成立，`v4-N1`／`v4-N2` 不符候選層級判準、依停下條件中止；worktree `b1b-1` 已還原到 (a′) golden 並確認 397 PASS、legacy manifest 83/83 OK；主工作區只有本 plan 修改**。本 plan design gate 通過後只重跑 Step 5 的兩個 file-confirm 輪與 Step 6
> 票源：Pre-M4 Readiness Backlog **B1b**（rev14 票面 0.3 pt；**owner 於本 plan gate 裁定改為 0.4 pt**，見 D3）。B1 驗收條件 (4)：「剩餘兩條前端候選須以現行 HEAD 重現——成立者補入 living 文件後併入本票或開續票，不成立者除名（裁決 #9）」
> 基準 commit：**`92719fba41c3402daed44140a280d32a90510c36`**（backlog rev14，已推送、`git ls-remote` 核實相符）
> 前置：B1a aggregate 已關閉（rev14）。本票關票後 **B1 票整體關閉**，B2 驗收條件 (3)「race＋全套測試升 required 前置 B1 完成」的前置成立

**Goal:** 依裁決 #9 對兩條前端候選做出可稽核的處置：兩條**已在現行 HEAD 重現**（preflight 三份併發 3/3），因此走「成立者併入本票」路徑——修掉測試的牆鐘相依（**production 零變更**），以測試側 negative／positive control 證明修正改變了鑑別力，補入 living 文件，關閉 B1。

**Architecture:** 兩條候選的失效形狀相同（同一 `Test timed out in 5000ms`、同在三份併發下出現），**根因目前為同一假設**——rev4 修正為：CodeMirror 的**模組動態載入＋首次 `EditorView` 建構**兩筆一次性成本落在執行中的測試（rev3 只假設前者，Task 1 (a) 的 P-r1 證明前者不足：F2 轉綠但成本移到 SpecWorkspace 第一條、F1 仍 timeout），待 `v4-N1-file-confirm`／`v4-N2-file-confirm` 確認。修法在測試檔而非元件：在 `beforeAll` 內完成 import **並建構一次即銷毀的 `EditorView`**，把兩筆一次性成本都移出測試本體，測試本體只剩它們真正要驗的契約（loading 顯示與輸出累積；換檔後丟棄過期 assist 結果）。**不動 `vitest.config.ts` 的 `testTimeout`**——放寬 timeout 是 B1a 系列明確拒絕的牆鐘式修法。

**Tech Stack:** Vitest 4.1.10（jsdom）、Node v26.8.1（`~/.nvm/versions/node/v26.8.1/bin`，本 shell PATH 預設不含，指令須顯式加入）、`@vue/test-utils`；無新依賴。

**參考文件：**
- `docs/architecture/pre-m4-readiness-backlog.md`（rev14：B1b 票面、B1 驗收條件 (4)、裁決 #9）
- `docs/architecture/wall-clock-test-register.md`（v1：B 段兩條候選「待重現（B1b）」、前端規則、更新責任）
- `docs/superpowers/plans/2026-09-03-b1a-2-wallclock-determinization.md`（同系列：production 零變更時以測試側 negative control 取代 §6.7 mutation 的 owner 核准例外——本票沿用其形式）
- `docs/superpowers/plans/2026-09-04-b1a-4-integration-acceptance.md`（D1 紅燈分類、帳表與隔離 worktree 慣例）
- `docs/architecture/sdlc-ai-agent-automation-plan.md` §6.7（本票 production 零變更，原文不適用；改採 B1b 專屬例外，見 Task 2）、§6.8

---

## Preflight 事實（2026-09-04，現行 HEAD `92719fb`，唯讀量測）

**兩條候選**（名稱與 backlog／register 逐字一致）：
- (F1) `frontend/src/components/PlanWorkspace.test.ts` `describe('PlanWorkspace') > it('PlanAssist 送出後草稿區顯示 loading，事件送達後輸出累積')`（現 line 31；該檔**第一個** `it`）
- (F2) `frontend/src/components/SpecWorkspace.test.ts` `describe('SpecWorkspace draft accept') > it('discards spec-assist result if the file switches during the call')`（現 line 46；該檔**第二個** `it`，第一個為 `accept writes draft via SpecWrite, not before`）

兩條測試本體自 2026-08-12（`912490c`／`2ea4883`）未再變動；兩檔最後一次 commit 為 2026-08-27 `7789f40`（A2，不涉這兩條）。

**環境**：`vitest.config.ts` 只設 `environment: 'jsdom'`，`testTimeout` 未設 → 安裝檔 `node_modules/vitest/dist/chunks/coverage.*.js` 內 `resolved.testTimeout ??= resolved.browser.enabled ? 15e3 : 5e3`，即 **5000ms**。全套 40 檔 397 條。

**重現矩陣**（`vitest run --reporter=verbose`，每次 stdout 與 exit code 落檔於 `/tmp/b1b-pre.88l8kP/`，26 個檔案的 SHA-256 存同目錄 `SHA256SUMS.txt`；F1／F2 數值以測試名稱錨定從 artifact 抽取——rev1 曾把 seq-r2、seq-r3、con-c3 三格 F1／F2 寫反，rev2 依 artifact 更正）：

| 執行方式 | artifact | 結果 | F1 耗時 | F2 耗時 |
|---|---|---|---|---|
| 全套單跑 ×1（基準） | `base-r1.txt` | 397 PASS，31s | 見 artifact | 見 artifact |
| 全套依序 r1／r2／r3 | `seq-r1..3.txt` | 397 PASS ×3（32／28／32s） | 2098／**142**／**2090**ms | 1903／**37**／**1889**ms |
| **全套三份併發 ×1**（rev1 執行，三份以 `&` 同時啟動後 `wait`；**未保存各份 start／end，重疊區間無法事後證明**，僅作 preflight 重現依據，不作 Gate A 證據） | `con-c1..3.txt` | **三份各 2 failed／395 passed**，wall 90s | **7471／6866／7544ms** | **7663／7332／6344ms** |

（rev1 初稿曾引用一次無 artifact 的全套基準 35.5s；rev2 以有 artifact 的 `base-r1` 取代。）

三份併發下失敗的**恰好只有這兩條**，失敗訊息逐字為 `Error: Test timed out in 5000ms.`。→ **裁決 #9 的「以現行 HEAD 重現」成立，3/3。**

**根因假設（目前假設，待 Task 1 differential control 驗證；下列第 1、2、5 項有 artifact 或可讀碼複核，第 3、4 項為推論）**：
1. `PlanWorkspace.vue` `initEditor()`（現 line 181–192）與 `SpecWorkspace.vue` `initEditor()`（現 line 87–98）在 `onMounted` 內以 `await Promise.all([import('codemirror'), import('@codemirror/state')])` **動態載入 CodeMirror**（`node_modules/@codemirror` 2.7 MB）；jsdom 掛載時 `<div ref="editorHost">` 存在（兩檔 template 各一處），因此 import 確實發生。
2. 單檔獨跑 ×3（artifact `single-PlanWorkspace-r1..3.txt`、`single-SpecWorkspace-r1..3.txt`）：PlanWorkspace 第 1／2／3 條＝**491／41／57、447／45／56、394／35／38ms**；SpecWorkspace 第 1／2／3 條＝**46／324／19、50／397／20、61／408／22ms**。候選各自多出約 300–450ms，其他測試 19–61ms。（rev1 曾引用一組無 artifact 的單檔數字 401／378／452 與 370／350／372ms，屬執行者觀察，rev2 以有 artifact 的重跑取代。）
3. （推論）兩檔之所以落在不同序位：動態 import 的模組轉換與求值在 worker 主執行緒進行，**成本落在它完成當下正在執行的測試**。PlanWorkspace 第一條流程較長，自己吸收；SpecWorkspace 第一條很快結束，成本落到第二條。
4. （推論）全套並行（每檔一個 worker）時放大到約 2s，三份併發時放大到 6–8s > 5s。若假設成立，這是**測試量到了牆鐘**（模組載入時間），不是它們要驗的契約（loading 顯示／換檔丟棄）失效。
5. 唯一其他使用同樣 pending-promise 寫法的 `TcaWorkspace.test.ts` 不動態載入 CodeMirror，三份併發下最慢 787ms，未失敗——與根因一致。

**已驗證**：以 (a′) 把兩筆成本移出測試本體後，三份併發下兩條穩定轉綠（`v4-P-r1`／`v4-P-r2` 六份 397 PASS，見 Gate A 證據）。**未驗證（本 plan design gate 通過後要做）**：逐檔移除 (a′) 後，該檔是否依檔案層級判準穩定轉紅（`v4-N1-file-confirm`／`v4-N2-file-confirm`）。**只有兩個 file-confirm 都成立（2/2），上述根因假設才升格為已確認。**

**Task 1 (a) 執行結果（2026-09-04，worktree `b1b-1` @ `92719fb`，證據目錄 `/tmp/b1b.RHppZ0`）——positive control 不成立，依停下條件中止**：
- 基準 397 PASS（`base-r1`）；原始 hash `db31bc91…`／`ef218eff…`；(a) 套用後 golden `83a946fa…`（Plan）／`86293572…`（Spec），diff 各 +9／−1。
- 單檔 ×3（`single-*`）：PlanWorkspace 第 1 條 **344／433／436ms**（其餘 46–70ms）；SpecWorkspace 第 1／2 條 **352／144、332／129、302／149ms**——成本自 F2 移到同檔第一條，未消失。
- `run3 P-r1`：rc=0（overlap 83.14s，wait／exit 相符）；**c1、c2 各 1 failed／396 passed，c3 397 passed**。失敗者恰為 F1（5523／5393ms `Test timed out in 5000ms`，c3 4607ms 險過）；F2 三份 1475／1737／1746ms PASS；SpecWorkspace 第 1 條 4209–4732ms。→ **import 預熱只解掉一部分，剩餘一次性成本落在首次建構 `EditorView` 的測試上**（新假設）。N1／N2 未執行。

**Spike（owner 裁定選項 1；未提交、只作 rev4 設計證據、不算 Gate A）**：在 (a) 之上，`beforeAll` 內以暫時 host 建構一次 `EditorState`＋`EditorView` 隨即 `destroy()` 並移除 host，不 catch。spike hash `db90d0d9…`（Plan）／`c3a7adc3…`（Spec）。
- 單檔 ×3（`spike-single-*`）：PlanWorkspace 第 1 條 **157／158／162ms**；SpecWorkspace 第 1／2 條 **61／108、66／131、76／139ms**。
- `run3 SPIKE-P`：rc=0（overlap 87.00s）；**三份皆 397 PASS**；F1 2317／2246／2427ms、F2 1302／1549／1318ms、SpecWorkspace 第 1 條 1073／1064／1116ms。
- 完成後還原到 (a) golden（`shasum -c hash-golden.txt` 兩檔 OK）。spike 的 P 只跑一輪、未跑 N1／N2，因此**只證明方向可行**，正式證據由 rev4 的 Task 1 重取。

---

## Production 零變更聲明

本票不改 `.vue`、不改 `vitest.config.ts`、不加依賴。只改兩個測試檔各加一段 `beforeAll`。若 Task 1 證明測試側修法不足（例如成本仍落在測試本體），本票停下回報，不擅自改元件的載入策略——那是 UX／bundle 層決策，屬另一張票。

**§6.7 不適用聲明與 B1b 專屬例外**：production 零變更，沒有可植入的 production mutation。沿用 B1a-2 經 owner 核准的形式：以**測試側 negative／positive control** 取代，並保留 N/N 全跑、hash 前後比對、byte-identical 還原與完整回歸。**不得寫成符合 §6.7 原文。**

---

## 待 owner 裁定事項（design gate）

**D1 處置方式**（改變 Task 1／Task 2 的 diff）。三個可行選項，preflight 證據下的取捨：

| 選項 | 做法 | 優點 | 缺點 |
|---|---|---|---|
| **(a) 預熱 import（建議）** | 兩個測試檔各加 `beforeAll(async () => { await Promise.all([import('codemirror'), import('@codemirror/state')]) }, 30_000)`，讓模組載入在 hook 內完成 | production 零變更；測試仍掛載真實編輯器，契約覆蓋不變；成本移到 hook，hook 的 timeout 只是卡死保險絲 | 兩檔重複三行；依賴「hook 先於第一條測試」的 vitest 語意（穩定、有文件） |
| (b) `vi.mock('codemirror')` 打樁 | 在兩檔 mock 掉 CodeMirror | 最快 | 元件從此在測試中不建構真實 `EditorView`，降低這兩檔所有測試的保真度；超出兩條候選的範圍 |
| (c) 兩條加 `{ timeout: 15_000 }` | 放寬單測 timeout | 一行 | 正是 B1a 拒絕的牆鐘式修法，負載更大時再度偽陽；三份併發已到 7.7s，餘裕不到 2 倍 |

**owner 裁定（第一輪）：採 (a)**。**Task 1 實測 (a) 不足（見上），owner 第二輪裁定採選項 1 ＝ (a′)：import 預熱＋一次 `EditorView` 建構預熱**，spike 已證明方向可行；正式 P／N1／N2 以 (a′) 重跑。若 (a′) 的正式 P 仍有任一 F1／F2 timeout，立即停止，屆時再裁定是否接受 (b) 的保真度代價；元件載入策略仍屬另一張票。hook 顯式 `30_000` 而非依賴預設，理由同 B1a-2 #3：它是卡死保險絲、不是成功判準，本 plan 不把「hook 跑得夠快」寫成通過條件。

**D2 living 文件的落法**——**owner 裁定：直接更新 register B 段，不新增 D 段，並補具名 FAIL 分類規則。** B 段兩條由「待重現（B1b）」改為「**已重現並處置**（B1b，commit）」並補根因與修法。規則段新增**規則 7（具名 F1／F2）**：

> 7. **F1 `PlanWorkspace > PlanAssist 送出後草稿區顯示 loading，事件送達後輸出累積` 與 F2 `SpecWorkspace draft accept > discards spec-assist result if the file switches during the call` 自 B1b 處置後，「相同 timeout 可單獨重跑一次判定」的前端規則對這兩條失效**，任何 FAIL 先分類：(i) 命中該測試的契約斷言（F1：`assist-busy` 顯示／`draft-text` 累積／busy 解除；F2：`draft-text` 為空／`accept-draft` disabled）、或可歸因於其契約路徑的卡死（含再次 `Test timed out in 5000ms` 且無環境訊號）→ **回歸，required check 阻擋，不得重跑吸收**；(ii) setup 失敗（掛載／mock 建立）、可證明的資源失效（OOM、worker 啟動失敗）、或其他測試造成的中斷 → **該次無效**，揭露後重跑，不算紅也不算綠。另：前端測試不得把模組動態載入成本留在測試本體。

其他前端測試維持原規則。

**D3 估點核對**——**owner 裁定：改為 0.4 pt**。拆解：preflight（已完成）0.5 hr／Task 1 隔離 worktree 落地＋三組 control 1.0–1.5 hr／Task 2 主 repo 落地＋hash 轉移＋回歸 0.5 hr／Task 3 文件 0.5 hr／gate 往返 1.0–1.5 hr ＝ 3.5–4.5 hr，中位 4.0 hr → **0.40 pt**。連帶（其餘數字不變）：B 軌 116.05 ＋ 1.0 ＝ **117.05 hr → 11.71 pt**；合計 181.05 ＋ 1.0 ＝ **182.05 hr → 18.21 pt**。由 Task 3 的 backlog rev15 落地。

---

## Global Constraints

- 所有 vitest 指令以 `PATH="$HOME/.nvm/versions/node/v26.8.1/bin:$PATH"` 前置，並用 `./node_modules/.bin/vitest`；`node --version` 記入證據。
- **Task 1 一律在隔離 worktree 執行**：`git worktree add --detach /Users/eason_tseng/scratch-worktrees/b1b-1 92719fb`；`frontend/node_modules` 不在 git 內，worktree 需 `cp -R`（或 symlink）主 repo 的 `frontend/node_modules`，複製方式與其後 `vitest run` 首次結果記入證據。主工作區 Task 1 期間零異動。
- 每個工具呼叫以三行前置開頭（`cd` worktree 絕對路徑、`B1B=<mktemp 實際絕對路徑>` 並 `test -d`、HEAD 核對 `92719fb…`），fail loud（沿 B1a-4）。**rev4 起的例外**：證據目錄**不再 `mktemp`**，沿用 rev3 Task 1 已建立的 `/tmp/b1b.RHppZ0`，前置行固定為 `B1B=/tmp/b1b.RHppZ0; test -d "$B1B" || exit 1`；既有 (a) 輪 artifact（`base-r1`、`single-*`、`P-r1-*`、`hash-original.txt`、`hash-golden.txt`）與 spike artifact（`spike-single-*`、`SPIKE-P-*`、`hash-spike.txt`）**一律不得覆寫或刪除**，正式輪全部使用 `v4-` 前綴。
- 每次 vitest 都保存 stdout（`--reporter=verbose`）、stderr、exit code。**三份併發（P、N1、N2 共用）一律用下列可逐字執行的 launcher**，每份保存 PID、`monotonic_ns` start／end、stdout、stderr、exit，父程序保存 `wait` 結果：

  ```sh
  # 用法：run3 <tag>   （在 worktree 的 frontend/ 目錄執行；$B1B 為證據目錄）
  # 回傳值：0 ＝ launcher 層全部成立（artifact 齊全、每份 wait rc 與 .exit 相符、三份有共同重疊區間）。
  #         非零 ＝ launcher 層失敗（2 缺件／3 wait≠exit／4 無重疊），該輪無效。
  # 注意：各份 vitest 自身的 exit code 不併入回傳值——P 預期三份皆 0；N*-file-confirm 預期
  #       至少兩個「計入的命中子程序」（exit 非零＋唯一 FAIL 在還原檔＋指定 timeout 訊息＋1 failed | 396 passed，
  #       四者同時成立才算命中；只數非零 exit 不夠）。這兩項由 Gate A 另行核對 .exit 與 .out。
  run3() {
    tag=$1
    export PATH="$HOME/.nvm/versions/node/v26.8.1/bin:$PATH"
    for c in 1 2 3; do
      ( python3 -c "import time; print(time.monotonic_ns())" > "$B1B/$tag-c$c.start"
        ./node_modules/.bin/vitest run --reporter=verbose > "$B1B/$tag-c$c.out" 2> "$B1B/$tag-c$c.err"; rc=$?
        python3 -c "import time; print(time.monotonic_ns())" > "$B1B/$tag-c$c.end"; echo $rc > "$B1B/$tag-c$c.exit"
        exit $rc ) &
      echo $! > "$B1B/$tag-c$c.pid"
    done
    : > "$B1B/$tag-wait.txt"
    for c in 1 2 3; do
      wait "$(cat "$B1B/$tag-c$c.pid")"; w=$?
      echo "c$c wait=$w exit=$(cat "$B1B/$tag-c$c.exit" 2>/dev/null || echo missing)" >> "$B1B/$tag-wait.txt"
    done
    python3 - "$B1B" "$tag" <<'EOF'
  import sys, os, re
  d, t = sys.argv[1], sys.argv[2]
  missing = [f"{t}-c{c}.{e}" for c in (1,2,3) for e in ("pid","start","end","out","err","exit") if not os.path.exists(f"{d}/{t}-c{c}.{e}")]
  if missing: print("MISSING:", *missing); sys.exit(2)
  waits = {c: (w, e) for c, w, e in re.findall(r"^(c\d) wait=(\d+) exit=(\S+)$", open(f"{d}/{t}-wait.txt").read(), re.M)}
  bad = [k for k,(w,e) in waits.items() if e == "missing" or w != e]
  if len(waits) != 3 or bad: print("WAIT/EXIT MISMATCH:", waits); sys.exit(3)
  st = [int(open(f"{d}/{t}-c{c}.start").read()) for c in (1,2,3)]
  en = [int(open(f"{d}/{t}-c{c}.end").read()) for c in (1,2,3)]
  ok = max(st) < min(en)
  print(t, "exits=", [waits[f"c{c}"][1] for c in (1,2,3)], "max(start)=", max(st), "min(end)=", min(en),
        "overlap_s=", (min(en)-max(st))/1e9, "=> VALID concurrent" if ok else "=> INVALID (not concurrent)")
  sys.exit(0 if ok else 4)
  EOF
  }
  ```

  **有效三份併發的判準：`run3` 回傳 0**（artifact 齊全、每份 `wait` rc 與 `.exit` 相符、`max(start) < min(end)`）；非零即該輪無效、artifact 保留並揭露、重跑。overlap 秒數與三份 exit 記入證據。**每次呼叫 `run3` 後必須立刻 `echo "run3 rc=$?"` 並抄錄。**
- **紅燈語意**：**file-confirm 狹義例外**（僅適用於 `v4-N1-file-confirm`／`v4-N2-file-confirm` 這兩輪 pre-merge control）：以**「計入的命中子程序」計數，不以非零 exit 計數**。「計入的命中子程序」＝同時符合：`.exit` 非零、唯一 FAIL 位於還原檔、訊息逐字為 `Test timed out in 5000ms`、結果為 `1 failed | 396 passed`（唯一 FAIL 可落在還原檔**任一條**測試，含候選以外）。三份中**至少兩個**命中子程序即成立。子程序若 `.exit` 非零但唯一 FAIL 落在**其他檔案**、setup 或可證明的資源失效 → **只揭露，不算命中也不算反證**；子程序全綠 → 不算命中也不算反證；任一子程序出現 **2 個以上 FAIL → 整輪無效並停下**。P 輪與 seq 輪不適用此例外，任何 FAIL 依 B1a-4 D1 分類並揭露、不吸收。**此例外不放寬 living 文件規則 7**：處置後 F1／F2 在 required check 的任何 FAIL 仍先分類、契約回歸不得重跑吸收。
- **時間常數不得成為成功判準**：候選在修法後的耗時只作觀察記錄，不預設數值、不設門檻。
- 三份併發若因環境失效（OOM、worker 啟動失敗、`run3` 回傳 2／3／4）→ 該輪無效、揭露並**直接停下回報**；**不降為兩份**（rev8 的檔案層級判準固定以三份中的 2/3 判定，兩份沒有對應分母），也不提高到四份。

---

## Task 1: 隔離 worktree 落地 (a′) ＋ 三組 control（不 commit）

**前置：D1 第二輪裁定為 (a′)。Step 0–4 已於正式輪完成（見 Gate A 證據）；本 plan design gate 通過後**只執行** Step 5 的 `v4-N1-file-confirm`／`v4-N2-file-confirm` 與 Step 6。**

- [ ] **Step 0（rev6 新增，正式輪任何 `v4-*` 輸出之前）：既有 artifact 的 checksum manifest**。目的：事後能證明 (a) 輪與 spike 的證據在正式輪期間未被覆寫。

```sh
B1B=/tmp/b1b.RHppZ0; test -d "$B1B" || exit 1
test ! -e "$B1B/legacy-artifacts-sha256.txt" || { echo "manifest already exists: refusing to regenerate"; exit 1; }
ls "$B1B" | wc -l                                   # 預期 83（建立前）
( cd "$B1B" && ls | LC_ALL=C sort | xargs shasum -a 256 ) > /tmp/b1b-legacy-manifest.tmp   # 先寫在目錄外
wc -l /tmp/b1b-legacy-manifest.tmp                  # 預期 83 筆
mv /tmp/b1b-legacy-manifest.tmp "$B1B/legacy-artifacts-sha256.txt"
ls "$B1B" | wc -l                                   # 預期 84（建立後）
shasum -a 256 "$B1B/legacy-artifacts-sha256.txt"    # manifest 自身 hash 記入證據段
```

  **manifest 之後不得重產或覆寫**；建立前 83 檔、建立後 84 檔兩個數字記入證據。之後每個 `v4-*` 檔案都是新增，不觸碰清單內任何檔案。

- [ ] **Step 1: worktree 與環境**：建 worktree、複製 `frontend/node_modules`、`mktemp -d /tmp/b1b.XXXXXX`、記 `node --version`／`vitest --version`／HEAD；**基準**：全套單跑 ×1 → 預期 397 PASS；記兩檔原始 SHA-256。**rev4 重做時**：既有 worktree `b1b-1` 與證據目錄 `/tmp/b1b.RHppZ0` 沿用（HEAD、node、vitest、基準、原始 hash 皆已記錄），先把兩檔還原到原始 hash `db31bc91…`／`ef218eff…`（在 worktree 的 `frontend/` 目錄執行 `shasum -a 256 -c "$B1B/hash-original.txt"`，兩檔須 OK）再自 Step 2 開始；正式輪的 artifact 一律加 `v4-` 前綴（golden hash 檔為 `v4-hash-golden.txt`），與 (a)／spike 的 artifact 區隔、不得覆寫（`shasum -a 256 frontend/src/components/PlanWorkspace.test.ts frontend/src/components/SpecWorkspace.test.ts`）。
- [ ] **Step 2: 套用模板（兩檔各一段，放在既有 `describe` 內第一個 `it` 之前；import 行併入既有 `vitest` import）**

```ts
import { beforeAll, describe, expect, it, vi } from 'vitest'   // 依各檔既有 import 只補 beforeAll
```

```ts
  // CodeMirror 由元件 onMounted 內動態 import 並建構 EditorView。B1b 觀察：本檔一條測試在
  // 全套並行下約 2s、三份併發下約 7s > 5s 預設 timeout，其餘測試僅數十 ms；只預熱 import
  // 時成本會移到同檔另一條測試而不消失（假設：剩餘為 jsdom 首次建構 EditorView 的一次性
  // 成本，由 B1b Task 1 的 differential control 驗證）。先在 hook 內 import 並建構一次即銷毀，
  // 讓測試本體只量契約。不 catch：建構或清理失敗就讓 hook 失敗。30s 是卡死保險絲，不是成功判準。
  beforeAll(async () => {
    const [{ EditorView, basicSetup }, { EditorState }] = await Promise.all([
      import('codemirror'),
      import('@codemirror/state'),
    ])
    const host = document.createElement('div')
    document.body.appendChild(host)
    const view = new EditorView({ state: EditorState.create({ doc: '', extensions: [basicSetup] }), parent: host })
    view.destroy()
    host.remove()
  }, 30_000)
```

  套用後 `git diff --stat` 預期兩檔各約 +18／−1（含 import 補字）；記 **golden SHA-256**（兩檔）。**rev3 的 (a) golden `83a946fa…`／`86293572…` 與 spike hash `db90d0d9…`／`c3a7adc3…` 均作廢，不得用作比對基準**（spike 註解含「SPIKE」字樣，與本模板不同）。

- [ ] **Step 3: 單檔觀察**：兩檔各獨跑 ×3 `--reporter=verbose`，artifact `v4-single-<檔>-r1..3.{out,err,exit}`，記錄前三條耗時（只記錄，不預設數值、不設門檻）。
- [ ] **Step 4: positive control（P）**：`run3 v4-P-r1`、`run3 v4-P-r2`（兩輪 `run3` rc 皆須為 0；且六份 `.exit` 皆 0）→ 預期六份全部 397 PASS，F1／F2 耗時（名稱錨定抽取）記錄；另全套依序 ×3（artifact `v4-seq-r1..3.{out,err,exit}`）→ 397 PASS ×3。
- [ ] **Step 5: negative control（N1、N2，differential）——rev8 改採檔案層級判準**

  **判準為何改（owner 裁定）**：`beforeAll` 是**整檔**處置；(a) 輪與正式輪的資料一致顯示，移除預熱後那筆一次性成本會落在該檔**當下正在執行的任一條**測試，不固定是候選。rev7 的「三份都恰為指定候選轉紅」對這個機制過嚴。**但既有 `v4-N1`／`v4-N2` 不得用來宣告新判準通過**——那會用同一批觀察同時制定並驗證 Gate；它們保留為「candidate-level Gate 失敗、file-level 診斷證據」（見 Gate A 證據段）。

  **檔案層級判準（每項都要成立）**：
  1. 三份中**至少兩個「計入的命中子程序」**——每個命中須同時符合：`.exit` 非零、唯一 FAIL 位於還原到原始 hash 的那一檔（任一條測試）、訊息逐字 `Test timed out in 5000ms`、`.out` 為 `1 failed | 396 passed`。四者缺一即不算命中。
  2. 保留 (a′) 的另一檔，三份中的**所有**測試都必須通過。
  3. 子程序的唯一失敗若落在 setup、資源失效或**其他檔案**（即使 `.exit` 非零），**只揭露、不算命中也不算反證**；全綠子程序同樣不算命中也不算反證；任何一個子程序出現 2 個以上 FAIL 即該輪無效並停下。
  4. 沿用原 `run3` 三份 launcher（`run3` rc 須為 0）。**任一項少於 2/3 即停止回報，不臨時提高到四份負載。**

  - N1-file-confirm：只還原 `PlanWorkspace.test.ts` 到原始 hash `db31bc91…`（`SpecWorkspace.test.ts` 保留 (a′) golden `8095da7d…`），`run3 v4-N1-file-confirm` → 判準：PlanWorkspace 至少兩個計入的命中子程序；SpecWorkspace 三份全過。
  - N2-file-confirm：Plan 回到 golden `ccd8264a…`、只還原 `SpecWorkspace.test.ts` 到原始 hash `ef218eff…`，`run3 v4-N2-file-confirm` → 判準：SpecWorkspace 至少兩個計入的命中子程序；PlanWorkspace 三份全過。
  - 每項四步：套用前後 hash（`v4-N?-file-confirm-hash-before.txt`）、vitest 能跑、紅在 `Test timed out in 5000ms`（逐字）且位置符合判準、還原後 hash 回 golden。**分母 2/2。**

- [ ] **Step 6: 還原到 golden**（`cp` 自 `v4-golden-*.test.ts` 備份）、`shasum -a 256 -c "$B1B/v4-hash-golden.txt"` 兩檔 OK、全套單跑 ×1（`v4-restore-r2`）397 PASS；hash 時點（套用後／N 還原後／結束時）皆命中 golden；`(cd "$B1B" && shasum -a 256 -c legacy-artifacts-sha256.txt)` 83/83；`git worktree remove --force`；主工作區零異動。

---

## Task 2: 主 repo 落地（Gate B）

- [ ] 依 Step 2 模板逐字套用到主 repo 兩檔；`shasum -a 256` 命中 Task 1 golden（轉移契約：hash 相符＋模板未改＋control 定義未改＋範圍仍為兩檔）。
- [ ] `frontend`：`vitest run` 全套 397 PASS；`npm run build`（`vue-tsc --noEmit && vite build`）exit 0。
- [ ] **提交前**（工作樹）：`git status --short` 只列兩個測試檔為 ` M`；`git diff --name-only <plan-commit>`（不帶 `..HEAD`，含工作樹）只含兩個測試檔。
- [ ] 一個 implementation commit。
- [ ] **提交後**：`git show --stat --format= <implementation-commit>` 只含兩個測試檔；`git diff --name-only 92719fb..HEAD` 只含本 plan＋兩個測試檔（Task 3 文件 commit 之前）。零 `.vue`／零 `.go`／`vitest.config.ts` 未動。

## Task 3: living 文件更新＋backlog rev15（B1 關閉）

- [ ] `wall-clock-test-register.md` v2：B 段兩條就地改「已重現並處置（B1b，`<commit>`）」，附根因一句（模組載入＋首次 `EditorView` 建構兩筆一次性成本；經 `v4-N1-file-confirm`／`v4-N2-file-confirm` 2/2 確認後才寫「已確認」）與修法 (a′)；規則段新增 **D2 裁定的規則 7 全文**（具名 F1／F2 的 FAIL 分類契約）——**此為 living 契約，不因本次 file-confirm pre-merge control 例外放寬**：F1／F2 日後任一 FAIL 仍先分類、契約回歸不得重跑吸收；另於 B 段處置紀錄中**分開**註明「本次 pre-merge negative control 採檔案層級判準（移除預熱後成本落在該檔任一條測試），與規則 7 的具名分類無關」；修訂記錄 v2。
- [ ] backlog rev15：B1b 已完成（plan／implementation commit）；**B1 票整體關閉**；B2 驗收條件 (3) 前置成立；**估點 0.3 → 0.4 pt（D3）**，B 軌 117.05 hr → 11.71 pt、合計 182.05 hr → 18.21 pt（以 hr 推導）；修訂記錄含 preflight 重現數字（名稱錨定值）、control 2/2、三份併發重疊區間、production 零變更。
- [ ] Task 3 一個 commit；Gate B range diff `92719fb..HEAD` 只含 plan＋兩測試檔＋register＋backlog。

---

## Gate A（Task 1 完成條件，隔離 worktree）
- [ ] 基準 397 PASS；(a′) golden hash 記錄，且與 (a) golden、spike hash 三者互異。
- [ ] P：`run3 v4-P-r1`／`v4-P-r2` 回傳 **0**（抄錄 `run3 rc=0`）；**六份 `.exit` 皆 0**、各 397 PASS；`v4-seq-r1..3` 全綠。
- [ ] `v4-N1-file-confirm`、`v4-N2-file-confirm`：`run3` 回傳 **0**；三份中**至少兩個計入的命中子程序**（每個：`.exit` 非零＋唯一 FAIL 在還原檔＋`Test timed out in 5000ms`＋`1 failed | 396 passed`）；保留 (a′) 的另一檔三份全部測試 PASS；非相關失敗或全綠的子程序已逐一揭露且不計入；無任一子程序 ≥2 FAIL；四步齊備，2/2。
- [ ] 既有 `v4-N1`／`v4-N2` 在證據段記為「candidate-level Gate 失敗、file-level 診斷證據」，**未改寫成通過**。
- [ ] 任何 `run3` 非零（2 缺件／3 wait≠exit／4 無重疊）的輪次已標無效、artifact 保留、重跑（新 tag 如 `v4-P-r1b`）並揭露。
- [ ] Step 0 的 `legacy-artifacts-sha256.txt`（83 筆）於正式輪開始前建立、其後未重產；Gate A 結束時 **在 `$B1B` 內**執行 `(cd "$B1B" && shasum -a 256 -c legacy-artifacts-sha256.txt)`（manifest 只含 basename，從其他目錄執行會全部 `No such file`）**83/83 OK**（含 `P-r1-*`、`SPIKE-P-*`、`hash-*.txt`、`run3.sh`），證明舊證據未被覆寫；正式輪與舊輪可逐字對照。
- [ ] 每份併發子程序的 `.pid`／`.start`／`.end`／`.out`／`.err`／`.exit` 與父程序 `-wait.txt` 齊全，overlap 秒數記錄。
- [ ] hash 三時點命中 golden；worktree 移除；主工作區零異動。

## Gate B（主 repo）
- [ ] 兩檔 hash 命中 golden；全套 397 PASS；`npm run build` exit 0。
- [ ] range diff 只含 plan＋兩測試檔（＋Task 3 的 register、backlog）；零 `.vue`／`.go`／config。
- [ ] `git diff --check` 乾淨；停在關票 review，不推送。

---

## Gate A 證據（正式 (a′) 輪，2026-09-04，worktree `b1b-1` @ `92719fb`，證據目錄 `/tmp/b1b.RHppZ0`）

- **Step 0**：manifest 建立前 83 檔、後 84 檔，`legacy-artifacts-sha256.txt` 83 筆（自身 SHA-256 `fdd81c3cdf773217742bbd12aae50502839ea4f5c7c55257eafd444946c420f4`）；正式輪結束時自檢 83/83 OK。
- **Step 1**：兩檔還原原始 hash（`shasum -a 256 -c hash-original.txt` 兩檔 OK）。
- **Step 2**：(a′) golden `v4-hash-golden.txt`：Plan `ccd8264af0e19579d663299afc65fa0a6ebafe6ee55d23d8f1709b276dc22cf3`、Spec `8095da7d946ce4d1a1fe1046fa6d178cfff2fc7bf5a1e8f3bce7e368e8658d87`；diff 兩檔各 +18／−1；golden 備份 `v4-golden-*.test.ts`。
- **Step 3**（`v4-single-*`）：PlanWorkspace 第 1／2／3 條 327／150／127、147／66／49、150／41／57ms；SpecWorkspace 58／105／37、64／114／26、57／106／39ms（只記錄）。
- **Step 4 P**：`run3 v4-P-r1` rc=0（overlap 81.45s）、`v4-P-r2` rc=0（overlap 84.51s）；六份 `.exit` 皆 0、各 397 PASS；F1 2229／2008／2358、2466／1512／2320ms；F2 1855／1286／1560、2109／1658／1576ms。`v4-seq-r1..3`：397 PASS ×3（wall 30／30／29s），F1 588／838／436ms、F2 522／545／628ms。
- **Step 5（rev7 候選層級判準）——不符，記為 candidate-level Gate 失敗、file-level 診斷證據**：
  - `v4-N1`（Plan 還原 `db31bc91…`、Spec golden）：rc=0（overlap 80.77s），三份 `.exit` 皆 1、各 `1 failed | 396 passed`；c2／c3 紅在 **F1**（5773／5108ms timeout），**c1 紅在同檔第二條 `套用草稿只更新編輯器 buffer；儲存才呼叫 PlanWrite`（5018ms timeout），F1 701ms PASS**；SpecWorkspace 三份全過（F2 1603／1564／1207ms）。→ 候選層級 2/3，檔案層級 3/3。
  - `v4-N2`（Plan golden、Spec 還原 `ef218eff…`）：rc=0（overlap 82.02s），`.exit` = 1／1／0；c1 紅在 **F2**（5150ms），**c2 紅在同檔 `scope 外路徑即時提示，送出 disabled`（5907ms），F2 35ms PASS；c3 397 全過（F2 161ms）**；PlanWorkspace 三份全過（F1 2394／1980／1792ms）。→ 候選層級 1/3，檔案層級 2/3。
  - 依 rev7 停下條件中止；owner 裁定改採檔案層級判準並以新 tag 重跑（rev8）。
- **Step 6（中止時）**：兩檔自備份還原、`v4-hash-golden.txt` 兩檔 OK、`v4-restore-r1` 397 PASS；legacy manifest 83/83 OK；證據目錄 195 檔；worktree 保留待 rev8。
- **file-confirm 輪**：（待執行後回填）

## 已知缺口（誠實標註）
1. **CI 上的表現未驗證**：本機 8 核三份併發是人工加壓；CI runner 的 worker 數與 CPU 不同構。B2 建立 CI 後首批跑批若這兩條再紅，依 register 規則分類，不得單獨重跑吸收。
2. **只處理兩條候選**：其他前端測試在三份併發下最慢 1.3s（`binds the draft…`、`驗證錯誤…`），未達 5s，但未逐一評估動態 import 影響；不在本票範圍。
3. **修法依賴 vitest 的 `beforeAll` 先於同檔第一條測試執行**：屬 vitest 文件化語意，本票以 control 實證，不另證明。
4. **preflight 的三份併發（`con-c1..3`）未保存各份 start／end**：重疊區間無法事後證明，只作重現依據；Gate A 一律改用 launcher。
5. **(a′) 預熱後 F1 在三份併發下仍約 2.3s**（spike 觀察），高於同檔其他測試（<1s）；剩餘差額來源未定位，本票不追（不影響 5s 判準，且時間常數不是成功判準），記入 register 觀察欄。

## 尚未完成
- design gate 待審（rev11 短複審）。正式 (a′) 輪 Step 0–4 成立，Step 5 待 file-confirm 重跑，Step 6 待重做；Task 2–3 未執行。rev7 已 commit（`b02d15e`），rev8 起未 commit。

## 修訂記錄
- rev11（2026-09-05，rev10 短複審 CHANGES_REQUIRED）：P2 驗證狀態與 Task 1 前置的「rev9 通過後」統一改為「本 plan design gate 通過後」（不再帶版號）；Task 3 的「不因 rev8 放寬」改為「不因本次 file-confirm pre-merge control 例外放寬」。未修改原始碼、未 commit。
- rev10（2026-09-05，rev9 短複審 CHANGES_REQUIRED）：P1 file-confirm 判準改以「計入的命中子程序」計數——命中須同時符合 `.exit` 非零、唯一 FAIL 位於還原檔、訊息 `Test timed out in 5000ms`、`1 failed | 396 passed`；三份中至少兩個命中；非相關單一失敗與全綠子程序只揭露、不算命中也不算反證；任一子程序 ≥2 FAIL 整輪無效停下。Global Constraints、launcher 註解、Step 5 判準 1／3 與 N1／N2 條目、Gate A 同步。P2 尚未完成段 rev 字樣更正；Architecture 與證據段的「rev8 file-confirm」去版號。未修改原始碼、未 commit。
- rev9（2026-09-05，rev8 短複審 CHANGES_REQUIRED）：P1 Global Constraints 的紅燈語意改為 file-confirm 狹義例外（至少兩份非零；唯一 timeout 可落在還原檔任一條；其他檔案／setup／資源失效不計入；任一份 ≥2 FAIL 即無效停下），並重申不放寬 living 規則 7；環境失效改為直接停下、不降兩份、不升四份；launcher 註解同步。P2 Architecture、未驗證段、Task 1 前置、Task 3 register 措辭改為具名 `v4-N1-file-confirm`／`v4-N2-file-confirm` 與「P 已驗證」。未修改原始碼、未 commit。
- rev8（2026-09-05，正式 (a′) 輪 Step 5 不符後修訂）：P 兩輪、seq 三輪成立並回填；`v4-N1`／`v4-N2` 不符 rev7 候選層級判準（N1 F1 2/3、c1 紅在同檔另一條；N2 F2 1/3、c2 紅在同檔另一條、c3 全綠），依停下條件中止並記為「candidate-level Gate 失敗、file-level 診斷證據」，不改寫成通過。owner 裁定 negative control 改採檔案層級判準：還原檔三份中至少 2 份出現同檔唯一一條 `Test timed out in 5000ms`（每份 `1 failed | 396 passed`）、保留 (a′) 的另一檔三份全過、其他檔案／setup／資源失敗不計入、沿用三份 `run3`、少於 2/3 即停、不提高到四份；以新 tag `v4-N1-file-confirm`／`v4-N2-file-confirm` 各重跑一輪；P／seq 不重跑；完成後再次還原 golden、核 hash、跑一次全套。Task 3 明定 pre-merge control 判準與 living 規則 7 的契約回歸規則分開、不放寬。未修改原始碼、未 commit。
- rev7（2026-09-04，rev6 短複審 CHANGES_REQUIRED）：P1 Gate A manifest 驗證改為 `(cd "$B1B" && shasum -a 256 -c legacy-artifacts-sha256.txt)`（manifest 只含 basename；owner 實證從 `frontend/` 執行為 rc 1 `No such file`、切入 `$B1B` 為 rc 0）；Step 0 開頭補 `test ! -e` 已存在即 exit 1。P2 Task 1 前置與狀態列的舊 rev 字樣改 rev7。未重跑 spike、未修改原始碼、未 commit。
- rev6（2026-09-04，rev5 短複審 CHANGES_REQUIRED）：P1 新增 Task 1 Step 0——正式輪任何 `v4-*` 輸出前，對 `/tmp/b1b.RHppZ0` 現有 83 個檔案建立排序後的 `legacy-artifacts-sha256.txt`（先寫到目錄外再 `mv` 入，避免自含；記建立前 83／後 84 檔；不得重產），Gate A 改為 `shasum -a 256 -c` 該 manifest 83/83 OK。P2 狀態列「rev4 通過後」改「rev6 通過後」；Step 1 還原檢查改為 `shasum -a 256 -c "$B1B/hash-original.txt"`。未重跑 spike、未修改原始碼、未 commit。
- rev5（2026-09-04，rev4 短複審 CHANGES_REQUIRED）：P1 正式輪 artifact 全部明定 `v4-` 前綴（`v4-single-*`／`v4-P-r1`／`v4-P-r2`／`v4-seq-r1..3`／`v4-N1`／`v4-N2`／`v4-hash-golden.txt`），Gate A 同步並新增「既有 `P-r1-*`、`SPIKE-P-*` 完整保留、hash 未變」勾選項。P2 Task 1 標題、前置、未驗證段、N1／N2 措辭統一為 (a′) 與「import＋首次 `EditorView` 建構」；「約 50ms」等預設值移除，時間只記錄。P2 Global Constraints 補 rev4 例外：`B1B=/tmp/b1b.RHppZ0; test -d`，不再 `mktemp`，舊 artifact 不得覆寫或刪除。未重跑 spike、未修改原始碼、未 commit。
- rev4（2026-09-04，Task 1 中止後修訂）：Task 1 以 (a) 執行到 Step 4：`run3 P-r1` 有效（overlap 83.14s）但 c1／c2 F1 timeout（5523／5393ms）、F2 轉綠、SpecWorkspace 第 1 條升至 4.2–4.7s，依停下條件中止、未跑 N1／N2。owner 裁定選項 1，未提交 spike（import＋一次 `EditorView` 建構預熱，不 catch）：單檔 F1 157–162ms、`run3 SPIKE-P` rc=0 三份 397 PASS（F1 2.2–2.4s、F2 1.3–1.5s）；還原到 (a) golden。rev4：Architecture 與根因假設改為「模組載入＋首次 `EditorView` 建構」兩筆成本；D1 改採 (a′) 並保留「正式 P 仍 timeout 即停、再裁定 (b)」；Step 2 模板換成 (a′)，(a) golden 與 spike hash 作廢；Step 1 沿用既有 worktree 與證據目錄、先還原原始 hash、正式 artifact 加 `v4-` 前綴；已知缺口新增 F1 殘餘約 2.3s 未定位。未 commit。
- rev3（2026-09-04，第二輪 owner CHANGES_REQUIRED）：P1 `run3` 改 fail loud——函式內 `export` Node v26 PATH；子 shell 寫完 `.end`／`.exit` 後以 vitest rc `exit`；父層逐一 `wait "$pid"` 並把 wait rc 與 `.exit` 寫入 `-wait.txt`；後置 python 檢查 artifact 齊全（缺件 exit 2）、wait 與 exit 相符（exit 3）、`max(start) < min(end)`（不成立 exit 4），`run3` 回傳非零即該輪無效。Gate A 明列 P 六份 `.exit` 皆 0、N1／N2 三份 `.exit` 皆非零且 `1 failed | 396 passed` 只失敗於指定候選。非阻斷：Architecture 與 beforeAll 註解模板改為「目前假設、待 N1／N2 驗證」語氣。未修改原始碼、未 commit。
- rev2（2026-09-04，第一輪 owner CHANGES_REQUIRED）：P1-1 preflight 表格依測試名稱錨定重抽 artifact，更正 seq-r2／seq-r3／con-c3 三格 F1／F2 寫反；補跑並落檔全套基準 ×1（`base-r1`）與兩檔單跑 ×3（`single-*`），26 個 artifact 加 `SHA256SUMS.txt`；根因改稱「目前假設，待 Task 1 differential control 驗證」，推論項明標。P1-2 新增可逐字執行的 `run3` launcher（每份 PID／`monotonic_ns` start／end／stdout／stderr／exit、父程序 wait），Gate A 明定 `max(start) < min(end)` 才是有效三份併發，P／N1／N2 共用。P1-3 D2 補規則 7 全文：具名 F1／F2 的 FAIL 分類（契約斷言或契約路徑卡死→回歸阻擋；setup／資源失效／他測試中斷→該次無效）。P2 Task 2 scope check 拆為提交前（工作樹 `git status`＋`git diff --name-only <plan-commit>`）與提交後（`git show --stat`＋range diff）。D1 採 (a)、D2 就地更新 B 段、D3 改 0.4 pt（B 軌 11.71、合計 18.21）回寫。本輪未修改任何原始碼、未 commit。
- rev1（2026-09-04）：初稿。preflight 唯讀量測：兩條候選在 HEAD `92719fb` 三份併發下 3/3 重現（6.3–7.7s > 5s），依序全綠；根因定位為 CodeMirror 動態 import 成本落在執行中的測試；提出 (a) 預熱 import 為建議處置，N1／N2 differential control 2/2；估點核對 0.35–0.45 pt 待裁定。主工作區零異動，未 commit。
