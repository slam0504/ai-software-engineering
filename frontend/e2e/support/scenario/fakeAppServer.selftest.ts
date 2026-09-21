// fakeAppServer.ts 的黑箱子程序測試（B3a-2b-1）。
//
// 執行：node frontend/e2e/support/scenario/fakeAppServer.selftest.ts
//
// 對齊既有慣例（同 artifactIntegrity.selftest.ts／networkLineParser.selftest.ts）：
// 不進 vitest 預設 suite，用 Node 原生 TS 支援直接執行、check()/assert 手動計數。
//
// 全程**不呼叫真 codex／claude**——只把自己剛啟動的 fakeAppServer.ts 子程序當黑箱：
// 用真正的 stdio pipe 送 wire frame 進去、讀它送出來的 frame，驗證行為；不是
// fake 自己跟自己在同一個 process 內對話（票面禁止的形狀）。
import assert from 'node:assert/strict';
import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import type { Frame, RawId, ScenarioConfig } from './protocol.ts';
import { splitFrames } from './protocol.ts';
import { parseManifest, parseRunLog } from './verify.ts';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const FAKE_BIN = path.join(__dirname, 'fakeAppServer.ts');

let passed = 0;
let failed = 0;
async function check(name: string, fn: () => Promise<void> | void): Promise<void> {
  try {
    await fn();
    passed += 1;
    console.log(`ok - ${name}`);
  } catch (e) {
    failed += 1;
    console.error(`FAIL - ${name}`);
    console.error(e);
  }
}

function baseConfig(overrides: Partial<ScenarioConfig> = {}): ScenarioConfig {
  return {
    scenario: 'selftest',
    threadId: 'thread-selftest-1',
    turnId: 'turn-selftest-1',
    itemId: 'item-selftest-1',
    approvalMethod: 'item/commandExecution/requestApproval',
    approvalRequestId: 'appr-selftest-1',
    afterApproval: [{ type: 'itemCompleted', text: 'done' }],
    turnStatus: 'completed',
    ...overrides,
  };
}

interface Harness {
  child: ChildProcessWithoutNullStreams;
  logPath: string;
  manifestPath: string;
  dir: string;
  send(f: Frame): void;
  readFrame(timeoutMs?: number): Promise<Frame>;
  waitExit(timeoutMs?: number): Promise<number | null>;
  cleanup(): Promise<void>;
}

function startHarness(cfg: ScenarioConfig | null, extraEnv: Record<string, string> = {}): Harness {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a2b1-scenario-fake-'));
  const logPath = path.join(dir, 'run.jsonl');
  const manifestPath = `${logPath}.manifest.json`;
  const env: Record<string, string> = { ...process.env } as Record<string, string>;
  if (cfg !== null) {
    const configPath = path.join(dir, 'scenario.json');
    fs.writeFileSync(configPath, JSON.stringify(cfg));
    env.SCENARIO_FAKE_CONFIG = configPath;
    env.SCENARIO_FAKE_LOG = logPath;
  }
  Object.assign(env, extraEnv);
  const child = spawn(FAKE_BIN, ['app-server'], { cwd: dir, env, stdio: ['pipe', 'pipe', 'pipe'] });
  let buf = '';
  const pendingFrames: Frame[] = [];
  const waiters: Array<(f: Frame) => void> = [];
  child.stdout.setEncoding('utf8');
  child.stdout.on('data', (chunk: string) => {
    buf += chunk;
    const { lines, rest } = splitFrames(buf);
    buf = rest;
    for (const line of lines) {
      const f = JSON.parse(line) as Frame;
      const w = waiters.shift();
      if (w) w(f);
      else pendingFrames.push(f);
    }
  });
  let stderrBuf = '';
  child.stderr.setEncoding('utf8');
  child.stderr.on('data', (c: string) => {
    stderrBuf += c;
  });

  return {
    child,
    logPath,
    manifestPath,
    dir,
    send(f: Frame) {
      child.stdin.write(JSON.stringify(f) + '\n');
    },
    readFrame(timeoutMs = 3000): Promise<Frame> {
      const existing = pendingFrames.shift();
      if (existing) return Promise.resolve(existing);
      return new Promise((resolve, reject) => {
        const t = setTimeout(() => {
          reject(new Error(`readFrame timeout after ${timeoutMs}ms (stderr=${stderrBuf})`));
        }, timeoutMs);
        waiters.push(f => {
          clearTimeout(t);
          resolve(f);
        });
      });
    },
    waitExit(timeoutMs = 3000): Promise<number | null> {
      if (child.exitCode !== null) return Promise.resolve(child.exitCode);
      return new Promise((resolve, reject) => {
        const t = setTimeout(() => reject(new Error(`waitExit timeout after ${timeoutMs}ms`)), timeoutMs);
        child.once('exit', code => {
          clearTimeout(t);
          resolve(code);
        });
      });
    },
    async cleanup() {
      // 清理自己起的 fake 子程序（票面要求 #4）：正常結束時已自行 exit，這裡
      // 對還活著的殘留一律 SIGKILL，不留孤兒；log／manifest 是暫存目錄檔案，
      // 保留給測試自己讀，不在這裡刪（selftest 進程結束後由 OS temp 回收，
      // 跟既有 artifactIntegrity.selftest.ts 的 tmpRepo 處理方式一致）。
      if (child.exitCode === null && child.signalCode === null) {
        child.kill('SIGKILL');
        await new Promise<void>(resolve => {
          child.once('exit', () => resolve());
          setTimeout(resolve, 1000);
        });
      }
    },
  };
}

async function driveHandshake(h: Harness): Promise<void> {
  h.send({ id: 1, method: 'initialize', params: { clientInfo: { name: 'selftest' } } });
  const initRes = await h.readFrame();
  assert.equal(initRes.id, 1);
  assert.equal(initRes.error, undefined);
  h.send({ method: 'initialized' });
}

async function driveThreadAndTurn(h: Harness, cfg: ScenarioConfig): Promise<void> {
  h.send({ id: 2, method: 'thread/start', params: { approvalPolicy: 'untrusted' } });
  const threadRes = await h.readFrame();
  assert.equal(threadRes.id, 2);
  assert.deepEqual(threadRes.result, { thread: { id: cfg.threadId } });

  h.send({ id: 3, method: 'turn/start', params: { threadId: cfg.threadId, input: [] } });
  const turnRes = await h.readFrame();
  assert.equal(turnRes.id, 3);
  assert.deepEqual(turnRes.result, { turn: { id: cfg.turnId, status: 'inProgress' } });
}

await check('handshake→thread/start→turn/start→approval request 依序抵達，id 對得上', async () => {
  const cfg = baseConfig();
  const h = startHarness(cfg);
  try {
    await driveHandshake(h);
    await driveThreadAndTurn(h, cfg);
    const appr = await h.readFrame();
    assert.equal(appr.method, cfg.approvalMethod);
    assert.equal(appr.id, cfg.approvalRequestId); // 型別保留：字串 id 原樣送達
    assert.equal(typeof appr.id, 'string');
    const params = appr.params as Record<string, unknown>;
    assert.equal(params.threadId, cfg.threadId);
    assert.equal(params.turnId, cfg.turnId);

    h.send({ id: appr.id as RawId, result: { decision: 'accept' } });
    const content = await h.readFrame();
    assert.equal(content.method, 'item/completed');
    const done = await h.readFrame();
    assert.equal(done.method, 'turn/completed');
    assert.deepEqual(done.params, { threadId: cfg.threadId, turn: { id: cfg.turnId, status: 'completed' } });

    const code = await h.waitExit();
    assert.equal(code, 0);
    const manifest = parseManifest(h.manifestPath);
    assert.equal(manifest.decisionReceived, 'accept');
    assert.equal(manifest.approvalMethod, cfg.approvalMethod);
    const log = parseRunLog(h.logPath);
    assert.ok(log.length >= 6, `expected at least 6 log entries, got ${log.length}`);
  } finally {
    await h.cleanup();
  }
});

await check('decline：decision 原樣記錄，turn 仍收斂', async () => {
  const cfg = baseConfig({ scenario: 'decline-selftest' });
  const h = startHarness(cfg);
  try {
    await driveHandshake(h);
    await driveThreadAndTurn(h, cfg);
    const appr = await h.readFrame();
    h.send({ id: appr.id as RawId, result: { decision: 'decline' } });
    await h.readFrame(); // item/completed
    const done = await h.readFrame();
    assert.equal(done.method, 'turn/completed');
    const code = await h.waitExit();
    assert.equal(code, 0);
    const manifest = parseManifest(h.manifestPath);
    assert.equal(manifest.decisionReceived, 'decline');
  } finally {
    await h.cleanup();
  }
});

await check('fileChange 核可方法：明確涵蓋第二種 approval method', async () => {
  const cfg = baseConfig({
    scenario: 'filechange-selftest',
    approvalMethod: 'item/fileChange/requestApproval',
    approvalRequestId: 'appr-fc-1',
  });
  const h = startHarness(cfg);
  try {
    await driveHandshake(h);
    await driveThreadAndTurn(h, cfg);
    const appr = await h.readFrame();
    assert.equal(appr.method, 'item/fileChange/requestApproval');
    h.send({ id: appr.id as RawId, result: { decision: 'accept' } });
    await h.readFrame();
    await h.readFrame();
    await h.waitExit();
    const manifest = parseManifest(h.manifestPath);
    assert.equal(manifest.approvalMethod, 'item/fileChange/requestApproval');
  } finally {
    await h.cleanup();
  }
});

await check('未知 method（handshake 前送錯誤方法）：fake 記錄後 fail，非 0 結束', async () => {
  const cfg = baseConfig({ scenario: 'unknown-method-selftest' });
  const h = startHarness(cfg);
  try {
    h.send({ id: 1, method: 'thread/start', params: {} }); // 跳過 initialize
    const code = await h.waitExit();
    assert.equal(code, 17);
    const manifest = parseManifest(h.manifestPath);
    assert.ok(manifest.unknownMethodsSeen.includes('thread/start'));
    assert.ok(manifest.fatalError && manifest.fatalError.length > 0);
  } finally {
    await h.cleanup();
  }
});

await check('未知 argv：拒絕並以 exit 17 結束（不進場）', async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a2b1-scenario-fake-argv-'));
  const child = spawn(FAKE_BIN, ['not-app-server'], { cwd: dir, stdio: ['ignore', 'ignore', 'pipe'] });
  const code: number | null = await new Promise(resolve => {
    child.once('exit', c => resolve(c));
  });
  assert.equal(code, 17);
});

await check('--version：固定格式、不進 server loop', async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a2b1-scenario-fake-version-'));
  const child = spawn(FAKE_BIN, ['--version'], {
    cwd: dir,
    env: { ...process.env, SCENARIO_FAKE_RUN_ID: 'selftest-run' } as NodeJS.ProcessEnv,
    stdio: ['ignore', 'pipe', 'pipe'],
  });
  let out = '';
  child.stdout.setEncoding('utf8');
  child.stdout.on('data', (c: string) => (out += c));
  const code: number | null = await new Promise(resolve => {
    child.once('exit', c => resolve(c));
  });
  assert.equal(code, 0);
  assert.equal(out.trim(), 'fake-codex-scenario 0.0.0-e2e+selftest-run');
});

await check('缺失設定：沒帶 SCENARIO_FAKE_CONFIG/LOG env 時 exit 17（不留半啟動狀態）', async () => {
  const h = startHarness(null);
  try {
    const code = await h.waitExit();
    assert.equal(code, 17);
  } finally {
    await h.cleanup();
  }
});

await check('malformed 設定：SCENARIO_FAKE_CONFIG 指向壞掉的 JSON 時 exit 17', async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a2b1-scenario-fake-badcfg-'));
  const configPath = path.join(dir, 'bad.json');
  fs.writeFileSync(configPath, '{not valid json');
  const logPath = path.join(dir, 'run.jsonl');
  const child = spawn(FAKE_BIN, ['app-server'], {
    cwd: dir,
    env: { ...process.env, SCENARIO_FAKE_CONFIG: configPath, SCENARIO_FAKE_LOG: logPath } as NodeJS.ProcessEnv,
    stdio: ['ignore', 'ignore', 'pipe'],
  });
  const code: number | null = await new Promise(resolve => {
    child.once('exit', c => resolve(c));
  });
  assert.equal(code, 17);
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exitCode = 1;
