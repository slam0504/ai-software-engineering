// stopProcedure.ts 的可注入相依測試（reviewer 複核 #34，2026-09-16，明確要求
// 「先建立可直接執行的小型停止程序測試，覆蓋 A1–A5、原本的『dead-root＋存活
// escaped child』與正常路徑。通過之前不要再用多輪真實 Wails 啟動試誤」）。
// 跟 artifactIntegrity.selftest.ts／networkLineParser.selftest.ts 同一個理由：
// 不放進 vitest 預設 suite（vitest.config.ts 排除 `e2e/**`），改用 Node 原生
// TS 支援直接執行。跟另外兩支 selftest 不同的是，`stopProcedure.ts` 本身有
// 跨檔案相依（`./logger.js`／`./psUtil.js`／`./runState.js`，用專案慣例的
// `.js` 寫法給 Playwright 的 esbuild-based loader 用），純 `node file.ts`
// 不會自動把 `.js` 解回真正存在的 `.ts` 檔，所以要另外掛一個只做這件事的
// 最小 resolve hook（`selftestJsToTsLoader.mjs`，只在這裡用，不影響任何正式
// 執行路徑）：
//   cd frontend && node --experimental-loader=./e2e/support/selftestJsToTsLoader.mjs e2e/support/stopProcedure.selftest.ts
//
// 這裡**不**啟動真正的 Wails／spawn 大量真實子行程——用 `stopProcessGroup`
// 第四個參數（`StopProcedureDeps`，只給 selftest 用）注入假的 `snapshot`／
// `kill`，讓每個情境的 `ps` 回傳內容與「送信號」的紀錄完全可控、可重現。
// `HarnessLogger` 本身寫真檔（唯一「不是假的」的相依），落在 /tmp 的獨立
// scratch 目錄，不動真正的 repo／證據目錄。
import assert from 'node:assert/strict';
import { ChildProcess } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { HarnessLogger } from './logger.ts';
// 注意：這兩個只拿來當型別用（不需要載入模組本身的執行期程式碼）。
// `runState.ts` 的 `RunStateStore` 用了 constructor 參數屬性簡寫，Node 原生
// TS strip-only 模式不支援這種語法——用 `import type` 讓它完全被抹除，不會
// 真的載入、解析那個模組，才能只匯入型別 `TrackedProc` 又不觸發那個限制。
import type { ProcRowWithCommand } from './psUtil.ts';
// L1（reviewer 複核 #34 第四次，2026-09-16）：這三個是實際會執行的函式（不是
// 只當型別用），用來直接測試 `ps` 語系修正——`snapshotProcessTableWithCommand`
// 真的呼叫 `ps`（驗證 PS_ENV 固定 LC_ALL=C 生效），`parsePsRowsWithCommand`
// 是純函式，用合成字串測試 malformed／空輸出的 fail-loud 行為，不需要真的
// 操控 `ps` 的輸出。
import { parsePsRowsWithCommand, snapshotProcessTableWithCommand } from './psUtil.ts';
import type { TrackedProc } from './runState.ts';
import { type StopProcedureDeps, stopProcessGroup } from './stopProcedure.ts';

let passed = 0;
async function check(name: string, fn: () => Promise<void> | void): Promise<void> {
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

const scratchDir = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a1-stopprocedure-selftest-'));
function newLogger(): HarnessLogger {
  return new HarnessLogger(fs.mkdtempSync(path.join(scratchDir, 'run-')));
}

function row(pid: number, pgid: number, command: string, startedAt: string, stat = 'S'): ProcRowWithCommand {
  return { pid, ppid: 1, pgid, stat, startedAt, command };
}

function tp(pid: number, pgid: number, command: string, startedAt: string, samePgid: boolean): TrackedProc {
  return { pid, ppid: 1, pgid, command, startedAt, samePgid };
}

interface KillCall { pid: number; signal: NodeJS.Signals }

function makeKillRecorder(): { kill: StopProcedureDeps['kill']; calls: KillCall[] } {
  const calls: KillCall[] = [];
  return { kill: (pid, signal) => { calls.push({ pid, signal }); }, calls };
}

// responses：依序供應每一次 `snapshot()` 呼叫的結果；`Error` 代表這一次要
// 拋出（模擬 ps 觀測失敗）。呼叫次數超過陣列長度時重複最後一筆（測項都會
// 精準算好次數，這只是防呆）。
function makeSnapshotQueue(responses: Array<ProcRowWithCommand[] | Error>): StopProcedureDeps['snapshot'] {
  let i = 0;
  return () => {
    const r = responses[Math.min(i, responses.length - 1)];
    i += 1;
    if (r instanceof Error) throw r;
    return r;
  };
}

function fakeHeldChild(pid: number, alive: boolean): ChildProcess {
  return { pid, exitCode: alive ? null : 0, signalCode: null } as unknown as ChildProcess;
}

async function main(): Promise<void> {
  // ---- A1：初次身分不符（reviewer 反例：tracked command=worker，目前同 pid command=other）----
  await check('A1：初次核對身分不符（command 不同）→ 不送信號、abandonedPids 有記錄、clean=false', async () => {
    const log = newLogger();
    const { kill, calls } = makeKillRecorder();
    const snapshot = makeSnapshotQueue([[row(100, 100, 'other', 'T1')]]);
    const result = await stopProcessGroup([tp(100, 100, 'worker', 'T1', true)], log, null, { snapshot, kill });
    assert.equal(calls.length, 0, '不應該對身分不符的目標送任何信號');
    assert.deepEqual(result.abandonedPids, [100]);
    assert.deepEqual(result.residualPids, []);
    assert.deepEqual(result.unconfirmedPids, []);
    assert.equal(result.clean, false);
  });

  // ---- A2：TERM 觀測失敗、無持有依據的目標不得升級 KILL ----
  await check('A2：TERM 等待期間 ps 觀測失敗（無 heldChild）→ 不升級 KILL、目標列為未確認', async () => {
    const log = newLogger();
    const { kill, calls } = makeKillRecorder();
    const snapshot = makeSnapshotQueue([
      [row(200, 200, 'fixture', 'T2')], // 初次驗證：身分相符，送 TERM
      new Error('ETIMEDOUT（模擬 ps 逾時）'), // TERM 等待期間：觀測失敗
    ]);
    const result = await stopProcessGroup([tp(200, 200, 'fixture', 'T2', false)], log, null, { snapshot, kill });
    assert.deepEqual(calls, [{ pid: 200, signal: 'SIGTERM' }], '只應該送過一次 TERM，絕對不能因為觀測失敗就送 KILL');
    assert.equal(result.escalatedToKill, false);
    assert.deepEqual(result.unconfirmedPids, [200]);
    assert.equal(result.observationFailed, true);
    assert.equal(result.clean, false);
  });

  // ---- A2 變形：heldChild 仍可靠，ps 失敗不影響它的升級 ----
  // late-write-after-stop 修正（2026-09-17）：`waitForDeathVerified` 的主迴圈
  // 現在對 heldChild 一律以 Node 自己的 `exitCode`／`signalCode` 為準，完全
  // 忽略這一輪 ps 快照的結果（見 stopProcedure.ts 對應段落的說明）——KILL
  // 階段「目標已死」不能再只靠下面這個 `[]`（ps 查無此 pid）的假回應來模擬，
  // 要讓 `heldChild.exitCode` 真的轉成非 null，才符合「heldChild 何時算已
  // 確認死亡」現在唯一的判斷依據。用 `setTimeout` 模擬「Node 稍後才回報
  // exit」，也一併驗證 KILL 階段確實會等到這個確認、不會被 ps 提前打斷。
  await check('A2（OR 分支）：ps 觀測失敗但目標是 heldChild 且 Node 回報仍存活 → 照常升級 KILL', async () => {
    const log = newLogger();
    const { kill, calls } = makeKillRecorder();
    const heldChild = fakeHeldChild(300, true);
    setTimeout(() => { (heldChild as unknown as { exitCode: number | null }).exitCode = 0; }, 100);
    const snapshot = makeSnapshotQueue([
      new Error('ETIMEDOUT（模擬 ps 逾時，TERM 等待期間）'),
      [], // KILL 階段：ps 這一輪回報查無此 pid——heldChild 優先規則下這個結果會被忽略，
      // 真正判定死亡的依據是上面 setTimeout 稍後才讓 exitCode 轉成非 null。
    ]);
    const result = await stopProcessGroup([tp(300, 300, 'wails-root', 'T3', true)], log, heldChild, { snapshot, kill });
    // 初次驗證不需要 ps（唯一目標就是 heldChild），第一次真正呼叫 snapshot 的是 TERM 等待階段。
    assert.deepEqual(calls, [
      { pid: -300, signal: 'SIGTERM' },
      { pid: -300, signal: 'SIGKILL' },
    ], 'heldChild 確認存活時，即使 ps 失敗也要能照常升級到 KILL');
    assert.equal(result.escalatedToKill, true);
    assert.deepEqual(result.residualPids, []);
    assert.deepEqual(result.unconfirmedPids, []);
    // 觀測失敗這件事本身仍然要誠實反映在最終結果，即使最後靠 heldChild 補救成功。
    assert.equal(result.observationFailed, true);
    assert.equal(result.clean, false);
  });

  // ---- A3：pid／pgid／command 相同，但開始時間不同 ----
  await check('A3：pid／pgid／command 相符但 startedAt 不同 → 視為身分不符，不送信號', async () => {
    const log = newLogger();
    const { kill, calls } = makeKillRecorder();
    const snapshot = makeSnapshotQueue([[row(400, 400, 'worker', 'T-NEW')]]);
    const result = await stopProcessGroup([tp(400, 400, 'worker', 'T-OLD', true)], log, null, { snapshot, kill });
    assert.equal(calls.length, 0, '開始時間對不上就不是同一次啟動的行程，不能送信號');
    assert.deepEqual(result.abandonedPids, [400]);
    assert.equal(result.clean, false);
  });

  // ---- A4a：括號命令列（argv 取不到）不是死亡證據 ----
  await check('A4a：等待期間 command 變成括號（非 zombie stat）→ 列為未確認，不當成死亡也不再送信號', async () => {
    const log = newLogger();
    const { kill, calls } = makeKillRecorder();
    const snapshot = makeSnapshotQueue([
      [row(500, 500, 'wails', 'T5')], // 初次驗證：相符，送 TERM
      [row(500, 500, '(wails)', 'T5', 'S')], // 等待期間：argv 取不到，stat 仍是一般存活狀態（非 Z）
    ]);
    const result = await stopProcessGroup([tp(500, 500, 'wails', 'T5', true)], log, null, { snapshot, kill });
    assert.deepEqual(calls, [{ pid: -500, signal: 'SIGTERM' }], '只送過一次 TERM，不該因為看到括號就判定已死而略過，也不該再送 KILL');
    assert.deepEqual(result.unconfirmedPids, [500]);
    assert.deepEqual(result.residualPids, []);
    // 關鍵區別：這是「這一批觀測成功、但這一列身分觀測不完整」，不是「這一批觀測本身失敗」。
    assert.equal(result.observationFailed, false);
    assert.equal(result.clean, false);
  });

  // ---- A4b：真正的 zombie（stat=Z／<defunct>）才是死亡證據 ----
  await check('A4b：等待期間 stat=Z（真正 zombie）→ 視為已死，正常收尾 clean=true', async () => {
    const log = newLogger();
    const { kill, calls } = makeKillRecorder();
    const snapshot = makeSnapshotQueue([
      [row(501, 501, 'wails', 'T5b')],
      [row(501, 501, '<defunct>', 'T5b', 'Z')],
    ]);
    const result = await stopProcessGroup([tp(501, 501, 'wails', 'T5b', true)], log, null, { snapshot, kill });
    assert.deepEqual(calls, [{ pid: -501, signal: 'SIGTERM' }]);
    assert.deepEqual(result.residualPids, []);
    assert.deepEqual(result.unconfirmedPids, []);
    assert.deepEqual(result.abandonedPids, []);
    assert.equal(result.observationFailed, false);
    assert.equal(result.clean, true);
  });

  // ---- A5：log 寫入失敗不得中斷其餘可安全執行的清理 ----
  await check('A5：harness.log 寫入持續拋錯 → 兩個 pgid 仍都送出信號，diagnosticWriteFailed=true、clean=false', async () => {
    const log = newLogger();
    // 讓這個 logger 實例的 log() 一律拋出，模擬磁碟寫入失敗——直接覆寫這個
    // instance 的方法（不影響其他測項用的 logger）。
    (log as unknown as { log: (msg: string) => void }).log = () => {
      throw new Error('ENOSPC（模擬磁碟寫滿，僅供測試）');
    };
    const { kill, calls } = makeKillRecorder();
    const snapshot = makeSnapshotQueue([
      [row(600, 600, 'a', 'T6'), row(601, 601, 'b', 'T6b')], // 初次驗證：兩個不同 pgid 都相符
      [], // TERM 等待：兩個都死了
    ]);
    const result = await stopProcessGroup(
      [tp(600, 600, 'a', 'T6', true), tp(601, 601, 'b', 'T6b', true)],
      log,
      null,
      { snapshot, kill },
    );
    const sigtermPids = calls.filter(c => c.signal === 'SIGTERM').map(c => c.pid).sort((a, b) => a - b);
    assert.deepEqual(sigtermPids, [-601, -600], 'log() 每次呼叫都拋錯，仍然要對兩個 pgid 都送出 TERM，不能因為第一個 log 失敗就漏掉第二個');
    assert.equal(result.diagnosticWriteFailed, true);
    assert.equal(result.clean, false);
  });

  // ---- 原始情境：dead root + 存活的 escaped child（samePgid=false）----
  await check('dead-root＋存活 escaped child：不對已消失的 root pgid 送信號，只處理 escaped child', async () => {
    const log = newLogger();
    const { kill, calls } = makeKillRecorder();
    const snapshot = makeSnapshotQueue([
      [row(701, 999, 'escaped-child', 'T7b')], // root(700) 已經不在表裡；只有 escaped child
      [], // TERM 之後 escaped child 也死了
    ]);
    const result = await stopProcessGroup(
      [tp(700, 700, 'root', 'T7', true), tp(701, 999, 'escaped-child', 'T7b', false)],
      log,
      null,
      { snapshot, kill },
    );
    assert.deepEqual(calls, [{ pid: 701, signal: 'SIGTERM' }], '不能有任何對負數 pid（group kill）的呼叫——沒有已驗證存活的 samePgid 成員背書 root 的 pgid');
    assert.equal(result.clean, true);
  });

  // ---- 正常路徑：TERM 後全部確認死亡 ----
  await check('正常路徑：TERM 後全部確認死亡，clean=true、不升級 KILL', async () => {
    const log = newLogger();
    const { kill, calls } = makeKillRecorder();
    const snapshot = makeSnapshotQueue([
      [row(800, 800, 'root', 'T8')],
      [],
    ]);
    const result = await stopProcessGroup([tp(800, 800, 'root', 'T8', true)], log, null, { snapshot, kill });
    assert.deepEqual(calls, [{ pid: -800, signal: 'SIGTERM' }]);
    assert.equal(result.escalatedToKill, false);
    assert.equal(result.clean, true);
  });

  // ==== L1（reviewer 複核 #34 第四次，2026-09-16）：ps 的 lstart 解析依賴語系 ====
  // 這三項刻意用真正的 `ps`／真正的 `parsePsRowsWithCommand`（不是注入假的
  // snapshot），因為要驗證的正是「PS_ENV 固定 LC_ALL=C」這個環境層級的修正
  // 有沒有真的生效，用 mock 沒辦法測到這件事。

  await check('L1-1：C 格式（PS_ENV 固定 LC_ALL=C）可正常解析，找得到自己的 pid', () => {
    const rows = snapshotProcessTableWithCommand();
    assert.ok(rows.length > 0, 'C 格式下批次快照不應該是空的');
    const self = rows.find(r => r.pid === process.pid);
    assert.ok(self, '批次快照裡應該找得到目前這個 Node 行程自己的 pid');
    assert.match(self!.startedAt, /^\w{3}\s+\w{3}\s+\d{1,2}\s+\d{2}:\d{2}:\d{2}\s+\d{4}$/, 'startedAt 應該是英文格式（lstart 的 %c 樣式）');
  });

  await check('L1-2：呼叫端環境繼承 zh_TW.UTF-8 時，PS_ENV 覆蓋仍能正常解析、找到活著的自身 pid', () => {
    const originalLcAll = process.env.LC_ALL;
    // 模擬 reviewer 的重現方式：呼叫端（這個 Node 行程）自己的環境設成
    // zh_TW.UTF-8——`PS_ENV` 其實是 `psUtil.ts` 模組**載入時**就建立好的一次性
    // 快照（`{ ...process.env, LC_ALL: 'C' }`，模組層級的 const），不是每次
    // `ps` 呼叫當下才動態疊加。這裡驗證的是：`PS_ENV.LC_ALL` 一律固定是
    // `'C'`（在模組載入當下就已經覆蓋、寫死），跟這個測試稍後才去改
    // `process.env.LC_ALL` 無關——已經算好的 `PS_ENV` 不會因為呼叫端事後
    // 改變自己的環境變數而跟著變動，這正是它能穩定覆蓋掉呼叫端語系的原因。
    process.env.LC_ALL = 'zh_TW.UTF-8';
    try {
      const rows = snapshotProcessTableWithCommand();
      assert.ok(rows.length > 0, 'zh_TW.UTF-8 呼叫端環境下批次快照不應該是空的（PS_ENV 應該已經覆蓋掉語系）');
      const self = rows.find(r => r.pid === process.pid);
      assert.ok(self, '即使呼叫端繼承 zh_TW.UTF-8，批次快照裡也應該找得到自己的 pid');
      assert.match(self!.startedAt, /^\w{3}\s+\w{3}\s+\d{1,2}\s+\d{2}:\d{2}:\d{2}\s+\d{4}$/, '就算呼叫端是 zh_TW.UTF-8，PS_ENV 覆蓋後 lstart 仍應該是英文格式');
    } finally {
      if (originalLcAll === undefined) delete process.env.LC_ALL;
      else process.env.LC_ALL = originalLcAll;
    }
  });

  await check('L1-3a：malformed（非空但解析不出來）批次輸出必須拋錯，不得回報空結果', () => {
    // 合成一行格式錯誤的輸出（模擬 zh_TW 的 lstart，例如「三  9/16 14:19:25 2026」
    // 這種完全不同的格式），混一行正常的，驗證只要有任何一行解析不出來就整批拋錯，
    // 不會悄悄放行成「只有一筆資料」。
    const malformed = '12345 1 12345 Ss 三  9/16 14:19:25 2026 /bin/fake\n'
      + '99999 1 99999 Ss Wed Sep 16 14:19:25 2026 /bin/fake2\n';
    assert.throws(() => parsePsRowsWithCommand(malformed), /觀測失敗/, 'malformed 列混在正常列裡也應該整批拋錯');
  });

  await check('L1-3b：整份空輸出必須拋錯，不得回報 clean（不能當成「沒有任何行程」）', () => {
    assert.throws(() => parsePsRowsWithCommand(''), /觀測失敗/, '空字串不是合法的 ps -A 空結果');
    assert.throws(() => parsePsRowsWithCommand('\n\n  \n'), /觀測失敗/, '只有空白行也不是合法的 ps -A 空結果');
  });

  await check('L1-3c：stopProcessGroup 的初次身分驗證若批次觀測直接拋錯，絕對不能回報 clean=true', async () => {
    const log = newLogger();
    const { kill, calls } = makeKillRecorder();
    const snapshot = makeSnapshotQueue([new Error('模擬 malformed 批次觀測失敗（L1）')]);
    const result = await stopProcessGroup([tp(900, 900, 'root', 'T9', true)], log, null, { snapshot, kill });
    assert.equal(calls.length, 0, '送信號前的身分驗證觀測失敗，不應該對任何目標送信號');
    assert.equal(result.clean, false, '初次驗證觀測失敗絕對不能回報 clean=true——這正是 L1 的核心風險');
    assert.equal(result.observationFailed, true);
  });

  console.log(`\n${passed} 項通過`);
  if (process.exitCode) {
    console.error('有測項失敗');
  } else {
    console.log('全部通過');
  }
}

main().finally(() => {
  fs.rmSync(scratchDir, { recursive: true, force: true });
});
