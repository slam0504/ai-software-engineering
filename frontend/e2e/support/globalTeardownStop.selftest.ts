// I1 修正的小測試（reviewer 六次審查，2026-09-16）：global-teardown.ts 在
// readRunEnv() 失敗（ready 前中斷／啟動逾時等路徑，run-env.json 從未寫出）
// 時的收尾邏輯（`teardownWithoutEnv`，透過公開的 `globalTeardown` 進入）。
// 用 Node 原生 TS 直接執行（需要 --experimental-loader，因為
// global-teardown.ts 依賴的部分模組還有 constructor 參數屬性簡寫）：
//   node --experimental-loader=./e2e/support/selftestJsToTsLoader.mjs \
//     e2e/support/globalTeardownStop.selftest.ts
//
// 用真實的隔離 /tmp artifactsRoot＋真實的 run-state.json／.active-run.json
// 檔案（不是 fs mock），只有 `runtime.processTree` 用可控延遲的假物件替換
// ——這是既有的 DI 模式（跟 OfflineSandboxPaths／StopProcedureDeps 同一套
// 做法），不是對 fs 本身做 mock。
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import globalTeardown from '../global-teardown.ts';
import { runtime } from './runtime.ts';

let passed = 0;
function check(name: string, fn: () => void | Promise<void>): Promise<void> {
  return Promise.resolve()
    .then(fn)
    .then(() => {
      passed += 1;
      console.log(`ok - ${name}`);
    })
    .catch(e => {
      console.error(`FAIL - ${name}`);
      console.error(e);
      process.exitCode = 1;
    });
}

function freshArtifactsDir(): { artifactsRoot: string; artifactsDir: string } {
  const artifactsRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a1-teardown-selftest-'));
  const artifactsDir = path.join(artifactsRoot, 'fake-run');
  fs.mkdirSync(artifactsDir, { recursive: true });
  return { artifactsRoot, artifactsDir };
}

function writeRunState(artifactsDir: string, overrides: Record<string, unknown> = {}): void {
  fs.writeFileSync(path.join(artifactsDir, 'run-state.json'), JSON.stringify({
    runId: 'fake-run', rootPgid: 12345, status: 'interrupted', failureStage: 'interrupted: SIGINT',
    processes: [{ pid: 12345, ppid: 1, pgid: 12345, command: 'fake', startedAt: 'x', samePgid: true }],
    ports: { wails: 34115 }, observationFailures: [],
    ...overrides,
  }, null, 2));
}

function writePointer(artifactsRoot: string, artifactsDir: string): void {
  fs.writeFileSync(path.join(artifactsRoot, '.active-run.json'), JSON.stringify({
    runId: 'fake-run', artifactsDir, rootPid: 12345, rootPgid: 12345, rootCommand: 'fake', rootStartedAt: 'x',
  }, null, 2));
}

function resetRuntime(artifactsDir: string): void {
  runtime.log = null;
  runtime.processTree = null;
  runtime.networkSampler = null;
  runtime.runState = null;
  runtime.artifactsDir = artifactsDir;
  runtime.artifactsRoot = null;
  runtime.fixtureRoot = null;
  runtime.setupPid = null;
  runtime.artifactBaselineSnapshot = null;
  delete process.env.E2E_ARTIFACTS_DIR;
  delete process.env.E2E_RUN_ID;
}

function delayedStop(delayMs: number, result: Record<string, unknown>) {
  let calls = 0;
  let resolved = false;
  let inFlight: Promise<unknown> | null = null;
  return {
    get calls() { return calls; },
    get resolved() { return resolved; },
    // 刻意模仿真正 ProcessTree.stop() 的 single-flight 寫法：第二次呼叫
    // 拿到同一個 in-flight promise，不會讓底層停止邏輯重跑一次。
    stop: () => {
      if (inFlight) return inFlight;
      calls += 1;
      inFlight = new Promise(resolve => {
        setTimeout(() => { resolved = true; resolve(result); }, delayMs);
      });
      return inFlight;
    },
  };
}

async function main(): Promise<void> {
  // 測項一：stop 尚未完成時，globalTeardown 不得返回（I1 的核心重現／修正）。
  await check('stop 尚未完成時 globalTeardown 不得返回（真的等到 resolve 才繼續）', async () => {
    const { artifactsRoot, artifactsDir } = freshArtifactsDir();
    writeRunState(artifactsDir);
    resetRuntime(artifactsDir);
    const fake = delayedStop(300, {
      clean: true, residualPids: [], abandonedPids: [], unconfirmedPids: [],
      observationFailed: false, diagnosticWriteFailed: false, portsReleased: true, residualPorts: [],
    });
    runtime.processTree = fake as unknown as typeof runtime.processTree;
    const t0 = Date.now();
    await assert.rejects(globalTeardown());
    const elapsed = Date.now() - t0;
    assert.ok(fake.resolved, 'globalTeardown 返回時 stop() 應該已經 resolve');
    assert.ok(elapsed >= 300, `globalTeardown 應該至少等滿 300ms 才返回，實際 ${elapsed}ms`);
    assert.equal(fake.calls, 1, 'stop() 應該恰好被呼叫一次');
    fs.rmSync(artifactsRoot, { recursive: true, force: true });
  });

  // 測項二：重複中斷（globalTeardown 被呼叫兩次，模擬跟 signal handler
  // 併發觸發的情境）只會真的停止一次——因為兩次呼叫都拿到 runtime.processTree
  // 同一個 single-flight 物件，底層 stop() 只執行一次。
  await check('globalTeardown 併發呼叫兩次，底層 stop() 只真的執行一次', async () => {
    const { artifactsRoot, artifactsDir } = freshArtifactsDir();
    writeRunState(artifactsDir);
    resetRuntime(artifactsDir);
    const fake = delayedStop(150, {
      clean: true, residualPids: [], abandonedPids: [], unconfirmedPids: [],
      observationFailed: false, diagnosticWriteFailed: false, portsReleased: true, residualPorts: [],
    });
    runtime.processTree = fake as unknown as typeof runtime.processTree;
    const [r1, r2] = await Promise.allSettled([globalTeardown(), globalTeardown()]);
    assert.equal(r1.status, 'rejected');
    assert.equal(r2.status, 'rejected');
    assert.equal(fake.calls, 1, '兩次併發呼叫 globalTeardown，底層 stop() 應該只被呼叫一次（single-flight）');
    fs.rmSync(artifactsRoot, { recursive: true, force: true });
  });

  // 測項三 a：完成且乾淨（clean=true, portsReleased=true）→ pointer 應該被清除。
  await check('停止程序完成且乾淨 → .active-run.json 指標被清除', async () => {
    const { artifactsRoot, artifactsDir } = freshArtifactsDir();
    writeRunState(artifactsDir);
    writePointer(artifactsRoot, artifactsDir);
    resetRuntime(artifactsDir);
    const fake = delayedStop(10, {
      clean: true, residualPids: [], abandonedPids: [], unconfirmedPids: [],
      observationFailed: false, diagnosticWriteFailed: false, portsReleased: true, residualPorts: [],
    });
    runtime.processTree = fake as unknown as typeof runtime.processTree;
    await assert.rejects(globalTeardown(), /run interrupted/, 'env 從未寫出時一律非零結束，即使清理乾淨');
    const pointerPath = path.join(artifactsRoot, '.active-run.json');
    assert.equal(fs.existsSync(pointerPath), false, '清理乾淨後 pointer 應該被移除');
    fs.rmSync(artifactsRoot, { recursive: true, force: true });
  });

  // 測項三 b：未完成／不乾淨（clean=false）→ pointer 必須保留，不得清除。
  await check('停止程序不完整 → .active-run.json 指標保留，供下次前次殘留檢查接手', async () => {
    const { artifactsRoot, artifactsDir } = freshArtifactsDir();
    writeRunState(artifactsDir);
    writePointer(artifactsRoot, artifactsDir);
    resetRuntime(artifactsDir);
    const fake = delayedStop(10, {
      clean: false, residualPids: [12345], abandonedPids: [], unconfirmedPids: [],
      observationFailed: false, diagnosticWriteFailed: false, portsReleased: true, residualPorts: [],
    });
    runtime.processTree = fake as unknown as typeof runtime.processTree;
    await assert.rejects(globalTeardown(), /cleanup incomplete/, '清理不完整時錯誤訊息要包含 cleanup incomplete');
    const pointerPath = path.join(artifactsRoot, '.active-run.json');
    assert.equal(fs.existsSync(pointerPath), true, '清理不完整時 pointer 必須保留，不能被清除');
    fs.rmSync(artifactsRoot, { recursive: true, force: true });
  });

  // 測項四（需求 5，reviewer 複核 #52 修正）：真正預檢前完全沒 spawn過——
  // 沒有同行程 runtime.processTree（沒有活證據）、run-state.json 也不存在
  // （沒有落地證據），兩者都沒有才是「未曾 spawn」的結論，不虛構任何停止
  // 工作，正常 return、不拋錯。
  //
  // 修正紀錄：先前這裡即使 `runtime.processTree` 存在（代表有活的追蹤
  // 狀態）也照樣斷言 `stopCalled === false`——這其實把缺陷 A（`state`
  // 缺失就跳過停止）當成預期行為寫死在測試裡。真正「有 runtime 卻不呼叫
  // stop」是 bug，不是正確行為；這裡改成不設定 `runtime.processTree`
  // （代表真的沒有活證據），只驗證「兩者都沒有時不虛構停止工作」。有
  // runtime 的情境另外用下面的測項四-b 驗證「必須呼叫且等待 stop()」。
  await check('無 runtime、無 run-state.json、無 pointer（未取得本次程序追蹤紀錄及殘留指標）→ 本分支不執行停止，正常返回（不據此宣稱從未啟動）', async () => {
    const { artifactsRoot, artifactsDir } = freshArtifactsDir();
    // 刻意不寫 run-state.json，也不設定 runtime.processTree。
    resetRuntime(artifactsDir);
    await globalTeardown(); // 不應該拋錯。
    fs.rmSync(artifactsRoot, { recursive: true, force: true });
  });

  // 測項四-b（缺陷 A 核心重現，reviewer 複核 #52 `missing-state-live-runtime`
  // 隔離案例）：run-state.json 缺失，但 `runtime.processTree` 是活的
  // （代表這個行程還持有追蹤狀態、極可能還在跑）——不得因為 state 缺失就
  // 跳過停止與等待，必須真的呼叫並等到 `stop()` resolve；env 從未寫出，
  // 缺乏執行證據仍要非零結束。
  await check('run-state.json 缺失但有 live runtime.processTree → 仍必須 await stop()，且非零結束', async () => {
    const { artifactsRoot, artifactsDir } = freshArtifactsDir();
    // 刻意不寫 run-state.json：這是 reviewer 重現的關鍵條件——先前的實作
    // 會在這裡直接 return，`stop()` 連呼叫都不會被呼叫。
    resetRuntime(artifactsDir);
    const fake = delayedStop(50, {
      clean: true, residualPids: [], abandonedPids: [], unconfirmedPids: [],
      observationFailed: false, diagnosticWriteFailed: false, portsReleased: true, residualPorts: [],
    });
    runtime.processTree = fake as unknown as typeof runtime.processTree;
    writePointer(artifactsRoot, artifactsDir);
    const t0 = Date.now();
    await assert.rejects(globalTeardown(), /run failed before ready/);
    const elapsed = Date.now() - t0;
    assert.equal(fake.calls, 1, 'stop() 應該恰好被呼叫一次');
    assert.ok(fake.resolved, 'globalTeardown 返回前 stop() 應該已經 resolve');
    assert.ok(elapsed >= 50, `應該真的等滿 stop() 的延遲才返回，實際 ${elapsed}ms`);
    // stop() 回報乾淨，pointer 應該被清除（不因為 state 缺失就保留一個
    // 已經確認清乾淨的指標）。
    assert.equal(fs.existsSync(path.join(artifactsRoot, '.active-run.json')), false, 'stop() 回報乾淨時 pointer 應該被清除');
    fs.rmSync(artifactsRoot, { recursive: true, force: true });
  });

  // 測項五（需求 4）：沒有 runtime.processTree（跨行程）且 run-state.json
  // 損毀——不得直接相信檔案內容去 kill，必須非零結束、不動任何東西
  // （連 pointer 都不動，因為根本沒走到核對 pointer 那一步）。
  await check('run-state.json 損毀且無 runtime 可用 → 不嘗試 kill，直接非零結束，pointer 不動', async () => {
    const { artifactsRoot, artifactsDir } = freshArtifactsDir();
    fs.writeFileSync(path.join(artifactsDir, 'run-state.json'), '{ 這不是合法的 JSON');
    writePointer(artifactsRoot, artifactsDir);
    resetRuntime(artifactsDir);
    runtime.processTree = null;
    await assert.rejects(globalTeardown(), /無法安全核對身分/);
    assert.equal(fs.existsSync(path.join(artifactsRoot, '.active-run.json')), true, '壞 JSON 時 pointer 必須保留');
    fs.rmSync(artifactsRoot, { recursive: true, force: true });
  });

  // 測項六（缺陷 B 核心重現，reviewer 複核 #52 `empty-processes-no-runtime`
  // 隔離案例）：沒有 runtime.processTree、run-state.json 可以解析，但
  // `processes` 是空陣列——不得被 staleRun 的結構驗證放行、更不得被
  // `stopProcessGroup([])` 的「沒東西可停＝clean=true」蒙混過去。必須拒絕、
  // 保留 pointer、零信號（這裡用「stopProcessGroup 從未真的執行到會送信號
  // 的階段」間接驗證：pointer 保留＋錯誤訊息指出 processes 是空陣列）。
  await check('無 runtime、processes 空陣列 → 拒絕、保留 pointer、不清指標', async () => {
    const { artifactsRoot, artifactsDir } = freshArtifactsDir();
    writeRunState(artifactsDir, { processes: [] });
    writePointer(artifactsRoot, artifactsDir);
    resetRuntime(artifactsDir);
    runtime.processTree = null;
    await assert.rejects(globalTeardown(), /processes 是空陣列/);
    assert.equal(fs.existsSync(path.join(artifactsRoot, '.active-run.json')), true, 'processes 空陣列時 pointer 必須保留');
    fs.rmSync(artifactsRoot, { recursive: true, force: true });
  });

  // 測項七（缺陷 B）：pointer 與 state 對 root 的記錄不一致（rootPid 對不
  // 上）——staleRun 的一致性驗證要能擋下，不得被當成合法紀錄去 kill。
  await check('無 runtime、pointer 與 state 的 rootPid 不一致 → 拒絕、保留 pointer', async () => {
    const { artifactsRoot, artifactsDir } = freshArtifactsDir();
    writeRunState(artifactsDir); // root pid=12345
    fs.writeFileSync(path.join(artifactsRoot, '.active-run.json'), JSON.stringify({
      runId: 'fake-run', artifactsDir, rootPid: 99999, rootPgid: 99999, rootCommand: 'fake', rootStartedAt: 'x',
    }, null, 2));
    resetRuntime(artifactsDir);
    runtime.processTree = null;
    await assert.rejects(globalTeardown(), /找不到 pointer 記錄的 rootPid/);
    assert.equal(fs.existsSync(path.join(artifactsRoot, '.active-run.json')), true, 'rootPid 不一致時 pointer 必須保留');
    fs.rmSync(artifactsRoot, { recursive: true, force: true });
  });

  // 測項八（需求 4，legit fallback 完成路徑）：沒有 runtime.processTree，
  // pointer／state 結構與一致性都通過驗證，且追蹤的 pid 確實已經不存在
  // （stopProcessGroup 對「查不到」的 pid 視為已死，不送信號、trivially
  // clean）——只有程序與埠都乾淨時才清 pointer；env 仍然從未寫出，這次
  // 執行依然非零結束，但 cleanup 本身應該判定為乾淨、pointer 應該被清除。
  await check('無 runtime、合法 fallback 且程序與埠皆乾淨 → 清 pointer（僅在乾淨時清）', async () => {
    const { artifactsRoot, artifactsDir } = freshArtifactsDir();
    // 用一個現實中極不可能存在的 pid／pgid 當作「已死」的追蹤目標，
    // stopProcessGroup 對查不到的 pid 視為已死、trivially clean。
    const deadPid = 999999;
    writeRunState(artifactsDir, {
      rootPgid: deadPid,
      processes: [{ pid: deadPid, ppid: 1, pgid: deadPid, command: 'fake-nonexistent-proc', startedAt: 'x', samePgid: true }],
      ports: { wails: 65533 },
    });
    fs.writeFileSync(path.join(artifactsRoot, '.active-run.json'), JSON.stringify({
      runId: 'fake-run', artifactsDir, rootPid: deadPid, rootPgid: deadPid, rootCommand: 'fake-nonexistent-proc', rootStartedAt: 'x',
    }, null, 2));
    resetRuntime(artifactsDir);
    runtime.processTree = null;
    // writeRunState 預設 status='interrupted'，跟這裡要驗證的重點（合法
    // fallback 完成後只在乾淨時清 pointer）無關，訊息比對只認這個事實。
    await assert.rejects(globalTeardown(), /run interrupted/);
    assert.equal(fs.existsSync(path.join(artifactsRoot, '.active-run.json')), false, '程序與埠皆乾淨時 pointer 應該被清除');
    fs.rmSync(artifactsRoot, { recursive: true, force: true });
  });

  // codex-review54 複核（2026-09-16）：以下四項對應 reviewer 用真實
  // globalTeardown 公開入口重現的兩個問題。`withKillGuard` monkey-patch
  // `process.kill`（跟 reviewer 的 probe.mjs 同一種觀測手段），用來證明
  // 「沒有送任何信號」不是靠巧合（沒被呼叫到才剛好是 0），而是因為程式
  // 根本沒有進入 stopProcessGroup 那條會呼叫 process.kill 的路徑。
  async function withKillGuard<T>(fn: () => Promise<T>): Promise<{ result?: T; error?: unknown; signalCalls: number }> {
    let signalCalls = 0;
    const original = process.kill;
    process.kill = ((...args: Parameters<typeof process.kill>) => {
      signalCalls += 1;
      throw new Error(`selftest：本測項預期完全不會呼叫 process.kill，但收到 ${JSON.stringify(args)}`);
    }) as typeof process.kill;
    try {
      const result = await fn();
      return { result, signalCalls };
    } catch (error) {
      return { error, signalCalls };
    } finally {
      process.kill = original;
    }
  }

  // 問題 1（`missing-state-with-pointer` 隔離案例）：env 不可用、沒有同行程
  // runtime.processTree、run-state.json 也讀不到，但 .active-run.json 指標
  // 仍然存在——先前這裡會直接 return（當成「app 從未啟動，無須收尾」），
  // 完全沒理會仍存在的 pointer。修正後必須 fail closed：拒絕（非零）、
  // 保留 pointer（位元組不變）、不送任何信號。
  await check('（問題 1）state 缺失但 .active-run.json 指標仍存在 → fail closed：拒絕、pointer 位元組不變、不送信號', async () => {
    const { artifactsRoot, artifactsDir } = freshArtifactsDir();
    // 刻意不寫 run-state.json。
    writePointer(artifactsRoot, artifactsDir);
    resetRuntime(artifactsDir);
    runtime.processTree = null;
    const pointerPath = path.join(artifactsRoot, '.active-run.json');
    const pointerBefore = fs.readFileSync(pointerPath);
    const { error, signalCalls } = await withKillGuard(() => globalTeardown());
    assert.ok(error, 'state 缺失但 pointer 存在時必須 reject，不能正常 return');
    assert.match(String(error), /無法確認「從未啟動」/);
    assert.equal(signalCalls, 0, 'fail closed 過程不應呼叫 process.kill（不送任何信號）');
    const pointerAfter = fs.readFileSync(pointerPath);
    assert.ok(pointerBefore.equals(pointerAfter), 'pointer 內容必須維持不變（逐位元組比對，不只是存在）');
    fs.rmSync(artifactsRoot, { recursive: true, force: true });
  });

  // 問題 2a（`pointer-to-other-run` 隔離案例，env 不可用的跨行程 fallback）：
  // .active-run.json 指向另一個 run（run-b）、而且 run-b 自己的
  // run-state.json 是自洽的——`loadValidatedPreviousRunState` 原本只核對
  // pointer 跟「它自己指向的」state 是否自洽，不核對這個 pointer 是不是
  // 「當前這次 teardown（run-a）」的。修正後必須先核對歸屬，不符就拒絕，
  // 完全不去碰 run-b 的內容（不清 pointer、不送信號）。
  await check('（問題 2a）pointer 指向另一個自洽的 run（env 不可用，跨行程 fallback）→ 拒絕、pointer 位元組不變、不送信號', async () => {
    const { artifactsRoot, artifactsDir: dirA } = freshArtifactsDir(); // 當前 teardown 自己的 run（run-a）
    writeRunState(dirA, { runId: 'run-a' });
    const dirB = path.join(artifactsRoot, 'run-b');
    fs.mkdirSync(dirB, { recursive: true });
    writeRunState(dirB, { runId: 'run-b' }); // 另一個 run 自己的、自洽的 run-state.json
    fs.writeFileSync(path.join(artifactsRoot, '.active-run.json'), JSON.stringify({
      runId: 'run-b', artifactsDir: dirB, rootPid: 12345, rootPgid: 12345, rootCommand: 'fake', rootStartedAt: 'x',
    }, null, 2));
    resetRuntime(dirA);
    runtime.processTree = null;
    const pointerPath = path.join(artifactsRoot, '.active-run.json');
    const pointerBefore = fs.readFileSync(pointerPath);
    const { error, signalCalls } = await withKillGuard(() => globalTeardown());
    assert.ok(error, 'pointer 屬於另一個 run 時必須 reject');
    assert.match(String(error), /不屬於當前 teardown/);
    assert.equal(signalCalls, 0, '歸屬核對失敗時不應進入 stopProcessGroup，不送任何信號');
    const pointerAfter = fs.readFileSync(pointerPath);
    assert.ok(pointerBefore.equals(pointerAfter), 'pointer 內容必須維持不變（歸屬不符時不能清除或改動，逐位元組比對）');
    fs.rmSync(artifactsRoot, { recursive: true, force: true });
  });

  // 問題 2b：同一個問題的 env 存在入口（global-teardown.ts 約 77–81 行，
  // 主 globalTeardown 本體的純檔案分支）。跟 2a 是同一個缺陷的兩個進入點，
  // 兩邊都要各自核對歸屬，不能只修其中一邊。
  await check('（問題 2b）pointer 指向另一個自洽的 run（env 存在，主路徑純檔案分支）→ 拒絕、pointer 位元組不變、不送信號', async () => {
    const { artifactsRoot, artifactsDir: dirA } = freshArtifactsDir();
    const dirB = path.join(artifactsRoot, 'run-b');
    fs.mkdirSync(dirB, { recursive: true });
    writeRunState(dirB, { runId: 'run-b' });
    fs.writeFileSync(path.join(artifactsRoot, '.active-run.json'), JSON.stringify({
      runId: 'run-b', artifactsDir: dirB, rootPid: 12345, rootPgid: 12345, rootCommand: 'fake', rootStartedAt: 'x',
    }, null, 2));
    resetRuntime(dirA);
    runtime.processTree = null;
    runtime.networkSampler = null;
    process.env.E2E_RUN_ID = 'run-a';
    process.env.E2E_ARTIFACTS_DIR = dirA;
    process.env.E2E_WORKSPACE_DIR = dirA;
    process.env.E2E_TOOLS_DIR = dirA;
    process.env.E2E_GLOSSARY_PATH = path.join(dirA, 'glossary.md');
    process.env.E2E_CLAUDE_VERSION = 'x';
    process.env.E2E_CODEX_VERSION = 'x';
    process.env.E2E_BASE_URL = 'http://127.0.0.1:1';
    const pointerPath = path.join(artifactsRoot, '.active-run.json');
    const pointerBefore = fs.readFileSync(pointerPath);
    try {
      const { error, signalCalls } = await withKillGuard(() => globalTeardown());
      assert.ok(error, 'env 存在時的純檔案分支，pointer 屬於另一個 run 也必須 reject');
      assert.match(String(error), /不屬於當前 teardown/);
      assert.equal(signalCalls, 0, '歸屬核對失敗時不應進入 stopProcessGroup，不送任何信號');
    } finally {
      delete process.env.E2E_RUN_ID;
      delete process.env.E2E_ARTIFACTS_DIR;
      delete process.env.E2E_WORKSPACE_DIR;
      delete process.env.E2E_TOOLS_DIR;
      delete process.env.E2E_GLOSSARY_PATH;
      delete process.env.E2E_CLAUDE_VERSION;
      delete process.env.E2E_CODEX_VERSION;
      delete process.env.E2E_BASE_URL;
    }
    const pointerAfter = fs.readFileSync(pointerPath);
    assert.ok(pointerBefore.equals(pointerAfter), 'pointer 內容必須維持不變（逐位元組比對）');
    fs.rmSync(artifactsRoot, { recursive: true, force: true });
  });

  // canonical 化：pointer.artifactsDir 跟「當前 teardown 自己的」artifactsDir
  // 只差在 /var 與 /private/var 這種 symlink 等價形式時，不能被誤判成
  // 「屬於另一個 run」。macOS 下 os.tmpdir() 回傳 /var/folders/...，
  // fs.realpathSync 之後會變成 /private/var/folders/...，兩者字串不同但是
  // 同一個目錄——這裡刻意讓 pointer 記錄 realpath 之後的形式、runtime 沿用
  // 原本的形式，驗證歸屬核對不會被這種字串差異誤傷。
  await check('canonical 化：pointer.artifactsDir 與當前 artifactsDir 只差 /var 與 /private/var 形式 → 視為同一個 run，不誤判', async () => {
    const { artifactsRoot, artifactsDir } = freshArtifactsDir();
    writeRunState(artifactsDir);
    writePointer(artifactsRoot, artifactsDir);
    const pointerPath = path.join(artifactsRoot, '.active-run.json');
    const pointer = JSON.parse(fs.readFileSync(pointerPath, 'utf8')) as Record<string, unknown>;
    const realArtifactsDir = fs.realpathSync(artifactsDir);
    assert.notEqual(realArtifactsDir, artifactsDir, '前提：本機 tmp 目錄的 /var 與 /private/var 形式字串應該不同，否則這個測項沒有驗證到 canonical 化');
    pointer.artifactsDir = realArtifactsDir;
    fs.writeFileSync(pointerPath, JSON.stringify(pointer, null, 2));
    resetRuntime(artifactsDir); // runtime.artifactsDir 維持原本（/var 形式）的字串
    runtime.processTree = null;
    // 路徑等價、身分核對應該通過，走到既有的合法 fallback 判定（因為
    // writeRunState 預設 status='interrupted'，env 從未寫出，最終仍然非零
    // 結束，但那是既有判定邏輯，不是被本次新增的歸屬核對擋下）。
    await assert.rejects(globalTeardown(), /run interrupted/, '路徑等價時不應被歸屬核對擋下，應該走到既有的合法 fallback 判定');
    fs.rmSync(artifactsRoot, { recursive: true, force: true });
  });

  // 阻擋缺陷修正（B3a-2b-2 Task C）：舊版 `determineExecutionMode` 在
  // execution-entry.json 落地內容為 JSON `null` 時，對 `marker.entry` 的
  // property access 會直接拋出未捕捉的 TypeError；而舊版 global-teardown.ts
  // 在呼叫這個判定「之前」都還沒呼叫 `runtime.processTree.stop()`——兩個
  // 缺陷疊加，會讓已擁有的程序完全不會被停（stopCalls=0）就整個中止。
  // 這裡用真正的 `globalTeardown()` 公開入口（不是只測
  // `determineExecutionMode` 回傳什麼字串）＋可控的 stop stub，驗證修正後：
  //   (1) identity 壞掉時 stop() 仍然被呼叫且等它跑完；
  //   (2) 最終結果仍然是失敗（不是被吞掉變成 PASSED）。
  await check('（阻擋缺陷修正）execution-entry.json 為 JSON null（壞掉的 identity）→ stop() 仍被呼叫並等待完成，且最終結果為失敗', async () => {
    const { artifactsRoot, artifactsDir } = freshArtifactsDir();
    const toolsDir = path.join(artifactsRoot, 'tools');
    fs.mkdirSync(toolsDir, { recursive: true });
    fs.writeFileSync(path.join(toolsDir, 'invocations.log'), '');
    // workspaceDir 刻意跟 artifactsDir 分開：global-teardown.ts 第 4 節會對
    // workspaceDir 做 `fs.rmSync(..., { recursive: true })`，如果沿用
    // artifactsDir 會把整個證據目錄（含 harness.log）一起刪掉，後續
    // log.log() 就會因為目錄消失而 ENOENT——這是測試 fixture 設置的問題，
    // 不是待驗證的缺陷，需要分開避免污染斷言。
    const workspaceDir = path.join(artifactsRoot, 'workspace');
    fs.mkdirSync(workspaceDir, { recursive: true });
    writeRunState(artifactsDir, { status: 'ready', failureStage: undefined });
    // 壞掉的 identity：execution-entry.json 落地內容是合法 JSON，但值是
    // `null`，不是預期的物件——這是 controller 實測重現的具體反例。
    fs.writeFileSync(path.join(artifactsDir, 'execution-entry.json'), 'null');
    resetRuntime(artifactsDir);
    process.env.E2E_RUN_ID = 'fake-run';
    process.env.E2E_ARTIFACTS_DIR = artifactsDir;
    process.env.E2E_WORKSPACE_DIR = workspaceDir;
    process.env.E2E_TOOLS_DIR = toolsDir;
    process.env.E2E_GLOSSARY_PATH = path.join(workspaceDir, 'glossary.md');
    process.env.E2E_CLAUDE_VERSION = 'x';
    process.env.E2E_CODEX_VERSION = 'x';
    process.env.E2E_BASE_URL = 'http://127.0.0.1:1';
    const fake = delayedStop(30, {
      clean: true, residualPids: [], abandonedPids: [], unconfirmedPids: [],
      observationFailed: false, diagnosticWriteFailed: false, portsReleased: true, residualPorts: [],
    });
    runtime.processTree = fake as unknown as typeof runtime.processTree;
    try {
      const { error, signalCalls } = await withKillGuard(() => globalTeardown());
      assert.ok(error, '壞掉的 identity 不得讓 globalTeardown 判成 PASSED，必須 reject');
      assert.match(
        String(error),
        /tripwire violations.*scenario identity 判定失敗/,
        `最終失敗原因應包含 tripwire／scenario identity 判定失敗，實際：${String(error)}`,
      );
      assert.equal(signalCalls, 0, '這個測項不涉及純檔案 kill 路徑，不應呼叫 process.kill');
      assert.equal(fake.calls, 1, 'stop() 應該恰好被呼叫一次（壞掉的 identity 不得阻止已擁有程序的 bounded stop）');
      assert.ok(fake.resolved, 'globalTeardown 返回前 stop() 應該已經 resolve（真的等完，不是提前中止）');
    } finally {
      delete process.env.E2E_RUN_ID;
      delete process.env.E2E_ARTIFACTS_DIR;
      delete process.env.E2E_WORKSPACE_DIR;
      delete process.env.E2E_TOOLS_DIR;
      delete process.env.E2E_GLOSSARY_PATH;
      delete process.env.E2E_CLAUDE_VERSION;
      delete process.env.E2E_CODEX_VERSION;
      delete process.env.E2E_BASE_URL;
    }
    fs.rmSync(artifactsRoot, { recursive: true, force: true });
  });

  console.log(`\n共 ${passed} 項通過`);
}

await main();
