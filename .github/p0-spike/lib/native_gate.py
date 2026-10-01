#!/usr/bin/env python3
"""candidate/lib/native_gate.py — 013 修 012 主審 N5（大小寫變體繞過 gate）。

012 已經修好 011 主審的 N4（P0_FAKE_ROOT 系統目錄／$HOME 過寬／bundle
路徑本身／真工具路徑的四層防線），但 012/main-review/FINDINGS.md 的
attack-007-case 指出：macOS（APFS 預設）的檔案系統不分大小寫,但
`os.path.realpath` 不會把大小寫正規化,而 012 的所有比對都是區分大小寫
的字串比對。下面幾種組合都被**錯誤允許**：

  - `.../sdlc-workbench.APP/Contents/MacOS/sdlc-workbench`（真 App，只是
    `.app` 拼成 `.APP`）
  - root=/USR/sbin，檢查 /USR/sbin/screencapture（真工具，只是路徑全大寫）
  - root=/USR/BIN，檢查 /USR/BIN/osascript（同上）
  - root=/LIBRARY/FRAMEWORKS，檢查真的 pyobjc Quartz 的 __init__.py
    （`.framework` 拼成 `.FRAMEWORK`）

本輪修法（N5）全部只加在這個共用判定模組——bash／python 都呼叫這裡，
仍然是唯一判定來源。

## N5 修法

  1. **路徑字串比對全部改成不分大小寫**：系統目錄前綴、$HOME 與
     ~/Library、~/Applications 的關係、bundle 副檔名，一律先對兩邊的
     字串做 `str.casefold()` 再比較。root 包含關係額外加上一層**檔案
     身分比對**（`os.stat` 的 `(st_dev, st_ino)`），從被檢查路徑逐層
     往上走訪祖先目錄，只要任一祖先與 root 的身分相同就算「在 root 之
     內」——這樣同時處理大小寫變體與 firmlink（/System/Volumes/Data/...
     這類 macOS 內部的卷冊別名，實體檔案身分相同但路徑字串不同）。
  2. **真工具比對改用檔案身分（`os.path.samefile`），不再用字串比對**：
     只要被檢查路徑與 `shutil.which("osascript")`／
     `shutil.which("screencapture")` 解析出的檔案是同一個檔案（含大小寫
     變體、hardlink、symlink 間接指向），就拒絕。
  3. **App bundle 偵測加兩道，不只副檔名**：
     a. 副檔名比對本身也改成不分大小寫（casefold）。
     b. 從被檢查路徑逐層往上，只要任一祖先目錄底下有
        `Contents/Info.plist`（用 `os.path.isfile` 讓作業系統自己處理
        大小寫，不用手動 casefold 判斷檔名是否存在），或路徑本身符合
        `*/Contents/MacOS/*` 結構（逐段 casefold 比對），就視為 bundle
        內的執行檔，拒絕。
  4. **Quartz origin** 走的是同一套 `check_path_in_fake_root`,不需要
     額外程式碼——`.framework`（不分大小寫）與系統前綴（不分大小寫）這
     兩條規則本來就會套用到它。

CLI 用法（供 bash 呼叫，行為不變）：
    python3 native_gate.py check <path>
成功：exit 0，stdout 印 `OK`。
失敗：exit 1，stderr 印明確原因（不印 `OK`）。
"""
import os
import shutil
import sys

# P0_FAKE_ROOT 的 realpath casefold 之後，若等於下列任一目錄，或位於其
# 底下，一律拒絕。
_SYSTEM_ROOT_PREFIXES = tuple(
    p.casefold() for p in (
        "/usr", "/bin", "/sbin", "/System", "/Library", "/Applications",
        "/opt", "/private", "/Volumes", "/cores", "/dev", "/etc", "/var", "/tmp",
    )
)

# 被檢查路徑（不是 root）自己的 realpath，任一段路徑名稱 casefold 之後
# 以下列（已 casefold）結尾就拒絕。
_DANGEROUS_PATH_SUFFIXES = tuple(
    s.casefold() for s in (".app", ".framework", ".bundle", ".appex", ".xpc")
)

# 真工具比對用的名字（本身不含大小寫問題，是 shutil.which 的查詢關鍵字）。
_REAL_TOOL_NAMES = ("osascript", "screencapture")

_SEP_CF = os.sep.casefold()


def _cf(s: str) -> str:
    """統一的 casefold 入口，方便之後如果要換更嚴謹的正規化（例如加上
    unicodedata.normalize）時只改一個地方。"""
    return s.casefold()


def _real_home():
    try:
        return os.path.realpath(os.path.expanduser("~"))
    except Exception:  # noqa: BLE001
        return None


def _cf_equal_or_under(candidate_cf: str, base_cf: str) -> bool:
    return candidate_cf == base_cf or candidate_cf.startswith(base_cf + _SEP_CF)


def _is_system_root(realroot: str) -> bool:
    cf_root = _cf(realroot)
    if cf_root == _cf("/"):
        return True
    for prefix_cf in _SYSTEM_ROOT_PREFIXES:
        if _cf_equal_or_under(cf_root, prefix_cf):
            return True
    # ~/Library、~/Applications 本身或其底下任何路徑，即使不在上面的
    # 系統前綴清單裡，也要單獨擋下（大小寫不分）。
    home = _real_home()
    if home:
        cf_home = _cf(home)
        for sub in ("Library", "Applications"):
            home_sub_cf = _cf(os.path.join(home, sub))
            if _cf_equal_or_under(cf_root, home_sub_cf):
                return True
    return False


def _is_home_or_home_ancestor(realroot: str) -> bool:
    """realroot 等於 $HOME，或是 $HOME 的祖先目錄（例如 /Users），都算
    過寬,一律拒絕（大小寫不分）。"""
    home = _real_home()
    if not home:
        return False
    cf_home = _cf(home)
    cf_root = _cf(realroot)
    return cf_home == cf_root or cf_home.startswith(cf_root + _SEP_CF)


def _contains_dot_app(realroot: str) -> bool:
    """P0_FAKE_ROOT 本身，或它底下第一層，只要有一段路徑名稱／項目
    casefold 之後以 `.app` 結尾，就視為含有 *.app bundle，拒絕。維持
    011/012 的非遞迴寫法，不對大目錄做遞迴掃描；有了「被檢查路徑本身」
    的後綴／bundle 結構檢查之後，這裡不再是主要防線，只是額外一層。"""
    app_suffix_cf = _cf(".app")
    if any(_cf(part).endswith(app_suffix_cf) for part in realroot.split(os.sep) if part):
        return True
    try:
        for entry in os.scandir(realroot):
            if _cf(entry.name).endswith(app_suffix_cf):
                return True
    except OSError:
        pass
    return False


def _path_has_dangerous_suffix(realpath_str: str) -> bool:
    """被檢查路徑（不是 root）自己的 realpath，任一段路徑名稱 casefold
    之後以 `.app`/`.framework`/`.bundle`/`.appex`/`.xpc` 結尾,就視為指向
    系統 bundle 的一部分,拒絕。"""
    for part in realpath_str.split(os.sep):
        if not part:
            continue
        cf_part = _cf(part)
        for suffix_cf in _DANGEROUS_PATH_SUFFIXES:
            if cf_part.endswith(suffix_cf):
                return True
    return False


def _has_info_plist_ancestor(realpath_str: str) -> bool:
    """從被檢查路徑逐層往上走訪，只要任一祖先目錄底下有
    `Contents/Info.plist`，就視為 bundle 內的執行檔。用
    `os.path.isfile` 讓作業系統自己處理大小寫（不分大小寫的檔案系統上，
    不論怎麼拼 Contents/Info.plist 都會查到同一個檔案），比手動 casefold
    字串比對更貼近檔案系統實際行為。"""
    current = os.path.normpath(realpath_str)
    seen = set()
    while True:
        candidate = os.path.join(current, "Contents", "Info.plist")
        try:
            if os.path.isfile(candidate):
                return True
        except OSError:
            pass
        parent = os.path.dirname(current)
        if parent == current or parent in seen:
            break
        seen.add(parent)
        current = parent
    return False


def _matches_contents_macos_structure(realpath_str: str) -> bool:
    """路徑本身（不看檔案系統，只看字串結構）是否符合
    `*/Contents/MacOS/*`（不分大小寫，逐段比對相鄰兩段）。"""
    parts = [p for p in realpath_str.split(os.sep) if p]
    for i in range(len(parts) - 1):
        if _cf(parts[i]) == _cf("Contents") and _cf(parts[i + 1]) == _cf("MacOS"):
            return True
    return False


def _is_app_bundle_path(realpath_str: str) -> bool:
    return _has_info_plist_ancestor(realpath_str) or _matches_contents_macos_structure(realpath_str)


def _samefile_safe(a: str, b: str) -> bool:
    try:
        return os.path.samefile(a, b)
    except OSError:
        return False


def _matches_real_tool_path(realpath_str: str):
    """被檢查路徑是否與 `shutil.which("osascript")`／
    `shutil.which("screencapture")` 解析出的檔案是**同一個檔案**（用
    `os.path.samefile`，也就是比對 `(st_dev, st_ino)`，不是字串比對）。
    這樣大小寫變體、hardlink、或透過 symlink 間接指到真工具，都會被
    正確判定成同一個檔案。回傳工具名稱；都不是則回傳 None。"""
    for name in _REAL_TOOL_NAMES:
        which = shutil.which(name)
        if not which:
            continue
        if _samefile_safe(realpath_str, which):
            return name
    return None


def _stat_identity_ancestor_match(realpath_str: str, realroot: str) -> bool:
    """檔案身分比對：從被檢查路徑逐層往上走訪，只要任一祖先與 root 的
    `(st_dev, st_ino)` 相同，就算「在 root 之內」——同時處理大小寫變體
    （不分大小寫的檔案系統上，不同拼法的同一路徑 stat 出來是同一個
    inode）與 firmlink（/System/Volumes/Data/... 這類卷冊別名，實體檔案
    身分相同）。

    014 修法（013 主審 blocker）：fail closed——target 本身必須實際
    存在；root 或 target 任一 stat 失敗（不存在、權限問題等），一律
    回傳 False（拒絕），不再「跳過這一層、試下一層祖先」。這是**唯一**
    的放行依據，不再有 casefold 字串比對這個備援放行路徑（見
    `_path_under_root` 的修訂說明）。"""
    try:
        root_stat = os.stat(realroot)
    except OSError:
        return False
    try:
        os.stat(realpath_str)
    except OSError:
        return False
    current = realpath_str
    seen = set()
    while True:
        try:
            cur_stat = os.stat(current)
        except OSError:
            return False
        if (cur_stat.st_dev, cur_stat.st_ino) == (root_stat.st_dev, root_stat.st_ino):
            return True
        parent = os.path.dirname(current)
        if parent == current or parent in seen:
            break
        seen.add(parent)
        current = parent
    return False


def _path_under_root(realpath_str: str, realroot: str) -> bool:
    """root 包含關係：**只**以檔案身分（`(st_dev, st_ino)` 逐層祖先比對）
    為放行依據。

    014 修法（013 主審 blocker，medium）：舊版先做 casefold 字串前綴
    比對，命中就直接放行，完全不會呼叫身分比對——在大小寫敏感的 volume
    上，這會放行 root 之外、只是「大小寫拼法剛好是 root 前綴」但其實是
    不同 inode 的兄弟目錄（例如 root=`/Volumes/CaseSensitive/FakeRoot`
    而 target=`/Volumes/CaseSensitive/fakeroot/tool`——`commonpath` 其實
    是 `/Volumes/CaseSensitive`，兩者不是父子關係，但 casefold 字串比對
    會誤判為「fakeroot 是 FakeRoot 的大小寫變體」而放行）。

    修法：拿掉 casefold 字串比對這個「短路放行」路徑，包含關係**只**由
    `_stat_identity_ancestor_match` 決定——target 必須存在，且必須能
    透過檔案身分逐層往上比對，證明它確實位於 root 之內（或就是 root 本
    身）。casefold 字串比對留在其他函式（`_is_system_root`／
    `_is_home_or_home_ancestor`／`_path_has_dangerous_suffix`／
    `_is_app_bundle_path`）當**額外的拒絕條件**——這些函式的 casefold
    命中只會讓判定更嚴格（多拒絕一些原本可能被身分比對放行的路徑），
    不會讓任何原本該被拒絕的路徑通過，所以維持 casefold 是安全的；唯獨
    「root 包含關係」這個會決定 ALLOW 的判斷，不能再讓 casefold 字串
    比對單獨拍板。"""
    return _stat_identity_ancestor_match(realpath_str, realroot)


def fake_root_from_env(env=None):
    """回傳 (ok: bool, realroot: str|None, reason: str|None)。"""
    env = env if env is not None else os.environ
    root = env.get("P0_FAKE_ROOT", "")
    if not root:
        return False, None, "P0_FAKE_ROOT is not set"
    try:
        realroot = os.path.realpath(root)
    except Exception as e:  # noqa: BLE001
        return False, None, f"P0_FAKE_ROOT realpath failed: {type(e).__name__}: {e}"
    if not os.path.isdir(realroot):
        return False, None, f"P0_FAKE_ROOT does not resolve to an existing directory: {realroot!r}"
    if _is_system_root(realroot):
        return False, None, f"P0_FAKE_ROOT resolves to a system directory (or a path under one), refusing: {realroot!r}"
    if _is_home_or_home_ancestor(realroot):
        return False, None, f"P0_FAKE_ROOT resolves to $HOME or an ancestor of $HOME, refusing (too broad): {realroot!r}"
    if _contains_dot_app(realroot):
        return False, None, f"P0_FAKE_ROOT resolves to a path containing a *.app bundle, refusing: {realroot!r}"
    return True, realroot, None


def check_path_in_fake_root(path, env=None):
    """單一權威判定。回傳 (allowed: bool, reason: str|None)。

    `path` 是要驗證的外部工具路徑（APP_BIN／OSASCRIPT_BIN／
    SCREENCAPTURE_BIN／CG_WINDOW_CHECK_CMD 的值，或 supervise.py app 模式
    的 argv[0]，或已經解析到的 Quartz 模組 origin 路徑）。native 模式下
    這個參數被忽略（不做路徑限制）。
    """
    env = env if env is not None else os.environ
    native = env.get("P0_ALLOW_NATIVE", "") == "1"
    fake = env.get("P0_FAKE_MODE", "") == "1"
    if native and fake:
        return False, "P0_ALLOW_NATIVE=1 and P0_FAKE_MODE=1 are mutually exclusive"
    if native:
        return True, None
    if not fake:
        return False, "neither P0_ALLOW_NATIVE=1 nor P0_FAKE_MODE=1 is set"
    ok, realroot, reason = fake_root_from_env(env)
    if not ok:
        return False, reason
    if not path:
        return False, "path to check is empty"
    try:
        realpath = os.path.realpath(path)
    except Exception as e:  # noqa: BLE001
        return False, f"realpath({path!r}) failed: {type(e).__name__}: {e}"
    # 第二層防線：被檢查路徑本身像不像系統 bundle 的一部分（不看 root）
    # ——副檔名（不分大小寫）,或 Info.plist／Contents/MacOS 結構。
    if _path_has_dangerous_suffix(realpath):
        return False, (
            f"{path!r} resolves to {realpath!r}, which contains a bundle-like "
            f"path component (.app/.framework/.bundle/.appex/.xpc, case-insensitive), refusing"
        )
    if _is_app_bundle_path(realpath):
        return False, (
            f"{path!r} resolves to {realpath!r}, which is inside an application "
            f"bundle (Contents/Info.plist ancestor or */Contents/MacOS/* structure), refusing"
        )
    # 第三層防線：這就是機器上那個真工具本身（用檔案身分比對，不是字串）。
    real_tool_name = _matches_real_tool_path(realpath)
    if real_tool_name is not None:
        return False, (
            f"{path!r} resolves to {realpath!r}, which is the same file as the "
            f"real system {real_tool_name!r} tool (matched by file identity), refusing"
        )
    # 第一層防線：root 本身是否合法＋被檢查路徑是否落在 root 之內
    # （014 修法：只以檔案身分比對為放行依據，見 `_path_under_root`）。
    if not _path_under_root(realpath, realroot):
        return False, f"{path!r} resolves to {realpath!r}, which is outside P0_FAKE_ROOT {realroot!r}"
    return True, None


def main(argv=None):
    argv = argv if argv is not None else sys.argv[1:]
    if len(argv) != 2 or argv[0] != "check":
        print("usage: native_gate.py check <path>", file=sys.stderr)
        return 2
    allowed, reason = check_path_in_fake_root(argv[1])
    if allowed:
        print("OK")
        return 0
    print(reason or "denied", file=sys.stderr)
    return 1


if __name__ == "__main__":
    sys.exit(main())
