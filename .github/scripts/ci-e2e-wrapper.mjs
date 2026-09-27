#!/usr/bin/env node
// ci-e2e-wrapper.mjs — B3a-CI-1。沿用 PR #12（`ci/b3a-e2e-feasibility`,
// 79819d8）codex-reviewer #78/#82/#84 四輪反例修法後的薄 wrapper 機制（decision470
// 收斂實作選擇：「沿用PR12已審過的機制並修已知缺陷，不整包照抄過期保證」——
// decision470 對這支 wrapper 本身沒有另外指出新缺陷，因此核心演算法原樣保留，
// 只精簡歷史複核註解，行為與介面不變）。
//
// 設計摘要（完整歷史見 PR #12 diff，本輪唯讀參考、不整包照抄）：
//   - 不用 `ps` 核對子行程身分；直接持有 spawn() 回傳的 ChildProcess handle，
//     只對這個 handle 呼叫 `.kill()`，送之前先檢查 'exit' 事件是否已發生。
//   - deadline／grace 用 setTimeout，子行程「真的結束」只信任 'exit' 事件。
//   - watchdog（deadline+grace+buffer）保證 wrapper 自己不會無限期不退出；
//     觸發時**不**送任何訊號給子行程，只負責寫最後一份 status 並結束。
//   - STATUS_FILE／OUT_FILE／TS_OUT_FILE／RC_FILE 只支援一般檔案（或尚不
//     存在、即將建立的一般檔案）；FIFO／socket／裝置／目錄／符號連結在
//     spawn child 之前一律拒絕，不嘗試支援。
//   - childRc（子行程自己的 exit code）與 wrapperRc（wrapper 最終 exit code）
//     分成兩個欄位，status 決定 wrapperRc 的對照表固定，不會有「status 已
//     判定 timeout 但 rc 卻沿用子行程 0」這種脫鉤。
//   - RC_FILE 先試寫，失敗就把 status 升級成 producer-error 並用新值重試；
//     STATUS_FILE 留到最後才寫，內容一定反映最終判定。
//
// 用法：
//   node ci-e2e-wrapper.mjs <status-json> <out-file> <ts-out-file> <rc-file> \
//     -- <command...>
//
// 環境變數：
//   E2E_WRAPPER_DEADLINE_SECONDS（預設 1500＝25 分鐘，decision470 收斂實作選擇 #3）
//   E2E_WRAPPER_GRACE_SECONDS（預設 120）
//   E2E_WRAPPER_WATCHDOG_BUFFER_SECONDS（預設 30）
//
// 已知限制（照實記錄，不誇大——沿用 PR #12 的既有邊界）：
//   - watchdog 觸發時子行程可能仍在背景存活；不送 SIGKILL，也不再送
//     SIGTERM。子行程樹的最終清理靠 CI 平台自己的 job/runner 層級回收。
//   - 這支 wrapper 只追蹤自己直接 spawn 的那個子行程本身；後代行程樹
//     （wails dev／Chrome／vite）的清理是 frontend/e2e/global-teardown.ts
//     的 stopProcedure 負責，這支 wrapper 不重複做、也不越界掃描。
//   - 平台硬取消可能阻斷收尾（`always()` 不保證一定執行），未經真實遠端
//     測試驗證，本輪只在本機隔離環境測過。

import { spawn } from 'node:child_process';
import { createWriteStream, lstatSync, writeFileSync } from 'node:fs';
import { writeFile } from 'node:fs/promises';

function usage() {
  process.stderr.write(
    'usage: ci-e2e-wrapper.mjs <status-json> <out-file> <ts-out-file> <rc-file> -- <command...>\n',
  );
  process.exit(2);
}

const argv = process.argv.slice(2);
if (argv.length < 5) usage();
const STATUS_FILE = argv[0];
const OUT_FILE = argv[1];
const TS_OUT_FILE = argv[2];
const RC_FILE = argv[3];
if (argv[4] !== '--') usage();
const cmdArgs = argv.slice(5);
if (cmdArgs.length < 1) usage();
const [cmd, ...cmdRest] = cmdArgs;

const DEADLINE_SECONDS = Number(process.env.E2E_WRAPPER_DEADLINE_SECONDS ?? '1500');
const GRACE_SECONDS = Number(process.env.E2E_WRAPPER_GRACE_SECONDS ?? '120');
const WATCHDOG_BUFFER_SECONDS = Number(process.env.E2E_WRAPPER_WATCHDOG_BUFFER_SECONDS ?? '30');

// 只支援一般檔案（或尚不存在、即將建立的一般檔案）當四個輸出參數。啟動
// child 之前先逐一 lstat 檢查，FIFO／socket／字元或區塊裝置／目錄／符號
// 連結一律拒絕，不 spawn child，直接非零結束。
function classifyUnsupportedOutputTarget(p) {
  let st;
  try {
    st = lstatSync(p);
  } catch {
    return null; // 路徑尚不存在：視為即將建立的一般檔案，允許。
  }
  if (st.isSymbolicLink()) return 'symbolic link';
  if (st.isFIFO()) return 'FIFO';
  if (st.isSocket()) return 'socket';
  if (st.isCharacterDevice()) return 'character device';
  if (st.isBlockDevice()) return 'block device';
  if (st.isDirectory()) return 'directory';
  if (st.isFile()) return null; // 既有的一般檔案：允許（會被覆寫）。
  return 'unsupported special file';
}

function rejectUnsupportedOutputTargets() {
  const targets = [
    ['STATUS_FILE', STATUS_FILE],
    ['OUT_FILE', OUT_FILE],
    ['TS_OUT_FILE', TS_OUT_FILE],
    ['RC_FILE', RC_FILE],
  ];
  const violations = [];
  const badNames = new Set();
  for (const [name, p] of targets) {
    const kind = classifyUnsupportedOutputTarget(p);
    if (kind) {
      violations.push(`${name}=${p} 是 ${kind}，不是一般檔案，拒絕啟動`);
      badNames.add(name);
    }
  }
  if (violations.length === 0) return;

  process.stderr.write(
    `[ci-e2e-wrapper] 輸入契約檢查失敗，拒絕 spawn child（未啟動受測程序）：\n${
      violations.map((v) => `  - ${v}`).join('\n')
    }\n`,
  );
  const rejectRc = 2;
  if (!badNames.has('STATUS_FILE')) {
    try {
      writeFileSync(
        STATUS_FILE,
        `${JSON.stringify(
          { status: 'rejected-unsupported-output-target', wrapperRc: rejectRc, violations },
          null,
          2,
        )}\n`,
      );
    } catch (err) {
      process.stderr.write(`[ci-e2e-wrapper] STATUS_FILE 寫入失敗：${err.message}\n`);
    }
  }
  if (!badNames.has('RC_FILE')) {
    try {
      writeFileSync(RC_FILE, `${rejectRc}\n`);
    } catch (err) {
      process.stderr.write(`[ci-e2e-wrapper] RC_FILE 寫入失敗：${err.message}\n`);
    }
  }
  process.exit(rejectRc);
}

rejectUnsupportedOutputTargets();

const startIso = new Date().toISOString();
const startEpochMs = Date.now();

let externalSignal = null;
for (const sig of ['SIGTERM', 'SIGINT']) {
  process.on(sig, () => {
    if (!externalSignal) externalSignal = sig;
  });
}

const outStream = createWriteStream(OUT_FILE);
const tsStream = createWriteStream(TS_OUT_FILE);
const producerErrors = [];
outStream.on('error', (err) => producerErrors.push(`out-file: ${err.message}`));
tsStream.on('error', (err) => producerErrors.push(`ts-out-file: ${err.message}`));

let rawLineCount = 0;
let timedLineCount = 0;
let tsPartial = '';

function feed(_streamName, chunk) {
  outStream.write(chunk);
  const text = chunk.toString('utf8');
  rawLineCount += (text.match(/\n/g) ?? []).length;
  tsPartial += text;
  const lines = tsPartial.split('\n');
  tsPartial = lines.pop() ?? '';
  for (const line of lines) {
    tsStream.write(`${new Date().toISOString()} ${line}\n`);
    timedLineCount += 1;
  }
}

const child = spawn(cmd, cmdRest, { stdio: ['ignore', 'pipe', 'pipe'] });
const childPid = child.pid;
const childCommandAtSpawn = `${cmd} ${cmdRest.join(' ')}`.trim();

child.stdout.on('data', (chunk) => feed('stdout', chunk));
child.stderr.on('data', (chunk) => feed('stderr', chunk));

let childRc = null;
let childSignal = null;
let childExited = false;
let signalSent = null;
let signalReason = null;
let status = 'running';

let resolveClosed;
const closed = new Promise((resolve) => {
  resolveClosed = resolve;
});

child.on('exit', (code, signal) => {
  childRc = code;
  childSignal = signal;
  childExited = true;
});

child.on('close', () => {
  resolveClosed();
});

child.on('error', (err) => {
  producerErrors.push(`spawn: ${err.message}`);
  resolveClosed();
});

let graceTimer = null;
function armGraceTimer() {
  if (graceTimer) return;
  graceTimer = setTimeout(() => {
    if (!childExited) {
      status = 'timeout-no-clean-exit';
      resolveClosed();
    }
  }, GRACE_SECONDS * 1000);
  graceTimer.unref?.();
}

function sendTermIfPossible(reason) {
  if (signalSent) return;
  if (childExited) return;
  let ok = false;
  try {
    ok = child.kill('SIGTERM');
  } catch {
    ok = false;
  }
  if (ok) {
    signalSent = 'SIGTERM';
    signalReason = reason;
    armGraceTimer();
  } else {
    signalReason = `kill-failed(${reason})`;
  }
}

const deadlineTimer = setTimeout(() => {
  sendTermIfPossible(`inner-deadline(${DEADLINE_SECONDS}s)`);
}, DEADLINE_SECONDS * 1000);
deadlineTimer.unref?.();

const externalSignalPoll = setInterval(() => {
  if (externalSignal && !signalSent) {
    sendTermIfPossible(`external-signal(${externalSignal})`);
  }
}, 200);
externalSignalPoll.unref?.();

const watchdogSeconds = DEADLINE_SECONDS + GRACE_SECONDS + WATCHDOG_BUFFER_SECONDS;
let inFinalizePhase = false;
let watchdogHardExited = false;
function watchdogHardExit() {
  if (watchdogHardExited) return;
  watchdogHardExited = true;
  const endIsoLocal = new Date().toISOString();
  const elapsedSecondsLocal = Math.round((Date.now() - startEpochMs) / 1000);
  const forcedRc = 125;
  producerErrors.push(
    inFinalizePhase
      ? 'finalize: watchdog 觸發時仍在等待輸出串流 flush／寫檔完成，視為 incomplete（停留中的 writer 不再等待）'
      : 'watchdog: 在子行程確認結束前就觸發（deadline/grace 都無效或卡住），強制結束',
  );
  const statusPayload = {
    status: 'watchdog-forced-exit',
    startIso,
    endIso: endIsoLocal,
    elapsedSeconds: elapsedSecondsLocal,
    deadlineSeconds: DEADLINE_SECONDS,
    graceSeconds: GRACE_SECONDS,
    watchdogSeconds,
    childPid,
    childCommandAtSpawn,
    childRc,
    childSignal,
    wrapperRc: forcedRc,
    signalSent,
    signalReason,
    externalSignalReceived: externalSignal,
    childConfirmedGone: childExited ? 'true' : 'unknown',
    producerErrors,
    rawLineCount,
    timedLineCount,
  };
  try {
    writeFileSync(STATUS_FILE, `${JSON.stringify(statusPayload, null, 2)}\n`);
  } catch (err) {
    process.stderr.write(`[ci-e2e-wrapper] watchdog：STATUS_FILE 寫入失敗：${err.message}\n`);
  }
  try {
    writeFileSync(RC_FILE, `${forcedRc}\n`);
  } catch (err) {
    process.stderr.write(`[ci-e2e-wrapper] watchdog：RC_FILE 寫入失敗：${err.message}\n`);
  }
  process.stderr.write(
    `[ci-e2e-wrapper] status=watchdog-forced-exit wrapperRc=${forcedRc} childRc=${childRc} `
      + `elapsedSeconds=${elapsedSecondsLocal} signalSent=${signalSent ?? 'none'} `
      + `childConfirmedGone=${childExited ? 'true' : 'unknown'} producerErrors=${producerErrors.length}\n`,
  );
  process.exit(forcedRc);
}
const watchdogTimer = setTimeout(watchdogHardExit, watchdogSeconds * 1000);

await closed;
inFinalizePhase = true;

clearTimeout(deadlineTimer);
clearInterval(externalSignalPoll);
if (graceTimer) clearTimeout(graceTimer);

async function endStream(stream, name) {
  return new Promise((resolve) => {
    stream.end(() => resolve());
    stream.on('error', (err) => {
      producerErrors.push(`${name}: ${err.message}`);
      resolve();
    });
  });
}
if (tsPartial.length > 0) {
  tsStream.write(`${new Date().toISOString()} ${tsPartial}\n`);
  timedLineCount += 1;
  tsPartial = '';
}
await Promise.all([endStream(outStream, 'out-file'), endStream(tsStream, 'ts-out-file')]);

const endIso = new Date().toISOString();
const elapsedSeconds = Math.round((Date.now() - startEpochMs) / 1000);

if (status === 'running') {
  status = 'completed';
}
if (status === 'completed' && signalSent) {
  status = externalSignal && signalReason === `external-signal(${externalSignal})` ? 'interrupted' : 'timeout';
}
if (producerErrors.length > 0 && status === 'completed') {
  status = 'producer-error';
}

function wrapperRcForStatus(s) {
  switch (s) {
    case 'completed':
      return childRc !== null ? childRc : 1;
    case 'timeout':
    case 'timeout-no-clean-exit':
      return 124;
    case 'interrupted':
      return 143;
    case 'producer-error':
      return 126;
    case 'watchdog-forced-exit':
      return 125;
    default:
      return 1;
  }
}

let wrapperRc = wrapperRcForStatus(status);

async function tryWriteRc(value) {
  try {
    await writeFile(RC_FILE, `${value}\n`);
    return true;
  } catch (err) {
    producerErrors.push(`rc-file: ${err.message}`);
    return false;
  }
}

if (!(await tryWriteRc(wrapperRc))) {
  if (wrapperRc === 0) {
    status = 'producer-error';
    wrapperRc = wrapperRcForStatus(status);
  }
  try {
    await writeFile(RC_FILE, `${wrapperRc}\n`);
  } catch {
    // 已在 tryWriteRc() 記錄過一次錯誤，這裡不重複計入。
  }
}

const childConfirmedGone = childExited ? 'true' : 'false';

const statusPayload = {
  status,
  startIso,
  endIso,
  elapsedSeconds,
  deadlineSeconds: DEADLINE_SECONDS,
  graceSeconds: GRACE_SECONDS,
  watchdogSeconds,
  childPid,
  childCommandAtSpawn,
  childRc,
  childSignal,
  wrapperRc,
  signalSent,
  signalReason,
  externalSignalReceived: externalSignal,
  childConfirmedGone,
  producerErrors,
  rawLineCount,
  timedLineCount,
};

try {
  await writeFile(STATUS_FILE, `${JSON.stringify(statusPayload, null, 2)}\n`);
} catch (err) {
  producerErrors.push(`status-file: ${err.message}`);
  if (wrapperRc === 0) {
    wrapperRc = 1;
    try {
      await writeFile(RC_FILE, `${wrapperRc}\n`);
    } catch {
      // 不重複計入。
    }
  }
}

process.stderr.write(
  `[ci-e2e-wrapper] status=${status} wrapperRc=${wrapperRc} childRc=${childRc} elapsedSeconds=${elapsedSeconds} signalSent=${signalSent ?? 'none'} childConfirmedGone=${childConfirmedGone} producerErrors=${producerErrors.length}\n`,
);

clearTimeout(watchdogTimer);
process.exit(wrapperRc);
