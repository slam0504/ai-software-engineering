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
import { type StopProcedureDeps, type StopResult, stopProcessGroup } from './stopProcedure.ts';

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
//
// `callCount()`（cleanup-observation-002 移植，2026-09-28）：原本的候選案例
// 用它斷言「案例在第幾筆快照就已經定案、之後的快照沒有被消費到」，是這批
// 案例特有的可觀察行為，既有 14 項不需要所以之前沒有——用一個型別化的交集
// （函式本體仍完全符合 `StopProcedureDeps['snapshot']`）掛上去，不影響既有
// 呼叫端。
function makeSnapshotQueue(
  responses: Array<ProcRowWithCommand[] | Error>,
): StopProcedureDeps['snapshot'] & { callCount: () => number } {
  let i = 0;
  const fn = (() => {
    const r = responses[Math.min(i, responses.length - 1)];
    i += 1;
    if (r instanceof Error) throw r;
    return r;
  }) as unknown as StopProcedureDeps['snapshot'] & { callCount: () => number };
  fn.callCount = () => i;
  return fn;
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

  // ==== cleanup-observation-002（reviewer 複核 #499 §C／decision.md #501
  // 「下一步：准許本機整合與提交」第 3 點，2026-09-28）：把
  // cleanup-observation-002/tests/candidate-c-cases.mjs 的 16 個案例移植成
  // 常駐 selftest。原始腳本用同一份情境分別跑 baseline／candidate 兩份
  // `stopProcedure.ts` 副本、比對兩邊行為差異；這裡只保留 candidate（也就是
  // 這個 repo 現在唯一的正式版本）該有的行為當斷言，不重建 baseline 分支、
  // 不寫回任何 results json。案例編號（「案例N」／括號英文字母）沿用原始
  // 腳本，方便跟 candidate-c-cases.mjs 對照。

  // ---- 案例1：match → 帶 E 的括號 → gone ----
  await check('案例1：match→帶E括號(?<E)→gone，clean=true、unconfirmedPids 清空', async () => {
    const log = newLogger();
    const { kill, calls } = makeKillRecorder();
    const t = tp(11001, 11001, 'fixture-a', 'T-1', false);
    const snapshot = makeSnapshotQueue([
      [row(11001, 11001, 'fixture-a', 'T-1')],
      [row(11001, 11001, '(node)', 'T-1', '?<E')],
      [],
    ]);
    const result = await stopProcessGroup([t], log, null, { snapshot, kill });
    assert.deepEqual(calls, [{ pid: 11001, signal: 'SIGTERM' }], '只應該送過一次 TERM（安全不變條件：不對未確認目標送信號）');
    assert.equal(result.clean, true, '應該解除未確認狀態、clean=true');
    assert.deepEqual(result.unconfirmedPids, [], '不應該留下未解的未確認 pid');
    assert.equal(snapshot.callCount(), 3, '應該讀完全部 3 筆快照');
  });

  // ---- 案例2：match → 不帶 E 的括號（?<）→ gone ----
  await check('案例2：match→不帶E括號(?<)→gone，clean=true、unconfirmedPids 清空', async () => {
    const log = newLogger();
    const { kill, calls } = makeKillRecorder();
    const t = tp(11002, 11002, 'fixture-b', 'T-1', false);
    const snapshot = makeSnapshotQueue([
      [row(11002, 11002, 'fixture-b', 'T-1')],
      [row(11002, 11002, '(node)', 'T-1', '?<')],
      [],
    ]);
    const result = await stopProcessGroup([t], log, null, { snapshot, kill });
    assert.deepEqual(calls, [{ pid: 11002, signal: 'SIGTERM' }], '只應該送過一次 TERM');
    assert.equal(result.clean, true, '應該解除未確認狀態、clean=true（不帶 E 一樣是括號，classifyRow 本來就不分 E）');
    assert.deepEqual(result.unconfirmedPids, [], '不應該留下未解的未確認 pid');
    assert.equal(snapshot.callCount(), 3, '應該讀完全部 3 筆快照');
  });

  // ---- 案例3：unconfirmed 一直存在到 deadline → failed，unknown 沒有收到任何
  // 額外信號。這個案例會真的等到底（TERM 10s＋KILL 觀測窗 5s，約 15s）——
  // sleep 沒有被注入，是真實時間，刻意如此才能驗證「到 deadline 仍無法確認
  // 就維持 failed」不是提早放棄，而是真的等滿。
  await check('案例3：unconfirmed一直存在到deadline→failed、unknown無額外信號', async () => {
    const log = newLogger();
    const { kill, calls } = makeKillRecorder();
    const t = tp(11003, 11003, 'fixture-c', 'T-1', false);
    const snapshot = makeSnapshotQueue([
      [row(11003, 11003, 'fixture-c', 'T-1')],
      [row(11003, 11003, '(node)', 'T-1', '?<E')], // 之後永遠重複這一筆，永不解決
    ]);
    const result = await stopProcessGroup([t], log, null, { snapshot, kill });
    assert.equal(result.clean, false, 'clean 應該是 false：這個 pid 永遠無法解除未確認狀態');
    assert.deepEqual(result.unconfirmedPids, [11003], 'unconfirmedPids 應該仍列著這個 pid');
    assert.deepEqual(calls, [{ pid: 11003, signal: 'SIGTERM' }], '除了最初那次 TERM，不應該再對這個未確認目標送任何信號');
  });

  // ---- 案例4：pid 重用，startedAt／pgid 不符 → 保留 mismatch，不對新程序動作 ----
  await check('案例4：先前unconfirmed的pid被重用（startedAt不同）→mismatch，不送信號', async () => {
    const log = newLogger();
    const { kill, calls } = makeKillRecorder();
    const t = tp(11004, 11004, 'fixture-d', 'T-OLD', false);
    const snapshot = makeSnapshotQueue([
      [row(11004, 11004, 'fixture-d', 'T-OLD')], // 初次驗證：相符，送 TERM
      [row(11004, 11004, '(fixture-d)', 'T-OLD', 'S')], // TERM 等待：argv 取不到，未確認
      [row(11004, 22222, 'other-proc', 'T-NEW', 'S')], // 再下一輪：pid 被重用，pgid／startedAt／command 都變了
    ]);
    const result = await stopProcessGroup([t], log, null, { snapshot, kill });
    assert.deepEqual(calls, [{ pid: 11004, signal: 'SIGTERM' }], '偵測到 pid 重用之後不應該對新程序送任何信號（安全不變條件）');
    assert.equal(result.clean, false, 'clean 應該是 false：曾經身分不符過');
    assert.deepEqual(result.abandonedPids, [11004], '應該把它移進 abandonedPids（確認身分不符），不是 unconfirmedPids');
    assert.deepEqual(result.unconfirmedPids, [], 'mismatch 解決後不應該還留在 unconfirmedPids');
    assert.equal(snapshot.callCount(), 3, '應該讀完全部 3 筆快照才發現 mismatch');
  });

  // ---- 案例5：真實觀測工具錯誤之後才 gone → 仍保留觀測失敗 ----
  await check('案例5：TERM等待期間ps觀測工具真的失敗一次，之後才確認gone→observationFailed仍為true', async () => {
    const log = newLogger();
    const { kill, calls } = makeKillRecorder();
    const t = tp(11005, 11005, 'fixture-e', 'T-1', false);
    const snapshot = makeSnapshotQueue([
      [row(11005, 11005, 'fixture-e', 'T-1')], // 初次驗證：相符，送 TERM
      new Error('ETIMEDOUT（模擬 ps 逾時，僅供測試）'), // TERM 等待期間：批次觀測本身失敗
      [], // 之後（沒有殘存可送信號，進入唯讀觀測窗）：查無此 pid，確認已死
    ]);
    const result = await stopProcessGroup([t], log, null, { snapshot, kill });
    assert.deepEqual(calls, [{ pid: 11005, signal: 'SIGTERM' }], '只送過一次 TERM，觀測失敗與之後確認死亡都不該補送任何信號');
    assert.equal(result.observationFailed, true, '這次執行期間真的發生過的觀測失敗必須保留（sticky），不因後來確認死亡就抹掉');
    assert.equal(result.clean, false, 'observationFailed 仍為 true，clean 不能是 true');
    assert.deepEqual(result.unconfirmedPids, [], '這個 pid 最終已經在 KILL 觀測窗被確認死亡，不應該留在最終未解清單');
  });

  // ---- 案例6：verified 與 unconfirmed 混合 ----
  await check('案例6：兩個目標，一個正常死亡、一個先unconfirmed後才確認死亡，互不干擾', async () => {
    const log = newLogger();
    const { kill, calls } = makeKillRecorder();
    const tA = tp(11006, 11006, 'fixture-f1', 'T-1', true);
    const tB = tp(11007, 11007, 'fixture-f2', 'T-1', true);
    const snapshot = makeSnapshotQueue([
      [row(11006, 11006, 'fixture-f1', 'T-1'), row(11007, 11007, 'fixture-f2', 'T-1')], // 初次驗證：兩個都相符
      [row(11007, 11007, '(fixture-f2)', 'T-1', 'S')], // TERM 等待：A 已死（查不到），B 未確認
      [], // 下一輪：B 也確認已死
    ]);
    const result = await stopProcessGroup([tA, tB], log, null, { snapshot, kill });
    const sigtermPids = calls.filter(c => c.signal === 'SIGTERM').map(c => c.pid).sort((a, b) => a - b);
    assert.deepEqual(sigtermPids, [-11007, -11006], '兩個都應該送過一次 TERM（各自 pgid group）');
    assert.equal(calls.filter(c => c.signal === 'SIGKILL').length, 0, 'A 一直沒有殘存可以觸發升級，不應該升級 KILL');
    assert.equal(result.clean, true, 'A／B 最終都確認死亡，應該 clean=true');
  });

  // ---- 案例7：root 先退出，escaped child 仍然存活（不觸發 unconfirmed／
  // pending 路徑，用來確認本次修法沒有動到既有「dead-root＋escaped-child」
  // 這個既有場景的既有邏輯）----
  await check('案例7：root先退出、escaped child存活→不對已消失的root pgid送信號，只處理escaped child', async () => {
    const log = newLogger();
    const { kill, calls } = makeKillRecorder();
    const root = tp(11008, 11008, 'root', 'T-1', true);
    const escaped = tp(11009, 99999, 'escaped-child', 'T-1b', false);
    const snapshot = makeSnapshotQueue([
      [row(11009, 99999, 'escaped-child', 'T-1b')], // root 11008 已經不在表裡；只有 escaped child
      [],
    ]);
    const result = await stopProcessGroup([root, escaped], log, null, { snapshot, kill });
    assert.deepEqual(calls, [{ pid: 11009, signal: 'SIGTERM' }], '不能有任何對負數 pid（group kill）的呼叫——沒有已驗證存活的 samePgid 成員背書 root 的 pgid');
    assert.equal(result.clean, true);
    assert.deepEqual(result.unconfirmedPids, []);
  });

  // ---- 案例8：TERM 尾聲驗證——一個目標撐過 TERM 第一輪、另一個在 TERM 尾聲
  // 變成未確認，但兩者第二輪都已查無此 pid，TERM 就地解決，從未進入 KILL
  // 階段（SIGKILL 呼叫數斷言為 0）。
  // 命名沿用 reviewer 複核 #499 的更正：原標題「KILL 前重新驗證」誤導成這個
  // 案例有實際進入 KILL 階段——它其實完全沒有觸發 escalatedToKill／
  // SIGKILL，是 TERM 階段內部就解決的情境。KILL 階段本身「殘存與未確認一起
  // 餵進同一次 KILL 呼叫」改由案例 15／16 覆蓋。 ----
  await check('案例8：TERM尾聲驗證（不進入KILL，SIGKILL=0）——TERM尾聲才變成未確認的目標在TERM第二輪就地解決，不會被誤納入KILL、也不會收到KILL信號', async () => {
    const log = newLogger();
    const { kill, calls } = makeKillRecorder();
    const tA = tp(11010, 11010, 'fixture-g1', 'T-1', true); // 這個會撐過 TERM 第一輪，第二輪才死
    const tB = tp(11011, 11011, 'fixture-g2', 'T-1', true); // 這個在 TERM 第一輪尾聲變成未確認
    const snapshot = makeSnapshotQueue([
      [row(11010, 11010, 'fixture-g1', 'T-1'), row(11011, 11011, 'fixture-g2', 'T-1')], // 初次驗證：都相符
      [row(11010, 11010, 'fixture-g1', 'T-1'), row(11011, 11011, '(fixture-g2)', 'T-1', 'S')], // TERM 第一輪：A 仍存活相符，B 變成未確認
      [], // 第二輪：兩者都查無此 pid
    ]);
    const result = await stopProcessGroup([tA, tB], log, null, { snapshot, kill });
    const sigtermPids = calls.filter(c => c.signal === 'SIGTERM').map(c => c.pid).sort((a, b) => a - b);
    assert.deepEqual(sigtermPids, [-11011, -11010], '初次驗證都相符，兩個都應該送過一次 TERM');
    assert.equal(calls.filter(c => c.signal === 'SIGKILL').length, 0, 'A 在 TERM 第二輪就已經查不到（死亡），不需要升級 KILL');
    assert.equal(result.clean, true, 'A 死亡、B 未確認後續也確認死亡，最終應該 clean=true');
    assert.deepEqual(result.unconfirmedPids, [], '不應該留下未解的 B');
  });

  // ---- 案例9：送信號前 argv 就取不到（沒有 E，且是唯一目標，沒有其他
  // residual 可以升級）----
  await check('案例9：送信號前argv就取不到→完全不送任何信號，之後才有機會確認死亡', async () => {
    const log = newLogger();
    const { kill, calls } = makeKillRecorder();
    const t = tp(11012, 11012, 'fixture-h', 'T-1', false);
    const snapshot = makeSnapshotQueue([
      [row(11012, 11012, '(fixture-h)', 'T-1', 'S')], // 送信號前：argv 就取不到
      [], // 之後：確認已死
    ]);
    const result = await stopProcessGroup([t], log, null, { snapshot, kill });
    assert.equal(calls.length, 0, '送信號前身分就觀測不完整，完全不應該送任何信號（安全不變條件）');
    assert.equal(result.clean, true, 'TERM 階段的觀測窗把送信號前就未確認的目標也一併纳入唯讀觀測，後續確認死亡後應該 clean=true');
    assert.equal(snapshot.callCount(), 2, '應該讀完 2 筆快照');
  });

  // ---- 案例10：unconfirmed 之後「看起來又穩定」（身分四要素重新完全相符）
  // 不因此重新授予送信號的權限；只有 dead 才能解除未確認狀態。跟「案例1／
  // 2：match→gone」對照：這裡驗證的是 match→unconfirmed→**又 match**（不是
  // →gone）時，不能被誤判成「原來只是短暫觀測不到，其實一直都在」而重新恢
  // 復成可送信號的已驗證集合。----
  await check('案例10：unconfirmed之後身分又完全相符→仍不解除未確認狀態、不重新授予送信號權限，直到之後才確認死亡', async () => {
    const log = newLogger();
    const { kill, calls } = makeKillRecorder();
    const t = tp(11013, 11013, 'fixture-i', 'T-1', true); // samePgid=true：如果誤判恢復成可送信號，會送 group KILL 給 -11013
    const snapshot = makeSnapshotQueue([
      [row(11013, 11013, 'fixture-i', 'T-1')], // 初次驗證：相符，送 TERM
      [row(11013, 11013, '(fixture-i)', 'T-1', 'S')], // TERM 第一輪：argv 取不到，未確認
      [row(11013, 11013, 'fixture-i', 'T-1')], // TERM 第二輪：身分四要素又完全相符了（「看起來穩定」）
      [], // 第三輪：真的死亡
    ]);
    const result = await stopProcessGroup([t], log, null, { snapshot, kill });
    assert.deepEqual(calls, [{ pid: -11013, signal: 'SIGTERM' }], '即使中途身分「看起來又穩定」，也絕對不能因此對它送出任何額外信號（尤其不能送 group KILL）');
    assert.equal(result.clean, true, '最終依既有死亡準則確認死亡，應該 clean=true');
    assert.equal(snapshot.callCount(), 4, '應該讀完全部 4 筆快照（包含「又穩定」那一輪與之後真正確認死亡那一輪）');
  });

  // ==== cleanup-observation-002（reviewer 複核 #499 §C，2026-09-28）：P1 常駐回歸 ====
  // 案例 11–14 直接對應 decision.md §C／cleanup/reviewer-probes.mjs 的反例，
  // 案例 15–16 把 reviewer 真正進入 KILL 階段的兩個補測（mixed-KILL-gone／
  // mixed-KILL-snapshot-error）轉成常駐測試。

  // ---- 案例11（a）：match→(node)→仍是(node)但startedAt OLD→NEW→gone ----
  // reviewer 反例（pending-startedAt-mismatch-then-gone）：command 因 argv 取
  // 不到而卡在括號，掩蓋掉 startedAt 其實已經變了這個事實。修正後 pgid／
  // startedAt 的比對要搶在括號判斷之前生效，第 3 筆快照（startedAt 已變）就
  // 要直接判 mismatch，不會再讀到第 4 筆。
  await check('案例11（a）：match→(node)→仍(node)但startedAt OLD→NEW→gone，預期clean=false、保留mismatch/abandoned', async () => {
    const log = newLogger();
    const { kill, calls } = makeKillRecorder();
    const t = tp(11014, 11014, 'fixture-j', 'OLD', false);
    const snapshot = makeSnapshotQueue([
      [row(11014, 11014, 'fixture-j', 'OLD')], // 初次驗證：相符，送 TERM
      [row(11014, 11014, '(node)', 'OLD', '?<E')], // TERM 第一輪：argv 取不到，pgid／startedAt 仍相符 → unconfirmed
      [row(11014, 11014, '(node)', 'NEW', '?<E')], // TERM 第二輪：仍是括號，但 startedAt 已經變了 → 應該直接判 mismatch，不能被括號遮蔽
      [], // 第三輪：不應該被消費到，因為案例應該在第 2 輪讀完（第 3 筆快照）就已經定案
    ]);
    const result = await stopProcessGroup([t], log, null, { snapshot, kill });
    assert.deepEqual(calls, [{ pid: 11014, signal: 'SIGTERM' }], '安全不變條件：不對未確認／已不符身分的目標送任何額外信號');
    assert.equal(result.clean, false, '不能因為 command 是括號就放行——已經觀測到的 startedAt 不符必須讓 clean=false');
    assert.deepEqual(result.abandonedPids, [11014], 'pgid／startedAt 比對要搶在括號判斷之前生效，第 3 筆快照就該判 mismatch→abandoned');
    assert.deepEqual(result.unconfirmedPids, [], '已經移進 abandonedPids，不應該同時還留在 unconfirmedPids');
    assert.equal(snapshot.callCount(), 3, '應該在第 3 筆快照（startedAt=NEW）就定案，不需要讀到第 4 筆「查無此 pid」');
  });

  // ---- 案例12（b）：同樣序列，第三筆改成 pgid 改變 ----
  await check('案例12（b）：match→(node)→仍(node)但pgid改變→gone，預期clean=false、保留mismatch/abandoned', async () => {
    const log = newLogger();
    const { kill, calls } = makeKillRecorder();
    const t = tp(11015, 11015, 'fixture-k', 'T-1', false);
    const snapshot = makeSnapshotQueue([
      [row(11015, 11015, 'fixture-k', 'T-1')], // 初次驗證：相符，送 TERM
      [row(11015, 11015, '(node)', 'T-1', '?<E')], // TERM 第一輪：argv 取不到，pgid／startedAt 仍相符 → unconfirmed
      [row(11015, 99999, '(node)', 'T-1', '?<E')], // TERM 第二輪：仍是括號，但 pgid 已經變了 → 應該直接判 mismatch
      [], // 不應該被消費到
    ]);
    const result = await stopProcessGroup([t], log, null, { snapshot, kill });
    assert.deepEqual(calls, [{ pid: 11015, signal: 'SIGTERM' }], '安全不變條件：不對未確認／已不符身分的目標送任何額外信號');
    assert.equal(result.clean, false, 'pgid 已經不符，不能因為 command 是括號就放行');
    assert.deepEqual(result.abandonedPids, [11015], 'pgid 比對要搶在括號判斷之前生效，第 3 筆快照就該判 mismatch→abandoned');
    assert.deepEqual(result.unconfirmedPids, [], '已經移進 abandonedPids');
    assert.equal(snapshot.callCount(), 3, '應該在第 3 筆快照（pgid 已變）就定案');
  });

  // ---- 案例13（c）：第一次被判為 unconfirmed 時，同一列就已經帶有欄位不符 ----
  // 驗證 `waitForDeathVerified` 對 `remaining` 的第一次分類（不是後續
  // `pending` 迴圈裡的分類）也要套用同一條修法——不是只修了 `pending` 那一段、
  // 漏了 `remaining` 那一段的另一份 classifyRow 呼叫。
  await check('案例13（c）：TERM第一輪唯一一次分類就同時是括號command＋pgid不符，第一次分類就要判mismatch而非unconfirmed', async () => {
    const log = newLogger();
    const { kill, calls } = makeKillRecorder();
    const t = tp(11016, 11016, 'fixture-l', 'T-1', false);
    const snapshot = makeSnapshotQueue([
      [row(11016, 11016, 'fixture-l', 'T-1')], // 初次驗證：相符，送 TERM
      [row(11016, 22222, '(node)', 'T-1', '?<E')], // TERM 第一輪：這一列「同時」是括號 command 且 pgid 不符
    ]);
    const result = await stopProcessGroup([t], log, null, { snapshot, kill });
    assert.deepEqual(calls, [{ pid: 11016, signal: 'SIGTERM' }], '安全不變條件：不對身分不符的目標送任何額外信號');
    assert.equal(result.clean, false, 'pgid 不符必須讓 clean=false');
    assert.deepEqual(result.abandonedPids, [11016], '第一次分類（remaining 迴圈，不是 pending 迴圈）就要判 mismatch，不能先誤判 unconfirmed 再也沒有機會修正');
    assert.deepEqual(result.unconfirmedPids, [], '不應該進最終 unconfirmedPids');
    assert.equal(snapshot.callCount(), 2, '應該在第 2 筆快照（第一次也是唯一一次 TERM 等待期間的分類）就定案，不需要進入 pending 迴圈的第二輪');
  });

  // ---- 案例14（d）：已經記錄 mismatch 之後才消失 → 仍然是 failed ----
  // 驗證「已經確認 mismatch、移出 pending」的目標是終態——不會因為後續某輪
  // 快照顯示它整個消失，就被重新撿回來、被誤判成「原來只是死了」而讓它從
  // abandonedPids 消失。同一次呼叫裡混一個正常會被解除的 unconfirmed 目標
  // （B），確保迴圈會真的跑到「A 消失後的那一輪」。
  await check('案例14（d）：已記錄mismatch之後才消失→仍然clean=false，abandonedPids不因此清空、不洩漏進unconfirmedPids', async () => {
    const log = newLogger();
    const { kill, calls } = makeKillRecorder();
    const tA = tp(11017, 11017, 'fixture-m', 'T-1', false); // 會在第 3 輪被判 mismatch→abandoned，第 4 輪「消失」
    const tB = tp(11018, 11018, 'fixture-n', 'T-1', false); // 陪跑：先 unconfirmed，第 4 輪才真正確認死亡，逼迴圈真的跑到第 4 輪
    const snapshot = makeSnapshotQueue([
      [row(11017, 11017, 'fixture-m', 'T-1'), row(11018, 11018, 'fixture-n', 'T-1')], // 初次驗證：都相符，各自送 TERM
      [row(11017, 11017, '(node)', 'T-1', '?<E'), row(11018, 11018, '(node)', 'T-1', '?<E')], // TERM 第一輪：都變成 unconfirmed→pending
      [row(11017, 33333, '(node)', 'T-1', '?<E'), row(11018, 11018, '(node)', 'T-1', '?<E')], // TERM 第二輪：A 的 pgid 變了→mismatch→abandoned；B 仍是括號，留在 pending
      [], // TERM 第三輪：兩者都查無此 pid——A 已經是終態，不會再被檢查；B 依既有死亡準則解除
    ]);
    const result = await stopProcessGroup([tA, tB], log, null, { snapshot, kill });
    const sigtermPids = calls.filter(c => c.signal === 'SIGTERM').map(c => c.pid).sort((a, b) => a - b);
    assert.deepEqual(sigtermPids, [11017, 11018], '兩個都應該送過一次 TERM（安全不變條件）');
    assert.equal(calls.length, 2, 'A 被判 mismatch 之後即使後來查無此 pid，也絕對不應該再對它送任何信號（不是「死亡」，是「已放棄追蹤」）');
    assert.equal(result.clean, false, 'A 曾經身分不符過，即使後來消失，這次執行仍然不算乾淨收尾');
    assert.deepEqual(result.abandonedPids, [11017], 'A 應該留在 abandonedPids，不因為後來查無此 pid 就被移除或改判');
    assert.deepEqual(result.unconfirmedPids, [], 'B 應該已經依既有死亡準則解除，不再留在 unconfirmedPids；A 也不應該洩漏進這裡');
    assert.equal(snapshot.callCount(), 4, '應該讀完全部 4 筆快照（B 撐到第 4 輪才解決）');
  });

  // ---- 案例15（e）／16（f）：把 reviewer 真正進入 KILL 階段的兩個補測轉成
  // 常駐測試（取自 cleanup/reviewer-probes.mjs 的 mixed-KILL-gone／
  // mixed-KILL-snapshot-error：a 全程已驗證存活、b 全程觀測不完整（括號），
  // TERM 10s 名目上限耗盡後兩者都升級進 KILL——a 因為仍是已驗證的 residual
  // 收到真正的 SIGKILL，b 只是 pendingUnconfirmed 跟著同一次 KILL 呼叫繼續
  // 唯讀觀測，全程不會收到任何信號）。這兩個案例會真的等滿 TERM 10s 名目上
  // 限（sleep 沒有被注入，屬於既有設計就沒有提供的注入點，維持原狀）。
  async function killMixCase(mode: 'gone' | 'snapshot-error'): Promise<{ result: StopResult }> {
    const aPid = mode === 'gone' ? 11019 : 11021;
    const bPid = mode === 'gone' ? 11020 : 11022;
    const log = newLogger();
    const calls: KillCall[] = [];
    let queries = 0;
    let killed = false;
    const kill: StopProcedureDeps['kill'] = (pid, signal) => {
      calls.push({ pid, signal });
      if (signal === 'SIGKILL') killed = true;
    };
    const snapshot: StopProcedureDeps['snapshot'] = () => {
      queries += 1;
      if (queries === 1) return [row(aPid, aPid, 'fixture-a', 'T-1'), row(bPid, bPid, 'fixture-b', 'T-1')];
      if (killed) {
        if (mode === 'snapshot-error') throw new Error('injected KILL snapshot failure（僅供測試，模擬 KILL 階段批次觀測本身失敗）');
        return [];
      }
      return [row(aPid, aPid, 'fixture-a', 'T-1'), { ...row(bPid, bPid, 'fixture-b', 'T-1'), command: '(node)' }];
    };
    const tA = tp(aPid, aPid, 'fixture-a', 'T-1', false);
    const tB = tp(bPid, bPid, 'fixture-b', 'T-1', false);
    const result = await stopProcessGroup([tA, tB], log, null, { snapshot, kill });
    assert.deepEqual(calls, [
      { pid: aPid, signal: 'SIGTERM' },
      { pid: bPid, signal: 'SIGTERM' },
      { pid: aPid, signal: 'SIGKILL' },
    ], `mode=${mode}：只有 a（全程已驗證存活的 residual）應該收到 SIGKILL，b（全程 pending／unconfirmed）絕對不能收到任何信號（含 KILL）`);
    return { result };
  }

  await check('案例15（e，取自reviewer probe-kill-gone）：verified殘存(a)+pending(b)一起進KILL，只有a收到假KILL，之後全部消失', async () => {
    const { result } = await killMixCase('gone');
    assert.equal(result.escalatedToKill, true, '應該真的升級到 KILL（a 全程是已驗證存活的 residual）');
    assert.deepEqual(result.abandonedPids, [], '不應該有身分不符的目標');
    assert.equal(result.clean, true, 'a／b 最終都依既有死亡準則確認消失，應該 clean=true（b 在 KILL 觀測窗被解決）');
    assert.deepEqual(result.unconfirmedPids, [], '不應該留下未解的未確認 pid');
  });

  await check('案例16（f，取自reviewer probe-kill-snapshot-error）：KILL階段批次觀測本身拋錯→observationFailed=true、clean=false，pending沒有遺失', async () => {
    const { result } = await killMixCase('snapshot-error');
    assert.equal(result.escalatedToKill, true, '應該真的升級到 KILL（a 全程是已驗證存活的 residual）');
    assert.equal(result.observationFailed, true, 'KILL 階段批次觀測本身失敗必須誠實反映在最終結果');
    assert.equal(result.clean, false, '觀測失敗時絕對不能回報 clean=true');
    assert.deepEqual([...result.unconfirmedPids].sort((a, b) => a - b), [11021, 11022], 'a／b 都不能因為 KILL 觀測失敗而遺失（不是「觀測不到＝乾淨」，也不是只保留其中一個；同一次 KILL 觀測窗兩者都還在 pending 時失敗）');
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
