#!/usr/bin/env node
// evaluate-e2e-evidence.review480.selftest.mjs — B3a-CI-1 review round 3
// （reviewer #480 CHANGES_REQUIRED）。把 reviewer 在
// `/Users/eason_tseng/b3a-evidence/2026-09-28-review479/probe.py` 裡的八個
// case 收進永久 selftest（原探針只讀不改，複製到
// `/Users/eason_tseng/b3a-evidence/ci-1/attempt-002/repro/` 另外重現一次；
// 這裡是把同一組反例正式收進 repo 的回歸測試，兩者互相佐證、不是取代）。
//
// 每個 mutation 都對照真實 fixture（`__fixtures__/real/`）做最小修改，不是
// 憑空編造 schema。
//
// 執行：node .github/scripts/evaluate-e2e-evidence.review480.selftest.mjs
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync, unlinkSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { ensureSelftestWorkRoot, cleanupWorkRootIfPortable } from './__fixtures__/selftestWorkroot.mjs';
import { entryById } from './ci-e2e-entries.mjs';
import { envelopeRepoRoot, makeStampedEnvelope } from './__fixtures__/envelopeFixture.mjs';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const EVALUATOR = path.join(__dirname, 'evaluate-e2e-evidence.mjs');
const REAL_FIXTURES_ROOT = path.join(__dirname, '__fixtures__', 'real');
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

function findRealFixtureDir(prefix) {
  const name = readdirSync(REAL_FIXTURES_ROOT).find((n) => n.startsWith(prefix));
  if (!name) throw new Error(`找不到 __fixtures__/real/ 底下以 "${prefix}" 開頭的目錄`);
  return path.join(REAL_FIXTURES_ROOT, name);
}

// review round 4（#480 R2 剩餘缺口）：entryId 預設從 fixturePrefix 推回
// （這個檔案裡全部呼叫都用 'gate1-pass'／entryId='gate1'，跟
// real-fixtures.selftest.mjs 同一套慣例），自動蓋一份合法的
// envelope.json，讓既有 case 不需要逐一修改就能繼續代表「證據齊全且正
// 確」。
function prepareCase(label, fixturePrefix, {
  wrapperOverrides = {},
  mutate = null,
  entryId = fixturePrefix.replace(/-(pass|failed)$/, ''),
} = {}) {
  const src = findRealFixtureDir(fixturePrefix);
  const runIdName = path.basename(src).replace(/^[a-z0-9]+-(pass|failed)-/, '');
  const caseDir = freshDir(label);
  const runCopy = path.join(caseDir, 'artifacts-root', runIdName);
  cpSync(src, runCopy, { recursive: true });
  if (mutate) mutate(runCopy);

  const status = {
    status: 'completed', wrapperRc: 0, childRc: 0, childConfirmedGone: 'true', producerErrors: [],
    startIso: '2026-09-28T00:00:00.000Z', endIso: '2026-09-28T00:10:00.000Z',
    ...wrapperOverrides,
  };
  writeFileSync(path.join(caseDir, 'e2e-wrapper-status.json'), JSON.stringify(status, null, 2));
  writeFileSync(path.join(caseDir, 'e2e.rc'), `${status.wrapperRc}\n`);
  writeFileSync(path.join(caseDir, 'new-run-dirs.json'), JSON.stringify([runCopy], null, 2));

  const stamped = makeStampedEnvelope(entryById(entryId), runIdName, {}, { repoRoot: ENVELOPE_REPO_ROOT });
  writeFileSync(path.join(caseDir, 'envelope.json'), JSON.stringify(stamped, null, 2));

  const packageDir = path.join(caseDir, 'package');
  mkdirSync(path.join(packageDir, 'artifacts'), { recursive: true });
  for (const f of ['e2e-wrapper-status.json', 'e2e.rc', 'new-run-dirs.json', 'envelope.json']) {
    cpSync(path.join(caseDir, f), path.join(packageDir, f));
  }
  cpSync(runCopy, path.join(packageDir, 'artifacts'), { recursive: true });
  return { caseDir, runCopy, packageDir };
}

function runEval(caseDir, packageDir, entryId) {
  const r = spawnSync('node', [EVALUATOR, caseDir, packageDir, entryId], {
    encoding: 'utf8',
    timeout: 15_000,
    env: {
      ...process.env,
      CI_E2E_REPO_ROOT: ENVELOPE_REPO_ROOT,
      // review round 6（#489 F3）：見 evaluate-e2e-evidence.selftest.mjs 同名
      // env var 的頭註。
      CI_E2E_SELFTEST_ACTUAL_NODE_VERSION: 'v26.10.0',
    },
  });
  return { rc: r.status, stdout: r.stdout, stderr: r.stderr };
}

// reviewer probe.py case: baseline（已在 evaluate-e2e-evidence.real-fixtures.selftest.mjs 涵蓋，這裡不重複）

// reviewer probe.py case: TEST_FAILED-present
check('R1／reviewer#480 case「TEST_FAILED-present」：真實 gate1 正例額外放一個 TEST_FAILED → exit 1', () => {
  const { caseDir, packageDir } = prepareCase('r480-testfailed', 'gate1-pass', {
    mutate: (run) => writeFileSync(path.join(run, 'TEST_FAILED'), 'evidence flush failure\n'),
  });
  const { rc, stdout } = runEval(caseDir, packageDir, 'gate1');
  assert.equal(rc, 1);
  assert.match(stdout, /TEST_FAILED 存在/);
  rmSync(caseDir, { recursive: true, force: true });
});

// reviewer probe.py case: gate-evidence-missing
check('R1／reviewer#480 case「gate-evidence-missing」：真實 gate1 正例拿掉 gate-evidence.json／gate-flow.json → exit 1', () => {
  const { caseDir, packageDir } = prepareCase('r480-gateevidence', 'gate1-pass', {
    mutate: (run) => {
      unlinkSync(path.join(run, 'gate-evidence.json'));
      unlinkSync(path.join(run, 'gate-flow.json'));
    },
  });
  const { rc, stdout } = runEval(caseDir, packageDir, 'gate1');
  assert.equal(rc, 1);
  assert.match(stdout, /gate-evidence\.json 不存在/);
  assert.match(stdout, /gate-flow\.json 不存在/);
  rmSync(caseDir, { recursive: true, force: true });
});

// reviewer probe.py case: run-env-wrong-identity
check('R2／reviewer#480 case「run-env-wrong-identity」：真實 gate1 正例的 run-env.json 換成別的 run/SHA → exit 1', () => {
  const { caseDir, packageDir } = prepareCase('r480-runenv', 'gate1-pass', {
    mutate: (run) => writeFileSync(path.join(run, 'run-env.json'), JSON.stringify({ runId: 'different-run', head: 'wrong-sha' })),
  });
  const { rc, stdout } = runEval(caseDir, packageDir, 'gate1');
  assert.equal(rc, 1);
  assert.match(stdout, /run-env\.json 的 runId="different-run" 與目錄名不一致/);
  rmSync(caseDir, { recursive: true, force: true });
});

// reviewer probe.py case: wrapper-contradiction
check('R3／reviewer#480 case「wrapper-contradiction」：wrapperRc=0 但 childRc=7／childConfirmedGone=unknown／producerErrors 非空 → exit 1', () => {
  const { caseDir, packageDir } = prepareCase('r480-wrappercontra', 'gate1-pass', {
    wrapperOverrides: { childRc: 7, childConfirmedGone: 'unknown', producerErrors: ['failed to persist'] },
  });
  const { rc, stdout } = runEval(caseDir, packageDir, 'gate1');
  assert.equal(rc, 1);
  assert.match(stdout, /childRc=7/);
  assert.match(stdout, /childConfirmedGone="unknown"/);
  assert.match(stdout, /producerErrors 有 1 筆/);
  rmSync(caseDir, { recursive: true, force: true });
});

// reviewer probe.py case: global-errors-malformed
check('R3／reviewer#480 case「global-errors-malformed」：report.errors 換成 object（非 array）→ exit 1', () => {
  const { caseDir, packageDir } = prepareCase('r480-globalerrors', 'gate1-pass', {
    mutate: (run) => {
      const p = path.join(run, 'playwright-results.json');
      const d = JSON.parse(readFileSync(p, 'utf8'));
      d.errors = { error: 'not an array' };
      writeFileSync(p, JSON.stringify(d, null, 2));
    },
  });
  const { rc, stdout } = runEval(caseDir, packageDir, 'gate1');
  assert.equal(rc, 1);
  assert.match(stdout, /errors 不是陣列（實際型別：object）/);
  rmSync(caseDir, { recursive: true, force: true });
});

// reviewer probe.py case: controls-additional-error（已在 real-fixtures selftest 涵蓋，這裡略）
// reviewer probe.py case: reverse-inconsistent（已在 real-fixtures selftest 涵蓋，這裡略）

// --- R2 新增反例：gate-flow.json 本身被換成別的 flow（同一份 run-env 沒動，只換領域證據）---
check('R2 新增反例：gate-flow.json 的 flow 欄位被改成 gate2（run-env／JSON reporter 都還是 gate1）→ identity 異常', () => {
  const { caseDir, packageDir } = prepareCase('r2-gateflowswap', 'gate1-pass', {
    mutate: (run) => {
      const p = path.join(run, 'gate-flow.json');
      const d = JSON.parse(readFileSync(p, 'utf8'));
      d.flow = 'gate2';
      writeFileSync(p, JSON.stringify(d, null, 2));
    },
  });
  const { rc, stdout } = runEval(caseDir, packageDir, 'gate1');
  assert.equal(rc, 1);
  assert.match(stdout, /gate-flow\.json 的 flow="gate2"，預期 "gate1"/);
  rmSync(caseDir, { recursive: true, force: true });
});

// --- R2 新增反例：gate-evidence.json 的 runId 被換成別的 run（跨 run 資料互換）---
check('R2 新增反例：gate-evidence.json 的 runId 被換成另一個 run → identity 異常（跨 run 資料互換）', () => {
  const { caseDir, packageDir } = prepareCase('r2-crossrun', 'gate1-pass', {
    mutate: (run) => {
      const p = path.join(run, 'gate-evidence.json');
      const d = JSON.parse(readFileSync(p, 'utf8'));
      d.runId = '20260101T000000Z-deadbeef';
      writeFileSync(p, JSON.stringify(d, null, 2));
    },
  });
  const { rc, stdout } = runEval(caseDir, packageDir, 'gate1');
  assert.equal(rc, 1);
  assert.match(stdout, /gate-evidence\.json 的 runId="20260101T000000Z-deadbeef" 與目錄名不一致/);
  rmSync(caseDir, { recursive: true, force: true });
});

// --- R2 新增反例：execution-entry.json 的 entry 欄位不符（gates 應為 'default'）---
check('R2 新增反例：execution-entry.json 的 entry 欄位被改成 "scenario"（gates 應為 "default"）→ identity 異常', () => {
  const { caseDir, packageDir } = prepareCase('r2-execentry', 'gate1-pass', {
    mutate: (run) => writeFileSync(path.join(run, 'execution-entry.json'), JSON.stringify({ entry: 'scenario' })),
  });
  const { rc, stdout } = runEval(caseDir, packageDir, 'gate1');
  assert.equal(rc, 1);
  assert.match(stdout, /execution-entry\.json 的 entry="scenario"，預期 "default"/);
  rmSync(caseDir, { recursive: true, force: true });
});

console.log(`\n${passed} passed, ${failed} failed`);
cleanupWorkRootIfPortable(SELFTEST_WORKROOT, { failed });
if (failed > 0) process.exitCode = 1;
