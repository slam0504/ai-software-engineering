#!/usr/bin/env bash
# candidate/driver.sh — B3b P0 spike driver（候選 008，本輪不執行）
#
# 本輪邊界：這是「候選程式」，本輪**不執行**（不啟動 App／GUI）。離線驗證
# 只測試 lib/*.sh 的純函式與本輪自己 spawn 的假程序（見 ../tests/）。
#
# 008 是對 007 main-review FINDINGS.md（2026-09-29，B1）的修正版；007
# 本身（含 007/main-review/）維持原狀不修改。B1：`_run_step` 只檢查呼叫端
# 的 rc,rc=0 時完全不驗證該步實際寫進 RESULT_LOG 的記錄內容,main-review
# 用 `step_audit_observe(){ record_result audit_observe error synthetic;
# return 0; }` 這類情境證明 fail-fast 級聯可以被繞過（AX/window/content/
# capture 仍全部被呼叫,只是最終 compute_overall() 仍判對）。008 新增
# `_postcheck_step`（見該函式定義）：不論 rc 為何,都無條件讀取
# `$RESULT_LOG` 這個檔案本身,驗證這批 owned step 的記錄是否恰好一筆、
# 欄位齊全、run_id 相符、status 在白名單內且是 normal 或固定分支表允許的
# 值；`_run_step`／`step_ax_content_read`／`step_window_identify` 都呼叫
# 它,同一 step 內的多個 helper（meta→title_json→tree、window_id→
# window_cg_crosscheck）也逐一 postcheck 才能呼叫下一個。細節見同目錄
# `PLAN-delta.md`／`compliance-table.md`。
#
# 005/006 已修正的 R1–R9（overall 語意收緊／結果紀錄白名單／residual gate
# 全呼叫點／PGID 未驗證不送 signal／總 deadline 約束等）全部沿用，不在此
# 重列——見 lib/classify.sh、safe_extract.py、lib/timeout.sh 各自檔頭。
#
# 007 是對 review571（`/Users/eason_tseng/b3a-evidence/2026-09-28-review571/
# decision.md`）三項裁定＋必修範圍 1–4 的修正版；006 本身維持原狀不修改。
# 本檔頭只列本輪相對 006 的新增修正,細節與逐項對照見同目錄
# `PLAN-delta.md`／`compliance-table.md`：
#
#   真正 fail-fast（必修 1）：main() 不再只用「剩餘總預算」在各 phase 之間
#   決定要不要繼續——改成每個 `_run_step` 呼叫完之後立刻檢查它的回傳碼；
#   非 0 就把所有下游必要 step 記成 not_run（附上「在哪一步、為什麼停」的
#   原因)，不再呼叫下游任何 step 函式，直接進 single-flight teardown。
#   `step_ax_content_read()` 內部三個 helper（meta／title_json／tree）也
#   套用同一條規則：前一個 helper 出現非白名單允許的 unexpected error（非
#   normal，且不是 title_json 專屬允許的 not_available 固定分支）,後面的
#   helper 完全不呼叫。compute_overall() 保留作為第二道完整性檢查,不取代
#   這裡的停止控制。
#
#   tar/zip 兩個逃逸（必修 2）：見 safe_extract.py 檔頭。
#
#   持有／回收改由 Python supervisor 實作（必修 3／裁定 B）：見
#   `lib/supervise.py`／`lib/timeout.sh`／`lib/window_fn_runner.sh` 檔頭。
#   `step_spawn()`／`step_teardown()` 改成啟動並透過 `--stop-file` 控制檔
#   要求收尾 `lib/supervise.py app` 模式,不再由 driver 自己 fork App、也
#   不再對 APP_PID/APP_PGID 送任何 PID/PGID signal——舊的
#   `_count_pgid_residual`／`_confirm_pgid_ownership`／裸 PID kill fallback
#   全部移除，改讀 supervisor 寫出的 JSON 結果檔。7 個 `with_timeout` 呼叫
#   點統一改成透過 `lib/window_fn_runner.sh <fn>` 呼叫（window.sh 的函式
#   不是可以被 `subprocess.Popen` 直接執行的可執行檔）。
#
#   multiple-meta（必修 4）：見 lib/window.sh 檔頭（`ax_meta_text`／
#   `ax_meta_title_json` 改成用同一個離線可測的候選選取器，恰好 1 個候選
#   才接受,兩者綁定同一個候選）。
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/timeout.sh
source "$SCRIPT_DIR/lib/timeout.sh"
# shellcheck source=lib/classify.sh
source "$SCRIPT_DIR/lib/classify.sh"
# shellcheck source=lib/window.sh
source "$SCRIPT_DIR/lib/window.sh"

WINDOW_FN_RUNNER="$SCRIPT_DIR/lib/window_fn_runner.sh"

# ---- 必要輸入 ----
: "${APP_BIN:?必須指向 .../sdlc-workbench.app/Contents/MacOS/sdlc-workbench}"
: "${RUNDIR:?必須是一個全新、專屬本次 run 的目錄}"

TOTAL_BUDGET_SECS="${TOTAL_BUDGET_SECS:-300}"
SPAWN_TIMEOUT_SECS="${SPAWN_TIMEOUT_SECS:-10}"
AUDIT_TIMEOUT_SECS="${AUDIT_TIMEOUT_SECS:-30}"
AX_AUTH_TIMEOUT_SECS="${AX_AUTH_TIMEOUT_SECS:-10}"
AX_CONTENT_TIMEOUT_SECS="${AX_CONTENT_TIMEOUT_SECS:-10}"
WINDOW_TIMEOUT_SECS="${WINDOW_TIMEOUT_SECS:-10}"
CG_CHECK_TIMEOUT_SECS="${CG_CHECK_TIMEOUT_SECS:-10}"
SCREENCAPTURE_TIMEOUT_SECS="${SCREENCAPTURE_TIMEOUT_SECS:-10}"
TERM_WAIT_SECS="${TERM_WAIT_SECS:-10}"
KILL_WAIT_SECS="${KILL_WAIT_SECS:-5}"

# TEARDOWN_RESERVE_SECS：main() 每個 phase 開始前都要保留至少這麼多剩餘
# 預算給 teardown。必須跟 step_teardown() 實際的 teardown_bound 用同一套
# worst-case 公式（見該函式旁註），否則預算保留不夠、teardown 還沒真的做
# 完就被總預算硬性截斷——leader TERM 等待＋leader KILL 等待＋leader 有界
# reap 確認＋group cleanup（_bounded_group_cleanup 用
# max(TERM_WAIT_SECS,KILL_WAIT_SECS,1) 各跑一次 TERM 等待與 KILL 等待，
# 最壞兩輪都要）。
_TD_GC_WAIT="$TERM_WAIT_SECS"
if [ "$KILL_WAIT_SECS" -gt "$_TD_GC_WAIT" ]; then _TD_GC_WAIT="$KILL_WAIT_SECS"; fi
if [ 1 -gt "$_TD_GC_WAIT" ]; then _TD_GC_WAIT=1; fi
TEARDOWN_RESERVE_SECS=$((TERM_WAIT_SECS + KILL_WAIT_SECS + _WT_REAP_TIMEOUT_SECS + _TD_GC_WAIT + _TD_GC_WAIT + 10))

# 裁定 C：_WT_REAP_TIMEOUT_SECS（定義在 lib/timeout.sh）也要算進
# App-supervisor 的 max-lifetime 與 teardown 的有界等待,不能只在
# with_timeout 的短 helper 呼叫裡算。
APP_MAX_LIFETIME_SECS=$(( TOTAL_BUDGET_SECS + TEARDOWN_RESERVE_SECS + _WT_REAP_TIMEOUT_SECS + 30 ))

DRIVER_START_EPOCH=$(date -u +%s)
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$"

# RUNDIR 一律解析成絕對、canonical 路徑；拒絕 symlink。
if [ -L "$RUNDIR" ]; then
  echo "FATAL: RUNDIR is a symlink, refusing: $RUNDIR" >&2
  exit 1
fi
RUNDIR_PARENT="$(dirname "$RUNDIR")"
if [ ! -d "$RUNDIR_PARENT" ]; then
  echo "FATAL: RUNDIR parent does not exist: $RUNDIR_PARENT" >&2
  exit 1
fi
RUNDIR_PARENT_CANON="$(cd "$RUNDIR_PARENT" && pwd -P)"
RUNDIR="$RUNDIR_PARENT_CANON/$(basename "$RUNDIR")"

# no-reuse 契約：判斷範圍是「driver 自己管理的 EVIDENCE_DIR/WORKSPACE_DIR
# 不得已存在且非空」，不是整個 RUNDIR——workflow-candidate.yml 把 setup
# 階段（metadata 核對/setup log）寫進 RUNDIR 底下一個 driver 不碰的 setup/
# 子目錄，讓整個 run 只有一個證據根目錄。
EVIDENCE_DIR="$RUNDIR/evidence"
WORKSPACE_DIR="$RUNDIR/workspace"
RESULT_LOG="$EVIDENCE_DIR/result.jsonl"

if [ -e "$EVIDENCE_DIR" ] && [ -n "$(ls -A "$EVIDENCE_DIR" 2>/dev/null)" ]; then
  echo "FATAL: EVIDENCE_DIR already exists and is not empty (no-reuse contract): $EVIDENCE_DIR" >&2
  exit 1
fi
if [ -e "$WORKSPACE_DIR" ] && [ -n "$(ls -A "$WORKSPACE_DIR" 2>/dev/null)" ]; then
  echo "FATAL: WORKSPACE_DIR already exists and is not empty (no-reuse contract): $WORKSPACE_DIR" >&2
  exit 1
fi

mkdir -p "$EVIDENCE_DIR"

# EXPECTED_TOOLS_DIR：resolveToolsDir() 在 toolsSource=="bundle" 分支的公式
# 是 filepath.Join(filepath.Dir(exe), "..", "Resources", "tools")——Go 的
# filepath.Join 只做**詞法**正規化（Clean），不解析 symlink。用 `cd && pwd
# -P` 算會因為 /tmp -> /private/tmp 這類系統 symlink 而得到跟 app 實際回報
# 的字串不同的結果，所以這裡刻意只做詞法正規化，不碰檔案系統。
APP_BIN_DIR="$(dirname "$APP_BIN")"
EXPECTED_TOOLS_DIR="$(python3 -c "import posixpath,sys; print(posixpath.normpath(posixpath.join(sys.argv[1], '..', 'Resources', 'tools')))" "$APP_BIN_DIR")"

remaining_budget() {
  local now; now=$(date -u +%s)
  echo $(( TOTAL_BUDGET_SECS - (now - DRIVER_START_EPOCH) ))
}

# _bounded_step_timeout <step_own_timeout_secs>
#
# R6：每次呼叫 with_timeout 前都要用這個算出實際要用的秒數——
# `min(step 自己配置的上限, remaining_budget() - TEARDOWN_RESERVE_SECS)`。
# 回傳值 <= 0 時，呼叫端必須完全跳過這次 with_timeout 呼叫（不送出）,記
# `partial`,不能假裝呼叫過。這保證任何單一 step 都不能無視總預算,把整個
# driver 撐過 `TOTAL_BUDGET_SECS`（main-review 反例 deadline_budget）。
_bounded_step_timeout() {
  local own_limit="$1"
  local rb; rb=$(remaining_budget)
  local avail=$(( rb - TEARDOWN_RESERVE_SECS ))
  if [ "$avail" -lt "$own_limit" ]; then
    echo "$avail"
  else
    echo "$own_limit"
  fi
}

# _wt_json_field <with_timeout_json> <field>
_wt_json_field() {
  python3 -c "
import json,sys
try:
    obj=json.loads(sys.argv[1])
    print(obj.get(sys.argv[2]))
except Exception:
    print('__PARSE_ERROR__')
" "$1" "$2" 2>/dev/null || echo "__PARSE_ERROR__"
}

# _extract_residual <with_timeout_json>
#
# R3：印出 "0"｜"<正整數>"｜"unknown"（stdout 一行）。JSON 解析失敗或欄位
# 缺失、型別不對，一律當 "unknown"（fail-closed，不能因為解析失敗就假設
# 乾淨）。
_extract_residual() {
  python3 -c "
import json,sys
try:
    obj = json.loads(sys.argv[1])
    r = obj.get('residual_pgid_members')
    if isinstance(r, bool):
        print('unknown')
    elif isinstance(r, int):
        print(str(r))
    else:
        print('unknown')
except Exception:
    print('unknown')
" "$1" 2>/dev/null || echo "unknown"
}

# _apply_residual_gate <classified-status> <with_timeout_json>
#
# R3：每個呼叫 with_timeout 的 step 在算完自己原本的分類（cls）之後,都要
# 再過這一關。殘留非 0（含 unknown）時,不論原本的分類是什麼,一律強制降
# 成 error,並把量測值放進全域變數 `_RESIDUAL_GATE_SUFFIX` 供呼叫端接到
# detail 字串裡；結果狀態放進 `_RESIDUAL_GATE_STATUS`。
_RESIDUAL_GATE_STATUS=""
_RESIDUAL_GATE_SUFFIX=""
_apply_residual_gate() {
  local cls="$1" json="$2"
  local residual; residual=$(_extract_residual "$json")
  if [ "$residual" != "0" ]; then
    _RESIDUAL_GATE_STATUS="error"
    _RESIDUAL_GATE_SUFFIX=" residual_pgid_members=$residual (with_timeout helper subprocess not verified fully reaped; see R3)"
  else
    _RESIDUAL_GATE_STATUS="$cls"
    _RESIDUAL_GATE_SUFFIX=""
  fi
}

# ============================================================
# record_result — 純粹寫入，不自己彙總 overall（見 compute_overall）。
# ============================================================
RECORD_RESULT_IO_FAILED=0

record_result() {
  local step="$1" status="$2" detail="$3"
  local ts; ts=$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)
  if ! python3 - "$RESULT_LOG" "$ts" "$RUN_ID" "$step" "$status" "$detail" <<'PYEOF'
import json, sys
path, ts, run_id, step, status, detail = sys.argv[1:7]
rec = {"ts": ts, "run_id": run_id, "step": step, "status": status, "detail": detail}
with open(path, "a") as f:
    f.write(json.dumps(rec) + "\n")
PYEOF
  then
    RECORD_RESULT_IO_FAILED=1
    echo "record_result: FAILED to append to $RESULT_LOG (step=$step status=$status)" >&2
  fi
}

# ============================================================
# check_no_test_injection_in_ci — 拒絕正式 GHA 入口的假 backend 注入。
# 只有 GITHUB_ACTIONS=true 時才啟用；離線測試 harness 不設這個變數。
# ============================================================
check_no_test_injection_in_ci() {
  if [ "${GITHUB_ACTIONS:-}" != "true" ]; then
    return 0
  fi
  local bad=()
  local v
  for v in OSASCRIPT_BIN SCREENCAPTURE_BIN CG_WINDOW_CHECK_CMD; do
    if [ -n "${!v:-}" ]; then
      bad+=("$v=${!v}")
    fi
  done
  local name
  while IFS='=' read -r name _; do
    [ -z "$name" ] && continue
    case "$name" in
      FAKE_*) bad+=("$name") ;;
    esac
  done < <(env)
  if [ "${#bad[@]}" -gt 0 ]; then
    echo "FATAL: refusing to run under GITHUB_ACTIONS=true with test-injection override(s) present: ${bad[*]}" >&2
    record_result "overall" "error" "refused: GITHUB_ACTIONS=true but test-injection env var(s) present (not a legitimate production evidence source): ${bad[*]}"
    exit 1
  fi
  return 0
}

# ============================================================
# check_native_opt_in_contradiction — 011（010 主審 N1-N3 後的一致性
# 收斂）：fake mode 現在是明確宣告的 P0_FAKE_MODE=1（＋P0_FAKE_ROOT）,
# 不再是「OSASCRIPT_BIN／SCREENCAPTURE_BIN／CG_WINDOW_CHECK_CMD 任一有
# 設定」——這裡的互斥檢查跟著改成檢查 P0_FAKE_MODE,與
# `lib/native_gate.py::check_path_in_fake_root` 的互斥規則一致（同一份
# 判定邏輯,這裡只是提前在 driver 前置就攔一次,不必等到第一個真工具呼叫
# 點才發現）。這個檢查對任何完全沒設定 P0_ALLOW_NATIVE／P0_FAKE_MODE 的
# 呼叫（例如覆寫整個 step_* 函式、不會走到真正呼叫真工具那條路的合成
# 反例）不會誤傷。
# ============================================================
check_native_opt_in_contradiction() {
  if [ "${P0_ALLOW_NATIVE:-}" != "1" ]; then
    return 0
  fi
  if [ "${P0_FAKE_MODE:-}" = "1" ]; then
    echo "FATAL: refusing to run with P0_ALLOW_NATIVE=1 AND P0_FAKE_MODE=1 set simultaneously (mutually exclusive)" >&2
    record_result "overall" "error" "refused: P0_ALLOW_NATIVE=1 but P0_FAKE_MODE=1 also present (fake mode and native opt-in are mutually exclusive)"
    exit 1
  fi
  return 0
}

# _last_status_is_bad <comma-separated-step-names>
#
# rc 0 = 這批 step 名稱裡至少有一個最後一筆記錄能合理解釋 rc!=0（error／
# partial／not_available／not_run 任一——not_available/not_run 是步驟函式
# 用來表示「明確停止這條分支」的正常控制流程返回值,不是自我回報造假；只有
# 記錄成 "normal" 卻仍 rc!=0，或者這批 step 完全沒有任何記錄，才是可疑的
# 自我回報造假）；rc 1 = 都不是。
_last_status_is_bad() {
  local owned_csv="$1"
  python3 - "$RESULT_LOG" "$owned_csv" <<'PYEOF'
import json, sys
path, owned_csv = sys.argv[1], sys.argv[2]
owned = [x for x in owned_csv.split(",") if x]
last_status = {}
try:
    with open(path) as f:
        lines = f.readlines()
except FileNotFoundError:
    sys.exit(1)
for line in lines:
    line = line.strip()
    if not line:
        continue
    try:
        rec = json.loads(line)
    except Exception:
        continue
    if rec.get("step") in owned:
        last_status[rec["step"]] = rec.get("status")
JUSTIFIES_NONZERO = ("error", "partial", "not_available", "not_run")
for name in owned:
    if last_status.get(name) in JUSTIFIES_NONZERO:
        sys.exit(0)
sys.exit(1)
PYEOF
}

STEP_RC_MISMATCH=()

# _postcheck_step <comma-separated-owned-step-names>
#
# 008 對 review571 必修 1／B1（main-review FINDINGS.md，2026-09-29）的修正：
# 006/007 的 `_run_step` 只檢查呼叫端回傳的 rc——rc=0 就當這批 owned step
# 全部正常,完全不回頭看 `$RESULT_LOG` 裡這一輪實際寫入的內容。main-review
# 用 `step_audit_observe(){ record_result audit_observe error synthetic;
# return 0; }` 這類「rc=0 但記錄本身是 error/partial/重複/未知狀態/錯誤
# run_id/完全沒記錄」的情境證明：main() 的 fail-fast 級聯在這種情況下不會
# 停,AX/window/content/capture 全部仍會被呼叫,只是最後 compute_overall()
# 從頭掃整份 log 時仍然判對（error/partial）——不是真正的
# stop-before-next-observation。
#
# 本函式不論呼叫端的 rc 是什麼,都要被 `_run_step`／`step_ax_content_read`／
# `step_window_identify` 無條件呼叫,直接讀 `$RESULT_LOG`**這個檔案本身**
# （不是任何本地分類變數）,對 owned_csv 裡每一個 step 名稱驗證：
#   1. 恰好一筆記錄（不缺、不重複）；
#   2. ts/run_id/step/status/detail 五個欄位齊全；
#   3. run_id 等於本次 RUN_ID；
#   4. status 在白名單內；
#   5. status 是 normal,或是這個 step 在固定分支表裡被允許的非 normal 值
#      （`FIXED_BRANCH_EXTRA`：ax_auth_probe／window_cg_crosscheck／
#      ax_content_read_title_json 允許 not_available；
#      process_group_escape_check 額外允許 error，裁定 A）；`not_run` 對
#      所有 step 通用允許——審查過全部真實程式碼路徑,唯一「rc=0 但某個
#      owned step 記錄 not_run」的情形是 step_teardown() 從未建立過
#      supervisor 的最前面分支,其餘 not_run 分支都伴隨 rc=1（rc 檢查本來
#      就會觸發停止,not_run 通用允許不會打開新的繞過路徑，見
#      PLAN-delta.md「為什麼 not_run 對所有 step 通用允許」）。
# 任一項不符,回傳非 0,原因寫進 `_POSTCHECK_FAIL_DETAIL`。
_POSTCHECK_FAIL_DETAIL=""
_postcheck_step() {
  local owned_csv="$1"
  _POSTCHECK_FAIL_DETAIL=$(python3 - "$RESULT_LOG" "$owned_csv" "$RUN_ID" <<'PYEOF'
import json, sys
path, owned_csv, run_id = sys.argv[1], sys.argv[2], sys.argv[3]
owned = [x for x in owned_csv.split(",") if x]
ALLOWED_STATUSES = {"normal", "partial", "not_available", "not_run", "error"}
REQUIRED_FIELDS = {"ts", "run_id", "step", "status", "detail"}
# 固定分支表（review571 decision.md + 007/008 的 PLAN-delta.md）：這個
# step 除了 normal／通用 not_run 以外,還額外允許哪些狀態視為「postcheck
# 通過」。
FIXED_BRANCH_EXTRA = {
    "ax_auth_probe": {"not_available"},
    "window_cg_crosscheck": {"not_available"},
    "ax_content_read_title_json": {"not_available"},
    "process_group_escape_check": {"not_available", "error"},
}
try:
    with open(path) as f:
        lines = f.readlines()
except FileNotFoundError:
    print("RESULT_LOG does not exist")
    sys.exit(1)
counts = {}
last_rec = {}
for line in lines:
    line = line.strip()
    if not line:
        continue
    try:
        rec = json.loads(line)
    except Exception:
        continue
    if not isinstance(rec, dict):
        continue
    step = rec.get("step")
    if step in owned:
        counts[step] = counts.get(step, 0) + 1
        last_rec[step] = rec
reasons = []
for name in owned:
    n = counts.get(name, 0)
    if n == 0:
        reasons.append(f"{name}: missing record (0 found)")
        continue
    if n > 1:
        reasons.append(f"{name}: duplicated record (count={n})")
        continue
    rec = last_rec[name]
    missing_fields = REQUIRED_FIELDS - set(rec.keys())
    if missing_fields:
        reasons.append(f"{name}: missing fields {sorted(missing_fields)}")
        continue
    rec_run_id = rec.get("run_id")
    if rec_run_id != run_id:
        reasons.append(f"{name}: wrong run_id {rec_run_id!r} (expected {run_id!r})")
        continue
    status = rec.get("status")
    if status not in ALLOWED_STATUSES:
        reasons.append(f"{name}: status {status!r} not in whitelist {sorted(ALLOWED_STATUSES)}")
        continue
    allowed_for_step = {"normal", "not_run"} | FIXED_BRANCH_EXTRA.get(name, set())
    if status not in allowed_for_step:
        reasons.append(f"{name}: status={status!r} is not normal and not an allowed fixed-branch status for this step (allowed: {sorted(allowed_for_step)})")
        continue
if reasons:
    print("; ".join(reasons))
    sys.exit(1)
sys.exit(0)
PYEOF
)
  return $?
}

# _run_step <func_name> <comma-separated-owned-step-names>
#
# 008：不論回傳的 rc 是什麼,都無條件呼叫 `_postcheck_step` 讀取
# `$RESULT_LOG` 實際內容驗證——這是 B1 的核心修正。postcheck 失敗時,即使
# 原本 rc=0,`_run_step` 也回傳非 0,讓呼叫端（main() 的 fail-fast 級聯）
# 停止呼叫任何下游 step,不是只靠呼叫端自己回報的 rc。
_RUN_STEP_LAST_REASON=""
_run_step() {
  local func="$1" owned="$2"
  "$func"
  local rc=$?
  if [ "$rc" -ne 0 ]; then
    if ! _last_status_is_bad "$owned"; then
      STEP_RC_MISMATCH+=("$func(rc=$rc,owned=[$owned],no matching error/partial record)")
    fi
  fi
  if ! _postcheck_step "$owned"; then
    # B1 的核心：只有「rc 宣稱成功（0）,但 postcheck 發現記錄本身有問題」
    # 才是真正的 self-report 不一致,才列進 STEP_RC_MISMATCH（讓
    # compute_overall 的 rc_mismatch_count 也看到）。如果 rc 本來就已經
    # 非 0（例如某步驟誠實記錄 partial／error 並回傳對應 rc）,postcheck
    # 在這裡失敗只是「同意」這個步驟不是 normal,是預期內的一致結果,不是
    # 新發現的不一致——不應該把它也塞進 rc_mismatch,否則會把一個本該是
    # partial 的合法結果錯誤地拉高成 error（見 review571 after_audit_partial
    # 反例：audit_observe 誠實記 partial／return 1,不該被本函式判成
    # mismatch）。不論是否列進 STEP_RC_MISMATCH,postcheck 失敗一律讓
    # _run_step 回傳非 0（停止級聯）,這才是 B1 真正要修的行為。
    if [ "$rc" -eq 0 ]; then
      STEP_RC_MISMATCH+=("$func(rc=$rc,owned=[$owned],postcheck_failed:$_POSTCHECK_FAIL_DETAIL)")
    fi
    _RUN_STEP_LAST_REASON="postcheck failed: $_POSTCHECK_FAIL_DETAIL"
    return 1
  fi
  if [ "$rc" -ne 0 ]; then
    _RUN_STEP_LAST_REASON="rc=$rc (non-zero return from $func)"
  else
    _RUN_STEP_LAST_REASON=""
  fi
  return "$rc"
}

# compute_overall <required-steps-csv> <rc-mismatch-count> <run_id>
#
# R1/R2 修正版；007 保留原樣不變——decision.md 明確要求「既有最後
# compute_overall可保留作第二道完整性檢查,不能代替停止控制」。真正的停止
# 控制現在在 main() 的每一步呼叫後立刻執行（見下）,這裡繼續做「事後從頭
# 讀整份 RESULT_LOG 再驗一次白名單/完整性」的獨立複核，兩者故意重疊。
compute_overall() {
  local required_csv="$1" rc_mismatch_count="$2" run_id="$3"
  python3 - "$RESULT_LOG" "$required_csv" "$rc_mismatch_count" "$RECORD_RESULT_IO_FAILED" "$run_id" <<'PYEOF'
import json, sys
path, required_csv, rc_mismatch_count, io_failed, expected_run_id = sys.argv[1:6]
required = [x for x in required_csv.split(",") if x]
rc_mismatch_count = int(rc_mismatch_count)
io_failed = io_failed == "1"
ESCAPE_STEP = "process_group_escape_check"
ALLOWED_STATUSES = {"normal", "partial", "not_available", "not_run", "error"}
ALLOWED_STEPS = set(required) | {"signal_interrupt"}
REQUIRED_FIELDS = {"ts", "run_id", "step", "status", "detail"}

counts = {}
last_status = {}
parse_errors = 0
whitelist_violations = []  # list of (step_or_None, reason)
result_log_unreadable = False
try:
    with open(path) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                rec = json.loads(line)
            except Exception:
                parse_errors += 1
                continue
            if not isinstance(rec, dict):
                parse_errors += 1
                continue
            step = rec.get("step")
            if step == "overall":
                # 不應該出現在 compute_overall 執行當下（overall 只在最後
                # 才寫一次），但如實忽略而不是報錯，避免自我循環依賴。
                continue
            missing_fields = REQUIRED_FIELDS - set(rec.keys())
            if missing_fields:
                whitelist_violations.append((step, "missing_fields:" + ",".join(sorted(missing_fields))))
                continue
            status = rec.get("status")
            rec_run_id = rec.get("run_id")
            if step not in ALLOWED_STEPS:
                whitelist_violations.append((step, "unknown_step"))
                continue
            if status not in ALLOWED_STATUSES:
                whitelist_violations.append((step, f"unknown_or_empty_status:{status!r}"))
                continue
            if rec_run_id != expected_run_id:
                whitelist_violations.append((step, f"wrong_run_id:{rec_run_id!r} expected {expected_run_id!r}"))
                continue
            counts[step] = counts.get(step, 0) + 1
            last_status[step] = status
except FileNotFoundError:
    pass
except Exception as e:
    result_log_unreadable = True
    print(f"compute_overall: RESULT_LOG unreadable ({type(e).__name__}: {e})", file=sys.stderr)

missing = [] if result_log_unreadable else [s for s in required if counts.get(s, 0) == 0]
duplicated = [] if result_log_unreadable else [s for s in required if counts.get(s, 0) > 1]

# R1: process_group_escape_check 的白名單例外——唯一合法狀態是
# not_available；其他任何值（包含完全沒記錄，會另外算進 missing）都是
# error，但它是 not_available 時**不**拉低也**不**拉高 overall。
escape_status = last_status.get(ESCAPE_STEP)
escape_violation = (ESCAPE_STEP in required) and (ESCAPE_STEP in counts) and (escape_status != "not_available")

other_required = [s for s in required if s != ESCAPE_STEP]
other_statuses = {s: last_status.get(s) for s in other_required if s in counts}
any_error = any(v == "error" for v in other_statuses.values())
any_nonnormal_nonerror = any(v in ("not_available", "not_run", "partial") for v in other_statuses.values())

if (any_error or missing or duplicated or rc_mismatch_count > 0 or io_failed
        or parse_errors > 0 or result_log_unreadable or escape_violation or whitelist_violations):
    reasons = []
    if any_error: reasons.append("step_error")
    if missing: reasons.append("missing:" + ",".join(missing))
    if duplicated: reasons.append("duplicated:" + ",".join(duplicated))
    if rc_mismatch_count: reasons.append("rc_mismatch:" + str(rc_mismatch_count))
    if io_failed: reasons.append("record_result_io_failed")
    if parse_errors: reasons.append("result_log_parse_errors:" + str(parse_errors))
    if result_log_unreadable: reasons.append("result_log_unreadable")
    if escape_violation: reasons.append(f"process_group_escape_check_whitelist_violation:{escape_status!r}")
    if whitelist_violations: reasons.append("whitelist_violations:" + ";".join(f"{s}:{r}" for s, r in whitelist_violations))
    print("error 1 " + "|".join(reasons))
elif any_nonnormal_nonerror:
    reasons = [f"{s}={v}" for s, v in other_statuses.items() if v in ("not_available", "not_run", "partial")]
    print("partial 2 nonnormal_required_step:" + ",".join(reasons))
else:
    print("normal 0 all_required_steps_normal_except_escape_check_not_available")
PYEOF
}

# ============================================================
# 前置：fixture 隔離
# ============================================================
preflight() {
  local mkdir_err="$EVIDENCE_DIR/preflight-mkdir.err"
  if ! mkdir -p "$WORKSPACE_DIR" 2>"$mkdir_err"; then
    record_result "preflight" "error" "mkdir -p WORKSPACE_DIR failed: $(cat "$mkdir_err" 2>/dev/null)"
    return 1
  fi
  if [ ! -d "$WORKSPACE_DIR" ]; then
    record_result "preflight" "error" "WORKSPACE_DIR does not exist after mkdir -p succeeded (unexpected)"
    return 1
  fi
  if [ -L "$WORKSPACE_DIR" ]; then
    record_result "preflight" "error" "WORKSPACE_DIR is a symlink, refusing"
    return 1
  fi
  local canon
  if ! canon=$(cd "$WORKSPACE_DIR" 2>/dev/null && pwd -P); then
    record_result "preflight" "error" "cannot cd into WORKSPACE_DIR to resolve canonical path"
    return 1
  fi
  WORKSPACE_DIR="$canon"

  unset WORKBENCH_TOOLS_DIR
  unset WORKBENCH_E2E_START_HIDDEN
  export WORKBENCH_WORKSPACE="$WORKSPACE_DIR"

  if ! cd "$WORKSPACE_DIR"; then
    record_result "preflight" "error" "cd into WORKSPACE_DIR failed after canonicalization"
    return 1
  fi
  record_result "preflight" "normal" "WORKSPACE_DIR fresh/canonical/non-symlink at $WORKSPACE_DIR; cwd set; WORKBENCH_WORKSPACE set; WORKBENCH_TOOLS_DIR/WORKBENCH_E2E_START_HIDDEN cleared"
  return 0
}

# ============================================================
# 步驟 1：spawn — 007 改成啟動 lib/supervise.py app 模式持有 App。
#
# driver 不再自己 `&` 出 App、不再對 APP_PID/APP_PGID 送任何 PID/PGID
# signal。supervisor 把 pid/pgid 寫進 APP_IDENTITY_FILE（唯讀資訊，供
# driver 記錄/顯示用，不是持有或送 signal 的依據）；收尾一律靠
# APP_STOP_FILE 控制檔（見 step_teardown）。
# ============================================================
APP_PID=""
APP_PGID=""
APP_SUPERVISOR_PID=""
APP_STOP_FILE=""
APP_SUPERVISE_RESULT=""
APP_IDENTITY_FILE=""
APP_UNREAPED_LOG=""

step_spawn() {
  # 011（010 主審 N1）：舊版只檢查三個工具變數是否有設定,完全不檢查
  # APP_BIN 本身——`010/main-review/attack-005-native-gate/case1` 證明：
  # 三個工具變數都指向 sentinel、P0_ALLOW_NATIVE 未設定,但 APP_BIN 指向
  # 一個 sentinel app 腳本時,driver 仍然把它啟動了（如果 APP_BIN 是真正
  # 的 sdlc-workbench,就會在沒有 opt-in 的情況下啟動真 App）。改成直接
  # 對 APP_BIN 本身呼叫共用的 `lib/native_gate.py`——跟 `window.sh` 的
  # `_native_gate`、`lib/cg_check.py`／`lib/supervise.py` 內部檢查用同一份
  # 判定邏輯,規則保證一致。這個檢查只在**真正呼叫本函式（沒有被合成反例
  # 整個覆寫掉）**時生效——覆寫掉 step_spawn 的反例（測試 main() 自己的
  # fail-fast 級聯）不會走到這裡，不受影響。
  local app_bin_reason
  if ! app_bin_reason=$(python3 "$SCRIPT_DIR/lib/native_gate.py" check "$APP_BIN" 2>&1 >/dev/null); then
    record_result "spawn" "error" "NATIVE_NOT_ALLOWED: $app_bin_reason"
    return 1
  fi

  APP_STOP_FILE="$EVIDENCE_DIR/app-stop"
  APP_SUPERVISE_RESULT="$EVIDENCE_DIR/app-supervise-result.json"
  APP_IDENTITY_FILE="$EVIDENCE_DIR/app-identity.json"
  APP_UNREAPED_LOG="$EVIDENCE_DIR/unreaped-children.jsonl"

  local argv_json
  argv_json=$(python3 -c "import json,sys; print(json.dumps(sys.argv[1:]))" "$APP_BIN")

  local prev_monitor=0
  case "$-" in *m*) prev_monitor=1 ;; esac
  set -m
  python3 "$SCRIPT_DIR/lib/supervise.py" app \
    --argv-json "$argv_json" \
    --cwd "$WORKSPACE_DIR" \
    --out "$EVIDENCE_DIR/app-stdout.log" --err "$EVIDENCE_DIR/app-stderr.log" \
    --result "$APP_SUPERVISE_RESULT" \
    --unreaped-log "$APP_UNREAPED_LOG" \
    --stop-file "$APP_STOP_FILE" \
    --identity-file "$APP_IDENTITY_FILE" \
    --max-lifetime "$APP_MAX_LIFETIME_SECS" \
    --term-wait "$TERM_WAIT_SECS" --kill-wait "$KILL_WAIT_SECS" --reap-timeout "$_WT_REAP_TIMEOUT_SECS" \
    >"$EVIDENCE_DIR/app-supervisor.log" 2>&1 &
  APP_SUPERVISOR_PID=$!
  if [ "$prev_monitor" -eq 0 ]; then set +m; fi

  local waited=0
  while [ "$waited" -lt "$SPAWN_TIMEOUT_SECS" ]; do
    if [ -s "$APP_IDENTITY_FILE" ]; then
      break
    fi
    sleep 1
    waited=$((waited + 1))
  done
  if [ ! -s "$APP_IDENTITY_FILE" ]; then
    record_result "spawn" "error" "app supervisor (bash job pid=$APP_SUPERVISOR_PID, not a signaling target) did not report identity within ${SPAWN_TIMEOUT_SECS}s; see app-supervisor.log/app-stderr.log"
    return 1
  fi

  APP_PID=$(python3 -c "
import json,sys
try:
    print(json.load(open(sys.argv[1]))['pid'])
except Exception:
    print('')
" "$APP_IDENTITY_FILE")
  APP_PGID=$(python3 -c "
import json,sys
try:
    print(json.load(open(sys.argv[1]))['pgid'])
except Exception:
    print('')
" "$APP_IDENTITY_FILE")
  if [ -z "$APP_PID" ] || [ -z "$APP_PGID" ]; then
    record_result "spawn" "error" "app-identity.json exists but is malformed/unreadable: $(cat "$APP_IDENTITY_FILE" 2>/dev/null)"
    return 1
  fi

  # 唯讀存活確認（kill -0，不是 signal）：確認 supervisor 剛剛回報的這個
  # pid 這一刻確實還在。持有依據本身在 supervisor 內部的
  # os.waitid(WNOWAIT)，driver 從頭到尾不會對 APP_PID/APP_PGID 送任何
  # signal，收尾一律透過 APP_STOP_FILE 控制檔。
  if ! kill -0 "$APP_PID" 2>/dev/null; then
    record_result "spawn" "error" "app-identity.json reports pid=$APP_PID but it is not alive right after supervisor reported identity (see app-stdout.log/app-stderr.log)"
    return 1
  fi

  # 006 同款保留：拿到 identity 檔、確認這一刻存活後，還要再觀察
  # SPAWN_TIMEOUT_SECS 秒確認程序撐得住這段窗口——不是拿到 identity 檔
  # 就直接判 normal。「啟動後幾乎立刻自己死掉」（例如 crash_immediately）
  # 必須在這裡被抓到，不能留到後面才被 audit_observe 之類的下游 step
  # 間接、且用錯誤原因發現。
  local stay_waited=0
  while [ "$stay_waited" -lt "$SPAWN_TIMEOUT_SECS" ]; do
    sleep 1
    stay_waited=$((stay_waited + 1))
    if ! kill -0 "$APP_PID" 2>/dev/null; then
      record_result "spawn" "error" "pid=$APP_PID exited during the SPAWN_TIMEOUT_SECS=${SPAWN_TIMEOUT_SECS}s post-identity observation window; custody was established but process did not stay up (see app-supervise-result.json once the supervisor observes and reports the exit)"
      return 1
    fi
  done

  record_result "spawn" "normal" "app supervisor (bash job pid=$APP_SUPERVISOR_PID) holds app pid=$APP_PID pgid=$APP_PGID (see $APP_IDENTITY_FILE); alive throughout ${SPAWN_TIMEOUT_SECS}s post-identity spawn window; custody and future TERM/KILL/reap is entirely inside supervise.py via Popen+os.waitid(WNOWAIT) (see PLAN-delta.md); driver never signals by PID"
  return 0
}

# ============================================================
# 步驟 2：audit 觀測
#
# R6：輪詢迴圈受 `_bounded_step_timeout` 約束。這個 step 沒有 not_available
# 分支——非 normal 一律是 fail-fast 停止（見 main()）。
# ============================================================
step_audit_observe() {
  local bounded_secs; bounded_secs=$(_bounded_step_timeout "$AUDIT_TIMEOUT_SECS")
  if [ "$bounded_secs" -le 0 ]; then
    record_result "audit_observe" "partial" "skipped: insufficient remaining total budget (remaining_budget=$(remaining_budget) reserve=$TEARDOWN_RESERVE_SECS own_limit=$AUDIT_TIMEOUT_SECS)"
    return 1
  fi
  local audit_path="$WORKSPACE_DIR/.workbench/audit.jsonl"
  local deadline=$(( $(date -u +%s) + bounded_secs ))
  local last_cls="pending_no_match"
  while [ "$(date -u +%s)" -lt "$deadline" ]; do
    if [ -f "$audit_path" ]; then
      last_cls=$(classify_audit_snapshot "$audit_path" "$WORKSPACE_DIR" "$EXPECTED_TOOLS_DIR" 2>>"$EVIDENCE_DIR/audit-classify.err")
      case "$last_cls" in
        normal)
          cp "$audit_path" "$EVIDENCE_DIR/audit.jsonl.snapshot" || record_result "audit_observe" "error" "matched but snapshot copy failed"
          record_result "audit_observe" "normal" "matched exactly one startup record for workspace=$WORKSPACE_DIR (source contract: tools_source=bundle, tools_dir=$EXPECTED_TOOLS_DIR); bounded_secs=$bounded_secs"
          return 0
          ;;
        error)
          cp "$audit_path" "$EVIDENCE_DIR/audit.jsonl.snapshot.corrupt" 2>/dev/null || true
          record_result "audit_observe" "error" "strict JSONL parse/source-contract failed (see audit-classify.err and audit.jsonl.snapshot.corrupt): $(tail -n1 "$EVIDENCE_DIR/audit-classify.err" 2>/dev/null)"
          return 1
          ;;
      esac
    fi
    sleep 1
  done
  if [ -f "$audit_path" ]; then
    cp "$audit_path" "$EVIDENCE_DIR/audit.jsonl.snapshot.timeout" 2>/dev/null || true
    case "$last_cls" in
      pending_incomplete_tail)
        record_result "audit_observe" "error" "timed out after ${bounded_secs}s (bounded by remaining budget) with an incomplete/unparseable tail line that never resolved (see audit.jsonl.snapshot.timeout)"
        return 1
        ;;
      *)
        record_result "audit_observe" "partial" "timed out after ${bounded_secs}s (bounded by remaining budget); audit.jsonl exists and is well-formed so far but no matching startup record found (see audit.jsonl.snapshot.timeout)"
        return 1
        ;;
    esac
  else
    record_result "audit_observe" "error" "timed out after ${bounded_secs}s (bounded by remaining budget); audit.jsonl does not exist at $audit_path"
  fi
  return 1
}

# ============================================================
# 步驟 3：AX 授權探測
#
# 007：with_timeout 呼叫點改成透過 lib/window_fn_runner.sh。回傳碼語意
# 不變（0=normal才能繼續；非0=not_available/error/partial，main() 一律停
# 止呼叫下游 step，見必修 1／裁定 A 的 fixed-branch 範例：
# ax_auth_probe=not_available(TCC)→window/content/capture not_run，直接
# 到teardown——error/partial 用同一個「停止」動作，detail 已寫明實際狀態）。
# ============================================================
AX_AVAILABLE=0

step_ax_auth_probe() {
  local bounded_secs; bounded_secs=$(_bounded_step_timeout "$AX_AUTH_TIMEOUT_SECS")
  if [ "$bounded_secs" -le 0 ]; then
    record_result "ax_auth_probe" "partial" "skipped: insufficient remaining total budget (remaining_budget=$(remaining_budget) reserve=$TEARDOWN_RESERVE_SECS own_limit=$AX_AUTH_TIMEOUT_SECS)"
    return 1
  fi
  local out_file="$EVIDENCE_DIR/ax-auth-stdout.txt"
  local err_file="$EVIDENCE_DIR/ax-auth-stderr.txt"
  local meta_json
  meta_json=$(with_timeout "$bounded_secs" "$out_file" "$err_file" "$WINDOW_FN_RUNNER" ax_auth_probe "$APP_PID")
  local rc; rc=$(python3 -c "import json,sys; print(json.loads(sys.argv[1])['rc'])" "$meta_json" 2>/dev/null || echo "null")
  local timed_out; timed_out=$(python3 -c "import json,sys; print(json.loads(sys.argv[1])['timed_out'])" "$meta_json" 2>/dev/null || echo "true")
  local out; out=$(cat "$out_file" 2>/dev/null || echo "")
  local err; err=$(cat "$err_file" 2>/dev/null || echo "")
  if [ "$timed_out" = "True" ] || [ "$timed_out" = "true" ]; then
    _apply_residual_gate "error" "$meta_json"
    record_result "ax_auth_probe" "$_RESIDUAL_GATE_STATUS" "timed out after ${bounded_secs}s (bounded by remaining budget; see $out_file/$err_file)${_RESIDUAL_GATE_SUFFIX}"
    return 1
  fi
  local cls; cls=$(classify_ax_auth_probe_output "${rc:-1}" "$out" "$err")
  _apply_residual_gate "$cls" "$meta_json"
  cls="$_RESIDUAL_GATE_STATUS"
  record_result "ax_auth_probe" "$cls" "rc=$rc out=[$out] err=[$err]${_RESIDUAL_GATE_SUFFIX}"
  if [ "$cls" = "normal" ]; then
    AX_AVAILABLE=1
    return 0
  fi
  return 1
}

# ============================================================
# 步驟 4：window 身分確認（get_window_id + cg 交叉核對）
#
# 007：with_timeout 呼叫點改成透過 lib/window_fn_runner.sh。回傳碼語意
# 不變（0=normal才能繼續；非0=window_cg_crosscheck 非 normal——含
# not_available(QUARTZ_NOT_AVAILABLE) 這個固定分支——main() 一律停止呼叫
# ax_content_read/screencapture）。
# ============================================================
WINDOW_ID=""
WINDOW_CONFIRMED=0

step_window_identify() {
  if [ "$AX_AVAILABLE" -ne 1 ]; then
    # 防禦性保留：main() 007 起只在 ax_auth_probe 回傳 0 時才呼叫本函式，
    # 這個分支理論上不會再被走到，但保留作第二層防呆，不因為上層已經檔過
    # 就假設這裡不需要自己再檢查一次。
    record_result "window_id" "not_run" "skipped: AX not available (window id probe depends on System Events)"
    record_result "window_cg_crosscheck" "not_run" "skipped: AX not available"
    return 1
  fi
  local win_bounded; win_bounded=$(_bounded_step_timeout "$WINDOW_TIMEOUT_SECS")
  if [ "$win_bounded" -le 0 ]; then
    record_result "window_id" "partial" "skipped: insufficient remaining total budget (remaining_budget=$(remaining_budget) reserve=$TEARDOWN_RESERVE_SECS own_limit=$WINDOW_TIMEOUT_SECS)"
    record_result "window_cg_crosscheck" "not_run" "skipped: window_id skipped due to budget"
    return 1
  fi
  local win_out="$EVIDENCE_DIR/window-id-stdout.txt"
  local win_err="$EVIDENCE_DIR/window-id-stderr.txt"
  local win_json
  win_json=$(with_timeout "$win_bounded" "$win_out" "$win_err" "$WINDOW_FN_RUNNER" get_window_id "$APP_PID")
  local win_timed_out; win_timed_out=$(python3 -c "import json,sys; print(json.loads(sys.argv[1])['timed_out'])" "$win_json" 2>/dev/null || echo "true")
  local win_rc; win_rc=$(python3 -c "import json,sys; print(json.loads(sys.argv[1])['rc'])" "$win_json" 2>/dev/null || echo "1")
  if [ "$win_timed_out" = "True" ] || [ "$win_timed_out" = "true" ]; then
    _apply_residual_gate "error" "$win_json"
    record_result "window_id" "$_RESIDUAL_GATE_STATUS" "timed out after ${win_bounded}s (bounded by remaining budget)${_RESIDUAL_GATE_SUFFIX}"
    record_result "window_cg_crosscheck" "not_run" "skipped: window id timed out"
    return 1
  fi
  local win_out_text; win_out_text=$(cat "$win_out" 2>/dev/null || echo "")
  local win_err_text; win_err_text=$(cat "$win_err" 2>/dev/null || echo "")
  local win_cls; win_cls=$(classify_window_id_output "$win_rc" "$win_out_text" "$win_err_text")
  _apply_residual_gate "$win_cls" "$win_json"
  win_cls="$_RESIDUAL_GATE_STATUS"
  record_result "window_id" "$win_cls" "rc=$win_rc out=[$win_out_text] err=[$win_err_text]${_RESIDUAL_GATE_SUFFIX}"
  # 008（B1）：同一 step 內下一個 helper（window_cg_crosscheck）呼叫前,
  # 對「window_id 這筆記錄實際寫進 RESULT_LOG 的內容」做 postcheck——不是
  # 只看本地變數 $win_cls（兩者在真正的程式路徑裡永遠一致,但 postcheck
  # 讀的是最終作為證據的那一份,不是變數的複本，見 PLAN-delta.md）。
  if ! _postcheck_step "window_id"; then
    record_result "window_cg_crosscheck" "not_run" "not_run: stopped because window_id postcheck failed: $_POSTCHECK_FAIL_DETAIL (fail-fast, next helper in this step not attempted)"
    return 1
  fi
  WINDOW_ID=$(echo "$win_out_text" | tr -d ' \n')

  local cg_bounded; cg_bounded=$(_bounded_step_timeout "$CG_CHECK_TIMEOUT_SECS")
  if [ "$cg_bounded" -le 0 ]; then
    record_result "window_cg_crosscheck" "partial" "skipped: insufficient remaining total budget (remaining_budget=$(remaining_budget) reserve=$TEARDOWN_RESERVE_SECS own_limit=$CG_CHECK_TIMEOUT_SECS)"
    return 1
  fi
  local cg_out="$EVIDENCE_DIR/cg-crosscheck-stdout.txt"
  local cg_err="$EVIDENCE_DIR/cg-crosscheck-stderr.txt"
  local cg_json
  cg_json=$(with_timeout "$cg_bounded" "$cg_out" "$cg_err" "$WINDOW_FN_RUNNER" cg_window_owner_check "$APP_PID" "$WINDOW_ID")
  local cg_timed_out; cg_timed_out=$(python3 -c "import json,sys; print(json.loads(sys.argv[1])['timed_out'])" "$cg_json" 2>/dev/null || echo "true")
  local cg_rc; cg_rc=$(python3 -c "import json,sys; print(json.loads(sys.argv[1])['rc'])" "$cg_json" 2>/dev/null || echo "1")
  local cg_result; cg_result=$(cat "$cg_out" 2>/dev/null | tr -d ' \n' || echo "")

  if [ "$cg_timed_out" = "True" ] || [ "$cg_timed_out" = "true" ]; then
    _apply_residual_gate "error" "$cg_json"
    record_result "window_cg_crosscheck" "$_RESIDUAL_GATE_STATUS" "cg_window_owner_check timed out after ${cg_bounded}s (bounded by remaining budget); owner/id not independently confirmed, refusing to capture${_RESIDUAL_GATE_SUFFIX}"
    return 1
  fi
  if [ "$cg_rc" != "0" ]; then
    _apply_residual_gate "error" "$cg_json"
    record_result "window_cg_crosscheck" "$_RESIDUAL_GATE_STATUS" "cg_window_owner_check exited rc=$cg_rc (unknown result is not confirmation); refusing to capture: $(cat "$cg_err" 2>/dev/null)${_RESIDUAL_GATE_SUFFIX}"
    return 1
  fi
  local cg_cls
  case "$cg_result" in
    MATCH) cg_cls="normal" ;;
    QUARTZ_NOT_AVAILABLE) cg_cls="not_available" ;;
    MISMATCH*) cg_cls="error" ;;
    *) cg_cls="error" ;;
  esac
  _apply_residual_gate "$cg_cls" "$cg_json"
  cg_cls="$_RESIDUAL_GATE_STATUS"
  local cg_suffix="$_RESIDUAL_GATE_SUFFIX"
  case "$cg_result" in
    MATCH)
      if [ "$cg_cls" = "normal" ]; then
        record_result "window_cg_crosscheck" "normal" "CGWindowListCopyWindowInfo confirms unique window owned by pid=$APP_PID matching AX id=$WINDOW_ID$cg_suffix"
        WINDOW_CONFIRMED=1
        return 0
      fi
      record_result "window_cg_crosscheck" "$cg_cls" "CGWindowListCopyWindowInfo confirms MATCH but residual gate failed:$cg_suffix"
      return 1
      ;;
    QUARTZ_NOT_AVAILABLE)
      record_result "window_cg_crosscheck" "$cg_cls" "no Quartz/pyobjc in this environment; cannot independently verify CGWindowID — treated as UNCONFIRMED, capture withheld (not_available no longer bypasses the capture gate; fixed-branch example in decision.md: QUARTZ_NOT_AVAILABLE → content/capture not_run, straight to teardown)$cg_suffix"
      return 1
      ;;
    MISMATCH*)
      record_result "window_cg_crosscheck" "$cg_cls" "CGWindowID cross-check mismatch: $cg_result; refusing to capture a possibly-wrong window$cg_suffix"
      return 1
      ;;
    *)
      record_result "window_cg_crosscheck" "$cg_cls" "unrecognized cg_window_owner_check output: $cg_result$cg_suffix"
      return 1
      ;;
  esac
}

# ============================================================
# 步驟 5：AX 內容讀取（僅當 window 已確認唯一，見 WINDOW_CONFIRMED）
#
# 007（必修 1，同一 step 內多個 helper 也適用 fail-fast）：meta／
# title_json／tree 三個 helper 現在嚴格依序執行，前一個出現非白名單允許
# 的 unexpected error 就不再呼叫下一個（helper 內部連 with_timeout 都不
# 送出）。title_json 是唯一允許 not_available 仍繼續（到 tree）的固定
# 分支——但如果 residual gate 把它從 not_available 降成 error，就不再算
# 固定分支允許的情況，一樣要停（裁定 A：已知失敗不得被結構性例外吸收）。
# 回傳碼：0 = meta=normal 且 title∈{normal,not_available} 且 tree=normal
# （main() 才會繼續呼叫 step_screencapture）；1 = 以上任一不成立。
# ============================================================
step_ax_content_read() {
  if [ "$AX_AVAILABLE" -ne 1 ]; then
    record_result "ax_content_read_meta" "not_run" "skipped: ax_auth_probe did not return normal"
    record_result "ax_content_read_title_json" "not_run" "skipped: ax_auth_probe did not return normal"
    record_result "ax_content_read_tree" "not_run" "skipped: ax_auth_probe did not return normal"
    return 1
  fi
  if [ "$WINDOW_CONFIRMED" -ne 1 ]; then
    record_result "ax_content_read_meta" "not_run" "skipped: window uniqueness not confirmed (see window_id/window_cg_crosscheck)"
    record_result "ax_content_read_title_json" "not_run" "skipped: window uniqueness not confirmed"
    record_result "ax_content_read_tree" "not_run" "skipped: window uniqueness not confirmed"
    return 1
  fi

  # ---- helper 1/3: meta ----
  local meta_bounded; meta_bounded=$(_bounded_step_timeout "$AX_CONTENT_TIMEOUT_SECS")
  if [ "$meta_bounded" -le 0 ]; then
    record_result "ax_content_read_meta" "partial" "skipped: insufficient remaining total budget"
    record_result "ax_content_read_title_json" "not_run" "not_run: stopped because ax_content_read_meta was skipped (insufficient budget; fail-fast, no further helper in this step attempted)"
    record_result "ax_content_read_tree" "not_run" "not_run: stopped because ax_content_read_meta was skipped (insufficient budget)"
    return 1
  fi
  local meta_out="$EVIDENCE_DIR/ax-meta-stdout.txt"
  local meta_err="$EVIDENCE_DIR/ax-meta-stderr.txt"
  local meta_json
  meta_json=$(with_timeout "$meta_bounded" "$meta_out" "$meta_err" "$WINDOW_FN_RUNNER" ax_meta_text "$APP_PID")
  local meta_timed_out; meta_timed_out=$(python3 -c "import json,sys; print(json.loads(sys.argv[1])['timed_out'])" "$meta_json" 2>/dev/null || echo "true")
  local meta_rc; meta_rc=$(python3 -c "import json,sys; print(json.loads(sys.argv[1])['rc'])" "$meta_json" 2>/dev/null || echo "1")
  local meta_final_cls
  if [ "$meta_timed_out" = "True" ] || [ "$meta_timed_out" = "true" ]; then
    _apply_residual_gate "error" "$meta_json"
    meta_final_cls="$_RESIDUAL_GATE_STATUS"
    record_result "ax_content_read_meta" "$meta_final_cls" "timed out after ${meta_bounded}s (bounded by remaining budget)${_RESIDUAL_GATE_SUFFIX}"
  elif [ "$meta_rc" != "0" ]; then
    _apply_residual_gate "error" "$meta_json"
    meta_final_cls="$_RESIDUAL_GATE_STATUS"
    record_result "ax_content_read_meta" "$meta_final_cls" "ax_meta_text exited rc=$meta_rc (non-zero AX command is not success even if stdout is non-empty): $(cat "$meta_out" 2>/dev/null)${_RESIDUAL_GATE_SUFFIX}"
  else
    local text; text=$(cat "$meta_out" 2>/dev/null || echo "")
    local cls; cls=$(classify_meta_text "$text" "$WORKSPACE_DIR" 2>>"$EVIDENCE_DIR/ax-meta-classify.err")
    _apply_residual_gate "$cls" "$meta_json"
    meta_final_cls="$_RESIDUAL_GATE_STATUS"
    record_result "ax_content_read_meta" "$meta_final_cls" "text=[$text] observed_at=$(date -u +%Y-%m-%dT%H:%M:%S.%NZ) source=ax_meta_text(independent AppleScript call, not the same read as ax_content_read_title_json)${_RESIDUAL_GATE_SUFFIX}"
  fi
  # meta 沒有 not_available 這個允許分支——非 normal 就 fail-fast 停止，
  # title_json／tree 完全不呼叫。008（B1）：改用 `_postcheck_step` 讀
  # RESULT_LOG 實際內容判定,不是只看本地變數 $meta_final_cls（原本的
  # 檢查在真正的程式路徑裡永遠跟 postcheck 結果一致,但只有讀日誌本身才
  # 是 review571 要求的「驗證用的值＝最終作為證據的值」）。
  if ! _postcheck_step "ax_content_read_meta"; then
    record_result "ax_content_read_title_json" "not_run" "not_run: stopped because ax_content_read_meta postcheck failed: $_POSTCHECK_FAIL_DETAIL (fail-fast, no further helper in this step attempted)"
    record_result "ax_content_read_tree" "not_run" "not_run: stopped because ax_content_read_meta postcheck failed: $_POSTCHECK_FAIL_DETAIL"
    return 1
  fi

  # ---- helper 2/3: title_json ----
  local title_bounded; title_bounded=$(_bounded_step_timeout "$AX_CONTENT_TIMEOUT_SECS")
  if [ "$title_bounded" -le 0 ]; then
    record_result "ax_content_read_title_json" "partial" "skipped: insufficient remaining total budget"
    record_result "ax_content_read_tree" "not_run" "not_run: stopped because ax_content_read_title_json was skipped (insufficient budget)"
    return 1
  fi
  local title_out="$EVIDENCE_DIR/ax-meta-title-stdout.txt"
  local title_err="$EVIDENCE_DIR/ax-meta-title-stderr.txt"
  local title_json
  title_json=$(with_timeout "$title_bounded" "$title_out" "$title_err" "$WINDOW_FN_RUNNER" ax_meta_title_json "$APP_PID")
  local title_timed_out; title_timed_out=$(python3 -c "import json,sys; print(json.loads(sys.argv[1])['timed_out'])" "$title_json" 2>/dev/null || echo "true")
  local title_rc; title_rc=$(python3 -c "import json,sys; print(json.loads(sys.argv[1])['rc'])" "$title_json" 2>/dev/null || echo "1")
  local title_final_cls
  if [ "$title_timed_out" = "True" ] || [ "$title_timed_out" = "true" ] || [ "$title_rc" != "0" ]; then
    _apply_residual_gate "not_available" "$title_json"
    title_final_cls="$_RESIDUAL_GATE_STATUS"
    record_result "ax_content_read_title_json" "$title_final_cls" "AXHelp probe timed_out=$title_timed_out rc=$title_rc bounded_secs=$title_bounded (known gap, see window.sh ax_meta_title_json)${_RESIDUAL_GATE_SUFFIX}"
  else
    local title_text; title_text=$(cat "$title_out" 2>/dev/null || echo "")
    local title_cls; title_cls=$(classify_cli_info_json "$title_text" "$WORKSPACE_DIR" "$EXPECTED_TOOLS_DIR" 2>>"$EVIDENCE_DIR/ax-meta-title-classify.err")
    # 010（必修 5）：meta／title 各自分類都是 normal 時,再比對兩次獨立
    # 觀測共有的欄位（workspace／toolsSource）是否矛盾——矛盾就把這裡的
    # 分類降成 error,不能混成一份不存在的原子快照。折進同一次分類、
    # 同一次 record_result,不額外多寫一筆（避免被 postcheck 的「恰好一筆
    # 記錄」規則誤判成重複）。
    local consistency_detail=""
    if [ "$title_cls" = "normal" ]; then
      local consistency_cls
      consistency_cls=$(classify_meta_title_consistency "$text" "$title_text" 2>"$EVIDENCE_DIR/ax-meta-title-consistency.err")
      if [ "$consistency_cls" != "normal" ]; then
        title_cls="error"
        consistency_detail=" meta/title_consistency_check_failed:[$(tail -n1 "$EVIDENCE_DIR/ax-meta-title-consistency.err" 2>/dev/null)]"
      fi
    fi
    _apply_residual_gate "$title_cls" "$title_json"
    title_final_cls="$_RESIDUAL_GATE_STATUS"
    record_result "ax_content_read_title_json" "$title_final_cls" "AXHelp text length=${#title_text} observed_at=$(date -u +%Y-%m-%dT%H:%M:%S.%NZ) source=ax_meta_title_json(independent AppleScript call, not the same read as ax_content_read_meta)${consistency_detail}${_RESIDUAL_GATE_SUFFIX}"
  fi
  # title_json 是決策明列的固定分支：not_available 可以繼續到 tree。但
  # residual gate 若把它強制降成 error（例如殘留非 0），就不是 helper 自
  # 己回報的 not_available 了——必須當成 unexpected error 一樣停止，不能
  # 被固定分支豁免（裁定 A）。008（B1）：同樣改用 `_postcheck_step` 讀
  # RESULT_LOG 實際內容——`_postcheck_step` 的 FIXED_BRANCH_EXTRA 已經把
  # ax_content_read_title_json 的 not_available 算進允許值,不需要在這裡
  # 重複列 not_available 這個分支。
  if ! _postcheck_step "ax_content_read_title_json"; then
    record_result "ax_content_read_tree" "not_run" "not_run: stopped because ax_content_read_title_json postcheck failed: $_POSTCHECK_FAIL_DETAIL (fail-fast, no further helper in this step attempted)"
    return 1
  fi

  # ---- helper 3/3: tree ----
  local tree_bounded; tree_bounded=$(_bounded_step_timeout "$AX_CONTENT_TIMEOUT_SECS")
  if [ "$tree_bounded" -le 0 ]; then
    record_result "ax_content_read_tree" "partial" "skipped: insufficient remaining total budget"
    return 1
  fi
  local tree_out="$EVIDENCE_DIR/ax-tree-dump.txt"
  local tree_err="$EVIDENCE_DIR/ax-tree-dump.err"
  local tree_json
  tree_json=$(with_timeout "$tree_bounded" "$tree_out" "$tree_err" "$WINDOW_FN_RUNNER" ax_dump_window_tree "$APP_PID")
  local tree_timed_out; tree_timed_out=$(python3 -c "import json,sys; print(json.loads(sys.argv[1])['timed_out'])" "$tree_json" 2>/dev/null || echo "true")
  local tree_rc; tree_rc=$(python3 -c "import json,sys; print(json.loads(sys.argv[1])['rc'])" "$tree_json" 2>/dev/null || echo "1")
  local tree_final_cls
  if [ "$tree_timed_out" = "True" ] || [ "$tree_timed_out" = "true" ]; then
    _apply_residual_gate "error" "$tree_json"
    tree_final_cls="$_RESIDUAL_GATE_STATUS"
    record_result "ax_content_read_tree" "$tree_final_cls" "timed out after ${tree_bounded}s (AX tree dump bounded but incomplete)${_RESIDUAL_GATE_SUFFIX}"
  elif [ "$tree_rc" != "0" ]; then
    _apply_residual_gate "error" "$tree_json"
    tree_final_cls="$_RESIDUAL_GATE_STATUS"
    record_result "ax_content_read_tree" "$tree_final_cls" "ax_dump_window_tree exited rc=$tree_rc (non-zero AX command is not success even if stdout non-empty)${_RESIDUAL_GATE_SUFFIX}"
  elif [ -s "$tree_out" ]; then
    _apply_residual_gate "normal" "$tree_json"
    tree_final_cls="$_RESIDUAL_GATE_STATUS"
    record_result "ax_content_read_tree" "$tree_final_cls" "AX tree captured with per-element role/name/value/help/title/position/size (not raw object references), see ax-tree-dump.txt${_RESIDUAL_GATE_SUFFIX}"
  else
    _apply_residual_gate "error" "$tree_json"
    tree_final_cls="$_RESIDUAL_GATE_STATUS"
    record_result "ax_content_read_tree" "$tree_final_cls" "empty AX tree dump${_RESIDUAL_GATE_SUFFIX}"
  fi

  # 008（B1）：最後一個 helper（tree）也一樣做 postcheck,用它取代原本
  # 只看 $tree_final_cls 的判斷——這裡沒有下一個 helper 要 gate,但函式本身
  # 的回傳碼會被外層 `_run_step` 拿去決定要不要呼叫 step_screencapture,
  # 一樣要以 RESULT_LOG 實際內容為準。
  if _postcheck_step "ax_content_read_tree"; then
    return 0
  fi
  return 1
}

# ============================================================
# 步驟 6：screencapture（僅當 window 已確認唯一）
# 007：with_timeout 呼叫點改成透過 lib/window_fn_runner.sh。
# ============================================================
step_screencapture() {
  if [ "$WINDOW_CONFIRMED" -ne 1 ]; then
    record_result "screencapture" "not_run" "skipped: window uniqueness not confirmed (see window_id/window_cg_crosscheck)"
    return 0
  fi
  local cap_bounded; cap_bounded=$(_bounded_step_timeout "$SCREENCAPTURE_TIMEOUT_SECS")
  if [ "$cap_bounded" -le 0 ]; then
    record_result "screencapture" "partial" "skipped: insufficient remaining total budget (remaining_budget=$(remaining_budget) reserve=$TEARDOWN_RESERVE_SECS own_limit=$SCREENCAPTURE_TIMEOUT_SECS)"
    return 1
  fi
  local png="$EVIDENCE_DIR/window-${WINDOW_ID}.png"
  local cap_out="$EVIDENCE_DIR/screencapture-stdout.txt"
  local cap_err="$EVIDENCE_DIR/screencapture-stderr.txt"
  local cap_json
  cap_json=$(with_timeout "$cap_bounded" "$cap_out" "$cap_err" "$WINDOW_FN_RUNNER" screencapture_window "$WINDOW_ID" "$png")
  local cap_timed_out; cap_timed_out=$(python3 -c "import json,sys; print(json.loads(sys.argv[1])['timed_out'])" "$cap_json" 2>/dev/null || echo "true")
  local cap_rc; cap_rc=$(python3 -c "import json,sys; print(json.loads(sys.argv[1])['rc'])" "$cap_json" 2>/dev/null || echo "1")
  if [ "$cap_timed_out" = "True" ] || [ "$cap_timed_out" = "true" ]; then
    _apply_residual_gate "error" "$cap_json"
    record_result "screencapture" "$_RESIDUAL_GATE_STATUS" "timed out after ${cap_bounded}s (bounded by remaining budget)${_RESIDUAL_GATE_SUFFIX}"
    return 1
  fi
  local cls; cls=$(classify_screencapture_output "$cap_rc" "$png")
  local detail="rc=$cap_rc png=$png"
  if [ -f "$png" ]; then
    detail="$detail sha256=$(shasum -a 256 "$png" 2>/dev/null | awk '{print $1}') bytes=$(stat -f%z "$png" 2>/dev/null || echo unknown)"
  fi
  _apply_residual_gate "$cls" "$cap_json"
  record_result "screencapture" "$_RESIDUAL_GATE_STATUS" "$detail${_RESIDUAL_GATE_SUFFIX}"
  if [ "$_RESIDUAL_GATE_STATUS" != "normal" ]; then
    return 1
  fi
  return 0
}

# ============================================================
# 步驟 7：收尾 — 007 改成用控制檔請求 App supervisor（`lib/supervise.py
# app` 模式）收尾，driver 不再對 APP_PID/APP_PGID 送任何 PID/PGID
# signal（移除舊的 `_count_pgid_residual`／`_confirm_pgid_ownership`／裸
# PID kill fallback）。single-flight（TEARDOWN_STARTED 只允許進入一次）。
# ============================================================
TEARDOWN_STARTED=0

step_teardown() {
  if [ "$TEARDOWN_STARTED" -eq 1 ]; then
    return 0
  fi
  TEARDOWN_STARTED=1

  if [ -z "$APP_STOP_FILE" ]; then
    record_result "teardown" "not_run" "no app supervisor was ever started (spawn did not succeed before an app-stop-file path was established)"
    record_result "process_group_escape_check" "not_available" "no supervised app to check (spawn never established custody)"
    return 0
  fi

  # 控制檔請求收尾——不對 supervisor 或 App 送任何 PID/PGID signal。
  local stopfile_err="$EVIDENCE_DIR/teardown-stopfile.err"
  if ! : > "$APP_STOP_FILE" 2>"$stopfile_err"; then
    record_result "teardown" "error" "failed to write stop-file $APP_STOP_FILE: $(cat "$stopfile_err" 2>/dev/null)"
    record_result "process_group_escape_check" "not_available" "teardown could not request stop; escape detection not attempted"
    return 1
  fi

  # teardown_bound：驅動輪詢 APP_SUPERVISE_RESULT 的上限，必須至少涵蓋
  # supervisor 內部（run_app）完整最壞情況序列，否則會在 supervisor 仍在
  # 自己有界視窗內合法工作時就先判 teardown=error（假警報，不是真正的
  # fail-open,但仍是不必要的誤判——見 PLAN-delta.md「訊號持有與 timeout
  # 表」）：leader TERM 等待（TERM_WAIT_SECS）+ leader KILL 等待
  # （KILL_WAIT_SECS）+ leader 有界 reap 確認（_WT_REAP_TIMEOUT_SECS）+
  # leader 回收後的 group cleanup（_bounded_group_cleanup 用
  # max(TERM_WAIT_SECS,KILL_WAIT_SECS,1) 各跑一次 TERM 等待與 KILL 等待,
  # 最壞兩輪都要）。
  # 用跟 TEARDOWN_RESERVE_SECS 同一個全域 _TD_GC_WAIT（頂層算好，見該處
  # 旁註），不在這裡重新算一次——避免兩處各自算導致將來改動時悄悄失準。
  local teardown_bound=$(( TERM_WAIT_SECS + KILL_WAIT_SECS + _WT_REAP_TIMEOUT_SECS + _TD_GC_WAIT + _TD_GC_WAIT + 10 ))
  local waited=0
  while [ "$waited" -lt "$teardown_bound" ]; do
    if [ -s "$APP_SUPERVISE_RESULT" ]; then
      break
    fi
    sleep 1
    waited=$((waited + 1))
  done

  if [ ! -s "$APP_SUPERVISE_RESULT" ]; then
    record_result "teardown" "error" "app supervisor (bash job pid=$APP_SUPERVISOR_PID) did not write a teardown result within ${teardown_bound}s after stop-file was written; driver does not signal it by PID, only observes (see app-supervisor.log)"
    record_result "process_group_escape_check" "not_available" "teardown result unavailable within bound; cannot confirm cleanup scope"
    return 1
  fi

  local result_text; result_text=$(cat "$APP_SUPERVISE_RESULT")
  local rc residual stopped_reason term_converged kill_required
  rc=$(_wt_json_field "$result_text" "rc")
  residual=$(_extract_residual "$result_text")
  stopped_reason=$(_wt_json_field "$result_text" "stopped_reason")
  term_converged=$(_wt_json_field "$result_text" "term_converged")
  kill_required=$(_wt_json_field "$result_text" "kill_required")

  local tc_bin=0 kr_bin=0
  case "$term_converged" in True|true) tc_bin=1 ;; esac
  case "$kill_required" in True|true) kr_bin=1 ;; esac

  local cls; cls=$(classify_teardown_residue "$tc_bin" "$kr_bin" "$residual")
  record_result "teardown" "$cls" "stopped_reason=$stopped_reason rc=$rc term_converged=$term_converged kill_required=$kill_required residual_pgid_members=$residual (see $APP_SUPERVISE_RESULT); driver never sent a PID/PGID signal to the app or its supervisor — only wrote $APP_STOP_FILE and observed the result"

  # process_group_escape_check：裁定 A——結構性限制仍然只有 not_available
  # 這個唯一合法值（不因為「未證明全機器無逃逸」而永遠非零）；但已知未回收
  # 子程序（supervisor 寫進 unreaped-children.jsonl）不得被這個例外吸收，
  # 必須 error。
  if [ -s "$APP_UNREAPED_LOG" ]; then
    record_result "process_group_escape_check" "error" "unreaped children recorded in $APP_UNREAPED_LOG — known unreclaimed child, must not be absorbed by the structural-limitation exception (see review571 裁定 A)"
  else
    record_result "process_group_escape_check" "not_available" "structural: cleanup scope only covers the app process this run's supervisor spawned, held via Popen, and verified exited via os.waitid(WNOWAIT) before reaping (see PLAN-delta.md 裁定 A); cannot prove absence of all descendants across the whole machine, and this round's process rules forbid a global scan to try."
  fi

  if [ "$cls" = "error" ]; then
    return 1
  fi
  return 0
}

# ============================================================
# signal handling
# ============================================================
handle_signal() {
  local sig="$1"
  record_result "signal_interrupt" "error" "driver received SIG$sig; stopping observation, running bounded teardown"
  _run_step step_teardown "teardown,process_group_escape_check" || true
  local overall_line; overall_line=$(compute_overall "$REQUIRED_STEPS_CSV" "${#STEP_RC_MISMATCH[@]}" "$RUN_ID")
  local overall_status; overall_status=$(echo "$overall_line" | awk '{print $1}')
  record_result "overall" "error" "driver received SIG$sig (would-be overall=$overall_status ignored; a signal always forces error)"
  exit 130
}
trap 'handle_signal INT' INT
trap 'handle_signal TERM' TERM

# ============================================================
# main — 007：真正 fail-fast（必修 1）。每個 `_run_step` 呼叫完之後立刻
# 用它的回傳碼決定要不要繼續下一個 phase；非 0（不論是已列明的
# not_available 固定分支還是真正的 error/partial）一律停止呼叫任何下游
# step 函式，把下游所有必要 step 記成 not_run 並附上「在哪一步、為什麼
# 停」的原因，接著進 single-flight teardown。budget 檢查獨立保留在每個
# phase 之前，作為第二種可能觸發停止的條件（belt-and-suspenders，不是
# fail-fast 的替代品）。compute_overall() 依然是最後一道完整性複核。
# ============================================================
REQUIRED_STEPS_CSV="preflight,spawn,audit_observe,ax_auth_probe,window_id,window_cg_crosscheck,ax_content_read_meta,ax_content_read_title_json,ax_content_read_tree,screencapture,teardown,process_group_escape_check"

_record_skip_group() {
  local reason="$1"; shift
  local name
  for name in "$@"; do
    record_result "$name" "not_run" "$reason"
  done
}

main() {
  echo "P0 driver candidate — NOT executed in this round." >&2

  check_no_test_injection_in_ci
  check_native_opt_in_contradiction

  if ! preflight; then
    record_result "spawn" "not_run" "not_run: stopped because preflight failed (fail-fast, no further steps attempted)"
    _record_skip_group "not_run: stopped because preflight failed (fail-fast, no further steps attempted)" audit_observe ax_auth_probe window_id window_cg_crosscheck ax_content_read_meta ax_content_read_title_json ax_content_read_tree screencapture
  elif ! _run_step step_spawn "spawn"; then
    _record_skip_group "not_run: stopped because spawn failed ($_RUN_STEP_LAST_REASON) (fail-fast, no further steps attempted)" audit_observe ax_auth_probe window_id window_cg_crosscheck ax_content_read_meta ax_content_read_title_json ax_content_read_tree screencapture
  elif [ "$(remaining_budget)" -le "$TEARDOWN_RESERVE_SECS" ]; then
    _record_skip_group "not_run: insufficient remaining budget before teardown reserve (after spawn)" audit_observe ax_auth_probe window_id window_cg_crosscheck ax_content_read_meta ax_content_read_title_json ax_content_read_tree screencapture
  elif ! _run_step step_audit_observe "audit_observe"; then
    # audit_observe 沒有 not_available 固定分支：非 normal 一律 fail-fast
    # 停止（review571 after_audit_error/after_audit_partial 兩個反例正是
    # 針對這裡——修法前 AX/window/content/capture 全部仍會被呼叫）。008：
    # 不論 rc 是否為 0，_run_step 內部的 _postcheck_step 都已經對照過
    # RESULT_LOG 的實際內容（B1 修正，見 PLAN-delta.md）。
    _record_skip_group "not_run: stopped because audit_observe did not pass ($_RUN_STEP_LAST_REASON) (fail-fast, no further observation steps run)" ax_auth_probe window_id window_cg_crosscheck ax_content_read_meta ax_content_read_title_json ax_content_read_tree screencapture
  elif [ "$(remaining_budget)" -le "$TEARDOWN_RESERVE_SECS" ]; then
    _record_skip_group "not_run: insufficient remaining budget after audit_observe" ax_auth_probe window_id window_cg_crosscheck ax_content_read_meta ax_content_read_title_json ax_content_read_tree screencapture
  elif ! _run_step step_ax_auth_probe "ax_auth_probe"; then
    # ax_auth_probe 非 normal（含 not_available/TCC 固定分支）一律停止：
    # decision.md 範例 ax_auth_probe=not_available(TCC) → window/content/
    # capture not_run，直接到 teardown；error/partial 走同一個停止動作。
    _record_skip_group "not_run: stopped because ax_auth_probe did not pass ($_RUN_STEP_LAST_REASON) (fixed-branch or fail-fast: window/content/capture not attempted)" window_id window_cg_crosscheck ax_content_read_meta ax_content_read_title_json ax_content_read_tree screencapture
  elif [ "$(remaining_budget)" -le "$TEARDOWN_RESERVE_SECS" ]; then
    _record_skip_group "not_run: insufficient remaining budget after ax_auth_probe" window_id window_cg_crosscheck ax_content_read_meta ax_content_read_title_json ax_content_read_tree screencapture
  elif ! _run_step step_window_identify "window_id,window_cg_crosscheck"; then
    # window_cg_crosscheck 非 normal（含 QUARTZ_NOT_AVAILABLE 固定分支）
    # 一律停止：decision.md 範例 CG報QUARTZ_NOT_AVAILABLE → content/
    # capture not_run。
    _record_skip_group "not_run: stopped because window_identify did not pass ($_RUN_STEP_LAST_REASON) (fixed-branch QUARTZ_NOT_AVAILABLE or fail-fast: content/capture not attempted)" ax_content_read_meta ax_content_read_title_json ax_content_read_tree screencapture
  elif [ "$(remaining_budget)" -le "$TEARDOWN_RESERVE_SECS" ]; then
    _record_skip_group "not_run: insufficient remaining budget after window_identify" ax_content_read_meta ax_content_read_title_json ax_content_read_tree screencapture
  elif ! _run_step step_ax_content_read "ax_content_read_meta,ax_content_read_title_json,ax_content_read_tree"; then
    # step_ax_content_read 內部已經對 meta/title_json/tree 三個 helper 做
    # 過 fail-fast；這裡只需要再檔一次「capture 不該被呼叫」。
    _record_skip_group "not_run: stopped because ax_content_read did not pass ($_RUN_STEP_LAST_REASON) (fail-fast: capture not attempted)" screencapture
  elif [ "$(remaining_budget)" -le "$TEARDOWN_RESERVE_SECS" ]; then
    _record_skip_group "not_run: insufficient remaining budget after ax_content_read" screencapture
  else
    _run_step step_screencapture "screencapture" || true
  fi

  _run_step step_teardown "teardown,process_group_escape_check" || true

  local overall_line; overall_line=$(compute_overall "$REQUIRED_STEPS_CSV" "${#STEP_RC_MISMATCH[@]}" "$RUN_ID")
  local overall_status overall_exit overall_reason
  overall_status=$(echo "$overall_line" | awk '{print $1}')
  overall_exit=$(echo "$overall_line" | awk '{print $2}')
  overall_reason=$(echo "$overall_line" | cut -d' ' -f3-)

  if ! [[ "$overall_exit" =~ ^[0-9]+$ ]]; then
    echo "FATAL: compute_overall did not return a valid exit code (got overall_line=[$overall_line]); this is an internal driver defect, not a normal AC outcome" >&2
    record_result "overall" "error" "compute_overall returned an unparseable line: [$overall_line]" || true
    exit 1
  fi

  record_result "overall" "$overall_status" "driver completed; overall=$overall_status is computed by compute_overall() from the full RESULT_LOG (required-step presence/duplication/whitelist + per-step rc-vs-record cross-check + R1 any-nonnormal-required-step rule, process_group_escape_check excepted), NOT a hand-set flag — this is the second, independent completeness check; the actual stop control happened earlier in main()'s fail-fast cascade above. reason=$overall_reason rc_mismatches=[${STEP_RC_MISMATCH[*]:-}]. overall=normal requires EVERY required step (except process_group_escape_check, whose only legal state is not_available and does not affect this by itself) to be normal; overall=normal still does NOT mean all AC passed."

  if [ "$RECORD_RESULT_IO_FAILED" -eq 1 ]; then
    echo "FATAL: record_result failed to persist at least one record (see stderr above); forcing non-zero exit regardless of computed overall_status=$overall_status" >&2
    exit 1
  fi
  exit "$overall_exit"
}

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  main "$@"
fi
