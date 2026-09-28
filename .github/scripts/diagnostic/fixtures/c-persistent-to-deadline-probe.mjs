// c-persistent-to-deadline-probe.mjs — b3a-ci2-platform-negative Run B 診斷
// 探針（新增診斷檔）。依 decision.md（review525）「Run B 第 2 點：C 的受控
// 分支證據……至少涵蓋『一直未確認到 deadline』」。
//
// 沿用 platform-negative-plan-001 的 c-pending-persists-probe.mjs 同一種合成
// 注入手法（跟既有 stopProcedure.selftest.ts／cleanup-observation-00{1,2} 的
// reviewer-probes.mjs 相同），呼叫**原路徑**的 91226366 原始
// `stopProcessGroup()`（未修改一行，golden-source-hashes.json 已先核對
// 指紋）。
//
// 合成輸入：追蹤目標 pid 990001，第一次快照身分完整相符（match）→ 送
// SIGTERM；之後每一輪批次快照的 command 皆為 `(fixture-worker)`（模擬 argv
// 取不到，合成資料，不是真的 ps 輸出），pgid／startedAt 皆維持跟追蹤記錄相
// 符（避免被誤判 mismatch，確保走「持續 unconfirmed」而不是「mismatch」分
// 支），從未出現 dead 或 mismatch，一路撐到 stopProcessGroup 自身
// TERM(10s)/KILL(5s) 名目上限到期。
//
// 明確界線：這只是受控分支測試——snapshot／kill 全部是合成輸入，不是真的
// ps 輸出、不是真的 process.kill。這不能被引用為「遠端某個真實行程的 argv
// 真的會消失」或「kernel 對行程身分的保證」；本探針沒有新增任何 signal 權
// 限（fakeKill 只記錄呼叫，不送真實訊號）。
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { stopProcessGroup } from '../../../../frontend/e2e/support/stopProcedure.ts';
import { HarnessLogger } from '../../../../frontend/e2e/support/logger.ts';

const outDir = process.argv[2];
if (!outDir) {
  process.stderr.write('usage: node --experimental-loader=<loader> c-persistent-to-deadline-probe.mjs <out-dir>\n');
  process.exit(2);
}
fs.mkdirSync(outDir, { recursive: true });

const TARGET = { pid: 990001, ppid: 1, pgid: 990001, command: 'fixture-worker', startedAt: 'Mon Sep 28 00:00:00 2026', samePgid: true };
const bracketedRow = () => ({
  pid: TARGET.pid, ppid: 1, pgid: TARGET.pgid, stat: 'S',
  startedAt: TARGET.startedAt, command: '(fixture-worker)',
});
const initialRow = () => ({
  pid: TARGET.pid, ppid: 1, pgid: TARGET.pgid, stat: 'S',
  startedAt: TARGET.startedAt, command: TARGET.command,
});

let snapshotCalls = 0;
function fakeSnapshot() {
  snapshotCalls += 1;
  if (snapshotCalls === 1) return [initialRow()];
  return [bracketedRow()];
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
  case: 'C-persistent-to-deadline（合成輸入，非自然觀測）',
  syntheticInput: true,
  fixtureDescription: '第一次快照身分完整相符→送 TERM；之後每一輪快照 command 皆為括號（argv 取不到，合成），從未變成 dead 或 mismatch，直到 stopProcessGroup 自身 TERM(10s)/KILL(5s) 名目上限到期',
  snapshotCalls,
  killCalls,
  elapsedMs,
  result,
  assertions: {},
};

try {
  assert.equal(result.clean, false, 'pending 從未解決時 clean 必須是 false');
  assert.deepEqual(result.residualPids, [], '身分從未確認過 match 以外的存活狀態，不應該進 residual');
  assert.deepEqual(result.abandonedPids, [], '從未觀測到 mismatch，不應該進 abandoned');
  assert.deepEqual(result.unconfirmedPids, [TARGET.pid], '從頭到尾都是括號 command，必須留在 unconfirmedPids');
  assert.equal(result.observationFailed, false, '這裡是「觀測到括號」不是「觀測工具本身失敗」，兩者是不同欄位');
  assert.equal(result.escalatedToKill, false, 'TERM 結束沒有已驗證存活的殘存，不應該送 KILL');
  assert.ok(killCalls.some((c) => c.signal === 'SIGTERM'), '送信號前身分相符，必須送過一次 TERM');
  assert.ok(!killCalls.some((c) => c.signal === 'SIGKILL'), '不應該送過 KILL');
  report.assertions = { allPassed: true };
} catch (e) {
  report.assertions = { allPassed: false, error: String(e) };
}

fs.writeFileSync(path.join(outDir, 'result.json'), `${JSON.stringify(report, null, 2)}\n`);
process.stdout.write(`${JSON.stringify(report, null, 2)}\n`);
process.exit(report.assertions.allPassed ? 0 : 1);
