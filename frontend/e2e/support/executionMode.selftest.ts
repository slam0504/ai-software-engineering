// executionMode.ts 的純函式測試（B3a-2b-2 Task C 第三輪限縮補正，缺陷 1）。
//
// 重點是四個 reviewer 反例（都應判 scenario-broken，先前版本全部判錯）：
//   (a) file.scenarioThreadId='other-run'、env 維持原值。
//   (b) file 缺 scenarioApprovalRequestId、env 完整。
//   (c) 兩側 scenarioTurnId=42（數字）。
//   (d) 兩側 scenario=42（數字）——先前版本誤判成 default，這裡驗證不再誤判。
//
// 執行：node frontend/e2e/support/executionMode.selftest.ts
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import type { RunEnv } from './env.ts';
import { HarnessLogger } from './logger.ts';
import { determineExecutionMode } from './executionMode.ts';

let passed = 0;
let failed = 0;
function check(name: string, fn: () => void): void {
  try {
    fn();
    passed += 1;
    console.log(`ok - ${name}`);
  } catch (e) {
    failed += 1;
    console.error(`FAIL - ${name}`);
    console.error(e);
  }
}

function freshDir(): string {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'b3a2b2-execmode-selftest-'));
}

function baseEnv(overrides: Partial<RunEnv> = {}): RunEnv {
  return {
    runId: 'run1',
    artifactsDir: '(unused-in-test)',
    workspaceDir: '/ws',
    toolsDir: '/tools',
    glossaryPath: '/ws/glossary.md',
    claudeVersion: 'v1',
    codexVersion: 'v1',
    baseUrl: 'http://127.0.0.1:34115',
    ...overrides,
  };
}

const scenarioFields = {
  scenario: 'commandExecution-allow',
  scenarioThreadId: 'thread-run1',
  scenarioTurnId: 'turn-run1',
  scenarioItemId: 'item-run1',
  scenarioApprovalMethod: 'item/commandExecution/requestApproval',
  scenarioApprovalRequestId: 'approval-run1',
  scenarioDecision: 'accept',
  scenarioConfigPath: '/artifacts/scenario-config.json',
  scenarioLogPath: '/artifacts/scenario-wire.log',
  scenarioManifestPath: '/artifacts/manifest.json',
};

function writeEntry(dir: string, entry: unknown): void {
  fs.writeFileSync(path.join(dir, 'execution-entry.json'), JSON.stringify(entry, null, 2));
}
function writeRunEnvFile(dir: string, content: unknown): void {
  fs.writeFileSync(path.join(dir, 'run-env.json'), JSON.stringify(content, null, 2));
}

// --- 正控制：default 入口 ---
check('正控制：execution-entry.json entry=default 判定為 default（即使沒有 run-env.json）', () => {
  const dir = freshDir();
  writeEntry(dir, { entry: 'default' });
  const log = new HarnessLogger(dir);
  const mode = determineExecutionMode(baseEnv(), dir, log);
  assert.equal(mode.kind, 'default');
});

// --- 正控制：scenario 入口，env／file 完整一致 ---
check('正控制：scenario 入口、env／file 完整一致判定為 scenario', () => {
  const dir = freshDir();
  writeEntry(dir, { entry: 'scenario' });
  const env = baseEnv(scenarioFields);
  writeRunEnvFile(dir, env);
  const log = new HarnessLogger(dir);
  const mode = determineExecutionMode(env, dir, log);
  assert.equal(mode.kind, 'scenario', `expected scenario, got ${JSON.stringify(mode)}`);
});

// --- 反例 (a)：file.scenarioThreadId 與 env 不一致 ---
check('反例(a)：file 與 env 的 scenarioThreadId 不一致必須判 scenario-broken（不得誤判為 scenario）', () => {
  const dir = freshDir();
  writeEntry(dir, { entry: 'scenario' });
  const env = baseEnv(scenarioFields);
  writeRunEnvFile(dir, { ...env, scenarioThreadId: 'other-run' });
  const log = new HarnessLogger(dir);
  const mode = determineExecutionMode(env, dir, log);
  assert.equal(mode.kind, 'scenario-broken', `expected scenario-broken, got ${JSON.stringify(mode)}`);
});

// --- 反例 (b)：file 缺 scenarioApprovalRequestId，env 完整 ---
check('反例(b)：file 缺少一個 identity 欄位必須判 scenario-broken', () => {
  const dir = freshDir();
  writeEntry(dir, { entry: 'scenario' });
  const env = baseEnv(scenarioFields);
  const fileContent: Record<string, unknown> = { ...env };
  delete fileContent.scenarioApprovalRequestId;
  writeRunEnvFile(dir, fileContent);
  const log = new HarnessLogger(dir);
  const mode = determineExecutionMode(env, dir, log);
  assert.equal(mode.kind, 'scenario-broken', `expected scenario-broken, got ${JSON.stringify(mode)}`);
});

// --- 反例 (c)：兩側 scenarioTurnId 都是數字 42 ---
check('反例(c)：兩側 scenarioTurnId 皆為數字（非字串）必須判 scenario-broken', () => {
  const dir = freshDir();
  writeEntry(dir, { entry: 'scenario' });
  const env = baseEnv({ ...scenarioFields, scenarioTurnId: 42 as unknown as string });
  writeRunEnvFile(dir, { ...baseEnv(scenarioFields), scenarioTurnId: 42 });
  const log = new HarnessLogger(dir);
  const mode = determineExecutionMode(env, dir, log);
  assert.equal(mode.kind, 'scenario-broken', `expected scenario-broken, got ${JSON.stringify(mode)}`);
});

// --- 反例 (d)：兩側 scenario 都是數字 42——不得誤判為 default ---
check('反例(d)：兩側 scenario 皆為數字（非字串）必須判 scenario-broken，不得誤判為 default', () => {
  const dir = freshDir();
  writeEntry(dir, { entry: 'scenario' });
  const env = baseEnv({ ...scenarioFields, scenario: 42 as unknown as string });
  writeRunEnvFile(dir, { ...baseEnv(scenarioFields), scenario: 42 });
  const log = new HarnessLogger(dir);
  const mode = determineExecutionMode(env, dir, log);
  assert.notEqual(mode.kind, 'default', 'must not silently fall back to default when scenario identity is corrupted');
  assert.equal(mode.kind, 'scenario-broken', `expected scenario-broken, got ${JSON.stringify(mode)}`);
});

// --- 額外：execution-entry.json 缺失，不得默認為 default ---
check('額外：execution-entry.json 缺失必須判 scenario-broken（不得默認為 default）', () => {
  const dir = freshDir();
  const log = new HarnessLogger(dir);
  const mode = determineExecutionMode(baseEnv(), dir, log);
  assert.equal(mode.kind, 'scenario-broken', `expected scenario-broken, got ${JSON.stringify(mode)}`);
});

// --- 額外：run-env.json 缺失（scenario 入口但檔案没落地）---
check('額外：入口標記為 scenario 但 run-env.json 缺失必須判 scenario-broken', () => {
  const dir = freshDir();
  writeEntry(dir, { entry: 'scenario' });
  const env = baseEnv(scenarioFields);
  const log = new HarnessLogger(dir);
  const mode = determineExecutionMode(env, dir, log);
  assert.equal(mode.kind, 'scenario-broken', `expected scenario-broken, got ${JSON.stringify(mode)}`);
});

// --- 阻擋缺陷修正（B3a-2b-2 Task C）：JSON.parse 成功但不是 plain object ---
// `JSON.parse(...) as T` 只是型別斷言，`'null'`／`'[]'`／`'42'` 都是合法
// JSON、都不會讓 JSON.parse 丟例外。沒有 runtime validation 時，後續的
// property access 對 `null` 會直接拋出未捕捉的 TypeError（controller 實測
// 重現），讓呼叫端（global-teardown.ts）在還沒做完 bounded stop 之前就整個
// 中止。以下四項逐一驗證：無論 execution-entry.json 或 run-env.json 是
// null／array／primitive，一律回 scenario-broken，且呼叫本身不得拋出。

check('阻擋缺陷(1a)：execution-entry.json 落地內容為 JSON null 必須判 scenario-broken，不得拋出例外', () => {
  const dir = freshDir();
  writeEntry(dir, null);
  const env = baseEnv();
  const log = new HarnessLogger(dir);
  const mode = determineExecutionMode(env, dir, log); // 不應拋出
  assert.equal(mode.kind, 'scenario-broken', `expected scenario-broken, got ${JSON.stringify(mode)}`);
});

check('阻擋缺陷(1b)：execution-entry.json 落地內容為 array 必須判 scenario-broken', () => {
  const dir = freshDir();
  writeEntry(dir, []);
  const env = baseEnv();
  const log = new HarnessLogger(dir);
  const mode = determineExecutionMode(env, dir, log);
  assert.equal(mode.kind, 'scenario-broken', `expected scenario-broken, got ${JSON.stringify(mode)}`);
});

check('阻擋缺陷(1c)：execution-entry.json 落地內容為 primitive（數字）必須判 scenario-broken', () => {
  const dir = freshDir();
  writeEntry(dir, 42);
  const env = baseEnv();
  const log = new HarnessLogger(dir);
  const mode = determineExecutionMode(env, dir, log);
  assert.equal(mode.kind, 'scenario-broken', `expected scenario-broken, got ${JSON.stringify(mode)}`);
});

check('阻擋缺陷(1d)：入口標記為 scenario、run-env.json 落地內容為 JSON null 必須判 scenario-broken，不得拋出例外', () => {
  const dir = freshDir();
  writeEntry(dir, { entry: 'scenario' });
  writeRunEnvFile(dir, null);
  const env = baseEnv(scenarioFields);
  const log = new HarnessLogger(dir);
  const mode = determineExecutionMode(env, dir, log); // 不應拋出
  assert.equal(mode.kind, 'scenario-broken', `expected scenario-broken, got ${JSON.stringify(mode)}`);
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exitCode = 1;
