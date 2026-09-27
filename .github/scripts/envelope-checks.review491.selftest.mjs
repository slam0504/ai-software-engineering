#!/usr/bin/env node
// envelope-checks.review491.selftest.mjs — B3a-CI-1 review round 7（#491
// G3：核定版本要精確比較，不能用 includes）。
//
// reviewer 已證實 `checkEnvelopeToolVersions()` 用 `actual.includes(requiredSubstring)`
// 核對版本，會放行前綴碰撞：Go 換成 `go1.26.50`、Wails 換成 `v2.13.00`、
// Playwright 換成 `Version 1.63.01`，這些都不是核定的
// `go1.26.5`／`v2.13.0`／`1.63.0`，但 violations 仍是空陣列（見
// `/Users/eason_tseng/b3a-evidence/2026-09-28-review491/boundary-probes.mjs`
// 的 case "wrong-prefix-tool-versions"，唯讀，不在這裡執行）。這裡直接對
// 純函式 checkEnvelopeToolVersions() 做單元測試，涵蓋前綴碰撞／非法格式／
// 預發版後綴三類，並保留既有 envelope-checks.selftest.mjs 的五案（那份不
// 動，只加嚴不放寬）。
//
// 執行：node .github/scripts/envelope-checks.review491.selftest.mjs
import assert from 'node:assert/strict';
import { checkEnvelopeToolVersions } from './envelope-checks.mjs';
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

function baseGoodEnvelope() {
  return {
    nodeVersionActual: REQUIRED_TOOL_VERSIONS.node,
    goVersionActual: `go version ${REQUIRED_TOOL_VERSIONS.go} darwin/amd64`,
    wailsVersionActual: `${REQUIRED_TOOL_VERSIONS.wails} (cli)`,
    playwrightVersionActual: `Version ${REQUIRED_TOOL_VERSIONS.playwright}`,
  };
}

check('baseline：四個工具版本都精確符合核定值 → violations=[]', () => {
  assert.deepEqual(checkEnvelopeToolVersions(baseGoodEnvelope()), []);
});

// --- reviewer#491 boundary-probes.mjs "wrong-prefix-tool-versions" 原文三案 ---

check('前綴碰撞（reviewer 原文）：goVersionActual="go version go1.26.50 darwin/amd64" → 被拒絕（不再被 includes("go1.26.5") 誤判相符）', () => {
  const violations = checkEnvelopeToolVersions({ ...baseGoodEnvelope(), goVersionActual: 'go version go1.26.50 darwin/amd64' });
  assert.ok(violations.some((v) => v.includes('goVersionActual') && v.includes('go1.26.50')), JSON.stringify(violations));
});

check('前綴碰撞（reviewer 原文）：wailsVersionActual="v2.13.00" → 被拒絕（不再被 includes("v2.13.0") 誤判相符）', () => {
  const violations = checkEnvelopeToolVersions({ ...baseGoodEnvelope(), wailsVersionActual: 'v2.13.00' });
  assert.ok(violations.some((v) => v.includes('wailsVersionActual') && v.includes('v2.13.00')), JSON.stringify(violations));
});

check('前綴碰撞（reviewer 原文）：playwrightVersionActual="Version 1.63.01" → 被拒絕（不再被 includes("1.63.0") 誤判相符）', () => {
  const violations = checkEnvelopeToolVersions({ ...baseGoodEnvelope(), playwrightVersionActual: 'Version 1.63.01' });
  assert.ok(violations.some((v) => v.includes('playwrightVersionActual') && v.includes('1.63.01')), JSON.stringify(violations));
});

// --- 本輪新增：非法格式 ---

check('非法格式：goVersionActual 完全不是版本字串 → 被拒絕（解析出 null）', () => {
  const violations = checkEnvelopeToolVersions({ ...baseGoodEnvelope(), goVersionActual: 'not-a-version-string-at-all' });
  assert.ok(violations.some((v) => v.includes('goVersionActual')), JSON.stringify(violations));
});

check('非法格式：wailsVersionActual 完全不是版本字串 → 被拒絕（解析出 null）', () => {
  const violations = checkEnvelopeToolVersions({ ...baseGoodEnvelope(), wailsVersionActual: 'garbage output, no version anywhere' });
  assert.ok(violations.some((v) => v.includes('wailsVersionActual')), JSON.stringify(violations));
});

check('非法格式：playwrightVersionActual 不是 "Version X.Y.Z" 開頭 → 被拒絕', () => {
  const violations = checkEnvelopeToolVersions({ ...baseGoodEnvelope(), playwrightVersionActual: '1.63.0（缺 "Version " 前綴）' });
  assert.ok(violations.some((v) => v.includes('playwrightVersionActual')), JSON.stringify(violations));
});

check('非法格式：nodeVersionActual 是空字串 → 被拒絕', () => {
  const violations = checkEnvelopeToolVersions({ ...baseGoodEnvelope(), nodeVersionActual: '' });
  assert.ok(violations.some((v) => v.includes('nodeVersionActual')), JSON.stringify(violations));
});

// --- 本輪新增：預發版後綴（緊接在核定版號後面，沒有空白分隔）---

check('預發版後綴：goVersionActual="go version go1.26.5-rc1 darwin/amd64" → 被拒絕（不是核定的 release 版本）', () => {
  const violations = checkEnvelopeToolVersions({ ...baseGoodEnvelope(), goVersionActual: 'go version go1.26.5-rc1 darwin/amd64' });
  assert.ok(violations.some((v) => v.includes('goVersionActual') && v.includes('go1.26.5-rc1')), JSON.stringify(violations));
});

check('預發版後綴：wailsVersionActual="v2.13.0-beta.1" → 被拒絕', () => {
  const violations = checkEnvelopeToolVersions({ ...baseGoodEnvelope(), wailsVersionActual: 'v2.13.0-beta.1' });
  assert.ok(violations.some((v) => v.includes('wailsVersionActual') && v.includes('v2.13.0-beta.1')), JSON.stringify(violations));
});

check('預發版後綴：playwrightVersionActual="Version 1.63.0-alpha" → 被拒絕', () => {
  const violations = checkEnvelopeToolVersions({ ...baseGoodEnvelope(), playwrightVersionActual: 'Version 1.63.0-alpha' });
  assert.ok(violations.some((v) => v.includes('playwrightVersionActual') && v.includes('1.63.0-alpha')), JSON.stringify(violations));
});

// --- 負控制：ANSI escape／前後空白正規化仍應通過（decision.md 明文允許正規化這兩種） ---

check('負控制：wailsVersionActual 帶前後空白與 ANSI escape → 正規化後仍精確相符，不被拒絕', () => {
  const violations = checkEnvelopeToolVersions({ ...baseGoodEnvelope(), wailsVersionActual: `\x1b[32m  ${REQUIRED_TOOL_VERSIONS.wails}  \x1b[0m` });
  assert.ok(!violations.some((v) => v.includes('wailsVersionActual')), JSON.stringify(violations));
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exitCode = 1;
