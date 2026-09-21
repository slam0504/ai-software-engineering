// B3a-2b-2 F2：run-env 雙向傳遞（env／file 兩路）與 determineExecutionMode
// 的 provider 判定——跨實際邊界，不只驗 leaf 函式。
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { readClaudeScenarioRunEnv, readRunEnv, writeRunEnv, type RunEnv } from '../env.ts';
import { determineExecutionMode } from '../executionMode.ts';
import { handleClaudeSetupFailure } from './claudeSetupFailure.ts';

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
const log = { log: (_m: string) => {} } as unknown as Parameters<typeof determineExecutionMode>[2];

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'f2-runenv-'));
const SAVED = { ...process.env };
function resetEnv(): void {
  for (const k of Object.keys(process.env)) if (k.startsWith('E2E_')) delete process.env[k];
  for (const [k, v] of Object.entries(SAVED)) if (k.startsWith('E2E_') && v !== undefined) process.env[k] = v;
  for (const k of Object.keys(process.env)) if (k.startsWith('E2E_') && !(k in SAVED)) delete process.env[k];
}

function claudeEnv(dir: string): RunEnv {
  return {
    runId: 'r1', artifactsDir: dir, workspaceDir: '/w', toolsDir: '/t', glossaryPath: '/g',
    claudeVersion: 'cv', codexVersion: 'xv', baseUrl: 'http://127.0.0.1:34115',
    scenario: 'claude-approval-allow',
    claudeExpectationPath: `${dir}/exp.json`, claudeEvidenceDir: `${dir}/ev`,
    claudeApprovedCommandPath: '/bin/app', claudeApprovedCommandSha256: 'abc',
    claudeStateDir: '/w/.workbench',
  };
}
function codexEnv(dir: string): RunEnv {
  return {
    runId: 'r1', artifactsDir: dir, workspaceDir: '/w', toolsDir: '/t', glossaryPath: '/g',
    claudeVersion: 'cv', codexVersion: 'xv', baseUrl: 'http://127.0.0.1:34115',
    scenario: 'commandExecution-allow',
    scenarioThreadId: 'th', scenarioTurnId: 'tu', scenarioItemId: 'it',
    scenarioApprovalMethod: 'm', scenarioApprovalRequestId: 'ar', scenarioDecision: 'accept',
    scenarioConfigPath: '/c', scenarioLogPath: '/l', scenarioManifestPath: '/m',
  };
}
/** 回傳**實際傳給 writeRunEnv 的那份輸入**——測試逐欄與它比較，不另外手寫路徑。 */
function mkRun(name: string, env: RunEnv, entry = 'scenario'): { dir: string; input: RunEnv } {
  const dir = path.join(tmp, name);
  fs.mkdirSync(dir, { recursive: true });
  const input = { ...env, artifactsDir: dir };
  fs.writeFileSync(path.join(dir, 'execution-entry.json'), JSON.stringify({ entry }));
  process.env.E2E_ARTIFACTS_DIR = dir;
  writeRunEnv(input);
  return { dir, input };
}

// --- writeRunEnv → readRunEnv：env 路徑 ------------------------------------
const CLAUDE_FIELDS = ['claudeExpectationPath', 'claudeEvidenceDir',
  'claudeApprovedCommandPath', 'claudeApprovedCommandSha256', 'claudeStateDir'] as const;

check('run-env：Claude 欄位經 process.env 路徑完整往返（逐欄對照實際輸入）', () => {
  resetEnv();
  const { dir, input } = mkRun('claude-env', claudeEnv(tmp));
  const back = readRunEnv();
  for (const k of CLAUDE_FIELDS) {
    assert.equal(back[k], input[k], `${k} 應與傳給 writeRunEnv 的輸入相同`);
  }
  // 確認確實走 env 路徑：把檔案刪掉仍讀得到同一組值
  fs.rmSync(path.join(dir, 'run-env.json'));
  const again = readRunEnv();
  for (const k of CLAUDE_FIELDS) {
    assert.equal(again[k], input[k], `${k} 應走 process.env 路徑，不依賴檔案`);
  }
});
check('run-env：Claude 欄位經純檔案 fallback 路徑完整往返（逐欄對照實際輸入）', () => {
  resetEnv();
  const { dir, input } = mkRun('claude-file', claudeEnv(tmp));
  delete process.env.E2E_RUN_ID;   // 清掉關鍵 env 讓 readRunEnv 退回讀檔
  process.env.E2E_ARTIFACTS_DIR = dir;
  const back = readRunEnv();
  for (const k of CLAUDE_FIELDS) {
    assert.equal(back[k], input[k], `${k} 應與傳給 writeRunEnv 的輸入相同（檔案路徑）`);
  }
});
check('run-env：readClaudeScenarioRunEnv 欄位齊全時通過', () => {
  resetEnv();
  mkRun('claude-ok', claudeEnv(tmp));
  assert.equal(readClaudeScenarioRunEnv().claudeStateDir, '/w/.workbench');
});
check('run-env：readClaudeScenarioRunEnv 缺欄位必須 throw，不回退', () => {
  resetEnv();
  const e = claudeEnv(tmp); delete (e as unknown as Record<string, unknown>).claudeStateDir;
  mkRun('claude-missing', e as RunEnv);
  assert.throws(() => readClaudeScenarioRunEnv(), /缺少 Claude scenario 欄位/);
});

// --- determineExecutionMode ------------------------------------------------
check('executionMode：Claude 完整 env＋一致落地檔 → scenario/claude（不再誤判 broken）', () => {
  resetEnv();
  const dir = mkRun('em-claude', claudeEnv(tmp)).dir;
  const m = determineExecutionMode(readRunEnv(), dir, log);
  assert.equal(m.kind, 'scenario', JSON.stringify(m));
  assert.equal(m.kind === 'scenario' ? m.provider : null, 'claude');
});
check('executionMode：Codex 案仍判 scenario/codex（既有契約不變）', () => {
  resetEnv();
  const dir = mkRun('em-codex', codexEnv(tmp)).dir;
  const m = determineExecutionMode(readRunEnv(), dir, log);
  assert.equal(m.kind, 'scenario', JSON.stringify(m));
  assert.equal(m.kind === 'scenario' ? m.provider : null, 'codex');
});
check('executionMode：Codex 案缺九欄之一仍 fail closed（未放寬）', () => {
  resetEnv();
  const e = codexEnv(tmp); delete (e as unknown as Record<string, unknown>).scenarioManifestPath;
  const dir = mkRun('em-codex-missing', e as RunEnv).dir;
  const m = determineExecutionMode(readRunEnv(), dir, log);
  assert.equal(m.kind, 'scenario-broken');
});
check('executionMode：Claude 案缺必要欄位 → scenario-broken', () => {
  resetEnv();
  const e = claudeEnv(tmp); delete (e as unknown as Record<string, unknown>).claudeApprovedCommandSha256;
  const dir = mkRun('em-claude-missing', e as RunEnv).dir;
  const m = determineExecutionMode(readRunEnv(), dir, log);
  assert.equal(m.kind, 'scenario-broken');
  assert.ok(m.kind === 'scenario-broken' && m.reason.includes('provider=claude'), m.kind === 'scenario-broken' ? m.reason : '');
});
check('executionMode：欄位型別錯誤（數字）必須 broken，不得 truthy 放行', () => {
  resetEnv();
  const dir = path.join(tmp, 'em-type');
  fs.mkdirSync(dir, { recursive: true });
  fs.writeFileSync(path.join(dir, 'execution-entry.json'), JSON.stringify({ entry: 'scenario' }));
  const e = { ...claudeEnv(tmp), artifactsDir: dir } as unknown as Record<string, unknown>;
  e.claudeApprovedCommandSha256 = 42;
  fs.writeFileSync(path.join(dir, 'run-env.json'), JSON.stringify(e));
  process.env.E2E_ARTIFACTS_DIR = dir; delete process.env.E2E_RUN_ID;
  const m = determineExecutionMode(readRunEnv(), dir, log);
  assert.equal(m.kind, 'scenario-broken');
});
check('executionMode：env 與檔案跨來源不一致必須 broken', () => {
  resetEnv();
  const dir = mkRun('em-cross', claudeEnv(tmp)).dir;
  const raw = JSON.parse(fs.readFileSync(path.join(dir, 'run-env.json'), 'utf8')) as Record<string, unknown>;
  raw.claudeStateDir = '/somewhere/else';
  fs.writeFileSync(path.join(dir, 'run-env.json'), JSON.stringify(raw));
  const m = determineExecutionMode(readRunEnv(), dir, log);
  assert.equal(m.kind, 'scenario-broken');
  assert.ok(m.kind === 'scenario-broken' && m.reason.includes('不一致'));
});
check('executionMode：未知 scenario 名稱必須 broken，**不得回退成 codex**', () => {
  resetEnv();
  const e = { ...claudeEnv(tmp), scenario: 'totally-unknown' };
  const dir = mkRun('em-unknown', e).dir;
  const m = determineExecutionMode(readRunEnv(), dir, log);
  assert.equal(m.kind, 'scenario-broken');
  assert.ok(m.kind === 'scenario-broken' && m.reason.includes('provider'), m.kind === 'scenario-broken' ? m.reason : '');
});
check('executionMode：scenario 名稱在 env 與檔案不一致必須 broken', () => {
  resetEnv();
  const dir = mkRun('em-name', claudeEnv(tmp)).dir;
  const raw = JSON.parse(fs.readFileSync(path.join(dir, 'run-env.json'), 'utf8')) as Record<string, unknown>;
  raw.scenario = 'commandExecution-allow';
  fs.writeFileSync(path.join(dir, 'run-env.json'), JSON.stringify(raw));
  const m = determineExecutionMode(readRunEnv(), dir, log);
  assert.equal(m.kind, 'scenario-broken');
});
check('executionMode：default 入口不受影響', () => {
  resetEnv();
  const dir = mkRun('em-default', codexEnv(tmp), 'default').dir;
  assert.equal(determineExecutionMode(readRunEnv(), dir, log).kind, 'default');
});

// --- app-ready 後失敗收尾：受控 stub 驗證「清理一定被嘗試」 -----------------
await acheck('收尾：正常情況下 log／setStatus／teardown 都被呼叫', async () => {
  const calls: string[] = [];
  const r = await handleClaudeSetupFailure(new Error('boom'), {
    log: { log: m => { calls.push(`log:${m.slice(0, 20)}`); } },
    runState: { setStatus: (s2, m) => { calls.push(`status:${s2}:${m.slice(0, 20)}`); } },
    teardown: async () => { calls.push('teardown'); },
    onDiagnostic: () => {},
  });
  assert.equal(r.teardownAttempted, true);
  assert.equal(r.teardownError, null);
  assert.ok(calls.includes('teardown'), JSON.stringify(calls));
  assert.ok(r.message.includes('boom'));
});
await acheck('收尾：**log.log 自己 throw 也不得跳過 teardown**', async () => {
  let teardownCalled = false;
  const r = await handleClaudeSetupFailure(new Error('boom'), {
    log: { log: () => { throw new Error('log broken'); } },
    runState: { setStatus: () => {} },
    teardown: async () => { teardownCalled = true; },
    onDiagnostic: () => {},
  });
  assert.ok(teardownCalled, 'teardown 必須仍被呼叫');
  assert.ok((r.logError ?? '').includes('log broken'));
  assert.equal(r.teardownAttempted, true);
});
await acheck('收尾：**setStatus 自己 throw 也不得跳過 teardown**', async () => {
  let teardownCalled = false;
  const r = await handleClaudeSetupFailure(new Error('boom'), {
    log: { log: () => {} },
    runState: { setStatus: () => { throw new Error('status broken'); } },
    teardown: async () => { teardownCalled = true; },
    onDiagnostic: () => {},
  });
  assert.ok(teardownCalled, 'teardown 必須仍被呼叫');
  assert.ok((r.statusError ?? '').includes('status broken'));
});
await acheck('收尾：teardown 自己失敗只記錄，不掩蓋原始錯誤', async () => {
  const r = await handleClaudeSetupFailure(new Error('original failure'), {
    log: { log: () => {} },
    runState: { setStatus: () => {} },
    teardown: async () => { throw new Error('cleanup broken'); },
    onDiagnostic: () => {},
  });
  assert.ok((r.teardownError ?? '').includes('cleanup broken'));
  assert.ok(r.message.includes('original failure'), '原始錯誤訊息必須保留');
  assert.equal(r.teardownAttempted, true);
});
await acheck('收尾：log 與 setStatus 同時壞掉，teardown 仍被嘗試', async () => {
  let teardownCalled = false;
  const r = await handleClaudeSetupFailure('plain string failure', {
    log: { log: () => { throw new Error('l'); } },
    runState: { setStatus: () => { throw new Error('s'); } },
    teardown: async () => { teardownCalled = true; },
    onDiagnostic: () => {},
  });
  assert.ok(teardownCalled);
  assert.equal(r.message, 'plain string failure');
});

resetEnv();
console.log(`\n${passed} passed, ${failures.length} failed`);
if (failures.length > 0) console.log(`failed: ${failures.join(' | ')}`);
