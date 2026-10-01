#!/usr/bin/env python3
"""candidate/lib/ax_candidate_select.py

007 對 review571 必修 4（multiple-meta）的離線可測選取器。輸入是
lib/window.sh 的 `ax_meta_candidates` AppleScript helper 吐出的候選清單
（一行一個候選，"value=<v>|help=<h>" 格式，跟 ax_dump_window_tree 既有的
"role=...|name=..." 分隔風格一致）——恰好 1 個候選才接受，0 個或多個一律
拒絕，不選第一個（舊版 bug：一遇到第一個 value 以 "ws: " 開頭的元素就直接
return，完全沒有唯一性檢查）。

ax_meta_text／ax_meta_title_json 現在共用這支腳本、共用同一套規則，兩者在
各自呼叫內部,value 與 help 都是從同一次 AppleScript walk 裡同一個候選上
一起讀出（見 window.sh 檔頭），不是分開查、各憑各的判斷。

不執行 GUI／osascript——本檔純粹處理文字，用 fixture（見
tests/unit/test_ax_candidate_select.py）離線驗證 0／1／2 個候選，以及候選
之間 help 不同的情形。

stdin：候選清單，每行一個 "value=<v>|help=<h>"（v/h 可能為空字串）。
exit 0：stdout 印一行 "<value>\t<help>"（唯一候選存在）。
exit 1：stderr 印原因，"NO_CANDIDATE"（0 個）或
        "MULTIPLE_CANDIDATES:<n>"（n>=2 個）；stdout 不印任何猜測值。
"""
import sys


def parse_line(line):
    """把一行 "value=<v>|help=<h>" 拆回 (v, h)。用最後一次出現的 "|help="
    當分割點（比對 window.sh 的 AppleScript emitter 固定順序：先 value 後
    help），不是天真的 str.split("|")——這樣 value 文字裡如果剛好含有
    "|" 也不會誤切。"""
    if not line.startswith("value="):
        raise ValueError(f"malformed candidate line (missing 'value=' prefix): {line!r}")
    rest = line[len("value="):]
    marker = "|help="
    idx = rest.rfind(marker)
    if idx == -1:
        raise ValueError(f"malformed candidate line (missing '|help=' field): {line!r}")
    return rest[:idx], rest[idx + len(marker):]


def select_unique(lines):
    """回傳 (value, help) 或 (None, reason)。reason 是
    "NO_CANDIDATE" 或 "MULTIPLE_CANDIDATES:<n>"。"""
    candidates = [parse_line(l) for l in lines if l.strip() != ""]
    if len(candidates) == 0:
        return None, "NO_CANDIDATE"
    if len(candidates) > 1:
        return None, f"MULTIPLE_CANDIDATES:{len(candidates)}"
    return candidates[0], None


def main():
    lines = sys.stdin.read().splitlines()
    try:
        result, err = select_unique(lines)
    except ValueError as e:
        print(f"MALFORMED_INPUT:{e}", file=sys.stderr)
        return 1
    if err:
        print(err, file=sys.stderr)
        return 1
    v, h = result
    print(f"{v}\t{h}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
