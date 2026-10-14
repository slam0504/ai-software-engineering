#!/usr/bin/env bash
# candidate/lib/classify.sh
#
# 純函式庫：把各 oracle 的原始輸出轉成五態分類之一。不 spawn、不
# side-effect（除了 echo 分類結果與可選的原因字串），可在完全離線、不啟動
# App 的情況下用合成輸入單元測試（見 ../../tests/）。
#
# 五態定義（沿用 001/002／decision.md 必修 3）：
#   normal        — 步驟完成，且證據滿足該步驟的正例判準。
#   partial       — 步驟完成，但證據不完整／有歧義。不得視為該步驟對應 AC 的 PASS。
#   not_available — 明確的「能力不存在／被拒絕」訊號（例如 TCC 明確拒絕）。
#   error         — 未預期的失敗（含 schema 違反、rc 非零、資料矛盾、來源契約違反）。
#   not_run       — 因為更早的停止條件或總時限耗盡，本步驟根本沒有嘗試。
#
# 2026-09-28 review564 CHANGES_REQUIRED 修正（見
# /Users/eason_tseng/b3a-evidence/2026-09-28-review564/decision.md）：
#
#   F4（bundle_oracle）：classify_cli_info_json / classify_jsonl_startup_line /
#   classify_meta_text / classify_audit_snapshot 過去只驗
#   workspace/workspace_source/startup_error/ready 這幾個「隔離契約」欄位，
#   從未驗過 tools_source／tools_dir／claudeVersion／codexVersion 這組
#   「來源契約」欄位——toolsSource=env、toolsSource=dev-repo、toolsDir 指到
#   bundle 以外的路徑、版本空字串、版本 pin 錯誤，全部被判成 normal。本輪
#   依 decision.md 給定的固定 pin 核對三個事實：
#     - resolveToolsDir()：合法來源只有一種字面值 "bundle"
#       （filepath.Join(dirname(exe), "..", "Resources", "tools")）；
#       "env"／"dev-repo" 都代表沒有用到固定 bundle 內的 tools。P0 用固定、
#       未修改過的 bundle 跑，這兩個值都是來源契約違反。
#     - claudePinVersion="2.1.223"，codexPinVersion="0.146.1"。
#     - claudeVersion 原始格式是 "2.1.223 (Claude Code)"（取第一個 field
#       比對 pin）；codexVersion 原始格式是 "codex-cli 0.146.1"（取最後一個
#       field）；exec 失敗時回傳空字串（不是 not_available 的訊號——固定
#       bundle 理論上一定 exec 得到，空字串代表非預期失敗，必須 error）。
#   新增 CLAUDE_VERSION_PIN / CODEX_VERSION_PIN 兩個常數，三個 classifier
#   分別按各自 schema 有的欄位取用。
#
#   quartz_unavailable_still_captures／pgrep_error_as_zero：
#   classify_screencapture_output 現在驗證 PNG magic bytes、IHDR 可解析、
#   寬高在合理範圍（>0 且 <=16384），不再「rc=0 且檔案非空」就 normal；
#   classify_teardown_residue 現在把 residual 引數的第三種合法值 "unknown"
#   （pgrep 觀測失敗，見 lib/timeout.sh／driver.sh 的 _count_pgid_residual）
#   視為不可判定，一律 error（fail-closed，不能把「查不到」當「查到 0」）。
#
# 本檔不 `set -e`：每個函式自行處理自己的錯誤路徑，呼叫端讀函式回傳值。

set -uo pipefail

# ---- 來源契約 pin（decision.md 給定固定值，對應 app.go/preflight.go 原始碼
# 核對出的 claudePinVersion/codexPinVersion） ----
CLAUDE_VERSION_PIN="2.1.223"
CODEX_VERSION_PIN="0.146.1"
TOOLS_SOURCE_BUNDLE="bundle"

# ---- audit.jsonl 單行 schema（沿用 app.go:2319-2321 實際欄位） ----
_AUDIT_REQUIRED_KEYS_PY='{"workspace","workspace_source","startup_error","node_path","tools_dir","tools_source","node"}'

# classify_jsonl_startup_line <jsonl-line> <expected-workspace-abs-path> [<expected-tools-dir>]
#
# 回傳：normal|partial|error（stdout 印分類，stderr 印原因）
classify_jsonl_startup_line() {
  local line="${1:-}" expected_workspace="${2:-}" expected_tools_dir="${3:-}"
  if [ -z "$expected_workspace" ]; then
    echo "error"; echo "classify_jsonl_startup_line: missing expected_workspace argument" >&2; return 0
  fi
  if [ -z "$line" ]; then
    echo "error"; echo "empty line" >&2; return 0
  fi
  if ! command -v python3 >/dev/null 2>&1; then
    echo "error"; echo "python3 not available for strict JSON parse" >&2; return 0
  fi
  python3 - "$line" "$expected_workspace" "$expected_tools_dir" <<'PYEOF'
import json, sys
line, expected, expected_tools_dir = sys.argv[1], sys.argv[2], sys.argv[3]
REQUIRED_KEYS = {"workspace", "workspace_source", "startup_error", "node_path", "tools_dir", "tools_source", "node"}
try:
    obj = json.loads(line)
except Exception as e:
    print("error"); print(f"invalid JSON: {e}", file=sys.stderr); sys.exit(0)
if not isinstance(obj, dict) or "kind" not in obj or "data" not in obj:
    print("error"); print("missing kind/data top-level keys", file=sys.stderr); sys.exit(0)
if obj.get("kind") != "startup":
    print("partial"); print(f"kind={obj.get('kind')!r} is not 'startup' (may be an unrelated record read out of order)", file=sys.stderr); sys.exit(0)
data = obj.get("data")
if not isinstance(data, dict):
    print("error"); print("data is not an object", file=sys.stderr); sys.exit(0)
keys = set(data.keys())
missing = REQUIRED_KEYS - keys
if missing:
    print("error"); print(f"data missing required keys (app.go:2319-2321 schema): {sorted(missing)}", file=sys.stderr); sys.exit(0)
ws = data.get("workspace")
if ws != expected:
    print("error"); print(f"data.workspace={ws!r} != expected {expected!r} (stale/foreign state dir)", file=sys.stderr); sys.exit(0)
src = data.get("workspace_source")
if src != "env":
    print("error"); print(f"data.workspace_source={src!r} != 'env' (driver always sets WORKBENCH_WORKSPACE; anything else means the override was not honored)", file=sys.stderr); sys.exit(0)
serr = data.get("startup_error")
if serr is None or serr != "":
    print("error"); print(f"data.startup_error={serr!r} is non-empty (a startup blocker was recorded; cannot be treated as a clean startup)", file=sys.stderr); sys.exit(0)
# F4: 來源契約——tools_source 必須是固定 bundle。
tsrc = data.get("tools_source")
if tsrc != "bundle":
    print("error"); print(f"data.tools_source={tsrc!r} != 'bundle' (env/dev-repo/other means the fixed bundle's tools were not used; source-contract violation)", file=sys.stderr); sys.exit(0)
tdir = data.get("tools_dir")
if expected_tools_dir and tdir != expected_tools_dir:
    print("error"); print(f"data.tools_dir={tdir!r} != expected {expected_tools_dir!r}", file=sys.stderr); sys.exit(0)
if not tdir:
    print("error"); print("data.tools_dir is empty", file=sys.stderr); sys.exit(0)
extra = keys - REQUIRED_KEYS
if extra:
    print("partial"); print(f"data has unexpected extra keys: {sorted(extra)}", file=sys.stderr); sys.exit(0)
print("normal")
PYEOF
}

# classify_audit_snapshot <audit-jsonl-file> <expected-workspace-abs-path> [<expected-tools-dir>]
#
# 對整份檔案做**嚴格單遍解析**（decision.md 必修：「讀取錯誤不得被後面一行
# 吸收；已完成的壞行不能跳過」）。除了檔案最後一行（若檔案未以換行結尾，
# 代表可能還在寫入中）以外，任何一行解析／schema 失敗都立即回報 error，不再
# 繼續掃描找「後面有沒有一行是好的」。同一組 F4 來源契約檢查
# （tools_source/tools_dir）內嵌在這裡的 classify_line()，因為 driver.sh
# 實際呼叫的是這個函式，不是上面的 classify_jsonl_startup_line（後者保留給
# 單元測試/未來直接呼叫用，兩者共用同一份契約，不得漂移）。
#
# 回傳（stdout 一行）：
#   normal                  — 找到恰好一組（去重後）相符的 startup 紀錄。
#   pending_no_match         — 目前檔案內容全部合法，但還沒有相符的 startup 紀錄。
#   pending_incomplete_tail  — 最後一行尚未以換行結尾且解析失敗，判定為可能還
#                              在寫入中。
#   error                    — 出現已完整但壞掉的行，或找到多組互不相同的相符
#                              紀錄（ambiguous authoritative record）。
classify_audit_snapshot() {
  local file="${1:-}" expected_workspace="${2:-}" expected_tools_dir="${3:-}"
  if [ ! -f "$file" ]; then
    echo "error"; echo "audit file missing at classify time" >&2; return 0
  fi
  python3 - "$file" "$expected_workspace" "$expected_tools_dir" <<'PYEOF'
import json, sys
path, expected, expected_tools_dir = sys.argv[1], sys.argv[2], sys.argv[3]
REQUIRED_KEYS = {"workspace", "workspace_source", "startup_error", "node_path", "tools_dir", "tools_source", "node"}

with open(path, "r") as f:
    raw = f.read()

if raw == "":
    print("pending_no_match"); print("audit file is empty so far", file=sys.stderr); sys.exit(0)

ends_with_newline = raw.endswith("\n")
lines = raw.split("\n")
if ends_with_newline:
    lines = lines[:-1]

def classify_line(line):
    try:
        obj = json.loads(line)
    except Exception as e:
        return ("parse_error", str(e))
    if not isinstance(obj, dict) or "kind" not in obj or "data" not in obj:
        return ("schema_error", "missing kind/data")
    if obj.get("kind") != "startup":
        return ("other_kind", obj.get("kind"))
    data = obj.get("data")
    if not isinstance(data, dict):
        return ("schema_error", "data not object")
    keys = set(data.keys())
    missing = REQUIRED_KEYS - keys
    if missing:
        return ("schema_error", f"missing keys {sorted(missing)}")
    if data.get("workspace") != expected:
        return ("schema_error", f"workspace mismatch {data.get('workspace')!r}")
    if data.get("workspace_source") != "env":
        return ("schema_error", f"workspace_source={data.get('workspace_source')!r}")
    serr = data.get("startup_error")
    if serr is None or serr != "":
        return ("schema_error", f"startup_error={serr!r}")
    tsrc = data.get("tools_source")
    if tsrc != "bundle":
        return ("schema_error", f"tools_source={tsrc!r} (source-contract violation: not the fixed bundle)")
    tdir = data.get("tools_dir")
    if not tdir:
        return ("schema_error", "tools_dir is empty")
    if expected_tools_dir and tdir != expected_tools_dir:
        return ("schema_error", f"tools_dir={tdir!r} != expected {expected_tools_dir!r}")
    return ("match", line)

n = len(lines)
matches = []
for i, line in enumerate(lines):
    is_last_possibly_incomplete = (not ends_with_newline) and (i == n - 1)
    kind, detail = classify_line(line)
    if kind in ("parse_error", "schema_error"):
        if is_last_possibly_incomplete:
            print("pending_incomplete_tail")
            print(f"last line not yet valid ({kind}: {detail}); file has no trailing newline, treating as possibly still being written", file=sys.stderr)
            sys.exit(0)
        print("error")
        print(f"line {i} is malformed and is NOT the (possibly-incomplete) last line ({kind}: {detail}); a completed corrupt line cannot be skipped", file=sys.stderr)
        sys.exit(0)
    elif kind == "match":
        matches.append(line)
    # other_kind: unrelated record, keep scanning

distinct = sorted(set(matches))
if len(distinct) > 1:
    print("error")
    print(f"{len(distinct)} distinct startup records match workspace={expected!r} (ambiguous authoritative record)", file=sys.stderr)
    sys.exit(0)
if distinct:
    print("normal")
    sys.exit(0)
print("pending_no_match")
print("all lines parsed and valid so far, but no startup record for this workspace yet", file=sys.stderr)
PYEOF
}

# classify_ax_command_result <rc> <timed_out> <stdout> <stderr>
classify_ax_command_result() {
  local rc="$1" out="$2" err="$3"
  local combined="$out"$'\n'"$err"
  if echo "$combined" | grep -qi "not allowed assistive access\|-1743\|osascript is not allowed to send keystrokes"; then
    echo "not_available"; return 0
  fi
  if [ "$rc" != "0" ]; then
    echo "error"; echo "non-zero rc=$rc (stdout non-empty does not make this success)" >&2; return 0
  fi
  echo "normal"
}

# classify_ax_auth_probe_output <exit_code> <stdout> <stderr>
classify_ax_auth_probe_output() {
  local rc="$1" out="$2" err="$3"
  local cls; cls=$(classify_ax_command_result "$rc" "$out" "$err")
  if [ "$cls" != "normal" ]; then
    echo "$cls"; return 0
  fi
  if [ -z "$out" ]; then
    echo "error"; echo "rc=0 but empty stdout" >&2; return 0
  fi
  echo "normal"
}

# classify_window_id_output <exit_code> <stdout> <stderr>
classify_window_id_output() {
  local rc="$1" out="$2" err="$3"
  if echo "$out$err" | grep -qi "not allowed assistive access\|-1743"; then
    echo "not_available"; return 0
  fi
  if echo "$out" | grep -q "^MULTI_WINDOW:"; then
    echo "error"; echo "multiple windows for this pid; refusing to guess window 1: $out" >&2; return 0
  fi
  if echo "$out" | grep -q "^NO_WINDOW:"; then
    echo "error"; echo "zero windows for this pid: $out" >&2; return 0
  fi
  if [ "$rc" -eq 0 ] && [[ "$out" =~ ^[0-9]+$ ]]; then
    echo "normal"; return 0
  fi
  echo "error"; echo "rc=$rc out=[$out] not a single bare integer" >&2; return 0
}

# classify_meta_text <meta-text> <expected-workspace-abs-path>
#
# F4：現在額外驗證 "tools: <toolsSource>" 這一段必須是 "bundle"，不再讀出來
# 卻完全不比對。
classify_meta_text() {
  local text="${1:-}" expected_workspace="${2:-}"
  if [ -z "$text" ]; then
    echo "error"; echo "empty AX text (element not found or read failed)" >&2; return 0
  fi
  if echo "$text" | grep -qE '^ws: *@ *\| *tools: *\| *node *$'; then
    echo "partial"; echo "meta text still at initial empty values" >&2; return 0
  fi
  python3 - "$text" "$expected_workspace" <<'PYEOF'
import re, sys
text, expected = sys.argv[1], sys.argv[2]
m = re.match(r'^ws: (\S+) @ (.*?) \| tools: (\S*) \| node (\S*)\s*$', text)
if not m:
    print("error"); print(f"text does not match App.vue:378 template: {text!r}", file=sys.stderr); sys.exit(0)
source, workspace, tools_source, node = m.groups()
if workspace != expected:
    print("error"); print(f"meta workspace={workspace!r} != expected {expected!r}", file=sys.stderr); sys.exit(0)
if source != "env":
    print("error"); print(f"meta workspaceSource={source!r} != 'env'", file=sys.stderr); sys.exit(0)
if tools_source != "bundle":
    print("error"); print(f"meta toolsSource={tools_source!r} != 'bundle' (source-contract violation)", file=sys.stderr); sys.exit(0)
if not node:
    print("error"); print("meta node field is empty", file=sys.stderr); sys.exit(0)
print("normal")
PYEOF
}

# classify_cli_info_json <title-text> <expected-workspace-abs-path> [<expected-tools-dir>]
#
# F4：新增 toolsSource=="bundle"、toolsDir==expected（若提供）、
# claudeVersion 第一個 field=="2.1.223"、codexVersion 最後一個
# field=="0.146.1" 四項來源契約檢查；空字串版本一律 error（固定 bundle 理論
# 上 exec 一定成功，空字串代表非預期失敗，不是 not_available 的訊號）。
classify_cli_info_json() {
  local text="${1:-}" expected_workspace="${2:-}" expected_tools_dir="${3:-}"
  if [ -z "$text" ]; then
    echo "not_available"; echo "AXHelp empty/unreadable (known gap, see window.sh ax_meta_title_json doc)" >&2; return 0
  fi
  if [ -z "$expected_workspace" ]; then
    echo "error"; echo "classify_cli_info_json: missing expected_workspace argument" >&2; return 0
  fi
  python3 - "$text" "$expected_workspace" "$expected_tools_dir" "$CLAUDE_VERSION_PIN" "$CODEX_VERSION_PIN" <<'PYEOF'
import json, sys
text, expected, expected_tools_dir, claude_pin, codex_pin = sys.argv[1:6]
REQUIRED = {"toolsDir", "toolsSource", "claudeVersion", "codexVersion", "node",
            "workspace", "workspaceSource", "startupError", "ready"}
try:
    obj = json.loads(text)
except Exception as e:
    print("error"); print(f"AXHelp text is not valid JSON: {e}", file=sys.stderr); sys.exit(0)
if not isinstance(obj, dict):
    print("error"); print("CLIInfo JSON is not an object", file=sys.stderr); sys.exit(0)
keys = set(obj.keys())
missing = REQUIRED - keys
if missing:
    print("error"); print(f"CLIInfo JSON missing required keys (app.go:3496-3505 schema): {sorted(missing)}", file=sys.stderr); sys.exit(0)
for k in REQUIRED - {"ready"}:
    if not isinstance(obj.get(k), str):
        print("error"); print(f"CLIInfo.{k} is not a string: {obj.get(k)!r}", file=sys.stderr); sys.exit(0)
if obj.get("ready") not in ("true", "false"):
    print("error"); print(f"CLIInfo.ready is not a FormatBool string: {obj.get('ready')!r}", file=sys.stderr); sys.exit(0)
if obj.get("workspace") != expected:
    print("error"); print(f"CLIInfo.workspace={obj.get('workspace')!r} != expected {expected!r}", file=sys.stderr); sys.exit(0)
if obj.get("workspaceSource") != "env":
    print("error"); print(f"CLIInfo.workspaceSource={obj.get('workspaceSource')!r} != 'env'", file=sys.stderr); sys.exit(0)
if obj.get("startupError") != "":
    print("error"); print(f"CLIInfo.startupError={obj.get('startupError')!r} is non-empty", file=sys.stderr); sys.exit(0)
if obj.get("ready") != "true":
    print("error"); print(f"CLIInfo.ready={obj.get('ready')!r} != 'true'", file=sys.stderr); sys.exit(0)
# F4: 來源契約——resolveToolsDir()，合法值只有 "bundle"。
tsrc = obj.get("toolsSource")
if tsrc != "bundle":
    print("error"); print(f"CLIInfo.toolsSource={tsrc!r} != 'bundle' (env/dev-repo/other = fixed bundle's tools were not used)", file=sys.stderr); sys.exit(0)
tdir = obj.get("toolsDir")
if expected_tools_dir and tdir != expected_tools_dir:
    print("error"); print(f"CLIInfo.toolsDir={tdir!r} != expected {expected_tools_dir!r}", file=sys.stderr); sys.exit(0)
if not tdir:
    print("error"); print("CLIInfo.toolsDir is empty", file=sys.stderr); sys.exit(0)
# F4: 版本 pin——claudePinVersion/codexPinVersion；cliVersionFrom() 空字串
# 代表 exec 失敗（不是 not_available）。
cv = obj.get("claudeVersion", "")
cv_first = cv.split()[0] if cv.split() else ""
if not cv or cv_first != claude_pin:
    print("error"); print(f"CLIInfo.claudeVersion={cv!r} first-field != pin {claude_pin!r} (empty means the exec itself failed unexpectedly on a fixed bundle)", file=sys.stderr); sys.exit(0)
xv = obj.get("codexVersion", "")
xv_last = xv.split()[-1] if xv.split() else ""
if not xv or xv_last != codex_pin:
    print("error"); print(f"CLIInfo.codexVersion={xv!r} last-field != pin {codex_pin!r} (empty means the exec itself failed unexpectedly on a fixed bundle)", file=sys.stderr); sys.exit(0)
print("normal")
PYEOF
}

# classify_screencapture_output <exit_code> <png_path>
#
# classify_meta_title_consistency <meta-text> <title-json-text>
#
# 010（review595 必修 5）：meta（AppleScript "ws: ..." 文字）與 title
# （AXHelp JSON）是兩次獨立觀測，不是同一次原子讀取——這是已知限制，
# 保留不變（P0 不要求擴大成一個新的原子讀取 API，見 PLAN-delta.md）。但
# 兩次都有的欄位（workspace／toolsSource）如果彼此矛盾，代表這兩次觀測
# 實際看到的是不同時刻的不同狀態，不能被靜默接受、混成一份不存在的原子
# 快照——一律拒絕（error）。只在呼叫端已經確認 meta／title 兩者「各自的
# 分類」都是 normal 時才有意義呼叫這個函式（title 若是 not_available 就
# 沒有 JSON 可比，呼叫端不應該呼叫）。
classify_meta_title_consistency() {
  local meta_text="${1:-}" title_text="${2:-}"
  python3 - "$meta_text" "$title_text" <<'PYEOF'
import json, re, sys
meta_text, title_text = sys.argv[1], sys.argv[2]
m = re.match(r'^ws: (\S+) @ (.*?) \| tools: (\S*) \| node (\S*)\s*$', meta_text)
if not m:
    print("error")
    print("meta text no longer matches expected template at consistency-check time", file=sys.stderr)
    sys.exit(0)
_source, meta_workspace, meta_tools_source, _meta_node = m.groups()
try:
    obj = json.loads(title_text)
except Exception as e:
    print("error")
    print(f"title JSON no longer parses at consistency-check time: {e}", file=sys.stderr)
    sys.exit(0)
title_workspace = obj.get("workspace")
title_tools_source = obj.get("toolsSource")
mismatches = []
if meta_workspace != title_workspace:
    mismatches.append(f"workspace: meta={meta_workspace!r} != title={title_workspace!r}")
if meta_tools_source != title_tools_source:
    mismatches.append(f"toolsSource: meta={meta_tools_source!r} != title={title_tools_source!r}")
if mismatches:
    print("error")
    print("meta/title two independent observations contradict each other (not one atomic snapshot): "
          + "; ".join(mismatches), file=sys.stderr)
    sys.exit(0)
print("normal")
PYEOF
}

# review564 反例 quartz_unavailable_still_captures：舊版只看 rc==0 且檔案
# 非空，9 bytes 的 "not a png" 文字檔被判 normal。004 額外驗證：
#   1. PNG magic bytes（\x89PNG\r\n\x1a\n）。
#   2. IHDR chunk 可解析（長度 13、chunk type == "IHDR"，緊接在 signature
#      之後——PNG 規格要求 IHDR 必須是第一個 chunk）。
#   3. IHDR 解出的寬高都 > 0 且 <= 16384（decision.md 給定的合理範圍例子）。
# 本函式不依賴任何 GUI／image 函式庫做完整解碼，只做離線可驗證的
# signature+IHDR 結構檢查，不宣稱已驗證真正的影像內容/像素正確性。
classify_screencapture_output() {
  local rc="$1" png="$2"
  if [ "$rc" -ne 0 ]; then
    echo "error"; echo "screencapture exit=$rc" >&2; return 0
  fi
  if [ ! -s "$png" ]; then
    echo "error"; echo "screencapture produced empty/missing file" >&2; return 0
  fi
  python3 - "$png" <<'PYEOF'
import struct, sys
path = sys.argv[1]
with open(path, "rb") as f:
    data = f.read(33)  # 8-byte signature + 4 length + 4 type + 8 width/height + 4 more (up to 29) with margin
SIG = b"\x89PNG\r\n\x1a\n"
if len(data) < 8 or data[:8] != SIG:
    print("error"); print(f"not a PNG (bad or missing signature, got {data[:8]!r})", file=sys.stderr); sys.exit(0)
if len(data) < 8 + 8 + 8:
    print("error"); print(f"file too short to contain a valid IHDR chunk (len={len(data)})", file=sys.stderr); sys.exit(0)
chunk_len = struct.unpack(">I", data[8:12])[0]
chunk_type = data[12:16]
if chunk_type != b"IHDR":
    print("error"); print(f"first chunk after signature is not IHDR (got {chunk_type!r}); PNG requires IHDR to be first", file=sys.stderr); sys.exit(0)
if chunk_len != 13:
    print("error"); print(f"IHDR chunk length != 13 (got {chunk_len}); malformed PNG", file=sys.stderr); sys.exit(0)
width = struct.unpack(">I", data[16:20])[0]
height = struct.unpack(">I", data[20:24])[0]
MAX_DIM = 16384
if width <= 0 or height <= 0:
    print("error"); print(f"IHDR width/height not positive (width={width} height={height})", file=sys.stderr); sys.exit(0)
if width > MAX_DIM or height > MAX_DIM:
    print("error"); print(f"IHDR width/height exceeds sanity bound {MAX_DIM} (width={width} height={height})", file=sys.stderr); sys.exit(0)
print("normal")
print(f"width={width} height={height}", file=sys.stderr)
PYEOF
}

# classify_teardown_residue <term_converged> <kill_required> <group_residual_count>
#
# group_residual_count 現在有三種合法輸入：非負整數、或字面值 "unknown"
# （review564 反例 pgrep_error_as_zero：pgrep 觀測本身失敗——rc=2 或工具缺失
# 或輸出無法解析——一律不能被當成 0，見 driver.sh/_count_pgid_residual 與
# lib/timeout.sh 的對應修正）。"unknown" 一律 error：查不到不等於查到 0，
# 不能假裝已確認乾淨。
classify_teardown_residue() {
  local term_converged="$1" kill_required="$2" group_residual_count="$3"
  if [ "$group_residual_count" = "unknown" ]; then
    echo "error"; echo "residual membership could not be determined (pgrep observation failed); cannot assert clean" >&2; return 0
  fi
  if [ "$group_residual_count" -gt 0 ] 2>/dev/null; then
    echo "error"; echo "residual=$group_residual_count members still in held process group after TERM/KILL" >&2; return 0
  fi
  if [ "$term_converged" = "1" ] && [ "$kill_required" = "0" ]; then
    echo "normal"; return 0
  fi
  echo "partial"; echo "term_converged=$term_converged kill_required=$kill_required (KILL path or early-exit, single observation)" >&2; return 0
}
