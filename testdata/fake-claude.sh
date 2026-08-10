#!/usr/bin/env bash
# FAKE_EXIT=退出碼 FAKE_HANG=收尾前掛住 FAKE_STDERR=吐 stderr FAKE_DIE=init 後立刻死（無 result）
# FAKE_ORPHAN=衍生忽略 SIGTERM 的孫程序（繼承 pipe） FAKE_BADLINE=吐超過 buffer 的行（scanner error）
if [ -n "${FAKE_MULTI:-}" ]; then
  echo '{"type":"system","subtype":"init","session_id":"fake-1","model":"m","mcp_servers":[]}'
  n=0
  while read -r _line; do
    n=$((n+1))
    printf '{"type":"stream_event","session_id":"fake-1","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"t%d"}}}\n' "$n"
    printf '{"type":"result","subtype":"success","session_id":"fake-1","result":"t%d","total_cost_usd":0,"is_error":false}\n' "$n"
  done
  exit 0
fi
read -r _prompt || true
[ -n "${FAKE_STDERR:-}" ] && echo "boom-stderr" >&2
[ -n "${FAKE_ORPHAN:-}" ] && bash -c 'trap "" TERM; sleep 30' &
echo '{"type":"system","subtype":"init","session_id":"fake-1","model":"m","mcp_servers":[]}'
[ -n "${FAKE_DIE:-}" ] && exit 7
[ -n "${FAKE_BADLINE:-}" ] && printf '{"type":"x","pad":"%s"}\n' "$(head -c 4096 </dev/zero | tr '\0' a)"
echo '{"type":"stream_event","session_id":"fake-1","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}}'
if [ -n "${FAKE_HANG:-}" ]; then sleep 30; fi
echo '{"type":"result","subtype":"success","session_id":"fake-1","result":"hi","total_cost_usd":0,"is_error":false}'
exit "${FAKE_EXIT:-0}"
