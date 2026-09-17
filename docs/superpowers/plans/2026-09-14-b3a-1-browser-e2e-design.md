# B3a-1 Browser E2E 基礎——最小設計（rev19，codex-reviewer mailroom #65 最終裁定：B3a-1 技術驗收通過，aggregate 標為技術驗收完成；未提交／推送／PR／合併；B3a-2／B3a-CI 不受影響、未獲施工授權）

> 狀態：**codex-reviewer（mailroom #65，2026-09-17）最終裁定 B3a-1 技術驗收通過**：**1a／1b 本次工程交付責任達成**；**aggregate 標為技術驗收完成**，但**未提交、未推送、未開 PR、未合併**；**B3a-2／B3a-CI 及其他票不因此通過，也未獲施工授權**。**受測功能快照**：`b3a1a-snapshot-20260917T055531Z`（HEAD `023481440017fea4e596f039258e35259f34ca8f`），49 檔逐檔雜湊相符。**三個正式 run-id（各一次、首次即通過）**：A 預設 smoke（未設 `E2E_KEEP_ARTIFACTS`，走自動刪除分支）`20260917T061013Z-2ddd9d` rc=0；B controls（`E2E_KEEP_ARTIFACTS=1`）`20260917T062716Z-b25f60` rc=0；C offline（`E2E_OFFLINE_SANDBOX=1`＋`KEEP_ARTIFACTS=1`＋`DEBUG=pw:browser,pw:api`）`20260917T063037Z-2cca8a` rc=0；三者皆 `cleanupClean=true`、`overallFailed=false`、`artifactViolations=0`、watchdog `stopSignalSent=0`。外部側錄批次 `frontend/e2e/.artifacts/1b-abc2-20260917T060356Z/`，MANIFEST 53/53 自驗相符。**reviewer 獨立複核範圍**（reviewer 親自執行，非本方回報）：HEAD 與 49 檔相符、53/53 相符、三份原始 stdout 的判定欄位、B 的 control A／B 原始證據（磁碟 sha 等於 baseline、延遲 2001ms／完整內容相等、2002ms）、C 的 `sandbox-exec`／`chromeWrapper.sh` 啟動與零 browser 違規、當下無 listener／無 pointer／無殘留程序、watchdog 短測原始結果。**Attempt 0（照實保留，不得美化）**：本批次先有一次 watchdog PATH 造成的啟動失敗（外部監看工具自身環境缺陷，`spawn wails ENOENT`），排除後三次正式驗證均通過；**不得從歷史刪除，也不得概括成「整個批次首次就零失敗」**；「首次失敗即停」規則**沒有**一般性的「工具錯誤就可自動重試」豁免，此次是依已保存的明確環境歸因被接受，**不構成未來跳過停止點的授權**。**階段觀察（只寫觀察，不得寫成原因）**：直接時間戳確立「globalSetup 完成 → Running 行」的差異為 A 約 12m24s、C 約 11m15s、B 約 4.5s；**不得**推論死結、資源競爭或 Chrome 冷啟動；C 的 browser launch 側錄出現在 Running **之後**，**不得**把 Running 之前的空窗稱為已證明的 Chrome 啟動耗時；這屬剩餘效能／診斷限制，不阻擋本次驗收，**本票不繼續優化或追加測試**。**其餘限制照實保留**：A 成功後目錄依預設刪除、證據僅到外部側錄；C 的網路阻斷為每秒取樣沿用歷史控制，**不得**擴張為封包層級全程證明；reverse-check 依既有 env gate `skipped`（本次未執行，**不得**新增一次全綠宣稱）。**工時（已量測／估計／未量測分開，不得虛構精確總工時）**：`06:10:11Z–06:44:16Z` 涵蓋 A/B/C 及其間隔操作，**不得**把整段當純等待之後又另外加計間隔 active；不足部分**保持未知**；原 **1.4 pt 只留原始估算**，不得冒充最終實績；先前 413 秒、55–70 分鐘未核實欄、1b active 未完整量測欄一律保留。**文件雜湊**：本次結案文件（design.md rev19、backlog.md rev63）另列 revision 與 hash（見文件末／§4／backlog 對應段落），**不改寫受測快照**、**不因純文件變動重跑功能測試**。**保留事項**：PoC 暫存與各失敗原始證據繼續保留；本次結案**不含**清除暫存、git 提交或發布。
> 分支 `b3a-1/browser-e2e` @ `02348144`。紀錄 `.superpowers/sdd/2026-09-14-b3a-1-browser-e2e/progress.md`。

## 修訂紀錄

- **rev1**（2026-09-14）：初版，含盤點、隔離 PoC、估點核對。
- **rev2**（2026-09-15）：依 owner 設計審查修訂。
  - 五項裁定：
    - replay provider 移到 B3a-2（若仍需協定探索，先拆 spike）
    - CI／macOS runner 另開可行性小票
    - 同意補 `data-test`，由 E2E 實際使用驗證，不另寫只檢查屬性的單元測試
    - 「不需外部網路」解讀為「依賴準備完成後，測試執行不需外部網路」
    - 冷啟動 180 s 接受為本機成本，300 s 上限暫用
  - 四項補正：
    1. 隔離失敗在啟動前攔下（§2.2），負控制第 4 項改為「缺 tools 設定時未啟動 app 即失敗」；tripwire 限定恰好一個 `--version`，呼叫紀錄缺失即失敗
    2. 補異常收尾契約（§2.3）：部分啟動失敗、啟動逾時、測試失敗、中斷，都走有上限的停止程序；34115 與 Vite 5173 都處理，不終止既有佔用者；單一 worker、零重試
    3. 存檔驗證（§2.5）：磁碟輪詢改為核對完整預期內容，並在同一受控延遲下對照舊判定（應失敗）與新判定（應成功）；**不據此宣稱證實歷史那次失敗的根因**
    4. 收斂事實措辭（§1）：F6、F7、F8、網路觀察範圍；證據目錄統一，啟動失敗也留 harness log；**不承諾所有失敗都有 trace**；實際開過一次失敗 trace
  - 估點依上述工作重算（§4），不再沿用 rev1「尚餘 1.5 hr」的結論。
- **rev3**（2026-09-15）：依 codex-reviewer 代 owner 的設計複核裁定修訂（owner 已確認 codex-reviewer 有代為裁定與依進度授權的權限）。
  - **裁定 1–3**：
    1. B3a-1 採 **14.0 hr／1.4 pt**，保留一次程序範圍的阻斷外網實測；1.0 pt 舊估作廢。
    2. 拆票寫入 backlog（rev48）：B3a-2a 6–7 hr（中位 6.5 hr／0.65 pt）；B3a-2s spike timebox 4–5 hr（中位 4.5 hr／0.45 pt）；B3a-2b 待 spike 後估、不計入已估小計；B3a-CI 可行性 3–5 hr（中位 4 hr／0.4 pt，CI 等候時間另列、不算工時）；**尚未包含 B3a-2b 與後續正式 CI 整合，不能宣稱總 scope 已完整估完**。B3a aggregate 的核心流與 CI 條件保留；本輪只施工 B3a-1。
    3. 同意獨立 `npm run test:e2e:controls`，不列預設 suite；仍是 B3a-1 驗收必跑項。
  - **審查補充 A–E**：
    - **A**：部分啟動失敗也必須清理——spawn 當下即追蹤自家程序（pid／pgid／啟動時間寫入 `run-state.json`，不等 ready）；收尾時走訪 spawn pid 的整棵後代樹，非同 pgid 的子程序個別 TERM／KILL；前次殘留核對 process identity（pid＋啟動時間＋命令列）與 pgid 歸屬，無法證明歸屬就失敗並保留診斷、不送任何信號。新增負控制 N10、N11a、N11b（§3）。
    - **B**：外網阻斷只作用於本次測試程序及其子程序，採 macOS `sandbox-exec`（只允許 loopback 的 profile 包住 `npm run test:e2e`，其後代皆繼承）；不關閉主機網路介面、不改全機防火牆、不用 sudo；若不可行，回報 reviewer 裁定替代或限制，不自行降格為完成。網路取樣改為從 spawn 起（含啟動期間）到停止程序開始為止。`GOPROXY=off`／`npm offline` 是輔助設定，不等於完整網路隔離；Go 端改用 `GOFLAGS=-mod=readonly`。
    - **C**：控制 A 的 `test.fail()` 只能標註「緊接目標存檔斷言之前」才發生的失敗，setup／locator／cleanup 失敗不得被吸收；核對結果確實因「延遲期間讀到舊的完整內容」而失敗；延遲 wrapper 保留原函式的 `this` 與 `args`，並留下呼叫與延遲證據。
    - **D**：N5 必須在通過啟動前預檢之後才注入版本不符，確實驗證啟動後核對；若在預檢就失敗，不算 N5。
    - **E**：progress 裡舊 PoC 的絕對措辭只作歷史，另補當前修正；本設計 §2.10「只有 loopback」改寫成目標及驗證方式，不當作既有事實。
  - §2.1、§2.3、§2.5、§2.7、§2.10、§3、§4、§5、§6 依上述裁定與補充改寫，§4 標為「已裁定」。
- **rev4**（2026-09-15）：依 codex-reviewer 節點 2 複核裁定修訂，**reviewer 裁定的驗證範圍調整**（施工中發現的 F1／F2 與三個 harness 問題）。
  - **F1 裁定**：施工時為壓系統 Chrome 背景流量加的 `--host-resolver-rules=MAP * 127.0.0.1` 會遮蔽頁面層級外連，**採方案 (2)**——不使用該旗標、也不加其他 Chrome 行為旗標，改在 browser 層對 `BrowserContext` 安裝 request／WebSocket 攔截判定（§2.7）。rev4 之前 agent 於節點 2 提出的「保留旗標＋補頁面監聽」（方案 1）**未被採納**。
  - **審查補充 R1–R4**：
    - **R1**：README 不得把本票（B3a-1）的驗收項目寫成後續票；README 由另一施工 agent 修改，此處只作提醒。
    - **R2**：`ps`／`lsof` 等觀測工具呼叫要設逾時，並區分「確實沒有符合結果」與「無法觀測」（工具遺失、權限、逾時）；無法觀測一律判失敗並留證據，不得判成 clean、也不得判成 0 違規（§2.3、§2.7）。
    - **R3**：殘留處理——對每個仍存活的 tracked process 逐一核對身分四要素；root 已消失時仍要檢查其後代；`run-state.json` 遺失或損毀時，失敗、不送信號、保留診斷（§2.3）。
    - **R4**：停止程序改用有上限的批次查詢加單調時鐘，TERM 與 KILL 階段耗時分別記錄；埠核對另計，不併入 TERM grace period；收尾確認乾淨後清除 `.active-run.json`，cleanup incomplete 時保留（§2.3）。
  - **§2.7 網路判定改為兩層**（程序取樣層＋browser 層，取代 rev3 單層 `lsof` 取樣＋事後 sandbox-exec 驗證的設計）；新增 Chrome 主程序 argv 證據要求。
  - **§3 負控制**：N9 拆為 N9a／N9b；N11a／N11b 各加一種新情境；新增 N11c（state 遺失／損毀）、N12（觀測工具錯誤注入）、N13（忽略 TERM 的自家 fixture，驗證升級時間與總上限）。
  - **§4 估點**：reviewer 要求新增工作（F1 browser 判定、R2–R4、N9a／N9b／N11c／N12／N13）完成後重算，1.4 pt 可能不再成立；**本輪不改數字**，不得為了趕估點縮減驗收。
- **rev5**（2026-09-15）：依 codex-reviewer **節點 3 進度審查裁定**（mailroom #12）修訂——**這是進度審查，不是驗收通過**。裁定回應「節點 3 第一部分審查」（progress 段）提出的 Q1–Q3，並新增缺口 A–C。
  - **Q1 裁定（§2.7 程序取樣層端點分類）**：`lsof` 逐行解析不能對整行套 regex；有 `->` 的紀錄（TCP 或已連線 UDP）解析右側遠端端點判斷 loopback；LISTEN 本機位址只允許 loopback，`0.0.0.0`／`::`／`*`／其他介面一律違規；沒有遠端也非 LISTEN 的紀錄（`CLOSED *:*`、未連線 UDP）只記為診斷「沒有觀察到遠端」，不得據此證明無外連；無法解析的紀錄保留並判為觀測失敗；補一組 parser 測試案例。
  - **Q3 裁定（新增小節，§2.3／§2.9）**：每次 run 前後檢查受版控產物內容與可執行位元，範圍 `frontend/wailsjs/**`（受版控部分）、`go.mod`、`go.sum`、`frontend/package.json`、`package-lock.json`、`package.json.md5`；有變動即讓 run 失敗、保留差異並提示，teardown 不自動還原；mode 基準為 HEAD 預期值；一次性授權把 `frontend/wailsjs/runtime/` 三個檔案 mode 還原為 644（先存 mode diff、確認內容與 HEAD 相同），**這是一次性修復，不是 harness 自動行為**。
  - **缺口 A（networkGuard 不得靜默放行）**：違規與觀測錯誤同時保存在記憶體並反映在最終結果；證據檔寫入／讀取失敗或缺失不能變綠。新增 N14a（證據寫入失敗）、N14b（證據檔判定前被刪除）。
  - **缺口 B（R2 補強）**：exit 1 要結合 stderr 與查詢語意，區分「合法無匹配」與「觀測失敗」；exit 1 時若有有效 stdout 要保留。N12 擴充：exit 1＋錯誤 stderr（例如 permission denied）情境。
  - **缺口 C（R3 補強）**：state 結構驗證涵蓋有效 PID／PGID、非空身分欄位、與 pointer 的一致性；損毀時保留指標、不送信號。N11c 擴充：空陣列、欄位缺失。
  - **N10 協調點**：改用測試專用的啟動故障協調點（確認目標後代已建立、app 尚未 ready，才觸發）；可用可控假啟動器驗證同一 lifecycle path，但要標明哪些是 fake 證據、哪些是真 Wails 證據。
  - **停止程序預算**：TERM 10 s、KILL 5 s 是等待預算；`+100 ms` 只是量測容差，不是額外等待額度、也不是 OS 硬即時保證；外部查詢逾時受剩餘預算限制；TERM、KILL、埠核對分別報告，超過容差照實列為失敗或需分析。
  - **Chrome sandbox**：實際 argv 帶 `--no-sandbox`（Playwright `chromiumSandbox` 預設值），reviewer 接受在本隔離 fixture 測試中沿用現有設定，但不能宣稱 Chrome 自身 sandbox 有啟用；macOS sandbox-exec 外網阻斷仍須另外實測。
  - **證據紀律**：失敗或作廢的證據今後保留並標示失效、不刪除；第一次 N9a 的原始證據已缺失（施工 agent 刪除），標示為「原始證據缺失」。
  - **Q2 拆票**：原 **B3a-1** 改為 aggregate，保留全部原有驗收責任與 1.4 pt（**歷史估計，需重估**），不重複計入小計；新增 **B3a-1a**（harness 實作、全部修正、controls、負控制、Vitest／build／`go test ./... -race`／gofmt，交付固定快照供整合驗收，**施工中**）與 **B3a-1b**（同一固定快照上三次正式連跑、完整 sandbox 外網阻斷實測、失敗 trace 檢視、獨立整合 review 與證據封存，依賴 1a、**尚未授權**）；兩票皆完成才關閉 B3a-1，拆票不得省略任何原有驗收項；估點待核定，目前預測區間 18.5–23 hr（見 §4）。
- **rev6**（2026-09-16）：依 codex-reviewer **對 B3a-1a 的複核結果**（mailroom #14）修訂——**複核不通過，1b 仍未授權**。reviewer 本輪核對 HEAD 與 tracked diff 的 sha256、29 個 untracked 檔案逐檔 hash、before/after manifest 相同；實跑 `networkLineParser` selftest 14/14 與 `artifactIntegrity` selftest 4/4；Go race、Vitest、build **只讀我方 log，沒有另外跑**；證據 MANIFEST 只有 **5/6** 通過（`checks.log` 在算完 hash 之後又被追加兩行）。
  - **R1（§2.3 殘留處理）**：送出任何信號之前，要先驗證 pointer、state、root 三者與分類的一致性（含 `rootPgid` 與 `samePgid`）；沒有已驗證存活成員支持的舊群組不得送信號；root 消失時只清理能證明歸屬的 escaped child；TERM 升級到 KILL 同樣適用。負控制涵蓋 pgid 不一致、root 已死但 escaped child 存活兩種情境。
  - **R2（§2.3 啟動階段）**：一旦 spawn 取得 child，就要立即具備可靠的持有與收尾路徑；身分觀測失敗要明確記為失敗，不得用虛構的時間或命令列冒充身分證據；所有 spawn 之後的啟動階段例外都走 single-flight 的有上限停止程序。負控制：預檢成功、spawn 成功、第一次 post-spawn 的 `ps` 才失敗。**這一項是程式路徑檢查，未對真實 Wails 做故障注入**，證據要標明這個邊界。
  - **R3（§2.7 browser 層與 §2.9 判定）**：smoke 與 controls A／B 都要在不會被 expected failure 吸收的位置檢查 guard 的記憶體狀態；對照 A 的預期失敗不得吞掉 teardown 或 guard 故障；`network-samples.log` 缺失、run-state 的 `observationFailures` 讀取失敗，都不得當成通過。
  - **R4（§3 驗收項）**：新增 E2E 專用 tsconfig 與 `typecheck:e2e`，strict，涵蓋 harness、spec、controls、support，支援原生 TS selftest 的 `.ts` import，列為固定快照的檢查項目；已知兩個真實型別問題（`global-setup.ts:259` TS2339、`support/psUtil.ts:180` TS2322）要真的修，不得用 blanket `any` 或 `ts-ignore`。
  - **R5（§2.9 證據）**：封存順序改為先關閉所有 log writer、再產生 manifest、最後獨立驗證全部 hash；驗證輸出不得再寫回已算過 hash 的檔案；舊快照與 manifest 保留並標示失效或補充說明，不覆寫舊 checksum。
  - **wailsjs 處理**：改為執行前預檢加執行後核對，兩者都不自動修復；任一 mode 或內容事故就把該次 run 記為失敗並保留證據，**不得反覆重跑直到湊出三次綠燈**；再次發生時要先捕捉 inode、mode 與產生路徑並定位原因，再提最小修正，不得把推測寫成根因；手動還原僅限前次的狹義條件（保留差異、只有這三個檔的 mode、無 writer、內容未變）。
  - **1b**：仍未授權；要等 R1–R5 完成、工時 breakdown 修訂、交付新凍結快照之後，由 reviewer 決定啟動。controls 維持獨立的 `test:e2e:controls`，但仍是必要驗收項，**不得改為 optional**。
  - **Q2 後續（1a 再拆子票）**：reviewer 裁定**不得**把 1a 的實作缺陷修正移進「固定快照驗收」的 1b。1a 保留為彙總，依已有可驗證的交付邊界拆成 **B3a-1a-browser**（browser／spec／save controls）與 **B3a-1a-lifecycle**（程序生命週期、隔離與失敗判定）兩張子票；若後者仍超過 20 hr，再依實際交付邊界續拆（見 §4）。
  - **估點寫法**：18–24 hr 的已投入照實保留為**事後估計**，明確標示不是量測值，不得為了壓到 20 hr 而回寫較小的數字；1b 的 4–8 hr 目前包含等候時間，**不可直接換算成 pt**，要先分出實際操作與分析工時，以及純等待的日曆時間；所有點數與 backlog 總計都要等修訂後的 breakdown 才核定；**14 hr／1.4 pt 留作原始基準，不得再當成目前的 forecast**。
- **rev7**（2026-09-16）：依 codex-reviewer **對 B3a-1a 第二次交接的複核結果**（mailroom #31）修訂——**R3 指定反證、R4、快照封存順序通過；R1／R2 仍有缺口，1a 未通過，1b 仍不授權**。reviewer 本輪核對：HEAD 與 binary diff hash 相符、repo-manifest 的 30 個 untracked 檔案 hash 全相符、新快照 MANIFEST 6/6、以指定 Node 實跑 `tsc -p tsconfig.e2e.json` exit 0、重算 evidence-manifest 的 9 runs／122 檔全相符、讀過四組 R3 run 的 harness.log 與 controls 的 `TEST_FAILED`、直接解讀三份 trace.zip；**Go race／Vitest／build 只讀我方 log，沒有重跑**。同時核定三張子票的工時基準並授權更新 backlog。
  - **A（R1 未完成）**：所有正式停止入口都要用明確可靠的持有或身分驗證，不得把歷史追蹤清單（`st.processes`）當成已驗證；`verifiedGroupPgids` 不得在開頭算一次就沿用到 KILL，**每次升級只針對仍能證明歸屬的 pid 或群組**；已消失、不符或無法驗證的目標不得送信號；觀測失敗判失敗並留診斷；維持 TERM／KILL 有上限契約，驗證不得變成無上限的同步查詢；新 spawn 仍持有 `ChildProcess` 時可用明確持有狀態，但不得冒充 `ps` 已核對。必驗情境三項：已死 root＋存活 escaped child、TERM 後 root 群組消失而 child 仍活、pid 身分變更或觀測失敗時不碰不相關目標。
  - **B（R2 迴歸）**：root 身分更新必須讓 `run-state.json` 與 `.active-run.json` 兩份持久資料保持一致；更新途中失敗不得留下會被誤信的狀態；placeholder 與已驗證身分要明確區分；測試要由正式寫入 API 產生 state／pointer，再模擬中斷與接續清理，不得只用手工準備的一致 fixture。post-spawn 的 catch 中，log／setStatus 失敗時仍須執行有上限的清理。
  - **C（R1 一致性）**：核對範圍補上 `RunState.rootPgid`，並逐筆核對 `samePgid === (pgid === rootPgid)`；state 矛盾時在任何信號前拒絕。
  - **封存缺口**：`evidence-manifest-20260916T-r1r5-round.json`（9 runs／122 檔）**不涵蓋** `014853Z`／`021929Z`／`023211Z`／`024527Z` 四組 R3 run，需另行補封存；原 manifest 保留，不得宣稱已涵蓋。
  - **R3-a 文案更正**：現行實作已是 chmod 0444 後觸發外部 fetch，reviewer 解讀三份 trace.zip 確認有真實 EACCES 與各自的 guard `writeFailures` 斷言錯誤。因此「再補一種唯讀反證」的選項與現況不符，**不再列為待辦**；同時註明這是 trace 內容檢查，**不等於 1b 的實際開啟檢視驗收**。
  - **wailsjs**：裁定不變（執行前預檢＋執行後核對、失敗留證據、不得反覆重跑湊綠燈）。第三次事故根因未知，限制保留。`package.json.md5` 的變動雖可歸因於已知的 `package.json` 編輯，但**該次 run 仍是 artifact check failure，不得改判為 PASS**。
  - **1b**：仍未授權。
  - **§4**：子票由兩張改為三張——**B3a-1a-browser**（6–9 hr，中位 7.5 hr／0.75 pt）、**B3a-1a-lifecycle-1**（程序生命週期與隔離，含 harness 骨架；11–16 hr，中位 13.5 hr／1.35 pt）、**B3a-1a-lifecycle-2**（失敗判定與觀測完整性；10–16 hr，中位 13 hr／1.30 pt）；1a 合計 27–41 hr，中位 **34 hr／3.40 pt**（事後估計、非量測）。B3a-1b：操作與分析 3–5 hr，中位 **4 hr／0.40 pt**；純等待 1–3 hr 另列、不計入工程量。**合計已知基準：30–46 hr，中位 38 hr／3.80 pt**——**不含本輪 A–C 的剩餘修正，不得標成完工總量或完整 forecast**；各子票原本寫的「剩餘 0」**撤回**，改為「A–C 剩餘修正待估」；**不得以這次核點宣稱 1a 通過**。
- **rev8**（2026-09-16）：owner 裁定＋codex-reviewer **mailroom #32／#33 正式授權**新增小節——**E2E 期間的原生視窗（限定 production 授權）**（§2.2a）。
  - **背景**：owner 雙螢幕作業，E2E 每次都會開出原生視窗造成干擾；owner 直接裁定先做「E2E 期間以環境旗標控制 Wails `StartHidden`」的最小驗證，reviewer 已正式授權。**這是對「production 僅允許加 `data-test`」邊界的限定擴充**，不是把該邊界整體放寬——僅限旗標接線、測試啟動器、文件與驗證，其餘 production 仍只能加 `data-test`。
  - **owner 更正（必須寫進文件，不得省略）**：`StartHidden` 是**隱藏視窗**，**不是取消建立視窗**；Wails v2.13 的 macOS 端仍會**無條件**呼叫 `activateIgnoringOtherApps:YES`（本機模組快取佐證：`internal/frontend/desktop/darwin/AppDelegate.m:54`、`WailsContext.m:425`、`:434`；`StartHidden` 接線於 `window.go:60`），**因此不能保證不搶焦點**。**文件不得寫成「已解決搶焦點」**——只能視實測結果寫「未驗證」或「此方案只解決顯示干擾」。
  - **旗標**：`WORKBENCH_E2E_START_HIDDEN`，值恰為 `1` 才啟用；未設定或其他值維持現行行為（正常開窗）；只設定在 E2E 啟動器給 app 子程序的 env，不得寫入全域 shell、使用者設定或一般開發用設定。
  - **範圍與禁止項**：範圍僅限旗標接線、測試啟動器、文件與驗證；**禁止**安裝視窗管理工具、改 macOS 全域偏好、patch／升級 Wails、加自動搶回焦點的行為、改動單一實例鎖／approval／provider。
  - **驗收條件**：hidden／unset 兩模式各自的驗收條件（見 §2.2a）；焦點與可見性分開報告；**此 PoC smoke 不算 1b 的三次正式連跑**，既有 A–C 負控制與 artifact 規則不豁免。
  - **歸屬與工時**：歸屬 **B3a-1a-lifecycle-1**；增量 0.5–1 hr，中位 **0.75 hr／0.075 pt**（兩位顯示 0.08 pt），屬**新增範圍的剩餘工作**，**不得回寫為既有已投入工時**；與 A–C 共用的驗證操作時間只計一次。lifecycle-1 中位由 13.5 → **14.25 hr**；1a＋1b 已知中位基準更新為 **38.75 hr／3.88 pt**（見 §4）；**仍不含**待估的 A–C 剩餘修正，並保留 B3a-2b、後續正式 CI 整合未估的註記，**不得稱完整 forecast**。
- **rev9**（2026-09-16）：依 codex-reviewer **對 B3a-1a 第三次交接的複核結果**（mailroom #35）修訂——**B、C、StartHidden、型別與封存補件通過；A 的停止契約仍未完成，1a 不通過，1b 仍不授權**。reviewer 本輪獨立確認：HEAD 與 tracked diff `ced113aa…`、30 個 untracked hash、snapshot 6/6、R3 addendum 87 檔均重算相符；實跑 `tsc -p tsconfig.e2e.json` exit 0；以原碼模擬確認 B、C 的原始反例已修好；**Go race／Vitest／build 只讀我方 log，未另跑**。
  - **A 仍未完成，新增 A1–A5**（§2.3；皆以目前原碼＋mock ps/kill＋虛擬時間重現，未送真實信號）：
    - **A1**：初次身分核對不符只 log＋continue，未回報 abandoned，導致「沒送任何信號卻 clean=true」。要求區分「確實不存在」與「身分不符／不明」；初次核對不符即失敗並**保留 active pointer**。
    - **A2**：TERM 後觀測拋錯仍用舊清單升級 KILL，KILL 前沒有成功的身分核對。**未知不等於已驗證存活**；只有重新取得可靠歸屬或仍有可靠持有依據才可升級，否則不碰未知目標、回報未清乾淨並保留診斷（`050030Z-70c019` 的真實 log 即此路徑，先前「TERM 後重新驗證」的敘述須更正）。
    - **A3**：身分比較省略 `startedAt`。契約為 PID＋pgid＋command＋開始時間四要素，非 held child 不得省略；可維持單次批次查詢。
    - **A4**：`isDying` 把括號命令列當死亡證據無依據。本機 `ps.1:401–421` 說明 `<defunct>` 為 zombie、括號 ucomm 為 argv unavailable／不一致，**括號不等於死亡**；argv 取不到應視為身分觀測不完整。
    - **A5**：`killGroup`／`killPid` 把 `process.kill` 與 `log.log` 包在同一 try，catch 又呼叫可拋出的 `log.log`（`appendFileSync`），磁碟失敗時可能送出第一個信號後即拋出、漏掉其餘目標與升級。要求：診斷失敗保留錯誤並使最終判定失敗，但**不得阻斷其餘可安全執行的清理**。
    - **方法要求**：先建立可直接執行的小型停止程序測試覆蓋 A1–A5、dead-root＋存活 escaped child、正常路徑；通過前不得再用多輪真實 Wails 啟動試誤。
  - **兩項風險裁定**：
    1. **觀測逾時＝待分析的阻擋項**，不准反覆重跑到綠。`050030Z` 的 ps 在 TERM 10002 ms 邊界被 timeout，**不得直接歸因高負載**；須記錄剩餘 budget、耗時、`error.code`／訊號，辨別是否把極小剩餘時間當成 ps timeout 而自造失敗。grace 到期與工具故障分開處理；KILL 前仍要有身分依據；既有上限與容差不放寬。
    2. **wailsjs 已有明確來源**（reviewer 實讀本機 v2.13.0 原碼）：`internal/app/app_bindings.go` 的 `generateBindings` 為 `RemoveAll(runtimeDir)`→`Extract`→`fs.SetPermissions(wailsjsbasedir, 0755)`；`internal/fs/fs.go:272` 的 `SetPermissions` 會 Walk **每個檔案**並 `os.Chmod`；`pkg/commands/build/base.go:428` 的 `generateRuntimeWrapper` 另有 `RemoveAll(wrapperDir)`→`Extract`。**尚未證明六次事故各走哪一階段**。授權在隔離暫存副本做一次有紀錄的 bindings／compile 階段驗證後提最小處置；禁止 patch 模組快取、升級 Wails、把 HEAD mode 全改 755、放寬 artifact 檢查；若考慮 `-skipbindings` 須先確認 CLI 支援、bindings 是否同步、跳過後仍產生哪些 runtime 檔，不得猜測。維持記失敗、不自動還原、不湊綠燈。
  - **證據更正**：hidden 模式證據寫「全程 0 個視窗」超出單點查詢範圍，須附更正並保留原始輸出（§2.2a）。
  - **§4：lifecycle-1 再拆**：中位已達 14.25＋6.9＋2.6＝**23.75 hr**，超過 20 hr 拆票線。依 reviewer 裁定改為 **aggregate**，拆成兩張可獨立驗證的子票：**startup／隔離與持久狀態（含 StartHidden）**、**stop／身分驗證與 stale recovery**。依實際交付分攤既有與剩餘工時，**不平均分、不重複計入，各票高端 ≤ 20 hr**；未分攤的部分標「待估」，**不得虛構完工**。已核定基準：本輪新增 3.1 hr（lifecycle-1 2.6、lifecycle-2 0.5）；A–C 那輪採中位（lifecycle-1 6.9、lifecycle-2 0.85）；**已知總基準 38.75＋6.9＋0.85＋3.1＝49.60 hr／4.96 pt**；**breakdown 明列「A–C 中位與新增 3.1 不重複計入」，已完成部分不得再寫成剩餘**。
- **rev10**（2026-09-16）：依 codex-reviewer **對 B3a-1a 第四次交接的複核結果**（mailroom #37）修訂——**A1–A5 原反例修正接受**；發現新的阻擋缺陷 **L1**，1a 仍不通過，1b 未授權。reviewer 已獨立驗證：HEAD 與 tracked diff SHA 與快照一致、MANIFEST 9/9、32 個 untracked 檔案 hash 相符、stopProcedure selftest 9/9 與 `tsc` 通過、已讀 N13／N7／N8／normal 原始紀錄並接受這些**單次執行**的結果。
  - **L1（嚴重，fail-open，§2.3）**：`psUtil.ts:52` 繼承 locale、`:133` 的 lstart regex 只接受英文格式、`:144` 解析失敗直接 continue。reviewer 以 `LC_ALL=zh_TW.UTF-8` 重現：macOS `ps` 回傳「三  9/16 14:19:25 2026」，`parsedRowCount=0` 但 `isAlive(self)=true`；交給實際 `stopProcessGroup`（僅 mock kill）得到 `clean=true`、residual／abandoned／unconfirmed 全空、`observationFailed=false`、signals=[]。**等於把觀測失敗判成程序已消失，整條停止契約在非英文語系下靜默失效。**
    - **授權最小修正**：**只在 `ps` 子程序環境固定 `LC_ALL=C`**（不改使用者或 app 全域環境），單筆 `processStartedAt` 與批次查詢一致；關鍵 `ps -A` snapshot 的**非空不可解析列**或**整份空結果**都必須拋 `ToolObservationError`；診斷只記必要格式資訊，**不得把整份程序命令列寫入 log**。
    - **focused selftest**（既有 9 項保留）：C 格式可解析；繼承 `zh_TW` 的實際查詢經正規化後仍含活著的自身 PID；malformed／空 snapshot 不得回報 clean。
  - **selftest npm scripts（核准）**：新增 selftest npm script、彙總指令與 README 說明，納入固定快照檢查；記錄 Node 與 loader 需求（`--experimental-loader`）。沿用現有工具、不新增框架；**不取代** `test:e2e:controls`，**不把負控制放進預設 smoke**。
  - **`-skipbindings`（有條件核准）**：採用前須在隔離副本以相同 Wails v2.13.0 產生 bindings，**完整比對** go bindings 與 runtime 檔案內容對目前工作樹，保存命令、版本與比較證據；**內容相同才可採用**，有差異則保留並交 reviewer 裁定、**不得自動覆寫 repo**。既有 mode 與內容前後檢查保留。文件須寫明：**未來 Go binding API 改動必須重新產生並核對**，`tsc` 通過或內容前後不變**不能證明 bindings 新鮮**。採用後以一次 normal smoke 與啟動失敗路徑確認；不改 Wails 或 module cache。另註明：先前的隔離重現只證明 chmod 機制，**不得據此宣稱歷史每次事故都已逐一證實**。
  - **逾時診斷措辭收斂**：失敗紀錄剩餘 **315 ms**、新 cutoff 為**小於 300 ms**，兩者不重疊，**不得寫成已完全消除該類逾時**。保留三點：避免極短預算再查詢、新的 N13 單次通過、`ps` 仍可能逾時且須判觀測失敗。不得以負載假說或單次 PASS 代替保證，也不得把逾時放寬為 clean。若要聲稱邊界保證，須另補可控制時間的 focused 證據。
  - **§4：新基準**（reviewer 核定）：本輪 +6.6 hr（lifecycle-1 +5.3、lifecycle-2 +1.3），**明列為回溯估算、非計時實績**。已知基線 **56.2 hr／5.62 pt**（49.6＋6.6）；lifecycle-1 aggregate **29.05 hr／2.91 pt**（23.75＋5.3）；lifecycle-2 **15.65 hr／1.57 pt**（14.35＋1.3）。**L1 修正與剩餘工作尚未估，不是最終完工預測**。lifecycle-1a／1b 的分攤尚未提供，維持「待估」，各票 ≤20 hr、兩張加總對齊 aggregate 29.05 hr、不重複計入，**未分攤不得當成已核准子票**。
- **rev11**（2026-09-16）：依 codex-reviewer **對 B3a-1a 第五次交接的複核結果**（mailroom #39）修訂——**接受 L1 修正、selftest 指令、`-skipbindings` 採用；程式面無新阻擋項。1a 只差一項 N13 證據；1b 已條件式授權**。reviewer 獨立驗證：snapshot MANIFEST 7/7、32 個 untracked 檔案 hash、HEAD 與 tracked diff SHA 全相符；實跑 `selftest:all` 通過——**實際是 32 項（artifact 4＋network parser 14＋stopProcedure 14），不是 14 項**（我方先前誤寫 14/14，本輪一併更正，見下）；`typecheck:e2e` rc 0；另從 Node 啟動前就設 `LC_ALL=zh_TW.UTF-8` 實測，單筆與批次 `ps` 的自身 PID／startedAt 一致，兩種 parser 對 malformed／空輸入均拋錯。reviewer 也自行在隔離副本跑 `wails generate module` rc 0，6 檔清單與逐檔 SHA-256 完全一致。
  - **接受項**：L1 修正、selftest npm 指令、`-skipbindings` 採用；程式面無新阻擋項。
  - **selftest 數量更正**：`selftest:all` 為 **32 項**（artifact 4／network parser 14／stopProcedure 14）。**凡文件中寫「14 項」代表 `selftest:all` 全部者，一律更正**（progress.md 兩處歷史記錄已附更正註記，見該檔）；§2.3「focused selftest（既有 9 項保留）」的 9 項是指 rev9 當時 stopProcedure selftest 的子集，非本次更正對象。
  - **N13 的完成條件（1a 最後一項）**：上次 N13 是觀測逾時造成的 fail-closed，**不得列為「成功升級 KILL 並清乾淨」**。補跑必須同時滿足：TERM 實際等滿、對已驗證自家目標升級 KILL、各階段耗時符合既定上限、最終 pid 與實際埠皆清空且 `clean=true`；預期啟動失敗 rc 仍可為 1。**原 428 ms 失敗證據保留、不得覆蓋**；若再次觀測失敗即停止並交代，**不無限重跑、不放寬判定、不得依舊 pid 直接 kill 未重新確認身分的殘留**。
  - **註解更正（§2.3 L1）**：`psUtil.ts` 的 L1 相關註解「呼叫當下才疊加」不精確——`PS_ENV`（`LC_ALL=C`）實際是**模組載入時**建立；**只修註解，實作正確不動**。
  - **1b 條件式授權**：條件為 (1) N13 通過、(2) 本輪裁定與狀態寫入文件、(3) 完成上述註解更正、(4) 封存新 manifest。另須逐項列出沿用之既有負控制證據的來源（版本／run-id）；受本輪改動影響者以本輪結果為準；**controls 不得省略**。功能檔須與已審快照一致（**允許純註解差異但需列 diff**）；**任何功能修改一律退回 1a 複核，不得混進 1b**。
    - **1b 範圍**：固定快照**三次正式 smoke**（先前 smoke 不算）、完整 `sandbox-exec` 外網阻斷實測、`expect.poll` 失敗 trace 的實際檢視與診斷內容核對、整合證據封存。逐項記 rc、實際網路阻斷證據、清理與 artifact 判定。**禁止把「取樣未觀察到外連」寫成「全程無外連」**。不得改主機全域網路／防火牆、不得碰真 provider 或 production 功能。**不再委派獨立 review，由 reviewer 負責最終 review**。失敗保留並分類；**屬實作缺陷即停回 1a，不得以重跑吸收**。**1b 完成不等於可自行關閉 aggregate**。
  - **語系結論收斂**：只能陳述「這次查詢為英文格式」，**不得以目前環境保證所有歷史 run 相同**。保留範圍：**現有證據未顯示曾觸發 L1；歷史環境未逐次封存**。
  - **§4：拆票採 (c)，三張子票**：**startup** 9.55 hr；**stop／身分驗證／stale recovery** 19.5 hr；新增子票「**ps 觀測語系與解析失敗處理（L1），含對應測試與驗證**」暫列 3.7 hr；三張合計 **32.75 hr**；lifecycle-1 維持 aggregate、不重複計入 backlog 小計。**必須寫明的矛盾**：原 19.5 hr 的範圍描述已含 L1，與「L1 為本輪新增 3.7 hr」衝突——文件要明確把 **L1 從舊 stop 範圍移出**；若 19.5 本就含 L1，**須扣除重複量**，不得只換標籤再加一次。施工 agent 會提供採用哪一種與修正後數字，**在他回報前，三張子票的數字一律標「待核定（去重處理中）」**，不先寫死。
  - **工時：改列待核定**：reviewer 確認 +6.1 hr 與 62.30／202.35／307.15 hr 的**算術正確**，但本輪表列「重跑＋等待」1.3 hr、前輪同類 1.4 hr **尚未確認符合「純等待不計工程量」**。這些數字**先列為待核定試算，不得宣稱已全部核定**；要把主動操作／排查／判讀與純等待分開、扣除純等待後再核定；沒有計時就照實標為估算。**各執行子票的剩餘區間上限同為 ≤ 20 hr**；lifecycle-2 暫列 **18.05 hr**，**不得默默沿用會超過 20 hr 的舊上限**。估點補正不阻擋 N13 或條件達成後的 1b，但**最終關票前必須核定**。
- **rev12**（2026-09-16）：依 codex-reviewer **對 1b 的複核結果**（mailroom #41）修訂——**三次正式 smoke、controls、trace 診斷能力接受；1b 整體未通過，aggregate 不關票**。裁定採 **(c)**。reviewer 獨立核對：tracked diff SHA 與 `073313Z` 相符、32 個 untracked hash 全相符；對 `071215Z` 的差異僅設計文件與 selftest 註解；N13 `072802Z` 的 TERM 10001 ms／KILL 580 ms／埠 609 ms／`clean=true` 確認，**1b 啟動前置條件成立**；三次 smoke `EXIT_CODE=0`、PASSED、觀測／網路／產物違規皆 0；controls `085255Z` rc 0 且 clean。reverse-check 沿用來源與缺 `EXIT_CODE` 的限制**保持原樣、不回填**。
  - **阻擋項 S1（§2.7）**：兩份完整 sandbox log **不只 Vite timeout**——attempt1 有 76 處 EPERM、attempt2 有 77 處，含 spawn 後身分補強與程序樹追蹤失敗。reviewer 最小重現：`sandbox-exec -f loopback-only.sb /bin/ps -o pid= -p <自身pid>` → rc 71、`execvp Operation not permitted`；**即使 profile 只有 `(version 1)(allow default)` 亦同**。因此存在「**`ps` 在 sandbox 內無法執行**」這個獨立障礙。系統 `/bin/ps` mode 為 4755，但**證據不得擴張為「底層機制已完全證明」**；reviewer 試過位元組相同、mode 755 的私有副本**直接被 SIGKILL**，**此路不採用**，且**不得改系統 ps、重簽系統檔或關閉系統防護**。
  - **必須更正的既有措辭**：INDEX／文件曾寫「證明 sandbox 沒有擋住任何功能所需連線」——**超出證據**；曾寫「卡住的是與網路隔離無關的逾時值」——**超出證據**；`Vite ready in 284 ms` 只是 Vite 自報啟動時間，**未量測完整區間**，也**未排除 ps EPERM**；`-viteservertimeout 60` 那次只能證明**某次診斷啟動成功**，**不能宣稱完整 suite 必通**。
  - **授權的受控診斷（回 1a，主動工作上限 0.5 hr，純等待另列）**：先用小型受控程序、**不啟動完整 Wails suite**，評估「觀測與清理由 sandbox 外的 harness 執行；受測 Wails／app 與 Chrome 子程序各在相同網路 sandbox 內」；**兩條受測子程序路徑都確實繼承阻斷**，**不得漏包 Chrome 或讓被測 app 留在 sandbox 外**；只操作自建測試程序、不改主機設定／sudo／防火牆、不放寬 network 限制、不得把觀測錯誤忽略成 PASS、**先不修改正式 harness**；0.5 hr 內無可行證據即交代阻擋，**不得改用「已知限制」關票**。
  - **trace 數字更正**（診斷能力已被接受，不需重跑）：`test.trace` 共 **762 筆事件**；**13 個內層失敗 expect**＋**1 個外層 poll timeout**，**非 21 次固定 500 ms**；screencast-frame 事件與 JPEG **皆 47 個**，**非 49 個**；**畫格為離散取樣，不能證明整段每一時刻畫面未變**。
  - **快照責任**：若後續功能快照改變，**新版正式連跑責任重新成立**；既有三次保留為 `073313Z` 的**有效歷史驗收**，不自動充當新版結果。
  - **§4 帳表更正**：**1b 狀態**改為「**已執行四項，sandbox 成功驗收仍未完成，剩餘待估**」，**不得**把原 1b 的 4 hr 預估與已完成的 2.05 hr 相加成 6.05。**三張子票**：32.45 不核定——扣減 0.6 hr（第九輪 5.3→5.0、第十輪 L1 3.7→3.4）＝**startup 9.55／stop 19.2／L1 3.4，合計 32.15**。**第十一輪 1.6 hr** 待依實際項目分攤，分攤前標「待分攤」。**核帳用已知量**：B3a-1 ＝ 62.30 − 0.60 ＋ 1.60 − 4 ＋ 2.05 ＝ **61.35 hr**；**B 軌 201.40 hr**；**全部 306.20 hr**。**不含**本次授權的受控診斷與尚未完成的 sandbox 修正／重新驗收，保留未估剩餘，**不稱最終 forecast**；子票分攤仍按 20 hr 規則，**不得為湊上限移動與範圍不符的工時**。
- **rev13**（2026-09-16）：依 codex-reviewer **接受 S1 架構方向並授權 offline 驗證模式實作**（mailroom #45）修訂——**授權在 1a 實作受控的 E2E offline 驗證模式（opt-in，新 §2.7a）；仍非 1a／1b 驗收通過，aggregate 未關。本輪主動工程工作上限 3 hr（上限非估點）**。
  - **S1 診斷證據的範圍更正（原文保留、附更正，見 §2.7）**：成立部分——外部 observer 的 `ps`／`lsof` 可讀身分、單 PID TERM 與埠釋放。**不成立部分**：腳本自印 `pgid=process.pid` **不是實測**（實際 `ps` 顯示 38502 的 pgid=**37744**）；**缺 lstart、未展示後代完整身分或群組停止** → **不得宣稱完整程序樹契約已驗證**。**Chrome 例證據不足**：使用的是**未指定 channel 的 Playwright 內建 Chromium**，非正式預設的系統 Google Chrome；loopback 頁面 fetch 外部得到的 `TypeError` **可能是 CORS**，缺同條件無 sandbox 的成功對照 → **不能單獨證明 Chrome 網路被阻斷**。
  - **前置小測試（納入授權）**：(1) 外部 observer 以 detached 啟動自家 sandbox 目標，**實測** pid／pgid／command／lstart 與後代關係，並經既有有界停止流程證明目標與埠釋放（不採腳本自印 PGID）；(2) 以**正式系統 Chrome**、相同啟動整合候選與相同網址，做**無 sandbox 成功／有 sandbox 阻斷／有 sandbox loopback 成功**三組對照，用頂層 navigation 或受控 CORS 允許端點排除 CORS，**不得**以 route 攔截器、DNS 改寫或離線模式製造紅燈；保留實際錯誤、browser argv／版本、profile 與命令。**兩項不成立即停止回報，不先跑完整 Wails**。
  - **實作授權範圍（新增 §2.7a「E2E offline 驗證模式（opt-in）」）**：只改 `frontend/e2e` 內必要 launcher、`processTree`、兩份 Playwright 設定、小型共用 support，加 profile 與必要文件；**明確 opt-in**，**預設路徑行為不變**；**harness／observer 在 sandbox 外**，Wails 與 app 後代、**正式系統 Chrome 及其後代**在**同一 loopback-only profile** 內；**不得把整個 Playwright worker 包進 sandbox**（會令 `ps` 失效）；Chrome 候選可用**本次 run 專用 executable wrapper**，須實際驗證 Playwright 整合、以 **exec** 保留 argv／pipe／信號、安全處理含空白路徑、**不得 shell 字串拼接**、**不得更動系統 Chrome**；wrapper／profile／executable 的路徑與 hash 要留證據；啟動前確認存在可執行，**不得失敗後靜默回退無 sandbox 瀏覽器**；**先只支援已驗證的系統 Chrome**，不支援的 `E2E_BROWSER` 組合須**啟動前明確失敗**。
  - **`-viteservertimeout` 併案核准**：**僅**在 offline 模式給 Wails 既有旗標 **60 s（候選值）**，預設模式不變；**不得宣稱 284 ms 已證明根因**。外層 startup 上限、**TERM 10 s／KILL 5 s**、埠與觀測失敗判定、網路 guard、artifact 規則**全部維持**；**不改 `main.go`、Wails／module cache 或主機系統設定**。
  - **1a 驗證順序**：兩項小測試 → `typecheck:e2e`／`selftest:all` → **新模式**下的正常啟動、啟動失敗／逾時／中斷（N7／N8）、ready 前後代處理（N10）、stale recovery（N11 受影響情境）、TERM→KILL（N13）。**重點是套上 sandbox wrapper 後仍能正確追蹤與收尾**，不是只在舊模式重跑；保留所有失敗。
  - **新快照要求**：manifest **必須同時收錄 tracked 與 untracked 的逐檔 hash、HEAD、彙總 diff，以及工具／profile／wrapper 版本**；原有固定快照檢查責任保留。由 reviewer 先複核 1a，再開始新版 1b。
  - **新版 1b 範圍**：新固定快照的**三次正式預設 smoke**、**controls A／B**、**完整 offline 模式的一次端到端成功及阻斷對照**、清理與證據完整性；**功能修改不得混入 1b**。已接受的 `084023Z` trace **保留為既有診斷能力證據**，若 spec／`expect.poll`／trace 設定未改可沿用並註明來源版本，**不必為錯誤計數再造一次失敗**；其餘未受影響的負控制依已核准規則沿用。**`073313Z` 的三次 smoke 保持有效歷史來源，但不是新版三次**。
  - **證據紀律**：接受 trace 更正與快照逐檔 hash 補強；**停止以 mtime 作決定性證明**。
  - **§4：分攤核定（無未分攤池）**：startup **9.55**；stop **19.50**（19.2＋N13 補跑 0.3）；L1 **3.60**（3.4＋註解更正 0.2）；lifecycle-2 **20.00**（18.05＋第十一輪其餘 1.1＋本輪 trace 0.2／總表 0.5／回報 0.15）→ **已達 20 hr 上限，後續 S1 實作與驗證另開票，不得塞回**；**S1 spike 0.60**（讀意見 0.1＋S1 診斷 0.5，**已完成**，不混入實作票）；browser **7.5**；1b 已知 **2.05**。**1a 合計 60.75**；**B3a-1 已知 62.80 hr／6.28 pt**；**B 軌 202.85 hr**；**全體 307.65 hr**（Decimal ROUND_HALF_UP 驗算相符）——**均不含未完成工作**；**S1 實作估算與 1b 剩餘仍須明列**；**不把 3 hr 授權上限當估點**，**不把已知量當完整 forecast**。
- **rev14**（2026-09-16）：依 codex-reviewer **複核 §2.7a offline 驗證模式實作**（mailroom #47）修訂——**架構、兩項前置對照、正常／N7／N8／N10／N13 成功路徑接受；1a 仍未通過**，另獨立重現兩個新缺口 **P1**（預檢未達宣稱契約）與 **P2**（config 載入前失敗無 harness.log）。**額外授權最多 1.5 hr 主動工作**（P1／P2 修正、小測試、N11、交付），**1b 仍不得啟動**。
  - **reviewer 驗證來源**：HEAD／diff SHA 與 `095245Z` 一致、MANIFEST 7/7、**tracked 逐檔 10 筆相符**；**untracked 實際 35 筆（不是先前所稱的 37 筆）**——工具區重複列出 wrapper／profile 導致重複計算，**文件如有引用一律更正**；Go race／Vitest／build／selftest 本輪核對既有封存證據、**未宣稱全部獨立重跑**；`typecheck:e2e` 由 reviewer 獨立執行 rc 0。
  - **P1（預檢未達宣稱契約）**：`offlineSandbox.ts:32–40` 對 profile 只做 `X_OK` 且**吞錯**、缺 `R_OK`／一般檔案檢查；`:65` 只查 `/usr/bin/sandbox-exec`**存在**未查**可執行**；**系統 Chrome 僅在 wrapper 執行時才檢查**（Wails 可能已啟動）。reviewer 以 fs mock 分別模擬三種壞情況，`validateOfflineSandboxPrereqs('chrome')` **全部通過**。修正契約：profile 須為**可讀的一般檔案**（**不對 `.sb` 要求 X_OK**、**不得吞存取錯誤**）；**wrapper／sandbox-exec／實際 Chrome 三者皆須在任何 Wails／Chrome 啟動前確認可執行**；**統一使用已驗證的絕對路徑**（`processTree` 目前 spawn 裸的 `sandbox-exec`，與預檢的 `/usr/bin/sandbox-exec` 解析契約不一致，可能命中 PATH 上另一支）；**新增小型負向測試**證明每個條件都在任何啟動前失敗（隔離路徑或 mock，不改系統檔）。
  - **P2（config 載入前失敗無 harness.log）**：reviewer 實跑不支援的 `E2E_BROWSER` 組合，rc 1 合理但**無新 artifact 目錄、無 harness.log**（globalSetup 尚未執行），**違反 §2.2「啟動前失敗也要留 log」**。修正契約：在 **`run-e2e` 入口、啟動 Playwright／載入 config 之前**建立證據目錄與早期 log，保存**失敗階段、原因、結束狀態**；**沿用同一 run-id／證據目錄**並與既有 logger 銜接避免覆寫；若從 config 直接執行需有**明確支援邊界**，**不得宣稱所有入口皆涵蓋**；**此階段無 trace 是預期**，不造 trace、不為取得 log 而啟動 app；負測須核對**非零 rc、harness.log 有原因、無新 Wails／Chrome／`.active-run.json` 殘留**。
  - **兩處表述收斂**：`placeholderCommand` 用實際 spawn 參數作占位合理，但 `sandbox-exec` 後續 `exec` 成 wails，**命令列仍會變**，**不得宣稱改用 `finalCommand` 已解決所有 A3 身分誤判**；仍依實測身分補強與 held `ChildProcess` 契約判定。**刪除未使用的 `wailsDevSandboxCommand`**（保留單一實際使用入口，不新增抽象層）。
  - **N13 紀錄紀律**：**兩次結果分開保留**——第一次觀測失敗且 `clean=false`、第二次成功；**不得抹掉第一筆或寫成連續穩定通過**。「不是新 bug」只能寫成「**尚未證明是新回歸**」，不得以表面相同當根因已確認。同一快照再遇同樣觀測失敗**須停止並分類**，不得反覆重跑湊成功。另須**補上殘留 fixture 如何核對身分後清理的證據來源**，不得只有「每次皆無殘留」的總結。
  - **1a 剩餘與順序**：P1／P2 小測試與 `typecheck:e2e`／`selftest:all` → offline 模式 **N11**（有效自家殘留可安全回收、身分不符不誤殺、損毀狀態不得判乾淨）→ 最後一次 offline 正常 smoke 確認預檢與 launcher 整合。**N7／N8／N10／N13 已讀成功證據保留**，僅在對應生命週期邏輯實際改動時重跑受影響項目。
  - **§4 帳表更新（reviewer 核定）**：本輪 **2.75 hr** 列為 S1 實作票的**回溯工程估算（非計時實績）**；已知 B3a-1：62.80 → **65.55 hr**；**B 軌 205.60 hr**；**全體 310.40 hr**（Decimal ROUND_HALF_UP 驗算相符：B3a-1 已知 65.55 hr／6.56 pt、B 軌 205.60 hr／20.56 pt、全體 310.40 hr／31.04 pt）。**授權上限（1.5 hr）不是估點**，不得寫成已投入；新版 1b 剩餘 **1.5–2.5 hr** 為估算區間、純等待另列，**仍未授權在 `095245Z` 啟動**，且**不得與已完成的舊版 1b 2.05 hr 混為同一批驗收**；**P1／P2 與 N11 的後續實際量另記，不塞回已滿 20 hr 的 lifecycle-2**。
- **rev15**（2026-09-16）：依 codex-reviewer **接受 P1／P2 修正與 N11a 的 root 存活回收**（mailroom #49）修訂——**1a 無新的實作阻擋**，給出「N11 應驗分支補齊即可直接開始新版 1b」的**條件式授權**（可直接開始，不需再請示）。
  - **reviewer 驗證來源**：`102306Z` tracked diff SHA 相符；**tracked 逐檔 10 筆、untracked 36 筆、MANIFEST 7/7 全部相符**；實跑新增的 **8 項 `offlineSandbox` selftest** 與 `typecheck:e2e` 均 rc 0；P2 亦實跑原反例，rc 1、新增目錄僅 harness.log、含實際拒絕原因、無 trace、無 active-run 指標；其餘固定檢查為**核對封存證據，未冒稱獨立重跑**。
  - **接受項**：P1 預檢契約（profile `R_OK`／`isFile`；三種執行檔 `X_OK`／`isFile`；spawn 與預檢統一絕對路徑）、P2 早期 log、N11a 的 root 存活回收。
  - **JS 入口與 TS 預檢的雙實作**：reviewer **接受現況、本輪不要求重構**，但**日後修改兩者必須維持一致**；**支援邊界**限於涵蓋 `test:e2e` 與 `test:e2e:controls` 的共同入口，**不得宣稱直接 playwright 入口也有早期 log**。
  - **N11 分支化驗收（取代先前的 1/3、3/3 寫法）**：
    - **N11a**：(i) root 存活回收（**已接受**，`100856Z`）；(ii) **root 已消失但自家後代仍存活**的 stale 回收（**與 N10 立即收尾不同**，**須有本輪 offline 模式新證據**）。
    - **N11b**：**root 身分不符**與**非 root 後代身分不符**兩種——**非零結束、保留診斷、完全不送信號、受控誘餌仍存活**。
    - **N11c**：**不可解析／缺必要欄位**與**可解析但結構無效（如空陣列）**兩類——**非零結束、不送信號、現有 pointer 保留不清除也不改寫**。
    - 做法：自家受控 fixture＋**真正的 stale recovery 入口**；保存 pointer／state 前後內容、身分與程序存活證據；**針對性驗證，不需每個負向情境再跑完整 smoke**；只清理自己建立的資料與誘餌；沿用舊證據須**逐項說明該分支未受 wrapper／入口變更影響**的理由。
  - **新版 1b 條件式授權（可直接開始，不需再請示）**：條件為 N11 應驗分支全數滿足、證據完整、**功能檔仍為 `102306Z` 已審內容（逐檔 hash 相同）**、無新的未知殘留或觀測失敗。屆時 1a 轉為「**補件條件達成、供整合驗收**」。文件與工時更新允許；須列出**相對 `102306Z` 的差異**並產生封存 manifest。**任一功能檔需修改即不適用**，須交新 diff 複核後才能啟動 1b。
  - **1b 範圍（維持 #45）**：三次正式預設 smoke（前輪不計入）、controls A／B（不得省略）、**offline 端到端成功與明確阻斷對照（必須本快照真正 offline 執行成功，不得以 curl 或 Node 小測試代替）**、清理與不可變證據封存。系統 Chrome／CORS 已排除的對照可沿用同一 wrapper／profile 內容的前置證據，**須明列 hash 與版本**；`expect.poll` 失敗 trace 依既定條件沿用並標來源版本；其他沿用負控制逐項列來源。**額外主動工作上限 2.5 hr**，剩餘估算仍 1.5–2.5 hr，純等待另列。
  - **§4 帳表更新（reviewer 核定）**：本輪 **1.4 hr** 回溯工程估算歸 S1 實作票（**S1 實作已知 4.15 hr**，2.75＋1.4）；**1a 合計 64.90 hr**；**B3a-1 已知量 66.95 hr／6.70 pt**；**B 軌 207.00 hr／20.70 pt**；**全部 311.80 hr／31.18 pt**（Decimal ROUND_HALF_UP 驗算相符）。**未含 N11 補件與新版 1b 剩餘，不是最終 forecast，不與先前預估重複相加，不計純等待**；**兩階段上限（N11 補件 0.75 hr、1b 2.5 hr）不是估點**；**lifecycle-2 維持 20 hr，不再加塞**。
- **rev16**（2026-09-16）：依 codex-reviewer **接受 `runState.ts`／`staleRun.ts` 語法相容性修改與 N11 補件、發現新阻擋 I1**（mailroom #51）修訂——**1a／1b 仍不放行，先前的條件式授權暫不恢復**。額外授權**最多 1.5 hr 主動工作**修正 I1。
  - **reviewer 驗證來源**：以保留的隔離副本直接 diff 確認 `runState.ts` 僅欄位宣告與 constructor 賦值、`staleRun.ts` 僅 type import；全量逐檔比對僅該兩功能檔與設計、backlog 有變；**獨立重跑 `typecheck:e2e` rc 0**。
  - **新阻擋 I1（ready 前中斷，teardown 未等待停止程序）**：`global-teardown.ts` 的 `readRunEnv` catch 分支**無條件 return**，只要 state 存在即宣稱「已在 teardownOnFailure 內完成」，**未檢查或等待 `runtime.processTree.stop` 的 single-flight promise**。reviewer 以該實際模組在「缺 run-env、有 interrupted state、processTree 可用」的受控條件呼叫 `globalTeardown`：**`stopCalls=0` 卻仍印已完成**。`103519Z` log 亦缺 TERM／KILL 完成、pid／埠核對與最終 cleanup 紀錄。**原 run 是否另有 Wails 訊號因素尚未證明，不得把原因全推給 sandbox 轉發。**
    - **修正契約**：確認 Playwright ready 前取消與 signal handler／globalTeardown 的**結束順序**（以可控延遲的 stop promise 小測試證明目前會提早返回）；**teardown 與 handler 等待同一套有界 single-flight 收尾**，**不得因 run-env 未寫出就跳過**；**保留 interrupted 終態**；**只有實際完成且 pid／埠／觀測結果符合才清 pointer**，不完整時**保留 state／pointer 並非零退出**；無 runtime 時走**既有安全身分核對／恢復契約**，**不得直接相信檔案中的 pid 去 kill**；預檢前未 spawn 者仍不需虛構停止工作；**不得只改 log 文字或拉長 timeout**，**不得新增第二套無界停止程序**。
    - **驗證限定**：小測試涵蓋「stop 未完成時 teardown 不得返回」「重複中斷只停止一次」「完成／失敗結果與 pointer 處理」；再做一次**新 offline 模式 ready 前 SIGINT 真實測試**，**記錄信號實際送給哪個 PID／PGID、角色與時間**，讓 TERM／KILL／埠核對**自然結束**，**不得數秒後人工 KILL 截斷證據**；**N7／N8 收尾路徑需確認無回歸**；手動緊急清理**只能在確認自家身分後**且**另記為非正常收尾、不得算 PASS**；上一輪 SIGINT 的敘述須從**實際命令**釐清（harness log 說「收到 SIGINT」與我方「送給 wails dev」的敘述不一致），**找不到原命令即標「未知」，不得追造**。
  - **三處必須更正（原文保留、附更正）**：
    - **「四者同 pgid 71315」錯誤**（此句為 controller 於 mailroom #50 交接時自行推論寫入，未核對 log）：實際 **app 73692 在 71315 群組**，**72455／72501／72584 為非同 pgid**，實際採 **group TERM 加個別 TERM**。
    - **N11b-1 原腳本未測到目標情境**：只改 `state.startedAt`、pointer 仍真值，先被「pointer 與 state 不一致」擋下，**未觸及「兩邊記錄一致但 root 實際身分不符」**。**reviewer 已自行補做正確反例**（隔離 tmp、正式 `handleStaleRun`、pointer 與 state 同為錯誤 1970 啟動時間）：`StaleRunConflictError` 正確、**signal 呼叫 0 次**、pointer 位元組不變、誘餌仍活。其證據將複製進我方證據區並**標明為 reviewer 獨立補驗**；**原反例保留但改標為「pointer／state 一致性測試」**。
    - **n11bc 腳本只印結果、無斷言、未捕捉 signal 呼叫**，**不得把 rc 0 包裝成自動驗收全過**；證據依**逐項人工判讀**呈現。N11b-2／c-1／c-2 的實際錯誤輸出與程式路徑 reviewer 接受。
  - **後續條件**：完成後交新 diff、限定驗證與固定快照；**仍需 reviewer 複核才能啟動 1b**，**先前的條件式授權暫不恢復**。**N11 已接受證據不必為報表全部重跑**；**僅當 I1 實際改到 stale recovery 才重驗受影響分支**。
  - **§4 帳表更新（reviewer 核定）**：0.75 hr 核定歸 S1（**S1 實作已知 4.90 hr**）；**1a 合計 65.65 hr**；**B3a-1 已知量 67.70 hr／6.77 pt**；**B 軌 207.75 hr／20.78 pt**；**全部 312.55 hr／31.26 pt**（Decimal ROUND_HALF_UP 驗算相符）。**尚不含 I1 與新版 1b 剩餘；1.5 hr 為上限、非已發生估點；不與先前預估重複相加；不計純等待**；**lifecycle-2 維持 20 hr，不再加塞**。
- **rev17**（2026-09-16）：依 codex-reviewer **複核 I1 修正**（mailroom #53）修訂——**I1 的「等待既有 stop promise」主路徑修正成立，但整份 diff 尚未通過；1a 不放行；1b 條件式授權仍不恢復**。（註：mailroom 編號由 controller 於交付前更正——裁定訊息為 #53，#52 是 claude-worker 送出的交接訊息，原稿誤植為 #52）
  - **reviewer 驗證來源**：獨立核對 tracked 10／untracked 37 逐檔雜湊、aggregate diff `d6398387…`、MANIFEST 7/7；重跑新自測 6 項通過；讀過四次原始 harness 紀錄。**N11 先前已接受的結論不撤回**。
  - **缺陷 A（必修，§2.7a／`global-teardown.ts` 約 358 行）**：先看 `run-state.json` 是否存在就直接 return，排在 `runtime.processTree` 之前。以實際 `globalTeardown` 公開入口重現的隔離案例 `missing-state-live-runtime`：有 runtime 與 active pointer、state 遺失 → `stopCalls=0`、正常返回，log 卻宣稱 app 從未啟動。**現行自測「測項四」把「有 runtime 卻不呼叫 stop」當成預期，等於固化缺陷，需一併修正**；狀態檔不存在不能證明沒有 spawn。
  - **缺陷 B（必修，同檔約 403 行）**：跨行程 fallback 只 `JSON.parse` 後直接 `stopProcessGroup`，缺 staleRun 對 pointer／state 的完整結構與一致性核對。隔離案例 `empty-processes-no-runtime`：保留有效 pointer、`state.processes=[]` → log clean=true、portsReleased=true、**pointer 被刪除**（恢復入口遺失），雖最後仍 throw interrupted。**違反既有「空清單／損毀資料保留指標」契約**。約 70 行 env 存在的純檔案分支有相同直接解析模式，需套同一檢核，避免兩套標準。
  - **重現材料**：保存於 `frontend/e2e/.artifacts/n11-tests/codex-review52-repro/`（`probe.mjs` sha256 `ef9beb926d944f51c42968df657d69792d4b586a12b2dbf919e69435cc9119b6`、`result.json` sha256 `faac8610f18686b3ae523b6fcfea810b2b377a4838007ba2566cef9e725f5852`，另含兩份隔離 artifacts）。**probe 攔截信號發送（signalCalls 均為 0）、未啟動 app、只讀查埠；probe rc=0 不得解讀為產品 PASS**。
  - **新授權（有界修正）**：**active effort 上限 1.0 hr**（等待不計；預見超時先停並報剩餘事項）；要求沿用既有驗證器與停止演算法，**不得新增 production 修改、不得開始 1b**。補測範圍：缺 state ＋ live runtime 仍 await stop；真正預檢未 spawn；無 runtime 的空 processes／pointer-state 衝突／壞 JSON 均拒絕且保留 pointer、零信號；合法 fallback 只在程序與埠都乾淨時清 pointer。最終版要跑相關自測／typecheck，並做一次 ready 前 SIGINT 與一次 N7 啟動逾時確認主流程；ready 後 SIGINT 與其他負控制可明列來源沿用。
  - **快照裁定（往後適用的證據規則）**：接受 `b3a1a-snapshot-20260916T111536Z` 中**已明列 112144Z 更正時間與原因的修正版 manifest** 作為本次比對依據，但**不得稱為 111536Z 原封不動封存**。**往後封存後的更正要另建 revision／addendum、保留原版，不得原地重寫已封存 manifest**。
  - **§4 帳表更新（reviewer 核定）**：本輪 I1 實際 **1.6 hr 全數照實列入 S1 implementation**，其中 **0.1 hr 明列為超出 1.5 hr 上限的超支，不得吸收或改寫成 1.5**。更新後：**S1 implementation = 6.50 hr**（原 4.90 ＋ 1.60）；**1a 合計 = 67.25 hr**；**B3a-1 已知 = 69.30 hr／6.93 pt**（原 67.70 ＋ 1.60）；**B 軌 = 209.35 hr／20.94 pt**（原 207.75 ＋ 1.60）；**全體總計 = 314.15 hr／31.42 pt**（原 312.55 ＋ 1.60，Decimal ROUND_HALF_UP 驗算相符）。**這些數字不包含本次新授權的最多 1.0 hr，也不包含尚未執行的 1b；不得把新上限當成已發生工時**；**下次超上限前須先報告**。**1a／1b 仍未通過，aggregate 未關票**。
- **rev18**（2026-09-16）：依 codex-reviewer **裁定 #56 的執行歸屬修正通過**（mailroom #57）修訂——**1a 功能審查放行，恢復 1b 驗證授權；這不是 B3a-1 全案驗收完成，aggregate 繼續開啟；仍無提交／推送／PR／合併授權；1b 沒有新的 production 修改授權（StartHidden 屬既有已核准功能）**。
  - **reviewer 本次獨立完成的查證**：HEAD `023481440017fea4e596f039258e35259f34ca8f`；快照 `b3a1a-snapshot-20260916T120427Z` 的 47 個逐檔雜湊全符、MANIFEST 8/8 全符；相對 `114515Z` 只有 `global-teardown.ts` 與 `globalTeardownStop.selftest.ts` 變動；三份沿用 log 與來源位元組完全相同，**接受部分重跑與 reuse-basis 做法**；自行重跑 `globalTeardownStop` 自測 14/14 rc=0、typecheck rc=0；自行在全新 `/tmp` 目錄重跑五案探針（missing-state-with-pointer／pointer-to-other-run／occupied-port／root-conflict／bad-json）均 rejected、pointer 保留、signalCalls=0；其 loopback listener 已正常關閉，repo 的 `.active-run.json` 現不存在。新證據保存於 `frontend/e2e/.artifacts/n11-tests/codex-review56-repro/`。
  - **明列限制**：env 存在入口的完整純檔案**正向**流程仍未單獨實測，reviewer 接受作為明列限制，不要求為此再造完整 fixture；**不得宣稱「所有 fallback 組合均經真實 E2E 驗證」**。
  - **已完成的措辭收斂（controller 於 1b 開工前執行，純文字、未動控制流程）**：`global-teardown.ts` 的 `!stateExists` no-pointer return 分支 log、相鄰註解與對應自測名稱，原本寫成「確定 app 從未啟動」，已改為「未取得本次程序追蹤紀錄及殘留指標，本分支未執行停止；執行結果依 setup 失敗回報，不能據此證明從未啟動」。另更正一項回報用語：`canonicalizePath` 是**catch realpath 例外後退回 `path.resolve`**，先前回報寫成「不吞例外」並不精確，應精確描述為既有 fallback。改完 typecheck rc=0、`selftest:all` 54 項 rc=0。
  - **最終功能快照**：`b3a1a-snapshot-20260916T121017Z`（1b 綁定此份）。部分重跑：gofmt／typecheck／`selftest:all`（54 項）為本輪重跑；go race／vitest／build 沿用 `120427Z`（附來源路徑與雜湊，並註明為位元組複本、非新執行），另有 `reuse-basis.txt` 以逐檔雜湊證明變動僅限兩個 e2e TypeScript 檔。
  - **1b 授權內容（active effort 上限 2.5 hr，含上述文字整理；等待另計不算工時）**：
    1. 同一固定功能快照上三次正式預設 smoke 全部成功；舊 smoke 僅歷史參考，不能湊次數。
    2. 獨立指令 controls A／B 必做；舊判定按設計失敗、新判定成功，且**失敗原因必須符合受控延遲**，不得用任意失敗代替。
    3. 完整一次 offline E2E 成功及阻斷外網控制證據；既有 profile／wrapper／Chrome 阻斷控制若雜湊與工具版本不變可明列來源沿用，但本輪完整 offline 成功仍需執行；**保留每秒取樣的觀測邊界**，不把抽樣未見流量寫成全面證明。
    4. `expect.poll` 失敗 trace 證據若 spec／poll／trace 設定未變可附來源與雜湊沿用；**需能指出 trace 內實際可見的錯誤內容**，沒看到就列限制，**不得用 Node stderr 代替 trace 證據**。
    5. 每次執行後核對 pid、實際用到的埠與 pointer；異常走既有有界停止；檔案完整性、tripwire、network 等判定照設計執行；**writer 全部關閉後再封存**，manifest 自驗，保留所有失敗證據與 check reuse mapping。
    6. 1b 只做驗證，不追加功能／production 修改；失敗先保留與分類，**不反覆重試到綠**；需要改碼或預見超 2.5 hr 先回報；完成後交 reviewer 做全案驗收，不自行結案。開工前仍需自行預檢程序／埠／pointer，遇他人占埠直接失敗、不動占用者。
  - **§4 帳表更新（reviewer 核定）**：本輪（歸屬修正）實測 11:55:13–12:02:06＝413 秒＝**約 0.115 hr**（顯示可四捨五入為 0.11 hr；**active 約等於 wall clock 是估計，未逐秒量測 active**）。**S1 implementation = 6.615 hr**（原 6.50 ＋ 0.115）；**1a 合計 = 67.365 hr**；**B3a-1 已知 = 69.415 hr／6.94 pt**；**B 軌 = 209.465 hr／20.95 pt**；**全體 314.265 hr／31.43 pt**（Decimal ROUND_HALF_UP 驗算相符；**保留未四捨五入的原始秒數計算〔413 秒〕，報表數字由同一公式產生，避免加總漂移**）。
  - **上一輪未核實回溯估算（獨立欄位，不併入上列數字）**：上一輪（歸屬修正前的整理階段）估計耗時約 55–70 分鐘，**未核實**；controller 實測其中兩次 harness 執行的起訖時間，合計約 47 秒——**這只證明那兩次執行本身的時間範圍，不能倒推整輪其餘時間全為 active**；此欄位維持獨立列示，**不抹除、不任選落點、不混入上列已核定的 hr 基底**。
  - **仍未通過項**：這不是 B3a-1 全案驗收完成，**aggregate 繼續開啟**；**本次 2.5 hr 為新增驗證上限，未發生前不得入帳**；**下次超上限前須先報告**。
- **rev19**（2026-09-17）：依 codex-reviewer **mailroom #65 最終裁定**修訂——**B3a-1 技術驗收通過**；1a／1b 本次工程交付責任達成；**aggregate 標為技術驗收完成**，但**未提交、未推送、未開 PR、未合併**；**B3a-2／B3a-CI 及其他票不因此通過，也未獲施工授權**。
  - **受測功能快照**：`b3a1a-snapshot-20260917T055531Z`（HEAD `023481440017fea4e596f039258e35259f34ca8f`），49 檔逐檔雜湊相符。
  - **三個正式 run-id（各一次、首次即通過）**：
    1. A 預設 smoke（未設 `E2E_KEEP_ARTIFACTS`，走自動刪除分支）：`20260917T061013Z-2ddd9d`，rc=0。
    2. B controls（`E2E_KEEP_ARTIFACTS=1`）：`20260917T062716Z-b25f60`，rc=0。
    3. C offline（`E2E_OFFLINE_SANDBOX=1`＋`KEEP_ARTIFACTS=1`＋`DEBUG=pw:browser,pw:api`）：`20260917T063037Z-2cca8a`，rc=0。

       三者皆 `cleanupClean=true`、`overallFailed=false`、`artifactViolations=0`、watchdog `stopSignalSent=0`。外部側錄批次 `frontend/e2e/.artifacts/1b-abc2-20260917T060356Z/`，MANIFEST 53/53 自驗相符。
  - **reviewer 獨立複核範圍**（reviewer 親自執行，非本方回報）：HEAD 與 49 檔相符、53/53 相符、三份原始 stdout 的判定欄位、B 的 control A／B 原始證據（磁碟 sha 等於 baseline、延遲 2001ms／完整內容相等、2002ms）、C 的 `sandbox-exec`／`chromeWrapper.sh` 啟動與零 browser 違規、當下無 listener／無 pointer／無殘留程序、watchdog 短測原始結果。
  - **Attempt 0（照實保留，不得美化）**：本批次先有一次 watchdog PATH 造成的啟動失敗（外部監看工具自身環境缺陷，`spawn wails ENOENT`），排除後三次正式驗證均通過。**不得從歷史刪除，也不得概括成「整個批次首次就零失敗」**。「首次失敗即停」規則**沒有**一般性的「工具錯誤就可自動重試」豁免；此次是依已保存的明確環境歸因被接受，**不構成未來跳過停止點的授權**。
  - **階段觀察（只寫觀察，不得寫成原因）**：直接時間戳確立「globalSetup 完成 → Running 行」的差異為 A 約 12m24s、C 約 11m15s、B 約 4.5s。**不得**推論死結、資源競爭或 Chrome 冷啟動。C 的 browser launch 側錄出現在 Running **之後**，因此**不得**把 Running 之前的空窗稱為已證明的 Chrome 啟動耗時。這屬剩餘效能／診斷限制，不阻擋本次驗收，**本票不繼續優化或追加測試**。
  - **其餘限制照實保留**：A 成功後目錄依預設刪除、證據僅到外部側錄；C 的網路阻斷為每秒取樣沿用歷史控制，**不得**擴張為封包層級全程證明；reverse-check 依既有 env gate `skipped`（本次未執行，**不得**新增一次全綠宣稱）。
  - **工時（已量測／估計／未量測分開，不得虛構精確總工時）**：`06:10:11Z–06:44:16Z` 涵蓋 A/B/C 及其間隔操作，**不得**把整段當純等待之後又另外加計間隔 active；已知區間與實際操作紀錄去掉重疊，不足部分**保持未知**。原 **1.4 pt 只留原始估算**，不得冒充最終實績。先前 413 秒、55–70 分鐘未核實欄、1b active 未完整量測欄一律保留；**本輪不重算 B3a-1 已知量／B 軌／全體合計，rev18 核定的 69.415 hr／6.94 pt 等數字維持不動、不因本次技術驗收通過而自動遞增**。
  - **文件雜湊**：本次結案文件（design.md rev19、backlog.md rev63）另列 revision 與 hash（見文件末／backlog 對應段落），**不改寫受測快照**、**不因純文件變動重跑功能測試**。
  - **狀態更新**：先前各輪「阻擋中／未通過／aggregate 繼續開啟」等敘述更新為現況（技術驗收通過），**歷史失敗與歷史修訂（rev1–rev18）原文保留**，不得因本次結案而被誤讀為從未發生。
  - **保留事項**：PoC 暫存與各失敗原始證據繼續保留；本次結案**不含**清除暫存、git 提交或發布。

## 0. 範圍與界線

- **B3a-1**：
  - 一個可重跑入口、一條 smoke flow、失敗證據保存方式
  - 啟動前的隔離預檢與啟動後核對
  - 異常收尾契約
  - tripwire 型 fake provider（保證不呼叫真實模型服務）
- **不在 B3a-1**：
  - replay provider 與 B3a-2 的核心流程（§4 拆票）
  - CI／macOS runner（另開可行性小票）
  - required checks 修改、分支清理
- **Browser E2E 與 native GUI 分開標示**（§2.10）

## 1. 事實基礎

| # | 事實 | 來源 |
|---|---|---|
| F1 | 前端 binding 直接呼叫 `window.go.main.App.*`（例如 `SpecWrite` 在 `frontend/wailsjs/go/main/App.js:181-182`）；前端**沒有替身開關**，vitest 的 `vi.mock` 對真瀏覽器無效 → browser E2E 必須真的跑 `wails dev`＋真 Go backend | 盤點；controller grep |
| F2 | 過去 M0–M3b 的 UI 驗收都是 `wails dev`＋`localhost:34115`＋Playwright 即時操作，repo 無可重跑腳本 | `docs/spikes/m3b-results.md` §11／§12（controller 親讀） |
| F3 | `resolveWorkspace`（`app.go:3246-3279`）候選依序為：`WORKBENCH_WORKSPACE` → 可寫的 cwd（非 `/`）→ HOME → `os.TempDir()`；**第一個能建出 `.workbench` 的勝出**。env 路徑無效或不可寫時會**無聲退回**。在 repo 根目錄啟動 `wails dev` 時，cwd 就是 repo 本身 | controller 親讀 |
| F4 | `resolveToolsDir`（`app.go:3288-3298`）：有設 `WORKBENCH_TOOLS_DIR` 就**直接採用，不檢查是否存在** | controller 親讀 |
| F5 | `CLIInfo()`（`app.go:3496-3505`）回傳 `toolsDir`、`toolsSource`、`claudeVersion`、`codexVersion`、`workspace`、`workspaceSource`、`startupError`、`ready`；標題列只顯示其中幾項（`App.vue:378`） | controller 親讀 |
| F6 | 既有 `testdata/fake-claude.sh` 具備 init、stream delta、result，以及 `FAKE_MULTI` 多輪回應，另有多種故障模式；`fake-codex-appserver.sh` 只做 handshake。**兩者是否足以支援 B3a-2 的目標流程（session recovery、approval）尚未證明** | controller 親讀 `testdata/fake-claude.sh:1-23` |
| F7 | 本次 PoC 在隔離 worktree 跑完 `wails dev` 後，**未觀察到受版控檔案變動**（`git status` 為空） | controller 核對 PoC worktree |
| F8 | Go 端直接取 HOME 的地方只見於 `resolveWorkspace` 的後備候選（`app.go:3255`），env 無效時就會用到。另外，`childEnvFor`（`app.go:3333-3338`）只在 PATH 前面補上 node 目錄，其餘環境變數原樣繼承；app 啟動的 git 子程序（例如 `app.go:5801` 的 `git config`）會依 HOME 讀取使用者的 git 設定。**不能宣稱 app 不讀 HOME** | controller grep／親讀 |
| F9 | `wails dev -help` **沒有「不開 native 視窗」的旗標**；有 `-noreload`、`-nogorebuild`、`-skipbindings`、`-devserver` | PoC `wails-dev-help.txt` |
| F10 | CI 四個 job 都沒有瀏覽器步驟；`go`、`wails-build` 在 `macos-15-intel`，但從未跑過 `wails dev` | `.github/workflows/ci.yml`（盤點） |

### PoC 結果（證據 `~/.local/share/sdlc-evidence/b3a-1/02348144…/poc-20260914T104817Z/`，manifest 24/24，controller 已核對）

- **可行**：
  - `wails dev` 由腳本在背景啟動；冷啟動 180 s、熱啟動 47 s；送 SIGTERM 後乾淨結束，34115 與 5173 兩個埠都釋放。
  - `playwright-core@1.63.0` 以 `channel: 'chrome'` 使用系統 Chrome，未下載瀏覽器；smoke 連跑兩次，結束碼都是 0；刻意失敗那次結束碼為 1，留下失敗截圖與 trace。
- **呼叫紀錄**：假 CLI 的 `invocations.log` 共 6 筆，每筆都只帶 `--version`。
- **網路觀察範圍**：只有**一次** `lsof` 快照，當下 app 程序樹只有 `127.0.0.1:5173` 與 `127.0.0.1:34115` 兩個連線。**單次快照不能證明全程只連 localhost**，全程驗證方式見 §2.7。
- **HOME 設定檔**：沒有先記錄 mtime 基準；`~/.claude.json` 的 mtime 落在測試期間，**成因未證實**。
- **疑似競態（race）**：有一次畫面已是新內容，磁碟卻還是舊的。
  - Spec 儲存鈕的條件是 `:disabled="busyReason !== '' || !dirty"`（`SpecWorkspace.vue:749`），儲存進行中本來就是 disabled。
  - PoC 腳本把「按鈕變 disabled」當作存檔完成，這個判定訊號有缺陷。
  - **這不代表已證實那次失敗的根因**。

### 失敗 trace 實際開啟（證據 `~/.local/share/sdlc-evidence/b3a-1/02348144…/trace-open-20260915T041221Z/`，manifest 5/5）

- **開啟方式**：
  - 在 `/tmp/b3a1-pw` 以 `npx playwright@1.63.0 show-trace --host 127.0.0.1 --port 9323` 開啟 PoC 的失敗 trace 副本（sha256 與原檔相同，原檔未動）。
  - 用系統 Chrome 截圖。
  - 結束後 9323 已釋放。
- **可供診斷**：
  - 左側動作清單完整：等待、點擊、`ControlOrMeta+A`、插入文字、點儲存、截圖。
  - 中間有動作前後的 DOM 快照：看得到編輯器內容 `WRONG-poc-marker…`，儲存鈕呈停用狀態。
  - 另有 Console、Network、Errors 等分頁。
- **限制**：PoC 的失敗斷言是在 Node 端以 `fs` 比對後丟出例外，**不是 Playwright 的斷言，錯誤訊息本身不在 trace 裡**。第一個「Wait for function」動作帶一個錯誤標記，內容未展開檢視。
  - 設計因此改用 `@playwright/test` 的 `expect.poll` 做磁碟核對（§2.4）；錯誤能否進 trace，**施工時驗證**。

## 2. 設計

### 2.1 可重跑入口與執行設定

```
cd frontend && npm run test:e2e
```

- `@playwright/test` 以固定版本加入 `frontend/package.json` devDependencies；設定檔 `frontend/e2e/playwright.config.ts`，測試放在 `frontend/e2e/*.spec.ts`。
- **單一 worker、零重試**：`workers: 1`、`retries: 0`、`fullyParallel: false`。
- **瀏覽器**：
  - 本機預設 `channel: 'chrome'`（系統 Chrome）
  - `E2E_BROWSER=chromium` 時改用 Playwright 內建 Chromium，須在準備階段 `npx playwright install chromium`
- **依賴準備與執行分離**（依「不需外部網路」裁定）：
  - 準備階段：`npm ci`、瀏覽器、`go mod download`
  - 執行階段對 `wails dev` 設 `GOFLAGS=-mod=readonly`（取代 rev2 的 `-mod=mod`（待評估），避免執行期改寫 `go.mod`／`go.sum`）、`GOPROXY=off`、`npm_config_offline=true`
  - `GOPROXY=off`、`npm_config_offline=true` 只是**輔助設定**，讓已知的下載路徑明確失敗，**不等於完整網路隔離**；完整隔離的驗證方式見 §2.7 的 sandbox-exec 阻斷實測
  - 這幾個設定能否與 wails dev 相容，**施工時驗證**

### 2.2 隔離：啟動前預檢與啟動後核對

**啟動前預檢**：任何一項不符，就在 spawn `wails dev` **之前**失敗，並寫入 `harness.log`。

1. fixture 由 `mkdtemp` 建立，取 `realpath`（macOS 的 `/tmp` 實際是 `/private/tmp`）。確認以下各項：
   - 是目錄
   - 可寫：建立並刪除一個探測檔
   - 能建立 `.workbench`
   - 是 git repo 且有初始 commit
   - **realpath 不在 repo 內，也不等於 HOME**
2. 假 tools 目錄下的兩支假 CLI 必須都存在、可執行。
   - 預檢時各執行一次 `--version`，輸出必須等於本次執行專屬的假版本字串，例如 `fake-claude 0.0.0-e2e+<run-id>`。
   - 預檢產生的呼叫紀錄另存為 `preflight-invocations.log`，並清空正式的 `invocations.log`。
3. `WORKBENCH_WORKSPACE`、`WORKBENCH_TOOLS_DIR` 兩個 env 值必須等於上述兩條路徑。**缺一即失敗，不啟動 app。**
4. 34115 埠已被佔用 → 失敗，**不終止佔用者**（§2.3）。

**啟動後核對**：在頁面內呼叫 `window.go.main.App.CLIInfo()`，並等 `ready === "true"`。

- `workspace` 等於 fixture 的 realpath，`workspaceSource === "env"`
- `toolsDir` 等於假 tools 目錄，`toolsSource === "env"`
- `claudeVersion`、`codexVersion` 等於本次執行的假版本字串
- `startupError` 為空

**只看標題列的 `tools: env` 不足以證明用的是預期的假 CLI**，所以改以上述欄位逐項核對。

### 2.2a E2E 期間的原生視窗（限定 production 授權，rev8）

**背景**：owner 雙螢幕作業，E2E 每次都會開出原生視窗造成干擾。owner 直接裁定先做「E2E 期間以環境旗標控制 Wails `StartHidden`」的最小驗證，reviewer（codex-reviewer，mailroom #32／#33，2026-09-16）已正式授權。**這是對「production 僅允許加 `data-test`」邊界的限定擴充**——僅限本節所列的旗標接線、測試啟動器、文件與驗證，**不是把該邊界整體放寬**；production 其餘部分仍只能加 `data-test`。

**owner 更正（必須明列，不得省略或簡化）**：`StartHidden` 是**隱藏視窗**，**不是取消建立視窗**；Wails v2.13 的 macOS 端仍會**無條件**呼叫 `activateIgnoringOtherApps:YES`（本機模組快取佐證：`internal/frontend/desktop/darwin/AppDelegate.m:54`、`WailsContext.m:425`、`:434`；`StartHidden` 接線於 `window.go:60`），**因此不能保證不搶焦點**。**本節與後續證據不得寫成「已解決搶焦點」**——只能依實測結果寫「未驗證」，或在仍搶焦點時寫「此方案只解決顯示干擾」。

**旗標**：`WORKBENCH_E2E_START_HIDDEN`。

- **值恰為 `1` 才啟用**；未設定或其他任何值，皆維持現行行為（正常開窗）。
- **只設定在 E2E 啟動器給 app 子程序的 env**；**不得**寫入全域 shell 設定檔、使用者設定，或一般開發用（非 E2E）的啟動設定。

**範圍與禁止項**：

- **範圍僅限**：旗標接線（讀取旗標並傳入 Wails `StartHidden` 選項）、測試啟動器（E2E harness 如何設定／不設定該 env）、文件、驗證。
- **禁止**：安裝視窗管理工具；修改 macOS 全域偏好設定；patch 或升級 Wails；新增任何「自動搶回焦點」的行為；改動單一實例鎖、approval 流程或 provider 相關邏輯。

**驗收條件**：

- **hidden 模式**（`WORKBENCH_E2E_START_HIDDEN=1`）：
  - 佐證旗標**實際生效**——以 app 子程序的 env（而非呼叫端設定或程式碼推論）為憑。
  - 原生視窗**未顯示**。
  - 34115 與 `CLIInfo()` 正常（依 §2.2 既有啟動後核對）。
  - 完整內容存檔 smoke（§2.4）通過。
  - 程序與埠收尾正常（依 §2.3 既有停止程序）。
- **unset 模式**（不設定該旗標）：
  - 需**實際觀察到正常開窗**（不是「預期會開窗所以省略觀察」），並完整收尾。
  - 須確認 app 子程序的 env **確實沒有**該旗標——**不得只靠呼叫端 unset 就認定子程序也沒有**（例如需核對子程序實際繼承到的環境變數）。
  - **不得以布林單元測試代替實際開窗檢查**——單元測試可以驗證旗標讀取邏輯，但不能取代對「視窗有沒有真的顯示」的實際觀察。

**焦點與可見性分開報告**：

- 記錄啟動前／期間／後的**前景 app**、觀察方式（例如 `NSWorkspace.frontmostApplication` 或等效手段）與時間點。
- **不得為了量測而送鍵盤事件，也不得反覆切換使用者正在工作的視窗**——觀察方式本身不能干擾 owner 當下的工作。
- **無法可靠觀測就照實寫「未驗證」，不得寫「不搶焦點」**；若實測仍出現搶焦點，結論只能寫「**此方案只解決顯示干擾**」，不得誇大為解決焦點問題。

**範圍限制**：此 PoC smoke **不算** B3a-1b 的三次正式連跑；既有的 A–C 剩餘修正、負控制與版控產物檢查規則**不因此豁免**。

**證據更正（rev9，依 B3a-1a 第三次複核 mailroom #35 裁定）**：先前 hidden 模式證據寫「全程 0 個視窗」——這句話**超出單點查詢的實際觀察範圍**（單次或有限次的查詢無法證明「全程」都是 0 個視窗，與 §2.7 網路取樣「取樣結果只能說明取樣時點沒有外連」是同一類問題）。**已附更正**，並保留原始查詢輸出（不刪除、不覆寫）；後續報告須依實際查詢頻率與涵蓋範圍如實描述，不得用「全程」這類超出證據支持範圍的措辭。

### 2.3 程序生命週期與異常收尾契約（依審查補充 A 改寫）

**狀態檔**：`<證據目錄>/run-state.json`，另在 `frontend/e2e/.artifacts/.active-run.json` 記錄指向本次執行的指標。**spawn `wails dev` 的當下、不等 ready**，立即把以下內容寫入狀態檔：

- `wails dev` 的 pid、pgid、啟動命令、啟動時間（用於身分核對，見下）
- 之後每偵測到一個後代程序（含非同 pgid 的後代），即時追加其 pid、ppid、pgid、啟動命令、啟動時間
- ready 後補上以 `lsof` 取得的自家程序樹各 pid 各自 LISTEN 的埠

**啟動**：以獨立程序群組（`detached: true`）啟動 `wails dev -noreload`。從 spawn 起持續走訪 spawn pid 的**整棵後代樹**（不只等 ready 後才收集），把每個新出現的後代（含不在同一 pgid 的）都寫入狀態檔並個別標記其 pgid 歸屬。

**啟動階段的可靠持有（R2，rev6，依 B3a-1a 複核 mailroom #14 裁定）**：一旦 spawn 呼叫取得 child（即使還沒 ready），就要**立即具備可靠的持有與收尾路徑**——不能有「取得 child 但尚未進入可控狀態」的空窗。

- **身分觀測失敗要明確記為失敗**：spawn 後第一次觀測 child 身分（pid／啟動時間／命令列／pgid）若失敗，就直接記為觀測失敗並走失敗收尾；**不得用虛構的時間或命令列冒充身分證據**去湊出一筆看似完整的記錄。
- **所有 spawn 之後的啟動階段例外都走 single-flight 的有上限停止程序**：不允許同一次執行並行觸發兩條停止路徑（例如 N8 曾出現的 SIGINT 雙停止程序並行、teardown 誤判 PASSED 的迴歸），一律收斂到單一入口的停止程序，且維持既有的時間上限。
- **負控制**：預檢成功、spawn 成功，**第一次 post-spawn 的 `ps` 才失敗**——驗證這個時間點的觀測失敗會被正確記錄並觸發收尾，不會被忽略或冒充成功。
- **範圍聲明**：這一項是**程式路徑檢查**（模擬 spawn 後第一次觀測失敗的程式邏輯），**未對真實 Wails 做故障注入**；證據與報告要標明這個邊界，不得宣稱已對真實 `wails dev` 啟動失敗做過端到端驗證。

**R2 迴歸修正，rev7 補正（依 B3a-1a 第二次複核 mailroom #31 裁定 B，屬 B3a-1a-lifecycle-1 剩餘修正）**：

- **兩份持久資料的一致性**：root 身分更新（例如取得或修正 pid／pgid／啟動時間等身分欄位）必須讓 **`run-state.json` 與 `.active-run.json` 兩份持久資料保持一致**——不得只更新其中一份就視為完成；更新途中若失敗，**不得留下會被誤信的狀態**（例如一份已更新、另一份仍是舊值、但後續邏輯誤以為兩者同步）。
- **placeholder 與已驗證身分要明確區分**：狀態檔中「尚未驗證、暫時占位」的記錄與「已核對身分四要素、確認存活」的記錄，資料結構或欄位上要能明確分辨，不能讓下游邏輯把 placeholder 誤當已驗證身分使用（這與 A 的「不得把歷史追蹤清單當成已驗證」是同一類風險，在啟動階段的落地點）。
- **測試方式**：驗證這條路徑的測試，要**由正式的寫入 API 產生 state／pointer**（走 harness 實際會走的程式碼路徑），**再模擬中斷與接續清理**；**不得只用手工準備的一致 fixture**（直接寫一份看起來正確的 JSON）去繞過正式寫入路徑，那樣無法驗證「正式寫入 API 本身是否會產生不一致」。
- **post-spawn 的 catch 路徑**：spawn 之後若進入例外處理（catch），即使 log 或 `setStatus` 之類的輔助呼叫本身失敗，**仍須執行有上限的清理**（不能因為輔助呼叫失敗就連清理本身都放棄或無上限卡住）。

**停止程序（R4，取代 rev3 的簡單等待版本）**：所有結束路徑共用，全程有上限，改用**有上限的批次查詢加單調時鐘**（`process.hrtime.bigint()` 或等效），TERM 與 KILL 兩階段耗時分別記錄，埠核對另計、不併入 TERM grace period：

1. 對狀態檔記錄的主 pgid 送 SIGTERM，**同時**對狀態檔中歸屬於非主 pgid 的每個後代個別送 SIGTERM；以批次查詢（單調時鐘量測）等待，最多 10 s；記錄本階段實際耗時
2. 仍有殘存 → 對主 pgid 送 SIGKILL，並對殘存的非同 pgid 後代個別送 SIGKILL；同樣以批次查詢等待，最多 5 s；記錄本階段實際耗時（與 TERM 階段分開記錄，不相加後才比對上限）
3. 埠核對（獨立於上述兩階段的計時，於程序確認消失後才做）：
   - 狀態檔記錄的 pid 全部消失
   - 這些 pid 原本 LISTEN 的埠（34115，以及 vite 實際使用的埠）都已釋放
4. 任一項不符 → 以「cleanup incomplete」失敗並列出殘存 pid 與埠，**不做其他處置**
5. 收尾確認全部乾淨（程序與埠皆核對通過）→ **清除 `.active-run.json`**；cleanup incomplete → **保留 `.active-run.json`** 供下次啟動辨識殘留

**停止程序預算的語意（rev5，節點 3 審查裁定）**：TERM 10 s、KILL 5 s 是**等待預算**（budget），不是「保證在這個時間內完成」的即時性承諾。實作中若加了 `+100 ms` 之類的量測容差，那**只是量測容差**（給批次查詢一點緩衝，避免時鐘精度造成誤判），**不是額外的等待額度，也不是 OS 對排程的硬即時保證**——不得把容差寫成「預算+容差」的新上限來放寬判定。外部查詢（`ps`／`lsof`）本身的逾時要**受剩餘預算限制**（不能讓一次查詢逾時吃光整個階段預算之外的時間）。TERM、KILL、埠核對三段**分別報告耗時**；任何一段超過其自身預算加量測容差，就照實列為失敗或標為需分析，不得含混合併判定（節點 3 審查發現 N7 重跑的 TERM 階段耗時 10327 ms，已超出 10 s 上限，須列為缺口而非忽略）。

**只終止狀態檔記錄的 pid（含自家程序群組與非同 pgid 的追蹤後代）**；對任何非自家程序一律不送信號。

**送信號前的一致性驗證（R1，rev6，依 B3a-1a 複核 mailroom #14 裁定，取代 rev3/rev5 對「只終止狀態檔記錄的 pid」的簡化敘述）**：送出任何信號（TERM 或 KILL，含一般停止程序與殘留處理兩種路徑）之前，都要先驗證 **pointer、state、root 三者與分類的一致性**（含 `rootPgid` 與實際觀測到的 `samePgid` 是否相符）——不能只憑狀態檔記錄的 pid 存在就送信號。

- **沒有已驗證存活成員支持的舊群組不得送信號**：如果 pgid 分類無法對應到至少一個目前已驗證存活、身分相符的成員，就不能對這個 pgid 送任何信號。
- **root 消失時**：只清理**能證明歸屬**的 escaped child（身分四要素相符者）；無法證明歸屬的候選程序一律跳過、不送信號、保留診斷（與 §2.3 殘留處理的既有規則一致）。
- **TERM 升級到 KILL 同樣適用**：不是只有第一次送 TERM 前才驗證，KILL 階段升級前也要重新核對此刻仍存活、身分相符的成員，避免對已經消失或身分不明的殘留 pid 送 KILL。
- **負控制**須涵蓋兩種情境：(1) pgid 不一致（狀態檔記錄的 pgid 與實際觀測到的 pgid 不符）；(2) root 已死但 escaped child 存活——兩者都要在送信號前被攔下或正確處理，不能誤送或誤判過關。

**R1 仍有缺口，rev7 補正（依 B3a-1a 第二次複核 mailroom #31 裁定 A、C；取代上述 R1 段落中尚未達成的部分，屬 B3a-1a-lifecycle-1 剩餘修正）**：

- **A（不得把歷史追蹤清單當成已驗證）**：所有正式停止入口都要用**明確可靠的持有或身分驗證**，**不得把歷史追蹤清單（`st.processes`）當成已驗證**——`st.processes` 只是「曾經觀測到」的清單，不等於「此刻仍存活且身分相符」。`verifiedGroupPgids`（或等效的已驗證 pgid 集合）**不得在開頭算一次就沿用到 KILL 階段**：TERM 與 KILL 各自升級時，**每次都只針對仍能證明歸屬的 pid 或群組**重新驗證，已消失、身分不符或本次無法驗證的目標一律不送信號。觀測失敗（無法確認存活與否）一律判失敗並保留診斷，不得預設「觀測不到＝已消失可跳過」。仍要維持 TERM／KILL 既有的有上限契約（§2.3 停止程序）——新增的驗證步驟**不得變成無上限的同步查詢**，本身也要受剩餘預算限制（呼應 R2 補強）。**新 spawn 仍持有 `ChildProcess` 物件時**，可以用「明確持有狀態」（例如物件參考仍存在、尚未 exit）作為身分的一部分依據，但**不得冒充「`ps` 已核對」**——兩者是不同強度的證據，不能互相取代。
  - **必驗情境三項**：(1) 已死 root＋存活 escaped child；(2) TERM 後 root 群組消失、但 child 仍存活；(3) pid 身分變更（重用）或觀測失敗時，不得碰觸與本次執行無關的目標。
- **C（rootPgid 與 samePgid 核對）**：一致性核對範圍補上 **`RunState.rootPgid`**，並對狀態檔記錄的每一筆程序**逐筆核對 `samePgid === (pgid === rootPgid)`**——即「記錄宣稱的 samePgid 欄位」必須與「該程序實際 pgid 是否等於 rootPgid」的計算結果一致，兩者不符就是 state 矛盾。**state 矛盾時，在送出任何信號之前一律拒絕**（不送信號、判失敗、保留診斷）。**C 已通過**（第三次複核，mailroom #35）。**B 已通過**（同輪，見 R2 迴歸修正段落，已依正式寫入 API 測試驗證）。

**A（送信號一致性）仍未完成，rev9 新增 A1–A5（依 B3a-1a 第三次複核 mailroom #35 裁定；皆以目前原碼＋mock ps/kill＋虛擬時間重現，未送真實信號）**：

- **A1（初次身分核對不符被吞掉）**：現況初次身分核對不符時只 log 並 continue，**沒有回報 abandoned**，導致出現「沒送任何信號卻 `clean=true`」的錯誤通過。要求區分「確實不存在」（程序已消失）與「身分不符／不明」（觀測到程序但身分核對不上）；**初次核對不符即以失敗結束，並保留 active pointer**（不清除、不改寫，供後續診斷與殘留處理沿用）。
- **A2（KILL 前未重新驗證身分）**：TERM 送出後若觀測拋錯，現況仍沿用**舊的**（TERM 前的）清單升級到 KILL，**KILL 前沒有成功的身分核對**。**「觀測失敗」（未知）不等於「已驗證存活」**：只有重新取得可靠歸屬（成功的身分核對），或仍有可靠持有依據（例如仍持有 `ChildProcess` 物件），才可以升級 KILL；否則**不碰未知目標**，以「未清乾淨」回報並保留診斷。**`050030Z-70c019` 的真實 log 即為此路徑**——rev7 §2.3「TERM 升級到 KILL 同樣適用……KILL 階段升級前也要重新核對」的敘述，實際實作**尚未達到這個契約**，本段是對該敘述的更正，不是新增規則。
- **A3（身分比較省略 `startedAt`）**：現況身分比較省略了 `startedAt`（啟動時間）。§2.3 既有的「身分核對四要素」契約是 **PID＋pgid＋command＋開始時間**，**非 held child（沒有直接持有 `ChildProcess` 物件的情況）不得省略任何一項**；仍可維持單次批次查詢（不因此要求逐一查詢）。
- **A4（`isDying` 誤把括號命令列當死亡證據）**：現況 `isDying` 把 `ps` 輸出中括號包住的命令列（例如 `(process)`）當成程序已死亡的證據，**這個推論無依據**。本機 `ps.1:401–421` 說明：**`<defunct>` 才代表 zombie**，括號 ucomm 只代表「argv unavailable 或不一致」（例如核心程序、權限不足）——**括號不等於死亡**；argv 取不到時，應視為**身分觀測不完整**（走觀測失敗路徑），不能當作死亡判定。
- **A5（`killGroup`／`killPid` 的 try/catch 會漏掉其餘目標）**：現況 `killGroup`／`killPid` 把 `process.kill` 與 `log.log` 包在同一個 try 區塊，**catch 區塊又呼叫可能拋出例外的 `log.log`**（底層是 `appendFileSync`，磁碟問題時會拋出）——這代表磁碟寫入失敗時，可能在送出**第一個**信號後就整段拋出，**漏掉其餘目標與後續的 TERM→KILL 升級**。要求：診斷（log）失敗要保留錯誤，並讓最終判定失敗；但**不得因為診斷失敗就阻斷其餘可安全執行的清理**——清理邏輯與診斷紀錄要能各自獨立失敗，不能讓後者拖垮前者。

**方法要求（rev9）**：先建立**可直接執行的小型停止程序測試**，覆蓋 A1–A5、dead-root＋存活 escaped child、正常路徑；**通過前不得再用多輪真實 Wails 啟動試誤**（避免用昂貴且不穩定的端到端啟動去驗證這些程式邏輯層級的修正）。

**風險裁定 1：觀測逾時是待分析的阻擋項，rev9（依 mailroom #35 裁定）**：觀測逾時（`ps`／`lsof` 等呼叫超過剩餘預算）**不准反覆重跑到綠**——不能靠多次重試找到不逾時的那一次就當作通過。`050030Z` 的 `ps` 在 TERM 10002 ms 邊界被判 timeout，**不得直接歸因於「系統高負載」**；必須記錄**剩餘 budget**、**實際耗時**、`error.code`／訊號等具體資訊，用來辨別是不是「把極小的剩餘時間當成 ps 自身的 timeout」而自己造成的失敗（也就是預算分配本身有問題，而不是 `ps` 真的跑不動）。**grace period 到期**（正常等待逾時）與**工具故障**（`ps`／`lsof` 本身失敗）要分開處理，不得混為一談；KILL 前仍要有身分依據（見 A2）；**既有的上限與容差不放寬**。

**逾時診斷措辭收斂（rev10，依 B3a-1a 第四次複核 mailroom #37 裁定）**：失敗紀錄當時剩餘 **315 ms**、新的查詢 cutoff 設為**小於 300 ms**才不查詢——**這兩個數字不重疊**（315 ms > 300 ms），**不得把這次修正寫成「已完全消除該類逾時」**。保留以下三點如實敘述：

- 修正**避免了「極短剩餘預算下還去查詢」**這一種情境（cutoff 之下不再送出可能逾時的查詢）。
- 新的 N13（見 §3）**單次通過**，只是這一次的結果，不是重複可靠性的保證。
- `ps` 本身**仍可能逾時**（cutoff 只是避開極短預算的情況，不是消除逾時的可能性），逾時發生時仍須依 R2 判為觀測失敗，不得放寬為 clean。

**不得**以「負載假說」（例如「應該是系統忙」）或「單次 PASS」代替「已解決」的保證；**若要聲稱邊界保證**（例如「cutoff 以上必定不逾時」），**須另外補上可控制時間的 focused 證據**（例如用可調整延遲的假 `ps` 驗證邊界行為），不能只靠一次真實執行的觀察。

**埠的處理**：

- **34115**：預檢時已被佔用 → 啟動前失敗，不終止佔用者
- **5173**：Vite 在 5173 被佔用時會自動改用下一個埠，wails 的 `serverUrl: auto` 會跟著偵測，所以不預先要求 5173 空閒；ready 後記錄**自家 vite 實際使用的埠**，收尾只核對這個埠

| 結束路徑 | 處理 |
|---|---|
| 部分啟動失敗（`wails dev` 在 ready 前自行結束） | 保留 `wails-dev.log`，對狀態檔已記錄的所有程序（含非同 pgid 後代）執行停止程序、釋放埠，`harness.log` 記錄 failure stage，以「startup failed」失敗 |
| 啟動逾時（300 s） | 執行停止程序，以「startup timeout」失敗，附 log 尾段 |
| 測試失敗 | 由 globalTeardown 執行停止程序，並收集證據 |
| 中斷（SIGINT／SIGTERM 送到測試程序） | harness 註冊信號處理：寫入 `interrupted` 後執行停止程序 |
| 前次執行且身分相符（`.active-run.json` 記錄的每個 pid 仍存活、且 pid＋啟動時間＋命令列＋pgid 歸屬皆與記錄相符） | 下次啟動時辨識並清理 |
| 前次執行但身分不符（pid 被重用，或啟動時間／pgid 不符） | 以失敗結束並保留診斷，**不送任何信號**，該存活程序（可能是誘餌程序）維持不動，只提示 |

**身分核對四要素**：pid＋啟動時間＋命令列＋pgid 歸屬。只核對命令列不足以判斷是否為前次殘留；四要素中任一無法取得或不符，即視為「無法證明歸屬」，走「身分不符」路徑。

**殘留處理（R3，取代 rev3 的單一整體判定）**：不是整批判定一次，而是**對 `.active-run.json` 記錄的每一個仍存活的 tracked process 逐一核對身分四要素**——即使 root（`wails dev` 主程序）已經消失，只要記錄中仍有存活的後代，也要對這些後代個別核對並清理；`.active-run.json` 本身遺失或內容損毀（無法解析、缺必要欄位）時，視同無法證明歸屬，**以失敗結束、不送任何信號、保留可用的診斷（例如目前存活的候選程序清單）**。
- **state 結構驗證（rev5，依節點 3 審查缺口 C 補強）**：讀入 `.active-run.json`／`run-state.json` 後，除了能否解析，還要驗證結構本身有效——PID／PGID 為有效正整數、身分欄位（命令列、啟動時間）非空、記錄之間的 pointer 關聯（例如後代記錄的父指標指回實際存在的 root 記錄）彼此一致。任一項不成立即視為損毀：**保留現有指標（不清除、不改寫），不送任何信號**，以失敗結束並附上具體驗證失敗的欄位。

**觀測工具可靠性（R2，rev5 依節點 3 審查缺口 B 補強）**：`ps`、`lsof` 等外部命令呼叫一律設逾時；結果須區分「確實沒有符合的程序／連線」與「無法觀測」（工具不存在、權限不足、呼叫逾時）兩種情況——**無法觀測時一律判定失敗並保留當次錯誤與可取得的診斷，不得因此判成 clean（殘留核對）或 0 違規（§2.7 網路判定）**。此原則同時適用於本節的程序追蹤與 §2.7 的網路取樣。
- **exit 1 的語意（缺口 B）**：外部命令回傳非零結束碼（例如 `lsof` 對多個 pid 查詢時的 exit 1）不能一律當「無符合結果」丟棄；要**結合 stderr 內容與查詢語意**判斷這是「合法的無匹配」（例如查詢的 pid 已消失，工具本身正常回報找不到）還是「觀測失敗」（例如 permission denied、逾時、工具錯誤）；**exit 1 時如果仍有有效的 stdout，必須保留、不得因為非零結束碼就整段丟棄**（節點 3 施工中發生過的迴歸：R2 第一版把 `lsof -p 多 pid` 的 exit 1 當空結果丟棄 stdout，導致取樣器實際空跑）。

**L1（嚴重，fail-open，rev10，依 B3a-1a 第四次複核 mailroom #37 裁定；屬 B3a-1a-lifecycle-1 剩餘修正）**：`ps` 輸出解析**依賴 locale**，現況三處問題連鎖：`psUtil.ts:52` 直接繼承呼叫端環境的 locale；`:133` 的 `lstart`（啟動時間）解析 regex **只接受英文格式**；`:144` 解析失敗時**直接 `continue`**（跳過該筆，不報錯）。

- **重現方式**：reviewer 以 `LC_ALL=zh_TW.UTF-8` 重現——macOS `ps` 回傳的日期欄位變成中文格式（例如「三　9/16 14:19:25 2026」），regex 解析不出來，`parsedRowCount=0`，但 `isAlive(self)` 仍回傳 `true`；交給實際 `stopProcessGroup`（僅 mock kill，未送真實信號）得到 `clean=true`、`residual`／`abandoned`／`unconfirmed` 全部是空、`observationFailed=false`、`signals=[]`。
- **後果**：**等於把「觀測失敗（無法解析輸出）」判成「程序已消失（不需清理）」**——在非英文語系環境下，整條停止契約會**靜默失效**（不報錯、不留診斷、看起來像成功收尾，實際上什麼都沒清理）。
- **授權最小修正**：
  - **只在 `ps` 子程序的環境固定 `LC_ALL=C`**（透過 spawn 的 env 參數，只影響這個子程序）——**不改使用者或 app 全域環境**，避免影響其他行為。
  - 單筆查詢（`processStartedAt`）與批次查詢的 locale 處理方式**保持一致**，不得一個固定、一個沿用呼叫端環境。
  - 關鍵 `ps -A` snapshot 中，**非空但無法解析的列**，或**整份查詢結果為空**，都必須**拋出 `ToolObservationError`**（走既有的「無法觀測即判失敗」路徑，見上方 R2 段落），不得吞掉繼續執行。
  - **診斷只記必要的格式資訊**（例如：解析失敗的欄位、原始 locale 設定），**不得把整份程序命令列寫入 log**（避免洩漏使用者本機路徑或敏感引數）。
- **focused selftest**（既有 9 項保留，新增以下情境）：
  - `LC_ALL=C` 格式的 `ps` 輸出可正確解析。
  - 繼承 `zh_TW` locale 的實際查詢，經正規化（固定 `LC_ALL=C`）後仍能解析出自身（測試程序）的存活 PID。
  - malformed（無法解析）或空的 snapshot **不得回報 clean**——必須觸發 `ToolObservationError` 並判失敗。

**L1 接受、註解更正與語系結論收斂（rev11，依 B3a-1a 第五次複核 mailroom #39 裁定）**：

- **接受**：上述 L1 修正、focused selftest**已通過 reviewer 第五次複核**——reviewer 從 Node 啟動前就設 `LC_ALL=zh_TW.UTF-8` 實測，單筆與批次 `ps` 的自身 PID／`startedAt` 一致，兩種 parser 對 malformed／空輸入均拋錯。程式面**無新阻擋項**。
- **註解更正（只修註解，實作正確不動）**：`psUtil.ts` 中描述 `PS_ENV`（`LC_ALL=C`）的註解原寫「呼叫當下才疊加」，**不精確**——`PS_ENV` 實際是**模組載入時**建立（不是每次呼叫才組裝）；這只是註解文字修正，上面「授權最小修正」所描述的實作行為本身正確、不需改動。
- **語系結論收斂**：驗證只能陳述「**這次查詢為英文格式**（`LC_ALL=C` 固定後）」，**不得以目前環境的驗證結果保證所有歷史 run 都相同**——這是單次或有限次驗證，不是對所有歷史執行環境的窮舉證明（與 §2.2a「全程 0 個視窗」超出單點查詢範圍是同一類問題）。**保留範圍**：現有證據**未顯示** L1 曾在過去實際觸發過（不是「已證實從未觸發」）；**歷史執行環境未逐次封存**，無法回溯查證每次 run 當時的 locale。

**N13 的完成條件（rev11，1a 最後一項）**：上一輪的 N13 執行是**觀測逾時造成的 fail-closed**，**不得列為「成功升級 KILL 並清乾淨」**——那是判定失敗被正確攔下，不是驗證升級路徑本身成功。補跑（重新執行 N13）必須**同時滿足**：

- TERM 實際等滿（不是被逾時提前打斷）。
- 對已驗證的自家目標升級 KILL（依 §2.3 送信號一致性驗證，KILL 前有身分依據）。
- 各階段耗時符合既定上限（TERM／KILL／埠核對，依 §2.3 停止程序預算）。
- 最終 pid 與實際埠皆清空，且 `clean=true`。
- 預期的啟動失敗 rc 仍可為 1（N13 本身是故意注入的失敗情境，這點不變）。

**原 428 ms 失敗證據保留、不得覆蓋**；若補跑再次觀測失敗，**立即停止並如實交代**（記錄失敗原因與現況），**不無限重跑、不放寬判定、不得依舊 pid 直接 kill 未重新確認身分的殘留**（呼應 §2.3 A1／A2 的身分驗證契約）。

**failure stage（如實寫入 `run-state.json`／`harness.log`）**：可能值為預檢 n/4（n 為第幾項預檢失敗）、啟動逾時、部分啟動失敗、啟動後核對、測試失敗、中斷；每次失敗都要落在其中一個實際發生的階段，不得含混寫成通用錯誤。

### 2.4 smoke flow（一條，不經 provider）

1. 載入頁面，完成 §2.2 的啟動後核對。
2. 規格分頁 → `glossary.md` → 以全選＋輸入把內容換成**完整的預期字串**（含本次執行的唯一標記）→ 按「儲存」。
3. **存檔完成的判定**：
   - `expect.poll(() => fs.readFileSync(glossary)).toBe(預期完整內容)`，上限 10 s——核對**完整內容相等**，不是只核對包含標記
   - 同時斷言沒有出現 `save-error`、`external-abort`
   - **不以「儲存鈕變 disabled」作為完成訊號**
4. 收尾時做 tripwire 判定（§2.6）與網路判定（§2.7）。
5. 選擇器：主分頁按鈕與檔案樹項目補上 `data-test`（owner 同意），由 E2E 實際使用來驗證。

### 2.5 存檔判定的受控對照（依審查補充 C 改寫）

- **目的**：證明新判定能區分「寫入尚未完成」與「已完成」。**不能據此宣稱已證實歷史那次失敗的根因**。
- **延遲注入方式**：只在測試端注入，production 不改。
  - 頁面 ready 後，以 `page.evaluate` 包裝 `window.go.main.App.SpecWrite`：原呼叫之前先等 2000 ms，再轉呼叫原函式，**呼叫時保留原函式的 `this` 與 `args`**（`orig.apply(this, args)`），不得改變呼叫語意。
  - 可行的依據：`frontend/wailsjs/go/main/App.js` 的 `SpecWrite` 是在**呼叫當下**才讀取 `window.go.main.App.SpecWrite`（F1），所以包裝會生效。
  - 包裝是否確實生效，由對照 A 的結果來判斷；同時留下呼叫與延遲證據（呼叫時間、args 摘要、實際延遲 ms）供驗證。
  - A、B 兩個對照各自還原 wrapper 或彼此隔離（各自使用 fresh page，並在各自開始前重設目標檔內容），避免互相污染。
- **對照 A（舊判定，應失敗）**：同一延遲下，按儲存 → 等儲存鈕變 disabled → 立即讀一次磁碟 → 斷言完整內容相等。預期失敗，因為讀到的是舊內容。
  - **`test.fail()` 的呼叫時機**：不得把任意失敗都算成功——在測試本體內、**緊接目標存檔斷言之前**才呼叫 `test.fail()`；在此之前的 setup／locator 失敗屬於正常失敗，**不得被 `test.fail()` 吸收**，也不得吸收 cleanup 失敗。
  - **核對根因**：驗證步驟另外核對 A 實際讀到的內容與其 sha256，確認等於「延遲注入前的舊的完整內容」，且失敗訊息確實來自目標存檔斷言（不是其他斷言或例外）。
- **對照 B（新判定，應成功）**：同一延遲下，以 §2.4 的 `expect.poll` 核對完整內容，預期成功。
- **guard 記憶體狀態檢查的位置（R3，rev6，依 B3a-1a 複核 mailroom #14 裁定）**：對照 A 的 `test.fail()` 只標註「這是預期會失敗的存檔斷言」，**不得因此吞掉 teardown 或 guard（tripwire／networkGuard）故障**——A、B 兩個對照都要在**不會被 expected failure 吸收的位置**（即 teardown／globalTeardown，而不是在 `test.fail()` 標註的那個測試本體判定內）檢查 guard 的記憶體狀態（違規清單、觀測失敗清單）。換句話說，A 的「存檔斷言預期失敗」與「guard 有沒有正確記錄狀態」是兩件獨立要驗證的事，不能因為前者預期失敗就連帶忽略後者。
- **不列入預設套件**：兩個對照放在 `frontend/e2e/controls/`，以 `npm run test:e2e:controls` 執行；仍是 B3a-1 驗收必跑項，**不得改為 optional**。

### 2.6 fake provider（tripwire）

- 兩支假 CLI 每次被呼叫都把 `時間、argv（逐一引號包住）、cwd、PPID` 追加寫入 `invocations.log`。
  - 參數**恰好是一個 `--version`** 時，輸出本次執行的假版本字串。
  - 其他任何參數：寫 stderr，並以非零碼 17 結束。
- **收尾判定**，任一項不符即整次執行失敗：
  - `invocations.log` **必須存在**，缺失不算通過
  - claude、codex 各至少被呼叫一次（`CLIInfo` 會各呼叫一次 `--version`）
  - 每一筆的 argv 都恰好是一個 `--version`
  - 不得有任何其他呼叫
- replay provider 不在本票（§4）。

### 2.7 執行期無外網需求的驗證（rev4：依節點 2 複核 F1 裁定與審查補充 R2，改為兩層判定）

rev3 原本以單一 `lsof` 取樣涵蓋全部自家程序樹、再加一次 sandbox-exec 事後驗證。節點 2 施工發現：為壓系統 Chrome 背景流量加的 `--host-resolver-rules=MAP * 127.0.0.1` 會把 Chrome 的外連全部改寫成 `127.0.0.1:443`，被取樣器誤判為 loopback，**連帶遮蔽了頁面層級的真實外連**（F1）。裁定**方案 (2)**：不使用該旗標，改為程序取樣層與 browser 層分開判定。

**程序取樣層**：

- 從 `wails dev` **spawn 起（含啟動期間）**到停止程序開始為止，每 1 s 對自家程序樹執行一次 `lsof -nP -iTCP -iUDP`，寫入 `network-samples.log`；呼叫依 R2（§2.3，rev5 補強見下）設逾時，並區分「確實無連線」與「無法觀測」，無法觀測一律判失敗、不得判成 0 違規。
- 受觀測程序**依命令列分類**：wails／go 建置、app 本體、vite／node、Chrome、取樣器自身工具（不列入觀測）、其他。
  - **Chrome 背景網路照樣記錄，作為診斷資料，但不列入預設 suite 的失敗判定**——真正的外連封鎖責任交給 browser 層（見下）。
  - 只有確實辨識為本次 Playwright 啟動的 Chrome／Chromium 程序才排除於「非 loopback 即失敗」判定之外；任何無法歸類或身分不明的程序，一律嚴格判定（出現非 loopback 遠端位址即失敗）。
  - **預設 suite 不宣稱 Chrome 整個程序沒有外連**；「整體外網被阻斷時仍能完成流程」只由下方 sandbox-exec 的完整實測證明。
- **限制**：兩次取樣之間的短暫連線可能漏掉，所以取樣結果只能說明「取樣時點沒有外連」。
- **端點分類與解析（rev5，依節點 3 審查 Q1 裁定，取代逐行整行 regex 的做法）**：
  - `lsof` 每行**先判斷是否有 `->`**（TCP 已建立連線，或已連線的 UDP）：有的話，**只解析 `->` 右側的遠端端點**，判斷是否為 loopback（`127.0.0.0/8`、`::1`）；不對整行套 regex，避免把本機端點或狀態欄位誤判為遠端。
  - **LISTEN 記錄**：本機監聽位址只允許 loopback；出現 `0.0.0.0`、`::`、`*` 或任何非 loopback 介面**一律判違規**（即使目前無人連入，監聽在所有介面本身就是風險）。
  - **既無遠端、也非 LISTEN 的記錄**（例如 `CLOSED *:*`、未連線的 UDP socket）：只記為**診斷**，文字寫「沒有觀察到遠端」；**不能據此證明它沒有外連過**（狀態轉換可能發生在取樣間隔之間）。
  - **無法解析的記錄**：原樣保留於 log，並判為**觀測失敗**（不是 0 違規、也不是略過）。
  - **parser 測試案例**（施工時須覆蓋）：實際的 `CLOSED` 行、IPv4 loopback、IPv6 loopback、外部遠端位址、wildcard LISTEN（`0.0.0.0:*`／`*:*`）、未知／異常格式。

**browser 層**（新增，取代 `--host-resolver-rules`）：

- 在任何 `page.goto` 之前，對整個 `BrowserContext` 安裝 HTTP(S) request 檢查與 `routeWebSocket`。
- 非 loopback 的 URL（含 `ws://`／`wss://`）要**記錄原始 URL、拒絕連線**，並留下 violation 讓整次 run 失敗；事件 callback 內**不得直接 throw**（避免打斷 Playwright 事件迴圈，改為記錄後在收尾階段核對 violation 清單並判失敗）。
- loopback 的 WebSocket 保持真實連線，不攔截。
- context 另設 `serviceWorkers: 'block'`，避免 Service Worker 繞過上述路由攔截；**這是測試設定，非 production 行為，要在證據與文件中明確揭露**。
- 不使用 `--host-resolver-rules`，也不為了壓 Chrome 背景流量再加其他 Chrome 行為旗標（F1 裁定方案 (2)）。
- **判定時機（R3，rev6）**：browser 層 guard 的 violation 清單在 smoke 與 controls A／B 都要於 teardown 階段核對，**不受測試本體內任何預期失敗（含 §2.5 對照 A 的 `test.fail()`）影響**；guard 本身若因寫入失敗等原因無法產生可信結果，視為觀測失敗（見 §2.9），不得當成「沒有違規」。

**Chrome argv 證據**：Playwright 的 `chromiumSandbox` 預設為 `false`，「沒有手動加旗標」不代表 Chrome sandbox 有啟用；驗收證據要保存 Chrome 主程序**實際的啟動參數**（`chrome-argv.txt`）。`browser.process()` 為 `null` 時印出的旗標判定**不算證據**，須改用可靠的方式取得 argv（例如觀測程序樹中該 pid 的命令列）。

**Chrome sandbox 裁定（rev5，節點 3 審查）**：節點 3 第一部分審查實際取得的 `chrome-argv.txt` 顯示 `--no-sandbox` 存在（Playwright `chromiumSandbox` 預設值所致），`--host-resolver-rules` 不存在（F1 修正已生效）。reviewer 接受**在本隔離 fixture 測試中沿用這個現有設定**，**但不得宣稱 Chrome 自身的 sandbox 有啟用**——這只是 harness 對隔離 fixture 的隔離保證，不是 Chrome process sandbox 的保證。macOS `sandbox-exec` 的外網阻斷仍須依下方另外實測，不能用「argv 已知」取代。

**阻斷外網的單次驗證（施工時做一次，本輪授權範圍內）**：

- **範圍**：只作用於本次測試程序及其子程序，**不關閉主機網路介面、不改全機防火牆、不用 sudo**。
- **方式**：macOS `sandbox-exec`，搭配只允許 loopback 對外連線的 profile，把 `npm run test:e2e` 整個命令包在該 sandbox 內；Playwright runner、`wails dev`、`go build`、Vite、app、Chrome 都是其後代，依 sandbox 繼承規則一併受限。
- **驗證順序**：先以簡單案例證明同一個 sandbox profile 確實「允許 loopback、阻擋對外連線」，再用它完整跑一次 suite；profile 內容與包裝命令本身也要存為證據。
- **不可行時**：回報給 reviewer 裁定替代方案或限制範圍，**不自行降格為「已完成」**；不得改採關閉網路介面等超出授權範圍的替代方式（該後備方案已在 rev3 裁定中刪除）。
- **依賴準備後才執行**（§2.1）：`GOPROXY=off`、`npm_config_offline=true`／`GOFLAGS=-mod=readonly` 是輔助設定，讓已知的下載路徑明確失敗，**不等於完整網路隔離**——完整隔離只能靠上述 sandbox-exec 的單次驗證證明。

**阻擋項 S1：`ps` 在 sandbox-exec 內無法執行（rev12，依 1b 複核 mailroom #41 裁定）**：完整 suite 在 sandbox 內跑兩次都失敗，**不只是** `wails dev` 內建 Vite URL 偵測的 10 s 逾時——兩份完整 sandbox log 分別有 **76 處**（attempt1）與 **77 處**（attempt2）**EPERM**，含 spawn 後身分補強與程序樹追蹤失敗。reviewer 的最小重現：`sandbox-exec -f loopback-only.sb /bin/ps -o pid= -p <自身pid>` → rc 71、`execvp Operation not permitted`；**即使 profile 只有 `(version 1)(allow default)` 也一樣**（不是 loopback-only profile 太嚴格造成的）。因此存在一個**與網路阻斷本身無關、獨立的障礙**：`ps` 這支工具本身在 sandbox-exec 環境下就無法被執行。

- **已知線索，但不得過度推論**：系統 `/bin/ps` 的 mode 為 `4755`（setuid）；**不得把這條線索擴張成「底層機制已完全證明」**（例如「因為是 setuid 所以一定是 sandbox 擋 setuid」這類未經驗證的結論）。
- **已排除的方向**：reviewer 試過用位元組相同、mode 改為 `755`（拿掉 setuid）的 `ps` 私有副本，結果**直接被 SIGKILL**——**這條路不採用**。
- **禁止事項**：**不得改系統 `ps`、不得重簽系統檔、不得關閉系統防護**去繞過這個限制。

**必須更正的既有措辭（rev12）**：以下敘述**超出目前實際證據支持的範圍**，文件與證據中若有同義敘述，一律更正（原文保留，附更正註記，不得刪除）：

- 「證明 sandbox 沒有擋住任何功能所需連線」——**超出證據**：目前只證明了「loopback 通、外部連線失敗」這個機制本身有效，沒有證明「所有功能所需的連線都不受影響」（`ps` 本身就是一個反例）。
- 「卡住的是與網路隔離無關的逾時值」——**超出證據**：`-viteservertimeout 60` 那次診斷成功不能推論「卡點與網路隔離無關」，因為 S1（`ps` EPERM）本身就是網路 sandbox 帶來的副作用，兩者未被真正拆解開。
- `Vite ready in 284 ms` 只是 **Vite 自報**的啟動時間，**沒有量測「npm 程序啟動 → Wails 收到 URL」的完整區間**，也**沒有排除 `ps` EPERM** 這個獨立障礙。
- `-viteservertimeout 60` 那次只能證明**某一次診斷用啟動成功**，**不能宣稱「完整 suite 一定會通過」**。

**授權的受控診斷（rev12，回 1a，主動工作時間上限 0.5 hr，純等待另列）**：

- **範圍**：先用**小型受控程序**評估，**不啟動完整 Wails suite**；評估的最小方案是「**觀測與清理由 sandbox 外的 harness 執行；受測 Wails／app 與 Chrome 子程序各自在相同的網路 sandbox 內**」（即 harness 本身不受 sandbox 限制、可以正常呼叫 `ps`，只有被測程序本身在 sandbox 內跑）。
- **必須證明**：(1) 外部 observer（sandbox 外的 harness）可以讀取 sandbox 內程序的身分；(2) 能追蹤自家後代程序樹；(3) 能安全停止（TERM／KILL）。
- **兩條受測子程序路徑都要確實繼承阻斷**：Wails／app 那條與 Chrome 那條**都要**證明 loopback 通、外部連線失敗——**不得漏包 Chrome**、也**不得讓被測 app 留在 sandbox 外面**（那樣就沒有驗證到目標）。
- **記錄**：命令、profile 內容、PID／PGID、結果，皆留存為證據。
- **限制**：只操作自建的測試程序；**不改主機設定、不用 sudo、不改防火牆、不放寬 network 限制**；**不得把觀測錯誤忽略成 PASS**；**先不修改正式 harness**（這是評估階段，不是正式導入）。
- **時間上限**：0.5 hr 內若沒有可行證據，**即交代阻擋現況**（寫清楚卡在哪、試過什麼），**不得改用「已知限制」的方式關票**（即不能因為評估花了時間就把 S1 降格成可接受的已知限制帶過）。
- **`-viteservertimeout 60` 的定位**：可以用在這次受控診斷裡；但若要在正式 harness 中**採用**這個旗標，需要**與 S1 一起驗證**（不能只驗證 timeout 問題、不驗證 ps EPERM），且**不得連帶放寬 TERM 10 s／KILL 5 s 的既有上限，也不得放寬觀測失敗的判定**（§2.3）。

**S1 診斷證據的範圍更正（rev13，依 codex-reviewer mailroom #45 裁定；原文保留、附更正）**：上述受控診斷執行完成後，reviewer 對其證據做了範圍核對，結果是**部分成立、部分不成立**：

- **成立**：外部 observer（sandbox 外的 harness）的 `ps`／`lsof` 可以讀取 sandbox 內程序的身分；對單一 PID 送 TERM 與埠釋放皆有效。
- **不成立（不得宣稱已驗證）**：
  - 診斷腳本自印 `pgid=process.pid` **不是實測**——**實際用 `ps` 查詢**顯示 pid 38502 的 pgid 是 **37744**（不等於 pid 本身），代表腳本印出的數字只是程式邏輯假設，不是對 sandbox 內程序群組關係的真實觀測。
  - 診斷**缺 `lstart`**（啟動時間，身分核對四要素之一）**、未展示後代（非 root）程序的完整身分、也未展示對整個程序群組（不只單一 PID）的停止**——**不得宣稱「完整程序樹契約」已被驗證**，目前只驗證了單一 PID 層級的觀測與停止。
- **Chrome 例證據不足**：診斷用的是**未指定 `channel` 的 Playwright 內建 Chromium**，**不是**正式預設要用的**系統 Google Chrome**（見 §2.7 上方「Chrome sandbox 裁定」，正式路徑用 `channel: 'chrome'`）；loopback 頁面對外部發出 fetch 得到的 `TypeError`，**可能只是 CORS 錯誤**（瀏覽器同源政策擋下，不是 sandbox 網路層擋下），**缺少「同樣條件、但沒有 sandbox」的成功對照組**來排除這個可能性——**不能單獨拿這個結果證明 Chrome 的網路確實被 sandbox 阻斷**。

**trace 數字更正（rev12，1b 的 trace 診斷能力已被 reviewer 接受，不需重跑，僅更正描述數字）**：

- `test.trace` 共 **762 筆事件**。
- **13 個內層失敗 expect**（`expect@277`–`398`）加**1 個外層 poll timeout**（`expect@276`）——**不是「21 次固定 500 ms」**；重試間隔實際觀察到 **100／250／500／約 1000 ms** 等不同值。
- screencast-frame 事件與對應 JPEG 圖檔**皆為 47 個**（timestamp 範圍 `577695.052`–`595779.201`）——**不是 49 個**。
- **限制**：畫格是**離散取樣**，**不能據此證明整段時間裡每一個時刻的畫面都沒有變化**（取樣間隔之間可能有變化未被捕捉，與 §2.7 網路取樣的限制邏輯相同）。

**快照責任（rev12）**：若後續施工改變了功能快照的內容，**新版本的正式連跑責任重新成立**（不能沿用舊快照的驗收結果）；目前既有的三次正式 smoke 保留作為 `20260916T073313Z` 快照的**有效歷史驗收**，**不會自動充當之後新快照的驗收結果**。

### 2.7a E2E offline 驗證模式（opt-in，rev13，依 codex-reviewer mailroom #45 裁定）

**背景**：reviewer 接受 S1（§2.7）的架構方向——「harness／observer 在 sandbox 外，受測程序在 sandbox 內」——並授權在 1a 實作一個**受控的、opt-in 的 offline 驗證模式**，取代直接把整個 Playwright worker 包進 sandbox 的做法（那樣會讓 `ps` 失效）。**本輪主動工程工作上限 3 hr（這是授權上限，不是估點）**。

**前置小測試（授權範圍的一部分，必須先做，未通過即停止）**：

1. **外部 observer 的實測身分核對**：以 `detached` 啟動一個自家的 sandbox 目標程序，**實際用 `ps`／`lsof`（不是腳本自印）**查出 pid、pgid、command、`lstart`，並核對其後代程序的關係；再走既有的有界停止流程（§2.3），證明目標程序與其佔用的埠都確實釋放。**不得沿用前一輪腳本自印 PGID 的做法**（見上方「S1 診斷證據的範圍更正」）。
2. **正式系統 Chrome 的三組對照**：用**正式系統 Chrome**（`channel: 'chrome'`）、相同的啟動整合候選方式、相同的目標網址，做三組對照——(a) 無 sandbox，成功；(b) 有 sandbox，外部連線被阻斷；(c) 有 sandbox，loopback 連線成功。用**頂層 navigation**（例如直接 `page.goto` 外部網址）或**受控的、明確允許 CORS 的端點**來排除「失敗其實是 CORS 而不是網路阻斷」這個可能性；**不得**用 route 攔截器、DNS 改寫或瀏覽器離線模式去人工製造出看似成功的紅燈（那樣不是真的測試網路 sandbox 的效果）。保留：實際錯誤訊息、Chrome 的 argv／版本、sandbox profile 內容、完整啟動命令。

**兩項小測試只要有一項不成立，就立即停止並回報**（寫清楚在哪一步、卡在什麼證據上）；**不得在小測試未通過的情況下就先去跑完整 Wails suite**。

**實作授權範圍（通過小測試後才進行）**：

- **改動範圍限定**：只能改 `frontend/e2e` 目錄內**必要**的 launcher、`processTree`、兩份 Playwright 設定檔、小型共用 support 模組；可以新增 sandbox profile 與必要的說明文件。**不得**擴大到這個範圍以外的檔案。
- **明確 opt-in**：這個模式要透過明確的旗標或設定開啟，**預設路徑（既有 harness 的行為）完全不變**。
- **sandbox 邊界**：harness 本體與 observer（負責觀測、追蹤、停止的邏輯）在 sandbox **外**執行；`wails dev`／Go backend／app 的所有後代，**以及正式系統 Chrome 及其後代**，一起放在**同一個 loopback-only sandbox profile** 內。**不得把整個 Playwright worker 包進 sandbox**——那樣連 harness 自己呼叫 `ps` 都會失效，等於重蹈 S1 的問題。
- **Chrome 啟動方式**：Chrome 候選可以用**本次 run 專用的 executable wrapper**（例如一個小 shell script 或等效機制，把 Chrome 的啟動包進 sandbox-exec）。這個 wrapper：
  - 必須**實際驗證**能與 Playwright 的啟動整合機制相容（不能只是理論上可行）。
  - 用 **`exec`**（不是產生子 shell 再等待）以保留原始 argv、維持 stdio pipe 與信號傳遞正確。
  - 必須**安全處理含空白的路徑**（正確加引號或用陣列形式傳參，不能假設路徑不含空白）。
  - **不得用字串拼接組出 shell 指令**（避免注入風險與路徑跳脫問題）。
  - **不得更動系統 Chrome 本身**（不裝、不改、不重簽）。
  - wrapper 檔案本身、sandbox profile 內容、以及被包裝的 Chrome executable 的路徑與 hash，都要留存為證據。
- **啟動前檢查**：使用這個模式前，要先確認 wrapper／profile／Chrome executable 都**存在且可執行**；**不得在檢查失敗後靜默改用沒有 sandbox 保護的瀏覽器繼續跑**（那樣會讓測試看似成功但其實沒有驗證到 sandbox 隔離）。
- **瀏覽器支援範圍**：**先只支援已經驗證過的系統 Chrome**這一種組合；如果使用者透過 `E2E_BROWSER` 指定了尚未驗證支援的瀏覽器組合，**必須在啟動前就明確失敗**（不是啟動後才發現不支援、或悄悄退回別的行為）。

**`-viteservertimeout` 併案核准**：**僅**在這個新的 offline 模式下，把 Wails 既有的 `-viteservertimeout` 旗標設為 **60 秒（候選值，非最終定案）**；**預設模式（不開 offline）維持不變、不受影響**。**不得宣稱先前那次 284 ms 的觀察已經證明了完整的根因**（呼應上方「必須更正的既有措辭」）。以下既有規則**全部維持、不因這個模式而放寬**：外層 startup 逾時上限（§2.2）、**TERM 10 s／KILL 5 s**（§2.3）、埠核對與觀測失敗判定（§2.3）、網路 guard（§2.7 程序取樣層與 browser 層）、版控產物 artifact 規則（§2.9 Q3）。**不改 `main.go`、不改 Wails 本身或 module cache、不改主機系統設定**。

**1a 驗證順序（先做完前面才做後面）**：

1. 上述兩項前置小測試。
2. `npm run typecheck:e2e`、`npm run selftest:all`。
3. **在新 offline 模式下**驗證：正常啟動、啟動失敗／逾時／中斷（對應 N7／N8）、ready 前後代程序處理（N10）、前次殘留辨識（N11 受影響的情境）、TERM→KILL 升級（N13）。**重點是套上 sandbox wrapper 之後，既有的身分追蹤與收尾邏輯仍然正確運作**——這不是只在舊的、沒有 sandbox 的模式下重跑一次而已；**所有失敗都要保留**。

**新快照要求**：交付新的固定快照時，manifest **必須同時收錄**：tracked 檔案的逐檔 hash、untracked 檔案的逐檔 hash、HEAD、彙總 diff，**以及本次用到的工具、sandbox profile、Chrome wrapper 的版本資訊**。既有的固定快照檢查責任（§2.9 各項）維持不變、不因新增這些欄位而減少。**由 reviewer 先複核這次 1a 交付，複核通過後才開始新一輪 1b**。

**新版 1b 範圍**：在**新固定快照**上——**三次正式的預設模式 smoke**（不是 offline 模式，是既有的 `npm run test:e2e`）、controls A／B（§2.5）、**完整 offline 模式的一次端到端成功案例與一次阻斷對照案例**、清理與證據完整性核對。**功能修改不得混入 1b**（沿用既有的「1a 缺陷修正不得移入 1b」原則）。已接受的 `084023Z` trace **保留作為既有診斷能力的證據**——如果這輪 spec／`expect.poll`／trace 設定都沒有改動，**可以沿用這份 trace 並註明來源版本**，**不必為了湊「有一次失敗案例的 trace 檢視」這個計數而刻意再製造一次失敗**；其餘沒有受本輪改動影響的負控制，依已核准的既有規則沿用（不必逐項重跑）。**`20260916T073313Z` 快照的三次正式 smoke 仍然是有效的歷史驗收來源，但不算是新版快照要求的「三次」**——新版仍需在新快照上重新跑三次。

**證據紀律（rev13）**：接受本輪的 trace 描述更正（見上方）與「新快照 manifest 必須同時收錄 tracked／untracked 逐檔 hash」這項補強；**停止以檔案的 mtime 作為任何判定的決定性證明**（mtime 可能因為系統操作、複製、還原等原因改變，不能當作「內容確實在某個時間點是這樣」的可靠依據；一律以 hash 與實際內容核對為準）。

**P1／P2 缺口與修正契約（rev14，依 codex-reviewer mailroom #47 裁定，複核 offline 實作）**：reviewer 複核本節（§2.7a）的 offline 驗證模式實作——**架構、兩項前置對照、正常／N7／N8／N10／N13 成功路徑接受；1a 仍未通過**，另獨立重現兩個新缺口。**額外授權最多 1.5 hr 主動工作**（P1／P2 修正、小測試、N11、交付），**1b 仍不得啟動**。reviewer 驗證來源：HEAD／diff SHA 與 `095245Z` 一致、MANIFEST 7/7、**tracked 逐檔 10 筆相符**；**untracked 實際 35 筆（不是先前所稱的 37 筆）**——工具區重複列出 wrapper／profile 導致重複計算，**文件如有引用一律更正**（本文件與 backlog 中原寫「37 筆」之處，請一律理解為此處更正的 35 筆）。Go race／Vitest／build／selftest 本輪由 reviewer 核對既有封存證據、**未宣稱全部獨立重跑**；`typecheck:e2e` 由 reviewer 獨立執行、rc 0。

- **P1（預檢未達宣稱契約）**：`offlineSandbox.ts:32–40` 對 sandbox profile 只做 `X_OK`（可執行）檢查且**吞掉存取錯誤**，缺 `R_OK`（可讀）與一般檔案型別檢查；`:65` 只檢查 `/usr/bin/sandbox-exec`**存在**、未檢查**可執行**；**系統 Chrome 只有在 wrapper 實際執行時才被檢查**（此時 Wails 可能已經啟動）。reviewer 以 fs mock 分別模擬這三種壞情況，**`validateOfflineSandboxPrereqs('chrome')` 全部通過**——等於預檢沒有真的擋下這三種壞情況，違反本節「啟動前檢查」原本宣稱的契約。
  - **修正契約**：sandbox profile 必須是**可讀的一般檔案**（**不對 `.sb` 檔要求 `X_OK`**，profile 本身不是可執行檔）；**不得吞掉存取錯誤**（讀取或 stat 失敗要如實回報，不能 catch 後靜默當成其他狀態處理）；**wrapper、`sandbox-exec`、實際要用的系統 Chrome 執行檔，三者都必須在任何 Wails 或 Chrome 啟動之前確認可執行**；**統一使用已驗證過的絕對路徑**——目前 `processTree` 是直接 spawn 裸的 `sandbox-exec`（依 PATH 解析），與預檢驗證的 `/usr/bin/sandbox-exec` 不是同一個解析契約，**可能命中 PATH 上另一支 `sandbox-exec`**；兩處須改成使用同一個已驗證的絕對路徑。**新增小型負向測試**，證明上述每一種壞情況都會在任何 Wails／Chrome 啟動之前失敗（用隔離路徑或 mock 建構，**不改動系統檔**）。
- **P2（config 載入前失敗無 harness.log）**：reviewer 實跑一組不支援的 `E2E_BROWSER` 組合，程式以 rc 1 結束（合理），但**沒有新的 artifact 目錄、也沒有 harness.log**——因為 globalSetup 這時候根本還沒執行到，**違反 §2.2「啟動前失敗也要留 log」**的既有契約。
  - **修正契約**：必須在**進入 `run-e2e` 入口、啟動 Playwright 或載入 config 之前**就先建立證據目錄與一份早期 log，記下**失敗發生的階段、原因、結束狀態**；後續若成功進入 globalSetup，須**沿用同一個 run-id／證據目錄**並與既有 logger 銜接，不得覆寫掉這份早期紀錄。若有些入口是直接從 config 執行（不經過 `run-e2e`），必須**明列這些入口目前是否有涵蓋這個早期 log 機制**，**不得宣稱所有入口都已涵蓋**。**這個階段本來就不會有 trace**（呼應 §2.9「不承諾所有失敗都有 trace」），**不得為了硬湊出一份 trace 而在這個階段去啟動 app**。負向測試須核對三件事：**非零 rc**、**harness.log 有記錄失敗原因**、**沒有留下新的 Wails／Chrome 程序或 `.active-run.json` 殘留**。
- **兩處表述收斂**：
  - `placeholderCommand` 用實際的 spawn 參數當佔位是合理的，但 `sandbox-exec` 之後會再 `exec` 成 wails 的實際命令，**命令列本來就會變**——**不得宣稱改用 `finalCommand` 就已經解決了所有 A3（身分比較省略 `startedAt`）的誤判風險**；身分判定仍要依賴**實測身分補強**與**held `ChildProcess` 契約**，不能只看命令列字串是否一致。
  - **刪除未使用的 `wailsDevSandboxCommand`**——這是與實際使用路徑重複組參數的另一套函式，未被任何呼叫點使用；**只保留單一實際使用的入口，不新增抽象層**（不為將來可能用到而留一套平行實作）。
- **N13 紀錄紀律**：本輪 offline 模式下的 N13 補跑**兩次結果必須分開保留**——第一次觀測失敗、`clean=false`；第二次成功。**不得抹掉第一筆、也不得把兩次寫成連續穩定通過**。「不是新 bug」這句話**只能寫成「尚未證明是新回歸」**，不得用「表面上跟舊有案例長得一樣」當成根因已確認的理由。**若同一份快照上再遇到同樣的觀測失敗，必須停止並分類**，**不得反覆重跑到湊出一次成功就結案**。另外要**補上「殘留 fixture 如何先核對身分、再清理」的實際證據來源**（例如是哪一次執行、留下什麼記錄），**不得只寫「每次執行後皆無殘留」這種總結性敘述而沒有可對照的來源**。

**1a 剩餘工作與驗證順序（rev14，額外授權上限 1.5 hr，這是授權上限、不是估點）**：P1／P2 的修正與對應的小型負向測試 → `npm run typecheck:e2e`／`npm run selftest:all` → offline 模式下的 **N11**（有效的自家殘留可以安全回收、身分不符的目標不誤殺、損毀或無法解析的狀態不得判成乾淨）→ 最後再跑一次 offline 模式的正常 smoke，確認預檢修正與 launcher 整合沒有回歸。**N7／N8／N10／N13 先前已讀取並接受的成功證據予以保留**，**只有在對應的生命週期邏輯本身被實際改動時，才需要重新跑受影響的那幾項**，不必無差別全部重跑。

**P1／P2 接受、N11 分支化與 1b 條件式授權（rev15，依 codex-reviewer mailroom #49 裁定）**：reviewer 複核 P1／P2 修正——**接受**：P1 的預檢契約（profile 改查 `R_OK`／`isFile`；wrapper／sandbox-exec／實際 Chrome 三種執行檔改查 `X_OK`／`isFile`；`processTree` spawn 與預檢統一使用同一已驗證絕對路徑）、P2 的早期 log（`run-e2e` 入口、載入 config 前建立證據目錄與早期 log）、**N11a 的 root 存活回收**（`100856Z`，見下方分支說明）。**1a 無新的實作阻擋**。驗證來源：`102306Z` tracked diff SHA 相符；**tracked 逐檔 10 筆、untracked 36 筆、MANIFEST 7/7 全部相符**；實跑新增的 **8 項 `offlineSandbox` selftest** 與 `typecheck:e2e` 均 rc 0；P2 亦實跑原反例，rc 1、新增目錄僅 harness.log、含實際拒絕原因、無 trace、無 active-run 指標；其餘固定檢查為**核對封存證據，未冒稱獨立重跑**。

**JS 入口與 TS 預檢的雙實作**：目前 JS 入口（`run-e2e`）與 TS 預檢各自實作了一份檢查邏輯，reviewer **接受現況、本輪不要求重構**——**但日後任一方修改，兩者必須維持一致**（不得只改一邊而讓另一邊的檢查邏輯落後）。**支援邊界**：早期 log 機制目前**涵蓋 `test:e2e` 與 `test:e2e:controls` 的共同入口**；**不得宣稱直接呼叫 playwright（跳過 `run-e2e`）的入口也有早期 log**，這是尚未涵蓋的已知邊界。

**N11 分支化驗收（rev15，取代先前的 1/3、3/3 寫法）**：

- **N11a**：(i) **root 存活回收**（**已接受**，`100856Z`，見上方「接受項」）；(ii) **root 已消失但自家後代仍存活**的 stale 回收——**這與 N10 的「ready 前立即收尾」是不同情境**，是**下次啟動時**辨識並清理前次殘留，**須有本輪 offline 模式下的新證據**，不能沿用 (i) 或舊模式的證據頂替。
- **N11b**：**root 身分不符**（pid 被重用、啟動時間或 pgid 與記錄不符）與**非 root 後代身分不符**兩種情境——皆須**非零結束、保留診斷、完全不送任何信號、受控誘餌程序仍存活**。
- **N11c**：**`.active-run.json` 不可解析或缺必要欄位**與**可解析但結構無效**（例如記錄為空陣列）兩類——皆須**非零結束、不送任何信號、現有 pointer 保留不清除也不改寫**。
- **做法**：用自家受控 fixture，走**真正的 stale recovery 入口**（不是模擬函式呼叫）；保存 pointer／state 的前後內容、身分核對與程序存活的證據；**這是針對性驗證，不需要每個負向情境都再跑一次完整 smoke**；只清理自己建立的測試資料與誘餌程序，不動其他狀態；若打算沿用舊模式或前一輪的證據，須**逐項說明該分支確實未受 wrapper／入口變更影響**，不能籠統宣稱「情境沒變所以證據沿用」。

**新版 1b 條件式授權（rev15，可直接開始，不需再請示）**：條件——**N11 三個分支的應驗情境全數滿足**、證據完整（含上述保存的 pointer／state 前後內容）、**功能檔仍為 `102306Z` 已審內容（逐檔 hash 相同，不是重新審過的新版本）**、**沒有新的未知殘留或觀測失敗**。這些條件同時成立時，B3a-1a 轉為「**補件條件達成、供整合驗收**」的狀態；此階段**允許補文件與工時更新**，但須**列出相對 `102306Z` 的差異**（即使只是文件或註解）並產生新的封存 manifest。**只要任一功能檔需要修改，這個條件式授權就不適用**——退回正常流程，須交出新的 diff 給 reviewer 複核後才能啟動 1b。

**1b 範圍（rev15，維持 mailroom #45 既有範圍）**：新固定快照上——**三次正式預設模式 smoke**（前面各輪跑過的都不計入這三次）；**controls A／B**（不得因為 offline 模式而省略）；**完整 offline 模式的一次端到端成功案例與一次明確阻斷對照**——**必須是這份快照上真正執行 offline 模式並成功**，**不得**用 `curl` 或 Node 小型測試腳本代替實際的 offline 端到端跑法；清理與不可變證據封存（依 §2.9 R5 順序）。「系統 Chrome／已排除 CORS 疑慮」的對照可以**沿用**兩項前置小測試裡已經驗證過的同一份 wrapper／profile 內容作為前置證據，**但須明列這份沿用證據的 hash 與版本**；`084023Z` 的 `expect.poll` 失敗 trace 依既有條件沿用並標明來源版本；其他未受本輪改動影響的負控制逐項列出沿用來源，不必逐項重跑。**額外主動工作上限 2.5 hr**（這是授權上限，不是估點），新版 1b 的剩餘估算仍維持 **1.5–2.5 hr**，純等待另列、不計工程量。

**N11 接受、新阻擋 I1 與三處更正（rev16，依 codex-reviewer mailroom #51 裁定）**：reviewer 接受 `runState.ts`／`staleRun.ts` 的語法相容性修改（以保留的隔離副本直接 diff 確認 `runState.ts` 僅欄位宣告與 constructor 賦值、`staleRun.ts` 僅 type import）與 N11 補件；全量逐檔比對僅該兩功能檔與設計、backlog 有變；**獨立重跑 `typecheck:e2e` rc 0**。但發現**新阻擋 I1**，**1a／1b 仍不放行，先前（mailroom #49）的條件式授權暫不恢復**。額外授權**最多 1.5 hr 主動工作**修正 I1。

**I1（ready 前中斷，teardown 未等待停止程序）**：`global-teardown.ts` 的 `readRunEnv` catch 分支**無條件 return**，只要 state 存在即宣稱「已在 teardownOnFailure 內完成」，**未檢查或等待 `runtime.processTree.stop` 的 single-flight promise**。reviewer 以該實際模組在「缺 run-env、有 interrupted state、processTree 可用」的受控條件呼叫 `globalTeardown`：**`stopCalls=0` 卻仍印已完成**。`103519Z` log 亦缺 TERM／KILL 完成、pid／埠核對與最終 cleanup 紀錄。**原 run 是否另有 Wails 訊號因素尚未證明，不得把原因全推給 sandbox 轉發。**

- **修正契約**：確認 Playwright ready 前取消與 signal handler／globalTeardown 的**結束順序**（以可控延遲的 stop promise 小測試證明目前會提早返回）；**teardown 與 handler 等待同一套有界 single-flight 收尾**，**不得因 run-env 未寫出就跳過**；**保留 interrupted 終態**；**只有實際完成且 pid／埠／觀測結果符合才清 pointer**，不完整時**保留 state／pointer 並非零退出**；無 runtime 時走**既有安全身分核對／恢復契約**，**不得直接相信檔案中的 pid 去 kill**；預檢前未 spawn 者仍不需虛構停止工作；**不得只改 log 文字或拉長 timeout**，**不得新增第二套無界停止程序**。
- **驗證限定**：小測試涵蓋「stop 未完成時 teardown 不得返回」「重複中斷只停止一次」「完成／失敗結果與 pointer 處理」；再做一次**新 offline 模式 ready 前 SIGINT 真實測試**，**記錄信號實際送給哪個 PID／PGID、角色與時間**，讓 TERM／KILL／埠核對**自然結束**，**不得數秒後人工 KILL 截斷證據**；**N7／N8 收尾路徑需確認無回歸**；手動緊急清理**只能在確認自家身分後**且**另記為非正常收尾、不得算 PASS**；上一輪 SIGINT 的敘述須從**實際命令**釐清（harness log 說「收到 SIGINT」與我方「送給 wails dev」的敘述不一致），**找不到原命令即標「未知」，不得追造**。

**三處必須更正（原文保留、附更正）**：

- **「四者同 pgid 71315」錯誤**（此句為 controller 於 mailroom #50 交接時自行推論寫入，未核對 log）：實際 **app 73692 在 71315 群組**，**72455／72501／72584 為非同 pgid**，實際採 **group TERM 加個別 TERM**。
- **N11b-1 原腳本未測到目標情境**：只改 `state.startedAt`、pointer 仍真值，先被「pointer 與 state 不一致」擋下，**未觸及「兩邊記錄一致但 root 實際身分不符」**。**reviewer 已自行補做正確反例**（隔離 tmp、正式 `handleStaleRun`、pointer 與 state 同為錯誤 1970 啟動時間）：`StaleRunConflictError` 正確、**signal 呼叫 0 次**、pointer 位元組不變、誘餌仍活。其證據將複製進我方證據區並**標明為 reviewer 獨立補驗**；**原反例保留但改標為「pointer／state 一致性測試」**。
- **n11bc 腳本只印結果、無斷言、未捕捉 signal 呼叫**，**不得把 rc 0 包裝成自動驗收全過**；證據依**逐項人工判讀**呈現。N11b-2／c-1／c-2 的實際錯誤輸出與程式路徑 reviewer 接受。

**後續條件（rev16）**：完成後交新 diff、限定驗證與固定快照；**仍需 reviewer 複核才能啟動 1b**，**先前的條件式授權暫不恢復**。**N11 已接受證據不必為報表全部重跑**；**僅當 I1 實際改到 stale recovery 才重驗受影響分支**。

**I1 主路徑修正成立、新增必修缺陷 A／B（rev17，依 codex-reviewer mailroom #53 裁定）**：reviewer 複核 I1 的「等待既有 stop promise」主路徑修正——**成立**，但**整份 diff 尚未通過**；**1a 不放行，1b 條件式授權仍不恢復**。驗證來源：獨立核對 tracked 10／untracked 37 逐檔雜湊、aggregate diff `d6398387…`、MANIFEST 7/7，重跑新自測 6 項通過，讀過四次原始 harness 紀錄；**N11 先前已接受的結論不撤回**。

**缺陷 A（`global-teardown.ts` 約 358 行）**：先看 `run-state.json` 是否存在就直接 return，排在 `runtime.processTree` 之前。以實際 `globalTeardown` 公開入口重現的隔離案例 `missing-state-live-runtime`：有 runtime 與 active pointer、state 遺失 → `stopCalls=0`、正常返回，log 卻宣稱 app 從未啟動。**現行自測「測項四」把「有 runtime 卻不呼叫 stop」當成預期，等於固化缺陷，需一併修正**；狀態檔不存在不能證明沒有 spawn。

**缺陷 B（同檔約 403 行）**：跨行程 fallback 只 `JSON.parse` 後直接 `stopProcessGroup`，缺 staleRun 對 pointer／state 的完整結構與一致性核對。隔離案例 `empty-processes-no-runtime`：保留有效 pointer、`state.processes=[]` → log clean=true、portsReleased=true、**pointer 被刪除**（恢復入口遺失），雖最後仍 throw interrupted；**違反既有「空清單／損毀資料保留指標」契約**。約 70 行 env 存在的純檔案分支有相同直接解析模式，需套同一檢核，避免兩套標準。

**重現材料**：保存於 `frontend/e2e/.artifacts/n11-tests/codex-review52-repro/`（`probe.mjs` sha256 `ef9beb926d944f51c42968df657d69792d4b586a12b2dbf919e69435cc9119b6`、`result.json` sha256 `faac8610f18686b3ae523b6fcfea810b2b377a4838007ba2566cef9e725f5852`，另含兩份隔離 artifacts）。**probe 攔截信號發送（signalCalls 均為 0）、未啟動 app、只讀查埠；probe rc=0 不得解讀為產品 PASS**。

**新授權（有界修正，rev17）**：**active effort 上限 1.0 hr**（等待不計；預見超時先停並報剩餘事項）；要求沿用既有驗證器與停止演算法，**不得新增 production 修改、不得開始 1b**。補測範圍：缺 state ＋ live runtime 仍 await stop；真正預檢未 spawn；無 runtime 的空 processes／pointer-state 衝突／壞 JSON 均拒絕且保留 pointer、零信號；合法 fallback 只在程序與埠都乾淨時清 pointer。最終版要跑相關自測／typecheck，並做一次 ready 前 SIGINT 與一次 N7 啟動逾時確認主流程；ready 後 SIGINT 與其他負控制可明列來源沿用。

**快照裁定（往後適用的證據規則，rev17）**：接受 `b3a1a-snapshot-20260916T111536Z` 中**已明列 112144Z 更正時間與原因的修正版 manifest** 作為本次比對依據，但**不得稱為 111536Z 原封不動封存**。**往後封存後的更正要另建 revision／addendum、保留原版，不得原地重寫已封存 manifest**。

**後續條件（rev17）**：修正缺陷 A／B 後交新 diff、限定驗證與固定快照；仍需 reviewer 複核才能啟動 1b。

**#56 執行歸屬修正通過、1a 功能審查放行、恢復 1b 驗證授權（rev18，依 codex-reviewer mailroom #57 裁定）**：reviewer 對 controller 的執行歸屬修正（mailroom #56）複核——**通過**，**1a 功能審查放行**，**恢復 1b 驗證授權**；**這不是 B3a-1 全案驗收完成，aggregate 繼續開啟**；**仍無提交／推送／PR／合併授權；1b 沒有新的 production 修改授權（StartHidden 屬既有已核准功能）**。

**reviewer 本次獨立完成的查證**：HEAD `023481440017fea4e596f039258e35259f34ca8f`；快照 `b3a1a-snapshot-20260916T120427Z` 的 47 個逐檔雜湊全符、MANIFEST 8/8 全符；相對 `114515Z` 只有 `global-teardown.ts` 與 `globalTeardownStop.selftest.ts` 變動；三份沿用 log 與來源位元組完全相同，**接受部分重跑與 reuse-basis 做法**；自行重跑 `globalTeardownStop` 自測 14/14 rc=0、typecheck rc=0；自行在全新 `/tmp` 目錄重跑五案探針（missing-state-with-pointer／pointer-to-other-run／occupied-port／root-conflict／bad-json）均 rejected、pointer 保留、signalCalls=0；其 loopback listener 已正常關閉，repo 的 `.active-run.json` 現不存在。新證據保存於 `frontend/e2e/.artifacts/n11-tests/codex-review56-repro/`。

**明列限制**：env 存在入口的完整純檔案**正向**流程仍未單獨實測，reviewer 接受作為明列限制，不要求為此再造完整 fixture；**不得宣稱「所有 fallback 組合均經真實 E2E 驗證」**。

**已完成的措辭收斂（controller 於 1b 開工前執行，純文字、未動控制流程）**：`global-teardown.ts` 的 `!stateExists` no-pointer return 分支 log、相鄰註解與對應自測名稱，原本寫成「確定 app 從未啟動」，已改為「未取得本次程序追蹤紀錄及殘留指標，本分支未執行停止；執行結果依 setup 失敗回報，不能據此證明從未啟動」。另更正一項回報用語：`canonicalizePath` 是**catch realpath 例外後退回 `path.resolve`**，先前回報寫成「不吞例外」並不精確，應精確描述為既有 fallback。改完 typecheck rc=0、`selftest:all` 54 項 rc=0。

**最終功能快照**：`b3a1a-snapshot-20260916T121017Z`（1b 綁定此份）。部分重跑：gofmt／typecheck／`selftest:all`（54 項）為本輪重跑；go race／vitest／build 沿用 `120427Z`（附來源路徑與雜湊，並註明為位元組複本、非新執行），另有 `reuse-basis.txt` 以逐檔雜湊證明變動僅限兩個 e2e TypeScript 檔。

**1b 授權內容（active effort 上限 2.5 hr，含上述文字整理；等待另計不算工時）**：(1) 同一固定功能快照上三次正式預設 smoke 全部成功，舊 smoke 僅歷史參考不能湊次數；(2) 獨立指令 controls A／B 必做，舊判定按設計失敗、新判定成功，**失敗原因必須符合受控延遲**，不得用任意失敗代替；(3) 完整一次 offline E2E 成功及阻斷外網控制證據，既有 profile／wrapper／Chrome 阻斷控制若雜湊與工具版本不變可明列來源沿用，但本輪完整 offline 成功仍需執行，**保留每秒取樣的觀測邊界**，不把抽樣未見流量寫成全面證明；(4) `expect.poll` 失敗 trace 證據若 spec／poll／trace 設定未變可附來源與雜湊沿用，**需能指出 trace 內實際可見的錯誤內容**，沒看到就列限制，**不得用 Node stderr 代替 trace 證據**；(5) 每次執行後核對 pid、實際用到的埠與 pointer，異常走既有有界停止，檔案完整性、tripwire、network 等判定照設計執行，**writer 全部關閉後再封存**，manifest 自驗，保留所有失敗證據與 check reuse mapping；(6) 1b 只做驗證，不追加功能／production 修改，失敗先保留與分類、**不反覆重試到綠**，需要改碼或預見超 2.5 hr 先回報，完成後交 reviewer 做全案驗收、不自行結案；開工前仍需自行預檢程序／埠／pointer，遇他人占埠直接失敗、不動占用者。

**§4 帳表更新（reviewer 核定）**：本輪（歸屬修正）實測 11:55:13–12:02:06＝413 秒＝**約 0.115 hr**（顯示可四捨五入為 0.11 hr；**active 約等於 wall clock 是估計，未逐秒量測 active**）。**S1 implementation = 6.615 hr**（原 6.50 ＋ 0.115）；**1a 合計 = 67.365 hr**；**B3a-1 已知 = 69.415 hr／6.94 pt**；**B 軌 = 209.465 hr／20.95 pt**；**全體 314.265 hr／31.43 pt**（Decimal ROUND_HALF_UP 驗算相符；**保留未四捨五入的原始秒數計算〔413 秒〕，報表數字由同一公式產生，避免加總漂移**）。**上一輪未核實回溯估算（獨立欄位，不併入上列數字）**：上一輪估計耗時約 55–70 分鐘，**未核實**；controller 實測其中兩次 harness 執行的起訖時間，合計約 47 秒——**只證明那兩次執行本身的時間範圍，不能倒推整輪其餘時間全為 active**；不抹除、不任選落點、不混入上列已核定的 hr 基底。**這不是 B3a-1 全案驗收完成，aggregate 繼續開啟**；**本次 2.5 hr 為新增驗證上限，未發生前不得入帳**；**下次超上限前須先報告**。

### 2.8 `data-test` 補強

- 為 E2E 需要的主分頁按鈕與檔案樹項目加上 `data-test`，只加屬性，不改行為。
- 以 E2E 實際使用來驗證，不另寫只檢查屬性存在的單元測試（owner 裁定）。
- 既有 vitest 必須全綠。

### 2.9 失敗證據（統一目錄）

- **統一目錄**：`frontend/e2e/.artifacts/<run-id>/`（加入 `.gitignore`）。
- **harness 產出**：`harness.log` 從預檢第一步就開始寫，**啟動失敗也一定有**。同一目錄另放：
  - `wails-dev.log`
  - `invocations.log`、`preflight-invocations.log`
  - `network-samples.log`（rev4：含程序取樣層分類標籤與 browser 層 violation 清單）
  - `chrome-argv.txt`（rev4 新增，§2.7：Chrome 主程序實際啟動參數）
  - `run-state.json`
  - fixture 的 `git log`／`git status`，以及目標檔案的最終內容
- **Playwright 產出**放在同一目錄的 `playwright/` 子目錄：`trace: 'retain-on-failure'`、`screenshot: 'only-on-failure'`。
- **不承諾所有失敗都有 trace**：trace 只記錄測試本體的瀏覽器操作；globalSetup／globalTeardown 階段的失敗（預檢、啟動、收尾）**不會有 trace**，只有 `harness.log` 與 `wails-dev.log`。
- **路徑提示**：失敗時在終端機印出證據目錄路徑；成功時預設清除，設 `E2E_KEEP_ARTIFACTS=1` 則保留。
- **README 措辭（R1，rev4）**：README 是由另一施工 agent 同步修改的文件，**不得把本票（B3a-1）已在本設計範圍內的驗收項目寫成「後續票才做」**；設計與 README 的驗收範圍敘述須一致。
- **觀測失敗不得當成通過（R3，rev6，依 B3a-1a 複核 mailroom #14 裁定）**：`network-samples.log` 缺失、或 `run-state.json` 的 `observationFailures` 欄位讀取失敗，**都不得當成通過**（不能因為「沒讀到違規記錄」就判定 0 違規）；缺失或讀取失敗本身就是一種觀測失敗，走 §2.3／§2.7 既有的「無法觀測即判失敗」規則。
- **封存順序（R5，rev6，取代 rev4/rev5 對證據目錄的敘述，避免驗證動作污染已核算的 hash）**：
  1. **先關閉所有 log writer**（`harness.log`、`network-samples.log`、`invocations.log`、`chrome-argv.txt`、browser violation 記錄等，全部 flush 並關閉 file handle）。
  2. **再產生 manifest**（對封存目錄下的所有檔案計算 hash，寫入 manifest）。
  3. **最後獨立驗證全部 hash**（重新讀取每個檔案、重算 hash、與 manifest 比對）。
  - **驗證輸出不得再寫回已算過 hash 的檔案**——這是節點 3a 複核發現的封存缺陷成因：`checks.log` 在算完 hash 之後又被追加了兩行，導致證據 MANIFEST 只有 5/6 通過。驗證步驟的輸出（例如驗證結果訊息）只能寫到 manifest 範圍**之外**的新檔案，不能回寫進已經被計入 manifest 的既有檔案。
  - **舊快照與 manifest 保留並標示失效或補充說明，不覆寫舊 checksum**——例如 20260915T115335Z 快照因這個成因判定未通過，**保留原檔與原 manifest**，另外附加一份說明文件標示失效原因，不得刪除或覆寫原有 hash 記錄。
  - **封存缺口（rev7，依 B3a-1a 第二次複核 mailroom #31 裁定）**：`evidence-manifest-20260916T-r1r5-round.json`（9 runs／122 檔）**不涵蓋** `014853Z`／`021929Z`／`023211Z`／`024527Z` 四組 R3 run（fail-closed 指定反證）——這四組 run 需**另行補封存**（依上述先關 writer→產生 manifest→獨立驗證的順序），原 manifest **保留不動**，**不得宣稱這 9 runs／122 檔已涵蓋這四組 R3 run**。

**受版控產物檢查（Q3，rev5 新增，依節點 3 審查裁定）**：

- **範圍**：`frontend/wailsjs/**`（受版控部分）、`go.mod`、`go.sum`、`frontend/package.json`、`package-lock.json`、`package.json.md5`。
- **時機**：**每次 run 的前後**都要檢查——不是只在收尾檢查一次；前後各存一份內容與可執行位元（mode）的快照。
- **判定**：內容或 mode 有任何變動 → **讓該次 run 失敗**，保留差異（diff）並在 harness.log 提示；**teardown 不自動還原**，避免掩蓋真實變更或造成後續狀態不一致。
- **mode 基準**：以 **HEAD 的預期值**為準，不能把工作樹裡已經意外變成 `755` 的既成狀態當成合格基準（否則會把既有問題誤判為「沒有變化」）。
- **既有工作樹變更的處理**：如果 run 開始前工作樹本來就與 HEAD 有差異（例如施工中未 commit 的合法改動），改用**執行前後比對**來保護——run 前存一份當下狀態，run 後比對是否相對 run 前又發生變化，而不是強制要求與 HEAD 完全一致。
- **一次性修復（不是 harness 自動行為，rev3 節點 3 審查當下核准的歷史記錄）**：reviewer 當時授權把 `frontend/wailsjs/runtime/` 三個檔案的 mode 一次性還原為 `644`——**先保存 mode diff**（還原前的實際 mode），**並確認內容與 HEAD 相同**後才還原；這是節點 3 審查當下的一次性人工修復，**不寫入 harness 的自動行為**。
- **wailsjs 裁定（rev6，取代上一條「一次性修復」作為現行行為，依 B3a-1a 複核 mailroom #14 裁定）**：harness 對 `frontend/wailsjs/**` 一律**執行前預檢加執行後核對，兩者都不自動修復**——不管是預檢還是收尾核對，發現 mode 或內容事故，都只把**該次 run 記為失敗並保留證據**，不嘗試自動改回預期狀態。
  - **不得反覆重跑直到湊出三次綠燈**：如果 mode 事故偶發，不能靠多次重跑「運氣好沒事故的那幾次」拼湊出 §3 要求的三次正式連跑，這樣不能反映真實的可重現性。
  - **再次發生時的處理程序**：先**捕捉 inode、mode 與產生路徑**（是哪個程序、哪個時間點、寫出了什麼 mode）並據此定位原因，再提最小修正；**不得把推測寫成根因**（例如「推測 wails 刪除重建時以 0755 寫檔，未驗證」這類措辭只能停留在推測，不能升級成報告裡的確定結論）。
  - **手動還原僅限前次的狹義條件**：保留差異（先存 mode diff）、**只有這三個檔案**的 mode 受影響、**沒有其他 writer** 介入、**內容未變**（與 HEAD 相同）——同時滿足這四個條件才可比照前例手動還原；條件不全滿足時，不還原、保留現場、回報 reviewer。
  - **wailsjs 裁定不變、限制保留（rev7，依 B3a-1a 第二次複核 mailroom #31 裁定）**：上述裁定**維持不變**（執行前預檢＋執行後核對、兩者都不自動修復、失敗留證據、不得反覆重跑湊綠燈）。**第三次事故根因仍未確認**，限制照舊保留、不得升級為確定結論。**`package.json.md5` 的變動即使可歸因於已知的 `package.json` 編輯**（能找到對應的合法改動來源），**該次 run 仍是 artifact check failure，不得改判為 PASS**——版控產物檢查是「這次 run 期間內容或 mode 是否變動」的判定，不因事後能解釋成因就回溯改判結果。
  - **風險裁定 2：wailsjs 已有明確來源，授權隔離驗證（rev9，依 B3a-1a 第三次複核 mailroom #35 裁定）**：reviewer 實讀本機 v2.13.0 原碼確認 wails mode／內容變動的可能來源——`internal/app/app_bindings.go` 的 `generateBindings` 依序為 `RemoveAll(runtimeDir)` → `Extract` → `fs.SetPermissions(wailsjsbasedir, 0755)`；`internal/fs/fs.go:272` 的 `SetPermissions` 會 **Walk 每個檔案**並個別 `os.Chmod`；`pkg/commands/build/base.go:428` 的 `generateRuntimeWrapper` 另有一段獨立的 `RemoveAll(wrapperDir)` → `Extract`。**這只是已知的可能路徑，尚未證明先前六次事故各自實際走了哪一階段**。
    - **授權範圍**：在**隔離暫存副本**（不動工作樹）做**一次有紀錄**的 bindings／compile 階段驗證，之後提出最小處置。
    - **禁止**：patch 模組快取；升級 Wails；把 HEAD 的 mode 全部改成 `755`（用「反正都一樣」的方式規避問題）；放寬 artifact 檢查（例如允許這幾個檔案的 mode 差異）。
    - **`-skipbindings` 的使用前提**：若考慮用 `-skipbindings` 規避問題，**須先確認** CLI 是否支援此旗標、bindings 是否仍與 Go 端同步、跳過後實際仍會產生哪些 runtime 檔——**不得用猜測代替實測確認**。
    - **維持既有裁定**：仍是記失敗、不自動還原、不湊綠燈；本次授權只是多了一次「有紀錄的隔離驗證」機會，不改變 wailsjs 裁定本身。
  - **`-skipbindings`（有條件核准，rev10，依 B3a-1a 第四次複核 mailroom #37 裁定，取代上一條「使用前提」的初步敘述）**：
    - **採用前置作業**：須在**隔離副本**（不動工作樹）以**相同的 Wails v2.13.0** 產生一份 bindings，**完整比對** go bindings 與 runtime 檔案的內容對照目前工作樹——保存這次比對的**命令、版本、比較證據**。
    - **判定**：**內容相同才可採用** `-skipbindings`；**有差異則保留差異、不採用，交 reviewer 裁定**，**不得自動覆寫 repo** 去湊出「內容相同」的假象。
    - **既有檢查保留**：mode 與內容的前後檢查（Q3 §2.9）不因採用 `-skipbindings` 而放寬或省略。
    - **文件必須明列的限制**：**未來只要 Go binding API 有改動，就必須重新產生一份 bindings 並重新核對**——這不是一次性驗證就能永久沿用的結論；`tsc` 通過或 bindings 內容前後不變，**都不能證明 bindings 本身是新鮮的**（可能只是剛好沒觸發到有差異的 API 面）。
    - **採用後的驗證**：以**一次 normal smoke**（正常啟動路徑）與**一次啟動失敗路徑**分別確認行為正常；**不改 Wails 本身或 module cache**。
    - **範圍聲明**：先前的隔離重現（風險裁定 2 的授權驗證）**只證明了 chmod 機制本身的可能路徑**，**不得據此宣稱歷史六次 wailsjs 事故都已逐一被證實**——這仍是限定範圍的一次驗證，不是對過去所有事故的根因結案。
  - **`-skipbindings` 已採用（rev11，依 B3a-1a 第五次複核 mailroom #39 裁定）**：reviewer 本輪**自行**在隔離副本跑 `wails generate module`，rc 0，**6 檔清單與逐檔 SHA-256 完全一致**（對照上述「採用前置作業」的比對條件）——**接受採用** `-skipbindings`；上方「有條件核准」各項限制（未來 Go binding API 改動須重新核對、`tsc` 通過不能證明 bindings 新鮮、採用後一次 normal smoke＋一次啟動失敗路徑確認、不改 Wails 或 module cache）**維持不變、持續適用**，不因本輪已採用就放寬。

### 2.10 真實執行與假資料；Browser E2E 與 native GUI 的界線

| 路徑 | 真實執行 | 假資料／隔離 |
|---|---|---|
| Go backend | ✓（`wails dev` 以 `-tags dev` 編譯） | — |
| Wails binding（JS↔Go） | ✓ | 對照 A／B 在測試端包裝 `SpecWrite` 加延遲 |
| 檔案 I/O、git | ✓ | 只作用於 `mkdtemp` 下的 fixture |
| git 使用者設定 | ✓（依 HOME 讀取，F8） | — |
| provider CLI | — | tripwire 腳本，只回應恰好一個 `--version` |
| 瀏覽器 | ✓ | — |
| 網路 | **目標**：只有 loopback；**驗證方式（rev4：兩層）**：程序取樣層（全程取樣，含啟動期，Chrome 背景網路記錄但不列入預設判定）＋browser 層（`BrowserContext` request／WebSocket 攔截，非 loopback 直接失敗）＋一次 sandbox-exec 阻斷實測（依審查補充 E，rev2「只有 loopback」改為目標與驗證方式，不當作既有已證實事實） | — |

- **Browser E2E 涵蓋**：Vue UI、Go backend、Wails 開發伺服器的 IPC 與事件，在 Chrome 中的行為。
- **不涵蓋**：WKWebView 渲染與 native window、打包後的 `.app` 與 `tools: bundle`、macOS 權限（TCC）。
- **E2E 通過不得寫成 GUI 驗收通過。**

## 3. 驗證計畫（施工時）

- `npm run test:e2e` 連跑 3 次，全部通過；既有 vitest、build、`go test ./... -race`、gofmt 不受影響。
- **對照**：`npm run test:e2e:controls` 在同一延遲下，A 如預期失敗、B 成功。
- **負控制**，各自須紅在正題，並確認沒有殘留程序或佔用的埠：

| # | 注入 | 預期結果 |
|---|---|---|
| N1 | 假 CLI 被以非 `--version` 參數呼叫 | 收尾的 tripwire 判定失敗 |
| N2 | 刪除 `invocations.log` | 失敗（呼叫紀錄缺失不算通過） |
| N3 | **缺少 `WORKBENCH_TOOLS_DIR` 設定** | **尚未啟動 app 就失敗**（`harness.log` 顯示中止點在 spawn 之前，程序表中沒有 `wails dev`） |
| N4 | fixture 不可寫 | 啟動前失敗 |
| N5（依審查補充 D 改寫） | **先通過啟動前預檢**（假 CLI 在預檢那次回正確版本），**之後**才讓假版本字串不符（例如之後的呼叫回不符版本） | 啟動後核對失敗，停止程序完整收尾；若在預檢階段就失敗，**不算 N5**，須重做讓中止點落在「啟動後核對」 |
| N6 | 34115 已被一個 dummy listener 佔用 | 啟動前失敗，**dummy listener 仍存活** |
| N7 | 啟動逾時上限設為極小值 | 停止程序執行，埠全部釋放 |
| N8 | 測試中途送 SIGINT | 中斷路徑收尾，埠全部釋放 |
| N9a（rev4：原 N9 拆分，程序取樣層） | 關閉取樣白名單，改把 loopback 視為外連 | 程序取樣層判定失敗（證明判定有作用）。**證據紀律（rev5）**：第一次 N9a 執行（`090853Z`）因 R2 exit 1 迴歸誤判為 0 違規通過，其原始證據已被施工 agent 刪除，標示為「**原始證據缺失**」（不可回補）；修正後重跑為 612 違規。**往後失敗或作廢的證據一律保留並標示失效，不再刪除。** |
| N9b（rev4 新增，browser 層） | 頁面對保留測試網域各發一次 fetch 外連與一次 WebSocket 外連 | 兩次都在連線前被攔下、記錄原始 URL、整次 run 失敗；**同一次執行中正常 loopback 請求仍要能跑完**，證明攔截不誤傷本地流量 |
| N10（依審查補充 A；rev5 改用測試專用協調點） | 改用**測試專用的啟動故障協調點**：確認目標後代程序已建立、app 尚未 ready，才觸發強制失敗（取代 rev4 對時機不明確的「spawn 後、ready 前」描述，避免命中窗口不穩）；必要時可用可控的假啟動器驗證同一條 lifecycle path，但**須明確標示哪些是 fake 證據、哪些是真 Wails 證據**，不得混用呈報 | 所有已追蹤程序（含非同 pgid 後代）清除、埠釋放，`harness.log` 記錄 failure stage |
| N11a（依審查補充 A；rev4 加一種情境） | (1) 前次執行殘留 `.active-run.json` 且記錄的程序仍存活、**身分（pid＋啟動時間＋命令列＋pgid）相符**；(2) rev4 新增：**root（`wails dev` 主程序）已消失，但記錄中的某個後代仍存活且身分相符** | 兩種情境下次啟動時都要被辨識並清理 |
| N11b（依審查補充 A；rev4 加一種情境） | (1) 以一個仍存活的誘餌程序模擬「身分不符」（pid 被重用，或啟動時間／pgid 與記錄不符）；(2) rev4 新增：**後代（非 root）身分不符** | 兩種情境都以失敗結束並保留診斷，**未送任何信號**，誘餌程序仍存活 |
| N11c（依審查補充 A；rev5 依缺口 C 擴充） | (1) `.active-run.json` 遺失或無法解析／缺必要欄位；(2) rev5 新增：**內容可解析但結構無效**——記錄為空陣列、或必要欄位（PID／PGID／身分欄位）缺失 | 兩種情境都以失敗結束並保留診斷，**不送任何信號**，保留現有指標不清除、不改寫 |
| N12（依審查補充 R2；rev5 依缺口 B 擴充） | (1) 注入 `ps`／`lsof` 呼叫錯誤（逾時、找不到指令、權限拒絕）；(2) rev5 新增：**exit 1 且 stderr 帶明確錯誤訊息**（例如 permission denied），同時核對此情境下若有有效 stdout 是否仍被保留 | 兩種情境都判定失敗並保留錯誤與可取得的診斷（含情境 (2) 的 stdout，若存在）；**不得誤判為通過**（不得判成 clean 或 0 違規），也不得把有效 stdout 隨 exit 1 一併丟棄 |
| N13（依審查補充 R4） | 一個忽略 SIGTERM 的自家 fixture 程序 | 驗證升級到 SIGKILL 的時間點與 TERM／KILL 各自上限，總停止時間在上限內；分別記錄的兩段耗時可對照驗證；任一段超出其自身預算＋量測容差即列為失敗或需分析 |
| N14a（rev5 新增，依缺口 A） | 注入證據檔（例如 `network-samples.log`）寫入失敗 | 判定失敗；違規與觀測錯誤仍須同時保存在記憶體並反映在最終測試結果，**不得因寫入失敗而靜默放行變綠** |
| N14b（rev5 新增，依缺口 A） | 證據檔在收尾判定讀取之前被刪除 | 判定失敗；networkGuard 不得因證據檔缺失而略過判定、更不得因此判成通過 |

**R3 指定反證（fail-closed 定點反證，rev7 狀態更新，依 B3a-1a 第二次複核 mailroom #31 裁定）**：這是 controller 於第二輪要求補做的一組指定反證（**不接受「與 N14a 同構」的模擬代替**），驗證 controls／smoke 在 guard 本身故障時確實 fail-closed：`20260916T014853Z-92994e`（controls 三支皆紅、失敗訊息指向 guard 且在 `test.fail()` 之前）、`20260916T021929Z-6ec7c7`（`networkLogMissingOrUnreadable=true`、`testFailed=false`）、`20260916T023211Z-322e0b`（`runStateReadFailed=true`）、正常路徑 `20260916T024527Z-892e38`（兩旗標皆 false、PASSED）。**現況與 R3-a 文案更正**：現行實作已是以 chmod 0444 使目標檔唯讀後觸發外部 fetch，reviewer 解讀三份 trace.zip 確認有真實 `EACCES` 與各自的 guard `writeFailures` 斷言錯誤——**「再補一種唯讀反證」的選項與現況不符，不再列為待辦**。**限制**：這是 reviewer 對 trace 內容的檢視（讀取 trace.zip 內容），**不等於 1b 驗收項「失敗案例的 `expect.poll` trace 實際開啟檢視」**，兩者不可互相取代。

- **驗收項清單**（本輪驗收必跑，逐項須有可對照的證據；**rev5：拆為 B3a-1a／B3a-1b 後，以下項目由兩票分別承接，見 §4**）：
  - **E2E 專用 typecheck（R4，rev6 新增，依 B3a-1a 複核 mailroom #14 裁定）**：新增 E2E 專用的 `tsconfig`（strict）與 `typecheck:e2e` 指令，涵蓋 harness、spec、controls、support 全部 `.ts`，並支援原生 TS selftest 的 `.ts` import；列為**固定快照的檢查項目**（與 Vitest／build／`go test`／gofmt 同等地位，必須通過才算快照交付）。已知兩個真實型別問題須**真的修**：`global-setup.ts:259`（TS2339）、`support/psUtil.ts:180`（TS2322）；**不得用 blanket `any` 或 `@ts-ignore` 繞過**。
  - **selftest npm scripts（核准，rev10，依 B3a-1a 第四次複核 mailroom #37 裁定；rev11 接受採用並更正數量）**：新增 selftest 的 npm script（逐一執行各 focused selftest，例如 §2.3 L1 新增的三項）、一個彙總指令 `npm run selftest:all`（一次跑完所有 selftest）、README 補充說明；**沿用現有工具、不新增框架**；記錄 Node 版本與 loader 需求（原生 TS 執行的 `--experimental-loader`）。selftest **納入固定快照的檢查項目**（與 typecheck、Vitest 等同等地位）。**不取代** `npm run test:e2e:controls`（§2.5 對照 A／B 仍是獨立驗收項）；**不把負控制（N 系列）放進預設 smoke**（`npm run test:e2e` 仍只跑主 smoke，負控制各自獨立執行）。**數量更正（rev11）**：`npm run selftest:all` 實際涵蓋 **32 項**（artifact 4／network parser 14／stopProcedure 14），**不是先前誤寫的 14 項**；本輪 reviewer 實跑通過並接受採用此指令。
  - `npm run test:e2e` 在**固定快照**上連跑三次，全部通過（B3a-1b）
  - `npm run test:e2e:controls`（對照 A／B）
  - 負控制 N1–N8、N9a、N9b、N10、N11a、N11b、N11c（含 rev5 擴充情境）、N12（含 rev5 擴充情境）、N13、N14a、N14b 全部紅在正題
  - 既有 Vitest 全綠、`npm run build` exit 0、`go test ./... -race` 全綠、`gofmt -l .` 無輸出
  - §2.7 網路端點分類 parser 測試案例（CLOSED、IPv4／IPv6 loopback、外部遠端、wildcard LISTEN、未知格式）全綠
  - 受版控產物檢查（Q3，§2.9）：run 前後內容與 mode 皆無非預期變動，或有變動時已如實列為失敗與差異
  - 一次 sandbox-exec 外網阻斷實測（§2.7），含 profile 內容與包裝命令證據（B3a-1b）
  - Chrome 主程序 argv 證據（`chrome-argv.txt`，§2.7）；已知含 `--no-sandbox`，不宣稱 Chrome 自身 sandbox 啟用
  - 至少一次失敗案例的 `expect.poll` trace 實際檢視（不只確認 zip 完整）（B3a-1b）
  - 各命令的執行結果與 rc、失敗原因、程序及埠清理結果、受測檔案的 hash／manifest 一併保存；TERM／KILL／埠核對三段耗時分別記錄並對照預算；驗收證據不足之處明列為限制，不得含混帶過
  - **兩次早期 smoke（`20260915T074838Z-dad886`、`20260915T075403Z-6b355f`）與節點 3 第一部分審查前的證據只當歷史初步證據，不算本輪驗收**；F1／F2／R2–R4／Q1／Q3／缺口 A–C 修正完成、交付固定快照後，才由 B3a-1b 另做三次正式連跑與整合 review
- 獨立 review（禁止再委派）。**rev11 更正（§4 B3a-1b）**：1b 階段**不再委派獨立 review，由 reviewer 負責最終 review**；本條僅適用於 1a 各子票的複核流程。

## 4. 拆票與估點調整（**#65 最終裁定：B3a-1 技術驗收通過，aggregate 標為技術驗收完成（rev19，codex-reviewer mailroom #65，2026-09-17）：未提交／推送／PR／合併，B3a-2／B3a-CI 不受影響、未獲施工授權**）

### B3a-1（**rev5 改為 aggregate**）

- 保留 B3a-1 原有的**全部驗收責任**（§2、§3 所列各項），不因拆票而省略任何一項。
- 原 **14.0 hr／1.4 pt 改列為歷史估計**（rev3 裁定值），**需重估**，不重複計入 backlog 小計（見 backlog rev49）。
- B3a-1a 與 B3a-1b **兩票都完成才能關閉** B3a-1。
- **rev19 更新**：codex-reviewer（mailroom #65）最終裁定 **B3a-1 技術驗收通過**，**aggregate 標為技術驗收完成**；「技術驗收完成」與「關閉／結案」為不同狀態——**未提交、未推送、未開 PR、未合併**，commit／push／PR／merge 需另行授權。

### B3a-1a（**彙總，第五次複核（mailroom #39）：接受 L1／selftest／`-skipbindings`，程式面無新阻擋項，只差 N13 證據；lifecycle-1 維持 aggregate、拆法改採 (c)（見下）**）

- **範圍**：harness 實作全部（§2.1–§2.10）、rev4–rev11 各項修正（F1／F2 browser 判定、R1–R5＋rev7 補正 A–C、A1–A5 原反例已接受、L1 已接受、Q1 端點分類、Q3 版控產物檢查、wailsjs 裁定＋風險裁定 2＋`-skipbindings` 已採用、E2E typecheck、selftest npm scripts（32 項）、封存缺口補件）、controls（§2.5）、全部負控制 N1–N14b（§3，**含 N13 補跑證據**）、受影響的既有檢查（Vitest、build、`go test ./... -race`、gofmt、`typecheck:e2e`、`selftest:all`）。
- **交付物**：一個**可供整合驗收的固定快照**（commit 或等效的凍結點），供 B3a-1b 在同一快照上做正式連跑與整合驗收，不在快照凍結後於 1a 範圍內繼續修改。
- **rev6 裁定沿用：1a 缺陷修正不得移入 1b**——1a 的實作缺陷一律留在 1a 修正，**不得**把它們挪到「固定快照驗收」的 1b 範圍內處理；1b 只做驗收，不做實作修正。
- **rev7：子票由兩張改為三張**（依已有可驗證的交付邊界，1a 仍為彙總，三子票皆完成才算 1a 完成；且仍須完成本輪 A–C 剩餘修正才算真正通過）；**rev9：lifecycle-1 因中位逾 20 hr 拆票線再拆兩張**；**rev11：lifecycle-1 拆法改採 (c)，共三張子票（見下方獨立小節）**：
  - **B3a-1a-browser**：browser／spec／save controls——§2.4 smoke flow、§2.5 存檔判定受控對照（含 R3 guard 記憶體狀態檢查位置）、§2.7 browser 層（含 N9b、R3 判定時機）、§2.6 fake provider tripwire。**狀態**：本子票範圍內的 R3 指定反證、R4 typecheck 已通過。
  - **B3a-1a-lifecycle-1（rev9 起 aggregate，rev11 拆法改採 (c)：startup／stop／新增「ps 觀測語系與解析失敗處理（L1）」三張子票；見下方獨立小節）**。
  - **B3a-1a-lifecycle-2（失敗判定與觀測完整性）**：teardown 判定邏輯（含缺口 B／C、fail-closed）、§2.7 程序取樣層（含 Q1 端點分類）、§2.9 證據封存（含 R5 封存順序、**rev7 封存缺口補件**、Q3 版控產物檢查、wailsjs 裁定、**rev9 風險裁定 2 的隔離驗證、rev11 已採用的 `-skipbindings`**）、trace 數字更正與快照 manifest 補強（§2.7、§2.7a）。**狀態**：R5 封存順序、封存缺口補件已通過；R1／R2 相關工作已於 lifecycle-1 完成。**rev13：核定 20.00 hr（已達 20 hr 拆票上限）**——18.05 ＋ 第十一輪其餘 1.1 ＋ 本輪 trace 更正 0.2／總表 0.5／回報 0.15；**後續與 S1 相關的實作與驗證工作另開「S1 offline 模式實作與驗證」新票（見下），不得塞回本票**。
  - **S1 spike（已完成，0.60 hr）**：讀 reviewer 意見 0.1 hr ＋ S1 診斷（受控診斷本體）0.5 hr；**已計入 1a 合計，不混入下方的 S1 實作新票**。
  - **S1 offline 模式實作與驗證（新票，rev13 新增，rev17／rev18 更新狀態）**：§2.7a 所述的兩項前置小測試、實作授權範圍（launcher／`processTree`／Playwright 設定／support／profile／Chrome wrapper）、`-viteservertimeout` 併案、1a 新驗證順序（N7／N8／N10／N11／N13 在新模式下）、新快照 manifest 要求；P1／P2 修正（rev14）、N11 分支化驗收（rev15，N11a／N11b／N11c）；I1 修正契約與驗證限定、三處更正（rev16：pgid 推論錯誤、N11b-1 補正反例、n11bc 腳本人工判讀）；**rev17 新增範圍**：I1 主路徑修正（等待既有 stop promise）已成立，但發現**新必修缺陷 A／B**（見上／§2.7a）待修，快照更正規則新增。**rev18 新增範圍**：codex-reviewer（mailroom #57）裁定 #56 執行歸屬修正通過，缺陷 A／B 修正通過，1a 功能審查放行。S1 實作**已知 6.615 hr**（6.50 已計入 rev17 ＋ 本輪歸屬修正實測 0.115），計入 1a 合計；整票仍**估點待估**（總工作量尚未核定）；**狀態（rev18）：缺陷 A／B 修正通過（mailroom #56 執行歸屬修正），1a 功能審查放行，恢復 1b 驗證授權（active effort 上限 2.5 hr，等待不計，六項授權內容見 §2.7a）；仍非全案驗收完成，aggregate 繼續開啟**；後續實際投入另計，**不塞回已滿 20 hr 的 lifecycle-2（維持 20 hr，不再加塞）**。
  - **若任一子票的剩餘修正使工作量再度超過 20 hr**，依實際交付邊界續拆，不勉強塞進單一子票。

### B3a-1a-lifecycle-1（**維持 aggregate，拆法採 (c)：三張子票，分攤已核定（無未分攤池）**）

- **背景**：本票原涵蓋 §2.2 隔離預檢／啟動後核對、§2.2a StartHidden 最小驗證（**已通過**）、§2.3 啟動階段可靠持有（R2，**已通過**）、停止程序與殘留處理（single-flight、R1 送信號一致性驗證）、state 結構驗證、N8 中斷路徑。第三次複核裁定 B（R2 迴歸）、C（R1 一致性）已通過；第四次複核：A1–A5 的原反例修正已接受，但發現新的阻擋缺陷 L1；第五次複核：L1 修正、selftest 指令、`-skipbindings` 採用皆已接受，程式面無新阻擋項，1a 只差 N13 補跑證據；第六次複核（1b）：三次正式 smoke、controls、trace 診斷能力接受，但整體未通過——新發現阻擋項 S1；**第七次複核（mailroom #45）：reviewer 接受 S1 架構方向並授權 offline 驗證模式實作（§2.7a）**。**依 reviewer 裁定維持 aggregate**，三張子票皆完成才算本票完成。
- **拆法 (c)，三張子票，分攤核定（無未分攤池，rev13）**（不平均分、不重複計入，各票高端 ≤ 20 hr）：
  - **B3a-1a-lifecycle-1-startup（startup／隔離與持久狀態，含 StartHidden）**：§2.2 隔離預檢／啟動後核對、§2.2a StartHidden 最小驗證、§2.3 程序生命週期骨架與啟動階段（R2 啟動階段可靠持有、兩份持久資料一致性）、state 結構驗證。**狀態**：本票涵蓋的 R2（迴歸 B）、StartHidden 皆已通過。**核定 9.55 hr**（不變）。
  - **B3a-1a-lifecycle-1-stop（stop／身分驗證／stale recovery）**：§2.3 停止程序（R4）、送信號前一致性驗證（R1，**A1–A5 原反例已接受**）、殘留處理與前次殘留辨識（staleRun recovery）、N8 中斷路徑、風險裁定 1（觀測逾時待分析，**rev10 收斂逾時診斷措辭**）、**N13 補跑證據（已通過，見 §2.3）**。**狀態**：已通過。**核定 19.50 hr**（rev12 的 19.2 hr ＋ N13 補跑本身 0.3 hr）。
  - **B3a-1a-lifecycle-1-locale（「ps 觀測語系與解析失敗處理（L1），含對應測試與驗證」）**：§2.3 L1（`ps` 輸出解析依賴 locale、`LC_ALL=C` 固定、`ToolObservationError`、focused selftest）、語系結論收斂措辭、`psUtil.ts` 註解更正（已完成，只改註解）。**狀態**：已接受（見 §2.3「L1 接受、註解更正與語系結論收斂」）。**核定 3.60 hr**（rev12 的 3.4 hr ＋ 註解更正本身 0.2 hr）。
  - **三張合計（rev13 核定，無未分攤池）**：9.55 ＋ 19.50 ＋ 3.60 ＝ **32.65 hr**。
- **去重處理已完成（rev12 裁定沿用）**：rev12 已用扣減方式解決「原 stop 範圍是否已含 L1」的矛盾，本輪（rev13）在此基礎上把先前留在 lifecycle-2（第十一輪 1.6 hr）中屬於 stop（N13 補跑 0.3 hr）與 locale（註解更正 0.2 hr）的部分，**依實際項目歸戶回本票**——**不是**重新引入矛盾，是把已知的第十一輪工作按項目分攤到正確的子票。

### B3a-1b（**舊快照四項已執行但整體未通過；新版 1b 範圍已改採 offline 驗證模式（mailroom #45）；I1 主路徑修正成立但整份 diff 未過，新阻擋缺陷 A／B，條件式授權仍暫停**）

- **rev13 範圍更新**：新版 1b 不再是「補完舊快照上的 sandbox 外網阻斷實測」，而是**在新的固定快照**（1a 完成 §2.7a offline 模式實作後交付）上重跑：三次正式**預設模式** smoke、controls A／B、**完整 offline 模式的一次端到端成功案例與一次阻斷對照案例**、清理與證據完整性核對；細節見 §2.7a「新版 1b 範圍」。舊快照 `20260916T073313Z` 上執行的四項（見下方 rev12 回顧記錄）**保留為歷史驗收依據**，但**不是**新版 1b 的替代。
- **rev14 範圍更新（mailroom #47）**：新版 1b 剩餘工作估算區間 **1.5–2.5 hr**（估算，非核定值），純等待另列；**仍未獲授權在快照 `20260916T095245Z` 上啟動**——1a 的 P1（預檢未達宣稱契約）與 P2（config 載入前失敗無 harness.log）兩個缺口尚未通過複核；**不得**把這個估算與已完成的舊版 1b 2.05 hr 混為同一批驗收證據或帳目。
- **rev15 範圍更新（mailroom #49）**：P1／P2 已接受，**1a 無新的實作阻擋**；**條件式授權可直接開始，不需再請示**——條件為 N11 三分支應驗情境全數滿足、證據完整、功能檔仍為 `102306Z` 已審內容（逐檔 hash 相同）、無新未知殘留或觀測失敗；**任一功能檔需修改即不適用**，須交新 diff 複核後才能啟動。範圍維持 mailroom #45 既定內容（三次正式預設 smoke、controls A／B、offline 端到端成功與阻斷對照、清理與證據封存），細節見 §2.7a「1b 範圍（rev15）」。**額外主動工作上限 2.5 hr**，剩餘估算仍 1.5–2.5 hr、純等待另列。
- **rev16 範圍更新（mailroom #51）**：reviewer 接受 `runState.ts`／`staleRun.ts` 的語法相容性修改與 N11 補件，但發現**新阻擋 I1**（ready 前中斷，`global-teardown.ts` 未等待停止程序的 single-flight 收尾即宣稱已完成，見 §2.7a）；**1a／1b 仍不放行，先前（mailroom #49）的條件式授權暫不恢復**。修正 I1 後須交新 diff、完成限定驗證與新固定快照，**仍需 reviewer 複核才能啟動 1b**；**N11 已接受證據不必為報表全部重跑**，僅當 I1 實際改到 stale recovery 才重驗受影響分支。額外授權**最多 1.5 hr 主動工作**修正 I1。
- **rev17 範圍更新**：reviewer 複核 I1 修正——**「等待既有 stop promise」主路徑成立，但整份 diff 尚未通過**；新增必修缺陷 A（`global-teardown.ts` 約 358 行，state 遺失即誤判 app 從未啟動）與缺陷 B（約 403 行跨行程 fallback 缺一致性核對，可能誤刪 pointer），詳見 §2.7a；**1a／1b 仍不放行，條件式授權仍暫停**。授權有界修正（active effort 上限 1.0 hr，等待不計）；完成後仍需 reviewer 複核才能啟動 1b。
- **rev18 範圍更新**：reviewer 裁定 #56 的執行歸屬修正通過（mailroom #57）——**缺陷 A／B 修正通過，1a 功能審查放行；1b 條件式授權恢復（active effort 上限 2.5 hr，等待不計，六項授權內容見 §2.7a）**；**這不是 B3a-1 全案驗收完成，aggregate 繼續開啟**；**仍無提交／推送／PR／合併授權，1b 無新 production 修改授權（StartHidden 屬既有已核准功能）**；完成後交 reviewer 做全案驗收，不自行結案。
- **rev19 範圍更新（mailroom #65 最終裁定）**：六項授權內容全數執行完成——快照 `b3a1a-snapshot-20260917T055531Z`（HEAD `023481440017fea4e596f039258e35259f34ca8f`，49 檔雜湊相符）上，三個正式 run-id 各一次、**首次即通過**：A 預設 smoke `20260917T061013Z-2ddd9d`（rc=0）、B controls `20260917T062716Z-b25f60`（rc=0）、C offline `20260917T063037Z-2cca8a`（rc=0），皆 `cleanupClean=true`／`overallFailed=false`／`artifactViolations=0`／watchdog `stopSignalSent=0`；外部側錄 `frontend/e2e/.artifacts/1b-abc2-20260917T060356Z/` MANIFEST 53/53 自驗相符。reviewer 獨立複核 HEAD、49 檔、53/53、三份原始 stdout 判定欄位、control A／B 原始磁碟證據、C 的 sandbox／wrapper 啟動與零 browser 違規、watchdog 短測。**批次先有一次 watchdog PATH 導致的啟動失敗（`spawn wails ENOENT`，外部監看工具環境缺陷），照實保留不得刪除或概括為零失敗**；排除後三次正式驗證均通過。**reviewer 最終裁定 B3a-1 技術驗收通過，aggregate 標為技術驗收完成**；**未提交、未推送、未開 PR、未合併**；**B3a-2／B3a-CI 及其他票不因此通過，也未獲施工授權**。詳見設計文件頂部狀態、修訂紀錄 rev19、backlog rev63。

**舊快照（`20260916T073313Z`）的執行記錄（rev12 回顧，僅供歷史對照，不是新版 1b 的替代）**：

- **狀態更正（rev12）**：原「四項無剩餘」的敘述**已更正**——四項啟動條件（見下）皆已於 mailroom #41 之前確認達成、1b 已實際啟動並交付三項成果（三次正式 smoke、controls、trace 實際檢視），**但 sandbox 外網阻斷實測的完整 suite 驗收仍未完成**（阻擋於 S1，見 §2.7），**1b 整體判定未通過**。
- **啟動條件（reviewer 裁定，四項須同時滿足，回顧記錄）**：(1) N13 補跑通過（見 §2.3「N13 的完成條件」）——**已通過**；(2) rev11 裁定與狀態寫入設計／progress／backlog 文件——**已完成**；(3) 完成 `psUtil.ts` 的 L1 註解更正（只修註解，不動實作）——**已完成**；(4) 封存新的整合 manifest——**已完成**。四項全部滿足，1b 當時已啟動施工。
- **沿用既有負控制證據的要求**：1b 施工前須**逐項列出**所有沿用的既有負控制證據來源（版本／run-id）；**受本輪（L1／註解更正／N13 補跑）改動影響的負控制，以本輪重跑結果為準，不得沿用舊證據**；**controls（§2.5 對照 A／B）不得省略**。
- **功能檔一致性**：1b 施工使用的功能檔須與已審快照一致——**允許純註解差異，但需列出 diff**；**任何功能修改（非純註解）一律退回 1a 複核，不得混進 1b**。
- **範圍與實際結果**：在舊固定快照上——
  - `npm run test:e2e` **固定快照三次正式 smoke**（先前 smoke 不算）——**已完成、通過**：`EXIT_CODE=0`、PASSED、觀測／網路／產物違規皆 0（reviewer 獨立核對）。
  - 完整 `sandbox-exec` 外網阻斷實測——**未完成、阻擋於 S1**（見 §2.7）：簡單案例（loopback／外部）機制本身有效，但完整 suite 因 `ps` 在 sandbox 內無法執行而未能全綠；**禁止把「取樣未觀察到外連」寫成「全程無外連」**。
  - `expect.poll` 失敗 trace 的**實際開啟檢視**與診斷內容核對——**已完成、診斷能力被接受**（見 §2.7「trace 數字更正」，數字已依 reviewer 核對更正；此份 trace 可沿用於新版 1b，見 §2.7a）。
  - controls（§2.5 對照 A／B）——**已完成、通過**：rc 0 且 clean；reverse-check 沿用舊證據（缺 `EXIT_CODE` 檔），此限制**保持原樣、不回填**。
  - 整合證據封存（依 §2.9 R5 封存順序）——已完成。
- **禁止**：不得改主機全域網路或防火牆設定；不得碰真 provider 或 production 功能（新版 1b 沿用）。
- **review 方式（rev11 更正）**：**不再委派獨立 review，由 reviewer 負責最終 review**——取代先前各輪「獨立 review（禁止再委派）」的敘述（§3）。
- **失敗處理**：失敗保留並分類；**若判定屬實作缺陷，立即停下並退回 1a 複核，不得以重跑吸收**。
- **完成條件**：**1b 完成不等於可自行關閉 B3a-1a aggregate**——仍須 reviewer 依整體證據裁定 1a 是否關閉；**mailroom #41 裁定舊版 1b 整體未通過、aggregate 不關票**；**mailroom #45 進一步授權 offline 模式實作，新版 1b 待 1a 交付新快照並經 reviewer 複核後才啟動**。

### 估點（**#56 執行歸屬修正通過、1a 功能審查放行、恢復 1b 驗證授權（rev18，codex-reviewer mailroom #57，2026-09-16）**）

- **B3a-1a-browser**：6–9 hr，中位 **7.5 hr／0.75 pt**（不變）。
- **B3a-1a-lifecycle-1**（**維持 aggregate，拆法採 (c)，三張子票已核定**）：startup **9.55 hr**、stop **19.50 hr**（19.2 ＋ N13 補跑本身 0.3）、locale（L1）**3.60 hr**（3.4 ＋ 註解更正本身 0.2），**合計 32.65 hr**（不變）。
- **B3a-1a-lifecycle-2**（失敗判定與觀測完整性）：**核定 20.00 hr（維持不變，不再加塞）**——**已達 20 hr 拆票上限**；後續與 S1 offline 模式相關的實作與驗證工作**另開「S1 offline 模式實作與驗證」新票**（見上方 B3a-1a 小節），**不得塞回本票**。
- **S1 spike（已完成）**：**0.60 hr**（讀 reviewer 意見 0.1 ＋ S1 診斷本體 0.5，不變）——**已計入 1a 合計，不與下方的 S1 offline 模式實作新票混算**。
- **S1 offline 模式實作與驗證（新票，rev13 新增，rev18 更新）**：**S1 實作已知 6.615 hr**（6.50 已計入 rev17 ＋ 本輪歸屬修正實測 0.115 hr），計入 1a 合計與下方已知量；狀態「**缺陷 A／B 修正通過（mailroom #56 執行歸屬修正），1a 功能審查放行；1b 驗證授權恢復（active effort 上限 2.5 hr，等待不計）；仍非全案驗收完成，aggregate 繼續開啟**」。整票仍**估點待估**（總工作量尚未核定）。
- **1a 合計**：7.5（browser）＋ 32.65（lifecycle-1 三子票）＋ 20.00（lifecycle-2）＋ 0.60（S1 spike）＋ 6.615（S1 offline 模式實作，含本輪歸屬修正 0.115 hr）＝ **67.365 hr**。
- **B3a-1b 已知量**：**2.05 hr（不變）**（1b 已執行的三次 smoke、controls、trace 檢視主動操作工時；原 rev12「第十一輪新增 1.6 hr 待分攤」已於 rev13 依實際項目歸戶到 lifecycle-1 的 stop／locale 與 lifecycle-2，**不再列在 1b**）；**不含**新版 1b 尚待執行的 offline 模式端到端與阻斷對照案例——**條件式授權恢復，額外主動工作上限 2.5 hr（等待不計，六項授權內容見 §2.7a）；本次 2.5 hr 為新增驗證上限，未發生前不得入帳**。
- **B3a-1 已知量核帳（reviewer 提供，Decimal ROUND_HALF_UP 驗算相符）**：**67.365（1a）＋ 2.05（1b）＝ 69.415 hr／6.94 pt**。對應 B 軌 **209.465 hr／20.95 pt**、全部合計 **314.265 hr／31.43 pt**——**backlog 小計本輪據此重算**。
- **上一輪未核實回溯估算（獨立欄位，不併入上列已核定數字）**：上一輪（歸屬修正前的整理階段）估計耗時約 55–70 分鐘，**未核實**；controller 實測其中兩次 harness 執行的起訖時間，合計約 47 秒——**這只證明那兩次執行本身的時間範圍，不能倒推整輪其餘時間全為 active**；此欄位維持獨立列示，**不抹除、不任選落點、不混入上列已核定的 hr 基底**。
- **重要限制（必明列，不得省略）**：
  - **保留未四捨五入的原始秒數計算（413 秒＝約 0.114722 hr，四捨五入為 0.115 hr、顯示可再四捨五入為 0.11 hr）**；報表兩位小數由同一公式產生，避免加總漂移；**active 約等於 wall clock 是估計，未逐秒量測 active**。
  - **不把本次 2.5 hr 新增驗證上限當估點**——這是「1b 這輪最多能投入多少主動工程工作」的授權邊界，不是對「這件事總共要花多少工時」的估計；**未發生前不得入帳**。
  - **不把已知量 69.415 hr 當完整 forecast**——這只是目前已核定項目的加總，新版 1b 完成後還會再增加。
  - **不得以這次核點宣稱 B3a-1 全案驗收完成**——1a 功能審查已放行、1b 驗證授權已恢復，但**這不是 B3a-1 全案驗收完成，aggregate 繼續開啟**；**仍無提交／推送／PR／合併授權**；帳表更正只是把已知量核實，不代表驗收結果。
  - 子票分攤仍按 20 hr 規則；**不得為了湊上限移動與範圍不符的工時**；**lifecycle-2 維持 20 hr，不再加塞**。
  - 仍保留 B3a-2b（session recovery／approval，待 spike 後再估）與後續正式 CI 整合未估的註記。
  - **下次超上限（2.5 hr）前須先報告**。
- **14 hr／1.4 pt 留作原始基準（rev3 的裁定值），不得再當成目前的 forecast**——它只用於對照「範圍與理解在複核過程中如何演變」，不代表目前的工作量預期。
- **不得為了讓實際工作量符合任何舊估點而縮減 §2、§3 所列的任一驗收項目**（rev4–rev18 十五輪審查都明文要求）。

### 歷史記錄（rev3／rev4 估點沿革，僅供追溯，不再是現行值）

| 項目 | 估計 hr（rev3） |
|---|---|
| 入口、設定、fixture、假 CLI、`wails dev` 啟動 | 2.5 |
| 啟動前預檢與啟動後核對（§2.2） | 1.5 |
| 生命週期與異常收尾契約，含殘留處理（§2.3） | 2.5 |
| smoke flow 與完整內容輪詢（§2.4） | 1.0 |
| 存檔判定的受控對照（§2.5） | 1.0 |
| 網路取樣、執行期離線設定、一次阻斷驗證（§2.7） | 1.5 |
| `data-test` 補強（§2.8） | 0.5 |
| 證據目錄統一與 README 使用說明（§2.9） | 1.0 |
| 驗證（連跑、對照、N1–N9）與獨立 review 往返 | 2.5 |
| **rev3 合計（已作廢，僅供歷史對照）** | **14.0 hr（1.4 pt）** |

- rev2 補正的四項實際增加約 5.5 hr，**1.0 pt 不再成立**；rev3 裁定 14.0 hr／1.4 pt。
- rev4 追加：F1 的 browser 層判定、R2–R4、N9a／N9b／N11c／N12／N13 都是節點 2 施工後才發現的必要工作，reviewer 要求完成後重算，rev4 當輪未改數字。
- **rev5：節點 3 施工中發現的實際工作量（重建約 14–17 hr 已花＋約 4.5–6 hr 剩餘 → 約 18.5–23 hr）已明顯超出 14.0 hr／1.4 pt 與 >2.0 pt 拆票門檻，裁定拆為 B3a-1a／B3a-1b（見上），14.0 hr／1.4 pt 正式改列歷史值。**
- **rev6：B3a-1a 複核未通過（mailroom #14）**，事後估計上修為 1a 已投入 18–24 hr（非量測值）、1b 剩餘 4–8 hr（含等候時間，未拆分）；依 R1–R5 修正後，1a 依交付邊界再拆為 B3a-1a-browser／B3a-1a-lifecycle；**所有點數待修訂後 breakdown 才核定，14.0 hr／1.4 pt 僅為原始基準、不是目前 forecast。**
- **rev7：B3a-1a 第二次複核（mailroom #31）**——R3 指定反證／R4／快照封存順序通過，R1／R2 仍有缺口，1a 整體未通過；1a 子票由兩張改為三張（browser／lifecycle-1／lifecycle-2），**工時基準已核定**：1a 合計中位 34 hr／3.40 pt、1b 操作與分析中位 4 hr／0.40 pt（純等待另列），**合計已知基準中位 38 hr／3.80 pt**——**不含本輪 A–C 剩餘修正，不代表完整 forecast，不得以此宣稱 1a 通過**。
- **rev8：owner 裁定＋codex-reviewer mailroom #32／#33 授權** StartHidden 最小驗證（§2.2a，限定擴充，歸屬 lifecycle-1）——lifecycle-1 中位由 13.5 → **14.25 hr**（+0.75 hr／+0.075 pt，兩位顯示 0.08 pt，屬新增剩餘工作、不回寫為已投入工時）；**1a＋1b 已知中位基準更新為 38.75 hr／3.88 pt**——**仍不含本輪 A–C 剩餘修正，仍不代表完整 forecast，不得以此宣稱 1a 通過**。owner 更正：`StartHidden` 只隱藏視窗、不取消建立視窗，macOS 端仍無條件呼叫 `activateIgnoringOtherApps:YES`，**不得宣稱已解決搶焦點**。
- **rev9：B3a-1a 第三次複核（mailroom #35）**——B（R2 迴歸）、C（R1 一致性）、StartHidden、型別（R4）、封存順序與缺口補件**皆通過**；**A（送信號一致性的核心契約）仍未完成**，新增 A1–A5（§2.3）；新增兩項風險裁定（觀測逾時待分析、wailsjs 隔離驗證授權）；證據更正（hidden 模式「全程 0 個視窗」超出單點查詢範圍）。**lifecycle-1 中位達 23.75 hr（14.25＋A–C 那輪中位 6.9＋本輪新增 2.6），超過 20 hr 拆票線，改為 aggregate**，依交付邊界再拆 **lifecycle-1a（startup／隔離與持久狀態，含 StartHidden）**與 **lifecycle-1b（stop／身分驗證與 stale recovery）**，兩票分攤額待定（估點標「待估」，不得虛構完工，各票高端 ≤20 hr）。lifecycle-2 中位改 **14.35 hr**（13＋A–C 那輪中位 0.85＋本輪新增 0.5）。**已知總基準更新為 49.60 hr／4.96 pt**（1a 合計 45.60 ＋ 1b 4）——**仍不含本輪待估的 A1–A5 修正與兩項風險裁定，仍不代表完整 forecast，不得以此宣稱 1a 通過，1b 仍未授權**。
- **rev10：B3a-1a 第四次複核（mailroom #37）**——**A1–A5 原反例修正接受**；發現新的阻擋缺陷 **L1**（`ps` 輸出解析依賴 locale，非英文語系下整條停止契約 fail-open，§2.3），**1a 仍不通過，1b 仍未授權**。授權 L1 最小修正（只在 `ps` 子程序固定 `LC_ALL=C`、不可解析或空 snapshot 拋 `ToolObservationError`）與 focused selftest；核准新增 selftest npm scripts（§3，不取代 controls、不進預設 smoke）；有條件核准 `-skipbindings`（§2.9，須隔離副本完整比對內容相同才可採用、有差異交 reviewer 裁定）；收斂逾時診斷措辭（剩餘 315 ms 與新 cutoff 300 ms 不重疊，不得宣稱已完全消除逾時）。**lifecycle-1 aggregate 中位改 29.05 hr（23.75＋本輪新增 5.3）**，lifecycle-2 中位改 **15.65 hr**（14.35＋本輪新增 1.3）；**已知總基準更新為 56.20 hr／5.62 pt**（1a 合計 52.20 ＋ 1b 4）——**本輪新增 6.6 hr 明列為回溯估算、非計時實績；仍不含 L1 修正與剩餘工作，不是最終完工預測；lifecycle-1a／1b 分攤仍待估，未分攤不得當成已核准子票**。
- **rev11：B3a-1a 第五次複核（mailroom #39）**——**接受 L1 修正、selftest 指令、`-skipbindings` 採用；程式面無新阻擋項，1a 只差一項 N13 證據；1b 已條件式授權**（四項條件：N13 通過、裁定寫入文件、完成 `psUtil.ts` 註解更正、封存新 manifest）。**selftest 數量更正**：`selftest:all` 實際為 **32 項**（artifact 4／network parser 14／stopProcedure 14），非先前誤寫的 14 項。**N13 完成條件**：不得列為「成功升級 KILL 並清乾淨」，補跑須同時滿足 TERM 等滿、對已驗證目標升級 KILL、各階段耗時符合上限、pid 與埠清空且 `clean=true`，原 428 ms 失敗證據保留不得覆蓋。`psUtil.ts` 的 L1 註解「呼叫當下才疊加」不精確（`PS_ENV` 實為模組載入時建立），**只修註解、實作不動**。語系結論收斂為「這次查詢為英文格式」，不得保證所有歷史 run 相同。**lifecycle-1 aggregate 拆法改採 (c)，三張子票**：startup（暫列 9.55 hr）、stop（暫列 19.5 hr）、新增「ps 觀測語系與解析失敗處理（L1）」（暫列 3.7 hr）；**須寫明矛盾**——原 stop 範圍已含 L1，與新增獨立 L1 子票 3.7 hr 衝突，須移出或扣除重複量，**在施工 agent 回報前，三張子票估點一律標「待核定（去重處理中）」**，暫列值與 32.75 hr 合計不是核定值。**工時：改列待核定**——reviewer 確認 +6.1 hr 與 62.30／202.35／307.15 hr 算術正確，但「重跑＋等待」1.3 hr（前輪 1.4 hr）尚未確認符合「純等待不計工程量」，**這些數字先列為待核定試算，backlog 小計本輪維持 rev10（rev54）數字不動**；lifecycle-2 暫列改為 **18.05 hr**（待核定試算），各執行子票剩餘區間上限統一改為 **≤ 20 hr**。1b 範圍更新：固定快照三次正式 smoke、完整 sandbox-exec 阻斷實測、trace 實際開啟檢視、整合證據封存；**不再委派獨立 review，由 reviewer 負責最終 review**；屬實作缺陷即停回 1a、不得以重跑吸收；**1b 完成不等於可自行關閉 aggregate**。
- **rev12**（2026-09-16）：依 codex-reviewer **對 1b 的複核結果**（mailroom #41）修訂——**三次正式 smoke、controls、trace 診斷能力接受；1b 整體未通過，aggregate 不關票**。裁定採 **(c)**。reviewer 獨立核對：tracked diff SHA 與 `073313Z` 相符、32 個 untracked hash 全相符；對 `071215Z` 的差異僅設計文件與 selftest 註解；N13 `072802Z` 的 TERM 10001 ms／KILL 580 ms／埠 609 ms／`clean=true` 確認，**1b 啟動前置條件成立**；三次 smoke `EXIT_CODE=0`、PASSED、觀測／網路／產物違規皆 0；controls `085255Z` rc 0 且 clean。reverse-check 沿用來源與缺 `EXIT_CODE` 的限制**保持原樣、不回填**。
  - **阻擋項 S1（§2.7）**：兩份完整 sandbox log **不只 Vite timeout**——attempt1 有 76 處 EPERM、attempt2 有 77 處，含 spawn 後身分補強與程序樹追蹤失敗。reviewer 最小重現：`sandbox-exec -f loopback-only.sb /bin/ps -o pid= -p <自身pid>` → rc 71、`execvp Operation not permitted`；**即使 profile 只有 `(version 1)(allow default)` 亦同**。因此存在「**`ps` 在 sandbox 內無法執行**」這個獨立障礙。系統 `/bin/ps` mode 為 4755，但**證據不得擴張為「底層機制已完全證明」**；reviewer 試過位元組相同、mode 755 的私有副本**直接被 SIGKILL**，**此路不採用**，且**不得改系統 ps、重簽系統檔或關閉系統防護**。
  - **必須更正的既有措辭**（文件中如有同義敘述一律更正，原文保留並附更正註記）：INDEX／文件曾寫「證明 sandbox 沒有擋住任何功能所需連線」——**超出證據**；曾寫「卡住的是與網路隔離無關的逾時值」——**超出證據**；`Vite ready in 284 ms` 只是 Vite 自報啟動時間，**未量測「npm 程序啟動 → Wails 收到 URL」的完整區間**，也**未排除 ps EPERM**；`-viteservertimeout 60` 那次只能證明**某次診斷啟動成功**，**不能宣稱完整 suite 必通**。
  - **授權的受控診斷（回 1a，主動工作上限 0.5 hr，純等待另列）**：先用小型受控程序、**不啟動完整 Wails suite**，評估最小方案「**觀測與清理由 sandbox 外的 harness 執行；受測 Wails／app 與 Chrome 子程序各在相同網路 sandbox 內**」。必須證明：外部 observer 可讀身分、追蹤自家後代、安全停止；**兩條受測子程序路徑都確實繼承阻斷**（loopback 通、外部失敗），**不得漏包 Chrome 或讓被測 app 留在 sandbox 外**；記錄命令、profile、PID／PGID 與結果。限制：只操作自建測試程序、不改主機設定／sudo／防火牆、不放寬 network 限制、不得把觀測錯誤忽略成 PASS、**先不修改正式 harness**；0.5 hr 內無可行證據即交代阻擋，**不得改用「已知限制」關票**。`-viteservertimeout 60` 可用於受控診斷，正式採用需與上述問題一起驗證，且**不得連帶放寬 TERM 10 s／KILL 5 s 或觀測失敗判定**。
  - **trace 數字更正**（診斷能力已被接受，不需重跑）：`test.trace` 共 **762 筆事件**；**13 個內層失敗 expect**（expect@277–398）＋**1 個外層 poll timeout**（expect@276），**非 21 次固定 500 ms**；間隔實際有 100／250／500／約 1000 ms；screencast-frame 事件與 JPEG **皆 47 個**（timestamp 577695.052–595779.201），**非 49 個**。並註明**畫格為離散取樣，不能證明整段每一時刻畫面未變**。
  - **快照責任**：若後續功能快照改變，**新版正式連跑責任重新成立**；既有三次保留為 `073313Z` 的**有效歷史驗收**，不自動充當新版結果。
  - **§4 帳表更正**：**1b 狀態**由「四項無剩餘」改為「**已執行四項，sandbox 成功驗收仍未完成，剩餘待估**」，**不得**把原 1b 的 4 hr 預估與已完成的 2.05 hr 相加成 6.05。**三張子票**：32.45 **不核定**——第九輪 5.3→5.0（扣 0.3）、第十輪 L1 3.7→3.4（扣 0.3），前一版 32.75 應扣 **0.6** ＝ **32.15**（扣減屬第九輪 stop 驗證工作）：**startup 9.55／stop 19.2／L1 3.4，合計 32.15**。**第十一輪 1.6 hr** 須依實際項目（N13、註解、證據與文件）分攤一次，不得漏算或重算；分攤數字由施工 agent 提供前，標「**待分攤**」。**核帳用已知量**：B3a-1 = 62.30 − 0.60 + 1.60 − 4 + 2.05 = **61.35 hr**；**B 軌 201.40 hr**；**全部 306.20 hr**。**不含本次授權的受控診斷與尚未完成的 sandbox 修正／重新驗收**，保留未估剩餘，**不稱最終 forecast**；子票分攤仍按 20 hr 規則，**不得為湊上限移動與範圍不符的工時**。
- **rev13**（2026-09-16）：依 codex-reviewer **接受 S1 架構方向並授權 offline 驗證模式實作**（mailroom #45）修訂——**授權在 1a 實作受控的 E2E offline 驗證模式（opt-in，新 §2.7a）；仍非 1a／1b 驗收通過，aggregate 未關。本輪主動工程工作上限 3 hr（上限非估點）**。S1 診斷證據範圍更正（成立：外部 observer 可讀身分、單 PID TERM 與埠釋放；不成立：`pgid=process.pid` 非實測（實際 pgid 37744≠pid 38502）、缺 `lstart`、未驗證完整程序樹契約；Chrome 例用未指定 channel 的內建 Chromium、`TypeError` 可能是 CORS，證據不足）；新增兩項前置小測試（外部 observer 實測身分、正式系統 Chrome 三組對照，不成立即停止）；`-viteservertimeout 60`（候選值）僅限 offline 模式併案核准，TERM／KILL／埠／觀測失敗判定／網路 guard／artifact 規則全部維持；1a 驗證順序（小測試→typecheck／selftest→新模式下 N7／N8／N10／N11／N13）；新快照 manifest 須含 tracked／untracked 逐檔 hash 與工具版本；新版 1b 範圍（新快照三次預設 smoke、controls、offline 端到端成功／阻斷對照，`084023Z` trace 可沿用不必重造失敗，`073313Z` 三次 smoke 不算新版三次）；停止以 mtime 作決定性證明。**§4 分攤核定（無未分攤池）**：startup **9.55**、stop **19.50**（19.2＋N13 補跑 0.3）、L1 **3.60**（3.4＋註解更正 0.2）、lifecycle-2 **20.00**（18.05＋第十一輪其餘 1.1＋本輪 trace 0.2／總表 0.5／回報 0.15，**已達 20 hr 上限**）、S1 spike **0.60**（已完成，讀意見 0.1＋診斷 0.5）；**1a 合計 60.75**；B3a-1b 已知量回歸 **2.05**（第十一輪 1.6 hr 已依項目歸戶到 lifecycle-1 與 lifecycle-2，不再留在 1b）；**B3a-1 已知量 62.80 hr／6.28 pt**；**B 軌 202.85 hr／20.29 pt**；**全部 307.65 hr／30.77 pt**（Decimal ROUND_HALF_UP 驗算相符）。**均不含未完成工作**：S1 offline 模式實作與驗證另開新票（估點待估）、新版 1b 剩餘未估；**3 hr 授權上限不是估點，62.80 hr 已知量不是完整 forecast**。
- **rev14**（2026-09-16）：依 codex-reviewer **複核 §2.7a offline 驗證模式實作**（mailroom #47）修訂——**架構、兩項前置對照、正常／N7／N8／N10／N13 成功路徑接受；1a 仍未通過**，另獨立重現兩個新缺口 **P1**（`offlineSandbox.ts:32–40` 對 profile 只查 `X_OK` 且吞錯、缺 `R_OK`／一般檔案檢查，`:65` 只查 `sandbox-exec` 存在未查可執行，系統 Chrome 僅在 wrapper 執行時才檢查，`validateOfflineSandboxPrereqs('chrome')` 對三種壞情況的 fs mock 全部通過）與 **P2**（不支援的 `E2E_BROWSER` 組合以 rc 1 結束但無新 artifact 目錄、無 harness.log，違反 §2.2「啟動前失敗也要留 log」）。修正契約：P1 要求 profile 為可讀一般檔案（不查 X_OK、不吞存取錯誤）、wrapper／sandbox-exec／Chrome 三者在任何啟動前皆須確認可執行、統一用已驗證絕對路徑（`processTree` 裸 spawn 的 `sandbox-exec` 須與預檢路徑一致）；P2 要求在 `run-e2e` 入口、載入 config 前就建立證據目錄與早期 log、沿用同一 run-id、明列涵蓋入口邊界、此階段無 trace 屬預期。另更正兩處表述（`finalCommand` 不等於已解決所有 A3 身分誤判、刪除未使用的 `wailsDevSandboxCommand`）與 N13 紀錄紀律（兩次結果分開保留、不得抹掉第一筆、不得反覆重跑湊成功、須補殘留 fixture 核對身分後清理的證據來源）。**額外授權最多 1.5 hr 主動工作**（P1／P2 修正、小測試、N11、交付），**1b 仍不得啟動**。reviewer 驗證：HEAD／diff SHA 與 `095245Z` 一致、MANIFEST 7/7、tracked 逐檔 10 筆相符、**untracked 逐檔更正為 35 筆（非先前誤稱的 37 筆）**；`typecheck:e2e` 獨立執行 rc 0，其餘既有檢查核對封存證據、未宣稱全部獨立重跑。**§4 帳表更新**：本輪 **2.75 hr** 列為 S1 實作票的回溯工程估算（非計時實績）；**1a 合計 63.50 hr**；**B3a-1 已知量 65.55 hr／6.56 pt**；**B 軌 205.60 hr／20.56 pt**；**全部 310.40 hr／31.04 pt**（Decimal ROUND_HALF_UP 驗算相符）。**1.5 hr 授權上限不是估點，不得寫成已投入**；新版 1b 剩餘估算區間 1.5–2.5 hr（純等待另列），**仍未授權在 `095245Z` 啟動**，不得與已完成的舊版 1b 2.05 hr 混為同一批驗收；P1／P2／N11 的後續實際量另記，**不塞回已滿 20 hr 的 lifecycle-2**。
- **rev15**（2026-09-16）：依 codex-reviewer **接受 P1／P2 修正與 N11a 的 root 存活回收**（mailroom #49）修訂——**1a 無新的實作阻擋**，給出「N11 應驗分支補齊即可直接開始新版 1b」的**條件式授權**（可直接開始，不需再請示）。reviewer 驗證：`102306Z` tracked diff SHA 相符、tracked 逐檔 10 筆、untracked 36 筆、MANIFEST 7/7 全部相符；實跑新增的 8 項 `offlineSandbox` selftest 與 `typecheck:e2e` 均 rc 0；P2 原反例亦實跑，rc 1、新增目錄僅 harness.log、含實際拒絕原因、無 trace、無 active-run 指標；其餘固定檢查核對封存證據、**未冒稱獨立重跑**。**接受項**：P1 預檢契約（profile `R_OK`／`isFile`；三種執行檔 `X_OK`／`isFile`；spawn 與預檢統一絕對路徑）、P2 早期 log、N11a 的 root 存活回收。**JS 入口與 TS 預檢的雙實作**：本輪不要求重構，但日後修改須維持一致，支援邊界限於 `test:e2e`／`test:e2e:controls` 共同入口，不宣稱直接 playwright 入口也有早期 log。**N11 分支化驗收（取代先前 1/3、3/3 寫法）**：N11a（root 存活回收已接受＋root 消失但後代存活的 stale 回收待補新證據）、N11b（root／非 root 後代身分不符，非零結束、不送信號、誘餌存活）、N11c（不可解析／結構無效，非零結束、不送信號、pointer 保留不清除）。**新版 1b 條件式授權**：N11 三分支全數滿足、功能檔仍為 `102306Z` 已審內容、無新未知殘留才可啟動，任一功能檔需修改即不適用；1b 範圍維持 #45（三次正式預設 smoke、controls A／B、offline 端到端成功與明確阻斷對照、清理與證據封存），額外主動工作上限 2.5 hr。**§4 帳表更新**：本輪 **1.4 hr** 回溯工程估算歸 S1 實作票（**S1 實作已知 4.15 hr**）；**1a 合計 64.90 hr**；**B3a-1 已知量 66.95 hr／6.70 pt**；**B 軌 207.00 hr／20.70 pt**；**全部 311.80 hr／31.18 pt**（Decimal ROUND_HALF_UP 驗算相符）。**未含 N11 補件與新版 1b 剩餘，不是最終 forecast，不與先前預估重複相加，不計純等待**；**兩階段上限（N11 補件 0.75 hr、1b 2.5 hr）不是估點**；**lifecycle-2 維持 20 hr，不再加塞**。
- **rev16**（2026-09-16）：依 codex-reviewer **接受 `runState.ts`／`staleRun.ts` 語法相容性修改與 N11 補件、發現新阻擋 I1**（mailroom #51）修訂——**1a／1b 仍不放行，先前（mailroom #49）的條件式授權暫不恢復**。reviewer 驗證：以保留的隔離副本直接 diff 確認 `runState.ts` 僅欄位宣告與 constructor 賦值、`staleRun.ts` 僅 type import；全量逐檔比對僅該兩功能檔與設計、backlog 有變；獨立重跑 `typecheck:e2e` rc 0。**I1（ready 前中斷，teardown 未等待停止程序）**：`global-teardown.ts` 的 `readRunEnv` catch 分支無條件 return，只要 state 存在即宣稱已完成，未檢查或等待 `runtime.processTree.stop` 的 single-flight promise；reviewer 以受控條件呼叫 `globalTeardown`：`stopCalls=0` 卻仍印已完成；`103519Z` log 缺 TERM／KILL 完成、pid／埠核對與最終 cleanup 紀錄；原 run 是否另有 Wails 訊號因素尚未證明，不得把原因全推給 sandbox 轉發。修正契約：確認 ready 前取消與 signal handler／globalTeardown 的結束順序、teardown 與 handler 等待同一套有界 single-flight 收尾、不得因 run-env 未寫出就跳過、保留 interrupted 終態、只有實際完成且 pid／埠／觀測結果符合才清 pointer、無 runtime 時走既有安全身分核對／恢復契約、不得只改 log 文字或拉長 timeout、不得新增第二套無界停止程序。驗證限定：小測試涵蓋三種情境、再做一次新 offline 模式 ready 前 SIGINT 真實測試並記錄信號實際送給哪個 PID／PGID、不得人工 KILL 截斷證據、N7／N8 收尾路徑需確認無回歸、手動緊急清理另記為非正常收尾不得算 PASS、上一輪 SIGINT 敘述須從實際命令釐清、找不到原命令即標「未知」。**三處更正**：controller 於 mailroom #50 自行推論寫入的「四者同 pgid 71315」錯誤（實際 app 73692 在 71315 群組，72455／72501／72584 為非同 pgid，實際採 group TERM 加個別 TERM）；N11b-1 原腳本未測到目標情境（reviewer 已自行補做正確反例，原反例改標「pointer／state 一致性測試」）；n11bc 腳本只印結果無斷言，不得把 rc 0 包裝成自動驗收全過。**後續條件**：完成後交新 diff、限定驗證與固定快照，仍需 reviewer 複核才能啟動 1b；N11 已接受證據不必為報表全部重跑，僅當 I1 實際改到 stale recovery 才重驗受影響分支。**§4 帳表更新**：0.75 hr 核定歸 S1（**S1 實作已知 4.90 hr**）；**1a 合計 65.65 hr**；**B3a-1 已知量 67.70 hr／6.77 pt**；**B 軌 207.75 hr／20.78 pt**；**全部 312.55 hr／31.26 pt**（Decimal ROUND_HALF_UP 驗算相符）。**尚不含 I1 與新版 1b 剩餘；1.5 hr 為上限、非已發生估點；不與先前預估重複相加；不計純等待**；**lifecycle-2 維持 20 hr，不再加塞**。
- **rev17**（2026-09-16）：依 codex-reviewer **複核 I1 修正**（mailroom #53）修訂——**I1 的「等待既有 stop promise」主路徑修正成立，但整份 diff 尚未通過；1a 不放行；1b 條件式授權仍不恢復**。reviewer 獨立核對 tracked 10／untracked 37 逐檔雜湊、aggregate diff `d6398387…`、MANIFEST 7/7，重跑新自測 6 項通過，讀過四次原始 harness 紀錄；N11 先前已接受的結論不撤回。以實際 `globalTeardown` 公開入口重現兩個新必修缺陷：**缺陷 A**（`global-teardown.ts` 約 358 行，state 遺失即誤判 app 從未啟動，現行自測「測項四」固化此缺陷需一併修正）與**缺陷 B**（約 403 行跨行程 fallback 缺一致性核對，`empty-processes-no-runtime` 案例中誤刪 pointer，違反既有「空清單／損毀資料保留指標」契約，約 70 行純檔案分支有相同模式）。重現材料存於 `frontend/e2e/.artifacts/n11-tests/codex-review52-repro/`（probe.mjs、result.json 各附 sha256，另含兩份隔離 artifacts；probe 攔截信號發送、未啟動 app，rc=0 不得解讀為產品 PASS）。**快照裁定（往後適用的證據規則）**：接受 `20260916T111536Z` 中已明列 `112144Z` 更正時間與原因的修正版 manifest 作為本次比對依據，但不得稱為 111536Z 原封不動封存；往後封存後的更正要另建 revision／addendum、保留原版，不得原地重寫已封存 manifest。**新授權**：有界修正，active effort 上限 1.0 hr（等待不計），不得新增 production 修改、不得開始 1b。**§4 帳表更新（reviewer 核定）**：本輪 I1 實際 **1.6 hr 全數照實列入 S1 implementation**（其中 **0.1 hr 明列為超出 1.5 hr 上限的超支，不得吸收或改寫成 1.5**）；**S1 實作已知 6.50 hr**；**1a 合計 67.25 hr**；**B3a-1 已知量 69.30 hr／6.93 pt**；**B 軌 209.35 hr／20.94 pt**；**全部 314.15 hr／31.42 pt**（Decimal ROUND_HALF_UP 驗算相符）。**尚不含缺陷 A／B 修正與新版 1b 剩餘；1.0 hr 為上限、非已發生估點；不與先前預估重複相加；不計純等待**；**下次超上限前須先報告**；**lifecycle-2 維持 20 hr，不再加塞**。
- **rev18**（2026-09-16）：依 codex-reviewer **裁定 #56 執行歸屬修正通過**（mailroom #57）修訂——**1a 功能審查放行，恢復 1b 驗證授權；非全案驗收完成，aggregate 繼續開啟；仍無提交／推送／PR／合併授權，1b 無新 production 修改授權（StartHidden 屬既有已核准功能）**。reviewer 獨立查證：HEAD `023481440017fea4e596f039258e35259f34ca8f`；快照 `b3a1a-snapshot-20260916T120427Z` 47 檔雜湊全符、MANIFEST 8/8；相對 `114515Z` 僅 `global-teardown.ts`／`globalTeardownStop.selftest.ts` 變動；接受部分重跑與 reuse-basis；自行重跑 selftest 14/14 rc=0、typecheck rc=0；自行在全新 `/tmp` 重跑五案探針（missing-state-with-pointer／pointer-to-other-run／occupied-port／root-conflict／bad-json）皆 rejected、pointer 保留、signalCalls=0；loopback listener 已關閉，`.active-run.json` 現不存在，新證據存於 `frontend/e2e/.artifacts/n11-tests/codex-review56-repro/`。**明列限制**：env 純檔案正向流程未單獨實測，接受為限制，**不得宣稱所有 fallback 組合均經真實 E2E 驗證**。**已完成措辭收斂**（純文字、未動控制流程）：no-pointer return 分支 log／註解／自測名稱由「確定 app 從未啟動」改為「未取得本次程序追蹤紀錄及殘留指標，本分支未執行停止；執行結果依 setup 失敗回報，不能據此證明從未啟動」；`canonicalizePath` 措辭由「不吞例外」更正為「catch realpath 例外後退回 `path.resolve`」；改完 typecheck rc=0、`selftest:all` 54 項 rc=0。**最終功能快照** `b3a1a-snapshot-20260916T121017Z`（1b 綁定此份），gofmt／typecheck／`selftest:all` 本輪重跑，go race／vitest／build 沿用 `120427Z`（位元組複本，附 `reuse-basis.txt` 逐檔雜湊）。**1b 授權（active effort 上限 2.5 hr，含文字整理；等待不計）六項**：(1) 三次正式預設 smoke 全過、舊 smoke 不計次；(2) controls A／B 必做，失敗原因須符合受控延遲；(3) 完整 offline E2E 成功及阻斷外網證據，既有對照可沿用但本輪仍需執行，保留每秒取樣邊界；(4) `expect.poll` 失敗 trace 可沿用但需指出實際可見錯誤內容，不得用 Node stderr 代替；(5) 每次核對 pid／埠／pointer，writer 全關才封存、manifest 自驗；(6) 1b 只做驗證不追加 production 修改，失敗留證不反覆重試，超 2.5 hr 先回報，完成後交 reviewer 全案驗收、不自行結案。**§4 帳表更新（reviewer 核定）**：本輪歸屬修正實測 11:55:13–12:02:06＝413 秒＝**約 0.115 hr**（顯示可四捨五入為 0.11 hr；active≈wall clock 為估計、未逐秒量測）；**S1 implementation = 6.615 hr**；**1a 合計 = 67.365 hr**；**B3a-1 已知 = 69.415 hr／6.94 pt**；**B 軌 = 209.465 hr／20.95 pt**；**全體 314.265 hr／31.43 pt**（Decimal ROUND_HALF_UP 驗算相符，**保留未四捨五入的原始秒數計算，報表數字由同一公式產生避免加總漂移**）。**上一輪未核實回溯估算（獨立欄位，不併入上列數字）**：上一輪估計約 55–70 分鐘、**未核實**，controller 實測其中兩次 harness 執行起訖合計約 47 秒，**只證明那兩次執行的時間範圍，不能倒推整輪其餘時間全為 active**，不抹除、不任選落點、不混入已列數字。**本次 2.5 hr 為新增驗證上限，未發生前不得入帳**；**下次超上限前須先報告**；**1a／1b 尚未全案驗收，aggregate 未關**。設計標頭本身更新為 **rev18**。
- **rev19**（2026-09-17）：依 codex-reviewer **mailroom #65 最終裁定**修訂——**B3a-1 技術驗收通過**，1a／1b 本次工程交付責任達成，**aggregate 標為技術驗收完成**；**未提交、未推送、未開 PR、未合併**；**B3a-2／B3a-CI 及其他票不因此通過，也未獲施工授權**。三個正式 run-id（A `20260917T061013Z-2ddd9d`、B `20260917T062716Z-b25f60`、C `20260917T063037Z-2cca8a`）各一次、首次即通過，`cleanupClean=true`、`overallFailed=false`、`artifactViolations=0`；受測快照 `b3a1a-snapshot-20260917T055531Z`（HEAD `023481440017fea4e596f039258e35259f34ca8f`）49 檔雜湊相符；外部側錄 MANIFEST 53/53 相符。**工時（已量測／估計／未量測分開）**：`06:10:11Z–06:44:16Z` 為 A/B/C 及間隔操作的涵蓋區間，**不得**重複加計間隔為額外 active，扣除重疊後不足部分**保持未知**；**本輪不重算 B3a-1 已知量／B 軌／全體合計**，rev18 核定的 **B3a-1 已知 69.415 hr／6.94 pt**、B 軌 209.465 hr／20.95 pt、全體 314.265 hr／31.43 pt **維持不動**；原 14 hr／1.4 pt 僅留作原始估算、不得冒充最終實績。**Attempt 0**（watchdog PATH 造成一次啟動失敗，`spawn wails ENOENT`，外部監看工具環境缺陷）**照實保留於歷史，不得刪除或概括為零失敗**；「首次失敗即停」規則不因此獲得「工具錯誤可自動重試」的一般性豁免。**階段觀察**（globalSetup 完成→Running 行差異 A≈12m24s／C≈11m15s／B≈4.5s）**僅為觀察，不推論死結、資源競爭或 Chrome 冷啟動**；C 的 browser launch 側錄在 Running 之後，不得倒推 Running 前空窗為已證明的啟動耗時，屬剩餘效能／診斷限制，**本票不繼續優化或追加測試**。**其餘限制**：A 目錄依預設刪除僅存外部側錄、C 每秒取樣不得擴張為封包層級全程證明、reverse-check 依既有 env gate `skipped`（本次未執行、不宣稱全綠）。**不得以本次核點宣稱總工作量已完整估算**——B3a-2b、後續正式 CI 整合仍未估；**此結案不含 commit／push／PR／merge，不影響 B3a-2／B3a-CI 授權狀態**。設計標頭本身更新為 **rev19**。

### B3a-2（原列 0.9 pt，涵蓋 Gate 1／Gate 2／STALE／session recovery／approval；**裁定：拆為 2a／2s／2b＋新增 B3a-CI**）

replay provider 移入後，未知工作集中在需要 provider 的流程，拆成三張：

| 票 | 內容 | 估點 |
|---|---|---|
| **B3a-2a** | Gate 1、Gate 2、STALE 三條流程，**不經 provider**（M3b §12 已用純 UI 走過） | **0.65**（6–7 hr，中位 6.5 hr） |
| **B3a-2s**（spike，timebox） | 評估既有 fake 或 replay 能否支援 session recovery 與 approval：claude 的 resume 參數、MCP approval 的 tool_use 流程、codex app-server 協定需要涵蓋到哪一層 | **0.45**（spike timebox 4–5 hr，中位 4.5 hr） |
| **B3a-2b** | session recovery、approval 兩條流程，以 replay provider 驅動 | **spike 後再估**，不計入已估小計 |

### B3a-CI 可行性小票（新增）

| 內容 | 估點 |
|---|---|
| 在 macOS runner 上確認 `wails dev` 能啟動、完成一條 smoke、完整收尾、上傳證據目錄。正式 CI 整合在實測後另估。本輪不授權推送實驗 workflow | **0.4**（3–5 hr，中位 4 hr；CI 等候時間另列，不算工時） |

B3a aggregate 的 CI 條件（驗收 (3)）保留，由 B3a-CI 與後續正式整合承接。

**本輪只施工 B3a-1**（rev5 起 B3a-1 為 aggregate；rev7 起 1a 再拆為 B3a-1a-browser／B3a-1a-lifecycle-1／B3a-1a-lifecycle-2 三張子票，皆為修正中，見上）；B3a-2a／B3a-2s／B3a-2b／B3a-CI 為拆票與估點寫入 backlog（rev48），尚未授權施工。

## 5. 待裁定（**已裁定（rev3）**）

- **Q1（已裁定）** B3a-1 改為 **1.4 pt**（14.0 hr），保留一次阻斷驗證，不移出降為 1.3 pt。
- **Q2（已裁定）** B3a-2 拆成 2a／2s／2b，並新增 B3a-CI，估點與內容照 §4 提案寫入 backlog（rev48），與 B3a-1 施工授權一併處理。
- **Q3（已裁定）** 對照 A／B 放在獨立指令 `npm run test:e2e:controls`（不列入預設套件），可以；仍是 B3a-1 驗收必跑項。

## 6. 風險（依可信度排序）

1. **CI runner 上能否開 native 視窗**：未實測，由 B3a-CI 回答。
2. **執行期離線設定與 wails dev 的相容性**：`GOFLAGS=-mod=readonly`、`GOPROXY=off` 等設定可能與 wails dev 的建置流程衝突，施工時驗證。
3. **程序群組假設**：app 與 vite 是否都在 `wails dev` 的 pgid 內，要在 ready 後核對；不在的部分改為個別追蹤（§2.3 已改為從 spawn 起即追蹤整棵後代樹），這會增加收尾邏輯。
4. **`sandbox-exec` 可行性**：已被標為 deprecated；**若不可行，回報 reviewer 裁定替代或限制，不改採關閉網路介面**（該後備方案已刪除）。
5. **斷言錯誤能否寫進 trace**：`expect.poll` 失敗時錯誤能否出現在 trace，施工時驗證；不能的話，harness log 仍會記錄。

## 文件雜湊（結案 revision 記錄，2026-09-17）

- 本次結案（rev19，見修訂紀錄與 §4）**不改寫受測快照**、**不因純文件變動重跑功能測試**。
- `docs/superpowers/plans/2026-09-14-b3a-1-browser-e2e-design.md`（rev19）sha256（計算範圍為**加入本節之前**的檔案內容，指令 `shasum -a 256`）：`a2d05e76177889bcfbd06686fda70223c927c37ca1b5525c4cd1f0d5b99d455e`
