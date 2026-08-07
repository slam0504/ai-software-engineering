#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RES="$ROOT/build/bin/sdlc-workbench.app/Contents/Resources/tools"
mkdir -p "$RES"
cp -R "$ROOT/tools/claude-cli" "$ROOT/tools/codex-cli" "$RES/"
echo "bundled: $(du -sh "$RES" | awk '{print $1}')"
