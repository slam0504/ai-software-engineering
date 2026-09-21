// B3a-2b-2 Task C：scenario 執行的呼叫紀律判定——跟 global-teardown.ts 內
// 既有、凍結不變的 `judgeTripwire`（default／controls 專用，假設 codex
// 永遠只會被 `--version` 呼叫）分開。scenario 執行本來就會真的以
// `app-server` 呼叫 codex CLI（見 support/scenario/scenarioCli.ts），沿用
// `judgeTripwire` 的「codex 只能 --version」假設一定會誤判成違規。
//
// 這裡改成：claude 沿用原本假設（scenario 沒有真的用到 claude，只在
// preflight／CLIInfo 呼叫 --version）；codex（scenario 版 CLI，log 裡的
// name=codex-scenario）允許 `--version` 與 `app-server` 兩種 argv，其餘一律
// 違規；且必須「至少一次 --version」＋「至少一次 app-server」——後者證明
// Start 真的觸發了 StartAppServer，不是單純沒跑到那一步就被略過判定。
import fs from 'node:fs';
import type { HarnessLogger } from '../logger.js';

const TRIPWIRE_LINE_RE = /^\[(?<ts>[^\]]+)\] name=(?<name>\S+) argv=\((?<argv>.*)\) cwd=(?<cwd>\S+) ppid=(?<ppid>\d+)$/;

export function judgeScenarioCliCalls(logFile: string, log: HarnessLogger): string[] {
  const violations: string[] = [];
  const text = fs.readFileSync(logFile, 'utf8');
  const lines = text.split('\n').map(l => l.trim()).filter(Boolean);
  if (lines.length === 0) violations.push('invocations.log 為空（缺失呼叫紀錄不算通過）');

  let claudeCalls = 0;
  let codexVersionCalls = 0;
  let codexAppServerCalls = 0;
  for (const line of lines) {
    const m = TRIPWIRE_LINE_RE.exec(line);
    if (!m || !m.groups) {
      violations.push(`invocations.log 出現無法解析的行：${line}`);
      continue;
    }
    const name = m.groups.name;
    const argv = m.groups.argv.trim();
    if (name === 'claude') {
      if (argv !== '--version') violations.push(`claude 收到非 --version 的呼叫：argv=(${argv})`);
      claudeCalls++;
    } else if (name === 'codex-scenario') {
      if (argv === '--version') codexVersionCalls++;
      else if (argv === 'app-server') codexAppServerCalls++;
      else violations.push(`codex-scenario 收到未預期的呼叫：argv=(${argv})`);
    } else {
      violations.push(`未知的假 CLI 名稱：${name}`);
    }
  }
  if (claudeCalls < 1) violations.push('claude 從未被呼叫（CLIInfo 應各呼叫一次 --version）');
  if (codexVersionCalls < 1) violations.push('codex-scenario 從未以 --version 被呼叫（CLIInfo／preflight 應各呼叫一次）');
  if (codexAppServerCalls < 1) violations.push('codex-scenario 從未以 app-server 被呼叫（Start 應觸發真正的 StartAppServer spawn）');
  log.log(
    `scenario 呼叫紀律判定：claude 呼叫 ${claudeCalls} 次、codex-scenario --version ${codexVersionCalls} 次、`
    + `codex-scenario app-server ${codexAppServerCalls} 次、違規 ${violations.length} 筆`,
  );
  return violations;
}
