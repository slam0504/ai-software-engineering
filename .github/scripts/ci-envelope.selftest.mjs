#!/usr/bin/env node
// ci-envelope.selftest.mjs — B3a-CI-1 review round 4（#480 CHANGES_REQUIRED
// R2 剩餘缺口）。離線驗證 generate-ci-envelope.mjs 本身的行為：必要欄位缺
// 漏、探測指令失敗、以及正常路徑下產出的 JSON 內容正確。不呼叫真實
// run-e2e／globalSetup／App／browser；Go／Chrome 探測用假指令覆寫
// （CI_ENVELOPE_GO_CMD_OVERRIDE／CI_ENVELOPE_CHROME_CMD_OVERRIDE），
// git rev-parse HEAD 用這個 worktree 自己的真實 repo（不用假的），checkout
// HEAD 交叉核對才有意義。
//
// 執行：node .github/scripts/ci-envelope.selftest.mjs
import assert from 'node:assert/strict';
import { spawnSync, execFileSync } from 'node:child_process';
import { existsSync, mkdtempSync, readFileSync, rmSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { ensureSelftestWorkRoot, cleanupWorkRootIfPortable } from './__fixtures__/selftestWorkroot.mjs';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const SCRIPT = path.join(__dirname, 'generate-ci-envelope.mjs');
const REPO_ROOT = path.resolve(__dirname, '..', '..');
const FAKE_VERSION_TOOL = path.join(__dirname, '__fixtures__', 'fake-version-tool.mjs');

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

const REAL_HEAD_SHA = execFileSync('git', ['-C', REPO_ROOT, 'rev-parse', 'HEAD'], { encoding: 'utf8' }).trim();

function baseEnv(overrides = {}) {
  return {
    GITHUB_SHA: '3333333333333333333333333333333333333333',
    CI_ENVELOPE_PR_HEAD_SHA: '4444444444444444444444444444444444444444',
    CI_ENVELOPE_PR_BASE_SHA: '5555555555555555555555555555555555555555',
    GITHUB_REPOSITORY: 'offline-selftest/repo',
    GITHUB_RUN_ID: '123456789',
    GITHUB_RUN_ATTEMPT: '1',
    RUNNER_OS: 'macOS',
    CI_ENVELOPE_ALLOW_MISSING_IMAGE_INFO: '1',
    CI_ENVELOPE_REPO_ROOT: REPO_ROOT,
    CI_ENVELOPE_GO_CMD_OVERRIDE: JSON.stringify(['node', FAKE_VERSION_TOOL]),
    CI_ENVELOPE_CHROME_CMD_OVERRIDE: JSON.stringify(['node', FAKE_VERSION_TOOL]),
    // review round 6（#489 item 0）：reviewer 首跑 161/2 failed——這兩個正例
    // 先前沒有覆寫 Wails／Playwright 探測指令，落到 generate-ci-envelope.mjs
    // 的預設值（真的 `wails version`／`npx playwright --version`），reviewer
    // 環境 PATH 沒有 wails，`spawnSync wails ENOENT`。selftest 對外部版本探
    // 測一律要完全隔離，不依賴本機是否裝了這些工具（見
    // envelope-environment-diagnosis.log）。
    CI_ENVELOPE_WAILS_CMD_OVERRIDE: JSON.stringify(['node', FAKE_VERSION_TOOL]),
    CI_ENVELOPE_PLAYWRIGHT_CMD_OVERRIDE: JSON.stringify(['node', FAKE_VERSION_TOOL]),
    FAKE_VERSION_TOOL_OUTPUT: 'go version go1.26.5 darwin/amd64\n',
    ...overrides,
  };
}

function run(outputPath, envOverrides = {}) {
  const result = spawnSync('node', [SCRIPT, outputPath], {
    encoding: 'utf8',
    timeout: 15_000,
    env: { ...process.env, ...baseEnv(envOverrides) },
  });
  return { rc: result.status, stdout: result.stdout, stderr: result.stderr };
}

// --- 缺 output-path 參數 ---
check('缺 output-path 參數 → exit 2', () => {
  const result = spawnSync('node', [SCRIPT], { encoding: 'utf8', timeout: 15_000 });
  assert.equal(result.status, 2);
  assert.match(result.stderr, /usage:/);
});

// --- 正常路徑：所有必要事實齊全 → exit 0，寫出正確內容 ---
check('全部必要事實齊全 → exit 0，envelope.json 內容正確（checkoutHeadSha=實際 HEAD，nodeVersionActual=實際 process.version）', () => {
  const dir = freshDir('envgood');
  const outputPath = path.join(dir, 'envelope.json');
  const { rc, stdout } = run(outputPath);
  assert.equal(rc, 0, stdout);
  assert.ok(existsSync(outputPath));
  const envelope = JSON.parse(readFileSync(outputPath, 'utf8'));
  assert.equal(envelope.testMergeSha, '3333333333333333333333333333333333333333');
  assert.equal(envelope.prHeadSha, '4444444444444444444444444444444444444444');
  assert.equal(envelope.prBaseSha, '5555555555555555555555555555555555555555');
  assert.equal(envelope.repository, 'offline-selftest/repo');
  assert.equal(envelope.workflowRunId, '123456789');
  assert.equal(envelope.workflowRunAttempt, '1');
  assert.equal(envelope.runnerOs, 'macOS');
  assert.equal(envelope.imageOs, '(offline-selftest-unavailable)');
  assert.equal(envelope.imageVersion, '(offline-selftest-unavailable)');
  assert.equal(envelope.checkoutHeadSha, REAL_HEAD_SHA, '應該是這個 worktree 實際的 git rev-parse HEAD，不是憑空捏造的值');
  assert.equal(envelope.nodeVersionActual, process.version, '應該是這支腳本自己執行期的 process.version');
  assert.match(envelope.goVersionActual, /go1\.26\.5/);
  assert.match(envelope.chromeVersionActual, /go1\.26\.5/); // 假指令 CHROME 和 GO 用同一份假輸出，只驗證有正確帶入
  assert.match(envelope.wailsVersionActual, /go1\.26\.5/); // 同上，四個假指令共用同一份假輸出
  assert.match(envelope.playwrightVersionActual, /go1\.26\.5/);
  assert.ok(typeof envelope.generatedAtIso === 'string' && envelope.generatedAtIso.length > 0);
  rmSync(dir, { recursive: true, force: true });
});

// --- review round 6（#489 item 0）：Wails／Playwright 版本探測失敗
// （reviewer 首跑 161/2 failed 的根因——四個工具探測共用同一把
// FAKE_VERSION_TOOL_RC，這裡確認 Wails／Playwright 也在失敗路徑內，且失敗
// 時不寫出殘缺 envelope、也不會意外落回真實 wails/playwright） ---
check('四個工具版本探測指令都失敗（假指令回傳非零）→ exit 1，Wails／Playwright 錯誤訊息都在', () => {
  const dir = freshDir('envallfail');
  const outputPath = path.join(dir, 'envelope.json');
  const { rc, stderr } = run(outputPath, { FAKE_VERSION_TOOL_RC: '1' });
  assert.notEqual(rc, 0);
  assert.match(stderr, /探測 Go 版本失敗/);
  assert.match(stderr, /探測 Chrome 版本失敗/);
  assert.match(stderr, /探測 Wails 版本失敗/);
  assert.match(stderr, /探測 Playwright 版本失敗/);
  assert.ok(!existsSync(outputPath));
  rmSync(dir, { recursive: true, force: true });
});

// --- CI_ENVELOPE_WAILS_CMD_OVERRIDE／CI_ENVELOPE_PLAYWRIGHT_CMD_OVERRIDE 格式不合法 ---
check('CI_ENVELOPE_WAILS_CMD_OVERRIDE 不是合法 JSON 字串陣列 → exit 1', () => {
  const dir = freshDir('envwailsbadoverride');
  const outputPath = path.join(dir, 'envelope.json');
  const { rc, stderr } = run(outputPath, { CI_ENVELOPE_WAILS_CMD_OVERRIDE: 'not json' });
  assert.notEqual(rc, 0);
  assert.match(stderr, /CI_ENVELOPE_WAILS_CMD_OVERRIDE 設定了但不是合法的 JSON 字串陣列/);
  assert.ok(!existsSync(outputPath));
  rmSync(dir, { recursive: true, force: true });
});
check('CI_ENVELOPE_PLAYWRIGHT_CMD_OVERRIDE 不是合法 JSON 字串陣列 → exit 1', () => {
  const dir = freshDir('envpwbadoverride');
  const outputPath = path.join(dir, 'envelope.json');
  const { rc, stderr } = run(outputPath, { CI_ENVELOPE_PLAYWRIGHT_CMD_OVERRIDE: 'not json' });
  assert.notEqual(rc, 0);
  assert.match(stderr, /CI_ENVELOPE_PLAYWRIGHT_CMD_OVERRIDE 設定了但不是合法的 JSON 字串陣列/);
  assert.ok(!existsSync(outputPath));
  rmSync(dir, { recursive: true, force: true });
});

// --- 缺必要 GitHub context 欄位 ---
check('GITHUB_SHA 未設定 → exit 1，不寫出殘缺 envelope', () => {
  const dir = freshDir('envmissingsha');
  const outputPath = path.join(dir, 'envelope.json');
  const result = spawnSync('node', [SCRIPT, outputPath], {
    encoding: 'utf8',
    timeout: 15_000,
    env: { ...process.env, ...baseEnv({ GITHUB_SHA: '' }) },
  });
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /GITHUB_SHA 未設定或為空/);
  assert.ok(!existsSync(outputPath), '探測失敗時不應該寫出殘缺的 envelope 檔案');
  rmSync(dir, { recursive: true, force: true });
});

// --- ImageOS/ImageVersion 缺漏且未放寬 → exit 1 ---
check('ImageOS／ImageVersion 缺漏且未設 ALLOW_MISSING → exit 1', () => {
  const dir = freshDir('envmissingimage');
  const outputPath = path.join(dir, 'envelope.json');
  const result = spawnSync('node', [SCRIPT, outputPath], {
    encoding: 'utf8',
    timeout: 15_000,
    env: { ...process.env, ...baseEnv({ CI_ENVELOPE_ALLOW_MISSING_IMAGE_INFO: '' }) },
  });
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /ImageOS 未設定或為空/);
  assert.match(result.stderr, /ImageVersion 未設定或為空/);
  assert.ok(!existsSync(outputPath));
  rmSync(dir, { recursive: true, force: true });
});

// --- git rev-parse HEAD 失敗（repo-root 不是 git repo） ---
check('CI_ENVELOPE_REPO_ROOT 指向非 git 目錄 → git rev-parse HEAD 失敗，exit 1', () => {
  const dir = freshDir('envnotgit');
  const notGitDir = freshDir('envnotgit-target');
  const outputPath = path.join(dir, 'envelope.json');
  const { rc, stderr } = run(outputPath, { CI_ENVELOPE_REPO_ROOT: notGitDir });
  assert.notEqual(rc, 0);
  assert.match(stderr, /git rev-parse HEAD 失敗/);
  assert.ok(!existsSync(outputPath));
  rmSync(dir, { recursive: true, force: true });
  rmSync(notGitDir, { recursive: true, force: true });
});

// --- Go 版本探測失敗 ---
check('Go 版本探測指令失敗（假指令回傳非零）→ exit 1', () => {
  const dir = freshDir('envgofail');
  const outputPath = path.join(dir, 'envelope.json');
  const { rc, stderr } = run(outputPath, { FAKE_VERSION_TOOL_RC: '1' });
  assert.notEqual(rc, 0);
  assert.match(stderr, /探測 Go 版本失敗/);
  assert.match(stderr, /探測 Chrome 版本失敗/); // 同一份假指令也失敗，兩者都該報
  assert.ok(!existsSync(outputPath));
  rmSync(dir, { recursive: true, force: true });
});

// --- Go 覆寫指令本身格式不合法（不是 JSON 陣列） ---
check('CI_ENVELOPE_GO_CMD_OVERRIDE 不是合法 JSON 字串陣列 → exit 1', () => {
  const dir = freshDir('envgobadoverride');
  const outputPath = path.join(dir, 'envelope.json');
  const { rc, stderr } = run(outputPath, { CI_ENVELOPE_GO_CMD_OVERRIDE: 'not json' });
  assert.notEqual(rc, 0);
  assert.match(stderr, /不是合法的 JSON 字串陣列/);
  assert.ok(!existsSync(outputPath));
  rmSync(dir, { recursive: true, force: true });
});

// --- 不存在的輸出目錄要能自動建立 ---
check('output-path 的父目錄尚不存在 → 自動 mkdir -p 後仍能寫出', () => {
  const dir = freshDir('envmkdir');
  const outputPath = path.join(dir, 'nested', 'deeper', 'envelope.json');
  const { rc } = run(outputPath);
  assert.equal(rc, 0);
  assert.ok(existsSync(outputPath));
  rmSync(dir, { recursive: true, force: true });
});

console.log(`\n${passed} passed, ${failed} failed`);
cleanupWorkRootIfPortable(SELFTEST_WORKROOT, { failed });
if (failed > 0) process.exitCode = 1;
