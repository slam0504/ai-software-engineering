#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
MIN_CLAUDE="2.1.219"
fail() { echo "FAIL: $1"; exit 1; }
check() { # $1=dir $2=pkg $3=bin
  local pinned actual bin="$ROOT/tools/$1/node_modules/.bin/$3"
  pinned="$(node -p "require('$ROOT/tools/$1/package.json').dependencies['$2']")"
  actual="$("$bin" --version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -1)"
  [ "$actual" = "$pinned" ] || fail "$3 binary $actual != pinned $pinned"
  echo "$3 $actual sha256=$(shasum -a 256 "$bin" | awk '{print $1}')"
}
check claude-cli @anthropic-ai/claude-code claude
printf '%s\n%s\n' "$MIN_CLAUDE" "$(check claude-cli @anthropic-ai/claude-code claude | awk '{print $2}')" | sort -V -C \
  || fail "claude < min $MIN_CLAUDE"
check codex-cli @openai/codex codex
echo "OK"
