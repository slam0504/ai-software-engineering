// c-unconfirmed-then-gone-probe.mjs — b3a-ci2-platform-negative Run B 診斷
// 探針（新增診斷檔）。依 decision.md（review525）「Run B 第 2 點：C 的受控
// 分支證據……至少涵蓋『未確認之後消失』」。
//
// 重用既有案例邏輯——跟 frontend/e2e/support/stopProcedure.selftest.ts
// 「案例6：兩個目標，一個正常死亡、一個先 unconfirmed 後才確認死亡」同一種
// snapshot 序列形狀（本探針只放單一目標，聚焦在「先 unconfirmed 後 gone」
// 這一條路徑本身），呼叫**原路徑**的 91226366 原始 `stopProcessGroup()`（未
// 修改一行，golden-source-hashes.json 已先核對指紋）。
//
// 合成輸入：第一輪快照身分完整相符（match）→ 送 SIGTERM；第二輪（TERM 等待
// 期間）command 變成括號（argv 取不到，合成，進入 unconfirmed）；第三輪查無
// 此 pid（合成的「確認已死」），在 TERM 階段內就解決，不進入 KILL。
//
// 明確界線：這只是受控分支測試——snapshot／kill 全部是合成輸入，不是真的
// ps 輸出、不是真的 process.kill。這不能被引用為「遠端某個真實行程的 argv
// 真的會消失」的保證；本探針沒有新增任何 signal 權限（fakeKill 只記錄呼
// 叫，不送真實訊號）。
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { stopProcessGroup } from '../../../../frontend/e2e/support/stopProcedure.ts';
import { HarnessLogger } from '../../../../frontend/e2e/support/logger.ts';

const outDir = process.argv[2];
if (!outDir) {
  process.stderr.write('usage: node --experimental-loader=<loader> c-unconfirmed-then-gone-probe.mjs <out-dir>\n');
  process.exit(2);
}
fs.mkdirSync(outDir, { recursive: true });

const TARGET = { pid: 990002, ppid: 1, pgid: 990002, command: 'fixture-worker-2', startedAt: 'Mon Sep 28 00:00:00 2026', samePgid: true };
const initialRow = () => ({
  pid: TARGET.pid, ppid: 1, pgid: TARGET.pgid, stat: 'S',
  startedAt: TARGET.startedAt, command: TARGET.command,
});
const bracketedRow = () => ({
  pid: TARGET.pid, ppid: 1, pgid: TARGET.pgid, stat: 'S',
  startedAt: TARGET.startedAt, command: '(fixture-worker-2)',
});

let snapshotCalls = 0;
function fakeSnapshot() {
  snapshotCalls += 1;
  if (snapshotCalls === 1) return [initialRow()]; // 初次驗證：相符，送 TERM
  if (snapshotCalls === 2) return [bracketedRow()]; // TERM 等待期間：未確認（argv 取不到）
  return []; // 之後：查無此 pid，確認已死——「未確認之後消失」
}

const killCalls = [];
function fakeKill(pid, signal) {
  killCalls.push({ pid, signal, atMs: Date.now() });
}

const startedAtMs = Date.now();
const logger = new HarnessLogger(outDir);
const result = await stopProcessGroup(
  [TARGET],
  logger,
  null,
  { snapshot: fakeSnapshot, kill: fakeKill },
);
const elapsedMs = Date.now() - startedAtMs;

const report = {
  case: 'C-unconfirmed-then-gone（合成輸入，非自然觀測）',
  syntheticInput: true,
  fixtureDescription: '第一輪快照相符→送TERM；第二輪括號command（unconfirmed）；第三輪查無此pid（確認已死），TERM 階段內解決，不進 KILL',
  snapshotCalls,
  killCalls,
  elapsedMs,
  result,
  assertions: {},
};

try {
  assert.equal(result.clean, true, '最終確認已死，應該 clean=true');
  assert.deepEqual(result.unconfirmedPids, [], '最終已確認死亡，不應該留在 unconfirmedPids（中途曾經 unconfirmed 但已解決）');
  assert.deepEqual(result.abandonedPids, [], '從未觀測到 mismatch，不應該進 abandoned');
  assert.equal(result.escalatedToKill, false, 'TERM 階段內已解決，不應該送 KILL');
  assert.ok(killCalls.some((c) => c.signal === 'SIGTERM'), '送信號前身分相符，必須送過一次 TERM');
  assert.ok(!killCalls.some((c) => c.signal === 'SIGKILL'), '不應該送過 KILL');
  assert.equal(snapshotCalls >= 3, true, '必須至少經過三輪快照才能走完 match→unconfirmed→gone 這條路徑');
  report.assertions = { allPassed: true };
} catch (e) {
  report.assertions = { allPassed: false, error: String(e) };
}

fs.writeFileSync(path.join(outDir, 'result.json'), `${JSON.stringify(report, null, 2)}\n`);
process.stdout.write(`${JSON.stringify(report, null, 2)}\n`);
process.exit(report.assertions.allPassed ? 0 : 1);
