# A4 Go minimum／validated toolchain：比較矩陣與收尾紀錄

> 2026-10-10：在**同一個受測 commit**（已加上 `toolchain go1.26.5`）上，分別用 Go 1.25.0（`GOTOOLCHAIN=local`）與 Go 1.26.5 執行完整 Go gate 與 Wails build，兩腳全綠，沒有任何可歸因於 Go 版本的失敗。依驗收條件 (2)，minimum 維持 `go 1.25.0`，1.26.5 為 validated。本 PR 在 `go.mod` 加上 `toolchain go1.26.5`（裁決 (4)），並同步 README 的版本語意（驗收 (3)）。

## 0. 範圍

- backlog A4 的驗收條件：
  1. 同一個 commit 用 1.25 與 1.26.5 跑 Go gate（test＋race＋vet）與 Wails build，並做比較矩陣；1.25 那腳必須記錄 `go version`，並以 `GOTOOLCHAIN=local` 執行。
  2. 只有能歸因於 Go 版本不相容的失敗才提高 minimum。
  3. README 同步為「X minimum、1.26.5 validated」。
  4. 採用 `toolchain go1.26.5`。
- 矩陣由實作者在 repo 外的 worktree 執行；主審逐項核對了原始輸出（見第 4 節）。原始證據都在 repo 外的 `~/b3a-evidence/a4/`（`MATRIX.md`、`legs/`、`setup/`）。

## 1. 受測對象

- 基底：`787612d`，tree `69fc223f…`，與 main `eb524f2` 相同。
- **受測 commit**：`76954aa4bb805c43753ad61b777284ad8c9a12cf`（tree `0fd3a29e…`）。這個 commit 只改了 `go.mod`：在 `go 1.25.0` 之後加一個空行和 `toolchain go1.26.5`，共 +2 行，與 `go mod edit -toolchain=go1.26.5` 的輸出相同。
- 本 PR 的 `go.mod` 與受測 commit 的 `go.mod` 逐位元組相同（blob 相同）。本 PR 其餘的改動都是文件，不影響 Go 建置與測試。

## 2. 環境

| 項目 | Go 1.25.0 腳 | Go 1.26.5 腳 |
|---|---|---|
| go 路徑 | `~/sdk/go1.25.0/bin/go`（由 `golang.org/dl` 安裝） | `~/sdk/go1.26.5/bin/go` |
| `go version` | `go1.25.0 darwin/amd64` | `go1.26.5 darwin/amd64` |
| `go env GOVERSION／GOTOOLCHAIN` | `go1.25.0`／`local` | `go1.26.5`／`local` |
| Wails CLI | v2.13.0；build 輸出的 Compiler 欄為 `~/sdk/go1.25.0/bin/go` | v2.13.0；Compiler 欄為 `~/sdk/go1.26.5/bin/go` |

共同條件：darwin/amd64，node v26.8.1，兩腳共用同一份 `frontend/dist`（`npm ci && npm run build`）。

## 3. 矩陣

步驟順序比照 CI 的 go job，最後加上 `wails build -s`。

| 步驟 | Go 1.25.0 | Go 1.26.5 |
|---|---|---|
| `test -z "$(gofmt -l .)"` | rc 0 | rc 0 |
| `go build ./...` | rc 0（46 s） | rc 0（46 s） |
| `go vet ./...` | rc 0（15 s） | rc 0（14 s） |
| `go test -race ./... -count=1 -timeout 30m -json` | **rc 0**（394 s） | **rc 0**（434 s） |
| 套件 | 26：23 pass／0 fail／3 skip | 26：23 pass／0 fail／3 skip |
| 測試（含 subtest） | 1368：1366 pass／0 fail／2 skip | 1368：1366 pass／0 fail／2 skip |
| `DATA RACE` | 0 | 0 |
| `wails build -s` | rc 0（140 s） | rc 0（73 s） |
| **binary 的 `go version -m`** | **go1.25.0** | **go1.26.5** |

- 兩腳的 1368 筆測試結果逐筆相同。skip 的項目兩腳也相同：
  - 套件：`cmd/probe-codex-approval`、`cmd/probe-multiturn`、`internal/ports`；
  - 測試：corpus 重生器 `TestUpdateCorpusInternal`、`TestRegenerateCorpusVerdicts`。
- 1.25 那腳產出的 binary 確認是由 go1.25.0 編譯，可證明 `GOTOOLCHAIN=local` 有效，沒有被自動切換到 1.26.5。
- 兩腳都沒有紅燈，因此沒有重跑，也沒有任何需要分類的失敗。

## 4. 主審核對（2026-10-10）

主審親自執行下列核對：

- `git diff 787612d 76954aa`：只有 `go.mod` 的 +2 行。
- 兩腳各自的 binary 都用 `go version -m` 確認：1.25.0 那腳是 `go1.25.0`，1.26.5 那腳是 `go1.26.5`。
- 1.25.0 那腳的 `env.txt`：`GOVERSION=go1.25.0`、`GOTOOLCHAIN=local`，`which go` 指向 `~/sdk/go1.25.0/bin/go`。
- 兩份 `go-test.json` 各自用 python 重新解析：tests 1366 pass、2 skip；packages 23 pass、3 skip；`DATA RACE` 0 次。
- 兩腳所有步驟的 rc 檔都是 0。

## 5. 判定

- **minimum**：維持 `go 1.25.0`。理由是 1.25.0 在完整 Go gate 與 Wails build 中都是全綠，沒有任何可歸因於版本不相容的失敗。
- **validated**：1.26.5，記錄在 `toolchain go1.26.5`，CI 也使用這個版本。
- **README**：改寫為「最低 1.25.0、已驗證 1.26.5」，並補充一句：本機 Go 低於 1.26.5 時，預設 `GOTOOLCHAIN=auto` 會切換至建議工具鏈，先找 PATH 中的 go1.26.5，否則使用快取或下載。若要使用符合最低 1.25.0 要求的本機 Go，請設定 `GOTOOLCHAIN=local`。（`toolchain` 是建議版本而非版本鎖定，較新的工具鏈不會因它降級；依據 https://go.dev/doc/toolchain#go-toolchain-selection ）

## 6. 限制

- 只測了 1.25.0，沒有測後續的 patch 版本；平台只有 darwin/amd64；1.25 那腳只在本機執行，沒有在 CI runner 上跑。
- 每腳只跑一次，無法排除間歇性失敗。不過兩腳都是全綠，這一點不影響 minimum 的判定。
- 沒有跑 `check-cli.sh`、`bundle-clis.sh`、vitest，也沒有 GUI 驗證；A4 的驗收條件不包含這些項目。
- `go-test.json` 只收 stdout，stderr 另存（兩腳都是 0 bytes）；CI 則是把兩者合併成同一個串流。
- 本 PR 的 CI（以及合併後的 main push CI）會在加上 toolchain 的 commit 上以 Go 1.26.5 執行；那些結果會記錄在 PR 與合併回報中，不在本紀錄的範圍內。
