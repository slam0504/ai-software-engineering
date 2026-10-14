#!/usr/bin/env python3
"""candidate/lib/cg_check.py — 010 對 review595 必修 4，011 對 N2 的實作。

從 006–009 版 `window.sh` 的 `cg_window_owner_check()` inline python
heredoc 抽出成獨立、可 import 的模組。

**011（010 主審 N2）**：010 版只靠 `window.sh` 的 bash 層 gate 保護這個
檔案——`010/main-review/attack-005-native-gate/case2` 示範：直接執行
`python3 cg_check.py <target_pid> <candidate_id>`（完全繞過 bash 與
`window.sh`），沒有 `P0_ALLOW_NATIVE=1`、也沒有宣告 fake mode，假 Quartz
模組仍然被 import 且被呼叫一次——如果是真正的 pyobjc Quartz，就會真的
查詢視窗伺服器。011 把檢查移進 `check()` 這個函式本身（不是只在
`main()`）——**在 `import Quartz` 之前**呼叫
`_quartz_import_allowed()`：

  - native 模式（`P0_ALLOW_NATIVE=="1"`，且沒有同時設定 `P0_FAKE_MODE`）
    → 放行，不做路徑限制（本輪邊界不執行這條路徑）。
  - fake 模式（`P0_FAKE_MODE=="1"` 且 `P0_FAKE_ROOT` 合法）→ 用
    `importlib.util.find_spec("Quartz")`（**不會執行模組本身**，只是
    定位它會從哪裡載入）取得 `spec.origin`，驗證這個路徑的 realpath 落在
    `P0_FAKE_ROOT` 之內，才允許接下來真的 `import Quartz`。
  - 都不成立 → 拒絕（`MISMATCH:native_not_allowed:...`），完全不
    `import Quartz`。

判定邏輯本身共用 `lib/native_gate.py`（唯一權威來源，`window.sh` 的
`_native_gate`／`driver.sh` 的 App spawn 檢查都呼叫同一份），不在這裡
重寫一份規則。

單元測試若要直接 `import` 這個模組測邏輯（不透過 `main()`／CLI），一樣
會經過 `check()` 內建的這道檢查——測試需要明確設定
`P0_FAKE_MODE=1`／`P0_FAKE_ROOT=<假 Quartz 模組所在目錄>`，讓
`_quartz_import_allowed()` 確認「即將載入的 Quartz 就是那個假模組」才會
放行；不會因為是被 import 呼叫就跳過這道檢查（沒有一條不經 gate、就能
呼叫真 Quartz 的路徑）。

review595 必修 4：移除全域 on-screen fallback。targeted 查詢
（`kCGWindowListOptionIncludingWindow`）必須**恰好一筆**資料，且這一筆的
`kCGWindowNumber` 等於要求的 `candidate_id`、`kCGWindowOwnerPID` 等於
`target_pid`，才是 MATCH。以下情況一律不得擴大查詢（不再呼叫第二次
`CGWindowListCopyWindowInfo`），只回報 NOT_AVAILABLE 或 MISMATCH（呼叫端
`window.sh`／`driver.sh` 把兩者分別對應到 `not_available`／`error`，都不
capture）：
  - gate 拒絕 → `MISMATCH:native_not_allowed:<原因>`
  - `import Quartz` 失敗（gate 通過後仍然找不到／載入失敗） → `QUARTZ_NOT_AVAILABLE`
  - 查無資料（0 筆） → `MISMATCH:targeted:empty`
  - 多筆資料 → `MISMATCH:targeted:multiple:<n>`
  - 唯一一筆但 `kCGWindowNumber` 不等於 `candidate_id` → `MISMATCH:targeted:wrong_id:<got>`
  - 唯一一筆但 `kCGWindowOwnerPID` 不等於 `target_pid` → `MISMATCH:targeted:wrong_owner:<got>`
  - `CGWindowListCopyWindowInfo` 本身丟例外 → `MISMATCH:targeted:exception:<type>`
  - 唯一一筆、id 與 owner 都相符 → `MATCH`

stdout 只印一行；`main()` 的 exit code：gate 拒絕時 97（比照 `window.sh`
的 `_native_gate` 慣例），其餘情況 0（結果本身在 stdout，由呼叫端依內容
分類——這個慣例從 006 沿用不變）。
"""
import os
import sys
from pathlib import Path

# 自己把所在目錄加進 sys.path——不論這個檔案是被當 CLI 執行、被
# `import cg_check` 正常匯入，還是被測試用
# `importlib.util.spec_from_file_location` 動態載入,都能找到同目錄的
# `native_gate.py`。
sys.path.insert(0, str(Path(__file__).resolve().parent))
from native_gate import check_path_in_fake_root  # noqa: E402


def _quartz_import_allowed():
    """N2：在 `import Quartz` 之前呼叫。回傳 (allowed: bool,
    reason: str|None)。"""
    env = os.environ
    native = env.get("P0_ALLOW_NATIVE", "") == "1"
    fake = env.get("P0_FAKE_MODE", "") == "1"
    if native and fake:
        return False, "P0_ALLOW_NATIVE=1 and P0_FAKE_MODE=1 are mutually exclusive"
    if native:
        return True, None
    if not fake:
        return False, "neither P0_ALLOW_NATIVE=1 nor P0_FAKE_MODE=1 is set"
    import importlib.util
    try:
        spec = importlib.util.find_spec("Quartz")
    except Exception as e:  # noqa: BLE001
        return False, f"find_spec('Quartz') failed: {type(e).__name__}: {e}"
    if spec is None or not spec.origin:
        # 根本沒有可載入的 Quartz 模組——不是 gate 拒絕，是「這個環境沒有
        # 這個模組」，交給呼叫端走既有的 QUARTZ_NOT_AVAILABLE 語意。
        return True, None
    allowed, reason = check_path_in_fake_root(spec.origin, env)
    if not allowed:
        return False, f"resolved Quartz module {spec.origin!r}: {reason}"
    return True, None


def check(target_pid, candidate_id):
    """回傳 (status, detail)。status 是 "MATCH"／"MISMATCH"／
    "NOT_AVAILABLE" 之一；detail 是要印到 stdout 的完整字串。**這個函式
    本身就內建 N2 的 gate**——不論是透過 `main()` 呼叫，還是被測試／其他
    程式碼直接 `import` 呼叫，都會先經過 `_quartz_import_allowed()`。"""
    allowed, reason = _quartz_import_allowed()
    if not allowed:
        return "MISMATCH", f"MISMATCH:native_not_allowed:{reason}"

    try:
        import Quartz
    except Exception:
        return "NOT_AVAILABLE", "QUARTZ_NOT_AVAILABLE"

    try:
        targeted_opts = Quartz.kCGWindowListOptionIncludingWindow
        info = Quartz.CGWindowListCopyWindowInfo(targeted_opts, candidate_id)
    except Exception as e:
        return "MISMATCH", f"MISMATCH:targeted:exception:{type(e).__name__}"

    if info is None:
        info = []
    n = len(info)
    if n == 0:
        return "MISMATCH", "MISMATCH:targeted:empty"
    if n > 1:
        return "MISMATCH", f"MISMATCH:targeted:multiple:{n}"

    w = info[0]
    got_id = w.get("kCGWindowNumber")
    got_owner = w.get("kCGWindowOwnerPID")
    if got_id != candidate_id:
        return "MISMATCH", f"MISMATCH:targeted:wrong_id:{got_id}"
    if got_owner != target_pid:
        return "MISMATCH", f"MISMATCH:targeted:wrong_owner:{got_owner}"
    return "MATCH", "MATCH"


def main():
    if len(sys.argv) != 3:
        print("usage: cg_check.py <target_pid> <candidate_id>", file=sys.stderr)
        return 2
    try:
        target_pid = int(sys.argv[1])
    except ValueError:
        target_pid = None
    try:
        candidate_id = int(sys.argv[2])
    except ValueError:
        # 沒有可用的數字 id 可以做 targeted 查詢——不得擴大查詢,直接回報
        # 一個以 MISMATCH 開頭的字串(呼叫端會歸類為 error,不 capture)。
        print(f"MISMATCH:targeted:unparseable_id:{sys.argv[2]!r}")
        return 0
    _status, detail = check(target_pid, candidate_id)
    print(detail)
    prefix = "MISMATCH:native_not_allowed:"
    if detail.startswith(prefix):
        print(f"NATIVE_NOT_ALLOWED: {detail[len(prefix):]}", file=sys.stderr)
        return 97
    return 0


if __name__ == "__main__":
    sys.exit(main())
