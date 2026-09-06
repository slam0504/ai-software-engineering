# `TestOrphanDoesNotHangNormalExit` CI-only 逾時診斷記錄（B2c／B2c-2／B2c-3，權威版）

> 版本：v3（2026-09-07，B2c-7 結案時新增 §8：B2c-4 裁定、B2c-5／B2c-6 修法落地、B2c-7 CI 驗證 run `34039387868` 四 runner × 四測試各 400/400；register v7 #7 → resolved；B2a 解除 blocked；前版 v2 2026-09-07 B2c-3 結案新增 §7（含 EPERM 勘誤）、v1 2026-09-06 B2c-2 結案）
> 性質：**權威診斷記錄**。診斷 plan（B2c rev1–rev13、B2c-2 rev1 至結案版本 rev21、B2c-3 rev1 至結案版本 rev6）與探針程式只存在於診斷分支（`b2c/diag`、`b2c3/diag` 皆已刪除）；B2c-7 的一次性驗證 workflow 只存在於 `b2c7/verify`（docs 落地核對後另申請刪除）。本文件保存三輪診斷（§2–§7）與裁定、修法、CI 驗證（§8）的證據指標、結果與依證據強度收斂的結論；B2c 系列（B2c 至 B2c-7）已全部關票，register #7 已 resolved。
> 對應文件（v3 現行）：`wall-clock-test-register.md` **v7**（#7 resolved）、`pre-m4-readiness-backlog.md` **rev23**（B2c-7 關票、B2a 解除 blocked）；B2c-4 裁定記錄 `docs/superpowers/plans/2026-09-07-b2c-4-supervisor-cleanup-contract-decision.md` rev3。（v2 當時對應 register v6／backlog rev19，屬歷史。）

---

## 1. 被診斷對象

- 測試：`internal/claude/session_test.go` `TestOrphanDoesNotHangNormalExit`（5 秒 combined-completion guard，`session_test.go:207`；oracle `groupDead(pgid) = syscall.Kill(-pgid, 0) != nil`，`session_test.go:210`）。
- 路徑：`claude.Start`（`internal/claude/session.go`：JSON prompt 寫 stdin 後 Close、scanner pump 進 `events`、`Wait()`＝`p.Wait()`）→ `proc.Start`／supervisor（`internal/proc/proc.go`：`cmd.Wait()` → `close(exitedCh)` → `SignalGroup(SIGKILL)` → 成功才發 `sigEventSupervisorCleanupKill` → 等 stderr EOF → `Done`）。
- fixture：`testdata/fake-claude.sh`，`FAKE_ORPHAN=1` 時 `bash -c 'trap "" TERM; sleep 30' &`（繼承 stdout pipe）。
- 程式碼基準：`81af8f2`（`internal/proc`、`internal/claude`、`testdata` 自 `05069e2` 後未變）；governance main `5fc3373`（本文件之前）。

## 2. 兩輪證據指標

| 項目 | B2c round 1 | B2c-2 round 2 |
|---|---|---|
| 分支 head | `b2c/diag` `d12b13b` | `b2c/diag` `b4174da` |
| run（event push） | `33968040444`（2026-09-05 13:08Z） | `33982853795`（2026-09-05 18:03Z） |
| matrix | `macos-15-intel`×2、`ubuntu-latest`×2，Go 1.26.5，`-race` | 同左，`timeout-minutes: 90` |
| 層 | layer 1 既有測試 ×100；layer 2 白箱探針（`proc.Start` 直呼＋自製 `diag-orphan.sh`）×100 | layer 2b（探針只換成真實 `fake-claude.sh`）×100；layer 3 full-path（`internal/claude` 暫時性探針，維持既有測試順序＋5 秒 checkpoint 只取證）×100；layer 2 parity ×20 |
| 結果 rc | layer 1 紅（見 §3）、layer 2 全綠 | 四 job 全 success；12 個 summary `pending`＝0、`invalidEvidence`＝0 |
| artifact | `diag-<runner>-r<n>`；`layer1-test.json` SHA-256 前 16：`876f5612e09c9364`／`ae76e6e2ffb72236`／`133377869e418668`／`fd7ffb30e3a112e9`（macOS r1／r2、ubuntu r1／r2） | `diag2-<runner>-r<n>`；manifest `HASHES.v2.txt`（6196 條，不含自身，`shasum -c` 全 OK）SHA-256 `a2cb72420829fdb73639e7954ef2c76f12bd804970229491e23a1050334b276c`；舊 `HASHES.v1-with-self.txt`（含自身空檔條目，僅供揭露）SHA-256 `a41bdde3b4823cc0c8a673ddb2676370d800bcc4ae5fa8765eb236429d933084` |

探針契約（兩輪共同）：所有等待有界；`onSignal` 於 `p.mu` 內寫入；單一 stdout reader；record 只由主 goroutine 持有；`pending`（彙報期限內未收斂，有效結果）與 `invalidEvidence`（取證失敗）分開計數，任一 >0 即 rc 非零；`kill(-pgid, 0)` 保存原始 errno（nil／ESRCH／其他）且各偏移以同一 monotonic base 獨立排程；`ps` 記起訖時刻並以 `ps.start >= tCleanupKill` 判先後；真實 fixture 路徑零診斷額外 rescue 訊號（既有 supervisor cleanup KILL 照常）。

## 3. 結果

### 3.1 round 1（既有測試與自製探針的分歧）

- layer 1 既有測試 ×100：macOS r1 36 紅／r2 45 紅，皆 `session_test.go:207: drain/Wait hung on orphan-held pipes`（5.00–5.02s；pass 皆 ≤0.04s，二值分布）；ubuntu r1 98 紅／r2 99 紅，皆 `session_test.go:210: orphan must be reaped by supervisor on parent exit`（0.00–0.01s）。
- layer 2 自製探針 ×100：四 job 0 重現，cleanup KILL 100/100，EOF 亞毫秒。

### 3.2 round 2

| job | 2b eofTimeout／100 | 2b cleanup KILL | 3 checkpointHit／100 | 3 `kill0@0` nil／ESRCH | 3 `kill0@+1ms` nil | `ps` 任一偏移見 Z | parity |
|---|---|---|---|---|---|---|---|
| ubuntu r1 | 0 | 100/100 | 0 | 97／3 | 0 | 0 | 20/20，0 timeout |
| ubuntu r2 | 0 | 100/100 | 0 | 97／3 | 0 | 0 | 20/20，0 timeout |
| macOS r1 | 27 | 100/100 | 27 | 27／73 | 27 | 0 | 20/20，0 timeout |
| macOS r2 | 26 | 100/100 | 29 | 29／71 | 29 | 0 | 20/20，0 timeout |

- **macOS layer 2b**：每個 eofTimeout 輪都同時具備——cleanup KILL event 成功（`tCleanupKill` 於 exited 後約 0–60ms）**且** `ps.start >= tCleanupKill` 的取樣顯示正確 PGID 仍有 live member：`bash -c trap "" TERM; sleep 30`（ppid 1，S）與 `sleep 30`（R），無 Z；(eofTimeout, orphanLike@0) 完全對應（r1：73×(否,0)／27×(是,2)；r2：74×(否,0)／25×(是,2)／1×(是,1)）。EOF 於約 30 秒後到達（全部 finalConverged），與 `sleep 30` 結束一致。
- **macOS layer 3**：命中 checkpoint 的輪次於 5 秒時 `Events()` 未關閉、`Wait()` 未返回、`kill0` 五個偏移皆 nil、`ps` 同樣兩個 live member、無 Z；`tEventsClosedMs` 30018–30191，`tWaitReturnMs` 僅晚 0.02–0.27ms——卡的是 EOF，不是 `Wait`。重現率 27–29% 低於 round 1 layer 1 的 36–45%（`-count=100` 同進程），差異未解釋。
- **ubuntu layer 3**：`Wait()` 返回瞬間 `kill(-pgid,0)` 為 nil 97/100（兩 replica 相同），+1ms 起 100/100 ESRCH。`ps@0` 與 `kill0@0` 為並行取樣、無嚴格先後（RFC3339Nano 奈秒精度，200 筆：`psStart<kill0@0` 22、`==` 0、`>` 178；`[psStart, psEnd]` 涵蓋 `kill0@0` 22；起點差 `psStart − kill0@0` 中位數 19762ns），`ps` 皆看不到任何成員、無 Z。
- **ubuntu layer 2b**：0/100；cleanup KILL 後 `ps@0` 無 orphan 殘留、無 Z。
- **parity**：四 job 20/20 收斂、cleanup KILL 20/20、0 timeout，與 round 1 layer 2（0/400）一致，參數化未改壞 baseline。

## 4. 結論（依證據強度）

1. **真實 fixture 足以在不經 `claude.Session` 的路徑重現；Session 非必要。** fixture-level differential 成立；具體時序與 Session 是否影響重現機率仍未定位。
2. **macOS：production proc supervisor 的 `SignalGroup(SIGKILL)` 成功返回後，同一 PGID 仍有 live member，EOF 延後約 30 秒；「成員持有繼承的 stdout pipe」是 fixture 原始碼與時序高度支持的最合理解釋，無 FD 取證。** SIGKILL 成功後成員為何存活**未定位**（候選：fork／setpgid 窗口；未量測）。fixture 是觸發條件，但**不能排除 production cleanup contract／實作缺口**；責任邊界待裁定（B2c-4）。
3. **ubuntu：既有 oracle `kill(-pgid,0)` 緊接 `Wait` 返回時仍回 0（<1ms 內），為 oracle timing race 的支持性證據**；殘存者狀態（zombie 與否）未觀察，因 `ps` 取樣窗口（毫秒級）大於群組殘存時間。既有測試 98–99/100 於 0 秒失敗與此一致。
4. **不判定 production defect，亦不排除。** 不得以放寬 5 秒 guard 或加 retry 作修法。

## 5. 未定位／未驗證

- ~~macOS 成員逃過群組 KILL 的具體機制與時間線（B2c-3）。~~ → v2：機制解讀已由 B2c-3 定位（§7），KILL 送達瞬間的成員集合仍未直接觀察。
- macOS 重現率在既有測試（36–45%）與 full-path 探針（27–29%）間的差異（B2c-3 production 順序探針為 36–54%，未再追）。
- ubuntu 殘存者的狀態（B2c-3 探針的 `kill0@0` 因 `/proc` 掃描延遲不可比，未補）。
- Start 失敗與 sampler 逾界兩條 `invalidateRecord` 路徑只以讀碼確認，未以執行驗證。
- （v2 新增）B2c-3 macOS 8 個 `observedBeforeReturn` 存活者未定位；ubuntu 可控延遲層 d≤1 ms 幾乎全數 `unverified` 跳過，Linux differential 在該區間只有 3–19 輪有效。

## 6. 後續（v3 更新；v2 當時的後續已由 §8 承接）

- register #7 **resolved**（v7，B2c-7）。
- backlog：B2c-3（rev19）、B2c-4（rev20）、B2c-5（rev21）、B2c-6（rev22）、B2c-7（rev23）皆已關票；B2c 系列結束。
- **B2a 解除 blocked**：PR #1 須 rebase 到含 B2c-5／B2c-6 的 main 並重新完成 Gate A（既有 run 不沿用）。
- 診斷／驗證分支：`b2c/diag`、`b2c3/diag` 已刪除；`b2c7/verify`（一次性 workflow，不進 main）於本文件 v3／register v7／backlog rev23 推送並核對後另申請刪除。
- 修法後若 `TestOrphanDoesNotHangNormalExit` 或兩層 fork 案例在新 HEAD 再度紅，依 register 規則 3 以現行 HEAD 重現並登記為新候選，不得沿用 #7。

## 7. B2c-3：macOS 群組 KILL 後成員存活窗口定位（2026-09-07）

### 7.1 證據指標

| 項目 | 值 |
|---|---|
| 分支 head | `b2c3/diag` `5654534`（探針 `20c93ab`；plan rev6 為本機 plan-only commit） |
| run（event push） | `34018874080`（2026-09-06 07:20Z；四 job 07:20:24→07:25:35／07:27:22／07:27:40Z，全 success） |
| matrix | `macos-15-intel`×2、`ubuntu-latest`×2，Go 1.26.5，`-race`，`timeout-minutes: 60` |
| runner 版本（env.txt） | macOS 15.7.9、`kern.version` `xnu-11417.140.69.711.44~1`（**與本機完整 build 字串相同**）、`/bin/bash` 3.2.57、4 vCPU；ubuntu 24.04.4、`6.17.0-1022-azure`、bash 5.2.21 |
| 層 | layer 4：`proc.Start`＋真實 fixture、production 順序、observe-only ×100，取樣器 on／off 各一組；layer 5：`exec.Command`＋`Setpgid`、`Wait` 後注入 d∈{0, 0.25, 0.5, 1, 2, 5, 10} ms 再由探針送群組 SIGKILL（兩次 KILL 皆經身分驗證）×20 |
| 結果 rc | 12 個 step 全 0；8 份 layer 4 summary `pending`＝0（layer 5 為同步流程、無此欄位）；12 份 summary `invalidEvidenceCount`＝0 |
| artifact | `diag3-<runner>-r<n>`；manifest `HASHES.txt`（60 條，不含自身，`shasum -c` 全 OK）SHA-256 `c7574e17c2752ab9e66cfe193fe174dc9cd367dfdfa120549c1db6166a983877` |
| 本機 preflight | `/tmp/b2c3-pre.1yecIp`（1,400 輪，八項 SHA-256 經 owner 核對）：真實 fixture＋production 順序＋注入延遲 d，880 輪中 43 輪存活、43/43 不在立即 pre-KILL 快照、跨越 KILL 區間出現；43 個存活輪第二次 KILL 43/43 返回 nil、`after2Live=0`（preflight 未實作身分驗證，僅作支持性證據） |

探針契約：cleanup callback 只記 monotonic 時間（`tCleanupKillReturned`＝`SignalGroup` **成功返回後**的時間，非送達時間）並非阻塞通知；固定偏移 0／1ms／10ms／100ms／300ms／1s 的快照＋`kill(-pgid, 0)` 自該時間獨立排程、不依賴 `Done`；取樣器 Darwin 200 µs（`kern.proc.pgrp`）／Linux 1 ms（`/proc`）只記成員集合變化；存活者分類 `observedBeforeReturn`／`firstObservedAfterReturn`／`straddledOrUnknown`（相對 callback 時間戳，不推到送達時點）；layer 5 每次 KILL 前以 `(pid, starttime)`＋錨定後連續快照驗明目標，無可驗明 live member 即跳過並計 `skippedNoVerifiedTarget`；逾時輪交背景、整批單一 60 s 期限彙報。

### 7.2 結果

| job | layer 4 on ×100 存活輪 | 分類 after／before／straddled | layer 4 off ×100 | layer 5 存活輪 | layer 5 `unverified` | layer 5 `after2Live` |
|---|---|---|---|---|---|---|
| macOS r1 | 46（pair 46） | 74／4／14 | 36（分類固定 straddled） | 53（0ms 16、0.25 11、0.5 20、1 5、5ms 1 single） | 0 | 0 |
| macOS r2 | 54（pair 52、single 2） | 84／4／18 | 41 | 56（0ms 12、0.25 20、0.5 13、1 6、10ms 5 single） | 0 | 0 |
| ubuntu r1 | 0 | — | 0 | 0 有效（38 輪 `unverified` 跳過） | 38 | 0 |
| ubuntu r2 | 0 | — | 0 | 0 有效（43 輪 `unverified` 跳過） | 43 | 0 |

- **macOS layer 4**：存活形狀以 `bash`（ppid 1，run）＋`sleep`（run）pair 為主，另有 2 輪單獨 `sleep`（ppid 1）；存活輪的群組於 `tCleanupKillReturned` 後 **30.0–30.4 s** 消失（**直接觀察**，與 `sleep 30` 自然結束一致；B2c-2 未觀察）；非存活輪約 2 ms（on）／約 224 ms（off，250 ms poller 粒度）內消失。198 個存活者中 158 個（80%）`firstObservedAfterReturn`（首次觀察起點晚於 callback 45 µs–73 ms）、**8 個 `observedBeforeReturn`（首次觀察區間結束於 callback 前 20 µs–2.8 ms，分開列示、未定位）**、32 個 `straddledOrUnknown`。取樣器 on／off 為 46／54 vs 36／41 per 100：**本輪未見 200 µs 取樣器壓低重現率**。`kill0`：存活輪六個偏移皆 nil；非存活輪 ≥1 ms 皆 ESRCH；`@0` 另有 EPERM 列（同時點快照仍列出 1–3 個非 zombie 成員；v2 勘誤：EPERM 只代表走訪時沒有可取得 ref 的成員，可能是 zombie、exit transition 或**建立中的 `P_REF_NEW` 成員**，本資料未區分）。
- **macOS layer 5**：窗口曲線 d＝0–1 ms 為主，2 ms 為 0，5／10 ms 各有單獨 `sleep`（C1 fork C2 窗口）；`unverified` 0；**經身分驗證的第二次群組 KILL 於 109／109 存活輪清除**（`after2Live` 0）。
- **ubuntu layer 4**：0／400；`kill0` 六個偏移 400/400 ESRCH（`@0` 在 `/proc` 掃描之後呼叫，不可與 B2c-2 的 `Wait` 返回瞬間比較）。
- **ubuntu layer 5**：38／43 輪於 +300 ms 有非 zombie 成員，但**全部**是第一次 KILL 因 1 ms 取樣序列尚未收錄 S1／C1 而 `unverified` 跳過（fail-safe 依設計運作），**非逃脫、不得歸入 fork 窗口**；第一次 KILL 實際送出的 199 輪（r1 102、r2 97）0 存活。

### 7.3 原始碼對照（D3；一致性對照，不作已證）

版本：本機與 runner 的完整 kernel build 字串相同（`xnu-11417.140.69.711.44~1`）；對照來源為公開 tag `xnu-11417.140.69`（commit `43a90889846e00bfb5cf1d255cdc0a701a1e05a4`），**僅為前綴相符的最接近公開版本**，不能證明 `.711.44` build 與 tag 原始碼等價；Linux 為 v6.17（commit `e5f0a698b34ed76002dc5cff3804a61c80233a7a`）`kernel/fork.c`，runner 為 `6.17.0-1022-azure`（含發行版補丁）。

- **XNU（與觀察一致）**：`bsd/kern/kern_fork.c:1012` `forkproc()` 在 `proc_list_lock` 內 `pgrp_enter_locked` → `kern_proc.c:2364 pgrp_add_member` `LIST_INSERT_HEAD(&pgrp->pg_members, …)`——子程序在建立早期已在 pg_members，此時 `p_stat = SIDL`（`:1027`）、`proc_signalstart(child_proc, 0)`（`:1151`）；建立完成點 `kern_proc.c:2542` `pinsertchild` 清除 `P_REF_NEW`。`kern_sig.c:1669 killpg1` → `:1703 pgrp_iterate`（`kern_proc.c:4037`：鎖內收集 pid 快照，解鎖後逐 pid `proc_find`，`if (!p) continue;` 靜默略過）；`kern_proc.c:2162 proc_find` → `proc_ref_try_fast`（`:620–631`，`os_ref_retain_try_mask(…, P_REF_NEW | P_REF_DEAD, NULL)`，「unless it is in flux (being made, or dead)」）——**`P_REF_NEW` 未清除的建立中子程序取 ref 失敗 → 不被 `psignal`**；其他成員使 `nfound>0`（`:1713`），`kill(-pgid, SIGKILL)` 仍回 0。fork 路徑無 fatal signal 中止或延後補送機制。副觀察對照：`kern_sysctl.c:917` `kern.proc.pgrp` 走 `proc_iterate` → `kern_proc.c:3828` 略過 `SIDL`（「ignore processes that are being forked」），取樣器與 `killpg1` 一樣看不到建立中的成員；`killpg1` 過濾 SZOMB、`kill()` 走 posix 路徑 → 走訪時沒有任何可取得 ref 的成員即回 **EPERM**：只剩 zombie、成員皆在 exit transition（`P_REF_DEAD`），**或成員仍在建立中（`P_REF_NEW`，即本輪的建立窗口）**三種情況皆然（v2 勘誤，B2c-4）；因此 **EPERM 不能代表群組已消失或已終止**。
- **Linux v6.17 `copy_process`**：`:1986–2000` fork 期間送達的多程序訊號被收集延後（`multiprocess`／`delayed`）、`task_sigpending` → `-ERESTARTNOINTR`；`:2321` `tasklist_lock` 內 `:2353` `fatal_signal_pending` → 中止 fork；`:2381` `shared_pending.signal = delayed.signal` 補給子程序；`:2393` 才 `attach_pid(p, PIDTYPE_PGID)`。與 ubuntu 0 存活一致。

### 7.4 結論（依證據強度）

1. **已在 CI 觀察**（macOS 15.7.9、production 順序、observe-only、×400）：`SignalGroup(SIGKILL)` 成功返回後 36–54% 輪次同一 PGID 仍有 live member（以 `bash`＋`sleep` pair 為主，另有 2 輪單獨 `sleep`），80% 存活者首次於 KILL 返回後才被觀察到，群組於約 30 s 後（`sleep 30` 自然結束）消失；8 個 `observedBeforeReturn` 與 32 個 `straddledOrUnknown` 分開列示、未定位。
2. **機制解讀（本機時間線＋原始碼一致性，非直接觀察；目前最有證據支持的解讀）**：fixture 第 16 行 `[ -n … ] && bash -c '…' &` 使子 shell S1 在 leader 退出後約 1 ms 才 fork C1；XNU 對建立中（`P_REF_NEW`／`SIDL`）的成員 `proc_find` 失敗、`killpg1` 靜默略過且不延後補送，C1 完成建立後以相同 PGID 存活並 fork `sleep`。**候選觸發條件**：成員在 KILL 送達時正處於建立中；送達瞬間仍未直接觀察。
3. **限制**：兩平台皆無法取得 KILL 送達瞬間的成員集合；`P_REF_NEW` 未被直接觀察；8 個 `observedBeforeReturn` 無法排除「已是成員仍被略過」。
4. 可控延遲層在 CI 重現窗口曲線（d＝0–1 ms），且**經身分驗證的第二次群組 KILL 在本探針條件下 109／109 清除**；Linux production 順序 0／400、實際送出第一次 KILL 的 199 輪 0 存活。
5. **不判定 production defect 亦不排除**（責任邊界屬 B2c-4）；不得以放寬 5 秒 guard 或加 retry 作修法。

### 7.5 供 B2c-4 的事實清單

1. macOS 上 production supervisor 的單次 `kill(-pgid, SIGKILL)` 成功返回**不蘊含**群組成員全數終止：CI ×400 有 36–54% 輪次留下以 `bash`＋`sleep` pair 為主的存活者，持有繼承的 stdout pipe 直到 `sleep 30` 自然結束（消失時刻已直接觀察）。
2. 逃脫者繼承 PGID；經身分驗證的第二次群組 KILL 在 CI 109／109、本機 preflight 43／43（未驗證身分）清除——「有界重送直到群組消失」在**本探針條件下可行**，能否作為 production 修法待 B2c-4 裁定；任何重送都需要身分驗證（PGID 重用）與「群組消失」判準（見 4）。
3. **候選觸發條件（機制解讀，送達瞬間未直接觀察）**：成員在 KILL 送達時正處於建立中（fixture 於 leader 退出前約 1 ms 才 fork 的兩層 orphan 鏈）；XNU `killpg1` 對 `P_REF_NEW` 成員 `proc_find` 失敗而靜默略過且無延後補送；production Claude CLI 是否會在退出前瞬間 fork 子程序**未知**，屬契約層面問題。
4. 兩平台 `kill(-pgid, 0)` 語意不同：macOS 在走訪時沒有可取得 ref 的成員即回 **EPERM**——只剩 zombie、成員皆在 exit transition，**或成員仍在建立中（`P_REF_NEW`）**（v2 勘誤；CI `kill0@0` 8–47/100、本機 89/89 未區分三者），故 EPERM 不能當作群組已消失；Linux 於 B2c-2 `Wait` 返回瞬間回 0；既有 oracle `groupDead = err != nil` 在 macOS 會把 EPERM 當「已消失」、在 Linux 會把 zombie 殘留當「存在」——任何以 `kill(-pgid,0)` 為終止判準的修法都要明定 EPERM／ESRCH／0 三種結果的語意。
5. Linux（`6.17.0-1022-azure`）：production 順序 0／400，可控延遲層實際送出第一次 KILL 的 199 輪 0 存活；v6.17 `copy_process` 有中止 fork／補送機制。
6. 窗口位置：CI macOS d＝0–1 ms 為主、5／10 ms 另有 C1 fork C2 的單獨 `sleep`；本機 0.25–1.75 ms 與 4–7 ms。
7. 取樣器 on／off 重現率 46／54 vs 36／41 per 100，本輪未見 200 µs 取樣器壓低重現率。
8. 本輪未處理 ubuntu 的 oracle timing race（B2c-2 `kill0=nil` 97/100 於 `Wait` 返回瞬間），本探針的 `kill0@0` 因 `/proc` 掃描延遲不可比。

## 8. B2c-4～B2c-7：裁定、修法與 CI 驗證（2026-09-07）

### 8.1 B2c-4 裁定（決策票；裁定記錄 `docs/superpowers/plans/2026-09-07-b2c-4-supervisor-cleanup-contract-decision.md` rev3，owner APPROVED）

- **契約**：supervisor 提供**有界清理**——子程序退出後送 group SIGKILL，以第一次 KILL 返回時刻（不限成功）為 base，於絕對偏移 1／2／4／8／16／32／64／128／256／512 ms 探測 `kill(-pgid, 0)`（`nil` 即重送、`ESRCH` 完成、`EPERM`／其他 errno 不送但繼續），1 s 做不送訊號的最終確認，非 `ESRCH` 一律 `Exit.CleanupIncomplete=true` 並強制解除本端 stdout／stderr 等待（`Wait()`／`Done()`／`Events()` 有界收斂）；`CleanupIncomplete` 傳到 `ports.Exit`、codex `recorder.Meta`、assist；`Exit.Err` 不混入；不再宣稱單次或有限次 KILL 絕對保證群組終止。
- **形狀歸屬**：macOS 形狀＝production 契約缺口（非 fixture 特有、非 oracle 誤紅）→ B2c-5；ubuntu 形狀＝測試 oracle 取樣（zombie 殘留）→ B2c-6。兩者皆不放寬 5 秒 guard、不加 retry 於被測路徑。
- **EPERM 語意**（本文件 v2 勘誤）：macOS 在走訪時沒有可取得 ref 的成員即回 EPERM——zombie、exit transition、**建立中（`P_REF_NEW`）**皆然，不能當作群組已消失；只有 `ESRCH` 代表群組確定不存在。

### 8.2 修法落地

| 票 | main commit | 內容 |
|---|---|---|
| B2c-5 | `b2efb1c`（proc 核心）、`b9c74e8`（揭露傳遞）；main `61c2201` | `internal/proc` `cleanupGroup` 狀態機、nil-safe seam `cleanupAfter`／`groupProbe`／`cleanupSignal`、`sigEventSupervisorCleanupRekill`、`Exit.CleanupIncomplete`、`ErrCleanupIncomplete`、`stdoutReader` 薄包裝（只在強制關閉後映射）、強制關閉；`ports.Exit.CleanupIncomplete`、claude `pump`／`toPortsExit`、codex `recorder.Meta.cleanup_incomplete`、assist `finishRun`、app.go wire log meta 與 payload；白箱六案＋errno subcases＋barrier 案例；新增兩層 fork 真實程序測試 `TestSupervisorCleanupTwoLayerForkOrphan`。mutation：Sonnet 五條＋owner 兩條＋主 agent 兩條各自紅。 |
| B2c-6 | `1b5e54c`（proc）、`dad85cf`（claude／codex／evidence）；main `852c287` | 四套件 oracle 改為 `pollGroupGone(probe, deadline, now, sleep)` 有界輪詢：只有 `ESRCH` 算 gone、`EPERM` 計數續跑、2 s deadline、退避 1→64 ms、失敗附 `ps -Ao` 篩選快照；`proc_cleanup_test.go` 不動。mutation「EPERM 視為 gone」→ 對應案例紅。 |

### 8.3 B2c-7 CI 驗證（run `34039387868`）

| 項目 | 值 |
|---|---|
| 驗證分支 | `b2c7/verify` head `a679fcd66adc1190cc3076f155a42a5f8440a57c`（一次性 workflow `verify-b2c7.yml`＋docs；不進 main） |
| implementation base | `852c28730139041732d02f639f55a18196c77775`（每 job 以 `git merge-base --is-ancestor` 驗證，記入 artifact） |
| run | `34039387868`，event push，2026-09-06 14:30:46–14:33:51Z，四 job 全 success，未 rerun、未 dispatch |
| matrix | `macos-15-intel`×2、`ubuntu-latest`×2，Go 1.26.5，`-race`，`timeout-minutes: 60` |
| 測試 | 每 job 三個 `-count=100 -json` step：`TestOrphanDoesNotHangNormalExit`（claude）、`TestSupervisorCleanupTwoLayerForkOrphan`（proc）、`TestCtxCancelKillsWholeGroup`＋`TestTerminateEscalatesToGroupKill`（proc escalation） |
| 結果 | 四條測試於四 runner 各 100/100 terminal pass（合計各 **400/400**）；三個 step rc 皆 0；JSON 無 `DATA RACE`／`panic`／timeout／套件級 FAIL；最長單次 0.24 s |
| runner | macOS 15.7.9 `xnu-11417.140.69.711.44`（與 B2c-2／B2c-3 重現時相同 kernel）、`/bin/bash` 3.2.57、4 vCPU；ubuntu 24.04.4 `6.17.0-1022-azure`、bash 5.2.21 |
| artifact | `b2c7-<runner>-r<n>`（各 12 檔）；每 job `SHA256SUMS.txt` 11 條 `shasum -c` 全 OK；總 manifest 48 條 SHA-256 `79ea4c880b5c37434e6303e02bcc804267a50936c496eff51d3e93efcb17e85c` |

對照修法前同 kernel：B2c round 1 既有測試 macOS 36–45%、ubuntu 98–99% 紅；B2c-3 production 順序探針 macOS 36–54% 存活。修法後 0/400。

### 8.4 結論與限制

1. D7 完整條件全部成立（exact implementation SHA、artifact 完整性、每條核心／escalation 測試各 400/400、零 invalid／setup／race／timeout、register v7 落地）→ #7 **resolved**。
2. **限制**：CI 未記錄 rekill 次數，也未觀察到 `CleanupIncomplete` 曾為 true（測試只斷言結果；兩層 fork 案例斷言 `CleanupIncomplete=false`）；修法生效的直接證據為 B2c-5 白箱測試（seam 注入六案＋mutation）與本次跨平台 0/400 紅。escalation 路徑窗口（B2c-4 假設 A6）以 ×400 未紅作經驗支持，未單獨量測。
3. 不得把本次綠燈外推為「所有 fork 時序皆已涵蓋」；修法後若再紅，依 register 規則 3 登記新候選。

## 修訂記錄

- v3（2026-09-07）：新增 §8（B2c-4 裁定、B2c-5／B2c-6 落地、B2c-7 CI 驗證與限制）；§6 更新為 v3 後續；標題版本更新。
- v2 勘誤（2026-09-07，B2c-4 決策 gate）：§7.2／§7.3 XNU 段落／§7.5 (4) 的 EPERM 語意補正——除 zombie／`P_REF_DEAD` 外，建立中的 `P_REF_NEW` 成員同樣使 `proc_find` 失敗而回 EPERM；EPERM 不能代表群組已消失或已終止。版本號不變。
- v2（2026-09-07）：新增 §7 B2c-3（證據指標、結果、D3 原始碼對照、結論、B2c-4 事實清單）；§5 更新（機制解讀已定位、新增未定位項）；§6 更新（register v6、backlog rev19、B2c-4 依賴成立、`b2c/diag` 已刪除、`b2c3/diag` 待刪）；標題與對應文件版本更新。
- v1（2026-09-06）：建立（結案複審後修正「持有 stdout pipe」為推論措辭）；彙整 B2c round 1 與 B2c-2 round 2 證據、結果、結論與後續。
