#!/usr/bin/env node
// ci-e2e-entries.selftest.mjs — 純函式測試，不 spawn 任何行程。核對 13 個
// invocation 的路由表本身：批次分組數量、id 唯一性、label 字串精確、
// entriesForBatch／entryById 的邊界行為。
//
// 執行：node .github/scripts/ci-e2e-entries.selftest.mjs
import assert from 'node:assert/strict';
import {
  ENTRIES,
  BATCH_LABELS,
  entriesForBatch,
  entryById,
  realCommandForEntry,
} from './ci-e2e-entries.mjs';

let passed = 0;
let failed = 0;
function check(name, fn) {
  try {
    fn();
    passed += 1;
    console.log(`ok - ${name}`);
  } catch (e) {
    failed += 1;
    console.error(`FAIL - ${name}`);
    console.error(e);
  }
}

check('共 13 個 entry', () => {
  assert.equal(ENTRIES.length, 13);
});

check('id 全部唯一', () => {
  const ids = ENTRIES.map((e) => e.id);
  assert.equal(new Set(ids).size, ids.length, `重複 id：${JSON.stringify(ids)}`);
});

check('smoke=2／gates=3／scenarios=8（對照 v2 design 第 2 節矩陣）', () => {
  assert.equal(entriesForBatch('smoke').length, 2);
  assert.equal(entriesForBatch('gates').length, 3);
  assert.equal(entriesForBatch('scenarios').length, 8);
});

check('smoke 剛好是 default+controls', () => {
  const ids = entriesForBatch('smoke').map((e) => e.id).sort();
  assert.deepEqual(ids, ['controls', 'default']);
});

check('gates 剛好是 gate1/gate2/stale，各自帶正確 E2E_GATE', () => {
  const byId = Object.fromEntries(entriesForBatch('gates').map((e) => [e.id, e]));
  assert.equal(byId.gate1.env.E2E_GATE, 'gate1');
  assert.equal(byId.gate2.env.E2E_GATE, 'gate2');
  assert.equal(byId.stale.env.E2E_GATE, 'stale');
});

check('scenarios 8 案的 E2E_SCENARIO 值與 spec 路由對齊 codex/claude、approval/recovery 分流', () => {
  const byId = Object.fromEntries(entriesForBatch('scenarios').map((e) => [e.id, e]));
  assert.equal(byId['commandExecution-allow'].expectedSpecFiles[0], 'scenarios/codexApproval.spec.ts');
  assert.equal(byId['commandExecution-recovery'].expectedSpecFiles[0], 'scenarios/codexSessionRecovery.spec.ts');
  assert.equal(byId['claude-approval-allow'].expectedSpecFiles[0], 'scenarios/claudeApproval.spec.ts');
  assert.equal(byId['claude-approval-recovery'].expectedSpecFiles[0], 'scenarios/claudeSessionRecovery.spec.ts');
});

check('entriesForBatch 對未知批次 throw（不可依 labels 集合推測本次觸發）', () => {
  assert.throws(() => entriesForBatch('unknown-batch'));
});

check('entryById 對未知 id 回傳 null（不是 throw、不是 undefined 混用）', () => {
  assert.equal(entryById('does-not-exist'), null);
});

check('BATCH_LABELS 三個批次各自精確一個 label 字串', () => {
  assert.equal(BATCH_LABELS.smoke, 'b3a-ci-smoke');
  assert.equal(BATCH_LABELS.gates, 'b3a-ci-gates');
  assert.equal(BATCH_LABELS.scenarios, 'b3a-ci-scenarios');
});

check('realCommandForEntry 產生直接呼叫 run-e2e.mjs 的 argv（不經 npm）', () => {
  const entry = entryById('gate1');
  const { cmd, args } = realCommandForEntry(entry);
  assert.equal(cmd, 'node');
  assert.equal(args[0], 'frontend/e2e/scripts/run-e2e.mjs');
  assert.equal(args[1], entry.configPath);
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exitCode = 1;
