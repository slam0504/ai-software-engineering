# B2c-4 supervisor cleanup 契約裁定與 register #7 處置（決策票，不改 code）

> 版本：rev1（2026-09-07，裁定文件草稿；待 owner 決策 gate D1–D7）
> 狀態：**待 owner 裁定**。本票不修改任何 code；產出為裁定記錄、implementation 票面與估點、register #7 處置路徑。本文件在 owner 核准後即為 B2c-4 的裁定記錄（backlog rev20 落地時引用）。
> 票源：Pre-M4 Readiness Backlog **B2c-4**（rev18 新增，決策票，**0.2 pt**＝1.5–2.5 hr，owner 2026-09-06 採用；rev19 依賴 B2c-3 已成立）
> 基準：`main`＝`origin/main`＝`a0966cc`（register v6、backlog rev19、diagnosis-record v2）；程式碼 `internal/proc/proc.go` 自 `82caf8b`（B1a-1）後未變。分支 `b2c4/decision`（本機，自 `a0966cc`）只放本文件。
> 事實來源：`docs/architecture/orphan-timeout-diagnosis-record.md` v2 §7（B2c-3）、§3–§4（B2c／B2c-2）；`internal/proc/proc.go:27-31,185-203,291-331`；呼叫端 `internal/claude/session.go:73-121`、`internal/codex/session.go:27-41`、`internal/codex/owner.go:98-230`、`internal/assist/oneshot.go:129-175`；既有 oracle `internal/proc/proc_test.go:37`、`internal/claude/session_test.go:180`、`internal/codex/session_test.go:84`、`internal/evidence/runner_test.go:106`。

---

## 1. 已確認事實（逐條可追溯）

1. **契約文字**（`proc.go:27-29`）：「子程序一退出即 group SIGKILL（清掉持有 pipe 的孫程序 → reader 的 EOF 保證到來）→ 收完 stderr → 快取 Exit」。supervisor 實作（`proc.go:185-203`）：`cmd.Wait()` → `close(exitedCh)` → **一次** `SignalGroup(SIGKILL)` → 成功才發 `sigEventSupervisorCleanupKill` → `wg.Wait()`（stderr EOF）→ 快取 Exit → `close(doneCh)`。
2. **macOS 上這個保證不成立**（diagnosis-record v2 §7.4 (1)，已在 CI 觀察）：production 順序 ×400 有 36–54% 輪次在 `SignalGroup(SIGKILL)` 成功返回後仍有 live member（以 `bash`＋`sleep` pair 為主），群組於約 30 s 後（`sleep 30` 自然結束）才消失。機制解讀（§7.3／§7.4 (2)，原始碼一致性、送達瞬間未直接觀察）：KILL 送達時正處於建立中的成員被 XNU `killpg1` 靜默略過且不延後補送。
3. **今天的實際影響**：`Done()`／`Wait()` 本來就要等 stderr EOF（`proc.go:195`），EOF 又被逃脫者持有的 pipe 卡住，所以失敗形狀的效果是**收尾延遲到逃脫者自然結束**——fixture 是 30 s；若真實 CLI 在退出前 fork 的 helper 長駐並持有 stdout／stderr，`Done()` 會無限期不關。呼叫端受影響面：`claude.Session.Wait()`／`Events()` 關閉（`session.go:109-119`）、`assist` one-shot 的 `p.Wait()`（`oneshot.go:166`）、codex `GenerationOwner.Done()` 死亡 reaper（`owner.go:101-106,208-230`）與 `FinalizeWith` 的 `Server.Wait()`（`owner.go:156`）。
4. **`Terminate()`／ctx 取消路徑最終也靠同一次 cleanup KILL**：`Terminate` 送 group TERM、grace 後 group KILL（`proc.go:291-331`），leader 死後 supervisor 再送一次 cleanup KILL；孫程序若在任一 KILL 送達時正在建立中，同樣可能逃脫。B2c-3 只量測了 cleanup KILL，escalation KILL 未量測（假設 A6）。
5. **經身分驗證的第二次群組 KILL 在探針條件下 109／109 清除**（§7.2、§7.5 (2)）：逃脫者繼承 PGID，+300 ms 後重送即清除；本機 preflight 43／43（未驗證身分）。
6. **Linux 免疫**：production 順序 0／400；v6.17 `copy_process` 在 fork 中收到 fatal signal 會中止 fork、或把多程序訊號補給子程序（§7.3）。
7. **兩平台 `kill(-pgid, 0)` 語意不同**（§7.5 (4)）：macOS 只剩 zombie 或成員皆在 exit transition 的群組回 **EPERM**（CI `kill0@0` 8–47/100、本機 89/89）；Linux 在 `Wait()` 返回瞬間回 **0**（B2c-2：97/100，+1 ms 起 100% ESRCH，殘存者狀態未觀察、zombie 為最合理解讀）。既有四個 oracle（`groupGone`／`groupDead`／`srvGroupDead`／evidence `groupGone`）都是一次性 `syscall.Kill(-pgid, 0) != nil`：macOS 把 EPERM 當「已消失」（碰巧正確）、Linux 把 zombie 殘留當「仍存在」（B2c round 1 ubuntu 98–99/100 紅的來源）。
8. **B2c-3 探針的 `kill0@0` 因 `/proc` 掃描延遲不可與 B2c-2 比較**，ubuntu oracle race 只有 B2c-2 的支持性證據，殘存者狀態未直接觀察。
9. **既有 proc 測試的 orphan fixture 只有一層 fork**（`proc_test.go:40` `bash -c 'trap "" TERM; sleep 30' & …`），與本機 preflight 實驗 B（0／140）同結構，故從未在 CI 命中窗口；`fake-claude.sh:16` 的 `[ -n … ] && … &` 多一層子 shell 才進窗口（§7.4 (2)）。
10. **B2a 現況**：PR #1（`b2a/ci-workflows` `ffcd161`）Gate A 停止，禁止合併、禁止 main-run、不再重跑；main 上沒有 CI workflow。

---

## 2. 契約缺口的定性

- **屬性**：production supervisor 的 cleanup 契約在 macOS 上有一個由 kernel 語意造成的缺口——「一次 group SIGKILL 成功返回」不蘊含「群組成員全數終止」，因而不蘊含「EOF 保證到來」。這不是測試 oracle 的誤紅，也不是 fixture 特有：任何在退出前瞬間 fork 的子程序都可能觸發，真實 CLI 是否如此**未知**（假設 A1），但契約不應依賴這個未知。
- **嚴重度**：不會造成資料錯誤或殺錯程序，影響是 `Done()`／`Wait()` 的收尾延遲（上限＝逃脫者壽命）與孫程序殘留。對 codex 長駐 server 的死亡 reaper 而言，延遲會推遲 replacement；對 claude one-shot 而言，會推遲 `Events()` 關閉。
- **ubuntu 形狀**是獨立的測試側問題：oracle 在 `Wait()` 返回瞬間取樣，Linux 的 zombie 殘留讓 `kill(-pgid, 0)` 回 0。production 在 Linux 沒有缺口。

---

## 3. 方案

### O1（建議）：supervisor 有界確認迴圈——cleanup KILL 後輪詢 `kill(-pgid, 0)`，群組仍可送達就重送 KILL

- **行為**：`SignalGroup(SIGKILL)` 成功後，以同一 monotonic base 排程有界輪詢：`kill(-pgid, 0)` 回 `nil`（仍有可送達成員）→ 再 `SignalGroup(SIGKILL)`；回 `ESRCH` 或 `EPERM` → 停止（群組已無可送達成員；macOS 的 EPERM 語意見事實 7）；退避序列建議 1／2／4／8／16／32／64／128 ms（合計約 255 ms，8 次），逾預算仍 `nil` → 停止並在 `Exit` 揭露（新增欄位如 `CleanupIncomplete bool`，或 `Exit.Err` wrap 一個具名錯誤；D3 裁定），不無限等待。
- **不改變**：`Done()` 仍在 stderr EOF 後關閉；`Exit`／`Wait()`／`Terminate()`／`PGID()` 介面不變；既有 seam 事件 `sigEventSupervisorCleanupKill` 仍只在第一次成功時發一次；重送另發 **新事件** `sigEventSupervisorCleanupRekill`（每次重送一次），既有測試 `TestSupervisorCleanupKillEventFiresOnlyWhenGroupActuallyCleaned` 的「恰一個 cleanup 事件」語意不受影響（需確認其 channel 讀法，見 D5）。
- **身分保護**：目標 PGID＝leader pid，leader 已被 `cmd.Wait()` 回收；只要群組還有成員，pgrp 就存在且不會被新程序取得（新程序的 pgid 是其父的 pgid，除非自行 `setpgid`）；本 repo 自己以 `Setpgid` 起的新 Proc 其 pgid＝自身 pid，要與舊 PGID 相撞需 pid 號碼在 ≤255 ms 內回繞重用（macOS `PID_MAX` 99999 遞增、Linux `pid_max` 遞增），視為可忽略但**明列為假設 A2**；迴圈以 `ESRCH`／`EPERM` 立即停止，不對已消失的群組送任何訊號。
- **與 B2c-3 證據的對應**：CI 於 +300 ms 重送 109／109 清除；本方案首次重送在 +1 ms，逃脫者仍可能在建立中（再度被略過），故用退避多次重送，總預算涵蓋本機／CI 觀察到的窗口（0–1 ms 為主、5／10 ms 另有 C1 fork C2 窗口）。
- **劣勢／風險**：(a) 失敗形狀下 cleanup 多花 ≤255 ms（正常路徑第一次輪詢即 `ESRCH`／`EPERM`，成本一次 syscall）；(b) EPERM 視為完成——在 macOS 是正確語意，在 Linux 對自己的子程序不應出現，若出現代表權限異常，會被當作完成而不揭露（D3 可裁定 EPERM 時另記 `Exit.StderrTail` 之外的診斷欄位）；(c) Linux 上第一次輪詢多半看到 zombie 而回 `nil`，會對 zombie 重送一次 KILL（無害，+1 ms 即 `ESRCH`）；(d) 逃脫者若持續 fork（fork bomb 型），有界預算後放棄並揭露，不保證清除——與現況相同，只是多了揭露。
- **相容性**：對呼叫端透明；`Exit` 若新增欄位，`ports.Exit`（claude `session.go:128`）與 codex meta 是否要傳遞由 D3 決定（建議先不傳遞，只在 proc 層揭露＋測試可觀察）。

### O2：等群組消失才關 `Done()`（強契約）

- 在 O1 之上，把 `Done()` 改為「stderr EOF **且** `kill(-pgid, 0)` 非 `nil`」。
- **不建議**：EOF 已隱含「持有 pipe 的成員已死」，不持有 pipe 的殘留成員對呼叫端沒有可觀察影響；改 `Done()` 語意會牽動 codex reaper 與 `FinalizeWith` 順序（`owner.go:110-128` 明文凍結的順序），相容性風險高於收益。

### O3：只修測試側（fixture 不在退出前 fork／oracle 放寬），production 不改

- **不建議**：契約文字與實作在 macOS 上確實不成立（事實 2、3），且 register 規則明文「不得以放寬 5 秒 guard 或加 retry 作修法」；把 fixture 改成不觸發只會讓 CI 看不到缺口。ubuntu 側的 oracle 修正則是必要的（見 §4 B2c-6），與 O1 並行。

---

## 4. Implementation 票面（供 backlog rev20 立項；估點以 hr 為權威）

| 票 | 範圍 | 驗收條件 | 估點 |
|---|---|---|---|
| **B2c-5** proc supervisor 有界確認迴圈（O1） | `internal/proc/proc.go` supervisor：cleanup KILL 後退避輪詢＋重送、`ESRCH`／`EPERM` 停止、預算內未清除揭露；新增 seam 事件 `sigEventSupervisorCleanupRekill`；白箱測試：以 seam 注入 `groupProbe`（或等價可注入的 `kill(-pgid,0)`）模擬「前 N 次回 nil、之後 ESRCH」驗證重送次數／事件／預算耗盡揭露；真實 fixture 測試沿用 `proc_test.go` 既有 orphan 案例並新增**兩層 fork** 案例（`[ -n … ] && bash -c '…' &` 結構）作 CI 證據（本機不保證重現）；`Terminate` 升級路徑不改（假設 A6 由 B2c-7 的 CI 結果檢查） | (1) 既有 proc／claude／codex／assist 測試無 tag 全 PASS（`-race`）；(2) 白箱測試覆蓋 0 次重送、N 次重送、預算耗盡三態，mutation：移除重送 → 「N 次」案例紅；(3) `sigEventSupervisorCleanupKill` 仍恰一次；(4) 不改 `Done()` 語意、不改 `Terminate` 介面 | **0.4 pt**（3.5–4.5 hr，中位 4.0） |
| **B2c-6** 測試 oracle 有界化（兩平台語意） | 四個 oracle（`proc_test.go:37`、`session_test.go:180`、`codex/session_test.go:84`、`evidence/runner_test.go:106`）改為共用語意：`Wait()` 返回後有界輪詢（上限 2 s，間隔 1 ms 起退避）直到 `kill(-pgid, 0)` 回 `ESRCH` 或 `EPERM`；回 `nil` 到逾時 → 失敗並印出最後一次 `ps -g <pgid>` 快照；不共用 helper 檔以避免跨套件 import（每套件一份，註解指向本裁定） | (1) 本機 `-race` 各套件 PASS；(2) mutation：把上限改 0 → Linux CI 應重現 B2c round 1 的 ubuntu 形狀（可選，CI 上驗）；(3) 5 秒 combined-completion guard 與 `TestOrphanDoesNotHangNormalExit` 的斷言形狀不變（不放寬 guard、不加 retry 於被測路徑，只有 oracle 取樣有界化） | **0.2 pt**（1.5–2.5 hr，中位 2.0） |
| **B2c-7** CI 驗證與 register #7 處置 | 在診斷分支（`b2c5/verify`，含 B2c-5＋B2c-6 的 commit＋一次性 workflow）以 B2c round 1 layer 1 的方式跑既有 `TestOrphanDoesNotHangNormalExit` 與 proc 兩層 fork 案例 `-count=100`，`macos-15-intel`×2＋`ubuntu-latest`×2；一次推送、不 rerun；結果 0 紅 → register v7 #7 → **resolved**（附 B2c-5／B2c-6 commit 與 run id）、規則 8 的兩種形狀轉為歷史；仍有紅 → 逐輪分類，回到 B2c-5 | (1) 四 job 400/400 PASS；(2) escalation 路徑（`TestCtxCancelKillsWholeGroup`／`TestTerminateEscalatesToGroupKill`）同批 ×100 無紅（檢查假設 A6）；(3) register／backlog／diagnosis-record v3 回寫 | **0.2 pt**（1.5–2.5 hr，中位 2.0） |

合計 **8.0 hr → 0.8 pt**（B 軌與總計於 backlog rev20 重算：132.05＋8.0＝140.05 hr → 14.01 pt；197.05＋8.0＝205.05 hr → 20.51 pt；依 rev8 捨入規則）。

順序：B2c-5 → B2c-6（可並行，皆本機）→ B2c-7（CI）。B2a 在 B2c-7 得出 #7 resolved 後解除 blocked：PR #1 rebase 到含 B2c-5／B2c-6 的 main，重啟 Gate A。

---

## 5. register #7 處置路徑

- 現況 **unresolved**（v6）。
- B2c-7 四 job 0 紅 → **resolved**（v7），commit 欄填 B2c-5＋B2c-6 的 main commit，「最後一次負載重驗」欄填 run id 與 400/400；規則 8「兩種已登記形狀」改為歷史敘述、分類契約保留給其他 CI-only 候選。
- B2c-7 仍有紅 → 維持 unresolved，逐輪分類（形狀 (i)／(ii)／其他），回到 B2c-5 修正；不得以重跑吸收。
- **no-change disposition 不適用**：契約文字與實作已被 CI 證明不成立（事實 2）。

---

## 6. 待驗證假設

- **A1**：真實 Claude／Codex CLI 是否在退出前瞬間 fork 子程序——未知；O1 不依賴此假設。
- **A2**：pid 在 ≤255 ms 內回繞重用且新程序以該 pid 自成 process group 的機率可忽略——依 macOS／Linux pid 分配遞增推論，未量測。
- **A3**：macOS `EPERM` 語意（只剩不可送達成員）——D3 原始碼（`killpg1` 過濾 SZOMB／`proc_find` 對 `P_REF_DEAD` 失敗）＋CI／本機資料一致，未在 production 路徑直接驗證。
- **A4**：Linux zombie 由 init／subreaper 於 ≤1 ms 內回收——B2c-2 +1 ms 起 100% ESRCH，僅 ubuntu-latest 觀察。
- **A5**：退避總預算 255 ms 足以涵蓋逃脫者完成建立並可被送達的時間——CI 窗口 0–1 ms 與 5／10 ms，+300 ms 重送 109／109；255 ms 內多次重送預期足夠，由 B2c-7 ×400 驗證。
- **A6**：`Terminate` 升級 KILL 的窗口由後續 cleanup KILL 迴圈涵蓋——未單獨量測，由 B2c-7 的 escalation 測試 ×100 檢查。

---

## 7. 待 owner 裁定

- **D1**：採 O1（supervisor 有界確認迴圈）作為 production 修法方向；O2／O3 不採。
- **D2**：退避序列與預算（建議 1→128 ms 倍增、8 次、約 255 ms）；是否可由 `Config` 覆寫（建議不開放，避免成為 timeout 旋鈕）。
- **D3**：預算耗盡的揭露方式——(a) `Exit` 新增 `CleanupIncomplete bool`（proc 層可觀察，`ports.Exit` 暫不傳遞）；(b) `Exit.Err` wrap 具名錯誤；(c) 只發 seam 事件＋stderr tail 註記。建議 (a)。EPERM 是否另記診斷欄位。
- **D4**：ubuntu oracle 修法採「`Wait()` 後有界輪詢至 `ESRCH`／`EPERM`」（B2c-6），不改 fixture、不放寬 5 秒 guard。
- **D5**：seam 事件設計——重送另立 `sigEventSupervisorCleanupRekill`，`sigEventSupervisorCleanupKill` 維持恰一次；既有事件測試以「恰一個 cleanup 事件」為斷言者需確認其 channel 不會把 rekill 事件誤判為非預期（`proc_test.go:561` 的 default 分支會 Fatal，B2c-5 需一併調整為容許 rekill）。
- **D6**：三張 implementation 票的估點（0.4／0.2／0.2 pt）與順序；B2c-7 的一次推送與分支命名。
- **D7**：B2a 解除 blocked 的條件明定為「register #7 resolved（v7）」，PR #1 rebase 後重啟 Gate A。

---

## 修訂記錄

- rev1（2026-09-07）：建立；十條已確認事實、契約缺口定性、O1／O2／O3 與取捨、三張 implementation 票面與估點（合計 0.8 pt）、register #7 處置路徑、假設 A1–A6、D1–D7。
