// tripwire 型 fake provider（§2.6）：兩支假 CLI，絕不呼叫真的 claude／codex。
//
// 目錄結構對齊 app.go claudeCLIPathIn／codexCLIPathIn 的期待：
//   <toolsDir>/claude-cli/node_modules/.bin/claude
//   <toolsDir>/codex-cli/node_modules/.bin/codex
//
// 版本字串不烤進腳本本體，改烤成旁邊一個 `<name>.version` 檔，每次呼叫時讀取。
// 這樣 N5（先過預檢、之後才回不符版本）只需要覆寫這個檔案，不必重寫腳本或
// 引入呼叫次數計數這種更複雜的機制。
import fs from 'node:fs';
import path from 'node:path';

export interface FakeCliSet {
  toolsDir: string;
  claudeVersion: string;
  codexVersion: string;
  invocationsLog: string;
}

function scriptBody(name: string, dirDepthUp: string): string {
  // 整行先組進一個變數，最後只用「一次」 printf 寫入 log——claude／codex 兩支
  // 假 CLI 可能被併發呼叫（CLIInfo 對兩者各呼叫一次 --version），O_APPEND
  // 只保證單一 write() 呼叫是原子的；分多次 printf 寫同一行會被另一個行程
  // 併發寫入的內容插在中間，兩行糊成一行、後續解析必然失敗（施工時實測到
  // 過這個問題，故改成單次寫入）。
  return `#!/usr/bin/env bash
# FAKE ${name} CLI — tripwire only, never calls a real provider binary (B3a-1 §2.6).
set -u
SELF_DIR="$(cd "$(dirname "$0")" && pwd)"
TOOLS_DIR="$(cd "$SELF_DIR/${dirDepthUp}" && pwd)"
LOG="$TOOLS_DIR/invocations.log"
VERSION_FILE="$TOOLS_DIR/${name}.version"
TS=$(date -u +%Y-%m-%dT%H:%M:%SZ)
ARGV=""
for a in "$@"; do ARGV="$ARGV$(printf '%q' "$a") "; done
LINE=$(printf '[%s] name=${name} argv=(%s) cwd=%s ppid=%s' "$TS" "$ARGV" "$(pwd)" "$PPID")
printf '%s\\n' "$LINE" >> "$LOG"
if [ "$#" -eq 1 ] && [ "$1" = "--version" ]; then
  cat "$VERSION_FILE"
  exit 0
fi
echo "FAKE ${name}: refusing non-version call, args=$*" >&2
exit 17
`;
}

function writeExecutable(file: string, content: string): void {
  fs.mkdirSync(path.dirname(file), { recursive: true });
  fs.writeFileSync(file, content);
  fs.chmodSync(file, 0o755);
}

export function createFakeCliSet(toolsDir: string, runId: string): FakeCliSet {
  const claudeVersion = `fake-claude 0.0.0-e2e+${runId}`;
  const codexVersion = `fake-codex 0.0.0-e2e+${runId}`;
  const invocationsLog = path.join(toolsDir, 'invocations.log');

  fs.mkdirSync(toolsDir, { recursive: true });
  fs.writeFileSync(invocationsLog, '');
  fs.writeFileSync(path.join(toolsDir, 'claude.version'), claudeVersion + '\n');
  fs.writeFileSync(path.join(toolsDir, 'codex.version'), codexVersion + '\n');

  // 腳本位於 <toolsDir>/<pkg>/node_modules/.bin/<bin>，回 toolsDir 要往上三層。
  writeExecutable(
    path.join(toolsDir, 'claude-cli', 'node_modules', '.bin', 'claude'),
    scriptBody('claude', '../../..'),
  );
  writeExecutable(
    path.join(toolsDir, 'codex-cli', 'node_modules', '.bin', 'codex'),
    scriptBody('codex', '../../..'),
  );

  return { toolsDir, claudeVersion, codexVersion, invocationsLog };
}

// N5 注入點：預檢通過之後才讓假版本字串不符（§3 表 N5）。
export function injectBadVersionAfterPreflight(toolsDir: string): void {
  fs.writeFileSync(path.join(toolsDir, 'claude.version'), 'fake-claude 9.9.9-WRONG\n');
  fs.writeFileSync(path.join(toolsDir, 'codex.version'), 'fake-codex 9.9.9-WRONG\n');
}

/**
 * B3a-2b-2 F2：Claude 案的結構化 argv 紀錄檔名。
 *
 * 它與 `invocations.log` **必須在同一階段保存與重設**——預檢只處理其中一份的話，
 * 正式 tripwire 的「兩份筆數一致」在正常路徑也必然差一筆（reviewer #369 實測
 * wrapperLines=0、structuredLines=1）。
 *
 * 檔案存在與否是 **provider 的判別**、不是 silent-optional：
 * `createScenarioClaudeCli` 會在建立 wrapper 時一併建出空檔，Codex 案則從不建立。
 */
export const CLAUDE_ARGV_LOG_NAME = 'claude-argv.jsonl';

export function snapshotInvocationsLog(toolsDir: string, destFile: string): void {
  fs.copyFileSync(path.join(toolsDir, 'invocations.log'), destFile);
  const argvSrc = path.join(toolsDir, CLAUDE_ARGV_LOG_NAME);
  if (fs.existsSync(argvSrc)) {
    fs.copyFileSync(argvSrc, path.join(path.dirname(destFile), `preflight-${CLAUDE_ARGV_LOG_NAME}`));
  }
}

export function truncateInvocationsLog(toolsDir: string): void {
  fs.writeFileSync(path.join(toolsDir, 'invocations.log'), '');
  const argv = path.join(toolsDir, CLAUDE_ARGV_LOG_NAME);
  if (fs.existsSync(argv)) fs.writeFileSync(argv, '');
}
