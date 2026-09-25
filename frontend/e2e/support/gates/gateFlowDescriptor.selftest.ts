// gateFlowDescriptor.ts 的正負案例——離線合成 gate-flow.json，不需要真的
// 跑 globalSetup。
//
// 執行：node --experimental-loader=./e2e/support/selftestJsToTsLoader.mjs e2e/support/gates/gateFlowDescriptor.selftest.ts
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { GateFlowDescriptorError, verifyGateFlowDescriptor } from './gateFlowDescriptor.js';

let passed = 0;
let failed = 0;
function check(name: string, fn: () => void): void {
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

function tmpArtifactsDir(): string {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'gate-flow-descriptor-selftest-'));
}

check('正例：runId／flow／specFile 皆相符時回傳解析結果', () => {
  const dir = tmpArtifactsDir();
  fs.writeFileSync(path.join(dir, 'gate-flow.json'), JSON.stringify({ runId: 'run-1', flow: 'gate2', specFile: 'gate2.spec.ts' }));
  const d = verifyGateFlowDescriptor({ artifactsDir: dir, runId: 'run-1' }, 'gate2');
  assert.equal(d.flow, 'gate2');
});

check('負案例：檔案不存在時 throw', () => {
  const dir = tmpArtifactsDir();
  assert.throws(() => verifyGateFlowDescriptor({ artifactsDir: dir, runId: 'run-1' }, 'gate1'), GateFlowDescriptorError);
});

check('負案例：壞 JSON 時 throw', () => {
  const dir = tmpArtifactsDir();
  fs.writeFileSync(path.join(dir, 'gate-flow.json'), '{not json');
  assert.throws(() => verifyGateFlowDescriptor({ artifactsDir: dir, runId: 'run-1' }, 'gate1'), GateFlowDescriptorError);
});

check('負案例：runId 不符時 throw（防止讀到跨 run 殘留的舊 descriptor）', () => {
  const dir = tmpArtifactsDir();
  fs.writeFileSync(path.join(dir, 'gate-flow.json'), JSON.stringify({ runId: 'OLD-RUN', flow: 'gate1', specFile: 'gate1.spec.ts' }));
  assert.throws(() => verifyGateFlowDescriptor({ artifactsDir: dir, runId: 'run-1' }, 'gate1'), GateFlowDescriptorError);
});

check('負案例：flow 不符時 throw（例如 stale spec 誤讀到 gate2 的 descriptor）', () => {
  const dir = tmpArtifactsDir();
  fs.writeFileSync(path.join(dir, 'gate-flow.json'), JSON.stringify({ runId: 'run-1', flow: 'gate2', specFile: 'gate2.spec.ts' }));
  assert.throws(() => verifyGateFlowDescriptor({ artifactsDir: dir, runId: 'run-1' }, 'stale'), GateFlowDescriptorError);
});

check('負案例：specFile 與 flow 不成對時 throw（descriptor 內部不自洽）', () => {
  const dir = tmpArtifactsDir();
  fs.writeFileSync(path.join(dir, 'gate-flow.json'), JSON.stringify({ runId: 'run-1', flow: 'gate1', specFile: 'gate2.spec.ts' }));
  assert.throws(() => verifyGateFlowDescriptor({ artifactsDir: dir, runId: 'run-1' }, 'gate1'), GateFlowDescriptorError);
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exitCode = 1;
