#!/usr/bin/env bash
# 只服務 runner 生命週期測試（協定邏輯用 Go in-test fake）。
# FAKE_DIE=handshake 後退出 7  FAKE_STDERR=吐 stderr  FAKE_EXIT=stdin 關閉後退出碼
# FAKE_ORPHAN=衍生忽略 SIGTERM 的孫程序（繼承 pipe）
[ -n "${FAKE_STDERR:-}" ] && echo "codex-stderr" >&2
[ -n "${FAKE_ORPHAN:-}" ] && bash -c 'trap "" TERM; sleep 30' &
read -r line || exit 1
id=$(printf '%s' "$line" | grep -oE '"id":[0-9]+' | head -1 | cut -d: -f2)
printf '{"id":%s,"result":{}}\n' "${id:-1}"
read -r _initialized || true
[ -n "${FAKE_DIE:-}" ] && exit 7
while read -r _; do :; done
exit "${FAKE_EXIT:-0}"
