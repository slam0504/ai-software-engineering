#!/bin/bash
# B3a-1 E2E offline 驗證模式專用：只有 E2E_OFFLINE_SANDBOX=1 時，
# playwright.config.ts 才會把這支腳本設成 Chrome 的 executablePath。
# 用 exec 把 sandbox-exec 疊上真正的系統 Chrome——exec 會直接取代目前這個
# 腳本行程的行程映像（不是 fork 一個子行程等待），argv／stdin／stdout／
# stderr／訊號都原樣繼承，Playwright 追蹤到的 pid 最終就是真正在跑的
# （sandboxed）Chrome 本身，不是這支 wrapper 腳本或任何中介行程。
# 不用字串拼接組指令（會在路徑含空白時出錯，Chrome 的路徑本身就含空白），
# 全部用陣列參數傳給 exec。
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROFILE="${SCRIPT_DIR}/loopback-only.sb"
CHROME_BIN="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"

if [ ! -f "$PROFILE" ]; then
  echo "chromeWrapper.sh: 找不到 sandbox profile：$PROFILE" >&2
  exit 1
fi
if [ ! -x "$CHROME_BIN" ]; then
  echo "chromeWrapper.sh: 找不到或不可執行的系統 Chrome：$CHROME_BIN" >&2
  exit 1
fi

exec /usr/bin/sandbox-exec -f "$PROFILE" "$CHROME_BIN" "$@"
