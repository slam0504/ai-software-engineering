# A3 文件版本標記同步：收尾紀錄

> 2026-10-10：A3 的驗收三項都有證據。前兩項已由既有 commit 完成；第三項（同類掃描）由本紀錄補齊，掃描中找到一處 living 文件不一致（backlog 的修訂記錄停在 rev75），已在同一個 PR 補上。**frozen 快照的內容都沒有修改**。

## 0. 範圍

- 只涵蓋 backlog A3 的驗收條件：
  1. header 版本與最新修訂同步；
  2. 明確標注 frozen／living 的區分原則；
  3. 對 `docs/architecture/` 其他規劃文件做同類檢查。
- frozen 快照的內容與 digest 都維持原樣；living 文件只補 backlog 的修訂記錄（見第 2 節）。

## 1. 驗收條件與證據

| 驗收 | 證據 | 狀態 |
|---|---|---|
| (1) app-plan 的 header 與最新修訂同步 | commit `26de25d`（2026-08-27，`docs(a3)`）：header 由 v1.11 改為 v1.14，修訂記錄新增 `### v1.14`，註明來源為 Pre-M4 backlog A3 | 完成 |
| (2) frozen／living 區分原則 | 同一個 commit 在 app-plan 的 header 新增「版本原則」：header 版本＝修訂記錄最新版（living）；核可綁定採 frozen 語意（digest 快照不隨 living 修訂移動）。另有 commit `66a7110`（2026-09-06，B2a D3）把 `docs/architecture/SHA256SUMS` 收斂為三份里程碑 plan，治理文件、app-plan 與 reference 都是 living，不列入凍結清單 | 完成 |
| (3) 同類檢查 | 見第 2 節（2026-10-10，掃描基準 main `985b60d`；frozen 與其他 living 文件在 rev80 中都沒有變動） | 本紀錄補齊 |

## 2. 同類掃描（`docs/architecture/*.md`，共 11 份）

判定規則：
- **frozen**：列在 `docs/architecture/SHA256SUMS` 的檔案。內容就是核可當下的原文，任何修改都會破壞 digest，所以 header 或狀態行看起來過時也**不得修改**。
- **living**：其他檔案。若有 header 版本與修訂記錄，兩者必須一致。

由 repo 根目錄執行 `(cd docs/architecture && shasum -a 256 -c SHA256SUMS)`：三份全部 OK。

| 檔案 | 類型 | header 版本 | 修訂記錄最新 | 判定 |
|---|---|---|---|---|
| `sdlc-workbench-m0-plan.md` | frozen（SHA256SUMS） | v1.10 | v1.10 | digest 相符，不修改 |
| `sdlc-workbench-m1-plan.md` | frozen（SHA256SUMS） | v13 | v13 | digest 相符。狀態行寫「草案…核可前 M1 coding NO-GO」，這是核可快照的原文（commit `9564727` 為 `snapshot approved M1 plan v13 (c2c1f237…42ac)`），依 frozen 原則**不修改** |
| `sdlc-workbench-m1.5-plan.md` | frozen（SHA256SUMS） | 標題 v6 | v6 | digest 相符，不修改 |
| `sdlc-workbench-app-plan.md` | living | v1.14 | v1.14 | 一致 |
| `sdlc-ai-agent-automation-plan.md` | living | v2.5 | v2.5 | 一致 |
| `sdlc-bdd-ddd-tdd-reference.md` | living | v2.1 | v2.1 | 一致 |
| `pre-m4-readiness-backlog.md` | living | 檔頭 rev79（掃描時 main `985b60d`），之後 rev80 | `## 修訂記錄` 清單最新只到 **rev75** | **不一致**：rev76～rev79 只寫在檔頭的「前版」行，沒有進修訂記錄。本 PR 在修訂記錄補上 rev76～rev80 的條目（摘自各版檔頭，不改寫原文），並新增 rev81 條目（rev80 已於 PR #34 合併為 main `367526b`，當時沿用 rev76 起只寫檔頭的做法） |
| `ci-merge-policy.md` | living | v1.2 | v1.2（`## 修訂記錄`，第 143 行；條列式） | 一致 |
| `wall-clock-test-register.md` | living | v12 | v12（`## 修訂記錄`，第 148 行；條列式） | 一致 |
| `orphan-timeout-diagnosis-record.md` | living（診斷紀錄） | v3 | v3（`## 修訂記錄`，第 167 行；條列式） | 一致 |
| `internals.md` | living | 沒有版本 | 沒有 | 不適用 |

結論：frozen 快照的 digest 全部相符，不修改。living 文件中只有 backlog 的修訂記錄落後檔頭（rev75 對 rev79／rev80），由本 PR 補齊；其餘有 header 版本與修訂記錄的文件，兩者都一致。

## 3. 限制

- 掃描範圍只到 `docs/architecture/*.md` 這一層，不含 `docs/architecture/evidence/` 底下的紀錄，也不含 `docs/superpowers/` 的 spec 與 plan（A3 的驗收文字限定為 `docs/architecture/` 的規劃文件）。
- 「header 版本」與「修訂記錄最新」的判讀方式：先抽出 header 與修訂記錄中的版本；同時涵蓋標題式（`### v…`）與條列式（`- v…`）記錄，再依版本數字與修訂順序逐檔核對。上表是人工核對後的結果。
