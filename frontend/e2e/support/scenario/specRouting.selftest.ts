// specRouting.ts 的純函式測試——確認四個既有 approval 案與新的 recovery 案
// 各自只挑到對應的那一支 spec 檔，且不會混雜（playwright.scenario.config.ts
// 每次 invocation 只該收集一支 spec）。
//
// 執行：node e2e/support/scenario/specRouting.selftest.ts
import assert from 'node:assert/strict';
import { APPROVAL_SPEC_FILE, RECOVERY_SPEC_FILE, resolveScenarioSpecFile } from './specRouting.ts';

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

const APPROVAL_NAMES = ['commandExecution-allow', 'commandExecution-deny', 'fileChange-allow', 'fileChange-deny'];

check('四個既有 approval 案一律路由到 codexApproval.spec.ts', () => {
  for (const name of APPROVAL_NAMES) {
    assert.equal(resolveScenarioSpecFile(name), APPROVAL_SPEC_FILE, `${name} 應路由到 ${APPROVAL_SPEC_FILE}`);
  }
});

check('commandExecution-recovery 路由到 codexSessionRecovery.spec.ts', () => {
  assert.equal(resolveScenarioSpecFile('commandExecution-recovery'), RECOVERY_SPEC_FILE);
});

check('兩支 spec 檔名彼此不同（routing 有意義，不是恆等映射）', () => {
  assert.notEqual(APPROVAL_SPEC_FILE, RECOVERY_SPEC_FILE);
});

check('未指定 scenario 不 throw（拒絕契約交給 globalSetupScenario），回傳中性預設值', () => {
  assert.equal(resolveScenarioSpecFile(undefined), APPROVAL_SPEC_FILE);
});

check('未知 scenario 名稱不 throw，回傳中性預設值', () => {
  assert.equal(resolveScenarioSpecFile('commandExecution-timeout'), APPROVAL_SPEC_FILE);
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exitCode = 1;
