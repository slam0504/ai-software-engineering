#!/usr/bin/env bash
# B2c 暫時性診斷 fixture——不得合併進 main，B2c 結案後刪除。
# 孫程序語意與 testdata/fake-claude.sh:16 相同（bash -c 'trap "" TERM; sleep 30' &），
# 差別只在：(1) 孫程序命令列內含本輪 DIAG_TOKEN，(2) 父 shell 以 $! 把孫程序 PID 寫到 DIAG_PIDFILE。
# 需要的環境變數：DIAG_TOKEN、DIAG_PIDFILE（由 orphan_diag_test.go 每輪產生）。
read -r _prompt || true
bash -c "trap '' TERM; : DIAG_TOKEN=${DIAG_TOKEN:-none}; sleep 30" &
echo $! > "${DIAG_PIDFILE:?DIAG_PIDFILE required}"
echo '{"type":"system","subtype":"init","session_id":"diag-1","model":"m","mcp_servers":[]}'
echo '{"type":"stream_event","session_id":"diag-1","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}}'
echo '{"type":"result","subtype":"success","session_id":"diag-1","result":"hi","total_cost_usd":0,"is_error":false}'
exit 0
