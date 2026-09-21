// B3a-2b-2 F1b：scenario 專用的**假 claude CLI wrapper**。
//
// 為什麼要 wrapper 而不是直接讓 App 執行 node：真 App 組給子行程的 env 只有單一
// 一個 `PATH=...`（app.go childEnvFor；Go 的 exec.Cmd.Env 一旦非 nil 就整份取代，
// 不會合併父行程環境）。因此「在啟動 App 的行程先 export 變數再指望繼承」這條路
// 在 App 那一層就會被丟掉——這不是臆測，是讀 childEnvFor 原始碼的直接結論。
// 解法與 Task C 的 `scenarioCli.ts` 相同：把 run 專屬的 config／證據位置**烤進
// wrapper 腳本本體**，腳本在 exec node 之前自己 export 給即將啟動的子行程。
//
// 本檔只產生 claude 這一支，**不動 fakeCli.ts、scenarioCli.ts 或任何既有檔案**。
import fs from 'node:fs';
import path from 'node:path';

export interface ClaudeScenarioCliPaths {
  /** 本次 run 的 mcp config（真 App 會寫在 host.mcpPath；F1b 由驅動端寫）。 */
  mcpConfigPath: string;
  /** 獨立 fixture 期望檔——**期望值的唯一來源，不由 config 推導**。 */
  expectationPath: string;
  /** 本次 run 的證據目錄。 */
  evidenceDir: string;
}

export interface ClaudeScenarioCli extends ClaudeScenarioCliPaths {
  /** 本次 run 專屬的輪次登記目錄（排他 claim 的所在，見 fakeClaudeCli claimConversationRound）。 */
  roundDir: string;
  /** wrapper 的實際路徑，與 app.go claudeCLIPathIn(toolsDir) 的版面相同。 */
  claudeBin: string;
  claudeVersion: string;
  /** 與 base fake CLI 共用的那一份（tools/invocations.log）。 */
  invocationsLog: string;
  /** 結構化 argv 紀錄（JSON Lines），tripwire 的判定來源。 */
  argvLog: string;
}

function shellSingleQuote(s: string): string {
  // 只用來包裝我們自己產生的絕對路徑，但仍照標準 shell single-quote escaping
  // 處理，不假設輸入乾淨。
  return `'${s.replace(/'/g, `'\\''`)}'`;
}

/**
 * 在 toolsDir 內產生假 claude CLI。版面刻意對齊 app.go claudeCLIPathIn：
 * `<toolsDir>/claude-cli/node_modules/.bin/claude`。
 */
export function createScenarioClaudeCli(
  toolsDir: string,
  runId: string,
  fakeClaudeCliPath: string,
  paths: ClaudeScenarioCliPaths,
): ClaudeScenarioCli {
  const binDir = path.join(toolsDir, 'claude-cli', 'node_modules', '.bin');
  fs.mkdirSync(binDir, { recursive: true });
  const claudeBin = path.join(binDir, 'claude');
  const claudeVersion = `fake-claude-scenario 0.0.0-e2e+${runId}`;
  // **共用 invocations.log**：base/preflight/teardown 都讀這一份
  // （fakeCli.ts:56、preflight snapshot/truncate、global-teardown 複製）。
  // 先前寫成 claude-invocations.log 會讓 Claude 的真實呼叫完全不進最後的
  // tripwire（reviewer #367）。
  const invocationsLog = path.join(toolsDir, 'invocations.log');
  // 結構化 argv 紀錄：由 node runtime 直接寫 JSON（argv 是原生陣列），
  // **不對 wrapper 的 printf %q 輸出拆字**——那種拆法對含空白的 --settings
  // 值不可靠，也看不出重複旗標／多餘 positional。
  const argvLog = path.join(toolsDir, 'claude-argv.jsonl');
  // 輪次登記目錄：**run 專屬**（跟著 toolsDir 一起新建），所以不會沿用上一次
  // 執行的計數。由這裡先建好空目錄，假 CLI 只做非 recursive 的 mkdir——少了
  // 這段接線就會 ENOENT 失敗，而不是自己補建、悄悄失去排他性。
  const roundDir = path.join(toolsDir, 'claude-rounds');
  fs.mkdirSync(roundDir, { recursive: true });

  const script = `#!/usr/bin/env bash
# FAKE claude CLI（B3a-2b-2 F1b）— 絕不呼叫真 claude。
# 對話協定交給獨立的 fakeClaudeCli.ts 處理；本腳本只負責烤入 run 專屬位置。
set -u
LOG=${shellSingleQuote(invocationsLog)}
TS=$(date -u +%Y-%m-%dT%H:%M:%SZ)
ARGV=""
for a in "$@"; do ARGV="$ARGV$(printf '%q' "$a") "; done
printf '[%s] name=claude-scenario argv=(%s) cwd=%s ppid=%s\\n' "$TS" "$ARGV" "$(pwd)" "$PPID" >> "$LOG"
export FAKE_CLAUDE_VERSION=${shellSingleQuote(claudeVersion)}
export FAKE_CLAUDE_MCP_CONFIG=${shellSingleQuote(paths.mcpConfigPath)}
export FAKE_CLAUDE_EXPECTATION=${shellSingleQuote(paths.expectationPath)}
export FAKE_CLAUDE_EVIDENCE_DIR=${shellSingleQuote(paths.evidenceDir)}
export FAKE_CLAUDE_RUN_ID=${shellSingleQuote(runId)}
export FAKE_CLAUDE_ARGV_LOG=${shellSingleQuote(argvLog)}
export FAKE_CLAUDE_ROUND_DIR=${shellSingleQuote(roundDir)}
exec node ${shellSingleQuote(fakeClaudeCliPath)} "$@"
`;
  fs.writeFileSync(claudeBin, script);
  fs.chmodSync(claudeBin, 0o755);
  // 先建出空檔：讓它的存在成為「這是 Claude 案」的明確判別，
  // 預檢的 snapshot/truncate 才能與 invocations.log 同階段處理（fakeCli.ts）。
  fs.writeFileSync(argvLog, '');

  return { ...paths, claudeBin, claudeVersion, invocationsLog, argvLog, roundDir };
}

/** 驅動端寫 mcp config 時使用的同一段字串組法（對齊 app.go startClaude 的格式）。 */
export function renderMcpConfig(commandPath: string, socketPath: string): string {
  return `{"mcpServers":{"workbench":{"type":"stdio","command":${JSON.stringify(commandPath)},"args":["mcp-approval","--socket",${JSON.stringify(socketPath)}]}}}`;
}
