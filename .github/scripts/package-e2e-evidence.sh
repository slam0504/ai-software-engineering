#!/usr/bin/env bash
# package-e2e-evidence.sh — 從 draft-e2e-smoke.yml「package evidence」step 抽出來的
# 修正版邏輯（codex-reviewer #78 F4、#82 G1／G2 反例：package_without_harness／
# invalid_json_early／failed_state_with_old_success）。
#
# #78 F4 反例證明的漏洞：舊版 inline 邏輯只檢查三個 wrapper 自己保證會寫的
# 檔案（e2e.out／e2e.rc／e2e-wrapper-status.json）存在，
# `frontend/e2e/.artifacts/` 整個目錄不存在時直接套用「啟動前就失敗」的例
# 外，不再要求任何 harness 產出的證據。修法：把「例外只適用於確認過的早期
# 失敗／未開始」跟「成功但缺證據」分開判斷。
#
# #82 G1／G2 反例證明上一輪的修法本身還有兩個缺口（用 bash `sed` 抓
# JSON／log 欄位這個做法本身就是問題核心）：
#   - G1 invalid_json_early：e2e-wrapper-status.json 是 "not json" 這種無
#     法解析的內容時，sed 抓不到任何欄位、全部當空字串，「status=unknown
#     rc=unknown」被誤判成『確認過的早期失敗／未開始』，照樣套用例外、
#     exit 0。
#   - G2 failed_state_with_old_success：舊版只用 `grep -q` 核對
#     'overallFailed=false'／'cleanupClean=true' 是否「出現在檔案裡任何
#     一行」，run-state.json 的 runId／status 也完全沒核對。log 前段有這
#     兩個字串、但最後一行其實是「最終結果：FAILED」時照樣判定成功。
#
# 修法：JSON／run-state／harness.log 的解析與內容驗證整段抽到同目錄的
# evaluate-e2e-evidence.mjs（Node，用 `JSON.parse`），這支 bash 腳本只保留
# 檔案存在性檢查與封裝（cp／tar／manifest）：
#   - wrapper JSON 解析失敗、必要欄位缺漏／型別不對、或 wrapperRc 跟
#     e2e.rc 內容兜不起來，一律視為「無法信任」，不套用「未開始」例外
#     （移除舊版讀取 "rc" 欄位的 bash 相容分支——新版 wrapper 一定會寫
#     wrapperRc，不需要、也不該再留一條會製造模糊判定的相容路徑）。
#   - 宣稱成功時要求 frontend/e2e/.artifacts/ 底下剛好一個 run 目錄、
#     run-state.json 的 runId 與目錄名一致且 status 恰好是 'stopped'、沒
#     有 failureStage／非空的 observationFailures；harness.log 用「最後
#     一筆」globalTeardown 判定行核對 cleanupClean=true 與
#     artifactViolations=0，且「最後一行」要是「最終結果：PASSED」，不是
#     在檔案任何位置 grep 到字串就算數。
#   - `cp ... || true` 換成有檢查的複製：來源檔案存在卻複製失敗才是錯誤
#     （記進 packagingErrors，最後讓 step 非零），來源本來就不存在不算錯誤
#     （已經在缺漏清單處理過）。
#   - 最後的完整性檢查核對「輸出包內容」（`e2e-evidence-package/` 底下實際
#     有沒有這個檔案），不是只核對來源檔案存不存在——避免「來源存在、但
#     cp 失敗，manifest 卻沒發現」這種漏洞。
#
# 用法：package-e2e-evidence.sh <workdir>
#   <workdir> 底下要有 e2e.out／e2e.rc／e2e-wrapper-status.json，以及
#   （若有的話）frontend/e2e/.artifacts/。輸出 e2e-evidence-package/、
#   e2e-evidence.tar.gz、e2e-evidence-manifest.txt、
#   e2e-evidence-manifest.sha256 都寫在 <workdir> 底下。
# codex-reviewer(#84)：加 pipefail，讓 find/sort/shasum/tar 這幾條管線任何
# 一節失敗都能讓 `if ! ( ... ) > file; then` 偵測到，不會因為管線最後一個
# 指令（例如 sort 對空輸入）自己成功就把前面的失敗吃掉。
set -uo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKDIR="${1:?usage: package-e2e-evidence.sh <workdir>}"
WORKDIR="$(cd "$WORKDIR" && pwd)"
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

shopt -s nullglob
artifact_dirs=(frontend/e2e/.artifacts/*/)

rm -rf e2e-evidence-package
mkdir -p e2e-evidence-package

copy_if_present() {
  local src="$1" dst="$2"
  if [ -f "$src" ]; then
    if ! cp -f "$src" "$dst" 2>/tmp/pkgcp.err; then
      packaging_errors+=("複製 ${src} 失敗：$(cat /tmp/pkgcp.err 2>/dev/null)")
    fi
  fi
}
copy_if_present e2e.out e2e-evidence-package/
copy_if_present e2e.timestamped.out e2e-evidence-package/
copy_if_present e2e.rc e2e-evidence-package/
copy_if_present e2e-wrapper-status.json e2e-evidence-package/

if [ "${#artifact_dirs[@]}" -gt 0 ]; then
  mkdir -p e2e-evidence-package/artifacts
  if ! cp -Rf frontend/e2e/.artifacts/*/ e2e-evidence-package/artifacts/ 2>/tmp/pkgcp.err; then
    packaging_errors+=("複製 frontend/e2e/.artifacts/ 失敗：$(cat /tmp/pkgcp.err 2>/dev/null)")
  fi
fi

# JSON／run-state／harness.log 的內容驗證（G1／G2）：Node 直接讀 workdir，
# 若「確認過的早期失敗／未開始」例外成立會自己把 NO-RUN.txt 寫進
# e2e-evidence-package/；任何 ::error:: 都印到這支腳本自己的 stdout（跟
# manifest 輸出混在一起，呼叫端本來就會看整段 log）。
content_eval_ok=1
if ! "$NODE" "$SCRIPT_DIR/evaluate-e2e-evidence.mjs" "$WORKDIR" "e2e-evidence-package"; then
  content_eval_ok=0
fi

# 完整性核對對象是「輸出包內容」，不是來源檔案。
for f in e2e.out e2e.rc e2e-wrapper-status.json; do
  if [ -f "$f" ] && [ ! -f "e2e-evidence-package/$f" ]; then
    packaging_errors+=("來源 ${f} 存在，但輸出包 e2e-evidence-package/${f} 沒有產生")
  fi
done

# codex-reviewer(#84)：tar／hash 命令失敗必須往外傳遞，不得繼續 exit 0——
# 逐一檢查每條 pipeline／指令自己的結束碼，失敗記進 packaging_errors。
if ! ( cd e2e-evidence-package && find . -type f | sort ) > e2e-evidence-manifest.txt; then
  packaging_errors+=("產生 e2e-evidence-manifest.txt 失敗（find/sort 非零）")
fi
if ! ( cd e2e-evidence-package && find . -type f -print0 | sort -z | xargs -0 shasum -a 256 ) > e2e-evidence-manifest.sha256; then
  packaging_errors+=("產生 e2e-evidence-manifest.sha256 失敗（find/sort/shasum 非零）")
fi
if ! tar -czf e2e-evidence.tar.gz -C e2e-evidence-package .; then
  packaging_errors+=("打包 e2e-evidence.tar.gz 失敗（tar 非零）")
fi

echo "--- manifest ---"
cat e2e-evidence-manifest.txt
echo "--- sha256 ---"
cat e2e-evidence-manifest.sha256

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
  echo "::error::內容驗證（wrapper JSON／run-state.json／harness.log）判定證據缺漏或不可信，fail（詳見上方 ::error:: 訊息）"
  exit_code=1
fi
exit "$exit_code"
