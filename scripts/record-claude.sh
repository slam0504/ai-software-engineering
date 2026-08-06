#!/usr/bin/env bash
# 用法: scripts/record-claude.sh <case> "<prompt>" [extra flags...]
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
"$ROOT/scripts/check-cli.sh" >/dev/null
BIN="$ROOT/tools/claude-cli/node_modules/.bin/claude"
case="$1"; prompt="$2"; shift 2
args=(-p --input-format stream-json --output-format stream-json --verbose --include-partial-messages "$@")
out="$ROOT/.workbench/recordings"; mkdir -p "$out"
set +e
printf '{"type":"user","message":{"role":"user","content":[{"type":"text","text":"%s"}]}}\n' "$prompt" | \
  "$BIN" "${args[@]}" >"$out/$case.ndjson" 2>"$out/$case.stderr.log"
code=$?; set -e
jq -n --arg v "$("$BIN" --version)" --arg cwd "$PWD" --arg at "$(date -u +%FT%TZ)" --argjson code "$code" \
  --args '{provider:"claude",cli_version:$v,cwd:$cwd,recorded_at:$at,exit_code:$code,argv:$ARGS.positional}' \
  -- "$BIN" "${args[@]}" > "$out/$case.meta.json"
echo "recorded: $out/$case.ndjson (exit $code)"
