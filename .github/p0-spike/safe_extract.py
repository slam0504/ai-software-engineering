#!/usr/bin/env python3
"""candidate/safe_extract.py

Safe ZIP+inner-tar extraction for the B3b P0 spike workflow candidate.

Validates every member of the outer ZIP and the inner .tgz BEFORE
extracting anything, using only the Python standard library (zipfile,
tarfile) -- never executes anything inside the bundle. Rejects:
  - absolute member paths
  - ".." path-traversal components (after normalization)
  - symlink targets that resolve outside the extraction root (normalized
    relative to the *link's own directory*, matching how the OS actually
    resolves a relative symlink)
  - hardlink targets that resolve outside the extraction root (normalized
    relative to the *archive root* and required to name another member
    that is actually present in this archive -- tar hardlink semantics,
    NOT the same as a symlink's own-directory-relative resolution; see
    check_hardlink_target doc)
  - device files (character/block) and FIFOs
  - any member whose path has a prefix matching another archive member
    that is itself declared as a symlink (see check_no_symlink_prefix --
    this is the R7 fix, see below)
  - any member whose destination path, once existing filesystem entries
    (pre-existing in the output directory, OR created earlier in this
    same extraction run) are resolved, would land outside the extraction
    root -- re-checked immediately before EACH member lands, not just
    once upfront (see _verify_containment doc)

Legitimate relative symlinks that stay inside the archive (e.g. npm's
`.bin/foo -> ../pkg/bin/foo.js`) are allowed; only genuine escapes are
rejected.

review564 CHANGES_REQUIRED (decision.md 「保存與 extractor」) 必修對照
（002 -> 004，已修正，此輪沿用不變）：

  1. hardlink 的語意沒有處理：舊版對 symlink 和 hardlink 都套用同一個
     check_link_target()，把 hardlink 的 linkname 當成「相對於 link 自己
     所在目錄」解析——這是 symlink 的語意，不是 hardlink 的。tar 格式裡
     hardlink（LNKTYPE）的 linkname 欄位指的是另一個 archive member 的
     name，是相對 archive root（不是相對這個 member 的目錄）解析，而且
     必須真的對應到 archive 裡存在的另一個 member。本檔拆成
     check_symlink_target()（相對 link 目錄）與 check_hardlink_target()
     （相對 archive root，且必須命中已知 member 名稱集合）兩個函式。

  2. 沒有 filter 時會 fallback 到完全不設防的 extractall：舊版一律靜默
     退回無 filter 的 extractall。本檔完全不呼叫 tf.extractall()：每個
     已經通過前置檢查的 member，逐一用 tf.extractfile()／自行 mkdir／
     os.symlink()／os.link() 手動落地。

007 修正（review571 `tar_raw_dotdot`／`zip_final_symlink`，見
`/Users/eason_tseng/b3a-evidence/2026-09-28-review571/decision.md` 必修 2）：

  d. tar_raw_dotdot：舊版 check_member_path 只檢查**正規化後**的路徑有沒有
     `..`——member name `d/../escape.txt`（先有一個 symlink member
     `d -> .`）正規化後變成 `escape.txt`，靜態檢查與 _verify_containment
     都通過；但 _manual_extract_member 實際落地時用的是**原始**
     `m.name`，`os.path.join(out_dir, "d/../escape.txt")` 在檔案系統層級
     會先經過真正存在的 symlink `d`（resolve 成 out_dir 自己），再套用
     `..`，結果落到 out_dir 外面。修法：(i) check_member_path 新增對
     **原始（正規化前）** path component 的檢查——任一 component 字面等於
     `..` 就直接拒絕（這是 member **名稱**欄位的規則，跟合法 symlink
     **target** 欄位裡的 `../`（例如 npm 的 `../pkg/bin/foo.js`）是不同
     欄位，不受影響）；(ii) 落地時不再對 dest 用 `os.path.join(out_dir,
     m.name)` 這種原始字串,改用 `_normalized_relparts()`
     算出的、跟 `_verify_containment()` 驗證時同一組正規化路徑分量重建
     dest（hardlink 的 `m.linkname` 同理），讓「驗證用的值」與「實際落地
     用的值」是同一個推導結果，不是兩份可能各自偏移的字串。

  e. zip_final_symlink：舊版 `safe_extract_zip()` 呼叫 `zf.extract()`
     落地內層 .tgz member——如果目的位置（`out_dir/<member>`）事先已經是
     一個 symlink（例如指到 review 案例自己的 owned-target.txt），
     `zf.extract()` 的實作會跟隨這個既有 symlink 寫入，把目標檔案覆寫掉；
     `_verify_containment()` 原本只檢查中間路徑分量與父目錄，從不檢查
     member 自己「最後一段」目的地是否已經是既存項目。修法：要求
     `--zip-out` 指定的輸出目錄**全新**（不存在或存在但是空目錄，否則直接
     拒絕）；落地前額外用 `os.path.lexists(dest)` 檔一次「這個確切位置」，
     已存在（不論是不是 symlink）就拒絕；不再呼叫 `zf.extract()`，改成
     `zf.open()` 手動串流讀取 + `os.open(dest, O_WRONLY|O_CREAT|O_EXCL|
     O_NOFOLLOW)` 落地（跟 tar 的一般檔案 member 用同一套「不跟隨、不覆寫」
     手法）。

005 main-review R7 修正（`../p0-candidate-004/main-review/
attack-002-extract/probe.py` 的 chain_escape 案；證據見
`../p0-candidate-004/main-review/FINDINGS.md` R7 一列）：

004 版的 _verify_containment 是解包前一次性把全部 member 驗證完，落地時
不再重新檢查，而且一般檔案的 open() 會跟隨最後一段路徑上已存在的 symlink。
攻擊手法：archive 內先放一個 symlink member `x/y -> ..`，再放
`x/y/q -> ../outside.txt`（這個 member 自己的詞法檢查會通過，因為它是相對
「宣告的」目錄 `x/y` 解析，看起來仍在 archive root 內），最後放一個一般
檔案 member `q`。因為 `x/y` 在落地當下已經是真正的 symlink（指到 `..`），
作業系統建立 `x/y/q` 這個 symlink 時，實際落地位置被重新導向成
`out_dir/q`（不是 `out_dir/x/y/q`）；隨後 `q` 這個一般檔案 member 若直接
`open(out_dir/"q", "wb")`，會跟隨這個剛被放置在那裡的 symlink，把內容寫到
`out_dir` 之外。修法（決策文件提供的簡化規則 + 落地時重新檢查 + O_EXCL）：

  a. check_no_symlink_prefix()：任一 member 的路徑，只要有任何前綴等於
     archive 內宣告為 symlink 的另一個 member 名稱，直接拒絕（`x/y/q` 的
     前綴 `x/y` 是宣告的 symlink member，`x/y/q` 本身在驗證階段就被拒絕，
     整條攻擊鏈斷在這裡，不需要在落地當下才發現重新導向）。這是決策文件
     提供的「更簡單的嚴格規則」，取代嘗試在落地當下即時解析每個 symlink
     鏈段的複雜作法，同時涵蓋 hardlink 經由 symlink 父層的變體（hardlink
     member 的路徑一樣受這條規則約束）。
  b. _verify_containment() 現在在解包時、逐一 member 落地**前**重新對
     「當下」的檔案系統重跑一次（不是解包前一次性跑完就不再檢查），涵蓋
     (a) 無法涵蓋的情境——例如這次執行前就留在 out_dir 裡的殘留 symlink，
     或既存 outdir 內符合 (a) 規則以外的 symlink 鏈。
  c. 一般檔案與 hardlink member：落地前若目的路徑已經存在（不論是不是
     symlink），一律拒絕，不嘗試覆寫或跟隨；一般檔案改用
     `os.open(..., O_WRONLY|O_CREAT|O_EXCL|O_NOFOLLOW)` 建立（而不是
     `open(dest, "wb")`），確保就算檢查與寫入之間出現時間差，作業系統
     本身也不會跟隨一個突然冒出來的 symlink（O_EXCL 保證目的地此刻真的
     不存在，O_NOFOLLOW 額外拒絕 dangling symlink 的邊界情況）。
"""
import argparse
import os
import posixpath
import sys
import tarfile
import zipfile


def fail(msg):
    print(f"::error::{msg}", file=sys.stderr)
    sys.exit(1)


def check_member_path(name: str, context: str):
    if name.startswith("/") or (len(name) > 1 and name[1] == ":"):
        fail(f"{context}: absolute path member rejected: {name!r}")
    # 007 (tar_raw_dotdot)：先檢查**原始（正規化前）**的 path component——
    # `d/../escape.txt` 正規化後會變成 `escape.txt`,把 `..` 這個 component
    # 完全隱藏掉,但實際落地（見 _manual_extract_member）用的是原始字串,
    # 如果 `d` 剛好是這個 archive 裡宣告的 symlink,`..` 就會真的在檔案系統
    # 層級生效。這裡只檢查 member **名稱**欄位,不影響 symlink target 欄位
    # 合法使用 `../`（見 check_symlink_target，是不同欄位）。
    raw_parts = name.split("/")
    if ".." in raw_parts:
        fail(f"{context}: member name contains a raw '..' path component (rejected even though normalization could hide it downstream): {name!r}")
    norm = posixpath.normpath(name)
    if norm == ".." or norm.startswith("../"):
        fail(f"{context}: path-traversal member rejected: {name!r} (normalized {norm!r})")


def check_symlink_target(name: str, link_target: str, context: str):
    """Symlink target resolved relative to the link's OWN directory --
    matches how the OS actually resolves a relative symlink. Legitimate
    npm-style `.bin/foo -> ../pkg/bin/foo.js` targets are allowed; only
    targets that normalize outside the archive root are rejected."""
    if link_target.startswith("/"):
        fail(f"{context}: absolute symlink target rejected: {name!r} -> {link_target!r}")
    member_dir = posixpath.dirname(name)
    resolved = posixpath.normpath(posixpath.join(member_dir, link_target))
    if resolved == ".." or resolved.startswith("../") or resolved.startswith("/"):
        fail(f"{context}: symlink target escapes archive root: {name!r} -> {link_target!r} (resolved {resolved!r})")


def check_hardlink_target(name: str, link_target: str, context: str, known_member_names):
    """Hardlink target resolved relative to the ARCHIVE ROOT (tar LNKTYPE
    semantics). Must land inside the archive AND must actually name a
    member present in this archive."""
    if link_target.startswith("/"):
        fail(f"{context}: absolute hardlink target rejected: {name!r} -> {link_target!r}")
    resolved = posixpath.normpath(link_target)
    if resolved == ".." or resolved.startswith("../") or resolved.startswith("/"):
        fail(f"{context}: hardlink target escapes archive root: {name!r} -> {link_target!r} (resolved {resolved!r})")
    if resolved not in known_member_names:
        fail(f"{context}: hardlink target does not name a known archive member: {name!r} -> {link_target!r} (resolved {resolved!r})")


def check_no_symlink_prefix(name: str, symlink_member_names, context: str):
    """R7: reject any member whose normalized path has a proper prefix
    that is itself a DECLARED symlink member in this archive. A symlink
    member can be silently re-pointed once materialized on disk (e.g.
    `x/y -> ..`), so any later member nested "under" it (`x/y/q`) can be
    redirected by the OS to an entirely different real location at
    creation time -- a purely lexical per-member check cannot see this,
    because it only reasons about the DECLARED path string, not what the
    OS will actually do once an earlier sibling member has been placed.
    Rejecting the nested member outright (rather than trying to predict
    the real landing spot) is the simple, provably-sufficient rule
    decision.md offers as an alternative to real-time symlink-chain
    resolution."""
    norm = posixpath.normpath(name)
    parts = norm.split("/")
    for i in range(1, len(parts)):
        prefix = "/".join(parts[:i])
        if prefix in symlink_member_names:
            fail(
                f"{context}: member path routes through an archive-declared symlink member, rejected: "
                f"{name!r} (prefix {prefix!r} is itself a symlink member)"
            )


def _normalized_relparts(member_name: str, context: str):
    """007：把一個已經通過 check_member_path（不含原始 `..` component、不是
    絕對路徑）的 archive member 名稱，轉成**唯一**一份正規化路徑分量列表
    ——_verify_containment() 用它驗證、_manual_extract_member()／
    safe_extract_zip() 的實際落地也用**同一份**呼叫結果建 dest，不是各自
    重新對同一個字串做一次正規化（那樣仍然可能因為實作細節不同而產生
    「驗證的值」與「實際使用的值」不是同一個值的問題,見 tar_raw_dotdot 的
    根因）。"""
    norm = posixpath.normpath(member_name)
    parts = [p for p in norm.split("/") if p not in ("", ".")]
    if not parts:
        fail(f"{context}: empty member path after normalization: {member_name!r}")
    return parts


def _verify_containment(out_dir_real: str, member_name: str, context: str):
    """Walk member_name's path components one at a time under out_dir_real,
    resolving any *existing* filesystem entry at each step (pre-existing in
    the output directory before this run, or created earlier in this same
    extraction run) via os.path.realpath -- which follows an arbitrary
    symlink chain -- and refuse if the resolved location escapes
    out_dir_real. Applies to every member (regular file, dir, symlink,
    hardlink). Called immediately before EACH member is materialized (see
    safe_extract_tar), using the CURRENT on-disk state at that exact point
    in the extraction sequence -- not just once upfront (R7 fix)."""
    parts = _normalized_relparts(member_name, context)
    current = out_dir_real
    for part in parts[:-1]:
        current = os.path.join(current, part)
        if os.path.lexists(current):
            real = os.path.realpath(current)
            if real != out_dir_real and not real.startswith(out_dir_real + os.sep):
                fail(
                    f"{context}: path component of {member_name!r} escapes extraction root via "
                    f"symlink chain or pre-existing entry (component {current!r} resolves to {real!r}, "
                    f"expected inside {out_dir_real!r})"
                )
    dest = os.path.join(out_dir_real, *parts)
    parent = os.path.dirname(dest)
    parent_real = os.path.realpath(parent) if os.path.lexists(parent) else parent
    if parent_real != out_dir_real and not parent_real.startswith(out_dir_real + os.sep):
        fail(f"{context}: destination parent of {member_name!r} escapes extraction root (resolves to {parent_real!r})")


def safe_extract_zip(zip_path: str, out_dir: str) -> str:
    """Validate then extract the outer ZIP. Returns the path to the single
    inner .tgz member.

    007 fix (review571 zip_final_symlink): `out_dir` must be brand new
    (nonexistent, or an existing-but-empty directory) -- a non-empty
    pre-existing out_dir is exactly how the reviewer's probe pre-placed a
    symlink at the eventual destination path before extraction ever ran.
    Also refuses outright if the destination path for the single .tgz
    member already lexists (including a dangling/broken symlink) right
    before landing it. Streams the member manually via zf.open() +
    os.open(O_CREAT|O_EXCL|O_NOFOLLOW) instead of calling zf.extract() --
    zf.extract() is exactly what followed the pre-placed symlink and
    overwrote through it."""
    tgz_members = []
    with zipfile.ZipFile(zip_path) as zf:
        for info in zf.infolist():
            check_member_path(info.filename, "zip")
            unix_mode = (info.external_attr >> 16) & 0xFFFF
            is_symlink = (unix_mode & 0o170000) == 0o120000
            if is_symlink:
                fail(f"zip: unexpected symlink member in outer ZIP (not seen in 001 static-check): {info.filename!r}")
            if info.filename.endswith(".tgz"):
                tgz_members.append(info.filename)
        if len(tgz_members) != 1:
            fail(f"zip: expected exactly 1 top-level .tgz member, found {len(tgz_members)}: {tgz_members}")
        member_name = tgz_members[0]

        if os.path.lexists(out_dir):
            if os.path.islink(out_dir):
                fail(f"zip: output directory is a symlink, refusing: {out_dir!r}")
            if not os.path.isdir(out_dir):
                fail(f"zip: output path exists and is not a directory, refusing: {out_dir!r}")
            if os.listdir(out_dir):
                fail(f"zip: output directory already exists and is not empty (must be brand new): {out_dir!r}")
        else:
            os.makedirs(out_dir, exist_ok=True)
        out_dir_real = os.path.realpath(out_dir)

        _verify_containment(out_dir_real, member_name, "zip")
        parts = _normalized_relparts(member_name, "zip")
        dest = os.path.join(out_dir_real, *parts)
        if os.path.lexists(dest):
            fail(f"zip: refusing to land {member_name!r}: destination already exists (symlink or otherwise); will not follow or overwrite: {dest!r}")
        parent = os.path.dirname(dest)
        if parent:
            os.makedirs(parent, exist_ok=True)
        src = zf.open(member_name)
        try:
            try:
                fd = os.open(dest, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
            except OSError as e:
                fail(f"zip: could not create destination for {member_name!r} (refusing to follow/overwrite): {e}")
            with os.fdopen(fd, "wb") as out:
                while True:
                    chunk = src.read(1024 * 1024)
                    if not chunk:
                        break
                    out.write(chunk)
        finally:
            src.close()
    return dest


def _manual_extract_member(tf: tarfile.TarFile, m: tarfile.TarInfo, out_dir: str, known_member_names):
    """Land a single, already-validated member on disk without ever
    calling tf.extractall(). R7: refuses (does not overwrite/follow) if
    the destination already exists for regular-file/symlink/hardlink/dir
    members; regular files are created with O_EXCL|O_NOFOLLOW so the
    filesystem itself refuses to follow a symlink placed at that exact
    path between our containment check and the write.

    007 (tar_raw_dotdot): dest is built from `_normalized_relparts(m.name)`
    -- the SAME normalized path components `_verify_containment()` just
    validated -- never from the raw `m.name` string. Using the raw string
    here (`os.path.join(out_dir, m.name)`) was the actual root cause: a
    raw `..` component that normalization had already validated away could
    still take effect at the filesystem level via a real symlink placed at
    an earlier path component. The hardlink target (`m.linkname`) gets the
    same treatment for the same reason."""
    dest = os.path.join(out_dir, *_normalized_relparts(m.name, "tar"))
    if m.isdir():
        if os.path.lexists(dest) and not os.path.isdir(dest):
            fail(f"tar: refusing to create directory member {m.name!r}: destination already exists as a non-directory")
        os.makedirs(dest, exist_ok=True)
        return
    parent = os.path.dirname(dest)
    if parent:
        os.makedirs(parent, exist_ok=True)
    if m.isreg():
        src = tf.extractfile(m)
        if src is None:
            fail(f"tar: could not open regular file member for read: {m.name!r}")
        if os.path.lexists(dest):
            fail(f"tar: refusing to write regular file member {m.name!r}: destination already exists (symlink or otherwise); will not follow or overwrite")
        try:
            fd = os.open(dest, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        except OSError as e:
            fail(f"tar: could not create destination for regular file member {m.name!r} (refusing to follow/overwrite): {e}")
        with os.fdopen(fd, "wb") as out:
            while True:
                chunk = src.read(1024 * 1024)
                if not chunk:
                    break
                out.write(chunk)
        os.chmod(dest, (m.mode & 0o777) | 0o600)
        return
    if m.issym():
        if os.path.lexists(dest):
            fail(f"tar: refusing to create symlink member {m.name!r}: destination already exists")
        os.symlink(m.linkname, dest)
        return
    if m.islnk():
        if os.path.lexists(dest):
            fail(f"tar: refusing to create hardlink member {m.name!r}: destination already exists")
        target_dest = os.path.join(out_dir, *_normalized_relparts(m.linkname, "tar"))
        if not os.path.exists(target_dest):
            fail(f"tar: hardlink {m.name!r} -> {m.linkname!r} target not yet materialized (unexpected member order for this fixed artifact)")
        os.link(target_dest, dest)
        return
    fail(f"tar: unsupported member type for {m.name!r} (only regular files, dirs, symlinks, hardlinks are handled for this fixed artifact)")


def safe_extract_tar(tgz_path: str, out_dir: str):
    with tarfile.open(tgz_path, "r:gz") as tf:
        members = tf.getmembers()
        known_member_names = {posixpath.normpath(m.name) for m in members}
        symlink_member_names = {posixpath.normpath(m.name) for m in members if m.issym()}

        # ---- Pass 1: static, no-I/O structural validation of every member.
        for m in members:
            check_member_path(m.name, "tar")
            if m.issym():
                check_symlink_target(m.name, m.linkname, "tar")
            elif m.islnk():
                check_hardlink_target(m.name, m.linkname, "tar", known_member_names)
            elif m.isdev() or m.isfifo():
                fail(f"tar: device/FIFO member rejected (not a regular file/dir/symlink/hardlink): {m.name!r} (type={m.type!r})")
            elif not (m.isreg() or m.isdir()):
                fail(f"tar: unsupported member type rejected: {m.name!r} (type={m.type!r})")
            check_no_symlink_prefix(m.name, symlink_member_names, "tar")

        # ---- Pass 2: extraction, interleaved with a fresh containment
        # check against the CURRENT on-disk state immediately before each
        # member lands (R7 -- not just once upfront).
        os.makedirs(out_dir, exist_ok=True)
        out_dir_real = os.path.realpath(out_dir)
        for m in members:
            _verify_containment(out_dir_real, m.name, "tar")
            _manual_extract_member(tf, m, out_dir, known_member_names)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--zip", required=True)
    ap.add_argument("--zip-out", required=True)
    ap.add_argument("--tar-out", required=True)
    args = ap.parse_args()

    tgz_path = safe_extract_zip(args.zip, args.zip_out)
    safe_extract_tar(tgz_path, args.tar_out)
    print(f"OK: extracted {tgz_path} -> {args.tar_out}")


if __name__ == "__main__":
    main()
