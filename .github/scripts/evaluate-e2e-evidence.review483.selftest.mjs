#!/usr/bin/env node
// evaluate-e2e-evidence.review483.selftest.mjs — B3a-CI-1 review round 5
// （reviewer #483 CHANGES_REQUIRED 第三次退回，S1／S2）。
//
// 把 `/Users/eason_tseng/b3a-evidence/2026-09-28-review483/probe.mjs` 的八個
// case（讀原始檔重現，原檔不改）收進永久 selftest，並補齊 probe.mjs 因為本
// 輪同時修好 `__fixtures__/evidence-builder.mjs` 而不再能重現的兩個案例
// （scenario-domain-absent／claude-domain-absent 的原始 mutate 是 no-op，
// `makeGoodRunDir()` 本輪起會正確產生 scenario 領域證據，所以那兩個 case
// 原本的「domain absent」語意消失了——這裡改用 `skipDomainFile` 明確、逐一
// 移除每個 scenario entry 的必要領域證據檔，覆蓋全部 8 個 scenario entry，
// 不是只測 probe.mjs 原本挑的那兩個）。
//
// 沒有真實遠端 run 樣本核對過 scenario 領域證據的內容（見
// ci-e2e-entries.mjs requiredDomainFilesForEntry() 頭註）——這裡全部用
// `makeGoodRunDir()` 的合成 fixture，明確標示 synthetic，不冒充已驗證的真
// 實樣本。
//
// 執行：node .github/scripts/evaluate-e2e-evidence.review483.selftest.mjs
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { cpSync, mkdirSync, mkdtempSync, writeFileSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { ensureSelftestWorkRoot, cleanupWorkRootIfPortable } from './__fixtures__/selftestWorkroot.mjs';
import { entryById, requiredDomainFilesForEntry } from './ci-e2e-entries.mjs';
import { envelopeRepoRoot, makeStampedEnvelope } from './__fixtures__/envelopeFixture.mjs';
import { makeGoodRunDir, suiteWithOneTest, controlsSuites } from './__fixtures__/evidence-builder.mjs';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const EVALUATOR = path.join(__dirname, 'evaluate-e2e-evidence.mjs');
const ENVELOPE_REPO_ROOT = envelopeRepoRoot();

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

const SELFTEST_WORKROOT = ensureSelftestWorkRoot();
const EVIDENCE_ROOT = SELFTEST_WORKROOT.root;
function freshDir(label) {
  return mkdtempSync(path.join(EVIDENCE_ROOT, `${label}-`));
}

/** 建一個含（依 entry 的）合成 run 目錄的完整 case，回傳 {caseDir, packageDir, runDir}。 */
function buildCase(label, entryId, { skipDomainFile = null, domainOverrides = {}, specsMutator = null } = {}) {
  const entry = entryById(entryId);
  const caseDir = freshDir(label);
  const artifactsRoot = path.join(caseDir, 'artifacts-root');
  const runId = `${label}-run`;
  const file = path.basename(entry.expectedSpecFiles[0]);
  const sub = path.dirname(entry.expectedSpecFiles[0]);
  const rootDir = entry.id === 'controls' ? '/synthetic/frontend/e2e/controls' : (sub === '.' ? '/synthetic/frontend/e2e' : `/synthetic/frontend/e2e/${sub}`);
  let specs = entry.id === 'controls' ? controlsSuites() : [suiteWithOneTest(file, 'actual test', 'actual test')];
  if (specsMutator) specs = specsMutator(specs);
  const runDir = makeGoodRunDir(artifactsRoot, runId, {
    entry, rootDir, specs, skipDomainFile, domainOverrides,
  });
  writeFileSync(path.join(caseDir, 'e2e-wrapper-status.json'), JSON.stringify({
    status: 'completed', wrapperRc: 0, childRc: 0, childConfirmedGone: 'true', producerErrors: [],
  }, null, 2));
  writeFileSync(path.join(caseDir, 'e2e.rc'), '0\n');
  writeFileSync(path.join(caseDir, 'new-run-dirs.json'), JSON.stringify([runDir], null, 2));
  const stamped = makeStampedEnvelope(entry, runId, {}, { repoRoot: ENVELOPE_REPO_ROOT });
  writeFileSync(path.join(caseDir, 'envelope.json'), JSON.stringify(stamped, null, 2));
  const packageDir = path.join(caseDir, 'package');
  mkdirSync(path.join(packageDir, 'artifacts'), { recursive: true });
  for (const f of ['e2e-wrapper-status.json', 'e2e.rc', 'new-run-dirs.json', 'envelope.json']) {
    cpSync(path.join(caseDir, f), path.join(packageDir, f));
  }
  cpSync(runDir, path.join(packageDir, 'artifacts'), { recursive: true });
  return { caseDir, packageDir, runDir };
}

function runEval(caseDir, packageDir, entryId) {
  const r = spawnSync('node', [EVALUATOR, caseDir, packageDir, entryId], {
    encoding: 'utf8', timeout: 15_000,
    // review round 6（#489 F3）：見 evaluate-e2e-evidence.selftest.mjs 同名
    // env var 的頭註。
    env: { ...process.env, CI_E2E_REPO_ROOT: ENVELOPE_REPO_ROOT, CI_E2E_SELFTEST_ACTUAL_NODE_VERSION: 'v26.10.0' },
  });
  return { rc: r.status, stdout: r.stdout, stderr: r.stderr };
}

// --- S1：8 個 scenario entry，各自綠燈（證明 makeGoodRunDir 產生的 fixture 真的完整正確） ---
const SCENARIO_IDS = [
  'commandExecution-allow', 'commandExecution-deny', 'fileChange-allow', 'fileChange-deny',
  'commandExecution-recovery', 'claude-approval-allow', 'claude-approval-deny', 'claude-approval-recovery',
];
for (const id of SCENARIO_IDS) {
  check(`S1 綠燈：${id} 的合成 fixture（含 scenario 領域證據）完整正確 → exit 0`, () => {
    const { caseDir, packageDir } = buildCase(`s1-green-${id}`, id);
    const { rc, stdout } = runEval(caseDir, packageDir, id);
    assert.equal(rc, 0, `stdout: ${stdout}`);
  });
}

// --- S1：8 個 scenario entry，各自缺第一個必要領域證據檔 → 紅燈 ---
for (const id of SCENARIO_IDS) {
  const domainFiles = requiredDomainFilesForEntry(entryById(id));
  const missing = domainFiles[0];
  check(`S1 反例：${id} 缺 ${missing}（reviewer#483 scenario-domain-absent／claude-domain-absent 的完整覆蓋版）→ exit 1`, () => {
    const { caseDir, packageDir } = buildCase(`s1-missing-${id}`, id, { skipDomainFile: missing });
    const { rc, stdout } = runEval(caseDir, packageDir, id);
    assert.equal(rc, 1);
    assert.match(stdout, new RegExp(`${missing.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')} 不存在`));
  });
}

// --- S1：identity 反例（scenario-config.json 的 scenario 欄位被換成別案） ---
check('S1 反例：commandExecution-allow 的 scenario-config.json.scenario 被換成 commandExecution-deny → identity 異常', () => {
  const { caseDir, packageDir } = buildCase('s1-identity-scenario', 'commandExecution-allow', {
    domainOverrides: { 'scenario-config.json': { scenario: 'commandExecution-deny' } },
  });
  const { rc, stdout } = runEval(caseDir, packageDir, 'commandExecution-allow');
  assert.equal(rc, 1);
  assert.match(stdout, /scenario-config\.json 的 scenario=.*identity 異常/);
});

// --- S1：identity 反例（app-audit.jsonl 的 codex_approval_decision.decision 與 entry 期望不符） ---
check('S1 反例：commandExecution-allow（accept 案）的 app-audit.jsonl decision 被換成 decline → exit 1', () => {
  const { caseDir, packageDir } = buildCase('s1-identity-decision', 'commandExecution-allow', {
    domainOverrides: { 'app-audit.jsonl': { decision: 'decline' } },
  });
  const { rc, stdout } = runEval(caseDir, packageDir, 'commandExecution-allow');
  assert.equal(rc, 1);
  assert.match(stdout, /codex_approval_decision\.decision=.*，預期 "accept"/);
});

// --- S1：identity 反例（claude-expectation.json 的 approval.decision 與 entry 期望不符） ---
check('S1 反例：claude-approval-allow（allow 案）的 claude-expectation.json approval.decision 被換成 deny → exit 1', () => {
  const { caseDir, packageDir } = buildCase('s1-identity-claude-decision', 'claude-approval-allow', {
    domainOverrides: { 'claude-expectation.json': { approval: { decision: 'deny' } } },
  });
  const { rc, stdout } = runEval(caseDir, packageDir, 'claude-approval-allow');
  assert.equal(rc, 1);
  assert.match(stdout, /claude-expectation\.json 的 approval\.decision=.*，預期 "allow"/);
});

// --- S2：reviewer#483 gate-journal-absent（bodyStatus=failed 但 finalStatus=passed 自相矛盾） ---
check('S2 反例（reviewer#483 gate-journal-absent）：gate-evidence.json bodyStatus=failed 但 finalStatus=passed → exit 1', () => {
  const { caseDir, packageDir } = buildCase('s2-gate-bodystatus', 'gate1', {
    domainOverrides: { 'gate-evidence.json': { bodyStatus: 'failed' } },
  });
  const { rc, stdout } = runEval(caseDir, packageDir, 'gate1');
  assert.equal(rc, 1);
  assert.match(stdout, /gate-evidence\.json 的 bodyStatus="failed"，與 finalStatus="passed" 矛盾/);
});

// --- S2：reviewer#483 ordinary-result-error（普通 passed 結果帶 afterEach errors） ---
check('S2 反例（reviewer#483 ordinary-result-error）：gate1 的 passed test 帶 results[0].errors 非空 → exit 1', () => {
  const { caseDir, packageDir } = buildCase('s2-ordinary-error', 'gate1', {
    specsMutator: (specs) => {
      specs[0].specs[0].tests[0].results[0].errors = [{ message: 'afterEach failed' }];
      return specs;
    },
  });
  const { rc, stdout } = runEval(caseDir, packageDir, 'gate1');
  assert.equal(rc, 1);
  assert.match(stdout, /results\[0\]\.errors 有 1 筆（成功宣稱下應為空）/);
});

// --- S2：reviewer#483 controls-conflicting-error ---
check('S2 反例（reviewer#483 controls-conflicting-error）：control A 的 error 與 errors[0] 互相矛盾 → exit 1', () => {
  const { caseDir, packageDir } = buildCase('s2-controls-conflict', 'controls', {
    specsMutator: (specs) => {
      const controlA = specs[0].specs[0].tests[0];
      controlA.results[0].error = { message: 'unrelated evidence error' };
      return specs;
    },
  });
  const { rc, stdout } = runEval(caseDir, packageDir, 'controls');
  assert.equal(rc, 1);
  assert.match(stdout, /正規化核對後不是恰好一筆目標斷言錯誤/);
});

// --- S2：reviewer#483 controls-domain-null ---
check('S2 反例（reviewer#483 controls-domain-null）：control-a-evidence.json 的 wrapperEvidence=null → exit 1', () => {
  const { caseDir, packageDir } = buildCase('s2-controls-null', 'controls', {
    domainOverrides: { 'control-a-evidence.json': { wrapperEvidence: null } },
  });
  const { rc, stdout } = runEval(caseDir, packageDir, 'controls');
  assert.equal(rc, 1);
  assert.match(stdout, /control-a-evidence\.json 的 wrapperEvidence 不是非空陣列/);
});

// --- S2：reviewer#483 reverse-wrong-expected ---
check('S2 反例（reviewer#483 reverse-wrong-expected）：reverse-check 的 expectedStatus 被換成 failed（actual 仍 skipped）→ exit 1', () => {
  const { caseDir, packageDir } = buildCase('s2-reverse-expected', 'controls', {
    specsMutator: (specs) => {
      specs[1].specs[0].tests[0].expectedStatus = 'failed';
      return specs;
    },
  });
  const { rc, stdout } = runEval(caseDir, packageDir, 'controls');
  assert.equal(rc, 1);
  assert.match(stdout, /reverse-check\.spec\.ts expectedStatus="failed"，預期 "skipped"/);
});

console.log(`\n${passed} passed, ${failed} failed`);
cleanupWorkRootIfPortable(SELFTEST_WORKROOT, { failed });
process.exit(failed > 0 ? 1 : 0);
