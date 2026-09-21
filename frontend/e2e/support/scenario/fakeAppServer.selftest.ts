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
import { spawn, type ChildProcess, type ChildProcessWithoutNullStreams } from 'node:child_process';
import { EventEmitter } from 'node:events';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import type { Frame, RawId, ScenarioConfig } from './protocol.ts';
import { splitFrames } from './protocol.ts';
import { parseManifest, parseRunLog } from './verify.ts';
import { judgeRecoveryManifest, judgeRecoverySequence } from './recoveryJudge.ts';

// waitForChildExit：C3 修正——共用的「等到 exit 或明確逾時失敗」邏輯。先前
// cleanup() 裡的 `setTimeout(resolve, 1000)` 會在逾時後照樣 resolve，把「子
// 程序其實還沒死」當成清理成功；本函式改成逾時就 reject（呼叫端必須處理，
// 不能被靜默吞掉），且會處理 spawn 過程本身的 error 事件。
function waitForChildExit(
  child: Pick<ChildProcess, 'exitCode' | 'signalCode' | 'once'>,
  timeoutMs: number,
): Promise<number | null> {
  if (child.exitCode !== null || child.signalCode !== null) {
    return Promise.resolve(child.exitCode);
  }
  return new Promise((resolve, reject) => {
    const t = setTimeout(() => {
      reject(new Error(`waitForChildExit: timed out after ${timeoutMs}ms waiting for exit`));
    }, timeoutMs);
    child.once('exit', code => {
      clearTimeout(t);
      resolve(code);
    });
    child.once('error', err => {
      clearTimeout(t);
      reject(err);
    });
  });
}

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
    threadMode: 'start',
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
      return waitForChildExit(child, timeoutMs);
    },
    async cleanup() {
      // 清理自己起的 fake 子程序（票面要求 #4）：正常結束時已自行 exit，這裡
      // 對還活著的殘留一律 SIGKILL，不留孤兒；log／manifest 是暫存目錄檔案，
      // 保留給測試自己讀，不在這裡刪（selftest 進程結束後由 OS temp 回收，
      // 跟既有 artifactIntegrity.selftest.ts 的 tmpRepo 處理方式一致）。
      //
      // C3 修正：先前 `setTimeout(resolve, 1000)` 在逾時後照樣 resolve，把
      // 「SIGKILL 送出但子程序其實還沒死」當成清理成功，check() 也不會看到
      // 任何錯誤。現在改用 waitForChildExit：逾時會 reject，讓這個
      // check() 明確回報 FAIL，而不是靜默過關；成功 exit 後再核對
      // exitCode／signalCode，確認真的是被我們的 SIGKILL 終止的。
      if (child.exitCode === null && child.signalCode === null) {
        child.kill('SIGKILL');
        const code = await waitForChildExit(child, 3000);
        if (child.signalCode === null && code === null) {
          throw new Error(
            `cleanup: fake child (pid=${child.pid}) reported exit but neither exitCode nor signalCode is set`,
          );
        }
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

await check('未知 method（approval response 階段送陌生 method 而非純 response）：fake 記錄後 fail', async () => {
  const cfg = baseConfig({ scenario: 'unknown-method-at-approval-selftest' });
  const h = startHarness(cfg);
  try {
    await driveHandshake(h);
    await driveThreadAndTurn(h, cfg);
    await h.readFrame(); // 消掉 approval request，不回它——改送一個帶 method 的陌生 frame
    h.send({ id: 999, method: 'thread/fork', params: {} });
    const code = await h.waitExit();
    assert.equal(code, 17);
    const manifest = parseManifest(h.manifestPath);
    assert.ok(manifest.unknownMethodsSeen.includes('thread/fork'));
    assert.ok(manifest.fatalError && manifest.fatalError.length > 0);
  } finally {
    await h.cleanup();
  }
});

await check('未知 argv：拒絕並以 exit 17 結束（不進場）', async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a2b1-scenario-fake-argv-'));
  const child = spawn(FAKE_BIN, ['not-app-server'], { cwd: dir, stdio: ['ignore', 'ignore', 'pipe'] });
  const code = await waitForChildExit(child, 3000);
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
  const code = await waitForChildExit(child, 3000);
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

await check('C4 負控制：SCENARIO_FAKE_LOG 可寫、只缺 SCENARIO_FAKE_CONFIG 時仍留下 failure manifest（不是 stderr-only）', async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a2b1-scenario-fake-noconfig-'));
  const logPath = path.join(dir, 'run.jsonl');
  const child = spawn(FAKE_BIN, ['app-server'], {
    cwd: dir,
    env: { ...process.env, SCENARIO_FAKE_LOG: logPath } as NodeJS.ProcessEnv,
    stdio: ['ignore', 'ignore', 'pipe'],
  });
  const code = await waitForChildExit(child, 3000);
  assert.equal(code, 17);
  const manifest = parseManifest(`${logPath}.manifest.json`);
  assert.equal(manifest.exitCode, 17);
  assert.ok(
    manifest.fatalError && manifest.fatalError.includes('SCENARIO_FAKE_CONFIG'),
    `fatalError=${manifest.fatalError}`,
  );
});

await check('C4 負控制：確實沒有可寫 log（SCENARIO_FAKE_LOG 也沒給）時 stderr＋非零、沒有 manifest 可讀', async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a2b1-scenario-fake-nolog-'));
  const child = spawn(FAKE_BIN, ['app-server'], {
    cwd: dir,
    env: { ...process.env, SCENARIO_FAKE_CONFIG: path.join(dir, 'scenario.json') } as NodeJS.ProcessEnv,
    stdio: ['ignore', 'ignore', 'pipe'],
  });
  let stderrBuf = '';
  child.stderr.setEncoding('utf8');
  child.stderr.on('data', (c: string) => (stderrBuf += c));
  const code = await waitForChildExit(child, 3000);
  assert.equal(code, 17);
  assert.ok(stderrBuf.includes('SCENARIO_FAKE_LOG'), `stderr=${stderrBuf}`);
  assert.throws(() => parseManifest(path.join(dir, 'run.jsonl.manifest.json')));
});

await check('C4 負控制：initialize／thread/start／turn/start 全用 id:null 時受控失敗，不得 rc0 通過 approval', async () => {
  const cfg = baseConfig({ scenario: 'null-request-id-selftest' });
  const h = startHarness(cfg);
  try {
    h.send({ id: null as unknown as RawId, method: 'initialize', params: { clientInfo: { name: 'selftest' } } });
    const code = await h.waitExit();
    assert.notEqual(code, 0, 'id:null on initialize must not be accepted (rc must not be 0)');
    const manifest = parseManifest(h.manifestPath);
    assert.notEqual(manifest.exitCode, 0);
    assert.ok(manifest.fatalError && manifest.fatalError.length > 0, `fatalError=${manifest.fatalError}`);
    assert.equal(manifest.decisionReceived, null);
  } finally {
    await h.cleanup();
  }
});

await check('malformed 設定：SCENARIO_FAKE_CONFIG 指向壞掉的 JSON 時 exit 17，且留下可解析的 manifest（R4）', async () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a2b1-scenario-fake-badcfg-'));
  const configPath = path.join(dir, 'bad.json');
  fs.writeFileSync(configPath, '{not valid json');
  const logPath = path.join(dir, 'run.jsonl');
  const child = spawn(FAKE_BIN, ['app-server'], {
    cwd: dir,
    env: { ...process.env, SCENARIO_FAKE_CONFIG: configPath, SCENARIO_FAKE_LOG: logPath } as NodeJS.ProcessEnv,
    stdio: ['ignore', 'ignore', 'pipe'],
  });
  const code = await waitForChildExit(child, 3000);
  assert.equal(code, 17);
  // R4 修正：先前 malformed config 整段走 fatalArgv，根本不建立 manifest；
  // 現在 log 路徑已提供時要留下可解析的 failure evidence。
  const manifest = parseManifest(`${logPath}.manifest.json`);
  assert.equal(manifest.exitCode, 17);
  assert.ok(manifest.fatalError && manifest.fatalError.includes('malformed'), `fatalError=${manifest.fatalError}`);
});

await check('bad-config-method：approvalMethod 不在白名單時 exit 17，協定從未開始也留下 manifest（R4）', async () => {
  const cfg = { ...baseConfig(), approvalMethod: 'not-approved/method' } as unknown as ScenarioConfig;
  const h = startHarness(cfg);
  try {
    const code = await h.waitExit();
    assert.equal(code, 17);
    const manifest = parseManifest(h.manifestPath);
    assert.ok(manifest.fatalError && manifest.fatalError.includes('approvalMethod'), `fatalError=${manifest.fatalError}`);
    // 白名單擋在協定開始前：approvalMethod 這個 manifest 欄位本身應仍是
    // null（表示從沒進到「送出 approval request」那一步）。
    assert.equal(manifest.approvalMethod, null);
  } finally {
    await h.cleanup();
  }
});

await check('bad-config-method：threadMode 缺漏或型別錯誤時 exit 17（R4）', async () => {
  const cfg = { ...baseConfig(), threadMode: 'bogus' } as unknown as ScenarioConfig;
  const h = startHarness(cfg);
  try {
    const code = await h.waitExit();
    assert.equal(code, 17);
    const manifest = parseManifest(h.manifestPath);
    assert.ok(manifest.fatalError && manifest.fatalError.includes('threadMode'), `fatalError=${manifest.fatalError}`);
  } finally {
    await h.cleanup();
  }
});

// --- R2 負向子程序案例：thread/resume 錯 ID／錯型別／錯順序都不得通過 ---

await check('R2 正向：threadMode=resume 且 client 送對的 threadId 時成功', async () => {
  const cfg = baseConfig({ scenario: 'resume-ok-selftest', threadMode: 'resume' });
  const h = startHarness(cfg);
  try {
    await driveHandshake(h);
    h.send({ id: 2, method: 'thread/resume', params: { threadId: cfg.threadId } });
    const threadRes = await h.readFrame();
    assert.deepEqual(threadRes.result, { thread: { id: cfg.threadId } });
    h.send({ id: 3, method: 'turn/start', params: { threadId: cfg.threadId, input: [] } });
    const turnRes = await h.readFrame();
    assert.deepEqual(turnRes.result, { turn: { id: cfg.turnId, status: 'inProgress' } });
    const appr = await h.readFrame();
    h.send({ id: appr.id as RawId, result: { decision: 'accept' } });
    await h.readFrame();
    await h.readFrame();
    const code = await h.waitExit();
    assert.equal(code, 0);
  } finally {
    await h.cleanup();
  }
});

await check('R2 負向：threadMode=resume 但 client 送錯 threadId（"WRONG"）時 fail，非 0 結束', async () => {
  const cfg = baseConfig({ scenario: 'resume-wrong-id-selftest', threadMode: 'resume' });
  const h = startHarness(cfg);
  try {
    await driveHandshake(h);
    h.send({ id: 2, method: 'thread/resume', params: { threadId: 'WRONG' } });
    const code = await h.waitExit();
    assert.equal(code, 17);
    const manifest = parseManifest(h.manifestPath);
    assert.ok(manifest.fatalError && manifest.fatalError.includes('threadId mismatch'), `fatalError=${manifest.fatalError}`);
  } finally {
    await h.cleanup();
  }
});

await check('R2 負向：threadMode=start 但 client 送 thread/resume（方法型別不符）時 fail', async () => {
  const cfg = baseConfig({ scenario: 'wrong-method-selftest', threadMode: 'start' });
  const h = startHarness(cfg);
  try {
    await driveHandshake(h);
    h.send({ id: 2, method: 'thread/resume', params: { threadId: cfg.threadId } });
    const code = await h.waitExit();
    assert.equal(code, 17);
    const manifest = parseManifest(h.manifestPath);
    assert.ok(manifest.unknownMethodsSeen.includes('thread/resume'));
  } finally {
    await h.cleanup();
  }
});

await check('R2 負向：secondTurn 存在但 threadMode=resume（#293 限定第一輪必須是 start）時 exit 17，handshake 之前就擋下，留下 failure manifest', async () => {
  const cfg = {
    ...baseConfig({ scenario: 'recovery-threadmode-resume-with-secondturn-selftest' }),
    threadMode: 'resume',
    secondTurn: {
      turnId: 'turn-r2-2',
      itemId: 'item-r2-2',
      approvalRequestId: 'appr-r2-2',
      afterApproval: [{ type: 'itemCompleted', text: 'second' }],
      turnStatus: 'completed',
    },
  } as unknown as ScenarioConfig;
  const h = startHarness(cfg);
  try {
    // 驗證「handshake 之前」就擋下：完全不送任何 c2s frame，直接等 exit。
    const code = await h.waitExit();
    assert.equal(code, 17);
    const manifest = parseManifest(h.manifestPath);
    assert.ok(
      manifest.fatalError && manifest.fatalError.includes('secondTurn') && manifest.fatalError.includes('threadMode'),
      `fatalError=${manifest.fatalError}`,
    );
    assert.equal(manifest.decisionReceived, null, 'protocol must never have started');
    assert.equal(manifest.secondTurn, null, 'secondTurn must never have been populated');
  } finally {
    await h.cleanup();
  }
});

await check('R2 正向：secondTurn 存在且 threadMode=start（合法組合）不受新檢查影響', async () => {
  const cfg = baseConfigWithSecondTurn({ scenario: 'recovery-threadmode-start-with-secondturn-selftest' });
  assert.equal(cfg.threadMode, 'start');
  const h = startHarness(cfg);
  try {
    await driveFirstRoundOnly(h, cfg);
    // 合法組合下 config 驗證必須放行，第一輪走完後 process 仍存活等第二輪
    // resume（不是被新檢查誤擋而提前 exit 17）——不呼叫 waitExit：它本來就
    // 不會 exit，呼叫只會製造逾時噪音，直接查 exitCode/signalCode 即可。
    await new Promise(resolve => setTimeout(resolve, 100));
    assert.equal(h.child.exitCode, null, 'process must still be alive after round 1 (not rejected by the new threadMode check)');
    assert.equal(h.child.signalCode, null);
  } finally {
    await h.cleanup();
  }
});

await check('R2 補強負控制：第二輪 thread/resume 帶不合法 frame 形狀（method+result 混雜）時 fail，非 0 結束（先前分支只查 method／id／threadId，不查形狀）', async () => {
  const cfg = baseConfigWithSecondTurn({ scenario: 'recovery-malformed-resume-shape-selftest' });
  const h = startHarness(cfg);
  try {
    await driveFirstRoundOnly(h, cfg);
    h.send({ id: 10, method: 'thread/resume', params: { threadId: cfg.threadId }, result: { thread: { id: cfg.threadId } } } as unknown as Frame);
    const code = await h.waitExit();
    assert.equal(code, 17);
    const manifest = parseManifest(h.manifestPath);
    assert.ok(
      manifest.fatalError && manifest.fatalError.includes('malformed') && manifest.fatalError.includes('thread/resume'),
      `fatalError=${manifest.fatalError}`,
    );
    // 第一輪結果不得被這個非法 frame 影響。
    assert.equal(manifest.decisionReceived, 'accept');
    assert.ok(manifest.secondTurn, 'secondTurn stub must exist once round 1 completed');
    assert.equal(manifest.secondTurn!.resumeAccepted, false, 'resume must not be marked accepted on a malformed frame');
  } finally {
    await h.cleanup();
  }
});

await check('R2 負向：turn/start 送錯 threadId（順序錯接）時 fail', async () => {
  const cfg = baseConfig({ scenario: 'wrong-turn-threadid-selftest' });
  const h = startHarness(cfg);
  try {
    await driveHandshake(h);
    h.send({ id: 2, method: 'thread/start', params: {} });
    await h.readFrame();
    h.send({ id: 3, method: 'turn/start', params: { threadId: 'WRONG', input: [] } });
    const code = await h.waitExit();
    assert.equal(code, 17);
    const manifest = parseManifest(h.manifestPath);
    assert.ok(manifest.fatalError && manifest.fatalError.includes('threadId mismatch'), `fatalError=${manifest.fatalError}`);
  } finally {
    await h.cleanup();
  }
});

// --- R3 負向子程序案例：result／error 互斥、error response、壞型別 frame ---

await check('R3 負向：approval response 同時帶 result 與 error 時 fail（互斥違規），非 0 結束', async () => {
  const cfg = baseConfig({ scenario: 'result-and-error-selftest' });
  const h = startHarness(cfg);
  try {
    await driveHandshake(h);
    await driveThreadAndTurn(h, cfg);
    const appr = await h.readFrame();
    h.send({ id: appr.id as RawId, result: { decision: 'accept' }, error: { code: -1, message: 'real error' } });
    const code = await h.waitExit();
    assert.equal(code, 17);
    const manifest = parseManifest(h.manifestPath);
    assert.ok(manifest.fatalError && manifest.fatalError.includes('mutual exclusivity'), `fatalError=${manifest.fatalError}`);
    assert.notEqual(manifest.decisionReceived, 'accept');
  } finally {
    await h.cleanup();
  }
});

await check('R3 負向：approval response 只帶 error 時視為未完成、非 0 結束（不得成功）', async () => {
  const cfg = baseConfig({ scenario: 'error-only-selftest' });
  const h = startHarness(cfg);
  try {
    await driveHandshake(h);
    await driveThreadAndTurn(h, cfg);
    const appr = await h.readFrame();
    h.send({ id: appr.id as RawId, error: { code: -32000, message: 'declined via error' } });
    const code = await h.waitExit();
    assert.equal(code, 17);
    const manifest = parseManifest(h.manifestPath);
    assert.equal(manifest.decisionReceived, null);
    assert.ok(manifest.fatalError && manifest.fatalError.length > 0);
  } finally {
    await h.cleanup();
  }
});

await check('R3 負向：approval response 送 null frame 時受控失敗，不是未捕捉例外（有 manifest 可診斷）', async () => {
  const cfg = baseConfig({ scenario: 'null-frame-selftest' });
  const h = startHarness(cfg);
  try {
    await driveHandshake(h);
    await driveThreadAndTurn(h, cfg);
    await h.readFrame(); // approval request
    h.child.stdin.write('null\n');
    const code = await h.waitExit();
    assert.equal(code, 17);
    const manifest = parseManifest(h.manifestPath);
    assert.ok(manifest.fatalError && manifest.fatalError.length > 0);
  } finally {
    await h.cleanup();
  }
});

await check('R3 負向：approval response 送 array frame 時受控失敗，不是未捕捉例外', async () => {
  const cfg = baseConfig({ scenario: 'array-frame-selftest' });
  const h = startHarness(cfg);
  try {
    await driveHandshake(h);
    await driveThreadAndTurn(h, cfg);
    await h.readFrame(); // approval request
    h.child.stdin.write('[1,2,3]\n');
    const code = await h.waitExit();
    assert.equal(code, 17);
    const manifest = parseManifest(h.manifestPath);
    assert.ok(manifest.fatalError && manifest.fatalError.length > 0);
  } finally {
    await h.cleanup();
  }
});

// --- R5 負向子程序案例：SIGTERM 在場景完成前送達，不得記成功 manifest ---

await check('R5 負向：initialize 完成後、stdin 保持開啟時送 SIGTERM，manifest 必須記為未完成（非 0、非 null fatalError）', async () => {
  const cfg = baseConfig({ scenario: 'sigterm-early-selftest' });
  const h = startHarness(cfg);
  try {
    await driveHandshake(h);
    // 刻意不關閉 stdin、不繼續協定，模擬 reviewer term205 反例：只送
    // SIGTERM，不觸發 stdin 'end' 事件。
    h.child.kill('SIGTERM');
    const code = await h.waitExit();
    assert.notEqual(code, 0, 'SIGTERM before scenario finished must not be exitCode 0');
    const manifest = parseManifest(h.manifestPath);
    assert.notEqual(manifest.exitCode, 0);
    assert.ok(manifest.fatalError && manifest.fatalError.includes('SIGTERM'), `fatalError=${manifest.fatalError}`);
    assert.equal(manifest.decisionReceived, null);
  } finally {
    await h.cleanup();
  }
});

// --- C3 控制：直接證明「逾時不會假成功」，不依賴真的殺不死的子程序 ---
// SIGKILL 本身不可攔截，真實子程序幾乎必然會死，沒辦法拿真 spawn 決定性地
// 逼出逾時分支。這裡用一個永不送出 'exit' 事件的假 EventEmitter 頂替
// ChildProcess，直接驗證 waitForChildExit／cleanup() 用到的逾時路徑真的會
// reject，而不是像修正前的 `setTimeout(resolve, 1000)` 那樣逾時後照樣成功。
await check('C3 控制：waitForChildExit 對永不 exit 的 child 必須逾時 reject，不能靜默 resolve', async () => {
  const neverExits = new EventEmitter() as unknown as Pick<ChildProcess, 'exitCode' | 'signalCode' | 'once'>;
  Object.assign(neverExits, { exitCode: null, signalCode: null });
  const start = Date.now();
  await assert.rejects(() => waitForChildExit(neverExits, 50), /timed out/);
  assert.ok(Date.now() - start >= 50, 'must actually wait out the timeout, not resolve early');
});

// =====================================================================
// Task E1：同一 fake 程序的兩輪 start→resume（secondTurn）
// =====================================================================
//
// judgeRecoverySequence／judgeRecoveryManifest：recovery 專屬的共用嚴格
// judge，定義在獨立的 recoveryJudge.ts（不是本檔內部函式）——刻意獨立於凍結
// 的 verify.ts#judgeApproval／scenarioProtocolJudge.ts#judgeFullProtocol，
// 兩者都是「單輪」判定，拿來對同一份雙輪 log 各套一次會漏掉中間的
// thread/resume 銜接與第二輪身分（task 說明 #9：不得「切成兩段各套一次舊
// judge」）。fake 的正控制與未來 E2 的 browser spec 都應該 import 這裡。

function baseConfigWithSecondTurn(overrides: Partial<ScenarioConfig> = {}): ScenarioConfig {
  return baseConfig({
    scenario: 'recovery-selftest',
    secondTurn: {
      turnId: 'turn-selftest-2',
      itemId: 'item-selftest-2',
      approvalRequestId: 'appr-selftest-2',
      afterApproval: [{ type: 'itemCompleted', text: 'second-round-done' }],
      turnStatus: 'completed',
    },
    ...overrides,
  });
}

async function driveFirstRoundOnly(h: Harness, cfg: ScenarioConfig): Promise<void> {
  await driveHandshake(h);
  await driveThreadAndTurn(h, cfg);
  const appr = await h.readFrame();
  assert.equal(appr.id, cfg.approvalRequestId);
  h.send({ id: appr.id as RawId, result: { decision: 'accept' } });
  await h.readFrame(); // item/completed（round 1 的 afterApproval）
  const done = await h.readFrame();
  assert.equal(done.method, 'turn/completed');
  assert.deepEqual(done.params, { threadId: cfg.threadId, turn: { id: cfg.turnId, status: 'completed' } });
}

await check(
  '真 child process 正控制：第一輪完成後仍 alive、manifest 尚未落地；第二輪 resume 完成才收尾（exit 0）',
  async () => {
    const cfg = baseConfigWithSecondTurn();
    const h = startHarness(cfg);
    try {
      await driveFirstRoundOnly(h, cfg);

      // 正控制核心斷言：第一輪完成後、第二輪 resume 送出前，process 仍 alive
      // 且 manifest 尚未落地（writeManifest 只在 finish() 內呼叫，中途完全沒
      // 有 manifest 檔——不是「有 manifest 但標未完成」，是根本不存在）。
      await new Promise(resolve => setTimeout(resolve, 150));
      assert.equal(h.child.exitCode, null, 'child must still be alive after round 1 turn/completed');
      assert.equal(h.child.signalCode, null, 'child must still be alive after round 1 turn/completed');
      assert.equal(fs.existsSync(h.manifestPath), false, 'manifest must not exist before second-round resume completes');

      h.send({ id: 10, method: 'thread/resume', params: { threadId: cfg.threadId } });
      const resumeRes = await h.readFrame();
      assert.deepEqual(resumeRes.result, { thread: { id: cfg.threadId } });

      h.send({ id: 11, method: 'turn/start', params: { threadId: cfg.threadId, input: [] } });
      const turnRes2 = await h.readFrame();
      assert.deepEqual(turnRes2.result, { turn: { id: cfg.secondTurn!.turnId, status: 'inProgress' } });

      const appr2 = await h.readFrame();
      assert.equal(appr2.id, cfg.secondTurn!.approvalRequestId);
      assert.notEqual(appr2.id, cfg.approvalRequestId, 'second-round approvalRequestId must differ from first round');
      const params2 = appr2.params as Record<string, unknown>;
      assert.equal(params2.turnId, cfg.secondTurn!.turnId);
      assert.equal(params2.itemId, cfg.secondTurn!.itemId);
      assert.notEqual(params2.turnId, cfg.turnId, 'second-round turnId must differ from first round');
      assert.notEqual(params2.itemId, cfg.itemId, 'second-round itemId must differ from first round');

      h.send({ id: appr2.id as RawId, result: { decision: 'accept' } });
      const content2 = await h.readFrame();
      assert.equal(content2.method, 'item/completed');
      const done2 = await h.readFrame();
      assert.equal(done2.method, 'turn/completed');
      assert.deepEqual(done2.params, { threadId: cfg.threadId, turn: { id: cfg.secondTurn!.turnId, status: 'completed' } });

      const code = await h.waitExit();
      assert.equal(code, 0);

      const manifest = parseManifest(h.manifestPath);
      // manifest 層核對不再手寫 assert，改走共用的 judgeRecoveryManifest——
      // 第一輪必須留在頂層欄位（協定契約 #8），第二輪落在獨立的 secondTurn
      // 子物件、resumeAccepted 必須是 true、兩輪 approvalRequestId 不得相同。
      const manifestViolations = judgeRecoveryManifest(manifest, {
        approvalMethod: cfg.approvalMethod,
        round1: { approvalRequestId: cfg.approvalRequestId, decision: 'accept' },
        round2: { approvalRequestId: cfg.secondTurn!.approvalRequestId, decision: 'accept' },
      });
      assert.deepEqual(manifestViolations, [], `judgeRecoveryManifest must accept a genuine two-round run, got: ${JSON.stringify(manifestViolations)}`);

      const log = parseRunLog(h.logPath);
      const violations = judgeRecoverySequence(log, {
        threadId: cfg.threadId,
        approvalMethod: cfg.approvalMethod,
        round1: {
          turnId: cfg.turnId,
          itemId: cfg.itemId,
          approvalRequestId: cfg.approvalRequestId,
          decision: 'accept',
          afterApproval: cfg.afterApproval,
          turnStatus: cfg.turnStatus,
        },
        round2: {
          turnId: cfg.secondTurn!.turnId,
          itemId: cfg.secondTurn!.itemId,
          approvalRequestId: cfg.secondTurn!.approvalRequestId,
          decision: 'accept',
          afterApproval: cfg.secondTurn!.afterApproval,
          turnStatus: cfg.secondTurn!.turnStatus,
        },
      });
      assert.deepEqual(violations, [], `judgeRecoverySequence must accept a genuine two-round run, got: ${JSON.stringify(violations)}`);
    } finally {
      await h.cleanup();
    }
  },
);

await check('負控制：第二輪 thread/resume 送錯 threadId 時 fail，非 0 結束（driver 寫錯參數，fake 自己的協定守門擋下）', async () => {
  const cfg = baseConfigWithSecondTurn({ scenario: 'recovery-wrong-resume-id-selftest' });
  const h = startHarness(cfg);
  try {
    await driveFirstRoundOnly(h, cfg);
    h.send({ id: 10, method: 'thread/resume', params: { threadId: 'WRONG-THREAD-ID' } });
    const code = await h.waitExit();
    assert.equal(code, 17);
    const manifest = parseManifest(h.manifestPath);
    assert.ok(
      manifest.fatalError && manifest.fatalError.includes('second') && manifest.fatalError.includes('threadId mismatch'),
      `fatalError=${manifest.fatalError}`,
    );
    // 第一輪結果保持不變（沒被第二輪的失敗抹掉）。secondTurn 子物件在轉入
    // 'awaitSecondThreadResume' 時就會被建立（stub），但 resumeAccepted 與
    // decisionReceived 必須仍是初始值——證明第二輪從未真正被接受，不是被
    // 「resume 失敗」偷偷標記成功。
    assert.equal(manifest.decisionReceived, 'accept');
    assert.ok(manifest.secondTurn, 'secondTurn stub must exist once round 1 completed');
    assert.equal(manifest.secondTurn!.resumeAccepted, false, 'resume must not be marked accepted on a rejected id');
    assert.equal(manifest.secondTurn!.decisionReceived, null, 'round 2 approval must never have been reached');
  } finally {
    await h.cleanup();
  }
});

await check('負控制：第二輪送成 thread/start（而非 thread/resume）時 fail，非 0 結束', async () => {
  const cfg = baseConfigWithSecondTurn({ scenario: 'recovery-wrong-method-selftest' });
  const h = startHarness(cfg);
  try {
    await driveFirstRoundOnly(h, cfg);
    h.send({ id: 10, method: 'thread/start', params: {} });
    const code = await h.waitExit();
    assert.equal(code, 17);
    const manifest = parseManifest(h.manifestPath);
    assert.ok(manifest.unknownMethodsSeen.includes('thread/start'));
    assert.ok(manifest.fatalError && manifest.fatalError.includes('thread/resume'), `fatalError=${manifest.fatalError}`);
  } finally {
    await h.cleanup();
  }
});

await check('負控制：第二輪被截斷（round1 完成後 stdin 直接關閉，不送 resume）時 fail，非 0 結束（不得把未完成寫成 success）', async () => {
  const cfg = baseConfigWithSecondTurn({ scenario: 'recovery-truncated-second-turn-selftest' });
  const h = startHarness(cfg);
  try {
    await driveFirstRoundOnly(h, cfg);
    h.child.stdin.end();
    const code = await h.waitExit();
    assert.equal(code, 17);
    const manifest = parseManifest(h.manifestPath);
    assert.notEqual(manifest.exitCode, 0);
    assert.ok(
      manifest.fatalError && manifest.fatalError.includes('stdin closed before scenario finished'),
      `fatalError=${manifest.fatalError}`,
    );
    assert.ok(manifest.fatalError!.includes('awaitSecondThreadResume'), `fatalError=${manifest.fatalError}`);
    // 第一輪已經是 accept，不得被第二輪的失敗覆寫。
    assert.equal(manifest.decisionReceived, 'accept');
  } finally {
    await h.cleanup();
  }
});

await check('C4 型別負控制：secondTurn 為 null（顯式）時 exit 17，config 驗證階段就擋下（單輪路徑不受影響，因為缺 key 才是合法單輪）', async () => {
  const cfg = { ...baseConfig({ scenario: 'recovery-null-secondturn-selftest' }), secondTurn: null } as unknown as ScenarioConfig;
  const h = startHarness(cfg);
  try {
    const code = await h.waitExit();
    assert.equal(code, 17);
    const manifest = parseManifest(h.manifestPath);
    assert.ok(manifest.fatalError && manifest.fatalError.includes('secondTurn'), `fatalError=${manifest.fatalError}`);
    assert.equal(manifest.decisionReceived, null, 'protocol must never have started');
  } finally {
    await h.cleanup();
  }
});

await check('C4 型別負控制：secondTurn 為 array 時 exit 17', async () => {
  const cfg = { ...baseConfig({ scenario: 'recovery-array-secondturn-selftest' }), secondTurn: [] } as unknown as ScenarioConfig;
  const h = startHarness(cfg);
  try {
    const code = await h.waitExit();
    assert.equal(code, 17);
    const manifest = parseManifest(h.manifestPath);
    assert.ok(manifest.fatalError && manifest.fatalError.includes('secondTurn'), `fatalError=${manifest.fatalError}`);
  } finally {
    await h.cleanup();
  }
});

await check('C4 型別負控制：secondTurn 缺 turnId 欄位時 exit 17', async () => {
  const cfg = {
    ...baseConfig({ scenario: 'recovery-missing-field-secondturn-selftest' }),
    secondTurn: { itemId: 'item-2', approvalRequestId: 'appr-2', afterApproval: [], turnStatus: 'completed' },
  } as unknown as ScenarioConfig;
  const h = startHarness(cfg);
  try {
    const code = await h.waitExit();
    assert.equal(code, 17);
    const manifest = parseManifest(h.manifestPath);
    assert.ok(manifest.fatalError && manifest.fatalError.includes('secondTurn.turnId'), `fatalError=${manifest.fatalError}`);
  } finally {
    await h.cleanup();
  }
});

await check('C4 型別負控制：secondTurn 與第一輪重複 ID（turnId 相同）時 exit 17（協定契約 #4：不得沿用第一輪 ID）', async () => {
  const base = baseConfig({ scenario: 'recovery-duplicate-turnid-selftest' });
  const cfg = {
    ...base,
    secondTurn: {
      turnId: base.turnId, // 刻意跟第一輪相同
      itemId: 'item-selftest-2',
      approvalRequestId: 'appr-selftest-2',
      afterApproval: [{ type: 'itemCompleted', text: 'second' }],
      turnStatus: 'completed',
    },
  } as unknown as ScenarioConfig;
  const h = startHarness(cfg);
  try {
    const code = await h.waitExit();
    assert.equal(code, 17);
    const manifest = parseManifest(h.manifestPath);
    assert.ok(
      manifest.fatalError && manifest.fatalError.includes('must differ from the first-turn turnId'),
      `fatalError=${manifest.fatalError}`,
    );
  } finally {
    await h.cleanup();
  }
});

await check('C1 正控制：缺省 secondTurn 時單輪路徑完全不變（manifest.secondTurn 明確為 null，不是省略）', async () => {
  const cfg = baseConfig({ scenario: 'recovery-default-singleturn-selftest' });
  const h = startHarness(cfg);
  try {
    await driveFirstRoundOnly(h, cfg);
    const code = await h.waitExit();
    assert.equal(code, 0, 'single-turn scenario (no secondTurn) must finish exactly as before');
    const manifest = parseManifest(h.manifestPath);
    assert.equal(manifest.secondTurn, null);
    // 單輪路徑也走同一套共用 judge（round2: null），不是只有雙輪案例才驗。
    const manifestViolations = judgeRecoveryManifest(manifest, {
      approvalMethod: cfg.approvalMethod,
      round1: { approvalRequestId: cfg.approvalRequestId, decision: 'accept' },
      round2: null,
    });
    assert.deepEqual(manifestViolations, [], `judgeRecoveryManifest must accept a genuine single-turn run, got: ${JSON.stringify(manifestViolations)}`);
  } finally {
    await h.cleanup();
  }
});

// judgeRecoverySequence／judgeRecoveryManifest 自身的正／負控制（含 R1 反例
// (a)(b)(c)(d)、錯 ID 型別、截斷／錯序）已搬到獨立的
// recoveryJudge.selftest.ts（授權範圍要求「新增 recovery judge 模組與其
// selftest」）；本檔只保留「真的啟動 fakeAppServer.ts 子程序」的黑箱測試。

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exitCode = 1;
