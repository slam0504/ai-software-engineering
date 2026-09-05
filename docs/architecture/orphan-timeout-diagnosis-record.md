# `TestOrphanDoesNotHangNormalExit` CI-only 逾時診斷記錄（B2c／B2c-2，權威版）

> 版本：v1（2026-09-06，B2c-2 結案時建立；owner 結案裁定：B2c-2 關閉、register #7 維持未解決、續票 B2c-3／B2c-4、B2a 維持 blocked）
> 性質：**權威診斷記錄**。診斷 plan（B2c rev1–rev13、B2c-2 rev1 至結案版本 rev21）與探針程式只存在於診斷分支 `b2c/diag`，該分支結案後將刪除；本文件保存兩輪的證據指標、結果與依證據強度收斂的結論，供 B2c-3／B2c-4 與 register #7 後續處置引用。
> 對應文件：`wall-clock-test-register.md` v5（#7 unresolved）、`pre-m4-readiness-backlog.md` rev18（B2c／B2c-2 關票、B2c-3／B2c-4）。

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

- macOS 成員逃過群組 KILL 的具體機制與時間線（B2c-3）。
- macOS 重現率在既有測試（36–45%）與 full-path 探針（27–29%）間的差異。
- ubuntu 殘存者的狀態。
- Start 失敗與 sampler 逾界兩條 `invalidateRecord` 路徑只以讀碼確認，未以執行驗證。

## 6. 後續

- register #7 維持 **unresolved**（v5）。
- backlog **B2c-3**（macOS 存活窗口定位，spike）、**B2c-4**（supervisor cleanup 契約裁定與 #7 處置，決策票）；估點 0.4／0.2 pt，owner 2026-09-06 採用。
- **B2a 維持 blocked**。
- `b2c/diag` 分支（含六個 branch-only 檔與兩份 plan 全部修訂）於本文件與 register v5／backlog rev18 推送並驗證後，另申請授權刪除。

## 修訂記錄

- v1（2026-09-06）：建立（結案複審後修正「持有 stdout pipe」為推論措辭）；彙整 B2c round 1 與 B2c-2 round 2 證據、結果、結論與後續。
