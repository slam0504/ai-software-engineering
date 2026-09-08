# Wall-clock 測試有效名單（living）

> 版本：v12（2026-09-08，**來源索引收斂**：改引用 `b2b-2/review-20260908T175707/`（`index.json`＋五筆 jobs JSON 與日誌，11/11 OK）——列出**完整 HEAD**、**完整六路徑 hash**、**四 job 起訖**與**各 job 的 image 版本原文**（labels 不代替 image 版本）；規則 5 的「測試耗時 n=1」限定為 `#2`／`#3`／`#6`，**F1／F2 各 n=2**；樣本 1–3 原採集檔的措辭改為「目前無法調閱，清除時點與原因未知；部分資料已由 GitHub 重新取得」）；前版：v11（2026-09-08，**A-1 補正**：樣本 4 的 `go` **job 耗時 95 s（failure，提前中止）**有紀錄、缺的只是測試耗時，A1a 組 job 統計改為 95／185.5／276 s（n=2，含提前中止）、#2／#3／#6 維持 n=1；規則 5 回填為「B2b-2 已完成 CI 分布量測、指向 A-1、冷啟動仍未驗證」；新增**逐筆來源索引**

資料來源：`~/.local/share/sdlc-evidence/b2b-2/review-20260908T175707/`（`index.json` ＋ 五筆 `<run>.jobs.json` 與 `<run>.log`，manifest **11 檔、11/11 OK**）。該目錄的 `provenance` 自述為「**Fresh read-only GitHub retrieval and local Git object lookup; not restoration of the lost original archive. No CI rerun or dispatch. Backup not verified.**」——即**本次重新取得**，非原封存恢復，備份未確認。

**六路徑指紋（完整 hash）**——五筆的 `internal`＝`e1d3e323079c5dd1b5d4543ac1418d183a0a51e3`、`testdata`＝`8cc2ff003ade14578e40d3db1054a3e36ade6878`、`go.mod`＝`83f5ffb9ffd23354cae6625c01ae506235d9c2c1`、`go.sum`＝`4ae874109feda96f181d689401b24331a2d7d205`、`ci.yml`＝`4efaf169f4b76c7b30792a3af6b06e498da80891` 皆相同；差異只在 `frontend`：

| 分組 | `frontend` tree hash |
|---|---|
| 舊組（樣本 1–3，同 main `1bbb47a`） | `c98b36dfdac28e229acc70913f465b377c7155a1` |
| A1a 組（樣本 4–5） | `278a5bf841dc968e1f73fd9d4129de57f6da5907` |

**完整 HEAD 與四 job 起訖（UTC）**

| # | run | 完整 HEAD | frontend | checksums | go | wails-build |
|---|---|---|---|---|---|---|
| 1 | `34075935919` | `5a83717b2b20cd709144e479abebead01455ff5b` | 02:20:02→02:20:53 | 02:20:02→02:20:10 | 02:20:57→02:26:16 | 02:20:56→02:23:28 |
| 2 | `34077383674` | `57b2448ab24be6694cfcfa7726b391a2c271c493` | 02:46:15→02:47:04 | 02:46:15→02:46:20 | 02:47:07→02:52:42 | 02:47:09→02:51:37 |
| 3 | `34080497639` | `ea5123aceec42634b11e6dda069ce9fd2501768d` | 03:41:33→03:42:32 | 03:42:08→03:42:14 | 03:42:35→03:48:32 | 03:42:36→03:45:45 |
| 4 | `34202299786` | `f2a5ebbf395ab58d6586fbddda7929c44ee9ec21` | 08:01:43→08:02:42 | 08:01:43→08:01:47 | 08:02:46→08:04:21（**failure**） | 08:02:45→08:06:12 |
| 5 | `34209630775` | `c549d45e9abe070253274e496e901a8b502d6381` | 09:22:29→09:23:16 | 09:22:29→09:22:33 | 09:23:20→09:27:56 | 09:23:20→09:27:11 |

（樣本 1–3 為 2026-09-07，樣本 4–5 為 2026-09-08。）

**runner image 版本（取自各 job `Set up job` 的日誌原文，非 labels）**——五筆完全一致：

| job | image | image 版本 | runner 版本 |
|---|---|---|---|
| frontend／checksums | `ubuntu-24.04` | `20260831.293.1` | `20260828.587` |
| go／wails-build | `macos-15` | `20260824.0482.1` | `20260819.586` |

**artifact ID**：樣本 1 go-test-json `10002143591`／vitest-output `10002042695`；樣本 2 `10002641420`／`10002541534`；樣本 3 `10003637798`／`10003532624`；樣本 4 wails-app-tar `10046441441`／frontend-dist `10046324117`／vitest-output `10046323042`（**無 go-test-json**）；樣本 5 go-test-json `10049410730`／wails-app-tar `10049385807`／frontend-dist `10049253502`／vitest-output `10049252638`。樣本 1–3 的 artifact ID 出自 `docs/superpowers/plans/2026-09-08-b2b-ruleset-enforcement.md` 的 D6 樣本表與 Task 4 勾選項，樣本 3 另見本機工作日誌 `.remember/now.md` 段落「## 2026-09-07 11:52 B2b-2 候選樣本 3 採集（PR #3 head ea5123a）」。

**證據涵蓋範圍**：`b2b-2/samples/` 的 manifest（21 檔）只涵蓋樣本 4／5 的 run／jobs／artifacts 與解析輸出；五筆的 HEAD、指紋、job 起訖與 image 版本由上述 `review-20260908T175707/` 的 11 檔支持。**樣本 1–3 的原本機採集檔（原 `/tmp/b2b-t1/samples/`）目前無法調閱，清除時點與原因未知；部分資料已由 GitHub 重新取得**（即本索引），逐測試耗時（#2／#3／#6、F1／F2）與 npm cache 狀態仍依既有文件與工作日誌。


**邊界**：五筆皆 npm cache hit，**不得稱冷啟動**；本表為 CI 實測，不外推至本機，也不反推本機餘裕（規則 4 不變）。


## B. 前端兩條（B1b 已重現並處置）

| # | 測試（檔案） | 狀態 | commit | 根因（已確認） | 修法 | 重驗 |
|---|---|---|---|---|---|---|
| F1 | `PlanWorkspace > PlanAssist 送出後草稿區顯示 loading，事件送達後輸出累積`（`frontend/src/components/PlanWorkspace.test.ts`） | **resolved**（B1b，2026-09-05） | `8aee222` | CodeMirror 模組動態載入＋jsdom 首次建構 `EditorView` 的一次性成本，落在該檔當下執行的測試；三份併發全套下超過 vitest 預設 5000ms（preflight 三份 3/3 重現，6.3–7.7s） | 測試檔 `beforeAll` 內預先 `import('codemirror')`／`import('@codemirror/state')` 並建構一次即銷毀的 `EditorView`；production 與 `vitest.config.ts` 零變更 | B1b Gate A：三份併發 P 兩輪六份 397 PASS（F1 1.5–2.5s）；`v4-N1-file-confirm` 2/3 |
| F2 | `SpecWorkspace draft accept > discards spec-assist result if the file switches during the call`（`frontend/src/components/SpecWorkspace.test.ts`） | **resolved**（B1b，2026-09-05） | `8aee222` | 同上（同一機制，成本落點依執行順序不同） | 同上 | 同上（F2 1.3–2.1s）；`v4-N2-file-confirm` 3/3 |

來源：2026-08-21 session 首錄、2026-08-25 更新；B1b（plan `docs/superpowers/plans/2026-09-04-b1b-frontend-wallclock-candidates.md`）於 HEAD `92719fb` 重現後併入處置。**處置紀錄註記**：本次 pre-merge negative control 採**檔案層級**判準（移除預熱後，一次性成本落在該檔任一條測試，非固定為候選；owner 於 plan rev8 裁定），此判準只用於 B1b 的 pre-merge 驗證，**與規則 7 的具名 FAIL 分類無關、不放寬之**。

**前端一般規則（owner 凍結，適用於 F1／F2 以外的前端測試）**：完整套件碰到**相同 timeout** 可單獨重跑一次判定，但**仍須揭露**；單獨重跑失敗、或失敗形狀改變（非 timeout），視為真正失敗。新候選須以現行 HEAD 重現並附證據才可補入本文件；不成立者除名並在修訂記錄留痕。


## B-1. CM6／jsdom 隔離執行候選（v8 新增，A1a-1；**候選，非名單成員**）

| # | 條目 | 狀態 | 重現指令 | 受測 SHA | 失敗形狀 | 重現計數 |
|---|---|---|---|---|---|---|
| C1 | `T8b-S：A→B→A——先前 A 回應延遲，經過 B 後回到 A，該延遲回應仍須丟棄`（`frontend/src/components/SpecWorkspace.test.ts`） | **候選**（待處置） | `npx vitest run src/components/SpecWorkspace.test.ts src/components/PlanWorkspace.test.ts -t "T8b-S"` | `2bd48902829899b4819560a2288c8ad1023a5346` | (i) 測試自身 fail-loud：`Error: CM6 view 未在 jsdom 下成功掛載——這是環境前置條件失敗，不是行為證據，應先修好再重跑`；(ii) `Test timed out in 5000ms` | 現行 HEAD **2/4 重現**（2026-09-07 16:2x）；同命令緊接著 4/4 未重現 → **間歇。原因未確認**（機器負載只是推測，未經實驗證實，不得寫成已確認原因） |

**範圍**：A1a-1 於 `SpecWorkspace.test.ts`／`PlanWorkspace.test.ts` 新增的 CM6 相關測試（T1–T14 系列）皆可能落入同一機制；本表目前只具名有現行 HEAD 重現證據的 `T8b-S`，其餘依規則 3 不得直接視為名單成員。

**登記限制（依 owner 2026-09-07 裁定）**：

1. **不加入可重跑名單**——不適用前端一般規則的「相同 timeout 可單獨重跑一次判定」。
2. **不併入 F1／F2**，規則 7 對本候選不適用（機制相近但條目不同，F1／F2 已 resolved）。
3. **全檔／全套的通過只能寫「這些批次未重現」**，不得寫成「已證明穩定」：實測 `SpecWorkspace`＋`PlanWorkspace` 全檔連跑 3 次皆 55/55、`PlanWorkspace` 單檔 3 次皆 37/37、全套 3 次皆 429/429——**這些批次未重現**。
4. **待補**：現行 HEAD 的失敗**原文**尚未擷取（2/4 那次只記錄了訊息計數，未保存輸出）；失敗形狀取自 A1a-1 mutation 的紅燈日誌（較早 SHA）。補齊前本條維持「候選（待處置）」，不得升格。
5. **（v9 補記，2026-09-08）原始失敗輸出已無法調閱**：可供補齊原文的來源——`/tmp/a1a-1/evidence/isolated-flaky-at-HEAD.txt` 與 A1a-1 mutation 紅燈日誌——已隨 `/tmp/a1a-1`（120 檔）整個遺失，清除時點與原因未知。owner 另查一份歷史 session，找到本條的登記內容，**未找到對應的原始失敗輸出**。部分回收僅有指令與重現計數（`~/.local/share/sdlc-evidence/a1a-1/2bd4890/recovered-from-session/isolated-flaky-measurement.md`，擷取自 session `df399e66-649a-4a05-b0aa-506259b0487f`、約 2026-09-07 16:26–16:32），**不以摘要代替原文**。
   **兩種缺失要分開**：(a) 現行 HEAD `2bd4890` 那次 2/4 重現**當時就未保存失敗原文**（只記了訊息計數），屬「從未產生」；(b) 較早 SHA 的 mutation 紅燈日誌**曾保存、後隨封存遺失**，屬「保存後遺失」。兩者都不能用摘要補足。owner 2026-09-08 裁定**不授權重新重現**，本條維持「候選（待處置）、原文待補」。

**與 A1a-1 mutation 證據的關係**：mutation 的紅在正題判定已排除本形狀——分類器對「CM6 未掛載／逾時」一律判 `ENV_FAIL`，不計為紅在正題；38 份紅燈日誌經複核皆為目標測試的斷言失敗。惟**多數紅燈日誌仍含 `getClientRects is not a function` 的 jsdom 量測 stderr 雜訊**（38 份中 **36 份**；不含的兩份是 `MU-nav-resubmit`／`MU-nav-filetree`，測 `App.test.ts`、不掛載 CM6），該雜訊不是失敗原因。

**與 B2b-2 回填的關係**：B2b-2 的 register 回填（#2／#3／#6、F1／F2、規則 5）採用**回填當時的最新版本號**，與本段 C1 並存，**不得覆蓋或除名 C1**；C1 的狀態轉換只能由其自身的處置票決定。


## C. 規則

1. **A 段 #1–#6 的 FAIL 先分類，契約回歸不得重跑吸收**。在 `-race`、套件併行或負載下任一條 FAIL，先依 B1a-4 plan D1 分類：**命中該測試的契約／oracle 斷言、或 goroutine dump 可歸因於其契約路徑的卡死（panic／`-timeout`）→ 契約回歸**，不得以「先單獨重跑再判定」吸收，§7 的舊規則對這六條自 2026-09-04 起失效；**命中 setup／前提校驗、可證明的資源失效、或可歸因於其他測試的 panic／`-timeout` → 該次無效**，揭露後可在調整負載後重跑，不算紅也不算綠。
2. **綠燈仍不是修正的通過證據**。六條全綠只證明「未重現」，任何修正的通過證據須來自其對應施工票的 mutation／negative control。
3. **新候選登記條件**：須以現行 HEAD 重現（含失敗輸出、重現指令、HEAD SHA），登記時標「候選」，不得直接視為名單成員；處置完成後改「resolved」或「no-change disposition」並附 commit（no-change 不得虛構 commit）。
4. **本機餘裕觀察（不外推至 CI）**：B1a-4 矩陣 M3 三份併發下，#2 最長 8.02s／預算 15s（約 1.9 倍）、#6 最長 9.3s／deadline 20s（約 2.2 倍），遠低於單跑時的約 30 倍。此為本機 8 核觀察，CI runner 若較弱，這兩條最先逼近預算。
5. **CI 耗時分布已由 B2b-2 量測（v10／v11 回填，取代原「歸屬 B2、待量測」的表述）**：#2／#3／#6 於 CI（`macos-15-intel`）的實測 Elapsed 與四個 job 的耗時分布見 **A-1 段**；依 D6 分為舊組 n=3 與 A1a 組；A1a 組的 **job 耗時 n=2**（含樣本 4 提前中止），**`#2`／`#3`／`#6` 的測試耗時 n=1**（樣本 4 未執行測試），**F1／F2 仍各 n=2**（前端測試在樣本 4 有正常執行）。兩組不混算。**冷啟動仍未驗證**——五筆樣本的 npm cache 皆為 hit（`node-cache-Linux-x64-npm-9dcd67fe…`），依 D6 不得稱冷啟動；B1a 的本機量測與本表不互相外推（規則 4 不變）。
6. **#6 的自然誤紅從未在本機重現**：現有證據是 B1a-3 的人工延遲證明機制與 B1a-4 負載下 27/27 全綠，結論僅為「本機負載下未重現」。
7. **F1 `PlanWorkspace > PlanAssist 送出後草稿區顯示 loading，事件送達後輸出累積` 與 F2 `SpecWorkspace draft accept > discards spec-assist result if the file switches during the call` 自 B1b 處置後，「相同 timeout 可單獨重跑一次判定」的前端規則對這兩條失效**，任何 FAIL 先分類：(i) 命中該測試的契約斷言（F1：`assist-busy` 顯示／`draft-text` 累積／busy 解除；F2：`draft-text` 為空／`accept-draft` disabled）、或可歸因於其契約路徑的卡死（含再次 `Test timed out in 5000ms` 且無環境訊號）→ **回歸，required check 阻擋，不得重跑吸收**；(ii) setup 失敗（掛載／mock 建立）、可證明的資源失效（OOM、worker 啟動失敗）、或其他測試造成的中斷 → **該次無效**，揭露後重跑，不算紅也不算綠。另：前端測試不得把模組動態載入或首次建構重型元件的成本留在測試本體。**此規則不受 B1b pre-merge 檔案層級 control 例外影響。**

8. **unresolved 狀態語意（v5 新增，#7；v6 同版本補記擴充；v7 起 #7 已 resolved，本規則保留給其他 CI-only 候選）**：CI 已重現，且（a）機制或責任邊界未定，**或（b）責任邊界與修法已裁定、但實作或驗證尚未完成**的條目。不是 resolved 也不是 no-change disposition，不得除名或改寫為誤紅。**#7 的兩種已登記形狀（v7 起為歷史；修法後在 B2c-7 run `34039387868` 四 runner ×100 未再出現；若在 B2c-5／B2c-6 之後的 HEAD 再度出現，依規則 3 以現行 HEAD 重現並登記為新候選，不得直接沿用 #7）**——(i) macOS 於 5 秒 guard 命中 `session_test.go:207: drain/Wait hung on orphan-held pipes`（EOF 卡死）、(ii) ubuntu 於 drain／Wait 返回後立即命中 `session_test.go:210: orphan must be reaped by supervisor on parent exit`（oracle 即時失敗）——才視為 #7 的已知未解決項；**其他訊息、panic、data race、`-timeout`、setup／環境問題仍須依規則 1 所定的分類方式另行分類**（命中契約／oracle 斷言或可歸因於契約路徑的卡死 → 契約回歸；setup／資源失效／他測試造成 → 該次無效），不得歸入 #7；此為 #7 的分類契約。已知形狀亦不得以 retry、放寬 guard 或跳過吸收（處置前該 job 維持紅燈語意）；處置路徑與狀態轉換由 backlog 續票決定（v5 當時為 B2c-3／B2c-4；**v6 補記後為 B2c-5／B2c-6／B2c-7**），轉為 resolved／no-change 時須附 commit 或裁定記錄；#7 轉 resolved 的完整條件見 B2c-4 裁定記錄 §5（exact implementation SHA、artifact 完整性、每條核心／escalation 測試各 400/400、零 invalid／setup／race／timeout、v7 落地）。

## 修訂記錄

- v12（2026-09-08）：來源索引收斂三處——(1) 改引用 owner 重新取得的 `~/.local/share/sdlc-evidence/b2b-2/review-20260908T175707/`（`index.json`＋五筆 `<run>.jobs.json` 與 `<run>.log`，manifest 11/11 OK），列出五筆**完整 HEAD**、**完整六路徑 tree hash**、**四 job 起訖（UTC）**與**各 job `Set up job` 日誌原文的 image／runner 版本**（`ubuntu-24.04` `20260831.293.1`／`macos-15` `20260824.0482.1`，五筆一致）；runner labels 不再代替 image 版本；樣本 1–3 亦補上 job 起訖。(2) 規則 5 的「測試耗時 n=1」明確限定為 `#2`／`#3`／`#6`，**F1／F2 各 n=2**（樣本 4 的前端測試正常執行）。(3) 樣本 1–3 原本機採集檔的措辭改為「**目前無法調閱，清除時點與原因未知；部分資料已由 GitHub 重新取得**」，不再寫成「與 A1a 封存一同遺失」。入樣、分組與統計數值不變。

- v11（2026-09-08）：A-1 補正三處——(1) 樣本 4 的 `go` **job 耗時有紀錄**（95 s、08:02:46Z→08:04:21Z、failure），先前誤填「缺」；缺的是**測試耗時**（#2／#3／#6）。A1a 組 job 耗時統計改為 95／185.5／276 s（n=2，註明含提前中止、不得解讀為完整測試流程），測試耗時維持有效 n=1、不補零。(2) 規則 5 由「歸屬 B2、待量測」回填為「B2b-2 已完成本次 CI 分布量測，指向 A-1；**冷啟動仍未驗證**（五筆 cache 皆 hit）」。(3) 新增**逐筆來源索引**：六路徑指紋、head／base、四 job 起訖與 runner labels、artifact ID 與各自來源；樣本 3 定位到 `.remember/now.md` 的具名段落；並標明樣本證據 manifest（21 檔）**只涵蓋樣本 4／5**，樣本 1–3 由既有文件與工作日誌支持、原始採集檔已遺失。

- v10（2026-09-08）：新增 **A-1 段（CI 耗時量測，B2b-2 五筆樣本）**——D5 入樣、D6 分組：舊組 n=3（指紋同 main `1bbb47a`）、A1a 組 n=2（`frontend`＝`278a5bf…`，其餘五路徑同 main）；兩組不混算。樣本 4（`34202299786`）的 `go` job 於 gofmt 檢查中止，build／vet／test 未執行，**go 耗時與 #2／#3／#6 記「缺」不補零**，A1a 組該類指標有效 n=1。五筆 `ci.yml` SHA-256 一致且與 main 相同、npm cache 皆 hit（不稱冷啟動）。A 段「最後一次負載重驗」以本表回寫。C1 與 B-1 段其餘限制不變。

- v9.1（2026-09-08）：C1 第 5 項精度修正——區分「2/4 那次**未保存**失敗原文（從未產生）」與「較早 mutation 紅燈日誌**保存後遺失**」，並補部分回收片段的 session ID 與約略時點。狀態不變（候選、原文待補）。
- v9（2026-09-08）：B-1 段 C1 補第 5 項限制——原始失敗輸出已隨 `/tmp/a1a-1` 遺失、無法調閱；部分回收僅存指令與重現計數，不得代替原文；不授權重新重現，維持「候選（待處置）、原文待補」。其餘條目不變。

- v8（2026-09-07，A1a-1）：新增 **B-1 段「CM6／jsdom 隔離執行候選」**，具名條目 C1（`T8b-S`）於現行 HEAD `2bd4890` 2/4 重現、緊接 4/4 未重現；明列四項登記限制（不入可重跑名單、不併入 F1／F2、全檔通過只寫「未重現」、失敗原文待補）。A 段與 B 段內容不變。

- v7（2026-09-07）：B2c-7 CI 驗證通過，#7 **unresolved → resolved**——commit 欄填 B2c-5 `b2efb1c`／`b9c74e8` 與 B2c-6 `1b5e54c`／`dad85cf`，修正方式欄改「已落地」，負載重驗欄填 run `34039387868`（base `852c287`、head `a679fcd`、四 runner × 四測試各 100/100、零 invalid／setup／race／timeout、manifest 全 OK）並列證據限制；規則 8 的兩種 #7 形狀轉為歷史、規則本身保留；版本摘要、更新責任、A 段標題同步。B2a 解除 blocked 的條件（#7 resolved 且 v7 落地）自本版成立，PR #1 rebase 後仍須重新完成 Gate A。
- v6 同版本補記（2026-09-07，B2c-4 APPROVED）：#7 維持 unresolved，但原因改為「責任邊界與修法已裁定（production 契約缺口→B2c-5 supervisor 有界清理；oracle 取樣→B2c-6 有界化），實作與 B2c-7 驗證尚未完成」；修正方式欄改列已裁定修法；現行後續改指 B2c-5／B2c-6／B2c-7（B2c-3／B2c-4 作歷史保留）；規則 8 擴充為包含「已裁定修法尚未完成實作或驗證」並附 #7 轉 resolved 的完整條件；版本摘要、更新責任、A 段標題同步。版本號不變，v7 留給 B2c-7。
- v6 勘誤（2026-09-07，B2c-4 決策 gate）：#7 row 的 EPERM 語意補正——建立中的 `P_REF_NEW` 成員同樣導致 EPERM，EPERM 不能當作群組已消失；版本號不變。
- v6（2026-09-07）：B2c-3 結果回寫 #7——狀態維持 **unresolved**，補機制欄：macOS CI production 順序 36–54% 重現（形狀 pair 為主）、群組約 30 s 後消失已直接觀察、機制解讀定位為 fork 窗口（XNU `killpg1` 對建立中成員靜默略過；原始碼一致性對照、送達瞬間未直接觀察；8 個 `observedBeforeReturn` 分開保留）、經身分驗證的第二次群組 KILL 於探針條件下 109／109 清除、兩平台 `kill(-pgid,0)` 語意差異、Linux 0／400；責任邊界與修法方向交 B2c-4。規則 8 的兩種已登記形狀與分類契約不變。完整證據見 `orphan-timeout-diagnosis-record.md` v2 §7。
- v5（2026-09-06）：B2c-2 round 2 結果回寫 #7——狀態 candidate → **unresolved**；macOS 於 production cleanup path＋真實 fixture 時序下確認重現、`claude.Session` 非必要、SIGKILL 成功後成員存活原因未定位、責任邊界待裁定；ubuntu 為 oracle timing race 支持性證據、殘存者狀態未觀察；不判定 production defect 亦不排除。後續 B2c-3／B2c-4。完整證據與 manifest 見 `orphan-timeout-diagnosis-record.md`。
- v4（2026-09-06）：B2c round 1 結果回寫 #7——既有測試 CI 高重現（macOS 81/200 於 5 秒 guard；ubuntu 197/200 於 `orphan must be reaped`，`kill(-pgid,0)` 顯示群組當下仍存在，原因未確認、zombie／oracle race 為待驗假設、不判定 production defect）；自製 proc 探針路徑 0/400 未重現；對真實路徑尚未排除任何候選機制；狀態維持 candidate，B2c-2 承接 round 2。
- v3（2026-09-05）：B2a 登記 Go 表 **#7** `TestOrphanDoesNotHangNormalExit` 為 candidate（CI-only、現行 HEAD `ffcd161` 2/2 重現、本機 bounded stress 未重現、根因未確認），附 run／attempt／HEAD／指令／artifact／逐字訊息／跨 SHA 對照／本機反證；候選不因後續 attempt 轉綠而除名或改寫為誤紅。#7 為 owner 明示的一次性診斷例外下取得的 attempt 2 證據，**不構成**「非八條 Go 測試可重跑」通則；處置由 B2c 承接。B2b 回填版本順延 v4。
- v2（2026-09-05）：B1b 更新。B 段兩條由「待重現（B1b）」改為 resolved（`8aee222`），附已確認根因、修法與 Gate A 重驗摘要；註明 pre-merge control 採檔案層級判準且與規則 7 無關；前端一般規則改為適用於 F1／F2 以外；新增規則 7（具名 F1／F2 的 FAIL 分類契約）。
- v1（2026-09-04）：B1a-4 建立。A 段六條處置狀態自 B1a-1／B1a-2／B1a-3 關票紀錄與 B1a-4 Gate A 帳表轉錄；B 段兩條前端候選自 backlog B1 驗收條件 (4) 轉錄，標「待重現（B1b）」；C 段規則 1–6 依 B1a-4 plan D1／D4 與 owner 裁定寫入；規則 1 於 closure review 依 owner 要求改為「先分類、僅契約回歸不得重跑吸收」，與 §7.1 及 plan D1 一致。
