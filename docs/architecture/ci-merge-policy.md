# CI 合併政策（`main` ruleset、required checks、維護程序）

> 版本：v1（2026-09-07，B2b 落地：ruleset `22394412` 建立、repo 改為 rebase-only、enforcement 三狀態實證完成；plan `docs/superpowers/plans/2026-09-08-b2b-ruleset-enforcement.md`）
> 性質：**living 文件**。任何 ruleset 或 repo 合併設定的變更都必須在本文件留下「變更前後完整 JSON」與授權紀錄；設定本身在 GitHub，本文件是它的權威說明與稽核依據。
> 讀者：對本 repo 有 push 或 admin 權限的人、以及代為操作 GitHub 設定的 agent。

---

## 1. 保護對象與現行設定

| 項目 | 值 |
|---|---|
| 受保護分支 | `refs/heads/main`（僅此一條） |
| ruleset | name `main-required-checks`、id **22394412**、`target: branch`、`enforcement: active`、`bypass_actors: []` |
| 建立時間 | 2026-09-07T00:59:05+08:00（`POST repos/slam0504/ai-software-engineering/rulesets`，owner 逐次授權） |
| 規則 | `deletion`（禁刪除）、`non_fast_forward`（禁 force-push）、`required_linear_history`（線性歷史）、`pull_request`（必須經 PR，0 個必要核准）、`required_status_checks`（strict，四個 required contexts） |
| repo 合併方式 | `allow_merge_commit: false`、`allow_squash_merge: false`、`allow_rebase_merge: true`（2026-09-07 PATCH）；`allow_auto_merge: false`、`delete_branch_on_merge: false` 維持不變 |

**效果**：`main` 上的每一個變更（包含只改文件的變更）都必須經由 PR，且 PR 的 head SHA 上四個 required contexts 全部成功、分支與 `main` 同步（strict）後，才能以 rebase 合併。合併後刪除分支仍逐次授權（不自動刪）。

**設定證據與阻擋實證分開**：ruleset 存在與有效規則以 `GET rulesets/22394412` 與 `GET rules/branches/main` 為證（附錄 A、B）；「不可合併」以 probe PR 的 merge state 為證（§7）。**direct push 被拒絕未實測**——B2b 不對 `main` 做任何推送嘗試（dry-run 也不能證明），列為已知未實測項。

## 2. required contexts 權威清單

| context | 來源 job（`.github/workflows/ci.yml`） | GitHub App | integration_id |
|---|---|---|---|
| `go` | `go` | `github-actions` | 15368 |
| `frontend` | `frontend` | `github-actions` | 15368 |
| `wails-build` | `wails-build` | `github-actions` | 15368 |
| `checksums` | `checksums` | `github-actions` | 15368 |

來源：B2a plan rev7 於 main `ee30055`／`19422bc` 以 `GET commits/<sha>/check-runs` 抄錄的實際 check 名稱與 app（不從 job 名稱推定）；B2b 建立前於 main `1bbb47a` 再核對一次相同。context 名稱等於 job 的 `name:` 欄位；改名程序見 §6。

## 3. direct push：禁止

`main` 不接受任何直接推送，包含 admin、包含 docs-only 變更、包含 fast-forward。B2a 期間「docs-only 直接 push main」的慣例自 ruleset 啟用（2026-09-07）起終止。所有變更走 PR 加 required checks，每次約 6 至 8 分鐘（§9）。

## 4. bypass：不設 bypass actor

ruleset 的 `bypass_actors` 為空，`current_user_can_bypass: never`。

**限制要明講**：無 bypass actor 不等於 admin 無法修改設定。repo admin 仍能透過 UI 或 API 修改、停用或刪除 ruleset；這是政策約束而非技術阻擋。因此任何 ruleset 或合併設定的變更都必須：(a) 由 owner 逐次授權；(b) 保存變更前後完整 JSON（`GET rulesets/<id>` 與 `GET rules/branches/main`）；(c) 以 PR 把變更紀錄寫回本文件（版本遞增）。

## 5. 緊急例外

唯一途徑：owner 以 API 將 ruleset 暫時設為 `enforcement: disabled`，執行必要操作，**立即恢復為 `active` 並以 GET 核對**（附錄 A 的內容須完全一致）。

- 適用範圍僅限「CI 基礎設施本身故障」（例如 GitHub Actions 全面停擺導致 required checks 無法產生）；測試紅燈不是例外事由，紅燈依 §8 處理。
- 恢復失敗（GET 不符或 API 錯誤）→ **停止所有後續外部寫入並回報**，直到人工修復並重新 GET 核對為止。
- 每次例外都要在本文件留下：時間、理由、停用前後 JSON、期間執行的操作、恢復後 GET。

## 6. required-check 改名程序（不會卡住的順序）

改 job 的 `name:` 就是改 check context 名稱。順序**不得顛倒**：

1. 開 PR 讓新舊 job 並存（新 job 用新名，舊 job 保留），required checks 全綠後合併；確認 `main` 上新 context 成功出現。
2. 更新 ruleset：required 清單加入新名（核對新舊名稱與 app 皆為 15368）；保存前後 GET。
3. **先從 required 清單移除舊名**，並以 GET 確認新名仍為 required；保存前後 GET。
4. **再以另一 PR 移除舊 job**；此時該 PR 只需滿足新名，required checks 全綠後合併。

若先移除舊 job 再移除舊 required，該 PR 的新 SHA 會缺少仍被要求的舊 check，而 `main` 上先前的成功紀錄不能滿足新 SHA，PR 會永遠 BLOCKED（§7 狀態 B 就是這個形狀）。每次 ruleset 更新分別授權並保存前後 GET。

## 7. ruleset 誤刪重建

以附錄 A 的建立 payload 重新 `POST rulesets` → 記錄新 id → `GET rulesets/<新 id>` 核對 name／enforcement／conditions／bypass_actors／rules → `GET rules/branches/main` 核對有效規則含四個 contexts → 以 PR 更新本文件 §1 的 id 與版本。

## 8. 紅燈處置

required check 紅燈依 `docs/architecture/wall-clock-test-register.md` 規則 1／7／8：先分類（命中契約斷言或契約路徑卡死＝回歸，不得重跑吸收；setup／資源失效＝該次無效並揭露）。**不 rerun、不 `workflow_dispatch` 來取得綠燈**；需要新的 run 就推送真實修訂。綠燈也不是修正的通過證據（register 規則 2）。

## 9. 量測資料出處

- probe 三狀態的 PR run 從建立到最後一個 job 結束：A 397 s、B 357 s、C 389 s（§附錄 C）。這三筆只是**補充耗時**，不納入 B2b (6) 的正式 n=5 樣本。
- 正式樣本（`b2b/closure` PR 的前五次合格 pull_request run、D6 欄位）與五條具名測試的 `Elapsed` 回寫於 register v8；本文件不重複。

---

## 附錄 A：ruleset 建立 payload 與 GET 快照（2026-09-07）

建立前：`GET rulesets` → `[]`；`GET rules/branches/main` → `[]`；main `1bbb47a`。

建立 payload（SHA-256 `9d81a5caaf67f718dc1b5d69d117cb30529406d49fa86324907cc97c95f0e3c7`）：

```json
{"name":"main-required-checks","target":"branch","enforcement":"active",
 "conditions":{"ref_name":{"include":["refs/heads/main"],"exclude":[]}},
 "bypass_actors":[],
 "rules":[
   {"type":"deletion"},
   {"type":"non_fast_forward"},
   {"type":"required_linear_history"},
   {"type":"pull_request","parameters":{"required_approving_review_count":0,"dismiss_stale_reviews_on_push":false,"require_code_owner_review":false,"require_last_push_approval":false,"required_review_thread_resolution":false}},
   {"type":"required_status_checks","parameters":{"strict_required_status_checks_policy":true,"required_status_checks":[
     {"context":"go","integration_id":15368},{"context":"frontend","integration_id":15368},
     {"context":"wails-build","integration_id":15368},{"context":"checksums","integration_id":15368}]}}
 ]}
```

建立後 `GET rulesets/22394412`（逐字；GitHub 另補伺服器預設欄位 `required_reviewers`、`require_extra_approval_for_unattributed_changes`、`allowed_merge_methods`、`do_not_enforce_on_create`，payload 未指定）：

```json
{"id":22394412,"name":"main-required-checks","target":"branch","source_type":"Repository","source":"slam0504/ai-software-engineering","enforcement":"active","conditions":{"ref_name":{"exclude":[],"include":["refs/heads/main"]}},"rules":[{"type":"deletion"},{"type":"non_fast_forward"},{"type":"required_linear_history"},{"type":"pull_request","parameters":{"required_approving_review_count":0,"dismiss_stale_reviews_on_push":false,"required_reviewers":[],"require_code_owner_review":false,"require_last_push_approval":false,"required_review_thread_resolution":false,"require_extra_approval_for_unattributed_changes":true,"allowed_merge_methods":["merge","squash","rebase"]}},{"type":"required_status_checks","parameters":{"strict_required_status_checks_policy":true,"do_not_enforce_on_create":false,"required_status_checks":[{"context":"go","integration_id":15368},{"context":"frontend","integration_id":15368},{"context":"wails-build","integration_id":15368},{"context":"checksums","integration_id":15368}]}}],"node_id":"RRS_lACqUmVwb3NpdG9yec5O9dHczgFVtiw","created_at":"2026-09-07T00:59:05.977+08:00","updated_at":"2026-09-07T00:59:06.071+08:00","bypass_actors":[],"current_user_can_bypass":"never","_links":{"self":{"href":"https://api.github.com/repos/slam0504/ai-software-engineering/rulesets/22394412"},"html":{"href":"https://github.com/slam0504/ai-software-engineering/rules/22394412"}}}
```

`allowed_merge_methods` 在 ruleset 端仍列三種；repo 端（附錄 B）已關閉 merge 與 squash，兩者共同適用，實際只剩 rebase。owner 裁定不另改 ruleset 的這個欄位。

## 附錄 B：有效規則與 repo 合併設定

`GET rules/branches/main`（ruleset 建立後；PATCH repo 後再 GET 一次，內容逐位元組相同）：

```json
[{"type":"deletion","ruleset_source_type":"Repository","ruleset_source":"slam0504/ai-software-engineering","ruleset_id":22394412},{"type":"non_fast_forward","ruleset_source_type":"Repository","ruleset_source":"slam0504/ai-software-engineering","ruleset_id":22394412},{"type":"required_linear_history","ruleset_source_type":"Repository","ruleset_source":"slam0504/ai-software-engineering","ruleset_id":22394412},{"type":"pull_request","parameters":{"required_approving_review_count":0,"dismiss_stale_reviews_on_push":false,"required_reviewers":[],"require_code_owner_review":false,"require_last_push_approval":false,"required_review_thread_resolution":false,"require_extra_approval_for_unattributed_changes":true,"allowed_merge_methods":["merge","squash","rebase"]},"ruleset_source_type":"Repository","ruleset_source":"slam0504/ai-software-engineering","ruleset_id":22394412},{"type":"required_status_checks","parameters":{"strict_required_status_checks_policy":true,"do_not_enforce_on_create":false,"required_status_checks":[{"context":"go","integration_id":15368},{"context":"frontend","integration_id":15368},{"context":"wails-build","integration_id":15368},{"context":"checksums","integration_id":15368}]},"ruleset_source_type":"Repository","ruleset_source":"slam0504/ai-software-engineering","ruleset_id":22394412}]
```

repo 合併設定 `PATCH repos/slam0504/ai-software-engineering`（2026-09-07，三欄位）：

| 欄位 | PATCH 前 | PATCH 後 |
|---|---|---|
| `allow_merge_commit` | true | **false** |
| `allow_squash_merge` | true | **false** |
| `allow_rebase_merge` | true | true |
| `allow_auto_merge` | false | false（未動） |
| `delete_branch_on_merge` | false | false（未動） |

全欄位比對前後兩份 `GET repos/...`，只有前兩個欄位改變；ruleset GET 與 `main` 有效規則在 PATCH 前後相同。

## 附錄 C：enforcement 三狀態實證（PR #2，2026-09-07）

probe 分支 `ci-probe/2026-09-08-enforcement` 自 main `1bbb47a` 建立，三個 commit 沿同一線性歷史依序推送（每次推送只指定該 commit 的 SHA，各自由 owner 授權），PR #2 head 隨之更新；每個狀態各觸發一次 `pull_request` run（attempt 1，未 rerun、未 dispatch）。取證完成後 PR #2 以留言關閉（`state: CLOSED`、`mergedAt: null`），分支以 `--force-with-lease` 鎖定 C 的 SHA 刪除；三個 commit **刻意不進 main**。

| 狀態 | head | 變更（相對 base） | run | 實際 check-runs（皆 app 15368） | required 對照 | merge state（REST `mergeable_state`／GraphQL `mergeStateStatus`） |
|---|---|---|---|---|---|---|
| A | `eda9f25a59e6a0061c471ccb957a084149f93b77` | `schemas/codex/SHA256SUMS` 第 1 筆 hash 首字元 `9`→`a` | `34047792759`（failure） | `checksums` **failure**（step `shasum -a 256 -c SHA256SUMS`：`./ApplyPatchApprovalParams.json: FAILED`）；`frontend`／`go`／`wails-build` success | 四個皆出現，`checksums` 失敗 | `blocked`／`BLOCKED` |
| B | `1c41d81757388c852561b1489c9af582fa9b2353` | `SHA256SUMS` 恢復與 base 相同；`ci.yml` job key 與 `name:` 由 `checksums` 改為 `checksums-renamed` | `34065893254`（success） | `checksums-renamed`／`frontend`／`go`／`wails-build` **全 success** | required `checksums` **缺席** | `blocked`／`BLOCKED` |
| C | `b699d0bc14acf19d39ea03c6950aacc56d5b2bd4` | 恢復原 job 名；tree 與 base 相同（tree `a7dbefd137b986b050b42120cd4cec7e7cb3cbd7`，`git diff 1bbb47a b699d0b` 為空） | `34066286258`（success） | `checksums`／`frontend`／`go`／`wails-build` 全 success | 四個皆出現且成功 | `clean`／`CLEAN` |

GitHub 對缺席 required context 的實際呈現（狀態 B）：GraphQL `statusCheckRollup.state` 為 `SUCCESS`，`checksums-renamed` 的 `isRequired` 為 `false`，**沒有**為缺席的 `checksums` 產生 expected 佔位；REST `commits/<B>/status` 的 `total_count` 為 0。因此「required 是否缺席」只能以 `GET rules/branches/main` 的 required 清單對照 `GET commits/<head>/check-runs` 的實際名稱判定，不能只看 rollup 顏色。狀態 A 與 C 的 rollup 中四個 context 的 `isRequired` 皆為 `true`。

三次 run 從建立到最後一個 job 結束：A 397 s（`go` 331 s、`wails-build` 230 s、`frontend` 59 s、`checksums` 6 s）、B 357 s（`go` 298、`wails-build` 281、`frontend` 49、`checksums-renamed` 7）、C 389 s（`go` 320、`wails-build` 200、`frontend` 57、`checksums` 7）。僅供參考，不入正式樣本（§9）。

## 修訂記錄

- v1（2026-09-07）：建立。§1–§9 依 B2b plan rev3 D4 (i)–(ix)；附錄 A／B 為 ruleset 建立與 repo PATCH 的實際 JSON；附錄 C 為 PR #2 三狀態實證。
