#!/usr/bin/env node
// envelope-checks.selftest.mjs — B3a-CI-1 review round 6（reviewer #489
// CHANGES_REQUIRED 第四次退回，F3）。
//
// 把 `/Users/eason_tseng/b3a-evidence/2026-09-28-review489/envelope-probe.mjs`
// 的五個 case（讀原始檔重現，原檔不改）收進永久 selftest：checkEnvelopeBaseAll()
// 先前只驗 envelope 欄位「非空字串」，從未核對實際 checkout tree／核定工具
// 版本——reviewer 已證實把 checkoutTreeSha 換成另一個合法 40-hex 值、
// Wails/Go/Playwright 換成任意版本仍 violations=[]。這裡直接對純函式
// checkEnvelopeBaseAll() 做單元測試（不經 CLI 子行程），baseline 案明確傳
// `actualNodeVersion: 'v26.10.0'`（正常 CI 核定 Node 版本）模擬「這份
// envelope 真的是在正常 CI 上產生」，其餘四案只改各自要測的欄位。
//
// 執行：node .github/scripts/envelope-checks.selftest.mjs
import assert from 'node:assert/strict';
import { makeBaseEnvelope, envelopeRepoRoot } from './__fixtures__/envelopeFixture.mjs';
import { checkEnvelopeBaseAll } from './envelope-checks.mjs';
import { REQUIRED_TOOL_VERSIONS } from './ci-e2e-entries.mjs';

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

const REPO_ROOT = envelopeRepoRoot();
function baseAll(patch = {}) {
  return checkEnvelopeBaseAll(
    { ...makeBaseEnvelope(), ...patch },
    { repoRootForGitCheck: REPO_ROOT, actualNodeVersion: REQUIRED_TOOL_VERSIONS.node },
  );
}

// --- reviewer#489 envelope-probe.mjs 的五個 case，收進永久 selftest ---
check('baseline：完全合法的 envelope → violations=[]', () => {
  assert.deepEqual(baseAll(), []);
});

check('wrong-checkout-tree：checkoutTreeSha 換成另一個合法 40-hex 值 → 被拒絕', () => {
  const violations = baseAll({ checkoutTreeSha: 'a'.repeat(40) });
  assert.ok(violations.length > 0, 'violations 不應為空');
  assert.ok(violations.some((v) => v.includes('checkoutTreeSha') && v.includes('HEAD^{tree}')), JSON.stringify(violations));
});

check('wrong-Wails：wailsVersionActual 換成 v9.9.9 → 被拒絕（不含核定版本 v2.13.0）', () => {
  const violations = baseAll({ wailsVersionActual: 'v9.9.9' });
  assert.ok(violations.some((v) => v.includes('wailsVersionActual') && v.includes(REQUIRED_TOOL_VERSIONS.wails)), JSON.stringify(violations));
});

check('wrong-Go：goVersionActual 換成 go1.24.0 → 被拒絕（不含核定版本 go1.26.5）', () => {
  const violations = baseAll({ goVersionActual: 'go version go1.24.0 darwin/amd64' });
  assert.ok(violations.some((v) => v.includes('goVersionActual') && v.includes(REQUIRED_TOOL_VERSIONS.go)), JSON.stringify(violations));
});

check('wrong-Playwright：playwrightVersionActual 換成 Version 1.0.0 → 被拒絕（不含核定版本 1.63.0）', () => {
  const violations = baseAll({ playwrightVersionActual: 'Version 1.0.0' });
  assert.ok(violations.some((v) => v.includes('playwrightVersionActual') && v.includes(REQUIRED_TOOL_VERSIONS.playwright)), JSON.stringify(violations));
});

// --- 額外反例：Chrome 依決定文件明文「只記錄有效實測版本，不額外釘版」，
// 換成任意版本不應該被這裡拒絕（負控制，證明沒有誤傷）。 ---
check('Chrome 版本不釘版（負控制）：chromeVersionActual 換成任意字串仍不因版本被拒絕', () => {
  const violations = baseAll({ chromeVersionActual: 'Google Chrome 1.0.0.0 (anything)' });
  assert.ok(!violations.some((v) => v.includes('chromeVersionActual')), JSON.stringify(violations));
});

// --- Node 版本：actualNodeVersion 沒有明確傳入時，預設用 process.version
//     （production 路徑的行為，不需要離線 selftest 額外設定）。 ---
check('Node 自我一致性（無 actualNodeVersion override）：nodeVersionActual=process.version 時通過自我一致性，但仍可能因不含核定版本而被工具版本 pin 拒絕', () => {
  const violations = checkEnvelopeBaseAll(
    { ...makeBaseEnvelope(), nodeVersionActual: process.version },
    { repoRootForGitCheck: REPO_ROOT },
  );
  assert.ok(!violations.some((v) => v.includes('nodeVersionActual') && v.includes('不符')), JSON.stringify(violations));
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exitCode = 1;
