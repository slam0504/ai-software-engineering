#!/usr/bin/env node
// ci-e2e-wrapper.mjs — B3a-CI R2/R4 修法之後、codex-reviewer(#78) 四個反例的修正版。
//
// 早期的 bash 版本（未進 repo，僅存於準備階段任務目錄）是用 `ps -o lstart=`
// 輪詢確認直接子行程身分、用 `> >(tee ... | while read ... date ...)`
// process substitution 做逐行時間戳、用一個 while true 迴圈輪詢 `kill -0`。
// codex-reviewer 用四個反例證明這個設計在四個地方都會生產出「看起來正常，
// 其實是假的」證據（詳見 codex-ci78-counterexamples/review.json、
// identity-review.json、package-review.json）：
//   F1 term_exit_zero：子行程收到 TERM 後自己 exit 0，wrapper 把 status 改成
//      timeout，但最終 wrapper rc／e2e.rc 仍然是子行程自己的 0 —— 因為舊版
//      「if [ -n "$rc" ]」的判斷式跟 status 是否被改寫成 timeout 完全脫鉤。
//   F2 log_drain：process substitution 是非同步管線，wrapper 主迴圈偵測到
//      子行程已經退出就直接往下走，不等 tee／逐行 date 那條管線真正把資料
//      寫完，raw=500 但 timestamped=20，資料還在寫就被當成「已經拿到」。
//   F3 identity_observation_failure：PATH 上的 `ps` 一律回傳失敗時，
//      `child_start`／`now_start` 永遠是空字串，`identity_matches` 永遠是
//      false，但 `signal_sent` 也永遠不會被設定 —— 迴圈裡「grace 從
//      signal_sent 才開始算」的分支永遠進不去，於是整支 wrapper 沒有上限地
//      空轉，直到被外層平台強殺。
//   F4 package_without_harness：這個反例其實是 YAML packaging step 的邏輯
//      漏洞（wrapper 本身沒有需要修的地方）——見同目錄
//      package-e2e-evidence.sh 與 draft-e2e-smoke.yml 的對應修法。
//
// 這支 Node 版直接用 child_process 的事件（'exit'／'close'）取代輪詢：
//   - 不再用 `ps` 核對身分。我們直接 spawn 子行程並保留住那個 ChildProcess
//     物件的 handle，之後只對這個 handle 呼叫 `.kill()`——不是重新用 pid
//     查一次「現在這個 pid 是誰」再送訊號；送之前也先檢查 `childExited`
//     （由 'exit' 事件維護），已經觀察到子行程結束就不再送。這是本程式
//     實際做到的邊界：沒有對 Node runtime 內部實作做任何語言層保證的宣稱，
//     只是「直接持有 handle＋送之前檢查存活狀態」這個具體行為，用來取代舊版
//     `ps + pid + lstart` 三要素比對。改用 Node 的另一個理由是 F2：Node 的
//     stream 有明確的 'finish'／'end' 事件可以等。
//   - deadline／grace 用 setTimeout 表示，不用輪詢迴圈；子行程的『真的結束
//     了』只信任 'exit' 事件（Node runtime 自己維護，不受 `ps` 好壞影響）。
//   - 額外加一個「硬上限」watchdog（deadline + grace + WATCHDOG_BUFFER_MS），
//     只要正常流程在這個時間內沒有結束（不管是因為哪個環節卡住——即使未來
//     又混進某種需要外部觀察工具的檢查、或 I/O 卡住），一定會被強制拉去寫
//     一份「watchdog-forced-exit」狀態並用非零結束，藉此把 F3 那種「無上限
//     空轉」在結構上排除掉，不只是「這次修好那個特定成因」。這個 watchdog
//     **不會**送任何訊號給子行程（無法確認當下是否還安全能送），只保證
//     wrapper 自己不會無限期不退出。watchdog 的計時器一路蓋到最終寫檔／
//     process.exit() 為止（見下方 codex-reviewer(#82) G4 修法），不是只蓋
//     到子行程結束為止。
//   - childRc（子行程自己回報的 exit code）與 wrapperRc（wrapper 最終
//     process.exit 用的碼、也是寫進 e2e.rc 的值）分成兩個欄位：
//     status.json 的 `childRc` 一定是子行程自己的原始值（`rc=7` 這種也如實
//     保留，不被覆寫）；wrapperRc 完全由 `status` 決定
//     （completed→沿用 childRc；timeout／timeout-no-clean-exit→124；
//     interrupted→143；watchdog-forced-exit／producer-error→125／126），
//     不會發生 F1 那種「status 已經改判成 timeout，但 rc 卻沿用子行程自己
//     的 0」的脫鉤。
//   - 逐行時間戳／raw 副本改成直接掛在 Node 拿到的 child.stdout／
//     child.stderr data 事件上寫檔，finalize 前用 `await Promise.all([...])`
//     等兩個檔案的 write stream 真的 'finish'（buffer 已經 flush 到 fd），
//     不是「子行程已退出」就代表「我們的檔案也寫完了」。任何寫入錯誤都記
//     進 status.json 的 `producerErrors`，並讓最終 status 變成
//     `producer-error`（強制非零），不得把不完整的證據標成 completed。
//
// codex-reviewer(#82) 用另外四個反例證明上面這版仍有兩個缺口，這裡一併記錄
// 修法（詳見 codex-ci82-counterexamples/）：
//   G3 rc_write_failure：RC_FILE 指到一個目錄，child 正常 exit 0，但寫
//      RC_FILE 失敗；舊版是「先寫 STATUS_FILE（此時還不知道 RC_FILE 會失
//      敗，內容記成 completed/wrapperRc=0/producerErrors=[]）→ 再寫
//      RC_FILE（失敗只 push producerErrors，不調整 wrapperRc）」，最終
//      process.exit(0)。新版把寫檔順序反過來：先試寫 RC_FILE，失敗就把
//      status 升級成 `producer-error`（wrapperRc=126）、用新值重試一次，
//      STATUS_FILE 留到最後才寫、內容一定反映這個最終判定，不會出現
//      「必要產物寫不出去，但 exit code 還是 0」。
//   G4 blocked_output_finish：OUT_FILE 是沒有 reader 的 FIFO，flush
//      （`stream.end()` 等 'finish'）會無上限卡住；舊版在 `await closed`
//      之後就 `clearTimeout(watchdogTimer)`，watchdog 從這裡開始就沒有再
//      保護 flush／寫檔這段，而且 watchdog 觸發時原本只呼叫
//      `resolveClosed()`，對「正在等 stream 'finish' 的 promise」完全沒用
//      （那個 promise 不是在等 `closed`）。上一輪的修法是讓 watchdog 計時器
//      本身成為一條獨立的最後退出路徑＋另外 spawn 一個外部保險行程兜底。
//
// codex-reviewer(#84) 收斂：上一輪 G4 修法本身還需要一個帶身分核對的外部
// 保險行程（`ps -o lstart=` + detached shell + `sleep`），reviewer 認定這
// 對一支薄 wrapper 而言超出必要——同步 `execFileSync('ps')` 沒有 timeout、
// guardPid 一旦核對通過就直接 kill 整個 process group 而非核對當下真正的
// 身分、wrapper 被外層平台提早中止時 guard 仍可能存活。這裡改採更簡單的輸
// 入契約，把問題在源頭排除，而不是繼續在下游補強 guard：
//   - spawn child 之前，先對四個輸出參數（STATUS_FILE／OUT_FILE／
//     TS_OUT_FILE／RC_FILE）逐一 `lstatSync`：只要有一個是 FIFO／
//     socket／字元或區塊裝置／目錄／符號連結，一律視為不支援的輸出目標，
//     不 spawn child，直接非零結束（見 `rejectUnsupportedOutputTargets()`）。
//     尚不存在的路徑視為「即將建立的一般檔案」，允許。
//   - 這支 wrapper 現在只承諾四個輸出參數是「一般檔案（或尚不存在的新一
//     般檔案）」，FIFO 不在支援範圍內；因此本來為了撐住 FIFO flush 卡住
//     而需要的外部保險行程整個移除，不再用 `ps` 核對任何行程身分。
//   - 內部 deadline／grace／watchdog 仍然要蓋到整個 finalize（寫 status／
//     rc）完成為止，不得在那之前被提前取消——這是為了涵蓋一般檔案 I/O
//     本身卡住（例如主機／檔案系統整體異常）的情境，不是為了撐 FIFO。
//     這種情況下的最後界線就是既有的 CI step 30 分鐘／job 45 分鐘平台限
//     制，資料可能不完整，這是已知限制、不再宣稱 Node 能保證任意 I/O 卡
//     死都寫得出完整 status。
//
// 用法：
//   node ci-e2e-wrapper.mjs <status-json> <out-file> <ts-out-file> <rc-file> \
//     -- <command...>
//
// 環境變數：
//   E2E_WRAPPER_DEADLINE_SECONDS（預設 1500＝25 分鐘）
//   E2E_WRAPPER_GRACE_SECONDS（預設 120）
//
// 已知限制（照實記錄，不誇大）：
//   - watchdog 觸發時，子行程可能仍在背景存活（我們沒有送 SIGKILL，也沒有
//     再次嘗試送 SIGTERM——watchdog 觸發代表連正常的「送訊號→等 grace」
//     流程本身都不可信了，這時候再送訊號沒有意義）。子行程樹的最終清理要
//     靠 CI 平台自己的 job/runner 層級回收，這支 wrapper 沒有能力也不嘗試
//     保證。
//   - 逐行時間戳精確到毫秒（`Date.toISOString()`），跟 harness.log 同精度
//     （原本 bash 版只有秒）。
//   - 這支 wrapper 仍然只追蹤『自己直接 spawn 的那個子行程』本身；後代行程
//     樹（wails dev／Chrome／vite）的清理仍然是
//     frontend/e2e/global-teardown.ts 的 stopProcedure 負責，這支 wrapper
//     不重複做、也不越界掃描。
//   - STATUS_FILE／OUT_FILE／TS_OUT_FILE／RC_FILE 只支援一般檔案（或尚不
//     存在、即將建立的新一般檔案）；FIFO／socket／裝置／目錄／符號連結一
//     律在 spawn child 之前被拒絕（見 `rejectUnsupportedOutputTargets()`），
//     不會嘗試支援。

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

// codex-reviewer(#84) G4 收斂：這支 wrapper 只支援一般檔案（或尚不存在、
// 即將建立的一般檔案）當四個輸出參數。啟動 child 之前先逐一 lstat 檢查，
// FIFO／socket／字元或區塊裝置／目錄／符號連結一律拒絕，不 spawn child，
// 直接非零結束。錯誤訊息盡量寫進「本身沒問題」的 STATUS_FILE／RC_FILE，
// 一定會寫 stderr；本身有問題的那個目標不會被寫入。
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

function feed(streamName, chunk) {
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

let childRc = null; // 子行程自己的 exit code；因訊號結束時維持 null
let childSignal = null;
let childExited = false; // 'exit' 事件真的有沒有發生過——不能只看 childRc
                          // 是否非 null，子行程被訊號殺死時 code 就是 null，
                          // 但『行程已經不在了』這件事是確定的。
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
  if (signalSent) return; // 已經送過，不重送
  if (childExited) return; // 已經結束了，不需要再送
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
    // kill() 回傳 false／丟例外：代表這個 ChildProcess handle 認為已經送不出去
    // （通常是行程已經不在了）。**不**假裝已經送出，也不當成「已清理」。
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

// Watchdog covers asynchronous waits through finalize. A blocked event loop or
// filesystem can still prevent this callback or its writes; CI timeouts remain
// the outer bound. Unsupported special output files are rejected before spawn.
const watchdogSeconds = DEADLINE_SECONDS + GRACE_SECONDS + WATCHDOG_BUFFER_SECONDS;
let inFinalizePhase = false; // await closed 之後才設成 true；用來在 producerErrors
                              // 裡如實記錄 watchdog 是在哪個階段觸發的。
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
  // 能寫的 status 就更新；不能寫的錯誤印 stderr，不承諾磁碟故障時檔案一定
  // 存在——這裡故意用同步 API 且各自獨立 try/catch，watchdog 這條路徑本身
  // 就是「別的東西都卡住了」的最後手段，不能再依賴另一個可能卡住的 await。
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
// Keep the watchdog alive through final evidence writes.

// codex-reviewer(#84)：上一輪在這裡有一個帶身分核對的外部保險行程（detached
// shell + `sleep` + `ps -o lstart=` 核對後 SIGKILL），reviewer 認定它對這支
// 薄 wrapper 而言超出必要（同步 `ps` 沒有 timeout、guardPid 核對通過就直接
// kill 整個 process group、wrapper 被外部提早中止時 guard 仍可能存活），已
// 移除、不再補強。watchdogHardExit() 本身（同步寫檔＋process.exit()）搭配
// 上面新增的輸出目標前置檢查（FIFO 等會在 spawn 前就被拒絕，不會再卡進
// blocked_output_finish 那種情境）已是這支 wrapper 對「自己不會無限期不
// 退出」所能做的完整承諾；一般檔案 I/O 若遇主機／檔案系統整體卡死，最後
// 界線就是既有的 CI step 30 分鐘／job 45 分鐘平台限制，資料可能不完整——
// 這是已知限制，不再宣稱能保證任意 I/O 卡死情境下都寫得出完整 status。

await closed;
inFinalizePhase = true;

clearTimeout(deadlineTimer);
clearInterval(externalSignalPoll);
if (graceTimer) clearTimeout(graceTimer);

// 等兩個檔案的 write stream 真的把 buffer flush 完（F2 的修法核心）。注意
// watchdogTimer 這裡還沒清掉——它必須蓋到這段 flush 為止（G4），如果卡住，
// 上面的 watchdogHardExit 會直接接手完成最後的寫檔與結束。
async function endStream(stream, name) {
  return new Promise((resolve) => {
    stream.end(() => resolve());
    stream.on('error', (err) => {
      producerErrors.push(`${name}: ${err.message}`);
      resolve();
    });
  });
}
// 把最後一段沒有換行結尾的殘留內容也算進 timed 輸出（若有）。
if (tsPartial.length > 0) {
  tsStream.write(`${new Date().toISOString()} ${tsPartial}\n`);
  timedLineCount += 1;
  tsPartial = '';
}
await Promise.all([endStream(outStream, 'out-file'), endStream(tsStream, 'ts-out-file')]);

// Keep watchdog active while writing rc and status as well as flushing logs.

const endIso = new Date().toISOString();
const elapsedSeconds = Math.round((Date.now() - startEpochMs) / 1000);

if (status === 'running') {
  status = 'completed';
}
if (status === 'completed' && signalSent) {
  status = externalSignal && signalReason === `external-signal(${externalSignal})` ? 'interrupted' : 'timeout';
}
if (producerErrors.length > 0 && status === 'completed') {
  // 子行程正常結束，但我們自己的證據副本沒能完整寫下去——不得標成完整證據。
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
      // 正常流程不會走到這裡（watchdog 觸發時是 watchdogHardExit 自己
      // process.exit()，不會回到這段程式碼）；保留這個分支只是讓 rc 對照
      // 表完整、方便閱讀，不代表這裡真的會被執行到。
      return 125;
    default:
      return 1;
  }
}

let wrapperRc = wrapperRcForStatus(status);

// G3 修法（codex-ci82 rc_write_failure）：必要產物（RC_FILE）寫不出去，不能
// 讓最終 exit code 還是 0。改成「先試寫 RC_FILE，失敗就把 status 升級成
// producer-error 並用新值重試一次，STATUS_FILE 留到最後才寫」，這樣
// STATUS_FILE 的內容一定反映已知的最終真相，不會把「其實還沒真正完成」的
// 結果先記成 completed／wrapperRc=0／producerErrors=[]（這正是舊版的漏洞：
// 舊版先寫 STATUS_FILE、後寫 RC_FILE，RC_FILE 失敗時 STATUS_FILE 已經寫錯
// 了）。
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
    // 已經在 tryWriteRc() 記錄過一次錯誤，這裡不重複計入 producerErrors；
    // 不承諾磁碟故障時 RC_FILE 一定寫得出來，stderr 摘要仍會印出最終
    // producerErrors 數量。
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
  // 連 status.json 都寫不出去：這是我們能做的最後手段，仍然要讓 rc 檔案
  // 跟 process exit code 保持「非零」，不假裝成功。
  producerErrors.push(`status-file: ${err.message}`);
  if (wrapperRc === 0) {
    wrapperRc = 1;
    try {
      await writeFile(RC_FILE, `${wrapperRc}\n`);
    } catch {
      // 不重複計入；已經在上面交代過磁碟故障時不承諾檔案一定寫得出來。
    }
  }
}

process.stderr.write(
  `[ci-e2e-wrapper] status=${status} wrapperRc=${wrapperRc} childRc=${childRc} elapsedSeconds=${elapsedSeconds} signalSent=${signalSent ?? 'none'} childConfirmedGone=${childConfirmedGone} producerErrors=${producerErrors.length}\n`,
);

clearTimeout(watchdogTimer);
process.exit(wrapperRc);
