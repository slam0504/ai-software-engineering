// B3a-2b-2 F2：provider routing／scenario identity／tripwire 的離線正負控制。
//
// 執行（cwd = frontend）：
//   node --experimental-loader=./e2e/support/selftestJsToTsLoader.mjs \
//        e2e/support/scenario/f2Routing.selftest.ts
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { resolveScenario } from './scenarios.ts';
import {
  APPROVAL_SPEC_FILE, CLAUDE_APPROVAL_SPEC_FILE, CLAUDE_RECOVERY_SPEC_FILE, RECOVERY_SPEC_FILE,
  resolveScenarioProviderForTripwire, resolveScenarioSpecFile,
} from './specRouting.ts';
import { judgeClaudeConversationArgvStrict, judgeScenarioCliCalls } from './scenarioTripwire.ts';
import { createScenarioClaudeCli } from './claudeScenarioCli.ts';
import { expectedConversationArgv } from './fakeClaudeCli.ts';
import { buildClaudeRecoveryExpectation } from './claudeApprovalProtocol.ts';

let passed = 0;
const failures: string[] = [];
function check(name: string, fn: () => void): void {
  try { fn(); console.log(`ok - ${name}`); passed += 1; }
  catch (e) { console.log(`FAIL - ${name}`); console.error(e); failures.push(name); process.exitCode = 1; }
}
async function acheck(name: string, fn: () => Promise<void>): Promise<void> {
  try { await fn(); console.log(`ok - ${name}`); passed += 1; }
  catch (e) { console.log(`FAIL - ${name}`); console.error(e); failures.push(name); process.exitCode = 1; }
}

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'f2-routing-'));
const log = { log: (_m: string) => {} } as unknown as Parameters<typeof judgeScenarioCliCalls>[1];

// --- scenario identity -----------------------------------------------------
const CODEX_CASES = ['commandExecution-allow', 'commandExecution-deny',
  'fileChange-allow', 'fileChange-deny', 'commandExecution-recovery'];

check('identity：既有五案的 provider 全部仍是 codex（契約不變）', () => {
  for (const n of CODEX_CASES) assert.equal(resolveScenario(n).provider, 'codex', n);
});
check('identity：既有五案的 kind／decision 不變', () => {
  assert.equal(resolveScenario('commandExecution-allow').decision, 'accept');
  assert.equal(resolveScenario('fileChange-deny').decision, 'decline');
  assert.equal(resolveScenario('commandExecution-recovery').kind, 'recovery');
});
check('identity：新增 claude-approval-allow，provider=claude、kind=approval', () => {
  const d = resolveScenario('claude-approval-allow');
  assert.equal(d.provider, 'claude');
  assert.equal(d.kind, 'approval');
});
check('identity：新增 claude-approval-deny，provider=claude、kind=approval、decision=decline', () => {
  const d = resolveScenario('claude-approval-deny');
  assert.equal(d.provider, 'claude');
  assert.equal(d.kind, 'approval');
  assert.equal(d.decision, 'decline');
  assert.throws(() => d.build('run'), /誤用了 Codex 路徑/);
});
check('identity：allow 與 deny 兩案的 decision 必須相反（不是同一個值的複製）', () => {
  assert.equal(resolveScenario('claude-approval-allow').decision, 'accept');
  assert.equal(resolveScenario('claude-approval-deny').decision, 'decline');
});
check('identity：新增 claude-approval-recovery，provider=claude、kind=recovery', () => {
  const d = resolveScenario('claude-approval-recovery');
  assert.equal(d.provider, 'claude');
  assert.equal(d.kind, 'recovery');
  assert.equal(d.decision, 'accept');
});
check('identity：Claude 案的 build() 必須 throw（不得被 Codex 路徑誤用）', () => {
  assert.throws(() => resolveScenario('claude-approval-allow').build('run'), /誤用了 Codex 路徑/);
  assert.throws(() => resolveScenario('claude-approval-recovery').build('run'), /誤用了 Codex 路徑/);
});
check('identity：既有五案的 build() 仍可正常產生 Codex config', () => {
  const cfg = resolveScenario('commandExecution-allow').build('run1');
  assert.equal(cfg.scenario, 'commandExecution-allow');
  assert.equal(cfg.threadId, 'b3a2b2-thread-run1');
});
check('identity：未知／缺失 scenario 仍一律 throw，不回退', () => {
  assert.throws(() => resolveScenario('nope'), /未知 scenario/);
  assert.throws(() => resolveScenario(undefined), /未設定/);
  assert.throws(() => resolveScenario('toString'), /未知 scenario/);
});

// --- spec routing ----------------------------------------------------------
check('routing：claude 案路由到 claudeApproval.spec.ts', () => {
  assert.equal(resolveScenarioSpecFile('claude-approval-allow'), CLAUDE_APPROVAL_SPEC_FILE);
});
check('routing：claude deny 案與 allow 案共用同一支 spec（decision 不影響選哪支）', () => {
  assert.equal(resolveScenarioSpecFile('claude-approval-deny'), CLAUDE_APPROVAL_SPEC_FILE);
  assert.equal(resolveScenarioSpecFile('claude-approval-allow'), CLAUDE_APPROVAL_SPEC_FILE);
});
check('routing：claude deny 案的 tripwire provider 仍是 claude', () => {
  assert.equal(resolveScenarioProviderForTripwire('claude-approval-deny'), 'claude');
});
check('routing：claude recovery 案路由到 claudeSessionRecovery.spec.ts（不掉回 approval）', () => {
  assert.equal(resolveScenarioSpecFile('claude-approval-recovery'), CLAUDE_RECOVERY_SPEC_FILE);
  assert.notEqual(CLAUDE_RECOVERY_SPEC_FILE, CLAUDE_APPROVAL_SPEC_FILE);
  assert.notEqual(CLAUDE_RECOVERY_SPEC_FILE, RECOVERY_SPEC_FILE);
});
check('routing：Codex approval／recovery 的既有對應完全不變', () => {
  for (const n of ['commandExecution-allow', 'commandExecution-deny', 'fileChange-allow', 'fileChange-deny']) {
    assert.equal(resolveScenarioSpecFile(n), APPROVAL_SPEC_FILE, n);
  }
  assert.equal(resolveScenarioSpecFile('commandExecution-recovery'), RECOVERY_SPEC_FILE);
});
check('routing：未知名稱仍回中性預設（拒絕由 globalSetup 負責），不路由到 claude', () => {
  assert.equal(resolveScenarioSpecFile('nope'), APPROVAL_SPEC_FILE);
  assert.equal(resolveScenarioSpecFile(undefined), APPROVAL_SPEC_FILE);
});
check('routing：每個登記案都恰好對應一支 spec 檔', () => {
  const all = [...CODEX_CASES, 'claude-approval-allow', 'claude-approval-deny', 'claude-approval-recovery'];
  const files = new Set(all.map(n => resolveScenarioSpecFile(n)));
  assert.deepEqual([...files].sort(),
    [CLAUDE_APPROVAL_SPEC_FILE, CLAUDE_RECOVERY_SPEC_FILE, APPROVAL_SPEC_FILE, RECOVERY_SPEC_FILE].sort());
});
check('routing：tripwire provider 解析正確，未知回中性 codex', () => {
  assert.equal(resolveScenarioProviderForTripwire('claude-approval-allow'), 'claude');
  assert.equal(resolveScenarioProviderForTripwire('claude-approval-recovery'), 'claude');
  assert.equal(resolveScenarioProviderForTripwire('commandExecution-allow'), 'codex');
  assert.equal(resolveScenarioProviderForTripwire('nope'), 'codex');
  assert.equal(resolveScenarioProviderForTripwire(undefined), 'codex');
});

// --- Claude conversation argv 判定 -----------------------------------------
const APPROVED_ARGV = expectedConversationArgv({ mcpConfigPath: '/tmp/app/.workbench/mcp-ws1.json' });

check('argv：核定 conversation argv 無違規', () => {
  assert.deepEqual(judgeClaudeConversationArgvStrict([...APPROVED_ARGV]), []);
});
check('argv：缺 --verbose 必須被擋', () => {
  assert.ok(judgeClaudeConversationArgvStrict(APPROVED_ARGV.filter(x => x !== '--verbose')).length > 0);
});
check('argv：缺 --strict-mcp-config 必須被擋', () => {
  assert.ok(judgeClaudeConversationArgvStrict(APPROVED_ARGV.filter(x => x !== '--strict-mcp-config')).length > 0);
});
check('argv：帶 --resume 必須被擋（本案 fresh start）', () => {
  assert.ok(judgeClaudeConversationArgvStrict([...APPROVED_ARGV, '--resume', 'sess1']).length > 0);
});
check('argv：未知旗標必須被擋', () => {
  assert.ok(judgeClaudeConversationArgvStrict([...APPROVED_ARGV, '--dangerously-skip-permissions']).length > 0);
});
check('argv：**旗標的值被改掉**必須被擋（舊的 flag-set 判定抓不到）', () => {
  const a = [...APPROVED_ARGV];
  a[a.indexOf('--permission-prompt-tool') + 1] = 'mcp__evil__approval_prompt';
  const v = judgeClaudeConversationArgvStrict(a);
  assert.ok(v.some(x => x.includes('mcp__evil__approval_prompt')), JSON.stringify(v));
});
check('argv：**順序被調換**必須被擋', () => {
  const a = [...APPROVED_ARGV];
  [a[0], a[1]] = [a[1], a[0]];
  assert.ok(judgeClaudeConversationArgvStrict(a).some((x: string) => x.includes('argv[0]')));
});
check('argv：**多餘 positional**必須被擋（舊的 flag-set 判定抓不到）', () => {
  assert.ok(judgeClaudeConversationArgvStrict([...APPROVED_ARGV, 'extra-positional']).length > 0);
});
check('argv：**重複旗標**必須被擋', () => {
  assert.ok(judgeClaudeConversationArgvStrict([...APPROVED_ARGV, '--verbose']).length > 0);
});
check('argv：多個 --mcp-config 必須被擋', () => {
  assert.ok(judgeClaudeConversationArgvStrict([...APPROVED_ARGV, '--mcp-config', '/x.json'])
    .some((x: string) => x.includes('多個 --mcp-config')));
});
check('argv：缺 --mcp-config 必須被擋', () => {
  assert.ok(judgeClaudeConversationArgvStrict(['-p', '--verbose'])
    .some((x: string) => x.includes('缺少 --mcp-config')));
});

// --- tripwire：Claude 案（結構化 argv 紀錄） --------------------------------
const L = (name: string, argv: string): string =>
  `[2026-09-21T00:00:00Z] name=${name} argv=(${argv}) cwd=/w ppid=1`;

interface ClaudeToolsFixture { toolsDir: string; logFile: string }
function makeClaudeTools(wrapperLines: string[], argvRecords: unknown[]): ClaudeToolsFixture {
  const toolsDir = fs.mkdtempSync(path.join(tmp, 'tools-'));
  fs.writeFileSync(path.join(toolsDir, 'invocations.log'), `${wrapperLines.join('\n')}\n`);
  fs.writeFileSync(path.join(toolsDir, 'claude-argv.jsonl'),
    argvRecords.map(r => JSON.stringify(r)).join('\n') + (argvRecords.length > 0 ? '\n' : ''));
  return { toolsDir, logFile: path.join(toolsDir, 'invocations.log') };
}
const goodWrapperLines = [L('claude-scenario', '--version'), L('codex', '--version'),
  L('claude-scenario', 'redacted')];
const goodArgvRecords = [{ ts: 't', pid: 1, argv: ['--version'] },
  { ts: 't', pid: 2, argv: [...APPROVED_ARGV] }];

check('tripwire(claude)：正控制無違規', () => {
  const f = makeClaudeTools(goodWrapperLines, goodArgvRecords);
  assert.deepEqual(judgeScenarioCliCalls(f.logFile, log, { provider: 'claude', toolsDir: f.toolsDir }), []);
});
check('tripwire(claude)：缺 toolsDir 必須判失敗，不得因為讀不到就放行', () => {
  const f = makeClaudeTools(goodWrapperLines, goodArgvRecords);
  assert.ok(judgeScenarioCliCalls(f.logFile, log, { provider: 'claude' })
    .some(x => x.includes('需要 toolsDir')));
});
check('tripwire(claude)：從未以 conversation argv 被呼叫必須被擋', () => {
  const f = makeClaudeTools(goodWrapperLines.slice(0, 2), [goodArgvRecords[0]]);
  assert.ok(judgeScenarioCliCalls(f.logFile, log, { provider: 'claude', toolsDir: f.toolsDir })
    .some(x => x.includes('出現 0 次，核定為 1 次')));
});
check('tripwire(claude)：conversation 被呼叫兩次必須被擋', () => {
  const f = makeClaudeTools([...goodWrapperLines, L('claude-scenario', 'redacted')],
    [...goodArgvRecords, { ts: 't', pid: 3, argv: [...APPROVED_ARGV] }]);
  assert.ok(judgeScenarioCliCalls(f.logFile, log, { provider: 'claude', toolsDir: f.toolsDir })
    .some(x => x.includes('超出核定 1 次')));
});
check('tripwire(claude)：非核定 argv（值被改）必須被擋', () => {
  const bad = [...APPROVED_ARGV]; bad[bad.indexOf('--settings') + 1] = '{"permissions":{}}';
  const f = makeClaudeTools(goodWrapperLines, [goodArgvRecords[0], { ts: 't', pid: 2, argv: bad }]);
  assert.ok(judgeScenarioCliCalls(f.logFile, log, { provider: 'claude', toolsDir: f.toolsDir })
    .some(x => x.includes('conversation 呼叫非核定')));
});
check('tripwire(claude)：wrapper 行數與結構化紀錄筆數不一致必須被擋（兩份必須同源）', () => {
  const f = makeClaudeTools([L('claude-scenario', '--version')], goodArgvRecords);
  assert.ok(judgeScenarioCliCalls(f.logFile, log, { provider: 'claude', toolsDir: f.toolsDir })
    .some(x => x.includes('必須同源')));
});
check('tripwire(claude)：共用 invocations.log 內沒有 claude-scenario 行必須被擋', () => {
  const f = makeClaudeTools([L('codex', '--version')], goodArgvRecords);
  assert.ok(judgeScenarioCliCalls(f.logFile, log, { provider: 'claude', toolsDir: f.toolsDir })
    .some(x => x.includes('wrapper 未接上正式紀錄')));
});
check('tripwire(claude)：codex 出現 app-server（跨 provider）必須被擋，不回退成 Codex 判定', () => {
  const f = makeClaudeTools([...goodWrapperLines, L('codex', 'app-server')], goodArgvRecords);
  assert.ok(judgeScenarioCliCalls(f.logFile, log, { provider: 'claude', toolsDir: f.toolsDir })
    .some(x => x.includes('codex 收到非 --version')));
});
check('tripwire(claude)：未知 CLI 名稱必須被擋', () => {
  const f = makeClaudeTools([...goodWrapperLines, L('codex-scenario', 'app-server')], goodArgvRecords);
  assert.ok(judgeScenarioCliCalls(f.logFile, log, { provider: 'claude', toolsDir: f.toolsDir })
    .some(x => x.includes('未知的假 CLI 名稱')));
});
check('tripwire(claude)：結構化紀錄無法解析必須被擋，不靜默略過', () => {
  const toolsDir = fs.mkdtempSync(path.join(tmp, 'tools-bad-'));
  fs.writeFileSync(path.join(toolsDir, 'invocations.log'), `${goodWrapperLines.join('\n')}\n`);
  fs.writeFileSync(path.join(toolsDir, 'claude-argv.jsonl'), 'NOT JSON\n');
  assert.ok(judgeScenarioCliCalls(path.join(toolsDir, 'invocations.log'), log,
    { provider: 'claude', toolsDir }).some(x => x.includes('無法解析')));
});

// --- 跨邊界：**實際 wrapper 輸出** → 正式紀錄 → tripwire --------------------
// 不是手刻 log：真的產生 wrapper、真的執行它兩次，再用產生出來的檔案判定。
await acheck('整合：實際 wrapper 執行 → invocations.log ＋ claude-argv.jsonl → tripwire 通過', async () => {
  const toolsDir = fs.mkdtempSync(path.join(tmp, 'wrapper-'));
  const fakeCliPath = path.resolve(import.meta.dirname, 'fakeClaudeCli.ts');
  const cli = createScenarioClaudeCli(toolsDir, 'itest', fakeCliPath, {
    mcpConfigPath: '', expectationPath: path.join(toolsDir, 'missing-expectation.json'),
    evidenceDir: path.join(toolsDir, 'evidence'),
  });
  assert.equal(cli.invocationsLog, path.join(toolsDir, 'invocations.log'), 'wrapper 必須寫共用的 invocations.log');
  const PATHV = `${path.dirname(process.execPath)}:/usr/bin:/bin`;
  const runWrapper = (argv: string[]): number => {
    const r = spawnSync(cli.claudeBin, argv, { env: { PATH: PATHV }, encoding: 'utf8' });
    return r.status ?? -1;
  };
  // 1) --version：應成功並印出本次版本字串
  const vr = spawnSync(cli.claudeBin, ['--version'], { env: { PATH: PATHV }, encoding: 'utf8' });
  assert.equal(vr.status, 0, `--version 應成功｜stderr=${vr.stderr}`);
  assert.equal(vr.stdout.trim(), cli.claudeVersion);
  // 2) conversation argv：期望檔不存在，因此會在記錄 argv 之後失敗——
  //    這正好驗證 argv 紀錄是在最前面寫下的，不會因後續失敗而遺失。
  const cr = runWrapper([...APPROVED_ARGV]);
  assert.notEqual(cr, 0, 'conversation 呼叫因缺期望檔應非零退出');

  const argvText = fs.readFileSync(cli.argvLog, 'utf8').trim().split('\n');
  assert.equal(argvText.length, 2, `應有兩筆結構化 argv 紀錄，實際 ${argvText.length}`);
  const invText = fs.readFileSync(cli.invocationsLog, 'utf8').trim().split('\n');
  assert.equal(invText.filter(l => l.includes('name=claude-scenario')).length, 2,
    `invocations.log 應有兩行 claude-scenario：${JSON.stringify(invText)}`);
  // 補上 codex 的 version 行（正式 run 由 base fake CLI 產生）
  fs.appendFileSync(cli.invocationsLog, `${L('codex', '--version')}\n`);
  const v = judgeScenarioCliCalls(cli.invocationsLog, log, { provider: 'claude', toolsDir });
  assert.deepEqual(v, [], `實際 wrapper 產物應通過 tripwire：${JSON.stringify(v)}`);
});

// --- tripwire：兩輪案（E1）----------------------------------------------
// 核定輪數與核定 resume 都由呼叫端（teardown 依已驗證 identity）給定，
// **不由觀測到的呼叫數或待驗 argv 推定**。
const S2 = buildClaudeRecoveryExpectation('trip').sessionId;
const RECOVERY_OPTS = { provider: 'claude' as const, expectedConversationCalls: 2, expectedResume: S2 };
const freshArgv = (): string[] => [...APPROVED_ARGV];
const resumeArgv = (id = S2): string[] =>
  expectedConversationArgv({ mcpConfigPath: '/tmp/app/.workbench/mcp-ws1.json', resume: id });
const twoRoundWrapper = [L('claude-scenario', '--version'), L('codex', '--version'),
  L('claude-scenario', 'redacted'), L('claude-scenario', 'redacted')];
const twoRoundArgv = (second: string[]): unknown[] => [
  { ts: 't', pid: 1, argv: ['--version'] },
  { ts: 't', pid: 2, argv: freshArgv() },
  { ts: 't', pid: 3, argv: second },
];

check('tripwire(claude,2 輪)：正控制——第一輪 fresh、第二輪 --resume S，無違規', () => {
  const f = makeClaudeTools(twoRoundWrapper, twoRoundArgv(resumeArgv()));
  const v = judgeScenarioCliCalls(f.logFile, log, { ...RECOVERY_OPTS, toolsDir: f.toolsDir });
  assert.deepEqual(v, [], JSON.stringify(v));
});
check('tripwire(claude,2 輪)：第二輪沒帶 --resume 必須被擋', () => {
  const f = makeClaudeTools(twoRoundWrapper, twoRoundArgv(freshArgv()));
  assert.ok(judgeScenarioCliCalls(f.logFile, log, { ...RECOVERY_OPTS, toolsDir: f.toolsDir })
    .some(x => x.includes('argv 卻沒有 --resume')));
});
check('tripwire(claude,2 輪)：第二輪 resume 值不是 S 必須被擋', () => {
  const f = makeClaudeTools(twoRoundWrapper, twoRoundArgv(resumeArgv('someone-else')));
  assert.ok(judgeScenarioCliCalls(f.logFile, log, { ...RECOVERY_OPTS, toolsDir: f.toolsDir })
    .some(x => x.includes('第 2 筆 conversation 呼叫非核定')));
});
check('tripwire(claude,2 輪)：**第一輪就帶 resume** 必須被擋（順序不得對調）', () => {
  const f = makeClaudeTools(twoRoundWrapper, [
    { ts: 't', pid: 1, argv: ['--version'] },
    { ts: 't', pid: 2, argv: resumeArgv() },
    { ts: 't', pid: 3, argv: freshArgv() },
  ]);
  const v = judgeScenarioCliCalls(f.logFile, log, { ...RECOVERY_OPTS, toolsDir: f.toolsDir });
  assert.ok(v.some(x => x.includes('第 1 筆')), JSON.stringify(v));
  assert.ok(v.some(x => x.includes('第 2 筆')), JSON.stringify(v));
});
check('tripwire(claude,2 輪)：只有一輪必須被擋（缺輪不得當成通過）', () => {
  const f = makeClaudeTools(goodWrapperLines, goodArgvRecords);
  assert.ok(judgeScenarioCliCalls(f.logFile, log, { ...RECOVERY_OPTS, toolsDir: f.toolsDir })
    .some(x => x.includes('出現 1 次，核定為 2 次')));
});
check('tripwire(claude,2 輪)：**第三次對話呼叫**必須被擋', () => {
  const f = makeClaudeTools([...twoRoundWrapper, L('claude-scenario', 'redacted')],
    [...twoRoundArgv(resumeArgv()), { ts: 't', pid: 4, argv: resumeArgv() }]);
  assert.ok(judgeScenarioCliCalls(f.logFile, log, { ...RECOVERY_OPTS, toolsDir: f.toolsDir })
    .some(x => x.includes('超出核定 2 次')));
});
check('tripwire(claude,2 輪)：核定兩輪卻沒給 expectedResume 必須 fail closed', () => {
  const f = makeClaudeTools(twoRoundWrapper, twoRoundArgv(resumeArgv()));
  assert.ok(judgeScenarioCliCalls(f.logFile, log,
    { provider: 'claude', toolsDir: f.toolsDir, expectedConversationCalls: 2 })
    .some(x => x.includes('沒有給 expectedResume')));
});
check('tripwire(claude,1 輪)：核定一輪時，第二輪的 resume 呼叫仍被擋（既有契約不放寬）', () => {
  const f = makeClaudeTools(twoRoundWrapper, twoRoundArgv(resumeArgv()));
  const v = judgeScenarioCliCalls(f.logFile, log, { provider: 'claude', toolsDir: f.toolsDir });
  assert.ok(v.some(x => x.includes('超出核定 1 次')), JSON.stringify(v));
});
check('tripwire(claude)：--version 呼叫不計輪次（多次探測不影響核定輪數）', () => {
  const f = makeClaudeTools(
    [L('claude-scenario', '--version'), L('claude-scenario', '--version'),
      L('codex', '--version'), L('claude-scenario', 'redacted'), L('claude-scenario', 'redacted')],
    [{ ts: 't', pid: 1, argv: ['--version'] }, { ts: 't', pid: 2, argv: ['--version'] },
      { ts: 't', pid: 3, argv: freshArgv() }, { ts: 't', pid: 4, argv: resumeArgv() }]);
  const v = judgeScenarioCliCalls(f.logFile, log, { ...RECOVERY_OPTS, toolsDir: f.toolsDir });
  assert.deepEqual(v, [], JSON.stringify(v));
});

function writeLog(lines: string[]): string {
  const p = path.join(tmp, `log-${Math.random().toString(36).slice(2)}.log`);
  fs.writeFileSync(p, `${lines.join('\n')}\n`);
  return p;
}

// --- tripwire：Codex 案（既有契約必須完全不變） -----------------------------
const codexGood = [L('claude', '--version'), L('codex-scenario', '--version'), L('codex-scenario', 'app-server')];
check('tripwire(codex)：既有正控制仍無違規', () => {
  assert.deepEqual(judgeScenarioCliCalls(writeLog(codexGood), log, { provider: 'codex' }), []);
});
check('tripwire(codex)：預設不帶 opts 時行為等同 codex（既有呼叫端相容）', () => {
  assert.deepEqual(judgeScenarioCliCalls(writeLog(codexGood), log), []);
});
check('tripwire(codex)：claude 收到非 --version 仍被擋', () => {
  const v = judgeScenarioCliCalls(writeLog([...codexGood, L('claude', '-p --verbose')]), log, { provider: 'codex' });
  assert.ok(v.some(x => x.includes('claude 收到非 --version')), JSON.stringify(v));
});
check('tripwire(codex)：缺 app-server 仍被擋', () => {
  const v = judgeScenarioCliCalls(writeLog(codexGood.slice(0, 2)), log, { provider: 'codex' });
  assert.ok(v.some(x => x.includes('從未以 app-server')), JSON.stringify(v));
});
check('tripwire(codex)：claude-scenario 這個名稱在 Codex 案屬未知，必須被擋', () => {
  const v = judgeScenarioCliCalls(writeLog([...codexGood, L('claude-scenario', '--version')]), log, { provider: 'codex' });
  assert.ok(v.some(x => x.includes('未知的假 CLI 名稱')), JSON.stringify(v));
});

console.log(`\n${passed} passed, ${failures.length} failed`);
if (failures.length > 0) console.log(`failed: ${failures.join(' | ')}`);
