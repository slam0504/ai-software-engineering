# B1b 前端兩條 wall-clock 候選重現與處置 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> 版本：rev3（2026-09-04，第二輪 owner CHANGES_REQUIRED 後修訂：一項 P1——`run3` launcher 改為 fail loud（子 shell 帶 Node PATH 並以 vitest rc 結束、父層逐一 `wait "$pid"` 並比對 `.exit`、artifact 缺件或無重疊即非零返回），Gate A 明列 P 三份 exit 0、N1／N2 非零且只失敗於指定候選；非阻斷：Architecture 與註解模板改為「目前假設」語氣；前版：rev2（第一輪 CHANGES_REQUIRED 後修訂：三項 P1——preflight 表格 F1／F2 三格寫反已更正並補齊 artifact、Gate A 三份併發改用可逐字執行的 launcher 與重疊區間判準、register 規則 7 具名 F1／F2 的 FAIL 分類契約——與一項 P2——Task 2 scope check 時機；D1–D3 裁定回寫；前版：rev1）
> 狀態：**design gate 待審（rev3 短複審）——preflight 已完成（唯讀量測，主工作區零異動），兩條候選在現行 HEAD 已重現；尚未修改任何檔案、未 commit**。D1–D3 已裁定，Task 1 待 rev3 複審通過後開始
> 票源：Pre-M4 Readiness Backlog **B1b**（rev14 票面 0.3 pt；**owner 於本 plan gate 裁定改為 0.4 pt**，見 D3）。B1 驗收條件 (4)：「剩餘兩條前端候選須以現行 HEAD 重現——成立者補入 living 文件後併入本票或開續票，不成立者除名（裁決 #9）」
> 基準 commit：**`92719fba41c3402daed44140a280d32a90510c36`**（backlog rev14，已推送、`git ls-remote` 核實相符）
> 前置：B1a aggregate 已關閉（rev14）。本票關票後 **B1 票整體關閉**，B2 驗收條件 (3)「race＋全套測試升 required 前置 B1 完成」的前置成立

**Goal:** 依裁決 #9 對兩條前端候選做出可稽核的處置：兩條**已在現行 HEAD 重現**（preflight 三份併發 3/3），因此走「成立者併入本票」路徑——修掉測試的牆鐘相依（**production 零變更**），以測試側 negative／positive control 證明修正改變了鑑別力，補入 living 文件，關閉 B1。

**Architecture:** 兩條候選的失效形狀相同（同一 `Test timed out in 5000ms`、同在三份併發下出現），**根因目前為同一假設**（CodeMirror 動態 import 成本落在執行中的測試，待 Task 1 N1／N2 確認），因此一起處置。修法在測試檔而非元件：把 CodeMirror 動態 `import()` 的模組載入成本從測試本體移到 `beforeAll`，測試本體只剩它們真正要驗的契約（loading 顯示與輸出累積；換檔後丟棄過期 assist 結果）。**不動 `vitest.config.ts` 的 `testTimeout`**——放寬 timeout 是 B1a 系列明確拒絕的牆鐘式修法。

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

**未驗證（Task 1 要做）**：把 import 成本移出測試本體後，三份併發下兩條是否穩定轉綠；以及移除修法後是否穩定轉紅（differential）。**只有 N1／N2 differential 成立，上述根因假設才升格為已確認。**

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

**owner 裁定：採 (a)**；根因仍需由 Gate A control 最終確認。hook 顯式 `30_000` 而非依賴預設，理由同 B1a-2 #3：它是卡死保險絲、不是成功判準，本 plan 不把「hook 跑得夠快」寫成通過條件。

**D2 living 文件的落法**——**owner 裁定：直接更新 register B 段，不新增 D 段，並補具名 FAIL 分類規則。** B 段兩條由「待重現（B1b）」改為「**已重現並處置**（B1b，commit）」並補根因與修法。規則段新增**規則 7（具名 F1／F2）**：

> 7. **F1 `PlanWorkspace > PlanAssist 送出後草稿區顯示 loading，事件送達後輸出累積` 與 F2 `SpecWorkspace draft accept > discards spec-assist result if the file switches during the call` 自 B1b 處置後，「相同 timeout 可單獨重跑一次判定」的前端規則對這兩條失效**，任何 FAIL 先分類：(i) 命中該測試的契約斷言（F1：`assist-busy` 顯示／`draft-text` 累積／busy 解除；F2：`draft-text` 為空／`accept-draft` disabled）、或可歸因於其契約路徑的卡死（含再次 `Test timed out in 5000ms` 且無環境訊號）→ **回歸，required check 阻擋，不得重跑吸收**；(ii) setup 失敗（掛載／mock 建立）、可證明的資源失效（OOM、worker 啟動失敗）、或其他測試造成的中斷 → **該次無效**，揭露後重跑，不算紅也不算綠。另：前端測試不得把模組動態載入成本留在測試本體。

其他前端測試維持原規則。

**D3 估點核對**——**owner 裁定：改為 0.4 pt**。拆解：preflight（已完成）0.5 hr／Task 1 隔離 worktree 落地＋三組 control 1.0–1.5 hr／Task 2 主 repo 落地＋hash 轉移＋回歸 0.5 hr／Task 3 文件 0.5 hr／gate 往返 1.0–1.5 hr ＝ 3.5–4.5 hr，中位 4.0 hr → **0.40 pt**。連帶（其餘數字不變）：B 軌 116.05 ＋ 1.0 ＝ **117.05 hr → 11.71 pt**；合計 181.05 ＋ 1.0 ＝ **182.05 hr → 18.21 pt**。由 Task 3 的 backlog rev15 落地。

---

## Global Constraints

- 所有 vitest 指令以 `PATH="$HOME/.nvm/versions/node/v26.8.1/bin:$PATH"` 前置，並用 `./node_modules/.bin/vitest`；`node --version` 記入證據。
- **Task 1 一律在隔離 worktree 執行**：`git worktree add --detach /Users/eason_tseng/scratch-worktrees/b1b-1 92719fb`；`frontend/node_modules` 不在 git 內，worktree 需 `cp -R`（或 symlink）主 repo 的 `frontend/node_modules`，複製方式與其後 `vitest run` 首次結果記入證據。主工作區 Task 1 期間零異動。
- 每個工具呼叫以三行前置開頭（`cd` worktree 絕對路徑、`B1B=<mktemp 實際絕對路徑>` 並 `test -d`、HEAD 核對 `92719fb…`），fail loud（沿 B1a-4）。
- 每次 vitest 都保存 stdout（`--reporter=verbose`）、stderr、exit code。**三份併發（P、N1、N2 共用）一律用下列可逐字執行的 launcher**，每份保存 PID、`monotonic_ns` start／end、stdout、stderr、exit，父程序保存 `wait` 結果：

  ```sh
  # 用法：run3 <tag>   （在 worktree 的 frontend/ 目錄執行；$B1B 為證據目錄）
  # 回傳值：0 ＝ launcher 層全部成立（artifact 齊全、每份 wait rc 與 .exit 相符、三份有共同重疊區間）。
  #         非零 ＝ launcher 層失敗（2 缺件／3 wait≠exit／4 無重疊），該輪無效。
  # 注意：各份 vitest 自身的 exit code 不併入回傳值——P 預期三份皆 0，N1／N2 預期三份皆非零，
  #       這兩項由 Gate A 另行核對 .exit 與 .out。
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
- **紅燈語意**：negative control 必須紅在兩條候選的 `Test timed out in 5000ms`（逐字抄錄）；其他測試若在三份併發下失敗，依 B1a-4 D1 分類並揭露，不吸收。
- **時間常數不得成為成功判準**：候選在修法後的耗時只作觀察記錄（預期由約 400ms 降至約 50ms），不設門檻。
- 三份併發若因環境失效（OOM、worker 啟動失敗）→ 該輪無效、揭露、降為兩份 ×2 輪（沿 B1a-4 M3 降級規則）。

---

## Task 1: 隔離 worktree 落地 (a) ＋ 三組 control（不 commit）

**前置：D1 裁定為 (a)。**

- [ ] **Step 1: worktree 與環境**：建 worktree、複製 `frontend/node_modules`、`mktemp -d /tmp/b1b.XXXXXX`、記 `node --version`／`vitest --version`／HEAD；**基準**：全套單跑 ×1 → 預期 397 PASS；記兩檔原始 SHA-256（`shasum -a 256 frontend/src/components/PlanWorkspace.test.ts frontend/src/components/SpecWorkspace.test.ts`）。
- [ ] **Step 2: 套用模板（兩檔各一段，放在既有 `describe` 內第一個 `it` 之前；import 行併入既有 `vitest` import）**

```ts
import { beforeAll, describe, expect, it, vi } from 'vitest'   // 依各檔既有 import 只補 beforeAll
```

```ts
  // CodeMirror 由元件 onMounted 內動態 import。B1b preflight 觀察：本檔一條測試在
  // 全套並行下約 2s、三份併發下約 7s > 5s 預設 timeout，其餘測試僅數十 ms；差額與
  // 模組載入成本量級一致（假設由 B1b Task 1 的 differential control 驗證）。先在
  // hook 內載入一次，讓測試本體只量契約、不量模組載入。30s 是卡死保險絲，不是成功判準。
  beforeAll(async () => {
    await Promise.all([import('codemirror'), import('@codemirror/state')])
  }, 30_000)
```

  套用後 `git diff --stat` 預期兩檔各 +8 行左右（含 import 補字）；記 **golden SHA-256**（兩檔）。

- [ ] **Step 3: 單檔觀察**：兩檔各獨跑 ×3 `--reporter=verbose`，記錄前三條耗時（預期候選降到與鄰近測試同量級，約 25–60ms；只記錄不設門檻）。
- [ ] **Step 4: positive control（P）**：`run3 P-r1`、`run3 P-r2`（兩輪 `run3` rc 皆須為 0；且六份 `.exit` 皆 0）→ 預期六份全部 397 PASS，F1／F2 耗時（名稱錨定抽取）記錄；另全套依序 ×3（`seq-r1..3`）→ 397 PASS ×3。
- [ ] **Step 5: negative control（N1、N2，differential）**：
  - N1：只還原 `PlanWorkspace.test.ts` 到原始 hash（`SpecWorkspace.test.ts` 保留修法），`run3 N1`（`run3` rc 須為 0；三份 `.exit` 皆非零）→ 預期三份都**只有 F1** `Test timed out in 5000ms`（`1 failed | 396 passed`），F2 PASS。
  - N2：只還原 `SpecWorkspace.test.ts`（PlanWorkspace 保留修法），`run3 N2`（`run3` rc 須為 0；三份 `.exit` 皆非零）→ 預期三份都**只有 F2** timeout（`1 failed | 396 passed`），F1 PASS。
  - 每項四步：套用前後 hash、編譯／載入正常（vitest 能跑）、紅在指定訊息（逐字）、還原後 hash 回 golden。**分母 2/2（N1、N2）。** 若三份併發下某輪未轉紅（負載不足），加到四份併發重試一次並揭露；仍不紅則本票停下回報（根因假設需重審）。
- [ ] **Step 6: 還原到 golden**、全套單跑 ×1 回綠、hash 三時點（套用後／N 還原後／結束時）皆命中 golden；`git worktree remove --force`；主工作區零異動。

---

## Task 2: 主 repo 落地（Gate B）

- [ ] 依 Step 2 模板逐字套用到主 repo 兩檔；`shasum -a 256` 命中 Task 1 golden（轉移契約：hash 相符＋模板未改＋control 定義未改＋範圍仍為兩檔）。
- [ ] `frontend`：`vitest run` 全套 397 PASS；`npm run build`（`vue-tsc --noEmit && vite build`）exit 0。
- [ ] **提交前**（工作樹）：`git status --short` 只列兩個測試檔為 ` M`；`git diff --name-only <plan-commit>`（不帶 `..HEAD`，含工作樹）只含兩個測試檔。
- [ ] 一個 implementation commit。
- [ ] **提交後**：`git show --stat --format= <implementation-commit>` 只含兩個測試檔；`git diff --name-only 92719fb..HEAD` 只含本 plan＋兩個測試檔（Task 3 文件 commit 之前）。零 `.vue`／零 `.go`／`vitest.config.ts` 未動。

## Task 3: living 文件更新＋backlog rev15（B1 關閉）

- [ ] `wall-clock-test-register.md` v2：B 段兩條就地改「已重現並處置（B1b，`<commit>`）」，附根因一句（經 N1／N2 確認後才寫「已確認」）與修法；規則段新增 **D2 裁定的規則 7 全文**（具名 F1／F2 的 FAIL 分類契約）；修訂記錄 v2。
- [ ] backlog rev15：B1b 已完成（plan／implementation commit）；**B1 票整體關閉**；B2 驗收條件 (3) 前置成立；**估點 0.3 → 0.4 pt（D3）**，B 軌 117.05 hr → 11.71 pt、合計 182.05 hr → 18.21 pt（以 hr 推導）；修訂記錄含 preflight 重現數字（名稱錨定值）、control 2/2、三份併發重疊區間、production 零變更。
- [ ] Task 3 一個 commit；Gate B range diff `92719fb..HEAD` 只含 plan＋兩測試檔＋register＋backlog。

---

## Gate A（Task 1 完成條件，隔離 worktree）
- [ ] 基準 397 PASS；golden hash 記錄。
- [ ] P：`run3 P-r1`／`P-r2` 回傳 **0**（抄錄 `run3 rc=0`）；**六份 `.exit` 皆 0**、各 397 PASS；依序 ×3 全綠。
- [ ] N1、N2：`run3` 回傳 **0**；**三份 `.exit` 皆非零**，且每份 `.out` 為 `1 failed | 396 passed`、失敗者恰為指定候選並紅在 `Test timed out in 5000ms`、另一條候選 PASS；四步齊備，2/2。
- [ ] 任何 `run3` 非零（2 缺件／3 wait≠exit／4 無重疊）的輪次已標無效、artifact 保留、重跑並揭露。
- [ ] 每份併發子程序的 `.pid`／`.start`／`.end`／`.out`／`.err`／`.exit` 與父程序 `-wait.txt` 齊全，overlap 秒數記錄。
- [ ] hash 三時點命中 golden；worktree 移除；主工作區零異動。

## Gate B（主 repo）
- [ ] 兩檔 hash 命中 golden；全套 397 PASS；`npm run build` exit 0。
- [ ] range diff 只含 plan＋兩測試檔（＋Task 3 的 register、backlog）；零 `.vue`／`.go`／config。
- [ ] `git diff --check` 乾淨；停在關票 review，不推送。

---

## 已知缺口（誠實標註）
1. **CI 上的表現未驗證**：本機 8 核三份併發是人工加壓；CI runner 的 worker 數與 CPU 不同構。B2 建立 CI 後首批跑批若這兩條再紅，依 register 規則分類，不得單獨重跑吸收。
2. **只處理兩條候選**：其他前端測試在三份併發下最慢 1.3s（`binds the draft…`、`驗證錯誤…`），未達 5s，但未逐一評估動態 import 影響；不在本票範圍。
3. **修法依賴 vitest 的 `beforeAll` 先於同檔第一條測試執行**：屬 vitest 文件化語意，本票以 control 實證，不另證明。
4. **preflight 的三份併發（`con-c1..3`）未保存各份 start／end**：重疊區間無法事後證明，只作重現依據；Gate A 一律改用 launcher。

## 尚未完成
- design gate 待審（rev3 短複審；D1–D3 已裁定）。Task 1–3 未執行。本 plan 未 commit。

## 修訂記錄
- rev3（2026-09-04，第二輪 owner CHANGES_REQUIRED）：P1 `run3` 改 fail loud——函式內 `export` Node v26 PATH；子 shell 寫完 `.end`／`.exit` 後以 vitest rc `exit`；父層逐一 `wait "$pid"` 並把 wait rc 與 `.exit` 寫入 `-wait.txt`；後置 python 檢查 artifact 齊全（缺件 exit 2）、wait 與 exit 相符（exit 3）、`max(start) < min(end)`（不成立 exit 4），`run3` 回傳非零即該輪無效。Gate A 明列 P 六份 `.exit` 皆 0、N1／N2 三份 `.exit` 皆非零且 `1 failed | 396 passed` 只失敗於指定候選。非阻斷：Architecture 與 beforeAll 註解模板改為「目前假設、待 N1／N2 驗證」語氣。未修改原始碼、未 commit。
- rev2（2026-09-04，第一輪 owner CHANGES_REQUIRED）：P1-1 preflight 表格依測試名稱錨定重抽 artifact，更正 seq-r2／seq-r3／con-c3 三格 F1／F2 寫反；補跑並落檔全套基準 ×1（`base-r1`）與兩檔單跑 ×3（`single-*`），26 個 artifact 加 `SHA256SUMS.txt`；根因改稱「目前假設，待 Task 1 differential control 驗證」，推論項明標。P1-2 新增可逐字執行的 `run3` launcher（每份 PID／`monotonic_ns` start／end／stdout／stderr／exit、父程序 wait），Gate A 明定 `max(start) < min(end)` 才是有效三份併發，P／N1／N2 共用。P1-3 D2 補規則 7 全文：具名 F1／F2 的 FAIL 分類（契約斷言或契約路徑卡死→回歸阻擋；setup／資源失效／他測試中斷→該次無效）。P2 Task 2 scope check 拆為提交前（工作樹 `git status`＋`git diff --name-only <plan-commit>`）與提交後（`git show --stat`＋range diff）。D1 採 (a)、D2 就地更新 B 段、D3 改 0.4 pt（B 軌 11.71、合計 18.21）回寫。本輪未修改任何原始碼、未 commit。
- rev1（2026-09-04）：初稿。preflight 唯讀量測：兩條候選在 HEAD `92719fb` 三份併發下 3/3 重現（6.3–7.7s > 5s），依序全綠；根因定位為 CodeMirror 動態 import 成本落在執行中的測試；提出 (a) 預熱 import 為建議處置，N1／N2 differential control 2/2；估點核對 0.35–0.45 pt 待裁定。主工作區零異動，未 commit。
