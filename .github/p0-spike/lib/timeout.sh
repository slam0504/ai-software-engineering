#!/usr/bin/env bash
# candidate/lib/timeout.sh
#
# with_timeout <seconds> <out_file> <err_file> <cmd> [args...]
#   前景呼叫 `candidate/lib/supervise.py helper`（見該檔），由它用
#   `subprocess.Popen(..., start_new_session=True)` 真正持有子程序、用
#   `os.waitid(..., WNOWAIT)` 做有身分依據的有界等待與收尾（見
#   PLAN-delta.md「signal 持有與 timeout 表」、
#   `scratch/waitid-probe/result.txt` 的本機實測記錄）。stdout/stderr 各自
#   寫到呼叫端傳入的檔案。回傳（stdout 印一行 JSON 供呼叫端解析）：
#     {"rc": <int|null>, "timed_out": true|false, "killed": true|false,
#      "residual_pgid_members": <int|"unknown">}
#   rc 為 null 代表逾時且無法在時限內取得正常 exit code，呼叫端須標記
#   error，不得當 0 用。residual_pgid_members 為 "unknown" 時，呼叫端
#   **不得**當 0 用。
#
# 007 對 review571 裁定 B 的修正（取代 004–006 用 bash `&`/`wait` 自行
# fork/收尾的作法）：
#
#   006 用「還沒呼叫 `wait`」當作「還持有這個子程序」的身分依據，但
#   reviewer 指出這不成立——command substitution 執行完就回傳，外層只
#   拿到 JSON 字串，沒有保留任何 owner handle；shell 可能已經在其他地方
#   把這個子程序的 exit status 收割掉，PID 因此可能已經被系統重用。
#
#   007 改成整個子程序生命週期都由 `supervise.py` 這個 Python 行程持有：
#   `subprocess.Popen(..., start_new_session=True)` 讓子程序 PGID==PID；
#   `os.waitid(P_PID, pid, WEXITED|WNOWAIT|WNOHANG)` 可以在不回收
#   （不讓 PID 可能被重用）的前提下，重複確認「已結束但還沒被回收」這個
#   狀態——只要還沒真的呼叫 `p.wait()`，POSIX 保證這個 PID/PGID 不會被
#   系統重用，對它送 `os.killpg` 才有可證明的身分依據。對 leader 本身的
#   signal 一律用 `Popen.send_signal`（內部先檢查 `returncode`）。
#
#   window.sh 的 7 個 helper（`ax_auth_probe`／`get_window_id`／
#   `cg_window_owner_check`／`ax_meta_text`／`ax_meta_title_json`／
#   `ax_dump_window_tree`／`screencapture_window`）都是 bash 函式，不是
#   `subprocess.Popen` 能直接執行的真實可執行檔——呼叫端（driver.sh）改用
#   `lib/window_fn_runner.sh <fn> [args...]` 包成一個真實可執行的腳本
#   （語意等同 decision.md 建議的 `bash -c 'source …/window.sh; fn "$@"'
#   _ args`，見該檔檔頭說明兩者等價）。
set -uo pipefail

_WT_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
_WT_SUPERVISE_PY="$_WT_LIB_DIR/supervise.py"

# _WT_REAP_TIMEOUT_SECS：supervisor 在確認「已結束但還沒回收」失敗時，
# 有界輪詢的上限秒數（裁定 C：暫定工程上限，不要求事前真 helper 校準；
# 已算進 PLAN-delta.md 的總 deadline worst-case 表）。
_WT_REAP_TIMEOUT_SECS=1

with_timeout() {
  local secs="$1"; shift
  local out_file="$1"; shift
  local err_file="$1"; shift

  # 結果檔／未回收子程序清單都落在 out_file 所在目錄（即呼叫端的
  # EVIDENCE_DIR，不新增任何系統暫存路徑；EVIDENCE_DIR 本身由 driver.sh
  # 頂層依 RUNDIR 算出，不是 /tmp 或系統 TMPDIR）。
  local result_file="${out_file}.supervise-result.json"
  local unreaped_log; unreaped_log="$(dirname "$out_file")/unreaped-children.jsonl"

  local argv_json
  argv_json=$(python3 -c "
import json, sys
print(json.dumps(sys.argv[1:]))
" "$@")

  python3 "$_WT_SUPERVISE_PY" helper \
    --argv-json "$argv_json" \
    --out "$out_file" --err "$err_file" \
    --result "$result_file" \
    --unreaped-log "$unreaped_log" \
    --timeout "$secs" --term-wait 2 --kill-wait 1 --reap-timeout "$_WT_REAP_TIMEOUT_SECS" \
    2>>"$err_file" || true

  if [ -s "$result_file" ]; then
    cat "$result_file"
  else
    # supervisor 本身沒能寫出結果檔（例如 Python 啟動失敗）：不得假裝
    # rc=0——回一個明確標記失敗的 JSON，呼叫端一樣要當 error 處理。
    printf '{"rc": null, "timed_out": true, "killed": false, "residual_pgid_members": "unknown"}\n'
  fi
}
