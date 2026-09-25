// gateRouting.ts 的純函式測試——確認 E2E_GATE 的三個合法值各自路由到正確
// spec 檔名，且缺失／空字串／未知值一律 throw（不像 scenario 入口那樣回退
// 到中性預設值，見 gateRouting.ts 開頭說明；reviewer #424 (d) 裁定）。
//
// 執行：node --experimental-loader=./e2e/support/selftestJsToTsLoader.mjs e2e/support/gates/gateRouting.selftest.ts
import assert from 'node:assert/strict';
import { GATE_FLOW_SPEC_FILE, resolveGateFlow, specFileForFlow } from './gateRouting.js';

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

check('gate1 解析為 gate1，對應 gate1.spec.ts', () => {
  assert.equal(resolveGateFlow('gate1'), 'gate1');
  assert.equal(specFileForFlow('gate1'), 'gate1.spec.ts');
});

check('gate2 解析為 gate2，對應 gate2.spec.ts', () => {
  assert.equal(resolveGateFlow('gate2'), 'gate2');
  assert.equal(specFileForFlow('gate2'), 'gate2.spec.ts');
});

check('stale 解析為 stale，對應 stale.spec.ts', () => {
  assert.equal(resolveGateFlow('stale'), 'stale');
  assert.equal(specFileForFlow('stale'), 'stale.spec.ts');
});

check('三個 flow 各自對應到彼此不同的 spec 檔名（routing 有意義，非恆等映射）', () => {
  const files = new Set(Object.values(GATE_FLOW_SPEC_FILE));
  assert.equal(files.size, 3);
});

check('未設定（undefined）時 throw，不回退預設值', () => {
  assert.throws(() => resolveGateFlow(undefined), /未設定或為空字串/);
});

check('空字串時 throw，不當作未設定而忽略、也不當作合法值', () => {
  assert.throws(() => resolveGateFlow(''), /未設定或為空字串/);
});

check('未知值（例如 gate4）時 throw，不回退到任何預設案', () => {
  assert.throws(() => resolveGateFlow('gate4'), /未知的 E2E_GATE 值/);
});

check('大小寫不符（Gate1）視為未知值，不做大小寫寬容', () => {
  assert.throws(() => resolveGateFlow('Gate1'), /未知的 E2E_GATE 值/);
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exitCode = 1;
