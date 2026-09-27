#!/usr/bin/env bash
# package-e2e-evidence.sh — B3a-CI-1。沿用 PR #12（79819d8）codex-reviewer
# #78/#82/#84 修法後的封裝邏輯（F4/G1/G2 反例），改為單一 entry、單一顯式
# run 目錄的介面（不再對整個 frontend/e2e/.artifacts/ 做萬用字元掃描）——
# 這是 decision470「每案獨立路徑，禁止新案覆寫前案」的直接後果：run-dir 由
# run-batch.mjs 做 before/after diff 算好、顯式傳入，本腳本只封裝「這一個」
# 目錄，不會意外把其他 entry 的證據一起打包進來。
#
# 用法：package-e2e-evidence.sh <workdir> [<run-dir>]
#   <workdir>  底下要有 e2e.out／e2e.rc／e2e-wrapper-status.json；輸出
#              e2e-evidence-package/、e2e-evidence.tar.gz、
#              e2e-evidence-manifest.txt、e2e-evidence-manifest.sha256 都寫在
#              <workdir> 底下。
#   <run-dir>  （選用）本次 entry 對應的唯一 harness run 目錄絕對路徑；沒有
#              提供或路徑不存在時，僅打包 wrapper 自身輸出（NO-RUN 情境）。
#
# 內容驗證（JSON／run-state／harness.log／playwright-results.json）交給同目錄
# evaluate-e2e-evidence.mjs（Node，見該檔頭），本腳本只做檔案存在性檢查與封
# 裝（cp／tar／manifest），失敗一律非零、不得用 warn 掩蓋、不得用 || true
# 靜默略過。
set -uo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKDIR="${1:?usage: package-e2e-evidence.sh <workdir> [<run-dir>]}"
WORKDIR="$(cd "$WORKDIR" && pwd)"
RUN_DIR="${2:-}"
ENTRY_ID="${3:-}"
cd "$WORKDIR"

if [ -z "${NODE:-}" ]; then
  if command -v node >/dev/null 2>&1; then
    NODE="$(command -v node)"
  fi
fi
if [ -z "${NODE:-}" ] || [ ! -x "$NODE" ]; then
  echo "FATAL：找不到可用的 node（本腳本的 JSON／log 內容驗證需要 Node，見 evaluate-e2e-evidence.mjs）" >&2
  exit 2
fi

packaging_errors=()
missing=()

for f in e2e.out e2e.rc e2e-wrapper-status.json; do
  [ -f "$f" ] || missing+=("$f")
done

rm -rf e2e-evidence-package
mkdir -p e2e-evidence-package

copy_if_present() {
  local src="$1" dst="$2"
  if [ -f "$src" ]; then
    if ! cp -f "$src" "$dst" 2>"$WORKDIR/.pkgcp.err"; then
      packaging_errors+=("複製 ${src} 失敗：$(cat "$WORKDIR/.pkgcp.err" 2>/dev/null)")
    fi
  fi
}
copy_if_present e2e.out e2e-evidence-package/
copy_if_present e2e.timestamped.out e2e-evidence-package/
copy_if_present e2e.rc e2e-evidence-package/
copy_if_present e2e-wrapper-status.json e2e-evidence-package/
copy_if_present new-run-dirs.json e2e-evidence-package/
# review round 4（#480 R2 剩餘缺口）：可信 CI invocation envelope
# （run-batch.mjs 蓋章的 entry 專屬 envelope.json）跟其他 wrapper 輸出一起
# 打包，讓 evaluate-e2e-evidence.mjs 的交叉核對結果也留在可稽核的證據包裡。
copy_if_present envelope.json e2e-evidence-package/

if [ -n "$RUN_DIR" ] && [ -d "$RUN_DIR" ]; then
  mkdir -p e2e-evidence-package/artifacts
  if ! cp -Rf "$RUN_DIR"/. e2e-evidence-package/artifacts/ 2>"$WORKDIR/.pkgcp.err"; then
    packaging_errors+=("複製 ${RUN_DIR} 失敗：$(cat "$WORKDIR/.pkgcp.err" 2>/dev/null)")
  fi
fi

# review round 3（R5）：候選 run 目錄是 0 或多於 1 時，evaluator 會拒絕驗
# 收，但「拒絕驗收」不等於「不保存原始資料」——reviewer 指出這種情況仍要
# 安全保存候選原始資料供診斷。多於 1 個候選時（identity 異常／並行污染），
# run-batch.mjs 會把完整清單寫進 candidate-dirs.txt；這裡逐一複製，保留原
# 始目錄名稱（不像唯一案例那樣攤平），避免多筆候選彼此覆寫。
if [ -f candidate-dirs.txt ]; then
  mkdir -p e2e-evidence-package/candidates
  while IFS= read -r candidate; do
    [ -z "$candidate" ] && continue
    [ -d "$candidate" ] || continue
    name="$(basename "$candidate")"
    if ! cp -Rf "$candidate" "e2e-evidence-package/candidates/$name" 2>"$WORKDIR/.pkgcp.err"; then
      packaging_errors+=("複製候選目錄 ${candidate} 失敗：$(cat "$WORKDIR/.pkgcp.err" 2>/dev/null)")
    fi
  done < candidate-dirs.txt
  copy_if_present candidate-dirs.txt e2e-evidence-package/
fi

# JSON／run-state／harness.log／playwright-results.json 的內容驗證：Node 直
# 接讀 workdir，若「未取得 run 證據」例外成立會自己把 NO-RUN.txt 寫進
# e2e-evidence-package/；任何 ::error:: 都印到這支腳本自己的 stdout。
content_eval_ok=1
if [ -n "$ENTRY_ID" ]; then
  if ! "$NODE" "$SCRIPT_DIR/evaluate-e2e-evidence.mjs" "$WORKDIR" "e2e-evidence-package" "$ENTRY_ID"; then
    content_eval_ok=0
  fi
else
  echo "::error::package-e2e-evidence.sh: 缺少 entry-id 參數，無法呼叫內容驗證（evaluate-e2e-evidence.mjs 需要它查預期 spec 集合）" >&2
  content_eval_ok=0
fi

for f in e2e.out e2e.rc e2e-wrapper-status.json; do
  if [ -f "$f" ] && [ ! -f "e2e-evidence-package/$f" ]; then
    packaging_errors+=("來源 ${f} 存在，但輸出包 e2e-evidence-package/${f} 沒有產生")
  fi
done

if ! ( cd e2e-evidence-package && find . -type f | sort ) > e2e-evidence-manifest.txt; then
  packaging_errors+=("產生 e2e-evidence-manifest.txt 失敗（find/sort 非零）")
fi
if ! ( cd e2e-evidence-package && find . -type f -print0 | sort -z | xargs -0 shasum -a 256 ) > e2e-evidence-manifest.sha256; then
  packaging_errors+=("產生 e2e-evidence-manifest.sha256 失敗（find/sort/shasum 非零）")
fi
if ! tar -czf e2e-evidence.tar.gz -C e2e-evidence-package .; then
  packaging_errors+=("打包 e2e-evidence.tar.gz 失敗（tar 非零）")
fi

# review round 3（R5）：封存可驗證性——reviewer 指出 manifest／tar 產生後從
# 未「解包讀回」核對過，manifest 只是 tar 之前對來源目錄的紀錄，不能證明
# tar 裡實際裝的就是同一份東西（tar 本身失敗、寫一半中斷、權限被改都可能
# 讓兩者不一致）。這裡解包到獨立目錄，重新核對檔案集合與逐檔 sha256 是否
# 跟 tar 之前算的 manifest 相符。
readback_ok=1
if [ -f e2e-evidence.tar.gz ]; then
  rm -rf e2e-evidence-readback
  mkdir -p e2e-evidence-readback
  if ! tar -xzf e2e-evidence.tar.gz -C e2e-evidence-readback; then
    packaging_errors+=("readback：解包 e2e-evidence.tar.gz 失敗（tar -x 非零）")
    readback_ok=0
  else
    if ! ( cd e2e-evidence-readback && find . -type f | sort ) > e2e-evidence-readback-filelist.txt; then
      packaging_errors+=("readback：列出解包後檔案清單失敗")
      readback_ok=0
    elif ! diff -q <(cd e2e-evidence-package && find . -type f | sort) e2e-evidence-readback-filelist.txt >/dev/null 2>&1; then
      packaging_errors+=("readback：解包後檔案集合與封裝前不一致（見 e2e-evidence-readback-filelist.txt 對照 e2e-evidence-manifest.txt）")
      readback_ok=0
    fi
    if ! ( cd e2e-evidence-readback && find . -type f -print0 | sort -z | xargs -0 shasum -a 256 ) > e2e-evidence-readback.sha256; then
      packaging_errors+=("readback：計算解包後 sha256 失敗")
      readback_ok=0
    elif ! diff -q e2e-evidence-manifest.sha256 e2e-evidence-readback.sha256 >/dev/null 2>&1; then
      packaging_errors+=("readback：解包後逐檔 sha256 與封裝前不一致——tar 內容可能損毀或不完整")
      readback_ok=0
    fi
  fi
else
  readback_ok=0
fi

# review round 8（decision497 §B-2／§B-3）：reviewer 已證實真實 controls
# run（wrapperRc=1／childRc=1／status="completed"）打包/readback 完整成功
# 時，verdict.json 先前只憑本腳本自己的封裝結果（missing／packaging_errors／
# content_eval_ok／readback_ok）就寫 overall:"passed"，完全沒看 wrapper／
# child 是否真的執行成功——讓人誤讀成「這個 entry 成功」。這裡把「執行結
# 果」（executionOutcome，依 e2e-wrapper-status.json／e2e.rc／
# new-run-dirs.json 獨立判定，不依賴 evaluator 的 exit code）與「打包／
# readback 結果」（packageStatus／packagingExitCode，即先前的 exit_code）
# 分開計算，overall 現在要求兩者都成立才是 passed。
#
# 注意：packagingExitCode／本腳本自己最終的 process exit code 刻意不因
# executionOutcome!=success 而變號——decision497 §B-3 明確要求「package腳本
# 可以成功封存失敗run」（保留 failed run 的封存能力），本腳本的 exit
# status 仍然只代表「封裝／readback 這件事本身有沒有做好」。
NODE_EXECUTION_OUTCOME_JS='
const fs = require("fs");
const path = require("path");
const workdir = process.argv[1];
const packageDir = process.argv[2];
function readJsonIfExists(p) {
  if (!fs.existsSync(p)) return null;
  try { return JSON.parse(fs.readFileSync(p, "utf8")); } catch { return undefined; }
}
const status = readJsonIfExists(path.join(workdir, "e2e-wrapper-status.json"));
const rcPath = path.join(workdir, "e2e.rc");
let rcVal = null;
if (fs.existsSync(rcPath)) {
  const t = fs.readFileSync(rcPath, "utf8").trim();
  rcVal = /^-?\d+$/.test(t) ? Number(t) : NaN;
}
const newRunDirs = readJsonIfExists(path.join(workdir, "new-run-dirs.json"));
const newRunCount = Array.isArray(newRunDirs) ? newRunDirs.length : null;

const statusUsable = !!status && typeof status === "object"
  && typeof status.status === "string"
  && Number.isInteger(status.wrapperRc)
  && !(rcVal !== null && (Number.isNaN(rcVal) || rcVal !== status.wrapperRc));

let executionOutcome = "unknown";
if (statusUsable) {
  // 跟 evaluate-e2e-evidence.mjs 的 claimedSuccess 用同一組寬鬆條件決定
  // 「evaluator 有沒有走深度驗證分支」；executionOutcome 本身另外疊上
  // childRc／childConfirmedGone／producerErrors／新 run 目錄數剛好 1
  // 這些較嚴格的條件（decision497 §B-2：「依 wrapper／child rc」），兩者
  // 故意不是同一個布林值。
  const looseClaimedSuccess = status.status === "completed" && status.wrapperRc === 0;
  const strictSuccess = looseClaimedSuccess
    && status.childRc === 0
    && status.childConfirmedGone === "true"
    && Array.isArray(status.producerErrors) && status.producerErrors.length === 0
    && newRunCount === 1;
  if (strictSuccess) {
    executionOutcome = "success";
  } else if (!looseClaimedSuccess && newRunCount === 0) {
    executionOutcome = "no-run";
  } else {
    executionOutcome = "failed";
  }
}

let contentValidationScope = "unavailable";
if (executionOutcome === "success") {
  contentValidationScope = "full";
} else if (fs.existsSync(path.join(packageDir, "NO-RUN.txt"))) {
  contentValidationScope = "no-run";
} else if (fs.existsSync(path.join(packageDir, "validation-scope.json"))) {
  contentValidationScope = "packaging-only";
}

process.stdout.write(executionOutcome + "\n" + contentValidationScope + "\n");
'
execution_outcome="unknown"
content_validation_scope="unavailable"
if exec_info="$("$NODE" -e "$NODE_EXECUTION_OUTCOME_JS" "$WORKDIR" "e2e-evidence-package" 2>"$WORKDIR/.execinfo.err")"; then
  execution_outcome="$(printf '%s\n' "$exec_info" | sed -n '1p')"
  content_validation_scope="$(printf '%s\n' "$exec_info" | sed -n '2p')"
else
  packaging_errors+=("計算 executionOutcome／contentValidationScope 失敗：$(cat "$WORKDIR/.execinfo.err" 2>/dev/null)")
fi
rm -f "$WORKDIR/.execinfo.err"

echo "--- manifest ($ENTRY_ID) ---"
cat e2e-evidence-manifest.txt
echo "--- sha256 ---"
cat e2e-evidence-manifest.sha256
echo "--- readback_ok=$readback_ok ---"

exit_code=0
if [ "${#missing[@]}" -gt 0 ]; then
  echo "::error::必要證據缺漏，fail（不得用 warn 掩蓋）：${missing[*]}"
  exit_code=1
fi
if [ "${#packaging_errors[@]}" -gt 0 ]; then
  echo "::error::封裝過程本身出錯（cp 失敗等），fail（不得用 || true 靜默略過）：${packaging_errors[*]}"
  exit_code=1
fi
if [ "$content_eval_ok" -eq 0 ]; then
  echo "::error::內容驗證（wrapper JSON／run-state.json／harness.log／playwright-results.json）判定證據缺漏或不可信，fail（詳見上方 ::error:: 訊息）"
  exit_code=1
fi
if [ "$readback_ok" -eq 0 ]; then
  echo "::error::封存 readback 驗證失敗（詳見上方 ::error:: 訊息），保存失敗必須使 entry 失敗"
  exit_code=1
fi

# review round 3（R5）：單一持久 verdict——先前 run-batch.mjs 在這支腳本跑
# 完之後又「重新呼叫一次」evaluate-e2e-evidence.mjs，兩次獨立呼叫理論上可
# 能得到不同結論（例如 NO-RUN.txt 只在第一次呼叫時被寫入，第二次呼叫看到
# 的檔案狀態已經不同）。改成本腳本是唯一的判定權威，把最終結論寫成
# machine-readable 的 verdict.json，run-batch.mjs 只讀這份檔案，不重新呼叫
# evaluator。
#
# review round 8（decision497 §B-2）：package_status／packaging_exit_code＝
# 「封裝／readback／內容驗證這件事本身有沒有做好」（即先前的 exit_code，
# 語意不變，本腳本自己的 process exit code 仍然只看這個——「package腳本可
# 以成功封存失敗run」）；execution_outcome＝「wrapper／child 這次實際執行
# 結果如何」（見上方 NODE_EXECUTION_OUTCOME_JS，獨立於 evaluator exit
# code）。overall 現在要求兩者同時成立才是 passed，不再只憑封裝面就宣稱
# passed（真實反例：controls wrapperRc=1／childRc=1，封裝/readback 全部成
# 功，先前 verdict.json 仍寫 overall:"passed"）。
package_status="$([ "$exit_code" -eq 0 ] && echo ok || echo failed)"
overall_status="failed"
if [ "$execution_outcome" = "success" ] && [ "$package_status" = "ok" ]; then
  overall_status="passed"
fi
ENTRY_ID_JSON="$("$NODE" -e "process.stdout.write(JSON.stringify(process.argv[1]))" "$ENTRY_ID")"
cat > verdict.json <<VERDICT_EOF
{
  "entryId": $ENTRY_ID_JSON,
  "missingWrapperFiles": ${#missing[@]},
  "packagingErrors": ${#packaging_errors[@]},
  "contentEvalOk": $([ "$content_eval_ok" -eq 1 ] && echo true || echo false),
  "readbackOk": $([ "$readback_ok" -eq 1 ] && echo true || echo false),
  "packagingExitCode": $exit_code,
  "packageStatus": "$package_status",
  "executionOutcome": "$execution_outcome",
  "contentValidationScope": "$content_validation_scope",
  "overall": "$overall_status",
  "generatedAtIso": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
VERDICT_EOF

rm -f "$WORKDIR/.pkgcp.err"
exit "$exit_code"
