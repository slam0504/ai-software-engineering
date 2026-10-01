#!/usr/bin/env bash
# candidate/lib/window.sh
#
# 只定義**候選命令**，本輪不執行（不啟動 GUI）。命令本身的正確性（例如
# AXWindowNumber 是否等同 screencapture -l 吃的 CGWindowID）本輪仍找不到
# Apple 一手文件證實，維持 001 PLAN.md §7 標記的未驗證假設——見 PLAN.md。
#
# 為了讓 fake-backend e2e 測試可以注入假的 osascript/screencapture，所有
# 這裡的函式都透過 `$OSASCRIPT_BIN`／`$SCREENCAPTURE_BIN`（測試可覆寫成
# PATH 上的假腳本）呼叫外部命令，不寫死絕對路徑。
#
# 010（review595 必修 3，native opt-in 落在實際呼叫層）：**不再**在這裡
# 用 `${VAR:=real_default}` 預設回退成真正的 `osascript`／
# `screencapture`——006–009 這樣寫的結果是：只要呼叫端忘了設定
# `OSASCRIPT_BIN`／`SCREENCAPTURE_BIN`（例如沿用舊版 probe、或漏設
# fixture），這裡就會靜默退回真正的系統指令；main-review／review595 都
# 各自抓到一次因此意外呼叫真 osascript 的事故。
#
# 011（010 主審 N3）：006–010 的 `_native_gate` 只檢查「變數有沒有設
# 定」——`OSASCRIPT_BIN=/usr/bin/osascript`（指向真工具本身）在非 native
# 模式下因為「有值」也會被放行,完全沒檢查值指向哪裡。改成明確宣告的
# fake mode：`P0_FAKE_MODE=1` + `P0_FAKE_ROOT=<目錄>`,每個真工具變數的
# 值都要 realpath 落在 realpath(P0_FAKE_ROOT) 之內才放行；判定邏輯全部
# 移到 `lib/native_gate.py`（唯一權威來源，bash 這裡只是呼叫它,不重寫
# 一份判斷式)——`driver.sh` 的 App spawn（N1）與 `lib/cg_check.py`／
# `lib/supervise.py`（N2,程序內部自己再檢查一次）都呼叫同一份邏輯，
# 三處規則保證一致,不會各自漂移。
set -uo pipefail

# _native_gate <env-var-name> <real-tool-default>
#
# 每一個真工具呼叫點（ax_auth_probe／get_window_id／ax_dump_window_tree／
# ax_meta_candidates／screencapture_window／cg_window_owner_check）都要在
# 真的執行外部命令之前呼叫這個函式。規則（實際判定在
# `lib/native_gate.py::check_path_in_fake_root`,這裡只是呼叫它）：
#   - <env-var-name> 已設定——這個值必須通過 native_gate 的檢查（native
#     模式,或 fake 模式且 realpath 落在 P0_FAKE_ROOT 之內）才可以使用。
#   - <env-var-name> 未設定——用 <real-tool-default> 這個名稱去檢查；
#     這在 fake 模式下幾乎必然失敗（真工具名稱不會落在假根目錄內），
#     只有 native 模式才會通過。
#   - 檢查沒通過：印 `NATIVE_NOT_ALLOWED: <native_gate.py 給的原因>` 到
#     stderr、回傳 97，**完全不觸碰**任何外部指令——sentinel／假 Quartz
#     模組的呼叫計數必須因此維持 0（不能只靠「輸出沒有錯誤字串」證明）。
# stdout 印出成功時實際要用的命令字串。
_native_gate() {
  local varname="$1" real_default="$2"
  local varval="${!varname:-}"
  local candidate="${varval:-$real_default}"
  local here; here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  local reason
  if ! reason=$(python3 "$here/native_gate.py" check "$candidate" 2>&1 >/dev/null); then
    echo "NATIVE_NOT_ALLOWED: $reason" >&2
    return 97
  fi
  echo "$candidate"
  return 0
}

# ax_auth_probe <target_pid>
ax_auth_probe() {
  local target_pid="$1"
  local bin
  bin=$(_native_gate OSASCRIPT_BIN osascript) || return $?
  "$bin" -e '
    on run argv
      set targetPid to (item 1 of argv) as integer
      tell application "System Events"
        set targetProc to first process whose unix id is targetPid
        return name of targetProc
      end tell
    end run
  ' "$target_pid"
}

# get_window_id <target_pid>
#
# 剛好 1 個視窗才回傳它的 id；0 個或 >1 個都不猜，印一個以
# `MULTI_WINDOW:`／`NO_WINDOW:` 開頭的診斷字串到 stdout，交給
# classify_window_id_output 分類為 error。
#
# 004：driver.sh 的呼叫順序改成「先呼叫本函式＋cg 交叉核對確認唯一視窗，
# 確認過後才呼叫 ax_meta_text／ax_meta_title_json／ax_dump_window_tree」
# （decision.md 必修：「AX目前仍然取第一個meta、用window1；而且實際讀取
# content的動作發生在確認window唯一性之前，和註解描述的順序不符」）。本函式
# 本身的邏輯不變，只是現在保證是「內容讀取類函式」的上游前置條件，不是並行
# 或事後才驗證。
get_window_id() {
  local target_pid="$1"
  local bin
  bin=$(_native_gate OSASCRIPT_BIN osascript) || return $?
  "$bin" -e '
    on run argv
      set targetPid to (item 1 of argv) as integer
      tell application "System Events"
        tell (first process whose unix id is targetPid)
          set winCount to (count of windows)
          if winCount is 0 then
            return "NO_WINDOW:0"
          else if winCount is greater than 1 then
            set idList to {}
            repeat with i from 1 to winCount
              set end of idList to (id of window i) as string
            end repeat
            set AppleScript'"'"'s text item delimiters to ","
            return "MULTI_WINDOW:" & winCount & ":" & (idList as string)
          else
            return (id of window 1) as string
          end if
        end tell
      end tell
    end run
  ' "$target_pid"
}

# cg_window_owner_check <target_pid> <candidate_window_id>
#
# 候選的獨立交叉核對來源。010（review595 必修 4）：**移除全域 on-screen
# fallback**——舊版（004–009）在 `kCGWindowListOptionIncludingWindow` 的
# targeted 查詢空結果時會退回列舉整個桌面所有 on-screen window 再
# filter；reviewer 用假 Quartz 模組證明這個 fallback 本身可能回傳
# MATCH（見 `2026-09-29-review595/gui-boundary-results.json` 的
# `target-empty` 案），而且 targeted 查詢**唯一一筆**但
# `kCGWindowNumber` 不等於要求的 id 時，舊版也只檢查 owner、沒有檢查
# id 本身（`target-wrong-id` 案）。010 改成呼叫獨立、可單元測試的
# `lib/cg_check.py`（見該檔）：只用 targeted 查詢，必須恰好一筆、且這
# 一筆的 `kCGWindowNumber`==candidate_id 且 `kCGWindowOwnerPID`==
# target_pid 才是 MATCH；查無/多筆/id 不符/owner 不符/例外，一律
# MISMATCH（不再有第二次 `CGWindowListCopyWindowInfo` 呼叫）。
#
# 輸出（stdout 一行）：
#   MATCH                      — targeted 查詢恰好 1 筆、id 與 owner 都符合。
#   MISMATCH:targeted:empty    — targeted 查詢查無資料。
#   MISMATCH:targeted:multiple:<n> — targeted 查詢回傳多筆（防禦性，正常
#                                不該發生）。
#   MISMATCH:targeted:wrong_id:<got> — 唯一一筆但 kCGWindowNumber 不符。
#   MISMATCH:targeted:wrong_owner:<got> — 唯一一筆但 kCGWindowOwnerPID 不符。
#   MISMATCH:targeted:exception:<type> — CGWindowListCopyWindowInfo 本身丟例外。
#   QUARTZ_NOT_AVAILABLE       — 環境沒有 Quartz 模組，無法做獨立核對。
cg_window_owner_check() {
  local target_pid="$1" candidate_id="$2"
  local here; here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  # 測試 injection point：fake-backend e2e 測試沒有 Quartz/真視窗可測，
  # 用 CG_WINDOW_CHECK_CMD 換成假腳本（見 ../../tests/fixtures/）——這個
  # override 本身是一個外部命令，用跟 OSASCRIPT_BIN／SCREENCAPTURE_BIN
  # 完全相同的 `_native_gate` 規則把關。
  if [ -n "${CG_WINDOW_CHECK_CMD:-}" ]; then
    local bin
    bin=$(_native_gate CG_WINDOW_CHECK_CMD "$CG_WINDOW_CHECK_CMD") || return $?
    "$bin" "$target_pid" "$candidate_id"
    return $?
  fi
  # 沒有 override：一律呼叫 `cg_check.py`——011（N2）：native/fake-mode
  # 與 Quartz 模組來源的檢查移到它自己內部做（見該檔），這裡不重複判斷。
  # 即使有人繞過這裡、直接執行 `python3 cg_check.py`（
  # `010/main-review/attack-005-native-gate/case2` 示範的正是這個繞過
  # 路徑），一樣會被 `cg_check.py` 自己的檢查擋下，不依賴這裡的 bash 層。
  python3 "$here/cg_check.py" "$target_pid" "$candidate_id"
}

# screencapture_window <window_id> <out_png>
screencapture_window() {
  local window_id="$1" out_png="$2"
  local bin
  bin=$(_native_gate SCREENCAPTURE_BIN screencapture) || return $?
  "$bin" -x -l "$window_id" "$out_png"
}

# ax_dump_window_tree <target_pid>
#
# 004（沿用 003 思路）修正 decision.md 必修：「raw tree 目前只保存參照
# 字串，需要保存實際內容」——AppleScript 的 `entire contents of window 1`
# 回傳的是一串 UI element **參照**（object specifier），osascript 把它
# 字串化成 `{UI element 1 of window 1 of ..., ...}` 這種對後續 review 沒有
# 實質內容價值的參照字串，不是每個元素的屬性值。改成逐一走訪 entire
# contents，對每個元素嘗試讀出 role/name/value/help/title/position/size
# 這組有界屬性集合（每一項讀取都包在 try 裡；讀不到的屬性留空，不中止整個
# dump），輸出一行一個元素的
# `role=|name=|value=|help=|title=|position=|size=` 格式，這樣離線 review
# 才看得到「視窗裡真的有什麼」而不是一串記憶體參照。
#
# 呼叫端（driver.sh）只在 get_window_id 已確認恰好 1 個視窗、且 cg 交叉核對
# 為 MATCH 之後才呼叫本函式，所以這裡沿用「window 1」是安全的——driver.sh
# 的實際呼叫順序已對齊這個假設（見 driver.sh 修正）。
ax_dump_window_tree() {
  local target_pid="$1"
  local bin
  bin=$(_native_gate OSASCRIPT_BIN osascript) || return $?
  "$bin" -e '
    on run argv
      set targetPid to (item 1 of argv) as integer
      set outLines to {}
      tell application "System Events"
        tell (first process whose unix id is targetPid)
          if (count of windows) is 0 then
            error "no windows for pid " & targetPid
          end if
          set allElems to (entire contents of window 1) as list
          repeat with elem in allElems
            set roleStr to ""
            set nameStr to ""
            set valueStr to ""
            set helpStr to ""
            set titleStr to ""
            set posStr to ""
            set sizeStr to ""
            try
              set roleStr to (role of elem) as string
            end try
            try
              set nameStr to (name of elem) as string
            end try
            try
              set valueStr to (value of elem) as string
            end try
            try
              set helpStr to (help of elem) as string
            end try
            try
              set titleStr to (title of elem) as string
            end try
            try
              set posStr to (position of elem) as string
            end try
            try
              set sizeStr to (size of elem) as string
            end try
            set end of outLines to "role=" & roleStr & "|name=" & nameStr & "|value=" & valueStr & "|help=" & helpStr & "|title=" & titleStr & "|position=" & posStr & "|size=" & sizeStr
          end repeat
        end tell
      end tell
      set AppleScript'"'"'s text item delimiters to linefeed
      return outLines as string
    end run
  ' "$target_pid"
}

# ax_meta_candidates <target_pid>
#
# 007（review571 必修 4，multiple-meta）：舊版 ax_meta_text／
# ax_meta_title_json 各自在 AppleScript 裡走訪 `entire contents of
# window 1`,一遇到第一個 value 以 "ws: " 開頭的元素就直接 return——完全
# 沒有檢查「唯一性」，也讓 review571 的合成反例（放兩個都符合前綴的元素）
# 直接取到第一個。決策要求把「唯一性判定」與「屬性讀取」分開,選取邏輯移
# 到離線可測的地方，不在 AppleScript 裡就地決定，也不選 first。
#
# 本函式取代兩個函式各自的 AppleScript walk：只做**一次**走訪，收集
# **全部**符合前綴的候選,把 value 與 help 這兩個屬性從**同一個** elem 上
# 一起讀出（不是分兩次各自查一次,那樣才會有「meta 選到的元素」跟「title
# 選到的元素」實際上不保證是同一個的風險）,一行一個候選印成
# "value=<v>|help=<h>"（跟既有 ax_dump_window_tree 的分隔風格一致）。count
# 上限 10——只需要分辨「剛好 1 個」還是「0 個或多個」,不需要看到全部。
#
# 唯一性判定交給 lib/ax_candidate_select.py（見該檔），離線可用 fixture
# 測試（0／1／2 個候選、以及候選之間 help 不同的情形）,不執行 GUI。
ax_meta_candidates() {
  local target_pid="$1"
  local bin
  bin=$(_native_gate OSASCRIPT_BIN osascript) || return $?
  "$bin" -e '
    on run argv
      set targetPid to (item 1 of argv) as integer
      set outLines to {}
      set foundCount to 0
      tell application "System Events"
        tell (first process whose unix id is targetPid)
          set allElems to (entire contents of window 1) as list
          repeat with elem in allElems
            try
              set v to value of elem
              if v starts with "ws: " then
                set foundCount to foundCount + 1
                if foundCount > 10 then exit repeat
                set helpStr to ""
                try
                  set helpStr to help of elem
                end try
                set end of outLines to "value=" & v & "|help=" & helpStr
              end if
            end try
          end repeat
        end tell
      end tell
      set AppleScript'"'"'s text item delimiters to linefeed
      return outLines as string
    end run
  ' "$target_pid"
}

# _ax_select_unique_meta_candidate — 呼叫離線可測的 lib/ax_candidate_select.py。
# stdin 傳入 ax_meta_candidates 的輸出；恰好 1 個候選時 stdout 印一行
# "<value>\t<help>"、rc=0；0 個或多個候選時 rc!=0,stderr 印
# NO_CANDIDATE／MULTIPLE_CANDIDATES:<n>（不印任何猜測值）。
_ax_select_unique_meta_candidate() {
  local here; here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  python3 "$here/ax_candidate_select.py"
}

# ax_meta_text <target_pid>
#
# 007：改叫 ax_meta_candidates 取得全部候選,交給
# _ax_select_unique_meta_candidate 判定唯一性——恰好 1 個才印出它的 value；
# 0 個或多個都回傳非 0（呼叫端本來就是靠 with_timeout 的 rc==0 才信任
# stdout,這裡讓「候選不唯一」直接走這條既有的錯誤路徑,不新增呼叫端要
# 另外處理的訊號)。
ax_meta_text() {
  local target_pid="$1"
  local candidates
  candidates=$(ax_meta_candidates "$target_pid") || return $?
  local selected
  selected=$(printf '%s\n' "$candidates" | _ax_select_unique_meta_candidate) || return 1
  printf '%s\n' "$selected" | cut -f1
}

# ax_meta_title_json <target_pid>
#
# 007：跟 ax_meta_text 共用同一個選取邏輯（同一支 lib/ax_candidate_select.py，
# 同一套「恰好 1 個候選才接受」規則），value 與 help 都是從同一次
# ax_meta_candidates walk 裡同一個 elem 上讀出——在**這一次呼叫自己內部**,
# help 保證是綁定在「被判定為唯一」的那個候選上,不是分開查、各憑各的判斷。
# ax_meta_text／ax_meta_title_json 目前仍是 driver.sh 兩個獨立的
# with_timeout 呼叫（各自獨立的 osascript 行程）,不是同一次 AppleScript
# 呼叫——如果兩次呼叫之間 UI 真的發生變化,兩次各自的唯一性判定依然各自
# 正確,但不是同一個 process-level 原子讀取；driver.sh 呼叫這兩個函式時
# window 唯一性已經確認過，兩次呼叫之間沒有其他操作，預期 UI 穩定。這個
# 殘留限制記在 PLAN-delta.md，不是 review571 具體反例要求涵蓋的範圍。
ax_meta_title_json() {
  local target_pid="$1"
  local candidates
  candidates=$(ax_meta_candidates "$target_pid") || return $?
  local selected
  selected=$(printf '%s\n' "$candidates" | _ax_select_unique_meta_candidate) || return 1
  printf '%s\n' "$selected" | cut -f2
}
