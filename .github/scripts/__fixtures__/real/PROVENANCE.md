# 真實 run 樣本來源（逐位元組複製，不是合成 fixture）

由主 agent review（#472 CI-1 復核）要求補上：先前的 selftest 全部用手動組出的
`playwright-results.json`（`__fixtures__/evidence-builder.mjs`）驗證 evaluator，
沒有拿任何真正跑出來的 Playwright JSON 報告核對格式，導致 evaluator 對
`spec.file` 相對路徑格式（相對於 `config.rootDir`，不含 `gates/`／`controls/`
前綴）與 `chrome-argv.txt`（只有呼叫 `captureChromeArgv()` 的 spec 才產生，不
是每個 entry 都有）兩處假設錯誤，四個真實通過的 run 全部被誤判失敗。

以下五個目錄都是用 `cp -Rp` 從既有 worktree 的 `frontend/e2e/.artifacts/<run-id>/`
逐位元組複製而來（複製前後用遞迴 sha256 聚合比對過，見
`/Users/eason_tseng/b3a-evidence/ci-1/attempt-001/real-fixtures-copy-verification.txt`），
原始 artifacts 完全沒有被搬動或修改。

| 本目錄名稱 | 來源 worktree | 原始 run-id | 用途 | 聚合 sha256（遞迴檔案清單+內容） |
|---|---|---|---|---|
| `controls-pass-20260922T003926Z-3503c4` | `ai-se-worktrees/b3a-regress` | `20260922T003926Z-3503c4` | controls 正例（Run B，真實通過） | `eae5cdf3aa6894e788d8804ab895e8cfcb8f96247ac22f1f7e73c5e5210fef05` |
| `gate1-pass-20260925T065454Z-924661` | `ai-se-worktrees/b3a-2a-gates` | `20260925T065454Z-924661` | gate1 正例（真實通過） | `059e273ec8938ab9430834aa7a1c2619eba00b6997c06e7f2c1d4b6fbba536ea` |
| `gate2-pass-20260925T083946Z-b4abcb` | `ai-se-worktrees/b3a-2a-gates` | `20260925T083946Z-b4abcb` | gate2 正例（真實通過） | `5e84392b3497e282f86ac1d3cba2f012c82edb987df2ec5dc9783464fcbdc94a` |
| `stale-pass-20260925T084336Z-1d7c3f` | `ai-se-worktrees/b3a-2a-gates` | `20260925T084336Z-1d7c3f` | stale 正例（真實通過） | `401f92d9fc302978fe8cc273378834d0385e9906e0e21bf41fdda3b055f8dda4` |
| `gate2-failed-20260925T071019Z-146267` | `ai-se-worktrees/b3a-2a-gates` | `20260925T071019Z-146267` | gate2 首跑失敗（真實失敗，`harness.log` 最終結果：FAILED） | `ed36f2dcdc881da3adac83264345aed7df1ed3be2208fa0503d340630ae01cdc` |

聚合 sha256 算法：`find <dir> -type f -exec shasum -a 256 {} \; | sed 's|<dir>/||' | sort | shasum -a 256`
（逐檔 hash 再排序聚合，複製前後分別算一次核對是否相同）。

## decision497（`/Users/eason_tseng/b3a-evidence/2026-09-28-review497/decision.md` §B）新增第六個真實樣本

reviewer 裁定：這次 PR26 attempt1 遠端 run（controls job，run 36347781855，artifact 10941635782）
`e2e-wrapper-status.json` 為 `wrapperRc=1`／`childRc=1`／`status="completed"`，
但 `verdict.json` 卻寫 `overall:"passed"`——package-e2e-evidence.sh 先前只用
封裝/readback 是否成功決定 `overall`，完全不看執行結果。修法需要一份「執行
真的失敗、但打包/readback 完整成功」的真實樣本做回歸，不能只用合成 fixture。

| 本目錄名稱 | 來源 | 原始 run-id | 用途 | 聚合 sha256（縮小前，逐位元組複製） | 聚合 sha256（縮小後，本目錄現狀） |
|---|---|---|---|---|---|
| `controls-execution-failed-packaged-20260927T202728Z-151ce1` | GitHub Actions run `36347781855`（artifact `10941635782`，下載副本見 `/Users/eason_tseng/b3a-evidence/ci-2/ci-fix-001/regression/attempt-001-artifact-x-copy/20260927T202458Z-0f94a4/controls/e2e-evidence-package/artifacts/`，逐位元組複製自 `/Users/eason_tseng/b3a-evidence/ci-2/attempt-001/artifact/x/`，原件不動） | `20260927T202728Z-151ce1` | controls 執行失敗但打包成功（本次 decision497 修法的核心反例） | `56b54c19eca3713aaa65b8724a655fd901deb779cf93cffa5f3b17acc4e714c0` | `19968940a4483a8dce290065f44e191f44d4671131e01107915e7f8743aee70b` |

這個目錄只供 `evaluate-e2e-evidence.mjs`「非成功宣稱但仍有新 run 目錄」分支
（約第 314-321 行）與 `package-e2e-evidence.review497.selftest.mjs` 的
`executionOutcome`／`overall` 語意回歸使用；該分支不讀 `runDir` 底下任何檔
案內容，只計數新 run 目錄數量，因此縮小規則沿用既有五個 fixture（整個刪除
`fake-tools/`／`playwright/`／`fixture-git-log.txt`／`fixture-git-status.txt`／
`glossary-final-content.md`；`network-samples.log` 截斷保留前 25＋後 10
行），細節見同目錄 `SHRINK-PROVENANCE.json`。真正搭配的 `e2e-wrapper-status.json`／
`e2e.rc`（`wrapperRc=1`／`childRc=1`／`status="completed"`／
`childConfirmedGone="true"`／`producerErrors=[]`）是 selftest 內用這次真實
下載的 `e2e-wrapper-status.json` 原文照抄的常數，不是憑空編造。

## review round 3（#480 R6）：縮小到最小必要切片

上表五個聚合 hash 是**縮小前**（逐位元組複製）的值，作為 provenance 保留；
縮小前的完整 106 檔（3,199,584 bytes，含 `playwright/` 的 trace.zip／PNG／
error-context.md 與 `fake-tools/` 整棵假 CLI 安裝樹）已先打包備份到 repo 外
的持久路徑
`/Users/eason_tseng/b3a-evidence/ci-1/attempt-002/fixtures-full-backup/real-fixtures-full-r2.tar.gz`
（sha256 `62ef586e327d059057e02e8fb05fdcf30238f775bc6e3c595ae8e2463d78bdc0`），
**不刪除**——最原始的來源（`ai-se-worktrees/b3a-2a-gates`／`b3a-regress` 的
`frontend/e2e/.artifacts/<run-id>/`）本來就完全沒被搬動過，這份 tar 是額外
的第二層備份。

縮小動作（腳本與結果見同目錄 `SHRINK-PROVENANCE.json`）：
- **整個刪除**：`fake-tools/`（假 CLI 安裝樹，evaluator 完全不讀）、
  `playwright/`（trace.zip／PNG／error-context.md，evaluator 完全不讀）、
  `fixture-git-log.txt`／`fixture-git-status.txt`／`glossary-final-content.md`
  （`captureFixtureSnapshot()` 產物，不在任何必要檔清單裡）。
- **截斷保留**：`network-samples.log`（evaluator 只檢查「存在」，不讀內
  容）——每份只留前 25 行＋後 10 行，中間截斷處留一行標註被截斷的行數、原
  始檔案 sha256／bytes 及本份備份 tar 的位置，可回頭核對。
- **完全不動**：`run-state.json`／`harness.log`／`playwright-results.json`／
  `run-env.json`／`execution-entry.json`／`gate-flow.json`／
  `gate-evidence.json`／`control-a-evidence.json`／`control-b-evidence.json`／
  `wails-dev.log`／`invocations.log`／`preflight-invocations.log`／
  `browser-network-violations.log`／`artifact-integrity-baseline.json`／
  `TEST_FAILED`（gate2-failed 唯一有這個檔）——這些是 evaluator 實際讀取
  內容或核對存在的檔案，逐位元組保留原樣，不做任何轉換。
- 縮小後：**68 檔、416KB**（原 106 檔、3,199,584 bytes）。縮小後重跑
  `evaluate-e2e-evidence.real-fixtures.selftest.mjs`／
  `evaluate-e2e-evidence.review480.selftest.mjs`／`run-batch.selftest.mjs`／
  `package-e2e-evidence.selftest.mjs`（共 29 項斷言）全數仍 rc=0，證明縮小
  沒有動到任何被實際驗證用到的內容。

## 尚未取得真實樣本

`default`（`glossary.spec.ts`）與全部 8 個 `scenario` entry**目前沒有真實
JSON 樣本**——本輪／先前所有 browser E2E run 都還在用只有 `list` reporter 的
`playwright.config.ts`／`playwright.scenario.config.ts`（JSON reporter 是本輪
新增的 `playwright.ci-default.config.ts`／`playwright.ci-scenario.config.ts`
才有）。evaluator 對這兩類 entry 的格式假設**只能靠「同一個 Playwright JSON
reporter、同一份 `defineConfig` 機制」的結構性推論**（`config.rootDir` +
`spec.file` 的相對路徑規則、`chrome-argv.txt` 只在呼叫
`captureChromeArgv()` 的 spec 才產生，見 `frontend/e2e/glossary.spec.ts:35`、
`frontend/e2e/scenarios/codexApproval.spec.ts:76`、
`codexSessionRecovery.spec.ts:66`、`claudeApproval.spec.ts:83`、
`claudeSessionRecovery.spec.ts:109`），**不是猜測出來的合成樣本冒充真實樣本**。
這點列入交付的「只能在遠端驗證」清單：首次對 default／scenario 的遠端執行
必須用實際產出的 `playwright-results.json` 核對 evaluator 的假設是否成立。
