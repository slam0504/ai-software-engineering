// B3a-2b-2 Task C：scenario 專用 codex CLI——跟 `../fakeCli.ts` 的 version-only
// tripwire（凍結，不擴充；R1/R2/R3/R4/R5 之後的版本，此檔不動它）分開，獨立
// 建立，只在本檔（scenario 專屬入口）使用。
//
// 背景：真 App 透過 `a.childEnv()` 組出的子行程 env 只有單一一個 `PATH=...`
// 條目（見 app.go childEnvFor）——Go 的 exec.Cmd.Env 一旦非 nil 就整份取代，
// 不會合併父行程既有的環境變數；因此無法用「先在啟動 App 的行程設好
// SCENARIO_FAKE_CONFIG／SCENARIO_FAKE_LOG 再指望繼承下去」這條路（會在真
// App 這一層被丟掉，不是本檔臆測，是讀 childEnvFor 原始碼得出的直接結論）。
// 改成把這兩個值直接烤進 codex CLI 腳本本體（bash heredoc 常數展開），腳本
// 在 `exec node <fakeAppServer.ts> app-server` 之前自己重新 export 這兩個
// 變數給即將啟動的 node 子行程——`../scenario/fakeAppServer.ts`（B3a-2b-1
// 已交付、凍結、不修改）完全不用感知這件事，一樣只讀自己的 process.env。
import fs from 'node:fs';
import path from 'node:path';
import { createFakeCliSet, type FakeCliSet } from '../fakeCli.js';

export interface ScenarioCli extends FakeCliSet {
  scenarioConfigPath: string;
  scenarioLogPath: string;
  scenarioManifestPath: string;
}

function shellSingleQuote(s: string): string {
  // 只用來包裝我們自己產生的絕對路徑（mkdtemp／repo 內固定路徑），但仍照標準
  // shell single-quote escaping 處理，不假設輸入乾淨。
  return `'${s.replace(/'/g, `'\\''`)}'`;
}

export function createScenarioCodexCli(
  toolsDir: string,
  runId: string,
  fakeAppServerPath: string,
  scenarioConfigPath: string,
  scenarioLogPath: string,
): ScenarioCli {
  // 先用既有 fakeCli.ts 建出完整鷹架（claude tripwire 原樣保留＋codex tripwire
  // 佔位）——下面只覆寫 codex 這一支，claude 那一支與 fakeCli.ts 本身完全不動。
  const base = createFakeCliSet(toolsDir, runId);

  const codexVersion = `fake-codex-scenario 0.0.0-e2e+${runId}`;
  const versionFile = path.join(toolsDir, 'codex.version');
  fs.writeFileSync(versionFile, codexVersion + '\n');

  const codexBin = path.join(toolsDir, 'codex-cli', 'node_modules', '.bin', 'codex');
  const invocationsLog = path.join(toolsDir, 'invocations.log');

  const script = `#!/usr/bin/env bash
# FAKE codex CLI（scenario 專屬版，B3a-2b-2 Task C）— 絕不呼叫真 codex；
# app-server argv 交給獨立、凍結不改的 fakeAppServer.ts 處理協定。
set -u
LOG=${shellSingleQuote(invocationsLog)}
TS=$(date -u +%Y-%m-%dT%H:%M:%SZ)
ARGV=""
for a in "$@"; do ARGV="$ARGV$(printf '%q' "$a") "; done
LINE=$(printf '[%s] name=codex-scenario argv=(%s) cwd=%s ppid=%s' "$TS" "$ARGV" "$(pwd)" "$PPID")
printf '%s\\n' "$LINE" >> "$LOG"
if [ "$#" -eq 1 ] && [ "$1" = "--version" ]; then
  printf '%s\\n' ${shellSingleQuote(codexVersion)}
  exit 0
fi
if [ "$#" -eq 1 ] && [ "$1" = "app-server" ]; then
  export SCENARIO_FAKE_CONFIG=${shellSingleQuote(scenarioConfigPath)}
  export SCENARIO_FAKE_LOG=${shellSingleQuote(scenarioLogPath)}
  export SCENARIO_FAKE_RUN_ID=${shellSingleQuote(runId)}
  exec node ${shellSingleQuote(fakeAppServerPath)} app-server
fi
echo "FAKE codex-scenario: refusing unexpected call, args=$*" >&2
exit 17
`;
  fs.writeFileSync(codexBin, script);
  fs.chmodSync(codexBin, 0o755);

  return {
    ...base,
    codexVersion,
    scenarioConfigPath,
    scenarioLogPath,
    scenarioManifestPath: `${scenarioLogPath}.manifest.json`,
  };
}
