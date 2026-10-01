#!/usr/bin/env python3
"""candidate/lib/supervise.py — 010 對 review595 必修 1／2 的重寫。

007 對 review571 裁定 B 的原始設計（`Popen(start_new_session=True)` 讓
PGID==PID；`os.waitid(P_PID, pid, WEXITED|WNOWAIT|WNOHANG)` 判定「已結束
但尚未回收」）維持不變，`scratch/waitid-probe/result.txt`（見 007
PLAN-delta.md）的實測結論仍然成立。010 修正的是這個依據在錯誤／競態
路徑上被誤用的兩個地方（`/Users/eason_tseng/b3a-evidence/
2026-09-29-review595/decision.md` 新阻擋一／二）：

**阻擋一：ECHILD 被誤當成「已結束但尚未回收」。** 009 版
`waitid_nowait()` 把 `ChildProcessError`（ECHILD）直接 `return True`，
後續邏輯因此誤以為仍持有這個 PGID 的身分依據，對它送 `os.killpg`。
ECHILD 只代表「沒有這個可等待的 child」，不能證明我們仍然持有未回收的
leader——尤其 `Popen.send_signal()` 內部會先呼叫 `self.poll()`（見
`python-send-signal-source.txt`：`self.poll(); if self.returncode is not
None: return`），如果 leader 剛好在上一次 WNOWAIT 檢查之後、這次
`send_signal` 之前自然結束，`poll()` 可能已經把它回收掉——這之後任何
`os.waitid()` 對同一個 pid 呼叫都會拋 ECHILD，這正是「我們自己的 API
呼叫造成了 ECHILD」，不是「仍然持有、只是查詢暫時失敗」。

010 改用三態模型（`_custody_state()`）：
  - `alive`：`p.returncode is None` 且 `os.waitid(WNOWAIT|WNOHANG)`
    回傳 `None`。
  - `exited_but_unreaped`：`p.returncode is None` 且 `os.waitid(...)`
    回傳非 `None`——**只有這一態才允許對 pgid 送 group signal**（
    `_bounded_group_cleanup` 只在這一態被呼叫）。
  - `lost_custody_or_query_error`：`p.returncode` 已經被設定（不論是
    自己呼叫 `p.wait()`／`p.poll()`，還是 `p.send_signal()` 內部的
    `poll()` 設的）、或 `os.waitid()` 拋 `ChildProcessError`（ECHILD）、
    或拋任何其他例外（例如某些平台不支援 `WNOWAIT`）。這一態一律
    `residual_pgid_members="unknown"`，不送任何 group signal，也不得
    偽裝成成功。

`p.send_signal()` 呼叫後一律重新呼叫 `_custody_state()`——不假設送出
signal 之後狀態不變（`_wait_for_state` 的每一輪迴圈也都重新查一次）。

**阻擋二：spawn 之後的證據 IO 例外會跳過收尾。** 009 版 `run_app()` 在
`Popen` 成功之後寫 `identity_file` 完全沒有 try/except 保護——這個寫入
本身失敗（例如目的地是目錄）時，例外直接往外逸出，函式提早中止，後面
所有 TERM/KILL/reap/group-cleanup 邏輯都不會執行，child 就此不受控制地
留在系統裡。010 把 identity_file 寫入失敗改成「記錄但不中止」（child
仍在持有中，繼續走正常收尾，不能因為中繼資訊寫不出去就放棄控制權）；
主要的等待／收尾邏輯另外包一層 try/except/finally 防禦性地涵蓋，任何
未預期的 IO 例外都會被導向同一個「best-effort 查一次目前的持有狀態、
記 unreaped、寫出（即使是降級的）result」路徑，不會讓例外把 child
控制權整個丟掉；最終 result 真的寫不進去時，印到 stderr 並回傳非 0。
supervisor 收到 SIGTERM/SIGINT 時，等待迴圈會提早跳出、走同一條收尾
路徑（不是另開一條路）；SIGKILL 無法在 Python 裡攔截，這是作業系統層級
無法迴避的限制，記在 PLAN-delta.md，不假裝可以攔截。

**必修 6（deadline）：** `_bounded_group_cleanup` 與 `run_helper`／
`run_app` 的每個等待階段，都改成從同一個 `time.monotonic()` 為基準
算出的單一整體 deadline 依序切出子 deadline（`_sub_deadline`），不再用
「迴圈跑了幾次」當作秒數上限——009 版 `_bounded_group_cleanup` 的
`while waited < max_wait: query(); ...; time.sleep(1); waited += 1`
這個寫法，每次 `query()`（`pgrep`，自己有 3 秒 timeout）本身就可能耗掉
遠超過 1 秒，但 `waited` 只加 1，導致 `max_wait` 次迭代的實際耗時可能是
`max_wait` 秒的數倍。010 的 `_bounded_group_cleanup(pgid, deadline_ts,
...)` 改成直接吃一個絕對時間戳，每次查詢的 `timeout` 參數也從「剩餘
時間」算出，deadline 已過就不再啟動下一次查詢或下一個 signal 階段。

兩種模式：
  - `helper`：對應 with_timeout 的單次有界呼叫。前景執行、寫一個 JSON
    結果檔後結束。
  - `app`：對應「App 本身」——背景長駐，受自己的 `--max-lifetime`（對齊
    driver 的總 budget）與 `--stop-file`（driver 寫入這個檔案要求收尾）
    共同約束。driver 不對這個 supervisor 或它持有的 App 送任何 PID
    signal，只用控制檔溝通。

殘留與未回收子程序：任何一次「有界時間內無法確認 group 乾淨」、「leader
本身在上限內未能確認回收」、或「失去持有依據（lost_custody_or_query_
error）」的情況，都會把這個子程序的身分（pid／command／spawn_time／
owner=本 supervisor 的 pid／最後一次觀測結果）附加寫進 `--unreaped-log`
指定的 `unreaped-children.jsonl`。
"""
import argparse
import json
import os
import signal
import subprocess
import sys
import time
from pathlib import Path

# 011（010 主審 N2）：自己把所在目錄加進 sys.path，確保不論怎麼被載入都
# 找得到同目錄的 `native_gate.py`（唯一權威判定來源，`window.sh` 的
# `_native_gate`／`driver.sh` 的 App spawn 檢查都呼叫同一份）。
sys.path.insert(0, str(Path(__file__).resolve().parent))
from native_gate import check_path_in_fake_root  # noqa: E402


def _app_argv0_allowed(argv):
    """N2：`run_app`（唯一真正會 spawn 使用者指定執行檔的模式）在
    `subprocess.Popen` 之前，自己再檢查一次 argv[0] 是否通過 native_gate
    ——不能只靠 `driver.sh::step_spawn()` 的 bash 層檢查（
    `010/main-review/attack-005-native-gate` 示範的正是「繞過 bash，
    直接執行 supervise.py」這個路徑）。`run_helper` 模式的 argv 一律是
    `window_fn_runner.sh` 這個 candidate 自帶的腳本（不是使用者可控的
    外部工具路徑），不受這個檢查約束。"""
    if not argv:
        return False, "empty argv"
    return check_path_in_fake_root(argv[0])


def _now():
    return time.monotonic()


def _now_iso():
    return time.strftime("%Y-%m-%dT%H:%M:%S", time.gmtime()) + f".{int((time.time() % 1) * 1e9):09d}Z"


def _sub_deadline(overall_deadline_ts, phase_budget):
    """回傳這個 phase 自己的絕對 deadline（time.monotonic() 時間戳）：不
    超過 overall_deadline_ts，也不超過「現在 + phase_budget」。必修 6：
    唯一計算 phase deadline 的入口，不在別處重新用迴圈次數算秒數。"""
    return min(overall_deadline_ts, _now() + max(0.0, phase_budget))


def _sleep_until(deadline_ts, step=1.0):
    """睡到 min(deadline_ts, now+step)，不超過 deadline_ts。回傳 deadline_ts
    是否仍有剩餘時間（False 表示已經到期，呼叫端不應該再啟動下一次
    查詢／signal 階段）。"""
    remaining = deadline_ts - _now()
    if remaining <= 0:
        return False
    time.sleep(min(step, remaining))
    return True


# ---- supervisor 自己收到 SIGTERM/SIGINT 時，提早跳出等待迴圈、走同一條
# 收尾路徑（不是另開一條路）。SIGKILL 無法在 Python 裡攔截，這是已知、
# 無法迴避的作業系統限制（見檔頭與 PLAN-delta.md）。
_external_signal = [None]


def _install_signal_forwarding():
    def _handler(signum, _frame):
        if _external_signal[0] is None:
            _external_signal[0] = signum
    signal.signal(signal.SIGTERM, _handler)
    signal.signal(signal.SIGINT, _handler)


def _custody_state(p, pid):
    """回傳 (state, info)。state 是三態之一：
      "alive" — 還活著，info=None。
      "exited_but_unreaped" — 已結束但尚未回收，唯一允許送 group signal
        的狀態，info 是 os.waitid() 的回傳值。
      "lost_custody_or_query_error" — 失去持有依據或查詢本身出錯，info
        是說明字串；這個狀態下絕不送 group signal，residual 一律
        "unknown"。
    """
    if p.returncode is not None:
        return "lost_custody_or_query_error", (
            f"Popen.returncode already set to {p.returncode!r} "
            "(leader already reaped by this or another poll/wait call, "
            "e.g. inside Popen.send_signal())"
        )
    try:
        res = os.waitid(os.P_PID, pid, os.WEXITED | os.WNOWAIT | os.WNOHANG)
    except ChildProcessError as e:
        return "lost_custody_or_query_error", f"ChildProcessError (ECHILD): {e}"
    except Exception as e:  # noqa: BLE001 - 任何查詢本身的例外都不得回退成「仍持有」
        return "lost_custody_or_query_error", f"{type(e).__name__}: {e}"
    if res is None:
        return "alive", None
    return "exited_but_unreaped", res


def _wait_for_state(p, pid, deadline_ts, respect_external_signal=False):
    """輪詢直到狀態不再是 alive，或 deadline_ts 已到，或（只有
    `respect_external_signal=True` 時）收到外部訊號提早跳出。回傳最後一次
    觀測到的 (state, info)。

    `respect_external_signal` 只給「自然等待」階段（run_helper 的階段 1）
    使用——目的是盡快對 SIGTERM/SIGINT 有反應、提早進入收尾。**不能**
    對 TERM-wait／KILL-wait／reap-wait 這些已經是收尾本身的階段也用同一個
    旗標：`_external_signal[0]` 一旦被設定就不會重置，這裡如果每個階段都
    檢查它，會讓收到訊號之後的每一次 `_wait_for_state` 呼叫都在第一輪就
    立刻回傳「alive」而完全不等待——term_wait/kill_wait/reap_timeout 這些
    收尾階段自己的有界預算因此形同虛設（真實回歸：對真正 spawn 的
    `/bin/sleep 30` 送 SIGTERM 後，supervisor 錯誤地立刻判定
    term_converged=False，沒有真的等它退出就跳去 KILL，見
    `test_review595_regressions.py::test_supervisor_sigterm_same_teardown_path`
    抓到的這個 bug 與修正說明）。"""
    while True:
        state, info = _custody_state(p, pid)
        if state != "alive":
            return state, info
        if respect_external_signal and _external_signal[0] is not None:
            return "alive", f"interrupted by external signal {_external_signal[0]}"
        if not _sleep_until(deadline_ts):
            return "alive", None


def _append_unreaped(unreaped_log, pid, command, spawn_time, owner_pid, last_observation):
    if not unreaped_log:
        return
    rec = {
        "ts": _now_iso(),
        "pid": pid,
        "command": command,
        "spawn_time": spawn_time,
        "owner": owner_pid,
        "last_observation": last_observation,
    }
    try:
        with open(unreaped_log, "a") as f:
            f.write(json.dumps(rec) + "\n")
    except OSError as e:
        # 寫不進去也要讓呼叫端知道——印到 stderr,不吞掉。
        print(f"supervise.py: FAILED to append unreaped-children log {unreaped_log}: {e}", file=sys.stderr)


def _write_result(path, obj):
    try:
        with open(path, "w") as f:
            json.dump(obj, f)
        return True
    except OSError as e:
        print(f"supervise.py: FAILED to write result file {path}: {e}", file=sys.stderr)
        return False


def _query_pgid_residual(pgid, timeout=3):
    """只查詢（不送 signal）：`pgrep -g <pgid>`，只查自己記下的這一個
    pgid，不做全域掃描。回傳 (status, count)：
      status="counted" — count 是可信的非負整數（含 0，pgrep rc=1 明確
                          查無成員）。
      status="unknown" — pgrep 不存在、rc>=2、輸出無法解析、或 timeout
                          本身就 <=0（沒有預算可以查了）,不能當 0。
    """
    if timeout <= 0:
        return "unknown", None
    try:
        r = subprocess.run(["pgrep", "-g", str(pgid)], capture_output=True, text=True, timeout=timeout)
    except (FileNotFoundError, subprocess.TimeoutExpired):
        return "unknown", None
    if r.returncode == 0:
        lines = [l for l in r.stdout.splitlines() if l.strip()]
        try:
            n = len(lines)
            for l in lines:
                int(l.strip())  # 確認每行都是可解析的 PID,解析不了就是 unknown
            return "counted", n
        except ValueError:
            return "unknown", None
    elif r.returncode == 1:
        return "counted", 0
    else:
        return "unknown", None


def _bounded_group_cleanup(pgid, deadline_ts, max_wait, evidence_note):
    """對 <pgid> 做一次有界收尾：只在「當下重新量到」的正整數成員數才送
    signal（不對 unknown 送任何 signal）；TERM／KILL 各自送出前都重新
    量測一次。回傳清理嘗試後的殘留量測（int 或 "unknown"）。這個函式只在
    呼叫端已經確認自己仍持有這個 pgid 的身分（leader 已結束但尚未回收，
    見 `_custody_state`）時才可以呼叫——身分依據由呼叫端保證，這裡不重複
    驗證。

    必修 6：`deadline_ts` 是整個函式呼叫（TERM 輪詢＋KILL 輪詢合計）不得
    超過的單一 time.monotonic() 絕對時間戳上限；TERM-wait／KILL-wait 各自
    另外再有自己的 `max_wait` 秒子視窗（`min(deadline_ts, now+max_wait)`
    ——兩階段各自最多 `max_wait` 秒，但都不能突破外層 `deadline_ts`），
    不是把兩階段擠進同一段時間；每次 `_query_pgid_residual` 的 timeout
    參數也從相對 `deadline_ts` 的剩餘時間算出，deadline 已過就不再啟動
    下一次查詢或下一個 signal 階段（不是用「迴圈跑了幾次」當作秒數上限，
    review595 指出的舊 bug：pgrep 本身的 3 秒 timeout 加上迴圈只把
    `waited` 加 1，導致 `max_wait` 次迭代的實際耗時可能遠超過 `max_wait`
    秒）。
    """
    def remaining():
        return deadline_ts - _now()

    def query():
        r = remaining()
        if r <= 0:
            return "unknown", None
        return _query_pgid_residual(pgid, timeout=min(3.0, r))

    status, n = query()
    if status == "counted" and n == 0:
        return 0
    if status == "unknown":
        return "unknown"
    # status=="counted" and n>0: 這一刻剛量到的正整數,送 TERM 是安全的。
    try:
        os.killpg(pgid, signal.SIGTERM)
    except (ProcessLookupError, PermissionError):
        pass
    term_deadline = min(deadline_ts, _now() + max(0.0, max_wait))
    while _now() < term_deadline:
        status, n = query()
        if status == "counted" and n == 0:
            return 0
        if status == "unknown":
            return "unknown"
        if not _sleep_until(term_deadline):
            break
    if status == "counted" and n > 0 and remaining() > 0:
        try:
            os.killpg(pgid, signal.SIGKILL)
        except (ProcessLookupError, PermissionError):
            pass
        kill_deadline = min(deadline_ts, _now() + max(0.0, max_wait))
        while _now() < kill_deadline:
            status, n = query()
            if status == "counted" and n == 0:
                return 0
            if status == "unknown":
                return "unknown"
            if not _sleep_until(kill_deadline):
                break
    return n if status == "counted" else "unknown"


def run_helper(args):
    _install_signal_forwarding()
    argv = json.loads(args.argv_json)
    spawn_time = _now_iso()
    out_f = open(args.out, "wb")
    err_f = open(args.err, "wb")
    try:
        p = subprocess.Popen(argv, stdout=out_f, stderr=err_f, start_new_session=True, cwd=args.cwd)
    except OSError as e:
        result = {"rc": None, "timed_out": False, "killed": False, "residual_pgid_members": "unknown",
                  "spawn_error": str(e)}
        ok = _write_result(args.result, result)
        out_f.close(); err_f.close()
        return 0 if ok else 1

    pid = p.pid
    pgid = pid  # start_new_session=True 保證 pgid == pid
    group_budget = 2 * max(args.term_wait, args.kill_wait, 1.0)
    overall_deadline = _now() + args.timeout + args.term_wait + args.kill_wait + args.reap_timeout + group_budget

    timed_out = False
    killed = False
    result = None
    try:
        # ---- 階段 1：等待自然結束（唯一 respect_external_signal=True 的
        # 階段——盡快對外部 SIGTERM/SIGINT 有反應，提早進入下面的收尾；
        # 收尾本身（TERM/KILL/reap-wait）不能再用同一個旗標,見
        # `_wait_for_state` 檔頭說明）----
        phase_deadline = _sub_deadline(overall_deadline, args.timeout)
        state, info = _wait_for_state(p, pid, phase_deadline, respect_external_signal=True)

        # ---- 階段 2：仍活著就 TERM，重新判定，再等，再 KILL ----
        if state == "alive":
            timed_out = True
            try:
                p.send_signal(signal.SIGTERM)
            except ProcessLookupError:
                pass
            state, info = _custody_state(p, pid)  # send_signal 後一律重新判定
            if state == "alive":
                phase_deadline = _sub_deadline(overall_deadline, args.term_wait)
                state, info = _wait_for_state(p, pid, phase_deadline)
            if state == "alive":
                killed = True
                try:
                    p.send_signal(signal.SIGKILL)
                except ProcessLookupError:
                    pass
                state, info = _custody_state(p, pid)  # 同樣重新判定
                if state == "alive":
                    phase_deadline = _sub_deadline(overall_deadline, args.kill_wait)
                    state, info = _wait_for_state(p, pid, phase_deadline)

        # ---- 階段 3：仍未確認結束時的最後有界輪詢 ----
        if state == "alive":
            phase_deadline = _sub_deadline(overall_deadline, args.reap_timeout)
            state, info = _wait_for_state(p, pid, phase_deadline)

        if state == "alive":
            _append_unreaped(args.unreaped_log, pid, argv, spawn_time, os.getpid(),
                              "still alive after bounded TERM/KILL/reap window")
            result = {"rc": None, "timed_out": True, "killed": killed, "residual_pgid_members": "unknown"}
        elif state == "lost_custody_or_query_error":
            # 必修 1：失去持有依據——不送 group signal,不偽裝成功。
            _append_unreaped(args.unreaped_log, pid, argv, spawn_time, os.getpid(),
                              f"lost custody or query error before any group signal: {info}")
            result = {"rc": None, "timed_out": timed_out, "killed": killed, "residual_pgid_members": "unknown",
                      "custody_error": str(info)}
        else:
            # exited_but_unreaped：唯一允許對 pgid 送 group signal 的狀態。
            group_deadline = _sub_deadline(overall_deadline, group_budget)
            residual = _bounded_group_cleanup(pgid, group_deadline, max(args.term_wait, args.kill_wait, 1.0),
                                               "post-exit cleanup")
            rc = p.wait()  # 此時保證不阻塞：已經用 WNOWAIT 確認過結束了。
            if residual == "unknown" or (isinstance(residual, int) and residual > 0):
                _append_unreaped(args.unreaped_log, pid, argv, spawn_time, os.getpid(),
                                  f"leader reaped rc={rc} but group residual={residual}")
            result = {"rc": rc, "timed_out": timed_out, "killed": killed, "residual_pgid_members": residual}
    except Exception as e:  # noqa: BLE001 - 必修 2：任何未預期的 IO/其他例外都要走到這裡
        try:
            fallback_state, fallback_info = _custody_state(p, pid)
        except Exception:
            fallback_state, fallback_info = "lost_custody_or_query_error", "custody re-check itself failed"
        _append_unreaped(args.unreaped_log, pid, argv, spawn_time, os.getpid(),
                          f"exception during supervised wait/teardown ({type(e).__name__}: {e}); "
                          f"last observed custody state={fallback_state} ({fallback_info})")
        result = {"rc": None, "timed_out": timed_out, "killed": killed, "residual_pgid_members": "unknown",
                  "supervise_error": f"{type(e).__name__}: {e}"}
    finally:
        ok = _write_result(args.result, result if result is not None else {
            "rc": None, "timed_out": timed_out, "killed": killed,
            "residual_pgid_members": "unknown", "supervise_error": "result was never computed",
        })
        try:
            out_f.close()
        except Exception:
            pass
        try:
            err_f.close()
        except Exception:
            pass
    return 0 if ok else 1


def run_app(args):
    """長駐模式：背景持有 App，受 stop-file 與 max-lifetime 共同約束。
    driver 不對這個 supervisor 或它持有的 App 送任何 PID signal，只用
    stop-file 溝通；supervisor 若在 max-lifetime 內都沒等到 stop-file、
    也沒等到 App 自己結束，就自行收尾（沿用 helper 模式同一套 TERM/KILL/
    reap/group-cleanup 邏輯，同一套三態持有判定）。"""
    _install_signal_forwarding()
    argv = json.loads(args.argv_json)
    spawn_time = _now_iso()

    # N2：在 Popen 之前，程序內部自己再檢查一次 argv[0]——不依賴呼叫端
    # （driver.sh::step_spawn）有沒有先把關過。`010/main-review/
    # attack-005-native-gate/case1` 示範的正是「繞過 driver.sh，直接執行
    # supervise.py app」這個路徑；這裡是最後一道、也是唯一在「真的會
    # Popen 使用者指定執行檔」這個時間點上的防線。
    gate_ok, gate_reason = _app_argv0_allowed(argv)
    if not gate_ok:
        print(f"supervise.py: NATIVE_NOT_ALLOWED: {gate_reason}", file=sys.stderr)
        result = {"rc": None, "stopped_reason": "native_not_allowed", "term_converged": False,
                   "kill_required": False, "residual_pgid_members": "unknown", "pid": None, "pgid": None,
                   "native_gate_error": gate_reason}
        ok = _write_result(args.result, result)
        return 97 if ok else 1

    out_f = open(args.out, "wb")
    err_f = open(args.err, "wb")
    try:
        p = subprocess.Popen(argv, stdout=out_f, stderr=err_f, start_new_session=True, cwd=args.cwd)
    except OSError as e:
        result = {"rc": None, "stopped_reason": "spawn_error", "term_converged": False, "kill_required": False,
                   "residual_pgid_members": "unknown", "pid": None, "pgid": None, "spawn_error": str(e)}
        ok = _write_result(args.result, result)
        out_f.close(); err_f.close()
        return 0 if ok else 1

    pid = p.pid
    pgid = pid

    # 必修 2：identity_file 的寫入失敗改成「記錄但不中止」——child 仍在
    # 持有中，繼續走下面正常的收尾邏輯，不能因為這份中繼資訊寫不出去就
    # 放棄控制權（009 的 bug：這裡原本完全沒有 try/except，IsADirectoryError
    # 之類的例外會讓整個函式提早中止，後面的 TERM/KILL/reap 都不會跑）。
    identity_error = None
    if args.identity_file:
        try:
            with open(args.identity_file, "w") as f:
                json.dump({"pid": pid, "pgid": pgid, "spawn_time": spawn_time}, f)
        except OSError as e:
            identity_error = f"{type(e).__name__}: {e}"
            print(f"supervise.py: FAILED to write identity file {args.identity_file}: {e}", file=sys.stderr)

    group_budget = 2 * max(args.term_wait, args.kill_wait, 1.0)
    overall_deadline = _now() + args.max_lifetime + args.term_wait + args.kill_wait + args.reap_timeout + group_budget

    stopped_reason = "max_lifetime"
    term_converged = False
    kill_required = False
    result = None
    try:
        main_deadline = _sub_deadline(overall_deadline, args.max_lifetime)
        state = "alive"
        info = None
        while True:
            state, info = _custody_state(p, pid)
            if state != "alive":
                stopped_reason = "self_exit" if state == "exited_but_unreaped" else stopped_reason
                break
            if _now() >= main_deadline:
                stopped_reason = "max_lifetime"
                break
            if args.stop_file and os.path.exists(args.stop_file):
                stopped_reason = "stop_file"
                break
            if _external_signal[0] is not None:
                stopped_reason = f"external_signal:{_external_signal[0]}"
                break
            time.sleep(min(1.0, max(0.01, main_deadline - _now())))

        if state == "alive":
            try:
                p.send_signal(signal.SIGTERM)
            except ProcessLookupError:
                pass
            state, info = _custody_state(p, pid)  # send_signal 後一律重新判定
            if state == "alive":
                phase_deadline = _sub_deadline(overall_deadline, args.term_wait)
                state, info = _wait_for_state(p, pid, phase_deadline)
            term_converged = (state != "alive")
            if state == "alive":
                kill_required = True
                try:
                    p.send_signal(signal.SIGKILL)
                except ProcessLookupError:
                    pass
                state, info = _custody_state(p, pid)
                if state == "alive":
                    phase_deadline = _sub_deadline(overall_deadline, args.kill_wait)
                    state, info = _wait_for_state(p, pid, phase_deadline)
        else:
            term_converged = True

        if state == "alive":
            phase_deadline = _sub_deadline(overall_deadline, args.reap_timeout)
            state, info = _wait_for_state(p, pid, phase_deadline)

        if state == "alive":
            _append_unreaped(args.unreaped_log, pid, argv, spawn_time, os.getpid(),
                              "app supervisor: still alive after bounded TERM/KILL/reap window")
            result = {"rc": None, "stopped_reason": stopped_reason, "term_converged": term_converged,
                       "kill_required": kill_required, "residual_pgid_members": "unknown", "pid": pid, "pgid": pgid}
        elif state == "lost_custody_or_query_error":
            _append_unreaped(args.unreaped_log, pid, argv, spawn_time, os.getpid(),
                              f"app: lost custody or query error before any group signal: {info}")
            result = {"rc": None, "stopped_reason": stopped_reason, "term_converged": term_converged,
                       "kill_required": kill_required, "residual_pgid_members": "unknown", "pid": pid, "pgid": pgid,
                       "custody_error": str(info)}
        else:
            group_deadline = _sub_deadline(overall_deadline, group_budget)
            residual = _bounded_group_cleanup(pgid, group_deadline, max(args.term_wait, args.kill_wait, 1.0),
                                               "app post-exit cleanup")
            rc = p.wait()
            if residual == "unknown" or (isinstance(residual, int) and residual > 0):
                _append_unreaped(args.unreaped_log, pid, argv, spawn_time, os.getpid(),
                                  f"app leader reaped rc={rc} but group residual={residual}")
            result = {"rc": rc, "stopped_reason": stopped_reason, "term_converged": term_converged,
                       "kill_required": kill_required, "residual_pgid_members": residual, "pid": pid, "pgid": pgid}
        if identity_error:
            result["identity_error"] = identity_error
    except Exception as e:  # noqa: BLE001 - 必修 2：任何未預期的 IO/其他例外都要走到這裡
        try:
            fallback_state, fallback_info = _custody_state(p, pid)
        except Exception:
            fallback_state, fallback_info = "lost_custody_or_query_error", "custody re-check itself failed"
        _append_unreaped(args.unreaped_log, pid, argv, spawn_time, os.getpid(),
                          f"app: exception during supervised wait/teardown ({type(e).__name__}: {e}); "
                          f"last observed custody state={fallback_state} ({fallback_info})")
        result = {"rc": None, "stopped_reason": stopped_reason, "term_converged": term_converged,
                   "kill_required": kill_required, "residual_pgid_members": "unknown", "pid": pid, "pgid": pgid,
                   "supervise_error": f"{type(e).__name__}: {e}"}
    finally:
        ok = _write_result(args.result, result if result is not None else {
            "rc": None, "stopped_reason": stopped_reason, "term_converged": term_converged,
            "kill_required": kill_required, "residual_pgid_members": "unknown", "pid": pid, "pgid": pgid,
            "supervise_error": "result was never computed",
        })
        try:
            out_f.close()
        except Exception:
            pass
        try:
            err_f.close()
        except Exception:
            pass
    # 次要（010 主審）：identity 或 result 寫入失敗時，程序本身要以非零
    # 退出——結果檔仍然盡量寫入（上面的 `_write_result` 呼叫不受這裡影響），
    # 只是退出碼要誠實反映「這一輪有 IO 失敗」，不能因為收尾本身還算正常
    # 就回報 0（010 版只靠 driver.sh 因為讀不到 identity 檔而判 spawn=
    # error 間接讓整體不成功，兩者不完全一致）。
    if not ok or identity_error:
        return 1
    return 0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("mode", choices=["helper", "app"])
    ap.add_argument("--argv-json", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--err", required=True)
    ap.add_argument("--result", required=True)
    ap.add_argument("--unreaped-log", default=None)
    ap.add_argument("--cwd", default=None)
    ap.add_argument("--timeout", type=float, default=10)
    ap.add_argument("--term-wait", type=float, default=2)
    ap.add_argument("--kill-wait", type=float, default=1)
    ap.add_argument("--reap-timeout", type=float, default=1)
    # app mode only
    ap.add_argument("--stop-file", default=None)
    ap.add_argument("--max-lifetime", type=float, default=300)
    ap.add_argument("--identity-file", default=None)
    args = ap.parse_args()

    if args.mode == "helper":
        return run_helper(args)
    else:
        return run_app(args)


if __name__ == "__main__":
    sys.exit(main())
