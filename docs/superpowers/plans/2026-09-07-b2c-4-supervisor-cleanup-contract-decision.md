# B2c-4 supervisor cleanup 契約裁定與 register #7 處置（決策票，不改 code）

> 版本：rev3（2026-09-07，rev2 複核修正：P1——第一次 cleanup KILL 的錯誤路徑納入迴圈，凍結完整狀態機（base＝第一次 KILL **返回**時刻不限成功；ESRCH 完成不排程；nil 發原事件並排程；EPERM／其他 errno 不發事件但排程；每次 rekill 依回傳值處理；1 s 確認非 ESRCH 一律 `CleanupIncomplete`）、B2c-5 增加 cleanup signal seam 與「第一次 EPERM→nil→ESRCH」確定性測試；D8 通過（薄包裝只在 supervisor 設定強制關閉狀態後才映射、支援 `errors.Is`、呼叫端自行 `Close()` 不誤標）；D9 不採（移除 gave-up 事件）；D10 通過；四處一致性修正（基準三份文件、EPERM 誤判「可能誤判」、Linux「本輪未觀察到相同缺口」、D10 引用改「§7.3 XNU 段落」）；前版：rev2（2026-09-07，決策 gate 第一輪 CHANGES_REQUIRED 後修訂：兩項 P1——EPERM 不得視為終止（`P_REF_NEW` 亦會造成 EPERM）、預算耗盡須有強制解除 pipe 等待的 fail-loud 路徑並把 `CleanupIncomplete` 傳到 `ports.Exit`／codex meta／assist；D1–D7 裁定回寫（固定絕對偏移 1–512 ms＋1 s 確認、`Exit.Err` 不混入、oracle 只以 ESRCH 為消失、rekill 事件與既有測試改名、估點 0.6／0.3／0.2、`b2c7/verify`、#7 resolved 完整條件）；三處措辭限縮；同分支附 main diagnosis-record／register 的 EPERM 勘誤；前版：rev1）
> 狀態：**待 owner 複核 rev3**。本票不修改任何 code；產出為裁定記錄、implementation 票面與估點、register #7 處置路徑。本文件在 owner 核准後即為 B2c-4 的裁定記錄（backlog rev20 落地時引用）；EPERM 勘誤隨本票 docs-only 推送落地，不等 B2c-7。
> 票源：Pre-M4 Readiness Backlog **B2c-4**（rev18 新增，決策票，**0.2 pt**＝1.5–2.5 hr，owner 2026-09-06 採用；rev19 依賴 B2c-3 已成立）
> 基準：`main`＝`origin/main`＝`a0966cc`（register v6、backlog rev19、diagnosis-record v2）；程式碼 `internal/proc/proc.go` 自 `82caf8b`（B1a-1）後未變。分支 `b2c4/decision`（本機，自 `a0966cc`）目前含三份文件：本文件、`orphan-timeout-diagnosis-record.md`（v2 勘誤）、`wall-clock-test-register.md`（v6 勘誤）。
> 事實來源：`docs/architecture/orphan-timeout-diagnosis-record.md` v2 §7（B2c-3）、§3–§4（B2c／B2c-2）；`internal/proc/proc.go:27-31,185-203,291-331`；呼叫端 `internal/claude/session.go:73-121`、`internal/codex/session.go:27-41`、`internal/codex/owner.go:98-230`、`internal/assist/oneshot.go:129-175`；既有 oracle `internal/proc/proc_test.go:37`、`internal/claude/session_test.go:180`、`internal/codex/session_test.go:84`、`internal/evidence/runner_test.go:106`。

---

## 1. 已確認事實（逐條可追溯）

1. **契約文字**（`proc.go:27-29`）：「子程序一退出即 group SIGKILL（清掉持有 pipe 的孫程序 → reader 的 EOF 保證到來）→ 收完 stderr → 快取 Exit」。supervisor 實作（`proc.go:185-203`）：`cmd.Wait()` → `close(exitedCh)` → **一次** `SignalGroup(SIGKILL)` → 成功才發 `sigEventSupervisorCleanupKill` → `wg.Wait()`（stderr EOF）→ 快取 Exit → `close(doneCh)`。
2. **macOS 上這個保證不成立**（diagnosis-record v2 §7.4 (1)，已在 CI 觀察）：production 順序 ×400 有 36–54% 輪次在 `SignalGroup(SIGKILL)` 成功返回後仍有 live member（以 `bash`＋`sleep` pair 為主），群組於約 30 s 後（`sleep 30` 自然結束）才消失。機制解讀（§7.3／§7.4 (2)，原始碼一致性、送達瞬間未直接觀察）：KILL 送達時正處於建立中的成員被 XNU `killpg1` 靜默略過且不延後補送。
3. **今天的實際影響**：`Done()`／`Wait()` 本來就要等 stderr EOF（`proc.go:195`），EOF 又被逃脫者持有的 pipe 卡住，所以失敗形狀的效果是**收尾延遲到逃脫者自然結束**——fixture 是 30 s；若真實 CLI 在退出前 fork 的 helper 長駐並持有 stdout／stderr，`Done()` 會無限期不關。呼叫端受影響面：`claude.Session.Wait()`／`Events()` 關閉（`session.go:109-119`）、`assist` one-shot 的 `p.Wait()`（`oneshot.go:166`）、codex `GenerationOwner.Done()` 死亡 reaper（`owner.go:101-106,208-230`）與 `FinalizeWith` 的 `Server.Wait()`（`owner.go:156`）。
4. **`Terminate()`／ctx 取消路徑最終也靠同一次 cleanup KILL**：`Terminate` 送 group TERM、grace 後 group KILL（`proc.go:291-331`），leader 死後 supervisor 再送一次 cleanup KILL；孫程序若在任一 KILL 送達時正在建立中，同樣可能逃脫。B2c-3 只量測了 cleanup KILL，escalation KILL 未量測（假設 A6）。
5. **經身分驗證的第二次群組 KILL 在探針條件下 109／109 清除**（§7.2、§7.5 (2)）：逃脫者繼承 PGID，+300 ms 後重送即清除；本機 preflight 43／43（未驗證身分）。
6. **Linux 本輪未觀察到相同逃脫，原始碼對照與此一致**：production 順序 0／400、實際送出第一次 KILL 的 199 輪 0 存活；v6.17 `copy_process` 在 fork 中收到 fatal signal 會中止 fork、或把多程序訊號補給子程序（§7.3）。
7. **兩平台 `kill(-pgid, 0)` 語意不同**（§7.5 (4)，本票勘誤）：macOS 在 `killpg1` 走訪時**沒有任何可取得 ref 的成員**即回 **EPERM**——包含只剩 zombie（filter 排除 SZOMB）、成員皆在 exit transition（`P_REF_DEAD`）**以及成員仍在建立中（`P_REF_NEW`）**三種情況（`kern_proc.c:620-631` 對 `P_REF_NEW | P_REF_DEAD` 一律取 ref 失敗；D3 對照）；因此 **EPERM 不能代表群組已消失或已終止**，它正是本票要處理的建立窗口的可能表徵（CI `kill0@0` EPERM 8–47/100、本機 89/89 的個案未區分三者）；Linux 在 `Wait()` 返回瞬間回 **0**（B2c-2：97/100，+1 ms 起 100% ESRCH，殘存者狀態未觀察、zombie 為最合理解讀）。既有四個 oracle（`groupGone`／`groupDead`／`srvGroupDead`／evidence `groupGone`）都是一次性 `syscall.Kill(-pgid, 0) != nil`：macOS 把 EPERM 當「已消失」（可能誤判：EPERM 也可能是建立中的 `P_REF_NEW` 成員）、Linux 把 zombie 殘留當「仍存在」（B2c round 1 ubuntu 98–99/100 紅的來源）。
8. **B2c-3 探針的 `kill0@0` 因 `/proc` 掃描延遲不可與 B2c-2 比較**，ubuntu oracle race 只有 B2c-2 的支持性證據，殘存者狀態未直接觀察。
9. **既有 proc 測試的 orphan fixture 只有一層 fork**（`proc_test.go:40` `bash -c 'trap "" TERM; sleep 30' & …`），與本機 preflight 實驗 B（0／140）同結構，兩者結果一致（該結構在 CI 從未紅、本機 0／140），但這只是相關性，不作因果斷言；`fake-claude.sh:16` 的 `[ -n … ] && … &` 多一層子 shell，是目前觀察到會進窗口的結構（§7.4 (2)）。
10. **B2a 現況**：PR #1（`b2a/ci-workflows` `ffcd161`）Gate A 停止，禁止合併、禁止 main-run、不再重跑；main 上沒有 CI workflow。

---

## 2. 契約缺口的定性

- **屬性**：production supervisor 的 cleanup 契約在 macOS 上有一個由 kernel 語意造成的缺口——「一次 group SIGKILL 成功返回」不蘊含「群組成員全數終止」，因而不蘊含「EOF 保證到來」。這不是測試 oracle 的誤紅，也不是 fixture 特有：任何在退出前瞬間 fork 的子程序都可能觸發，真實 CLI 是否如此**未知**（假設 A1），但契約不應依賴這個未知。
- **嚴重度**：本輪未觀察到資料錯誤或殺錯程序（PGID 重用是剩餘風險，見假設 A2），影響是 `Done()`／`Wait()` 的收尾延遲（上限＝逃脫者壽命）與孫程序殘留。對 codex 長駐 server 的死亡 reaper 而言，延遲會推遲 replacement；對 claude one-shot 而言，會推遲 `Events()` 關閉。
- **ubuntu 形狀**是獨立的測試側問題：oracle 在 `Wait()` 返回瞬間取樣，Linux 的 zombie 殘留讓 `kill(-pgid, 0)` 回 0。production 在 Linux 本輪未觀察到相同缺口，原始碼對照與此一致。

---

## 3. 方案

### O1（owner D1 通過，rev2 修正版）：有界清理——cleanup KILL 後以固定絕對偏移確認並重送，無法確認時強制解除本端 pipe 等待並揭露未完成

- **契約（新）**：supervisor 提供**有界清理**：子程序退出後送 group SIGKILL，並在固定時間表內重送與確認；**不再宣稱單次或有限次 KILL 絕對保證群組終止**。預算內未能確認群組消失時，supervisor **強制解除本端對 stdout／stderr 的等待**（關閉 proc 持有的 read end），讓 `Wait()`、`Done()` 與呼叫端的 `Events()` 有界收斂，並以 `Exit.CleanupIncomplete=true` 揭露；`Exit.Err` 維持既有「子程序死因」語意，不混入 cleanup 狀態。
- **狀態機（D2 固定、不開放 `Config` 覆寫；rev3 凍結）**：
  1. **第一次 cleanup KILL**：`cmd.Wait()` 返回後送 `SignalGroup(SIGKILL)`，以其**返回**（不限成功）的 monotonic 時刻為 **base**。回傳值：**`ESRCH`** → 群組確定不存在，完成，**不啟動排程**；**`nil`** → 送出成功，發 `sigEventSupervisorCleanupKill`（恰一次），啟動排程；**`EPERM` 或其他 errno** → **不發事件，但仍啟動排程**（EPERM 可能是只剩 `P_REF_NEW` 成員的建立窗口，不能視為完成）。
  2. **排程**：於 base 的絕對偏移 **1／2／4／8／16／32／64／128／256／512 ms** 各做一次 `kill(-pgid, 0)` 探測：**`ESRCH`** → 立即完成；**`nil`** → 立即 `SignalGroup(SIGKILL)`（rekill），rekill 的回傳值同樣處理——`nil` 才發 `sigEventSupervisorCleanupRekill`、`ESRCH` 立即完成、`EPERM`／其他錯誤不發事件並繼續；**`EPERM`／其他 errno** → 本輪不送 KILL，繼續下一個偏移。
  3. **1 s 最終確認**（不送訊號）：**`ESRCH`** → 完成；**其他任何結果（`nil`／`EPERM`／其他 errno）** → **`CleanupIncomplete`**，進入 fail-loud 路徑。
  時間表包含一次晚於 B2c-3 +300 ms 證據點的重送機會（512 ms），且最後一次重送後仍有確認點（1 s）。
- **fail-loud 路徑（D3 擴充版 (a)）**：預算耗盡時 (1) 設 `Exit.CleanupIncomplete=true`；(2) 關閉 proc 持有的 stderr read end（`errR`）並讓 stderr reader 結束，`wg.Wait()` 不再依賴 EOF；(3) 關閉 proc 持有的 stdout read end（`p.Stdout`），呼叫端的 reader 會收到讀取錯誤——為了讓呼叫端能分辨「supervisor 放棄清理」與其他 I/O 錯誤，`p.Stdout` 以薄包裝提供：**只有在 supervisor 已設定「強制關閉」狀態之後**，底層的 closed error 才映射為具名錯誤 **`proc.ErrCleanupIncomplete`**（支援 `errors.Is`）；呼叫端自行 `Close()` 造成的 closed error **不得**被誤標（D8 通過）；(4) 快取 `Exit`、`close(doneCh)`。之後逃脫者若仍存活，屬揭露後的已知殘留，不再由本 Proc 負責。
- **揭露傳遞（D3）**：`ports.Exit` 新增 `CleanupIncomplete bool`（claude `Session.Wait()` `session.go:128` 對應）；codex `recorder.Meta` 新增 `cleanup_incomplete`（`owner.go:156-160` 取 `ex` 時一併寫入）；assist one-shot 在 EOF 後 `p.Wait()` 若 `CleanupIncomplete` 則回傳 wrap 了 `proc.ErrCleanupIncomplete` 的錯誤（不改 `ex.Err` 語意）；claude 的 stream 在讀到 `ErrCleanupIncomplete` 時照既有路徑送出 `KindStreamError`（`session.go:107-113`），`Raw` 帶該錯誤文字。
- **不改變**：`Done()` 仍以「`Exit` 已快取」為語意（EOF 或強制解除其一）；`Wait()`／`Terminate()`／`PGID()` 介面不變；`sigEventSupervisorCleanupKill` 仍只在第一次 group KILL **成功送出**時發一次；重送成功送出另發 **`sigEventSupervisorCleanupRekill`**（每次一次）；`signalEvent` 契約維持「只記錄實際成功送出的訊號」，**不新增 gave-up 事件**（D9 不採）——預算耗盡由 `CleanupIncomplete` 與有界 `Wait()` 確定性斷言。
- **seam（D5）**：偏移時間表經**獨立、nil-safe 的 seam**（沿 B1a-1 `seamAfter` 慣例，例如 `cleanupAfter afterFunc`，nil 時退回真實時鐘），白箱測試可把整個時間表壓縮；`kill(-pgid, 0)` 探測經可注入的 `groupProbe func(pgid int) error`（nil 時退回 `syscall.Kill`），白箱測試可模擬 `nil`／`EPERM`／`ESRCH` 序列；**cleanup signal 也經可注入邊界**（例如 `cleanupSignal func(pgid int, sig syscall.Signal) error`，nil 時退回 `SignalGroup`），使「第一次 KILL 回 EPERM、之後探測 `nil`、rekill 成功、再 `ESRCH`」可確定性重演。
- **身分保護**：目標 PGID＝leader pid，leader 已被 `cmd.Wait()` 回收；只要群組還有成員，pgrp 就存在且不會被新程序取得（新程序的 pgid 是其父的 pgid，除非自行 `setpgid`）；本 repo 自己以 `Setpgid` 起的新 Proc 其 pgid＝自身 pid，要與舊 PGID 相撞需 pid 號碼在 ≤1 s 內回繞重用（macOS `PID_MAX` 99999 遞增、Linux `pid_max` 遞增），視為可忽略但**明列為假設 A2**；`ESRCH` 後不再送任何訊號；`EPERM` 時不送訊號。
- **與 B2c-3 證據的對應**：CI 於 +300 ms 重送 109／109 清除；本方案在 1–512 ms 之間最多十次探測、對 `nil` 逐次重送，涵蓋本機／CI 觀察到的窗口（0–1 ms 為主、5／10 ms 另有 C1 fork C2 窗口）並包含 512 ms 這個晚於證據點的重送。
- **劣勢／風險**：(a) 失敗形狀下 cleanup 最多多花 1 s 才 fail-loud（正常路徑第一次探測即 `ESRCH`，成本一次 syscall；Linux 上第一次多半看到 zombie 回 `nil`，會對 zombie 重送一次 KILL，無害，2 ms 探測即 `ESRCH`）；(b) 強制關閉 read end 是新的收斂路徑，呼叫端會看到 `ErrCleanupIncomplete`（claude 為 `KindStreamError` 事件、assist 為回傳錯誤、codex 為 meta 欄位），這是刻意的 fail-loud，但屬使用者可見行為變更（僅在失敗形狀下發生）；(c) 逃脫者仍可能殘留（fork bomb 型或長駐 helper），本 Proc 只揭露不保證清除；(d) `EPERM` 連續到 1 s 的情況（例如成員長時間處於 `P_REF_NEW`／exit transition）也會 fail-loud，可能把「慢但終究會消失」的群組判為未完成——1 s 上限由 D2 裁定，B2c-7 ×400 觀察是否出現。
- **相容性**：`Exit`／`ports.Exit`／`recorder.Meta` 各新增一個欄位（向後相容：預設 false／缺省）；`p.Stdout` 型別仍為 `io.ReadCloser`；既有測試 `TestSupervisorCleanupKillEventFiresOnlyWhenGroupActuallyCleaned` 的名稱與語意已不成立，改名為「第一次 cleanup signal 成功」語意並**逐事件種類斷言精確次數**（D5）。

### O2：等群組消失才關 `Done()`（強契約）

- 在 O1 之上，把 `Done()` 改為「stderr EOF **且** `kill(-pgid, 0)` 非 `nil`」。
- **不建議**：EOF 已隱含「持有 pipe 的成員已死」，不持有 pipe 的殘留成員對呼叫端沒有可觀察影響；改 `Done()` 語意會牽動 codex reaper 與 `FinalizeWith` 順序（`owner.go:110-128` 明文凍結的順序），相容性風險高於收益。

### O3：只修測試側（fixture 不在退出前 fork／oracle 放寬），production 不改

- **不採（owner D1）**：契約文字與實作在 macOS 上確實不成立（事實 2、3），且 register 規則明文「不得以放寬 5 秒 guard 或加 retry 作修法」；把 fixture 改成不觸發只會讓 CI 看不到缺口。ubuntu 側的 oracle 修正則是必要的（見 §4 B2c-6），與 O1 並行。O2 亦不採（owner D1）。

---

## 4. Implementation 票面（供 backlog rev20 立項；估點以 hr 為權威）

| 票 | 範圍 | 驗收條件 | 估點 |
|---|---|---|---|
| **B2c-5** proc supervisor 有界清理（O1 修正版） | `internal/proc/proc.go` supervisor：§3 O1 凍結的狀態機（base＝第一次 cleanup KILL 返回時刻不限成功；ESRCH 完成不排程；nil 發事件並排程；EPERM／其他 errno 不發事件但排程；排程 1–512 ms 探測、`nil` 即 rekill 且依回傳值處理、`ESRCH` 終止、`EPERM`／其他繼續；1 s 確認非 `ESRCH` 一律 `CleanupIncomplete`）；預算耗盡 fail-loud：`Exit.CleanupIncomplete`、關閉 stderr／stdout read end、`p.Stdout` 薄包裝只在強制關閉狀態後映射 `ErrCleanupIncomplete`（`errors.Is`，呼叫端自行 `Close()` 不誤標）、快取 Exit 與 `close(doneCh)`；新增 seam：`cleanupAfter`（nil-safe）、`groupProbe`（nil-safe）、`cleanupSignal`（nil-safe）、事件 `sigEventSupervisorCleanupRekill`（不新增 gave-up 事件）；`ports.Exit.CleanupIncomplete`、claude `Session.Wait` 對應、codex `recorder.Meta.cleanup_incomplete`（`FinalizeWith` 寫入）、assist one-shot 回傳 wrap 錯誤；既有事件測試改名為「第一次 cleanup signal 成功」語意並逐事件種類斷言精確次數；白箱測試以 `groupProbe`＋`cleanupSignal`＋`cleanupAfter` 模擬序列：(i) 第一次 KILL 後探測立即 `ESRCH`（0 rekill）；(ii) `nil`×N 後 `ESRCH`（N 次 rekill、N 個 rekill 事件）；(iii) `EPERM`×k 後 `ESRCH`（0 rekill、排程繼續、成功）；(iv) 到 1 s 仍 `nil`／`EPERM`（`CleanupIncomplete=true`、`Wait()`／`Done()` 有界返回、stdout reader 收到 `ErrCleanupIncomplete` 且 `errors.Is` 成立；呼叫端自行 `Close()` 的對照案例不得被標記）；(v) **第一次 KILL 回 `EPERM`**（不發 cleanup 事件、仍啟動排程）→ 探測 `nil` → rekill 成功（rekill 事件一次）→ `ESRCH`（成功、`CleanupIncomplete=false`）；(vi) 第一次 KILL 回 `ESRCH`（完成、0 探測、0 事件）；真實 fixture 測試沿用 `proc_test.go` 既有 orphan 案例並新增**兩層 fork** 案例（`[ -n … ] && bash -c '…' &` 結構）作 CI 證據（本機不保證重現）；`Terminate` 升級路徑不改（假設 A6 由 B2c-7 檢查） | (1) 既有 proc／claude／codex／assist 測試無 tag 全 PASS（`-race`）；(2) 白箱六案各一，mutation：移除重送 → (ii) 紅；把 `EPERM` 當終止 → (iii) 紅（斷言成功前仍有後續探測）；移除強制關閉 → (iv) 紅（`Wait()` 逾時）；第一次 KILL 非 nil 就不排程 → (v) 紅；(3) `sigEventSupervisorCleanupKill` 恰 0／1 次、rekill 恰 N 次，逐種類斷言精確次數；(4) `Done()` 仍以「Exit 已快取」為語意、`Terminate` 介面不變、`Exit.Err` 語意不變；(5) codex／assist／claude 的揭露各有一條測試 | **0.6 pt**（5.0–7.0 hr，中位 6.0；owner D6） |
| **B2c-6** 測試 oracle 有界化（兩平台語意，D4） | 四個 oracle（`proc_test.go:37`、`session_test.go:180`、`codex/session_test.go:84`、`evidence/runner_test.go:106`）改為共用語意：`Wait()` 返回後有界輪詢（上限 2 s，間隔 1 ms 起退避）；**只有 `ESRCH` 算群組消失**；`EPERM` 繼續輪詢並記錄次數；逾 2 s 仍為 `nil` 或 `EPERM` → 失敗並附最後一次 `ps -o pid,pgid,stat,command -g <pgid>` 快照與 errno 序列，**不把 EPERM 偽裝成 gone**；不共用 helper 檔以避免跨套件 import（每套件一份，註解指向本裁定） | (1) 本機 `-race` 各套件 PASS；(2) mutation：把上限改 0 → Linux CI 應重現 B2c round 1 的 ubuntu 形狀（可選，CI 上驗）；把 `EPERM` 視為 gone → 對應單測（synthetic errno 序列）紅；(3) 5 秒 combined-completion guard 與 `TestOrphanDoesNotHangNormalExit` 的斷言形狀不變（不放寬 guard、不加 retry 於被測路徑，只有 oracle 取樣有界化） | **0.3 pt**（2.5–3.5 hr，中位 3.0；owner D6） |
| **B2c-7** CI 驗證與 register #7 處置（D7） | 在驗證分支 **`b2c7/verify`**（含 B2c-5＋B2c-6 已合併 main 的 exact implementation SHA＋一次性 workflow）以 B2c round 1 layer 1 的方式跑：核心測試 `TestOrphanDoesNotHangNormalExit` 與 proc 兩層 fork 案例、escalation 測試 `TestCtxCancelKillsWholeGroup`／`TestTerminateEscalatesToGroupKill`，各 `-count=100`，`macos-15-intel`×2＋`ubuntu-latest`×2；一次推送、不 rerun；artifact manifest 完整；逐輪分類 | #7 轉 **resolved** 的完整條件：(1) exact implementation SHA 與 artifact 完整性（manifest `shasum -c` 全 OK）；(2) 每條核心／escalation 測試各 **400/400** PASS；(3) **零** invalid／setup／race／timeout；(4) register **v7** 已落地（commit 欄填 B2c-5／B2c-6 的 main commit、負載重驗欄填 run id）；仍有紅 → 逐輪分類（形狀 (i)／(ii)／其他），維持 unresolved，回到 B2c-5 | **0.2 pt**（1.5–2.5 hr，中位 2.0；owner D6） |

合計 **11.0 hr → 1.1 pt**（B 軌與總計於 backlog rev20 重算：132.05＋11.0＝143.05 hr → 14.305 → **14.31 pt**；197.05＋11.0＝208.05 hr → 20.805 → **20.81 pt**；依 rev8 捨入規則）。

順序：B2c-5 → B2c-6 → B2c-7（owner D6）。B2a 解除 blocked 的條件（D7）：#7 依上述完整條件轉 resolved、register v7 已落地之後；PR #1 rebase 到含 B2c-5／B2c-6 的 main 後**仍須重新完成 Gate A**（不沿用 `ffcd161` 的既有 run）。

---

## 5. register #7 處置路徑

- 現況 **unresolved**（v6）；本票隨附的 EPERM 勘誤（diagnosis-record v2 §7.2／§7.3／§7.5 (4)、register #7 row）不改狀態。
- B2c-7 滿足 D7 完整條件（exact implementation SHA、artifact manifest 完整、每條核心／escalation 測試各 400/400、零 invalid／setup／race／timeout）→ register **v7** #7 → **resolved**，commit 欄填 B2c-5＋B2c-6 的 main commit，「最後一次負載重驗」欄填 run id；規則 8「兩種已登記形狀」改為歷史敘述、分類契約保留給其他 CI-only 候選。**v7 落地之後**，B2a 才解除 blocked，且 rebase 後仍須重新完成 Gate A。
- B2c-7 任一條件不成立 → 維持 unresolved，逐輪分類（形狀 (i)／(ii)／其他），回到 B2c-5 修正；不得以重跑吸收。
- **no-change disposition 不適用**：契約文字與實作已被 CI 證明不成立（事實 2）。

---

## 6. 待驗證假設

- **A1**：真實 Claude／Codex CLI 是否在退出前瞬間 fork 子程序——未知；O1 不依賴此假設。
- **A2**：pid 在 ≤1 s 內回繞重用且新程序以該 pid 自成 process group 的機率可忽略——依 macOS／Linux pid 分配遞增推論，未量測；為剩餘風險。
- **A3**：macOS `EPERM` 語意為「走訪時沒有任何可取得 ref 的成員」，涵蓋 zombie、`P_REF_DEAD` 與 **`P_REF_NEW`**——D3 原始碼一致，CI／本機的 EPERM 個案未區分三者；因此 O1 對 EPERM 只能「不送、繼續」。
- **A4**：Linux zombie 由 init／subreaper 於 ≤1 ms 內回收——B2c-2 +1 ms 起 100% ESRCH，僅 ubuntu-latest 觀察。
- **A5**：1–512 ms 十次探測＋1 s 確認足以涵蓋逃脫者完成建立並可被送達的時間——CI 窗口 0–1 ms 與 5／10 ms，+300 ms 重送 109／109；含 512 ms 重送，由 B2c-7 ×400 驗證；連續 EPERM 到 1 s 的情況是否出現亦由 B2c-7 觀察。
- **A6**：`Terminate` 升級 KILL 的窗口由後續 cleanup KILL 迴圈涵蓋——未單獨量測，由 B2c-7 的 escalation 測試 ×100 檢查。

---

## 7. owner 裁定（決策 gate 第一輪，2026-09-07，rev2 回寫）與待複核項

- **D1（通過）**：採修正後的 O1；不採 O2／O3。契約改為「有界清理；無法確認時強制解除本端 pipe 等待並揭露未完成」，不再宣稱單次或有限次 KILL 絕對保證群組終止。
- **D2（固定、不開放 `Config` 覆寫）**：自同一 monotonic base 的絕對偏移 1／2／4／8／16／32／64／128／256／512 ms，最後於 1 s 做不送訊號的確認（rev1 的 255 ms 退避不採）。
- **D3（採擴充版 (a)）**：`CleanupIncomplete` 必須可由 production 呼叫端取得（`ports.Exit`、codex meta、assist）；預算耗盡須有 fail-loud 路徑（關閉 proc 持有的 stdout／stderr read end，讓 `Wait()`／`Done()`／`Events()` 有界收斂）；`Exit.Err` 不混入 cleanup 狀態；EPERM 不另當成功或獨立診斷欄位，而是迴圈中的非終止狀態。
- **D4（方向通過、終止條件修正）**：oracle 有界輪詢，只有 `ESRCH` 算群組消失；`EPERM` 繼續輪詢並記錄；逾 2 s 仍為 `nil`／`EPERM` 皆失敗並附快照。
- **D5（通過）**：另立 rekill 事件；既有 `…ActuallyCleaned` 測試改為「第一次 cleanup signal 成功」語意，逐事件種類斷言精確次數；退避時間表用獨立、nil-safe seam。
- **D6**：B2c-5 0.6 pt、B2c-6 0.3 pt、B2c-7 0.2 pt，合計 11 hr → 1.1 pt；順序 B2c-5 → B2c-6 → B2c-7；驗證分支 `b2c7/verify`。
- **D7（通過，完整條件）**：#7 只有在 exact implementation SHA、artifact 完整性、每條核心／escalation 測試各 400/400、零 invalid／setup／race／timeout、且 register v7 已落地後才轉 resolved；之後 B2a 才解除 blocked，rebase 後仍須重新完成 Gate A。
- **D8（通過，rev2 複核）**：強制關閉 stdout read end 時，`p.Stdout` 薄包裝**只在 supervisor 已設定強制關閉狀態後**才把底層 closed error 映射為 `proc.ErrCleanupIncomplete`（支援 `errors.Is`）；呼叫端自行 `Close()` 不得被誤標；`Done()` 語意改述為「`Exit` 已快取（EOF 或強制解除其一）」。
- **D9（不採，rev2 複核）**：`signalEvent` 契約只記錄實際成功送出的訊號；gave-up 不是訊號，`CleanupIncomplete` 與有界 `Wait()` 已足以確定性斷言；不新增 `sigEventSupervisorCleanupGaveUp`，不擴張或改名既有事件模型。
- **D10（通過，rev2 複核）**：EPERM 勘誤同分支落地——diagnosis-record §7.2／**§7.3 XNU 段落**／§7.5 (4) 與 register #7 row 補上 `P_REF_NEW`，兩份修訂記錄各加同版本勘誤條目（版本號不變），與本票同一次 docs-only 推送。
- **rev3 P1（rev2 複核提出）**：第一次 cleanup KILL 的錯誤路徑納入迴圈——狀態機已於 §3 O1 凍結，B2c-5 增加 `cleanupSignal` seam 與案例 (v)／(vi)。

---

## 修訂記錄

- rev3（2026-09-07）：rev2 複核修正——P1：第一次 cleanup KILL 的錯誤路徑納入迴圈，§3 O1 凍結完整狀態機（base 不限成功；ESRCH／nil／EPERM 三路；rekill 依回傳值；1 s 非 ESRCH 一律 `CleanupIncomplete`），B2c-5 增加 `cleanupSignal` seam 與案例 (v)／(vi)；D8 通過（映射時機、`errors.Is`、呼叫端 `Close()` 不誤標）；D9 不採（移除 gave-up 事件）；D10 通過；四處一致性（基準三份文件、「可能誤判」、Linux「本輪未觀察到相同缺口」、D10 引用「§7.3 XNU 段落」）。
- rev2（2026-09-07）：決策 gate 第一輪 CHANGES_REQUIRED 修正——P1：EPERM 不得視為終止（涵蓋 `P_REF_NEW`），O1 改為 `ESRCH` 唯一終止、`EPERM` 不送並繼續；P1：預算耗盡的 fail-loud 路徑（關閉 read end、`ErrCleanupIncomplete`、`CleanupIncomplete` 傳到 `ports.Exit`／codex meta／assist、`Exit.Err` 不混入）；D1–D7 回寫（固定絕對偏移 1–512 ms＋1 s、估點 0.6／0.3／0.2、`b2c7/verify`、#7 完整條件、B2a 重做 Gate A）；三處措辭限縮（Linux「本輪未觀察到相同逃脫」、「本輪未觀察到殺錯程序」、一層 fixture 改為相關性）；新增 D8–D10 待複核；隨附 main 兩份文件 EPERM 勘誤。
- rev1（2026-09-07）：建立；十條已確認事實、契約缺口定性、O1／O2／O3 與取捨、三張 implementation 票面與估點（合計 0.8 pt）、register #7 處置路徑、假設 A1–A6、D1–D7。
