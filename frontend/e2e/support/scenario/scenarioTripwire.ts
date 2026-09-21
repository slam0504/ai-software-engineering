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
import path from 'node:path';
import type { HarnessLogger } from '../logger.js';
import { expectedConversationArgv } from './fakeClaudeCli.js';

const TRIPWIRE_LINE_RE = /^\[(?<ts>[^\]]+)\] name=(?<name>\S+) argv=\((?<argv>.*)\) cwd=(?<cwd>\S+) ppid=(?<ppid>\d+)$/;

/**
 * B3a-2b-2 F2：Claude 對話呼叫的**嚴格判定**。
 *
 * 判定來源是 `fakeClaudeCli.ts` 寫出的**結構化 argv 紀錄**（JSON Lines，argv 是
 * 原生字串陣列），**不是** wrapper 那行 `printf %q` 引號化過的文字——對含空白的
 * `--settings` 值拆字不可靠，而且看不出重複旗標與多餘 positional（reviewer #367）。
 *
 * `expectedResume`：**這一筆呼叫核定的 resume 值**，由呼叫端依已驗證的 scenario
 * identity（登記表的 kind）與固定 fixture builder 決定，**不從待驗 argv 反填**。
 * null＝該筆必須是 fresh start；非空字串＝該筆必須恰好帶 `--resume <該值>`。
 *
 * 判定方式：與 `expectedConversationArgv()` **逐位置全等**。config 路徑本身是
 * 動態值（真 App 的 `<stateDir>/mcp-<WSID>.json`），因此拿該次 argv 自己的
 * `--mcp-config` 值代入期望再做全等——**其餘 token 的值、順序、重複、多餘
 * positional 一項都沒有放過**。該動態值本身是否合法（父目錄 canonical 與檔名
 * 契約）由假 CLI 在執行當下判定並留證，不在這裡重複推測。
 */
export function judgeClaudeConversationArgvStrict(
  argv: string[], expectedResume: string | null = null,
): string[] {
  const v: string[] = [];
  const i = argv.indexOf('--mcp-config');
  if (i < 0) return ['claude conversation argv 缺少 --mcp-config'];
  if (argv.indexOf('--mcp-config', i + 1) >= 0) return ['claude conversation argv 出現多個 --mcp-config'];
  const cfg = argv[i + 1];
  if (typeof cfg !== 'string' || cfg === '' || cfg.startsWith('--')) {
    return [`claude conversation argv 的 --mcp-config 值不合法：${JSON.stringify(cfg)}`];
  }
  const expected = expectedConversationArgv({ mcpConfigPath: cfg, resume: expectedResume });
  // resume 的有無先單獨說清楚——只靠長度／位置差異的訊息讀起來像「少了兩個
  // token」，看不出本輪該不該續聊（與 fakeClaudeCli.validateConversationArgv 同語意）。
  if (expectedResume === null && argv.includes('--resume')) {
    v.push('claude conversation argv 本輪核定為 fresh start，不得含 --resume');
  }
  if (expectedResume !== null && !argv.includes('--resume')) {
    v.push(`claude conversation argv 本輪核定為 resume ${JSON.stringify(expectedResume)}，argv 卻沒有 --resume`);
  }
  if (argv.length !== expected.length) {
    v.push(`claude conversation argv 長度應為 ${expected.length}，實際 ${argv.length}`);
  }
  const n = Math.min(argv.length, expected.length);
  for (let k = 0; k < n; k += 1) {
    if (argv[k] !== expected[k]) {
      v.push(`claude conversation argv[${k}] 應為 ${JSON.stringify(expected[k])}，實際 ${JSON.stringify(argv[k])}`);
    }
  }
  return v;
}

export interface ScenarioTripwireOptions {
  /** 本次 scenario 的 provider——**兩種 provider 不共用同一個寬鬆判定**。 */
  provider: 'codex' | 'claude';
  /** Claude 案必填：結構化 argv 紀錄所在的 tools 目錄。 */
  toolsDir?: string;
  /**
   * B3a-2b-2 E1：本次核定的**對話呼叫次數**，由已驗證的 scenario identity 決定
   * （approval → 1、recovery → 2），**不得從觀測到的呼叫數推定**。未指定時
   * 維持既有的「恰一次」契約。
   */
  expectedConversationCalls?: number;
  /**
   * 第二輪（含之後）核定的 resume 值——來自固定 fixture builder 的 sessionId。
   * expectedConversationCalls >= 2 時必填；缺少即違規（fail closed）。
   */
  expectedResume?: string | null;
}

export function judgeScenarioCliCalls(
  logFile: string, log: HarnessLogger,
  opts: ScenarioTripwireOptions = { provider: 'codex' },
): string[] {
  return opts.provider === 'claude'
    ? judgeClaudeScenarioCalls(logFile, log, opts)
    : judgeCodexScenarioCalls(logFile, log);
}

interface StructuredArgvRecord { ts?: unknown; pid?: unknown; argv?: unknown }

/**
 * Claude 案：以結構化 argv 紀錄為準；同時要求共用的 invocations.log 裡確實
 * 出現 `name=claude-scenario`（證明 wrapper 真的接上了正式紀錄，而不是只有
 * 手刻的結構化檔）。codex 在本案退回 version-only。
 */
function judgeClaudeScenarioCalls(
  logFile: string, log: HarnessLogger, opts: ScenarioTripwireOptions,
): string[] {
  const toolsDir = opts.toolsDir;
  const violations: string[] = [];
  // 核定輪數：identity 決定，預設 1（既有單輪契約）。
  const expectedCalls = opts.expectedConversationCalls ?? 1;
  const expectedResume = opts.expectedResume ?? null;
  if (!Number.isInteger(expectedCalls) || expectedCalls < 1) {
    violations.push(`核定對話次數不合法：${JSON.stringify(opts.expectedConversationCalls)}`);
  }
  if (expectedCalls >= 2 && (expectedResume === null || expectedResume === '')) {
    violations.push('核定兩輪以上卻沒有給 expectedResume——無法判定第二輪該帶哪個 resume，拒絕通過');
  }
  const text = fs.readFileSync(logFile, 'utf8');
  const lines = text.split('\n').map(l => l.trim()).filter(Boolean);
  if (lines.length === 0) violations.push('invocations.log 為空（缺失呼叫紀錄不算通過）');

  let claudeWrapperLines = 0;
  let codexVersionCalls = 0;
  for (const line of lines) {
    const m = TRIPWIRE_LINE_RE.exec(line);
    if (!m || !m.groups) {
      violations.push(`invocations.log 出現無法解析的行：${line}`);
      continue;
    }
    const name = m.groups.name;
    const argv = m.groups.argv.trim();
    if (name === 'claude-scenario') { claudeWrapperLines += 1; continue; }
    if (name === 'codex') {
      if (argv !== '--version') violations.push(`codex 收到非 --version 的呼叫：argv=(${argv})`);
      codexVersionCalls += 1;
      continue;
    }
    violations.push(`未知的假 CLI 名稱：${name}`);
  }
  if (claudeWrapperLines < 1) {
    violations.push('共用 invocations.log 內沒有任何 name=claude-scenario 行——wrapper 未接上正式紀錄');
  }

  if (toolsDir === undefined || toolsDir === '') {
    violations.push('Claude 案的 tripwire 需要 toolsDir 才能讀結構化 argv 紀錄');
    log.log(`scenario 呼叫紀律判定（claude）：缺 toolsDir，違規 ${violations.length} 筆`);
    return violations;
  }
  const argvLogPath = path.join(toolsDir, 'claude-argv.jsonl');
  let argvText: string;
  try { argvText = fs.readFileSync(argvLogPath, 'utf8'); }
  catch (e) {
    violations.push(`無法讀取結構化 argv 紀錄 ${argvLogPath}：${String(e)}`);
    log.log(`scenario 呼叫紀律判定（claude）：argv 紀錄讀取失敗，違規 ${violations.length} 筆`);
    return violations;
  }

  let versionCalls = 0;
  let conversationCalls = 0;
  const argvLines = argvText.split('\n').map(l => l.trim()).filter(Boolean);
  argvLines.forEach((line, idx) => {
    let rec: StructuredArgvRecord;
    try { rec = JSON.parse(line) as StructuredArgvRecord; }
    catch (e) { violations.push(`argv 紀錄第 ${idx} 行無法解析：${String(e)}`); return; }
    const argv = rec.argv;
    if (!Array.isArray(argv) || argv.some(x => typeof x !== 'string')) {
      violations.push(`argv 紀錄第 ${idx} 行的 argv 不是字串陣列：${JSON.stringify(argv)}`);
      return;
    }
    const a = argv as string[];
    // `--version` 探測不計輪次（app.go:3365 CLIInfo／preflight 都會呼叫）。
    if (a.length === 1 && a[0] === '--version') { versionCalls += 1; return; }
    conversationCalls += 1;
    // 這裡的序號**只是紀錄順序**，不是輪次的權威來源——權威是假 CLI 的排他
    // claim（見 fakeClaudeCli claimConversationRound 與各輪的 round.json）。
    // 這一層是獨立的第二道：第 1 筆必須 fresh、其後每筆必須帶核定的 resume。
    if (conversationCalls > expectedCalls) {
      violations.push(
        `claude-scenario 的 conversation 呼叫超出核定 ${expectedCalls} 次：第 ${conversationCalls} 筆 ${JSON.stringify(a)}`,
      );
      return;
    }
    const wantResume = conversationCalls === 1 ? null : expectedResume;
    const cv = judgeClaudeConversationArgvStrict(a, wantResume);
    if (cv.length > 0) {
      violations.push(
        `claude-scenario 第 ${conversationCalls} 筆 conversation 呼叫非核定（核定 resume=${JSON.stringify(wantResume)}）：`
        + `${JSON.stringify(a)}｜${cv.join('; ')}`,
      );
    }
  });

  if (codexVersionCalls < 1) {
    violations.push('codex 從未以 --version 被呼叫——Claude 整合仍必須留下兩個工具的隔離證據');
  }
  if (versionCalls < 1) violations.push('claude-scenario 從未以 --version 被呼叫（CLIInfo／preflight 應呼叫）');
  if (conversationCalls !== expectedCalls) {
    violations.push(
      `claude-scenario 的 conversation 呼叫出現 ${conversationCalls} 次，核定為 ${expectedCalls} 次`
      + '（次數由 scenario identity 決定，不由觀測值反推）',
    );
  }
  if (claudeWrapperLines !== argvLines.length) {
    violations.push(
      `wrapper 行數（${claudeWrapperLines}）與結構化 argv 紀錄筆數（${argvLines.length}）不一致`
      + '——兩份紀錄必須同源',
    );
  }
  log.log(
    `scenario 呼叫紀律判定（claude）：--version ${versionCalls} 次、conversation ${conversationCalls}/${expectedCalls} 次、`
    + `核定 resume=${JSON.stringify(expectedResume)}、codex --version ${codexVersionCalls} 次、`
    + `wrapper 行 ${claudeWrapperLines} 筆、違規 ${violations.length} 筆`,
  );
  return violations;
}

function judgeCodexScenarioCalls(logFile: string, log: HarnessLogger): string[] {
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
