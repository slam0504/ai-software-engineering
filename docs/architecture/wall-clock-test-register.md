# Wall-clock 測試有效名單（living）

> 版本：v12（2026-09-08，**來源索引收斂＋內容修復**：逐筆來源改引用重新取得的 `b2b-2/review-20260908T175707/`（`index.json`＋五筆 jobs JSON 與日誌，11/11 OK），列出**完整 HEAD**、**完整六路徑 tree hash**、**四 job 起訖**與**各 job `Set up job` 日誌原文的 image／runner 版本**（labels 不代替 image 版本）；規則 5 的測試耗時 n=1 限定為 `#2`／`#3`／`#6`，**F1／F2 各 n=2**；樣本 1–3 原採集檔改述為「目前無法調閱，清除時點與原因未知；部分資料已由 GitHub 重新取得」。**修復**：前一次提交 `b9c249a` 因替換錨點誤命中版本行，刪去 A 段、A-1 段與前言，本版以 `c6b776c` 為基礎僅替換 A-1 段內的索引區塊還原）；前版：版本：v11（2026-09-08，**A-1 補正**：樣本 4 的 `go` **job 耗時 95 s（failure，提前中止）**有紀錄、缺的只是測試耗時，A1a 組 job 統計改為 95／185.5／276 s（n=2，含提前中止）、#2／#3／#6 維持 n=1；規則 5 回填為「B2b-2 已完成 CI 分布量測、指向 A-1、冷啟動仍未驗證」；新增**逐筆來源索引**（六路徑指紋、head／base、四 job 起訖與 runner、artifact ID）並標明樣本證據 manifest 只涵蓋樣本 4／5）；前版：v10（2026-09-08，**B2b-2 五筆樣本回寫**：新增 A-1 段（CI 耗時量測），依 D6 分為舊組 n=3 與 A1a 組 n=2；樣本 4 的 `go` job 因 gofmt 檢查中止、未執行測試，go 相關耗時記「缺」不補零，故 A1a 組的 go／#2／#3／#6 有效資料僅 n=1；五筆皆 npm cache hit、不稱冷啟動；A 段「最後一次負載重驗」以本表回寫。**C1 的既有限制不變**）；前版：v9.1（2026-09-08，**C1 補記精度修正**：區分「2/4 那次未保存失敗原文（從未產生）」與「較早 mutation 日誌保存後遺失」，並補來源 session ID 與約略時點；前版 v9 同日，**C1 補記**：可供補齊失敗原文的原始輸出已隨 `/tmp/a1a-1` 封存整個遺失、清除時點與原因未知；owner 查歷史 session 找到登記內容但無原始輸出；不以摘要代替原文、不授權重新重現，C1 維持候選與原文待補。A 段、B 段與 B-1 段其餘內容不變）；前版：v8（2026-09-07，**A1a-1 新增候選：CM6／jsdom 隔離執行**——`-t` 單條隔離模式下 CM6 於 jsdom 掛載時序敏感，具名條目 `T8b-S` 於現行 HEAD `2bd4890` 以 2/4 重現；分類為**候選**，不加入可重跑名單、不併入 F1／F2 的規則 7；全檔／全套批次只記「這些批次未重現」。詳見 B-1 段。前版：版本：v7（2026-09-07，B2c-7 CI 驗證通過後 #7 → **resolved**：exact implementation base `852c287`（B2c-5 `b2efb1c`／`b9c74e8`、B2c-6 `1b5e54c`／`dad85cf`）、驗證分支 head `a679fcd`、run `34039387868`，`macos-15-intel`×2＋`ubuntu-latest`×2 上 `TestOrphanDoesNotHangNormalExit`／`TestSupervisorCleanupTwoLayerForkOrphan`／`TestCtxCancelKillsWholeGroup`／`TestTerminateEscalatesToGroupKill` 各 400/400、零 invalid／setup／race／timeout、artifact manifest 全 OK；規則 8 的兩種形狀轉為歷史；B2a 解除 blocked（rebase 後仍須重做 Gate A）；前版 v6 2026-09-07，B2c-3 結果回寫 #7——macOS 機制解讀定位為「KILL 送達時正處於建立中的成員被 XNU `killpg1` 靜默略過」的 fork 窗口（本機時間線＋原始碼一致性，送達瞬間未直接觀察）、CI production 順序 36–54% 重現、群組約 30 s 後消失已直接觀察、經身分驗證的第二次群組 KILL 於探針條件下 109／109 清除；#7 維持未解決；**v6 同版本補記（2026-09-07，B2c-4）**：責任邊界已裁定為 production 契約缺口＋測試 oracle 取樣問題，修法為 supervisor 有界清理（B2c-5）與 oracle 有界化（B2c-6），待 B2c-7 CI 驗證後才轉 resolved；EPERM 語意勘誤；前版 v5 2026-09-06，B2c-2 診斷 round 2 結果回寫 #7——macOS 於 production cleanup path＋真實 fixture 時序下確認重現、`claude.Session` 非必要、責任邊界待裁定；ubuntu 為 oracle timing race 支持性證據；#7 維持未解決；前版 v4 2026-09-06 B2c round 1、v3 2026-09-05 B2a 登記 #7、v2 B1b、v1 B1a-4）
> 性質：**living 文件**——「目前有效名單」與規則以本文件為準。`docs/spikes/m3b-results.md` §7 保留為 2026-08-21 的歷史觀察與具名來源，§7.1 為 B1a 收尾時的處置結果快照；兩者原文不再更新。
> 更新責任：B1b 已於 v2 更新前端兩條候選；B2a 於 v3 登記 #7 候選；**B2c** round 1 已於 v4 回寫 #7（結論：自製 proc 探針路徑未重現、真實路徑機制未定位）；**B2c-2** round 2 已於 v5 回寫 #7；**B2c-3** 已於 v6 回寫 #7 的機制欄（見 A 段與 `orphan-timeout-diagnosis-record.md` §7）；**B2c-4** 已於 v6 同版本補記裁定責任邊界與修法（裁定記錄 `docs/superpowers/plans/2026-09-07-b2c-4-supervisor-cleanup-contract-decision.md`）；#7 的實作由 B2c-5／B2c-6 落地（main `61c2201`／`852c287`），**B2c-7** 已於 v7 依完整條件回寫 #7 → resolved（run `34039387868`）；B2b 於 CI ruleset 啟用後回寫 #2／#3／#6 的 CI 量測（版本＝當時最新＋1）。任何新候選的登記與除名都在本文件的修訂記錄留痕。

---

## A. Go 測試（#1–#7 皆已處置；#7 於 v7 resolved——B2c／B2c-2／B2c-3 診斷、B2c-4 裁定、B2c-5／B2c-6 實作、B2c-7 CI 驗證）

| # | 測試 | 套件 | 狀態 | commit | 修正方式 | 最後一次負載重驗 |
|---|---|---|---|---|---|---|
| 1 | `TestAppServerTerminateKillsGroup` | `internal/codex` | **resolved**（B1a-1，2026-09-03） | `82caf8b`、`f7ad1ed` | `Proc` 未匯出 timer／signal-event seam＋三條白箱測試；codex 端改驗 supervisor 收尾 | 2026-09-04 B1a-4 矩陣，27 次有效指定執行全 PASS |
| 2 | `TestClaudeAssistFailsLoudOnOversizedLine` | `internal/assist` | **resolved**（B1a-2，2026-09-04） | `7b1bb0c` | 移除 fixture `tr` 轉換、保留 15 秒 context | 同上，27/27；三份併發下最長 8.02s（見 C.4） |
| 3 | `TestMultiTurnSendAndTurnBoundaries` | `internal/claude` | **resolved**（B1a-2，2026-09-04） | `05069e2` | `waitResult` 局部 deadline 5s→15s（卡死保險絲） | 同上，27/27 |
| 4 | `TestInFlightTurnDoesNotBlockNewSession` | root | **resolved**（B1a-2，2026-09-04） | `b0a8404` | `afterFn` 接上 `newFakeAfter()`，quiesce 逾時不由真實時鐘決定 | 同上，27/27 |
| 5 | `TestAppServerMidStreamDeath` | `internal/codex` | **no-change disposition**（B1a-3，2026-09-04） | 無（不存在 implementation／resolved commit） | preflight 未證實存在可修的牆鐘缺陷（非宣稱它完全不含牆鐘） | 同上，27/27 |
| 6 | `TestOutputCancellationKillsGrandchildren` | `internal/proc` | **resolved**（B1a-3，2026-09-04） | `39aa732` | 第二段輪詢 `deadline` 重設＋fixture 父 shell 寫 `$!`＋`pid != pgid` oracle 斷言 | 同上，27/27；三份併發下最長 9.3s（見 C.4） |
| 7 | `TestOrphanDoesNotHangNormalExit` | `internal/claude` | **resolved**（B2c-7，2026-09-07）——修法 B2c-5（production supervisor 有界清理）＋B2c-6（測試 oracle 有界化）落地 main `852c287` 後，CI 驗證 run `34039387868` 於兩平台四 runner 上四條測試各 400/400（見「最後一次負載重驗」欄）。**歷史（v6，B2c-4 裁定後 unresolved 的原因）**：機制解讀已定位、責任邊界已裁定為 production 契約缺口＋測試 oracle 取樣問題，修法尚未實作、CI 驗證尚未完成——CI-only。**macOS 機制（B2c-3，v6 新增）**：CI production 順序、observe-only ×400 下 36–54% 輪次於 `SignalGroup(SIGKILL)` 成功返回後仍有 live member（以 `bash`＋`sleep` pair 為主，另有 2 輪單獨 `sleep`），198 個存活者中 158 個首次於 KILL 返回後才被觀察到（8 個 `observedBeforeReturn`、32 個 `straddledOrUnknown` 分開列示、未定位）；群組於約 30 s 後（`sleep 30` 自然結束）消失，**已直接觀察**；**機制解讀（本機時間線＋XNU 原始碼一致性，送達瞬間未直接觀察）**：fixture 第 16 行 `[ -n … ] && bash -c '…' &` 使子 shell 在 leader 退出後約 1 ms 才 fork orphan，XNU `killpg1` 對建立中（`P_REF_NEW`／`SIDL`）的成員 `proc_find` 失敗而靜默略過且不延後補送（對照 tag `xnu-11417.140.69` commit `43a90889…`，僅前綴相符的最接近公開版本；runner 與本機 kernel build 字串相同）；可控延遲層在 CI 重現 d＝0–1 ms 窗口曲線，**經身分驗證的第二次群組 KILL 於探針條件下 109／109 清除**；兩平台 `kill(-pgid, 0)` 語意不同（macOS 在走訪時沒有可取得 ref 的成員即回 EPERM——zombie-only／exit transition／**建立中的 `P_REF_NEW`** 皆然，EPERM 不能當作群組已消失；Linux 回 0）；Linux v6.17 `copy_process` 有中止 fork／補送機制，ubuntu 0／400。**B2c-2 結果（v5）**：於 production proc supervisor cleanup path（`SignalGroup(SIGKILL)` 成功返回後）＋真實 fixture `fake-claude.sh`（`FAKE_ORPHAN=1`）時序下確認重現：同一 PGID 仍有 live member（`bash -c trap "" TERM; sleep 30`、`sleep 30`，S／R，無 Z），EOF 延後約 30 秒——與其持有繼承的 stdout pipe 一致（最合理解釋；無 FD 取證）；`claude.Session` 非必要（proc 直呼路徑 27／26 per 100 重現，full-path 27／29 per 100 命中 5 秒 checkpoint，卡在 EOF 而非 `Wait`）；SIGKILL 成功後成員為何存活未定位，**責任邊界待裁定**（fixture 為觸發條件，但不能排除 production cleanup contract／實作缺口）。**ubuntu**：既有 oracle `kill(-pgid,0)` 於 `Wait` 返回後 <1ms 內仍回 0（97／100 ×2，+1ms 起全 ESRCH），為 oracle timing race 的支持性證據；殘存者狀態（zombie 與否）未觀察。歷史後續（v5 當時）：backlog B2c-3、B2c-4；**現行後續（v6 補記）：backlog B2c-5（proc supervisor 有界清理）、B2c-6（測試 oracle 有界化）、B2c-7（`b2c7/verify` CI 驗證與本欄 → resolved）**；**v7：B2c-7 完成，本欄已 resolved** | `b2efb1c`、`b9c74e8`（B2c-5，main `61c2201`）；`1b5e54c`、`dad85cf`（B2c-6，main `852c287`） | **已落地（B2c-5／B2c-6，2026-09-07；依 B2c-4 裁定）**：(a) production：supervisor 有界清理——第一次 cleanup KILL 返回後於絕對偏移 1–512 ms 探測 `kill(-pgid, 0)`，`nil` 即重送、`ESRCH` 完成、`EPERM`／其他 errno 不送但繼續，1 s 最終確認非 `ESRCH` 一律 `Exit.CleanupIncomplete` 並強制解除本端 stdout／stderr 等待（`Wait()`／`Done()`／`Events()` 有界收斂），`CleanupIncomplete` 傳到 `ports.Exit`／codex meta／assist，`Exit.Err` 不混入；(b) 測試側：四個 oracle 改為 `Wait()` 後有界輪詢，只有 `ESRCH` 算群組消失，EPERM 繼續輪詢，逾 2 s 失敗並附快照。不放寬 5 秒 guard、不加 retry 於被測路徑。裁定記錄：`docs/superpowers/plans/2026-09-07-b2c-4-supervisor-cleanup-contract-decision.md` rev3 | **CI 證據（B2a）**：PR #1 run `33953144191` attempt 1（07:39Z）與 attempt 2（08:00Z，owner 一次性診斷例外、只重跑 `go`）皆紅，HEAD `ffcd16140e13399451b69833fa106f0c7fa5980b`，runner `macos-15-intel`（image macos-15 `20260824.0482.1`），Go 1.26.5，指令 `go test -race ./... -count=1 -timeout 30m -json`，artifact `go-test-json` id 9965561717／9965826248（`go-test.rc`=1），逐字失敗 `session_test.go:207: drain/Wait hung on orphan-held pipes`、`--- FAIL: TestOrphanDoesNotHangNormalExit (5.01s)`；同套件其餘 34 條 0–0.06s。**跨 SHA 對照**：`109b407`（`internal/claude` 與 workflow 相關 bytes 相同）run `33952217785` 一次綠（0.04s）。**本機反證**（2026-09-05，8 核 x86_64）：focused `-race -count=30` 全過 2.02s；三份 `./internal/...` `-race` 併發下 focused `-count=20` 最慢 0.02s；B1a-4 各層 0.01–0.03s。**B2c round 1 CI 證據**（`b2c/diag` head `d12b13b`，run `33968040444`，2026-09-05 13:08Z，四 job）：layer 1 既有測試 ×100——macos-15-intel r1 36 紅／r2 45 紅（皆 `session_test.go:207: drain/Wait hung on orphan-held pipes`，5.00–5.02s；pass 皆 ≤0.04s）、ubuntu-latest r1 98 紅／r2 99 紅（皆 `session_test.go:210: orphan must be reaped by supervisor on parent exit`，0.00–0.01s）；layer 2 白箱探針（`proc.Start` 直呼＋暫時性 fixture）×100 四 job 0 重現、cleanup KILL 100/100、EOF 亞毫秒、`invalidEvidence` 0。artifact `layer1-test.json` SHA-256 前 16：`876f5612e09c9364`／`ae76e6e2ffb72236`／`133377869e418668`／`fd7ffb30e3a112e9`（macOS r1／r2、ubuntu r1／r2）。**B2c-2 round 2 CI 證據**（`b2c/diag` head `b4174da`，run `33982853795`，2026-09-05 18:03Z，四 job 全 success，`pending`／`invalidEvidence` 皆 0）：layer 2b（proc 直呼＋真實 fixture ×100）eofTimeout macOS r1 27／r2 26、ubuntu 0／0，cleanup KILL 100/100，每個 eofTimeout 輪的 `ps.start >= tCleanupKill` 取樣皆顯示正確 PGID 兩個 live member；layer 3（full-path ×100）checkpointHit macOS 27／29、ubuntu 0／0，`kill0@0` nil macOS 27／29、ubuntu 97／97，ubuntu +1ms 全 ESRCH，`tEventsClosedMs` 於命中輪 30018–30191、`tWaitReturnMs` 僅晚 ≤0.27ms；parity（自製 fixture ×20）四 job 20/20；artifact manifest `HASHES.v2.txt`（6196 條，`shasum -c` 全 OK）SHA-256 `a2cb72420829fdb73639e7954ef2c76f12bd804970229491e23a1050334b276c`。**B2c-3 CI 證據**（`b2c3/diag` head `5654534`，run `34018874080`，2026-09-06 07:20Z，四 job 全 success，8 份 layer 4 summary `pending`＝0、12 份 `invalidEvidenceCount`＝0）：layer 4 production 順序 ×100 存活輪 macOS r1 46（取樣器 on）／36（off）、r2 54／41，ubuntu 0／0 ×2；分類 after 158／before 8／straddled 32；存活輪群組消失 30.0–30.4 s；layer 5 可控延遲 7×20：macOS r1 53／r2 56 輪存活、`unverified` 0、`after2Live` 0，ubuntu 第一次 KILL 實際送出 199 輪 0 存活（另 38／43 輪 `unverified` 跳過，非逃脫）；runner macOS 15.7.9 `xnu-11417.140.69.711.44`、`/bin/bash` 3.2.57；artifact manifest `HASHES.txt`（60 條，`shasum -c` 全 OK）SHA-256 `c7574e17c2752ab9e66cfe193fe174dc9cd367dfdfa120549c1db6166a983877`。**B2c-7 CI 驗證（v7，resolved 依據）**：驗證分支 `b2c7/verify` head `a679fcd66adc1190cc3076f155a42a5f8440a57c`（一次性 workflow＋docs，不進 main），implementation base `852c28730139041732d02f639f55a18196c77775`（每 job 以 `git merge-base --is-ancestor` 驗證並記入 artifact），run **`34039387868`**（event push，2026-09-06 14:30:46–14:33:51Z，四 job 全 success，未 rerun、未 dispatch）；每 job 三個 `go test -race … -count=100 -json` step：`TestOrphanDoesNotHangNormalExit`（`internal/claude`）、`TestSupervisorCleanupTwoLayerForkOrphan`（`internal/proc`，B2c-5 新增的兩層 fork 案例）、`TestCtxCancelKillsWholeGroup`＋`TestTerminateEscalatesToGroupKill`（escalation）——四條測試於 macos-15-intel r1／r2、ubuntu-latest r1／r2 各 100/100 terminal pass（合計各 400/400），三個 step rc 皆 0，JSON 中無 `DATA RACE`／`panic`／timeout／套件級 FAIL；最長單次 0.24 s（escalation grace 0.2 s）；runner macOS 15.7.9 `xnu-11417.140.69.711.44`（與 B2c-2／B2c-3 重現時相同 kernel）、`/bin/bash` 3.2.57；ubuntu 24.04.4 `6.17.0-1022-azure`；每 job 的 `SHA256SUMS.txt`（11 條）`shasum -c` 全 OK，四份 artifact 總 manifest 48 條 SHA-256 `79ea4c880b5c37434e6303e02bcc804267a50936c496eff51d3e93efcb17e85c`。**限制**：CI 未記錄 rekill 次數或 `CleanupIncomplete` 是否曾為 true（測試只斷言結果），修法生效的直接證據為 B2c-5 白箱測試與本次跨平台 0/400 紅；對照修法前同 kernel 上 B2c／B2c-3 的 36–54%（macOS）與 98–99%（ubuntu round 1）重現。完整記錄：`docs/architecture/orphan-timeout-diagnosis-record.md`（§7 為 B2c-3、§8 為 B2c-4～B2c-7） |

負載重驗定義：B1a-4 plan（`docs/superpowers/plans/2026-09-04-b1a-4-integration-acceptance.md`）Gate A 的四層矩陣——M1 逐包單跑、M2 五套件併行 ×3、M3 三份併發 ×1、M4 背景負載下 focused ×20，全部 `-race -p=8`，整合 HEAD `583387d`，本機 8 核 16 GB。每條 27 次「有效、指定執行」（背景負載中的執行不計）全 PASS；18 個 `-json` artifact 頂層 FAIL 為 0。

## A-1. CI 耗時量測（B2b-2 五筆樣本，v10 新增）

**規則**：D5 決定入樣資格、D6 決定統計分組；**指紋不同不得混算**。指紋＝`internal`／`testdata`／`go.mod`／`go.sum`／`frontend`／`ci.yml` 的 git tree hash。全部樣本的 `ci.yml` SHA-256 皆為 `8966b7030cc0a540402e48900593908ba397c1392772f121e8e62231a9f9ffff`（與 main 相同）；**npm cache 五筆皆 hit（`node-cache-Linux-x64-npm-9dcd67fe…`），依 D6 不稱冷啟動**。

| # | run／attempt／event | head | 分組 | 合格 | 總長 | frontend | checksums | wails-build | go |
|---|---|---|---|---|---|---|---|---|---|
| 1 | `34075935919`／1／pull_request | `5a83717` | 舊組 | 是 | 378 s | 51 s | 8 s | 152 s | 319 s |
| 2 | `34077383674`／1／pull_request | `57b2448` | 舊組 | 是 | 390 s | 49 s | 5 s | 268 s | 335 s |
| 3 | `34080497639`／1／pull_request | `ea5123a` | 舊組 | 是 | 424 s | 59 s | 6 s | 189 s | 357 s |
| 4 | `34202299786`／1／pull_request | `f2a5ebb` | A1a 組 | 是（D5） | 273 s | 59 s | 4 s | 207 s | **95 s（failure，提前中止）**——gofmt 檢查失敗即結束，未執行 build／vet／test |
| 5 | `34209630775`／1／pull_request | `c549d45` | A1a 組 | 是 | 331 s | 47 s | 4 s | 231 s | 276 s |

**逐測試耗時**（#2 `TestClaudeAssistFailsLoudOnOversizedLine`／#3 `TestMultiTurnSendAndTurnBoundaries`／#6 `TestOutputCancellationKillsGrandchildren`；F1／F2 見 B 段）

| # | #2 | #3 | #6 | F1 | F2 |
|---|---|---|---|---|---|
| 1 | 0.39 s | 0.04 s | 0.35 s | 195 ms | 149 ms |
| 2 | 0.35 s | 0.03 s | 0.35 s | 218 ms | 147 ms |
| 3 | 0.50 s | 0.04 s | 0.35 s | 269 ms | 164 ms |
| 4 | **缺** | **缺** | **缺** | 211 ms | 188 ms |
| 5 | 0.35 s | 0.04 s | 0.35 s | 175 ms | 142 ms |

**分組統計（不混算；缺值不補零）**

| 指標 | 舊組（n=3，指紋同 main `1bbb47a`） | A1a 組（指紋 `frontend`＝`278a5bf…`） |
|---|---|---|
| run 總長 | 378／390／424 s | 273／302／331 s（n=2） |
| frontend | 49／51／59 s | 47／53／59 s（n=2） |
| checksums | 5／6／8 s | 4／4／4 s（n=2） |
| wails-build | 152／189／268 s | 207／219／231 s（n=2） |
| **go（job 耗時）** | 319／335／357 s | 95／185.5／276 s（n=2，**含樣本 4 的提前中止 job**，不得解讀為完整測試流程耗時） |
| #2 | 0.35／0.39／0.50 s | **0.35 s（n=1）**——樣本 4 未執行 Go 測試 |
| #3 | 0.03／0.04／0.04 s | **0.04 s（n=1）** |
| #6 | 0.35／0.35／0.35 s | **0.35 s（n=1）** |
| F1 | 195／218／269 ms | 175／193／211 ms（**n=2**，樣本 4 前端測試正常執行） |
| F2 | 147／149／164 ms | 142／165／188 ms（**n=2**，同上） |

（格式：min／median／max）

**A 段「最後一次負載重驗」的回寫**：#2／#3／#6 於 CI（`macos-15-intel`）的實測 Elapsed 見上表。**要分清兩件事**：樣本 4 的 `go` **job 耗時有紀錄**（95 s，08:02:46Z→08:04:21Z，failure），缺的是**測試耗時**（#2／#3／#6）——該 job 在 gofmt 檢查即結束，未執行 build／vet／test，測試輸出缺席亦導致 artifact 上傳失敗。因此 **A1a 組的 job 耗時 n=2（含提前中止）、#2／#3／#6 有效 n=1**；測試耗時的缺值**不補零**。此為**格式檢查缺漏，不是 Go 測試失敗**。

**逐筆來源索引**

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
5. **CI 耗時分布已由 B2b-2 量測（v10／v11 回填，取代原「歸屬 B2、待量測」的表述）**：#2／#3／#6 於 CI（`macos-15-intel`）的實測 Elapsed 與四個 job 的耗時分布見 **A-1 段**；依 D6 分為舊組 n=3 與 A1a 組；A1a 組的 **job 耗時 n=2**（含樣本 4 提前中止），**`#2`／`#3`／`#6` 的測試耗時 n=1**（樣本 4 未執行 Go 測試），**F1／F2 仍各 n=2**（樣本 4 的前端測試正常執行）。兩組不混算。**冷啟動仍未驗證**——五筆樣本的 npm cache 皆為 hit（`node-cache-Linux-x64-npm-9dcd67fe…`），依 D6 不得稱冷啟動；B1a 的本機量測與本表不互相外推（規則 4 不變）。
6. **#6 的自然誤紅從未在本機重現**：現有證據是 B1a-3 的人工延遲證明機制與 B1a-4 負載下 27/27 全綠，結論僅為「本機負載下未重現」。
7. **F1 `PlanWorkspace > PlanAssist 送出後草稿區顯示 loading，事件送達後輸出累積` 與 F2 `SpecWorkspace draft accept > discards spec-assist result if the file switches during the call` 自 B1b 處置後，「相同 timeout 可單獨重跑一次判定」的前端規則對這兩條失效**，任何 FAIL 先分類：(i) 命中該測試的契約斷言（F1：`assist-busy` 顯示／`draft-text` 累積／busy 解除；F2：`draft-text` 為空／`accept-draft` disabled）、或可歸因於其契約路徑的卡死（含再次 `Test timed out in 5000ms` 且無環境訊號）→ **回歸，required check 阻擋，不得重跑吸收**；(ii) setup 失敗（掛載／mock 建立）、可證明的資源失效（OOM、worker 啟動失敗）、或其他測試造成的中斷 → **該次無效**，揭露後重跑，不算紅也不算綠。另：前端測試不得把模組動態載入或首次建構重型元件的成本留在測試本體。**此規則不受 B1b pre-merge 檔案層級 control 例外影響。**

8. **unresolved 狀態語意（v5 新增，#7；v6 同版本補記擴充；v7 起 #7 已 resolved，本規則保留給其他 CI-only 候選）**：CI 已重現，且（a）機制或責任邊界未定，**或（b）責任邊界與修法已裁定、但實作或驗證尚未完成**的條目。不是 resolved 也不是 no-change disposition，不得除名或改寫為誤紅。**#7 的兩種已登記形狀（v7 起為歷史；修法後在 B2c-7 run `34039387868` 四 runner ×100 未再出現；若在 B2c-5／B2c-6 之後的 HEAD 再度出現，依規則 3 以現行 HEAD 重現並登記為新候選，不得直接沿用 #7）**——(i) macOS 於 5 秒 guard 命中 `session_test.go:207: drain/Wait hung on orphan-held pipes`（EOF 卡死）、(ii) ubuntu 於 drain／Wait 返回後立即命中 `session_test.go:210: orphan must be reaped by supervisor on parent exit`（oracle 即時失敗）——才視為 #7 的已知未解決項；**其他訊息、panic、data race、`-timeout`、setup／環境問題仍須依規則 1 所定的分類方式另行分類**（命中契約／oracle 斷言或可歸因於契約路徑的卡死 → 契約回歸；setup／資源失效／他測試造成 → 該次無效），不得歸入 #7；此為 #7 的分類契約。已知形狀亦不得以 retry、放寬 guard 或跳過吸收（處置前該 job 維持紅燈語意）；處置路徑與狀態轉換由 backlog 續票決定（v5 當時為 B2c-3／B2c-4；**v6 補記後為 B2c-5／B2c-6／B2c-7**），轉為 resolved／no-change 時須附 commit 或裁定記錄；#7 轉 resolved 的完整條件見 B2c-4 裁定記錄 §5（exact implementation SHA、artifact 完整性、每條核心／escalation 測試各 400/400、零 invalid／setup／race／timeout、v7 落地）。

## 修訂記錄

- v12（2026-09-08）：來源索引收斂三處（引用重新取得的 `review-20260908T175707/`、規則 5 的 n 分開限定、遺失措辭改述），並**修復 `b9c249a` 的內容刪除**——該次替換以「逐筆來源索引」為錨點，但版本行內也含該詞，切點落到檔首，導致 A 段（Go #1–#7 名單、處置證據、負載重驗定義）、A-1 段（五筆耗時表、逐測試耗時表、分組統計與提前中止限制）與前言（文件性質、更新責任）一併遺失。本版以 `c6b776c` 為基礎，僅在 A-1 段範圍內替換索引區塊，其餘原文保留。入樣、分組與統計數值不變。

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
