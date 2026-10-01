#!/usr/bin/env bash
# candidate/lib/window_fn_runner.sh
#
# 007 對 review571 裁定 B 的一部分：window.sh 裡的 ax_auth_probe／
# get_window_id／cg_window_owner_check／ax_meta_text／ax_meta_title_json／
# ax_dump_window_tree／screencapture_window 都是 bash 函式，不是可以被
# `subprocess.Popen` 直接執行的真實可執行檔。decision.md 建議的作法是
# `bash -c 'source …/window.sh; fn "$@"' _ args`；本檔用一個實際存在的
# 腳本檔取代內嵌的 `-c` 字串——語意完全等價（一樣是啟動一個新的 bash
# 行程，source window.sh，呼叫指定函式並轉發參數），但避免多層 shell
# quoting（`-c` 字串裡又要放函式呼叫又要放參數）容易出錯的問題，也更容易
# 單獨測試（`./window_fn_runner.sh <fn> <args...>` 可以直接執行）。
#
# 用法：window_fn_runner.sh <function-name> [args...]
#
# 供 lib/timeout.sh 的 with_timeout() 透過 supervise.py 的
# `subprocess.Popen` 呼叫；環境變數（OSASCRIPT_BIN／SCREENCAPTURE_BIN／
# CG_WINDOW_CHECK_CMD 等測試注入點）由 Popen 預設繼承父行程環境，不需要
# 額外處理。
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=window.sh
source "$HERE/window.sh"

fn="${1:?window_fn_runner.sh: missing function name}"
shift
"$fn" "$@"
