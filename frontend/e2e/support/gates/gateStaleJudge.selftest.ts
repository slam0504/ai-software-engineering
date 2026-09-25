// gateStaleJudge.ts 的正負案例（review #427 point 5：G1 的 digest 錯誤、
// S-P1 的 evidence_ref 錯誤都必須被各自的正式判定函式拒絕）。
//
// 執行：node --experimental-loader=./e2e/support/selftestJsToTsLoader.mjs e2e/support/gates/gateStaleJudge.selftest.ts
import assert from 'node:assert/strict';
import { judgeBindingsUnchanged, judgeStaleTransition } from './gateStaleJudge.js';

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

// ---- judgeBindingsUnchanged ----
check('正例：四個 binding 完全相符時 ok=true，mismatches 為空', () => {
  const actual = { spec_manifest: 'sha256:a', plan: 'sha256:b', risk_policy: 'sha256:c', permission_manifest: 'sha256:d' };
  const r = judgeBindingsUnchanged(actual, actual);
  assert.equal(r.ok, true);
  assert.deepEqual(r.mismatches, []);
});

check('負案例（review #427 point 5：G1 的 digest 錯誤必須被拒絕）：spec_manifest 不符時 ok=false 且列出該 kind', () => {
  const actual = { spec_manifest: 'sha256:WRONG', plan: 'sha256:b' };
  const expected = { spec_manifest: 'sha256:a', plan: 'sha256:b' };
  const r = judgeBindingsUnchanged(actual, expected);
  assert.equal(r.ok, false);
  assert.equal(r.mismatches.length, 1);
  assert.match(r.mismatches[0], /spec_manifest/);
});

check('負案例：多個 binding 同時不符時，全部列出不只回第一個', () => {
  const actual = { spec_manifest: 'X', plan: 'Y', risk_policy: 'c' };
  const expected = { spec_manifest: 'a', plan: 'b', risk_policy: 'c' };
  const r = judgeBindingsUnchanged(actual, expected);
  assert.equal(r.ok, false);
  assert.equal(r.mismatches.length, 2);
});

check('負案例：actual 缺少某個 expected 要求的 kind（undefined !== 期望值）判 fail', () => {
  const r = judgeBindingsUnchanged({}, { spec_manifest: 'a' });
  assert.equal(r.ok, false);
});

// ---- judgeStaleTransition ----
check('正例：cause／evidence_ref／to 皆符合預期時 ok=true', () => {
  const r = judgeStaleTransition({ to: 'stale', cause: 'spec_manifest changed', evidence_ref: 'sha256:x' }, { cause: 'spec_manifest changed', evidenceRef: 'sha256:x' });
  assert.equal(r.ok, true);
});

check('負案例（review #427 point 5：S-P1 的 evidence_ref 錯誤必須被拒絕）：evidence_ref 不符時 ok=false', () => {
  const r = judgeStaleTransition({ to: 'stale', cause: 'spec_manifest changed', evidence_ref: 'sha256:WRONG' }, { cause: 'spec_manifest changed', evidenceRef: 'sha256:x' });
  assert.equal(r.ok, false);
  assert.match(r.reason ?? '', /evidence_ref/);
});

check('負案例：cause 不符時 ok=false', () => {
  const r = judgeStaleTransition({ to: 'stale', cause: 'plan changed', evidence_ref: 'sha256:x' }, { cause: 'spec_manifest changed', evidenceRef: 'sha256:x' });
  assert.equal(r.ok, false);
  assert.match(r.reason ?? '', /cause/);
});

check('負案例：transition 是 undefined（找不到）時 ok=false', () => {
  const r = judgeStaleTransition(undefined, { cause: 'spec_manifest changed', evidenceRef: 'sha256:x' });
  assert.equal(r.ok, false);
  assert.match(r.reason ?? '', /不存在/);
});

check('負案例：to 不是 stale（例如誤傳了 superseded 的 transition）時 ok=false', () => {
  const r = judgeStaleTransition({ to: 'superseded', cause: 'spec_manifest changed', evidence_ref: 'sha256:x' }, { cause: 'spec_manifest changed', evidenceRef: 'sha256:x' });
  assert.equal(r.ok, false);
  assert.match(r.reason ?? '', /to 不是 stale/);
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exitCode = 1;
