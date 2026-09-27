#!/usr/bin/env node
// gha-runtime-guard.selftest.mjs — B3a-CI-1 review round 7（#491 G2：
// selftest override 仍能進入正常 CI 入口）。
//
// reviewer 已證實：本機受控子行程模擬 `GITHUB_ACTIONS=true`／`CI=true`，
// 同時提供 offline 測試替身／假 runner／版本覆寫，跑 run-batch.mjs 的
// smoke 批次，整批仍 rc0（見
// `/Users/eason_tseng/b3a-evidence/2026-09-28-review491/boundary-probes.mjs`
// 與 `boundary-probe-results.json` 的 case
// "GITHUB_ACTIONS-true-accepts-offline-fake-runtime"，唯讀，不在這裡執
// 行）。這裡把修法（gha-runtime-guard.mjs）收成永久 selftest，涵蓋三個正
// 常 GitHub Actions CLI 入口：run-batch.mjs／evaluate-e2e-evidence.mjs／
// generate-ci-envelope.mjs。
//
// 每個負向案例都要證明「測試替身／探測指令沒有被呼叫」（marker 檔不存
// 在），不是只看 rc——decision.md 明文要求。
//
// 執行：node .github/scripts/gha-runtime-guard.selftest.mjs
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { existsSync, mkdtempSync, writeFileSync, rmSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { ensureSelftestWorkRoot, cleanupWorkRootIfPortable } from './__fixtures__/selftestWorkroot.mjs';
import { envelopeRepoRoot, makeBaseEnvelope } from './__fixtures__/envelopeFixture.mjs';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const RUN_BATCH = path.join(__dirname, 'run-batch.mjs');
const EVALUATOR = path.join(__dirname, 'evaluate-e2e-evidence.mjs');
const GENERATE_ENVELOPE = path.join(__dirname, 'generate-ci-envelope.mjs');
const MARKER_TOUCH = path.join(__dirname, '__fixtures__', 'marker-touch.mjs');
const FAKE_RUNNER = path.join(__dirname, '__fixtures__', 'fake-runner.mjs');
const REPO_ROOT = envelopeRepoRoot();

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
function freshDir(label) {
  return mkdtempSync(path.join(SELFTEST_WORKROOT.root, `${label}-`));
}

const GUARD_MESSAGE_MARK = '#491 G2 正常 GHA 入口邊界';

// ---------------------------------------------------------------------------
// run-batch.mjs
// ---------------------------------------------------------------------------

check('run-batch.mjs：GITHUB_ACTIONS=true＋fake runner／offline／deadline覆寫（reviewer 反例原文）→ exit 2，guard 訊息，且 WORK_ROOT／ARTIFACTS_ROOT 從未建立（fake runner 沒被呼叫）', () => {
  const dir = freshDir('rb-gha-fake');
  const envelopePath = path.join(dir, 'envelope.json');
  writeFileSync(envelopePath, JSON.stringify(makeBaseEnvelope({ repoRoot: REPO_ROOT })));
  const workRoot = path.join(dir, 'work');
  const artifactsRoot = path.join(dir, 'artifacts');
  const r = spawnSync('node', [RUN_BATCH, 'smoke'], {
    encoding: 'utf8',
    timeout: 15_000,
    env: {
      ...process.env,
      GITHUB_ACTIONS: 'true',
      CI: 'true',
      CI_E2E_REPO_ROOT: REPO_ROOT,
      CI_E2E_ENVELOPE_PATH: envelopePath,
      CI_E2E_WORK_ROOT: workRoot,
      CI_E2E_ARTIFACTS_ROOT: artifactsRoot,
      CI_E2E_TEST_RUNNER: FAKE_RUNNER,
      CI_E2E_OFFLINE_SELFTEST: '1',
      CI_E2E_ATTEMPT_ID: 'guard-selftest',
      E2E_WRAPPER_DEADLINE_SECONDS_OVERRIDE: '5',
      E2E_WRAPPER_GRACE_SECONDS_OVERRIDE: '1',
    },
  });
  assert.equal(r.status, 2, r.stdout + r.stderr);
  assert.match(r.stderr, new RegExp(GUARD_MESSAGE_MARK));
  assert.ok(r.stderr.includes('CI_E2E_TEST_RUNNER'), r.stderr);
  assert.ok(r.stderr.includes('CI_E2E_OFFLINE_SELFTEST'), r.stderr);
  assert.ok(r.stderr.includes('E2E_WRAPPER_DEADLINE_SECONDS_OVERRIDE'), r.stderr);
  assert.ok(r.stderr.includes('E2E_WRAPPER_GRACE_SECONDS_OVERRIDE'), r.stderr);
  // 證明 fake runner 沒被呼叫：WORK_ROOT／ARTIFACTS_ROOT 連建立都沒有
  // （run-batch.mjs 的 mkdirSync(WORK_ROOT) 在 guard 之後才執行）。
  assert.equal(existsSync(workRoot), false, 'WORK_ROOT 不該被建立');
  assert.equal(existsSync(artifactsRoot), false, 'ARTIFACTS_ROOT 不該被建立（fake runner 從未 spawn）');
});

check('run-batch.mjs：GITHUB_ACTIONS=true 但沒有任何測試替身 env（正例，正常 GHA 設定）→ guard 不阻擋（失敗理由是別的合法原因，不是 G2 guard）', () => {
  const dir = freshDir('rb-gha-clean');
  const r = spawnSync('node', [RUN_BATCH, 'smoke'], {
    encoding: 'utf8',
    timeout: 15_000,
    env: {
      ...process.env,
      GITHUB_ACTIONS: 'true',
      CI: 'true',
      CI_E2E_REPO_ROOT: REPO_ROOT,
      CI_E2E_WORK_ROOT: path.join(dir, 'work'),
      CI_E2E_ARTIFACTS_ROOT: path.join(dir, 'artifacts'),
    },
  });
  assert.doesNotMatch(r.stderr, new RegExp(GUARD_MESSAGE_MARK));
});

check('run-batch.mjs：本機離線 selftest（無 GITHUB_ACTIONS）＋CI_E2E_TEST_RUNNER／OFFLINE_SELFTEST 正常配對 → guard 不阻擋（正常離線路徑不受影響）', () => {
  const dir = freshDir('rb-local-offline');
  const envelopePath = path.join(dir, 'envelope.json');
  writeFileSync(envelopePath, JSON.stringify(makeBaseEnvelope({ repoRoot: REPO_ROOT })));
  const r = spawnSync('node', [RUN_BATCH, 'smoke'], {
    encoding: 'utf8',
    timeout: 15_000,
    env: {
      ...process.env,
      CI_E2E_REPO_ROOT: REPO_ROOT,
      CI_E2E_ENVELOPE_PATH: envelopePath,
      CI_E2E_WORK_ROOT: path.join(dir, 'work'),
      CI_E2E_ARTIFACTS_ROOT: path.join(dir, 'artifacts'),
      CI_E2E_TEST_RUNNER: FAKE_RUNNER,
      CI_E2E_OFFLINE_SELFTEST: '1',
      CI_E2E_ATTEMPT_ID: 'guard-selftest-local',
      // 本機離線測試才會用的 Node 版本模擬 override（跟 GITHUB_ACTIONS
      // 無關，makeBaseEnvelope() 的 nodeVersionActual 固定用核定版本，這裡
      // 讓 envelope 的自我一致性核對能通過，不是這支 selftest 要測的重
      // 點）。
      CI_E2E_SELFTEST_ACTUAL_NODE_VERSION: 'v26.10.0',
    },
  });
  assert.doesNotMatch(r.stderr, new RegExp(GUARD_MESSAGE_MARK), r.stdout + r.stderr);
  assert.equal(r.status, 0, r.stdout + r.stderr);
});

// ---------------------------------------------------------------------------
// evaluate-e2e-evidence.mjs
// ---------------------------------------------------------------------------

check('evaluate-e2e-evidence.mjs：GITHUB_ACTIONS=true＋CI_E2E_SELFTEST_ACTUAL_NODE_VERSION → exit 2，guard 訊息，且連 workdir 都不存在也不會因為別的錯誤先觸發（guard 在最前面）', () => {
  const r = spawnSync('node', [EVALUATOR, '/nonexistent/workdir', '/nonexistent/pkg', 'claude-approval-deny'], {
    encoding: 'utf8',
    timeout: 15_000,
    env: { ...process.env, GITHUB_ACTIONS: 'true', CI_E2E_SELFTEST_ACTUAL_NODE_VERSION: 'v26.10.0' },
  });
  assert.equal(r.status, 2, r.stdout + r.stderr);
  assert.match(r.stderr, new RegExp(GUARD_MESSAGE_MARK));
  assert.ok(r.stderr.includes('CI_E2E_SELFTEST_ACTUAL_NODE_VERSION'), r.stderr);
});

check('evaluate-e2e-evidence.mjs：GITHUB_ACTIONS=true 但沒有 CI_E2E_SELFTEST_ACTUAL_NODE_VERSION（正例）→ guard 不阻擋（失敗理由是別的合法原因）', () => {
  const r = spawnSync('node', [EVALUATOR, '/nonexistent/workdir', '/nonexistent/pkg', 'claude-approval-deny'], {
    encoding: 'utf8',
    timeout: 15_000,
    env: { ...process.env, GITHUB_ACTIONS: 'true' },
  });
  assert.doesNotMatch(r.stderr, new RegExp(GUARD_MESSAGE_MARK));
});

// ---------------------------------------------------------------------------
// generate-ci-envelope.mjs
// ---------------------------------------------------------------------------

check('generate-ci-envelope.mjs：GITHUB_ACTIONS=true＋CI_ENVELOPE_*_CMD_OVERRIDE 指向 marker-touch → exit 2，guard 訊息，且 marker 檔從未被建立（版本探測指令沒被 spawn）', () => {
  const dir = freshDir('gce-gha-fake');
  const markerPath = path.join(dir, 'marker.txt');
  const outputPath = path.join(dir, 'envelope.json');
  const r = spawnSync('node', [GENERATE_ENVELOPE, outputPath], {
    encoding: 'utf8',
    timeout: 15_000,
    env: {
      ...process.env,
      GITHUB_ACTIONS: 'true',
      CI: 'true',
      MARKER_TOUCH_PATH: markerPath,
      CI_ENVELOPE_ALLOW_MISSING_IMAGE_INFO: '1',
      CI_ENVELOPE_GO_CMD_OVERRIDE: JSON.stringify(['node', MARKER_TOUCH]),
      CI_ENVELOPE_WAILS_CMD_OVERRIDE: JSON.stringify(['node', MARKER_TOUCH]),
      CI_ENVELOPE_PLAYWRIGHT_CMD_OVERRIDE: JSON.stringify(['node', MARKER_TOUCH]),
      CI_ENVELOPE_CHROME_CMD_OVERRIDE: JSON.stringify(['node', MARKER_TOUCH]),
    },
  });
  assert.equal(r.status, 2, r.stdout + r.stderr);
  assert.match(r.stderr, new RegExp(GUARD_MESSAGE_MARK));
  assert.equal(existsSync(markerPath), false, 'marker-touch.mjs 不該被 spawn（版本探測指令從未執行）');
  assert.equal(existsSync(outputPath), false, 'envelope.json 不該被寫出');
});

check('generate-ci-envelope.mjs：GITHUB_ACTIONS=true 但沒有任何 CI_ENVELOPE_*_OVERRIDE／ALLOW_MISSING（正例，正常 GHA 設定）→ guard 不阻擋（失敗理由是別的合法原因）', () => {
  const dir = freshDir('gce-gha-clean');
  const outputPath = path.join(dir, 'envelope.json');
  const r = spawnSync('node', [GENERATE_ENVELOPE, outputPath], {
    encoding: 'utf8',
    timeout: 15_000,
    env: { ...process.env, GITHUB_ACTIONS: 'true', CI: 'true' },
  });
  assert.doesNotMatch(r.stderr, new RegExp(GUARD_MESSAGE_MARK));
});

check('generate-ci-envelope.mjs：本機離線 selftest（無 GITHUB_ACTIONS）＋CI_ENVELOPE_GO_CMD_OVERRIDE 指向 marker-touch → guard 不阻擋，marker 檔確實被建立（正常離線測試入口仍可用）', () => {
  const dir = freshDir('gce-local-offline');
  const markerPath = path.join(dir, 'marker.txt');
  const outputPath = path.join(dir, 'envelope.json');
  const r = spawnSync('node', [GENERATE_ENVELOPE, outputPath], {
    encoding: 'utf8',
    timeout: 15_000,
    env: {
      ...process.env,
      GITHUB_SHA: '3333333333333333333333333333333333333333',
      CI_ENVELOPE_PR_HEAD_SHA: '4444444444444444444444444444444444444444',
      CI_ENVELOPE_PR_BASE_SHA: '5555555555555555555555555555555555555555',
      GITHUB_REPOSITORY: 'offline-selftest/repo',
      GITHUB_RUN_ID: '1',
      GITHUB_RUN_ATTEMPT: '1',
      RUNNER_OS: 'macOS',
      CI_ENVELOPE_ALLOW_MISSING_IMAGE_INFO: '1',
      CI_ENVELOPE_REPO_ROOT: REPO_ROOT,
      MARKER_TOUCH_PATH: markerPath,
      CI_ENVELOPE_GO_CMD_OVERRIDE: JSON.stringify(['node', MARKER_TOUCH]),
      CI_ENVELOPE_WAILS_CMD_OVERRIDE: JSON.stringify(['node', MARKER_TOUCH]),
      CI_ENVELOPE_PLAYWRIGHT_CMD_OVERRIDE: JSON.stringify(['node', MARKER_TOUCH]),
      CI_ENVELOPE_CHROME_CMD_OVERRIDE: JSON.stringify(['node', MARKER_TOUCH]),
    },
  });
  assert.doesNotMatch(r.stderr, new RegExp(GUARD_MESSAGE_MARK), r.stdout + r.stderr);
  assert.equal(r.status, 0, r.stdout + r.stderr);
  assert.equal(existsSync(markerPath), true, 'marker-touch.mjs 應該真的被 spawn（本機離線測試入口仍要能用）');
});

console.log(`\n${passed} passed, ${failed} failed`);
cleanupWorkRootIfPortable(SELFTEST_WORKROOT, { failed });
if (failed > 0) process.exitCode = 1;
