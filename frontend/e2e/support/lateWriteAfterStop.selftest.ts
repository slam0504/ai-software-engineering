// late-write-after-stop 受控回歸（B3a-1 offline attempt-1 crash-after-pass
// 修正，2026-09-17）。
//
// 根因：`global-setup.ts` 的 `child.on('exit', ...)`（第 258 行一帶）會呼叫
// `HarnessLogger.log` 寫 harness.log；`stopProcedure.ts` 的
// `waitForDeathVerified` 先前只在「初次驗證」與「批次觀測本身失敗」這兩處
// 對 heldChild（我們自己持有的 root `ChildProcess`）優先信任 Node 自己的
// `exitCode`／`signalCode`，主要的每輪批次驗證迴圈卻沒有比照辦理，仍然拿
// `ps` 快照的結果來判斷 heldChild 死活。`ps`（另一支子行程即時查詢的核心
// 行程表）跟 Node 自己「這個 ChildProcess 有沒有 emit('exit')」是兩條完全
// 獨立、沒有同步保證的觀測路徑——ps 可能先回報「查無此 pid」，但 Node 對
// 這個直接子行程的 onexit／emit('exit') 回呼（會觸發上面那個會寫 log 的
// listener）還沒被事件迴圈處理到。一旦 `stopProcessGroup` 因此提早回報乾淨，
// globalTeardown 可能已經把整個證據目錄刪掉，那個遲到的 listener 才觸發，
// 對著已刪除的 harness.log 呼叫 `appendFileSync`，丟出未捕捉的 ENOENT，
// 整個命令非零結束（實際堆疊見
// frontend/e2e/.artifacts/1b-final-20260917T021716Z/item3-offline/
// attempt-1-crash-after-pass/stdout.log）。
//
// 修法（`support/stopProcedure.ts` 的 `waitForDeathVerified` 主迴圈）：跟
// `verifyInitialTargets` 用同一條規則——heldChild 這個特定 pid 永遠以
// `heldChildAlive()`（Node 自己的 `exitCode`／`signalCode`）為準，忽略這
// 一輪 `ps` 快照對它的判斷。不是新引入等待，也沒有放寬 TERM／KILL 名目上限
// （10s／5s）；只是把「這個 pid 何時算已確認死亡」的判斷來源換成保證不會
// 早於 exit listener 執行完畢的那一個。
//
// 本檔案涵蓋 reviewer 要求的四項受控回歸（皆為真實 assert，不是靠保留目錄
// 遮蔽缺陷）：
//   1／2（regressionDefaultMode）：用真實子行程＋真實 `child.on('exit')`
//        listener＋決定性造假的 `ps`，重現「stop 判定完成後 exit 回呼才
//        到達」的時序，**在預設會刪除證據目錄的分支**驗證沒有 late write、
//        沒有未捕捉例外、rc=0。
//   3（regressionKeepArtifacts）：同一個決定性時序，`E2E_KEEP_ARTIFACTS=1`
//        路徑（不刪除）下 harness.log 仍留有完整的最後一筆紀錄
//        （wails dev 行程結束那一行）。
//   4（regressionNeverConfirmed）：heldChild 的 `exitCode`／`signalCode`
//        永遠是 `null`（模擬「Node 始終沒收到確認」），驗證 TERM＋KILL
//        兩階段名目上限（10s／5s，本測項就是照實際時間等這 15 秒，不是
//        用縮短過的假上限）耗盡後 `clean=false`、目標仍列在
//        `residualPids`——對應 globalTeardown 既有的「不乾淨→FAILED→
//        保留證據目錄、非零結束」邏輯（那段既有邏輯本身這一輪不重跑，只
//        驗證餵給它的 `clean` 判定本身是誠實的「無法確認」）。
//
// 執行：
//   cd frontend && node --experimental-loader=./e2e/support/selftestJsToTsLoader.mjs \
//     e2e/support/lateWriteAfterStop.selftest.ts
import { ChildProcess, spawnSync } from 'node:child_process';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { HarnessLogger } from './logger.ts';
import { stopProcessGroup } from './stopProcedure.ts';
import type { TrackedProc } from './runState.ts';

let passed = 0;
async function check(name: string, fn: () => void | Promise<void>): Promise<void> {
  try {
    await fn();
    passed += 1;
    console.log(`ok - ${name}`);
  } catch (e) {
    console.error(`FAIL - ${name}`);
    console.error(e);
    process.exitCode = 1;
  }
}

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const frontendRoot = path.resolve(__dirname, '..', '..'); // .../frontend/e2e/support -> .../frontend
const scratchRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a1-latewrite-selftest-'));

function runFixture(mode: 'default' | 'keep'): { status: number | null; stdout: string; stderr: string; artifactsDir: string } {
  const artifactsDir = path.join(scratchRoot, `run-${mode}-${Date.now()}`);
  const fixturePath = 'e2e/support/__fixtures__/lateWriteHarness.ts';
  const loaderPath = './e2e/support/selftestJsToTsLoader.mjs';
  const result = spawnSync(process.execPath, [`--experimental-loader=${loaderPath}`, fixturePath], {
    cwd: frontendRoot,
    env: { ...process.env, LATE_WRITE_FIXTURE_ARTIFACTS_DIR: artifactsDir, LATE_WRITE_FIXTURE_MODE: mode },
    encoding: 'utf8',
    timeout: 30_000,
  });
  return { status: result.status, stdout: result.stdout ?? '', stderr: result.stderr ?? '', artifactsDir };
}

function fakeHeldChildAlwaysAlive(pid: number): ChildProcess {
  return { pid, exitCode: null, signalCode: null } as unknown as ChildProcess;
}

async function main(): Promise<void> {
  // ---- 1／2：預設自動刪除模式，重現時序＋驗證沒有 late write／未捕捉例外／rc=0 ----
  await check(
    '回歸 1／2：預設自動刪除模式下重現「stop 判定完成才到達 exit 回呼」的時序，沒有 late write、沒有未捕捉例外、rc=0',
    () => {
      const startTs = new Date().toISOString();
      const { status, stdout, stderr, artifactsDir } = runFixture('default');
      const endTs = new Date().toISOString();
      console.log(`  [active] regressionDefaultMode start=${startTs} end=${endTs}`);
      assert.equal(status, 0, `子行程應該正常結束（rc=0），實際 status=${status}\nstdout=${stdout}\nstderr=${stderr}`);
      assert.doesNotMatch(stderr, /ENOENT/, `不應該出現 ENOENT（late write 撞上已刪除的 harness.log）：${stderr}`);
      assert.doesNotMatch(stderr, /Uncaught/i, `不應該有未捕捉例外：${stderr}`);
      assert.match(stdout, /RESULT snapshotCalls=\d+ clean=true residualPids=/, `stopProcessGroup 應該最終判定 clean=true：${stdout}`);
      assert.equal(fs.existsSync(artifactsDir), false, '預設模式應該已經清除證據目錄');
    },
  );

  // ---- 3：E2E_KEEP_ARTIFACTS=1 路徑，harness.log 保留完整最後紀錄 ----
  await check(
    '回歸 3：E2E_KEEP_ARTIFACTS=1 路徑下 harness.log 仍保有 wails dev 行程結束那一行完整紀錄',
    () => {
      const startTs = new Date().toISOString();
      const { status, stdout, stderr, artifactsDir } = runFixture('keep');
      const endTs = new Date().toISOString();
      console.log(`  [active] regressionKeepArtifacts start=${startTs} end=${endTs}`);
      assert.equal(status, 0, `子行程應該正常結束（rc=0），實際 status=${status}\nstdout=${stdout}\nstderr=${stderr}`);
      assert.doesNotMatch(stderr, /ENOENT/, `不應該出現 ENOENT：${stderr}`);
      assert.equal(fs.existsSync(artifactsDir), true, 'keep 模式應該保留證據目錄');
      const harnessLogPath = path.join(artifactsDir, 'harness.log');
      assert.equal(fs.existsSync(harnessLogPath), true, 'harness.log 應該存在');
      const content = fs.readFileSync(harnessLogPath, 'utf8');
      assert.match(content, /wails dev 行程結束：exit code=/, `harness.log 應該留有完整的「wails dev 行程結束」紀錄：\n${content}`);
    },
  );

  // ---- 4：heldChild 永遠無法確認死亡 → TERM＋KILL 名目上限耗盡後 clean=false、非零結束 ----
  await check(
    '回歸 4：heldChild 始終無法確認死亡，TERM＋KILL 名目上限（10s＋5s）耗盡後 clean=false、residualPids 仍列著目標（對應 FAILED／保留證據）',
    async () => {
      const log = new HarnessLogger(fs.mkdtempSync(path.join(scratchRoot, 'never-confirmed-')));
      const heldChild = fakeHeldChildAlwaysAlive(999999);
      const tracked: TrackedProc[] = [
        { pid: 999999, ppid: 1, pgid: 999999, command: 'stuck-wails', startedAt: 'T', samePgid: true },
      ];
      const waitStartTs = new Date().toISOString();
      const waitStartMs = Date.now();
      const result = await stopProcessGroup(tracked, log, heldChild, {
        snapshot: () => [], // ps 這一輪永遠回報查無此 pid；heldChild 優先於 ps 的規則下這個結果應該被忽略。
        kill: () => { /* 模擬信號送出，heldChild 永遠不會真的回報死亡 */ },
      });
      const waitEndTs = new Date().toISOString();
      const elapsedMs = Date.now() - waitStartMs;
      console.log(`  [waiting] regressionNeverConfirmed start=${waitStartTs} end=${waitEndTs} elapsedMs=${elapsedMs}`);
      assert.equal(result.clean, false, 'heldChild 始終無法確認死亡，不能判定 clean=true');
      assert.deepEqual(result.residualPids, [999999], '目標應該仍列在 residualPids（無法確認＝保留證據的依據）');
      assert.equal(result.escalatedToKill, true, 'TERM 階段結束仍有殘存，應該升級 KILL');
      assert.ok(elapsedMs >= 14_500, `應該實際等滿 TERM(10s)＋KILL(5s) 名目上限，不能提前判定完成（實際 elapsedMs=${elapsedMs}）`);
    },
  );

  console.log(`\n${passed} 項通過`);
  if (process.exitCode) {
    console.error('有測項失敗');
  } else {
    console.log('全部通過');
  }
}

void main();
