# B2a 最小 CI（workflows、checksum、可重現說明、B3b artifact）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> 版本：rev4（2026-09-05，rev3 短複審 CHANGES_REQUIRED 後修訂：P1 Step 2b control 的 `rc=97` 改在寫 `.rc` 之前設定（原順序會讓 job exit 97 但 artifact `.rc` 為 0，owner 已重現）；P2 revert 檢查改 `git diff --exit-code <control>^ <revert> -- .github/workflows/ci.yml`；P2 `checksums` 的 diff-check step 改由 `env:` 傳入 Actions 值、shell 只讀環境變數，使同一段可在本機以 shell control 執行；前版：rev3（rev2 短複審 CHANGES_REQUIRED 後修訂：P1 三段測試 wrapper 改為 `set +e` → pipeline → 立即存 `PIPESTATUS[0]` → `set -e` → 寫 `.rc` → `exit`（GitHub `shell: bash` 實為 `bash -eo pipefail`，原寫法失敗時寫不出 `.rc`，owner 已重現）；P1 新增 Task 1 的暫時性 failure-path control；P2 final B2a HEAD 須再跑全綠並分開記錄兩次 main run、check-runs 抄 `app.id` 並以 main SHA 為權威、Gate A 事件覆蓋語意修正；前版：rev2（第一輪 owner CHANGES_REQUIRED 後修訂：D1–D6 裁定回寫（拆 B2a／B2b、`macos-15-intel`＋Go 1.26.5、SHA256SUMS 只留 m0／m1／m1.5、`ci-merge-policy.md`、0.6／0.7 pt、n=5 clean PR run attempts）；五項 P1——artifact 建置鏈閉合、README 可重現順序納入 scope、失敗時仍保存 artifact 且不假綠、checksums／`git diff --check` 可逐字執行、B2b closure PR 與外部寫入授權路徑；兩項 P2——required check context 以實際 run 讀取、量測樣本來源；前版：rev1）
> 狀態：**design gate APPROVED（rev4，2026-09-05）——Task 1 進行中；GitHub 外部寫入（push／PR／merge／刪分支）逐步取得 owner 授權；GitHub 設定面零變更**。B2b 另開 plan
> 票源：Pre-M4 Readiness Backlog **B2**（rev15，1.2 pt）→ owner 於本 plan gate 裁定拆為 **B2a 0.6 pt**（本 plan）與 **B2b 0.7 pt**（另開），合計 1.3 pt；backlog rev16 於 Task 3 落地。B2 驗收條件 (1)(4) 與 (3) 前置屬 B2a；(2)(5)(6) 屬 B2b
> 基準 commit：**`c6f8099c906f25deb43bfe0d859b1b10e0826a79`**（backlog rev15，已推送、與 `origin/main` 相同）
> 裁決：GitHub-first（backlog 裁決 #4）

**Goal（B2a）:** `.github/workflows/ci.yml` 四個 job（`frontend`／`go`／`wails-build`／`checksums`）在 PR 與 main 上全綠；artifact 建置鏈閉合（同一份 `frontend/dist` 餵 Go 與 Wails，`.app` 以 tar 保留權限供 B3b）；`docs/architecture/SHA256SUMS` 依 D3 收斂到三份里程碑 plan；README「測試」段改寫成可從乾淨 checkout 逐字執行、與 CI 逐項對應的順序（驗收 (4)）。**不建 ruleset、不做 enforcement 實證、不量測**——那些是 B2b。

**Architecture:** CI 是「已編碼政策的權威執行紀錄」（automation plan §7 D1）。B2a 只把現有驗證指令搬進 workflow 並修正 README 的執行順序；不新增測試、不改 production／測試碼。所有測試 step 都以 `bash` 執行、`tee` 落檔、保留原始 rc、`if: always()` 上傳 artifact，**上傳不得改變 job 成敗**。GitHub 設定面在 B2a **零變更**；B2a 唯一的 GitHub 外部寫入是 push 分支、開 PR、合併 PR、刪遠端分支，每一步都先取得 owner 授權。

**Tech Stack:** GitHub Actions；runner：`ubuntu-latest`（frontend、checksums）、**`macos-15-intel`**（go、wails-build；x86_64 與既有本機驗證同構，D2）；`actions/checkout`／`setup-go`／`setup-node`／`upload-artifact`／`download-artifact` 以 full SHA 釘版（SHA 於 Task 1 以 `gh api repos/actions/<repo>/git/ref/tags/<tag>` 取得並抄錄）；Go **1.26.5**（D2；A4 落地 `toolchain` 後改 `go-version-file: go.mod`）；Node 26；Wails CLI v2.13.0；`shasum -a 256 -c`。

**參考文件：** backlog rev15 `### B2`／`### A3`／`### A4`／`### B3b`；automation plan §2、§7 D1、§12；README §測試、`:95`、`:107-112`、`:380`；`wall-clock-test-register.md` v2；`scripts/check-cli.sh`、`scripts/bundle-clis.sh`、`wails.json`、`main.go:22`。

---

## Preflight 事實（2026-09-05，HEAD `c6f8099`，唯讀；rev1 蒐集，rev2 不變）

- **GitHub**：repo public、owner User、token admin；`rulesets` `[]`；main protection 404；Actions enabled、`allowed_actions: all`、`sha_pinning_required: false`；PR 歷史 0。owner 於複審核實：public repo 的 GitHub-hosted 標準 runner 免費且不限分鐘；`macos-latest` 目前指向 arm64。
- **建置相依**：`main.go:22` `//go:embed all:frontend/dist`，dist 被 ignore，無 dist 時 `go vet .`／`go test .` 失敗；`wails.json` `frontend:install`=`npm install`、`frontend:build`=`npm run build`（`wails build -s` 可略過 frontend 建置、消費既有 dist）；`frontend/package-lock.json` 存在、無 `engines`／`.nvmrc`；`go.mod` `go 1.25.0` 無 `toolchain`，本機 1.27.0，validated 1.26.5；`-race` 需 cgo、root 含 Wails；darwin-only 測試 2 條；pinned CLI 需 `npm ci --prefix tools/<x>` 後 `check-cli.sh`。
- **checksum**：`schemas/codex/SHA256SUMS` 275 筆全 OK（於 `schemas/codex` 內執行）；`docs/architecture/SHA256SUMS` 6 筆，`sdlc-ai-agent-automation-plan.md` FAILED（v2.2–v2.4 後未重算）；**兩者都只含 basename，必須 `cd` 進該目錄才能 `-c`**（owner 實測 repo root 執行為 6 筆 No such file）。
- **README §測試（:136）現況**：先列 Go 再列 frontend、無 `npm ci`／`go build`——乾淨 checkout 照做會在 dist 未產生時失敗（驗收 (4) 目前不成立）；且仍寫「三個 package 牆鐘測試紅了先單獨重跑」——已被 register v2 規則 1／7 取代。
- **耗時基準（本機 8 核）**：root `-race` 234s、`internal/*` 約 21s、vitest 30–36s、`npm run build` 14.5s。
- **政策**：automation plan 無 bypass／緊急例外章節；§12:327 列 ruleset 啟用並驗證阻擋效力為前置。B3b 依賴 B2 的 `.app` artifact。

---

## owner 裁定紀錄（design gate 第一輪）

- **D1 拆票**：B2a ＝ workflow、checksum、可重現說明、B3b artifact（本 plan）；B2b ＝ ruleset、probe、政策、量測、關票（另開 plan，見文末「B2b 承接事項」）。
- **D2 runner／Go**：`macos-15-intel`＋Go **1.26.5**；A4 加入 `toolchain go1.26.5` 後改 `go-version-file: go.mod`（新版 setup-go 優先讀 toolchain）。
- **D3 SHA256SUMS**：只保留 **m0／m1／m1.5** 三份里程碑 plan；移除 automation plan、app-plan（A3 已宣告 living）、BDD/DDD/TDD reference。
- **D4 政策文件**：新增 `docs/architecture/ci-merge-policy.md`，automation plan §12 指向它；active ruleset **不設常駐 bypass actor**；緊急例外逐次 owner 授權、保存變更前後 JSON、理由與復原證據；admin 仍能改 ruleset，只能以政策與稽核約束。（B2b 落地）
- **D5 估點**：B2a 0.6 pt、B2b 0.7 pt，合計 1.3 pt。
- **D6 量測**：n=5，樣本來自 **ruleset 啟用後、workflow 未修改、四個 required contexts 都出現的 clean PR run attempts**；記錄 run ID、attempt、runner image、cache hit、五條 elapsed；main／dispatch 只作補充資料；紅燈依 register 分類、不得重跑吸收。（B2b 落地）
- **P2**：建 ruleset 前先從 B2a 成功的 main run 讀取實際 check context 名稱與 GitHub Actions integration（`gh api repos/.../commits/<sha>/check-runs`），不從 job 名稱推定。（B2b 落地，B2a Task 3 先抄錄一份）

---

## Production 與測試碼零變更聲明

B2a 不改任何 `.go`／`.ts`／`.vue`／`vitest.config.ts`／`go.mod`。新增 `.github/workflows/ci.yml`；修改 `docs/architecture/SHA256SUMS`（D3）、`README.md`（§測試順序與 :380 一句）、backlog rev16。**§6.7 不適用**（無 production seam）。

---

## Global Constraints

- **GitHub 外部寫入逐步授權**：push 分支、開 PR、合併 PR、刪遠端分支，每一步執行前取得 owner 明示授權；GitHub 設定面（ruleset／repo settings）B2a **一律不動**。所有 `gh` GET 可自行執行並抄錄。
- **測試 step 契約（Vitest、frontend build、Go test 三段皆同）**：`shell: bash`（GitHub 實際以 `bash -eo pipefail` 執行，pipeline 非零會在到達 `PIPESTATUS` 前就退出）；因此固定寫法為 `set +e` → `<指令> 2>&1 | tee <artifact>` → `rc=${PIPESTATUS[0]}` → `set -e` → `echo "$rc" > <artifact>.rc` → `exit "$rc"`。上傳 step 用 `if: always()`。契約是：**測試 rc 不得被 `tee` 或 upload 掩蓋**——setup、build、驗證 step 本來就能讓 job 失敗，但測試 step 的紅燈必須以原始 rc 傳出，且 `.rc` 與 log 在紅燈時仍寫出並上傳。
- **artifact 建置鏈**：`frontend` job 產生 `frontend-dist`（`frontend/dist/**`）；`go`、`wails-build` 皆 `needs: frontend`，下載到 `frontend/dist/` 後先 `test -f frontend/dist/index.html || exit 1`；Wails 用 `wails build -s` 消費同一份 dist，不再自行 `npm install`／build；`.app` 以 `tar -czf` 封裝後上傳（GitHub artifact 不保留檔案權限），Gate A 下載解開驗 `Contents/MacOS/sdlc-workbench` 可執行。
- **checksums 可逐字執行**：`(cd schemas/codex && shasum -a 256 -c SHA256SUMS)`、`(cd docs/architecture && shasum -a 256 -c SHA256SUMS)`；`git diff --check` 依事件分流（見 Task 1 Step 1 `checksums` job），`fetch-depth: 0`。
- required check 名稱為契約：job `name:` 固定 `go`／`frontend`／`wails-build`／`checksums`；實際 context 名稱以 B2a 成功 main run 的 check-runs 為準抄錄，供 B2b 建 ruleset。
- Action 以 full SHA 釘版並附 `# vX.Y.Z` 註解。每個 job `timeout-minutes`（frontend 15、checksums 10、go 45、wails-build 45，Task 1 實測後調整）；`go test` 帶 `-timeout 30m`。
- 八條具名 wall-clock 測試在 CI 紅燈時**不自動重跑**（workflow 無 retry）；分類依 register 規則 1／7 由人執行並揭露。
- 每個工具呼叫以 `cd /Users/eason_tseng/playground/project/ai-software-engineering` 開頭；`gh` 輸出逐字抄錄。

---

## Task 1（B2a）: workflow 落地並在 PR 內迭代到綠

- [ ] **Step 0 授權**：owner 授權建立分支 `b2a/ci-workflows` 並 push、開 PR。
- [ ] **Step 1 `.github/workflows/ci.yml`**（結構如下；Action SHA 於現場取得）：

  ```yaml
  name: ci
  on:
    pull_request:
    push:
      branches: [main]
    workflow_dispatch:
  concurrency:
    group: ci-${{ github.workflow }}-${{ github.ref }}
    cancel-in-progress: true
  jobs:
    frontend:
      name: frontend
      runs-on: ubuntu-latest
      timeout-minutes: 15
      steps:
        - uses: actions/checkout@<SHA> # v4.x
        - uses: actions/setup-node@<SHA> # v4.x
          with: { node-version: '26', cache: npm, cache-dependency-path: frontend/package-lock.json }
        - run: npm --prefix frontend ci
        - name: vitest
          shell: bash
          run: |
            set +e
            npm --prefix frontend run test -- --reporter=verbose 2>&1 | tee vitest.out
            rc=${PIPESTATUS[0]}
            set -e
            echo "$rc" > vitest.rc
            exit "$rc"
        - name: build (vue-tsc + vite)
          shell: bash
          run: |
            set +e
            npm --prefix frontend run build 2>&1 | tee frontend-build.out
            rc=${PIPESTATUS[0]}
            set -e
            echo "$rc" > frontend-build.rc
            exit "$rc"
        - name: verify dist
          run: test -f frontend/dist/index.html
        - uses: actions/upload-artifact@<SHA> # v4.x
          if: always()
          with:
            name: vitest-output
            if-no-files-found: error
            path: |
              vitest.out
              vitest.rc
              frontend-build.out
              frontend-build.rc
        - uses: actions/upload-artifact@<SHA> # v4.x
          with:
            name: frontend-dist
            path: frontend/dist
            if-no-files-found: error
    go:
      name: go
      needs: frontend
      runs-on: macos-15-intel
      timeout-minutes: 45
      steps:
        - uses: actions/checkout@<SHA>
        - uses: actions/setup-go@<SHA> # v5.x
          with: { go-version: '1.26.5', check-latest: false }
        - uses: actions/download-artifact@<SHA> # v4.x
          with: { name: frontend-dist, path: frontend/dist }
        - run: test -f frontend/dist/index.html
        - run: go version && test -z "$(gofmt -l .)"
        - run: go build ./...
        - run: go vet ./...
        - name: go test -race
          shell: bash
          run: |
            set +e
            go test -race ./... -count=1 -timeout 30m -json 2>&1 | tee go-test.json
            rc=${PIPESTATUS[0]}
            set -e
            echo "$rc" > go-test.rc
            exit "$rc"
        - uses: actions/upload-artifact@<SHA>
          if: always()
          with:
            name: go-test-json
            if-no-files-found: error
            path: |
              go-test.json
              go-test.rc
    wails-build:
      name: wails-build
      needs: frontend
      runs-on: macos-15-intel
      timeout-minutes: 45
      steps:
        - uses: actions/checkout@<SHA>
        - uses: actions/setup-go@<SHA>
          with: { go-version: '1.26.5', check-latest: false }
        - uses: actions/setup-node@<SHA>
          with: { node-version: '26' }
        - uses: actions/download-artifact@<SHA>
          with: { name: frontend-dist, path: frontend/dist }
        - run: test -f frontend/dist/index.html
        - run: go install github.com/wailsapp/wails/v2/cmd/wails@v2.13.0 && wails version
        - run: wails build -s          # 略過 frontend 建置，消費同一份 dist
        - run: npm --prefix tools/claude-cli ci && npm --prefix tools/codex-cli ci
        - run: scripts/check-cli.sh
        - run: scripts/bundle-clis.sh
        - run: test -x build/bin/sdlc-workbench.app/Contents/MacOS/sdlc-workbench
        - run: tar -C build/bin -czf sdlc-workbench.app.tgz sdlc-workbench.app
        - uses: actions/upload-artifact@<SHA>
          with:
            name: wails-app-tar
            path: sdlc-workbench.app.tgz
            if-no-files-found: error
    checksums:
      name: checksums
      runs-on: ubuntu-latest
      timeout-minutes: 10
      steps:
        - uses: actions/checkout@<SHA>
          with: { fetch-depth: 0 }
        - run: (cd schemas/codex && shasum -a 256 -c SHA256SUMS)
        - run: (cd docs/architecture && shasum -a 256 -c SHA256SUMS)
        - name: git diff --check（依事件分流；Actions 值經 env 傳入，shell 只讀環境變數）
          shell: bash
          env:
            EVENT_NAME: ${{ github.event_name }}
            SHA: ${{ github.sha }}
            BEFORE_SHA: ${{ github.event.before }}
            PR_BASE_SHA: ${{ github.event.pull_request.base.sha }}
            PR_HEAD_SHA: ${{ github.event.pull_request.head.sha }}
          run: |
            set -euo pipefail
            case "$EVENT_NAME" in
              pull_request)
                git diff --check "$PR_BASE_SHA" "$PR_HEAD_SHA" ;;
              push)
                if [ "$BEFORE_SHA" = "0000000000000000000000000000000000000000" ] || [ -z "$BEFORE_SHA" ]; then
                  echo "first push / new ref: no before SHA, checking HEAD commit diff (whole tree only for a root commit)"; git diff-tree -r --check --root "$SHA"
                else
                  git diff --check "$BEFORE_SHA" "$SHA"
                fi ;;
              workflow_dispatch)
                echo "manual dispatch: no PR base; checking HEAD commit diff against its parent (whole tree only for a root commit)"; git diff-tree -r --check --root "$SHA" ;;
              *) echo "unsupported event: $EVENT_NAME"; exit 1 ;;
            esac
  ```

- [ ] **Step 2 PR 內迭代到四個 job 全綠**：每輪失敗的 log 摘要與修正記入證據段（這是唯一允許「重跑到綠」的地方——修的是 workflow 本身，不是測試）。若 `checksums` 因 D3 尚未落地而紅，先做 Task 2 再回來。
- [ ] **Step 2b 暫時性 failure-path control（PR 內、只動 workflow 檔）**：在 `b2a/ci-workflows` 上以一個獨立 commit 讓 `frontend` job 的 vitest wrapper 在 log 產生後、**寫 `.rc` 之前**把 rc 覆寫為 97（control 版 wrapper 尾段固定為：

  ```sh
  rc=${PIPESTATUS[0]}
  set -e
  echo "B2A-FAILURE-PATH-CONTROL"
  rc=97
  echo "$rc" > vitest.rc
  exit "$rc"
  ```

  這樣 job exit 與 artifact 內 `vitest.rc` 一致皆為 97），push 後確認：該 run 的 `frontend` job 為 **紅**、`vitest-output` artifact 仍可下載且 `vitest.rc` 內容為 `97`、`go`／`wails-build` 因 `needs` 被 skip。記錄 control commit SHA、run ID、artifact 內 `.rc` 值；隨後以 revert commit 還原 workflow，並以 fail-loud 檢查 `git diff --exit-code "$control_sha^" "$revert_sha" -- .github/workflows/ci.yml`（rc 0 才算還原；只比對 workflow 檔，不把其間其他變更算進來），再跑正式綠燈。**不改產品碼或測試碼。** 若 Gate A 迭代期間已自然出現測試 job 紅燈且 artifact 齊全，可以該 run 代替，但仍須記錄同樣欄位。
- [ ] **Step 3 記錄**：四個 job 的耗時、runner image（`ImageOS`／`ImageVersion`）、`go version`／`node --version`／`wails version` 實際輸出、cache hit；`go-test.json` 內 #2／#3／#6 與 `vitest.out` 內 F1／F2 的 elapsed（補充資料，非 D6 樣本）。
- [ ] **Step 4 artifact 驗證**（本機）：`gh run download <run-id> -n wails-app-tar`，`tar -xzf`，`test -x sdlc-workbench.app/Contents/MacOS/sdlc-workbench`；`gh run download -n frontend-dist`、`-n go-test-json`、`-n vitest-output` 可下載且 `.rc` 皆為 0。
- [ ] **Step 5 預檢 check contexts**：對 PR head SHA 執行 `gh api repos/slam0504/ai-software-engineering/commits/<sha>/check-runs --jq '.check_runs[]|{name,app:{slug:.app.slug,id:.app.id},conclusion}'`，抄錄四個 context 名稱、app slug 與 **app id**（B2b 建 ruleset 的 `integration_id` 需要）。**此為預檢；權威版本以 Task 3 的成功 main SHA 再抄一次為準。**

## Task 2（B2a）: D3 checksum 收斂＋README 可重現順序（同一 PR，獨立 commit）

- [ ] **SHA256SUMS**：`docs/architecture/SHA256SUMS` 只留 `sdlc-workbench-m0-plan.md`／`sdlc-workbench-m1-plan.md`／`sdlc-workbench-m1.5-plan.md` 三筆（hash 不變，三筆現況 OK；以 `(cd docs/architecture && shasum -a 256 sdlc-workbench-m0-plan.md sdlc-workbench-m1-plan.md sdlc-workbench-m1.5-plan.md > SHA256SUMS)` 重產並 `-c` 驗證）。
- [ ] **README:380 一句**改為：里程碑執行計畫（m0／m1／m1.5）經外部審核後凍結於 `docs/architecture/`（`SHA256SUMS` 可驗證，`cd docs/architecture && shasum -a 256 -c SHA256SUMS`）；app-plan 與治理文件為 living 文件（版本見各自 header 與修訂記錄），不在凍結清單。
- [ ] **README §測試改寫**為可從乾淨 checkout 逐字執行、與 CI job 逐項對應的順序：

  ```sh
  npm --prefix frontend ci
  npm --prefix frontend run test      # CI job: frontend（vitest）
  npm --prefix frontend run build     # CI job: frontend（vue-tsc typecheck + vite build → frontend/dist，root 套件 go:embed 需要）
  go build ./...                      # CI job: go
  go vet ./...                        # CI job: go
  go test -race ./... -count=1        # CI job: go（所有 package，含 production path 的同步與競態測試）
  (cd schemas/codex && shasum -a 256 -c SHA256SUMS)          # CI job: checksums
  (cd docs/architecture && shasum -a 256 -c SHA256SUMS)      # CI job: checksums
  ```

  並把「三個 package 牆鐘測試紅了先單獨重跑」那段**改為**指向 `docs/architecture/wall-clock-test-register.md` 規則 1／7（八條具名測試紅燈先分類、契約回歸不得重跑吸收；其他前端測試依規則 B 段一般規則）。
- [ ] 這兩個檔案的變更各自獨立 commit；`checksums` job 在 PR 內轉綠。

## Task 3（B2a）: 合併、main run、backlog rev16

- [ ] owner 授權後合併 PR（本 repo 慣例線性 fast-forward；若 GitHub 端只提供 merge/squash/rebase，選 **rebase and merge** 保持線性並記錄）；owner 授權後刪遠端分支。
- [ ] **implementation main run**：合併後 main 上 `push` 觸發跑一次全綠；抄錄 run ID、四個 job 耗時與 check-runs（Step 5 同樣指令，對該 main SHA，含 `app.id`）——此為 B2b 建 ruleset 的權威 context 清單。
- [ ] backlog rev16：B2 拆為 B2a（已完成，0.6 pt，plan／commit）／B2b（未完成，0.7 pt，範圍 (2)(5)(6)）；合計由 1.2 → 1.3 pt，B 軌與合計以 hr 重算；A4 互動註記（CI Go 釘 1.26.5，A4 落地後改讀 go.mod）；D3 落地註記（SHA256SUMS 只留三份）；B3b 前置（`wails-app-tar` artifact）成立。backlog 變更以另一個 PR 或直接 push？——**依既有慣例 backlog 修訂為 docs commit 直接 push main；但 B2a 合併後 main 已有 CI，任何 push 都會觸發 workflow**：直接 push 仍可（B2a 未建 ruleset），owner 授權後執行。**closure-commit main run**：plan／backlog closure commit push 後，**final B2a HEAD 必須再跑四個 job 全綠**，與 implementation main run 分開記錄（run ID、SHA、耗時）；Gate A 綁定 final HEAD，不得只綁上一個 SHA。

---

## Task 1 證據（進行中，2026-09-05）

- **Action pin（`gh api` GET：`releases/latest` → `git/ref/tags/<tag>`，annotated tag 再解 `git/tags/<sha>`）**：`actions/checkout` v7.0.1 `3d3c42e5aac5ba805825da76410c181273ba90b1`；`actions/setup-node` v7.0.0 `820762786026740c76f36085b0efc47a31fe5020`；`actions/setup-go` v7.0.0 `b7ad1dad31e06c5925ef5d2fc7ad053ef454303e`；`actions/upload-artifact` v7.0.1 `043fb46d1a93c77aae656e7c1c64a875d1fc6a0a`；`actions/download-artifact` v8.0.1 `3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c`。
- 本機分支 `b2a/ci-workflows`（自 `c6f8099`）；`.github/workflows/ci.yml` 已寫入並以 YAML 解析器核對四個 job／needs。
- **Step 0**：owner 授權（綁定 HEAD `109b407`、遠端 main `c6f8099`）後 `git push -u origin b2a/ci-workflows`（新分支），**PR #1** 建立：https://github.com/slam0504/ai-software-engineering/pull/1（base main、head `109b407`）。
- **首次 PR run `33952217785`（pull_request，2026-09-05 07:19Z）——四個 job 第一次即全綠**：

  | job | runner image | 起訖（UTC） | 耗時 | 關鍵輸出 |
  |---|---|---|---|---|
  | `checksums` | ubuntu-24.04 | 07:19:17–07:19:23 | 6s | 兩份 SHA256SUMS `-c` OK；`git diff --check`（pull_request 路徑）綠 |
  | `frontend` | ubuntu-24.04 | 07:19:16–07:20:00 | 44s | node v26.8.1；vitest **40 檔 397 passed**；`vitest.rc`=0、`frontend-build.rc`=0；npm cache 首次未命中、post 已存 key `node-cache-Linux-x64-npm-9dcd67fe…` |
  | `go` | macos-15（`macos-15-intel` 標籤，`go version go1.26.5 darwin/amd64`） | 07:20:04–07:26:27 | 6m23s | gofmt 空；build／vet 綠；`go test -race -json`：root **422 pass＋1 skip**、`internal/codex` 47、`assist` 20、`claude` 22、`proc` 18、其餘套件全 pass、另 `internal/domainspec` 1 skip（env gate）；`go-test.rc`=0 |
  | `wails-build` | macos-15（同上） | 07:20:03–07:26:18 | 6m15s | Wails CLI v2.13.0；`wails build -s` Built in 1m25s；`check-cli.sh`：claude 2.1.223 sha256 `350e6574…`、codex 0.146.1 sha256 `134063e1…`、OK；`.app` tar 上傳（306.6 MB） |

  六條 Go wall-clock 測試在 CI 單跑 elapsed（補充資料，非 D6 樣本）：#1 0.06s、#2 0.61s、#3 0.05s、#4 0.03s、#5 0.06s、#6 0.34s；F1 157ms、F2 95ms。
- **artifact 本機驗證**（`gh run download 33952217785`）：`vitest-output`（`vitest.rc`／`frontend-build.rc` 皆 0）、`go-test-json`（`go-test.rc` 0，18 個 pass 計數如上）、`frontend-dist`（`index.html` 存在）、`wails-app-tar`（解開後 `Contents/MacOS/sdlc-workbench` 可執行、`Contents/Resources/tools/` 含 `claude-cli`／`codex-cli`）。
- **check-runs 預檢**（head `109b407`）：`frontend`／`go`／`wails-build`／`checksums` 四個 context，app `github-actions`，**app id 15368**，皆 success。權威版本待 Task 3 的 main run 再抄。
- **Step 2b failure-path control**：待 owner 授權推送（見下方 control commit）。

## Gate A（B2a 完成條件）
- [ ] PR 內四個 job 全綠；implementation main run 全綠；**final B2a HEAD（含 closure commit）的 main run 全綠**；各 run ID、SHA、耗時、runner image、工具版本、cache hit 記錄。
- [ ] artifact 鏈：`frontend-dist` → `go`／`wails-build` 皆 `needs: frontend` 且下載後驗 `index.html`；`wails-app-tar` 下載解開後主程式可執行；`go-test-json`／`vitest-output` 含 `.rc`＝0；failure path 已由 Step 2b control 實證：測試 job 為紅、`.rc` 為非零（97 或實際值）且 artifact 可下載，control commit 已 revert（diff 為空）。
- [ ] `checksums`：兩份 SHA256SUMS 於各自目錄 `-c` 全 OK；`git diff --check` 的 `pull_request` 分支與 `push` 分支各實跑一次並綠；`workflow_dispatch` 分支**不混入**——以本機 shell control 驗證：把 step 的 `run:` 區塊逐字抽出，以 `EVENT_NAME=workflow_dispatch SHA=<sha>`（及 `EVENT_NAME=push BEFORE_SHA=000…0 SHA=<sha>` 零 SHA 路徑）在本 repo 執行並記錄 rc；或另經 owner 授權後手動 dispatch 一次實跑並記錄。
- [ ] README §測試順序與 CI job 逐項對應；:380 一句同步；SHA256SUMS 三筆。
- [ ] check-runs 的四個 context 名稱、app slug 與 app id 已以 implementation main SHA 為權威抄錄（PR head 版本為預檢）。
- [ ] repo 變更只含 `.github/workflows/ci.yml`、`docs/architecture/SHA256SUMS`、`README.md`、本 plan、backlog；零 `.go`／`.ts`／`.vue`／config；GitHub 設定面零變更。

---

## B2b 承接事項（另開 plan 時的已裁定輸入，本 plan 不執行）

- **範圍**：(2) main ruleset（required contexts 以 B2a 抄錄的 check-runs 為準；require PR、線性歷史、禁 direct push；**不設常駐 bypass actor**）；(5a) probe PR 故意讓 `checksums` 紅→不可合併；(5a′) probe 改 job 名使 required context 缺席→永遠 pending（＝(5d) fail loud 實證）；(5b) 修正後全綠→`mergeStateStatus: CLEAN`；(5c)(5e) `docs/architecture/ci-merge-policy.md`＋automation plan §12 指向；(6) n=5 clean PR run attempts 量測 #2／#3／#6／F1／F2，回寫 register v3；backlog rev17 關票。
- **兩條分支路徑（P1-5）**：`ci-probe/<日期>-<目的>` 分支**只做 enforcement 驗證**，驗完關閉 PR、刪分支、不合併；**另設 `b2b/closure` PR** 提交政策文件、automation plan 指標、register v3、backlog rev17，required checks 全綠後才合併——Gate B 文件不得只存在工作區。
- **外部寫入授權清單**：建立／修改／刪除 ruleset；push 任一分支；開／關 PR；merge；刪遠端分支；每一步逐次授權並抄錄前後狀態（ruleset 以 JSON）。
- **D6 樣本定義**：ruleset 啟用後、workflow 檔未修改、四個 required contexts 都出現的 clean PR run attempts，n=5；記錄 run ID、attempt、runner image、cache hit、五條 elapsed；main／dispatch 為補充；紅燈依 register 分類。
- **估點** 0.7 pt（6.0–8.5 hr）。

---

## 已知缺口（誠實標註）
1. **Wails build 在 `macos-15-intel` 的可行性未驗證**（Xcode CLT、`wails build -s` 對既有 dist 的消費、`go install` 的網路）；屬 Task 1 迭代風險，估點已含。
2. **`macos-15-intel` 的排隊與耗時未實測**；Task 1 Step 3 記錄後更新 `timeout-minutes`。
3. **admin 仍可修改 ruleset**：只能由 B2b 的政策文件與稽核（前後 JSON）約束，非技術阻擋。
4. **CI 上八條 wall-clock 測試的表現**：B2a 只記補充資料，不作結論；正式量測在 B2b D6。

## 尚未完成
- design gate 待審（rev4 短複審）。Task 1–3 未執行。本 plan 未 commit。

## 修訂記錄
- rev4（2026-09-05，rev3 短複審 CHANGES_REQUIRED）：P1 Step 2b control 尾段固定為「存 `PIPESTATUS` → `set -e` → 印標記 → `rc=97` → 寫 `.rc` → `exit`」，job exit 與 artifact `.rc` 一致為 97（本機重現舊順序 artifact_rc=0、新順序 97）。P2 revert 檢查改 `git diff --exit-code <control>^ <revert> -- .github/workflows/ci.yml`。P2 `checksums` diff-check step 改為 `env:` 傳入 `EVENT_NAME`／`SHA`／`BEFORE_SHA`／`PR_BASE_SHA`／`PR_HEAD_SHA`，shell 只讀環境變數，Gate A 的 dispatch shell control 改為以環境變數驅動同一段。未新增檔案、未動 GitHub 設定、未 commit。
- rev3（2026-09-05，rev2 短複審 CHANGES_REQUIRED）：P1 三段 wrapper 改 `set +e`／存 `PIPESTATUS`／`set -e`／寫 `.rc`／`exit`（本機以 `bash -eo pipefail` 重現：舊寫法 rc_file=no，新寫法 rc_file=1）；Global Constraints 契約改為「測試 rc 不得被 tee／upload 掩蓋」。P1 新增 Step 2b 暫時性 failure-path control（vitest wrapper 寫出 log／`.rc` 後回傳 97、確認 job 紅且 artifact 可下載、revert 後 diff 為空）。P2 Task 3 分開 implementation main run 與 final closure-commit main run，Gate A 綁 final HEAD；Step 5 抄 `app.id`、以 main SHA 為權威；Gate A 事件覆蓋改為 PR 與 push 各實跑一次、dispatch 以 shell control 或授權後實跑。未新增檔案、未動 GitHub 設定、未 commit。
- rev2（2026-09-05，第一輪 owner CHANGES_REQUIRED）：D1–D6 裁定回寫，本 plan 縮為 B2a，B2b 承接事項另列。P1-1 artifact 鏈：`frontend-dist` 固定名稱、`go`／`wails-build` `needs: frontend` 並驗 `index.html`、`wails build -s`、`.app` tar 封裝＋Gate A 驗可執行。P1-2 README §測試改寫為乾淨 checkout 可逐字執行且與 CI 逐項對應，納入 B2a Task 2；同時把過期的「先單獨重跑」段改指 register。P1-3 測試 step 統一 `bash`＋`pipefail`＋`tee`＋保留 rc＋`if: always()` 上傳、job 成敗只由 rc 決定。P1-4 checksums 改 `(cd <dir> && shasum -c)`；`git diff --check` 依 pull_request／push（含零 SHA）／workflow_dispatch 分流並 `fetch-depth: 0`。P1-5 B2b 明定 probe 分支與 `b2b/closure` PR 兩條路徑及外部寫入授權清單。P2：check contexts 以實際 check-runs 抄錄；D6 樣本改為 ruleset 後 clean PR run attempts。未新增檔案、未動 GitHub 設定、未 commit。
- rev1（2026-09-05）：初稿（preflight 與 D1–D6 提案）。
