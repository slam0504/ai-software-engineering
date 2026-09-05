#!/usr/bin/env bash
# B2c 暫時性診斷 fixture——不得合併進 main，B2c 結案後刪除。
# 孫程序語意與 testdata/fake-claude.sh:16 相同（忽略 TERM、持有 stdout/stderr、sleep 30），差別：
#   (1) 孫程序 bash 命令列內含本輪 DIAG_TOKEN；(2) 父 shell 以 $! 寫出孫程序 bash 的 PID（DIAG_PIDFILE）；
#   (3) 孫程序 bash 以「sleep 30 & echo $! > DIAG_CHILDPIDFILE; wait」保持自身為 sleep 的父程序並寫出 sleep 的確切 PID。
# DIAG_MODE（強制控制用，預設 normal）：
#   normal  ——與 fake-claude 相同：孫程序留在 leader 的 process group，leader 立即正常退出。
#   hang    ——leader 送完三行後 sleep 60 不退出：用來強制 exitedTimeout 路徑（leader 與孫程序皆在群組內）。
#   escape  ——孫程序以 perl POSIX::setsid 脫離 leader 的 process group：用來強制 eofTimeout → targeted-pid → guard 2 路徑。
: "${DIAG_TOKEN:?DIAG_TOKEN required}"; : "${DIAG_PIDFILE:?DIAG_PIDFILE required}"; : "${DIAG_CHILDPIDFILE:?DIAG_CHILDPIDFILE required}"
MODE="${DIAG_MODE:-normal}"
read -r _prompt || true
INNER="trap '' TERM; : DIAG_TOKEN=$DIAG_TOKEN; sleep 30 & echo \$! > '$DIAG_CHILDPIDFILE'; wait"
if [ "$MODE" = "escape" ]; then
  perl -e 'use POSIX; POSIX::setsid(); exec @ARGV' -- bash -c "$INNER" &
else
  bash -c "$INNER" &
fi
echo $! > "$DIAG_PIDFILE"
if [ "$MODE" = "escape" ]; then
  # 等孫程序真的完成 setsid 並寫出 sleep PID（否則 leader 退出後的 group KILL 會在 perl 完成 setsid 前殺掉它，控制失效）。
  for _ in $(seq 1 200); do [ -s "$DIAG_CHILDPIDFILE" ] && break; sleep 0.01; done
fi
echo '{"type":"system","subtype":"init","session_id":"diag-1","model":"m","mcp_servers":[]}'
echo '{"type":"stream_event","session_id":"diag-1","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}}'
echo '{"type":"result","subtype":"success","session_id":"diag-1","result":"hi","total_cost_usd":0,"is_error":false}'
if [ "$MODE" = "hang" ]; then sleep 60; fi
exit 0
