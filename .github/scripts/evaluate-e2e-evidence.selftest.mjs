#!/usr/bin/env node
// evaluate-e2e-evidence.selftest.mjs — 離線驗證 evaluate-e2e-evidence.mjs 的
// 判定邏輯。全部用合成 fixture（見 __fixtures__/evidence-builder.mjs）手動
// 組 run-state.json／harness.log／playwright-results.json，不呼叫真實
// run-e2e／globalSetup／App／browser。逐一呼叫實際的
// evaluate-e2e-evidence.mjs（子行程，非 import 內部函式），確保測到的是這
// 支腳本真正的 CLI 行為。
//
// 執行：node .github/scripts/evaluate-e2e-evidence.selftest.mjs
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { makeGoodRunDir, suiteWithOneTest, controlsSuites } from './__fixtures__/evidence-builder.mjs';
import { entryById } from './ci-e2e-entries.mjs';
import { ensureSelftestWorkRoot, cleanupWorkRootIfPortable } from './__fixtures__/selftestWorkroot.mjs';
import { envelopeRepoRoot, makeStampedEnvelope } from './__fixtures__/envelopeFixture.mjs';

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

// review round 3（#480 R6）：可攜預設輸出——見
// __fixtures__/selftestWorkroot.mjs 的邏輯說明。
const SELFTEST_WORKROOT = ensureSelftestWorkRoot();
const EVIDENCE_ROOT = SELFTEST_WORKROOT.root;
function freshDir(label) {
  return mkdtempSync(path.join(EVIDENCE_ROOT, `${label}-`));
}

function writeWrapperStatus(caseDir, {
  status = 'completed',
  wrapperRc = 0,
  rcFileValue = wrapperRc,
  skipRcFile = false,
  childRc = 0,
  childConfirmedGone = 'true',
  producerErrors = [],
} = {}) {
  writeFileSync(
    path.join(caseDir, 'e2e-wrapper-status.json'),
    JSON.stringify({
      status, wrapperRc, childRc, childConfirmedGone, producerErrors,
      startIso: '2026-09-28T00:00:00.000Z', endIso: '2026-09-28T00:00:01.000Z',
    }, null, 2),
  );
  if (!skipRcFile) writeFileSync(path.join(caseDir, 'e2e.rc'), `${rcFileValue}\n`);
}

function writeNewRunDirs(caseDir, dirs) {
  writeFileSync(path.join(caseDir, 'new-run-dirs.json'), JSON.stringify(dirs, null, 2));
}

// 模擬 package-e2e-evidence.sh 在呼叫 evaluate-e2e-evidence.mjs 之前已經做
// 完的複製動作（wrapper 輸出＋唯一 run 目錄攤平進 artifacts/）——production
// 流程裡 run-batch.mjs 一律先呼叫 package script 再呼叫 evaluator，這裡的
// selftest 只測 evaluator 自己的判定邏輯，所以手動重現同一份複製前提，不
// 依賴 bash／package script 本身（那支腳本另有自己的 selftest）。
// review round 4（#480 R2 剩餘缺口）：evaluateClaimedSuccessRun() 現在也會
// 驗證 workdir/envelope.json（見 evaluate-e2e-evidence.mjs
// validateEnvelope()）。`envelope: 'auto'`（預設）在有 runDirToCopy（也就是
// 會走進 claimed-success 路徑）時自動寫一份合法、跟 entryId／runDir 對得上
// 的 envelope，讓既有測試不需要逐一修改就能繼續代表「證據齊全且正確」；
// `envelope: null` 明確不寫（測「缺檔」反例）；`envelope: {...}` 直接用呼
// 叫端提供的內容覆寫（測 schema／identity 反例）。
function runEvaluator(caseDir, entryId, { runDirToCopy = null, envelope = 'auto' } = {}) {
  if (envelope === 'auto') {
    if (runDirToCopy) {
      const entry = entryById(entryId);
      const stamped = makeStampedEnvelope(entry, path.basename(runDirToCopy), {}, { repoRoot: ENVELOPE_REPO_ROOT });
      writeFileSync(path.join(caseDir, 'envelope.json'), JSON.stringify(stamped, null, 2));
    }
  } else if (envelope !== null) {
    writeFileSync(path.join(caseDir, 'envelope.json'), JSON.stringify(envelope, null, 2));
  }

  const packageDir = path.join(caseDir, 'package');
  mkdirSync(packageDir, { recursive: true });
  for (const f of ['e2e-wrapper-status.json', 'e2e.rc', 'new-run-dirs.json', 'envelope.json']) {
    const src = path.join(caseDir, f);
    if (existsSync(src)) cpSync(src, path.join(packageDir, f));
  }
  if (runDirToCopy && existsSync(runDirToCopy)) {
    const artifactsDst = path.join(packageDir, 'artifacts');
    mkdirSync(artifactsDst, { recursive: true });
    cpSync(runDirToCopy, artifactsDst, { recursive: true });
  }
  const result = spawnSync('node', [EVALUATOR, caseDir, packageDir, entryId], {
    encoding: 'utf8',
    timeout: 15_000,
    env: {
      ...process.env,
      CI_E2E_REPO_ROOT: ENVELOPE_REPO_ROOT,
      // review round 6（#489 F3）：envelope fixture 的 nodeVersionActual 現
      // 在固定用 REQUIRED_TOOL_VERSIONS.node（正常 CI 核定版本），本機
      // Node 版本可能不同，這裡明確隔離地告訴 checkEnvelopeNodeVersion 用
      // 哪個值當自我一致性比對基準——正式 workflow 絕不設定這個變數。
      CI_E2E_SELFTEST_ACTUAL_NODE_VERSION: 'v26.10.0',
    },
  });
  return { rc: result.status, stdout: result.stdout, stderr: result.stderr, packageDir };
}

// --- 缺 status 檔：交給呼叫端既有檔案存在性檢查，evaluator 自己 exit 0 ---
check('e2e-wrapper-status.json 不存在 → evaluator exit 0（不重複判斷）', () => {
  const caseDir = freshDir('nostatus');
  const { rc } = runEvaluator(caseDir, 'default');
  assert.equal(rc, 0);
  rmSync(caseDir, { recursive: true, force: true });
});

// --- status JSON 本身壞掉 ---
check('e2e-wrapper-status.json 是無法解析的內容 → exit 1，不套用 NO-RUN 例外', () => {
  const caseDir = freshDir('badjson');
  writeFileSync(path.join(caseDir, 'e2e-wrapper-status.json'), 'not json');
  writeFileSync(path.join(caseDir, 'e2e.rc'), '0\n');
  const { rc, stdout, packageDir } = runEvaluator(caseDir, 'default');
  assert.equal(rc, 1);
  assert.ok(!existsSync(path.join(packageDir, 'NO-RUN.txt')), '不可信的狀態不得套用 NO-RUN 例外');
  assert.match(stdout, /無法解析為合法 JSON/);
  rmSync(caseDir, { recursive: true, force: true });
});

// --- wrapperRc 與 e2e.rc 不一致 ---
check('wrapperRc 與 e2e.rc 內容不一致 → exit 1（視為不可信）', () => {
  const caseDir = freshDir('rcmismatch');
  writeWrapperStatus(caseDir, { status: 'completed', wrapperRc: 0, rcFileValue: 7 });
  const { rc, stdout } = runEvaluator(caseDir, 'default');
  assert.equal(rc, 1);
  assert.match(stdout, /不一致，視為不可信/);
  rmSync(caseDir, { recursive: true, force: true });
});

// --- 啟動前就失敗（非 completed），且 0 個新 run 目錄：NO-RUN（decision470
//     語意修正）。「宣稱 completed/rc=0 但 0 個新 run 目錄」是另一種矛盾
//     （wrapper 明確宣稱成功卻無任何佐證），evaluator 把它歸類成「缺漏」而
//     非 NO-RUN——NO-RUN 專指 wrapper 自己也承認沒有正常完成（timeout／
//     interrupted／producer-error／watchdog／輸出目標被拒絕）且找不到任何
//     harness 產出的情境，見下方另一個 test。 ---
check('宣稱 completed/rc=0 但 0 個新 run 目錄 → 缺漏（不是 NO-RUN；wrapper 明確宣稱成功卻無佐證，矛盾程度更高）', () => {
  const caseDir = freshDir('claimedsuccessnodir');
  writeWrapperStatus(caseDir, { status: 'completed', wrapperRc: 0 });
  writeNewRunDirs(caseDir, []);
  const { rc, stdout, packageDir } = runEvaluator(caseDir, 'default');
  assert.equal(rc, 1);
  assert.ok(!existsSync(path.join(packageDir, 'NO-RUN.txt')), 'claimedSuccess 分支不套用 NO-RUN 例外');
  assert.match(stdout, /沒有偵測到任何新 run 目錄/);
  rmSync(caseDir, { recursive: true, force: true });
});

// --- 早期失敗（rejected-unsupported-output-target）＋ 0 個新 run 目錄：真正的 NO-RUN ---
check('wrapper 自己回報啟動前就被拒絕（未 spawn child）且 0 個新 run 目錄 → NO-RUN.txt 措辭是「未取得 run 證據」不是「確認未啟動」，且 evaluator 仍判定失敗', () => {
  const caseDir = freshDir('norun');
  writeWrapperStatus(caseDir, { status: 'rejected-unsupported-output-target', wrapperRc: 2 });
  writeNewRunDirs(caseDir, []);
  const { rc, packageDir } = runEvaluator(caseDir, 'default');
  assert.equal(rc, 1, 'NO-RUN 本身仍是失敗（不是可略過不計）');
  const noRunPath = path.join(packageDir, 'NO-RUN.txt');
  assert.ok(existsSync(noRunPath), 'NO-RUN.txt 應該被寫出');
  const text = readFileSync(noRunPath, 'utf8');
  assert.match(text, /未取得 run 證據，無法判定是否曾啟動/);
  assert.ok(
    !text.includes('判定為啟動前就失敗／未開始'),
    'decision470 明確禁止 PR#12 原文「判定為啟動前就失敗／未開始」這種過度宣稱（等於「確認未啟動」）的舊措辭',
  );
  // 注意：文字裡允許出現「（不是「確認未啟動」）」這種明確否定／澄清的寫法
  // （見 evaluate-e2e-evidence.mjs 的 NO-RUN 文案），不是禁止出現這個字串
  // 本身——禁止的是把它當「已確認」的結論來寫。
  rmSync(caseDir, { recursive: true, force: true });
});

// --- 宣稱成功但有 2 個新 run 目錄：identity 異常 ---
check('宣稱 completed/rc=0 但 2 個新 run 目錄 → identity 異常，exit 1', () => {
  const caseDir = freshDir('dupdirs');
  writeWrapperStatus(caseDir, { status: 'completed', wrapperRc: 0 });
  writeNewRunDirs(caseDir, ['/nonexistent/run-a', '/nonexistent/run-b']);
  const { rc, stdout, packageDir } = runEvaluator(caseDir, 'default');
  assert.equal(rc, 1);
  assert.match(stdout, /identity 異常/);
  assert.ok(!existsSync(path.join(packageDir, 'NO-RUN.txt')), '有候選目錄時不該落入 NO-RUN 分支');
  rmSync(caseDir, { recursive: true, force: true });
});

// --- 非 completed 卻 rc=0（自相矛盾） ---
check('status≠completed 但 wrapperRc=0（自相矛盾）→ exit 1，不套用 NO-RUN', () => {
  const caseDir = freshDir('contradiction');
  writeWrapperStatus(caseDir, { status: 'timeout', wrapperRc: 0 });
  writeNewRunDirs(caseDir, []);
  const { rc, stdout, packageDir } = runEvaluator(caseDir, 'default');
  assert.equal(rc, 1);
  assert.match(stdout, /自相矛盾/);
  assert.ok(!existsSync(path.join(packageDir, 'NO-RUN.txt')));
  rmSync(caseDir, { recursive: true, force: true });
});

// --- 全綠：default entry 完整證據 ---
check('default entry：完整正確證據 → exit 0', () => {
  const caseDir = freshDir('defaultgreen');
  const artifactsRoot = path.join(caseDir, 'artifacts-root');
  const runDir = makeGoodRunDir(artifactsRoot, 'run-001', {
    entry: entryById('default'),
    specs: [suiteWithOneTest('glossary.spec.ts', 'glossary', 'renders glossary')],
  });
  writeWrapperStatus(caseDir, { status: 'completed', wrapperRc: 0 });
  writeNewRunDirs(caseDir, [runDir]);
  const { rc, stdout } = runEvaluator(caseDir, 'default', { runDirToCopy: runDir });
  assert.equal(rc, 0, `預期 exit 0，實際 stdout:\n${stdout}`);
  rmSync(caseDir, { recursive: true, force: true });
});

// --- default entry：spec 檔身份錯誤（identity 異常：多了一支不該有的 spec） ---
check('default entry：出現非預期的額外 spec 檔 → identity 異常，exit 1', () => {
  const caseDir = freshDir('defaultextra');
  const artifactsRoot = path.join(caseDir, 'artifacts-root');
  const runDir = makeGoodRunDir(artifactsRoot, 'run-002', {
    entry: entryById('default'),
    specs: [
      suiteWithOneTest('glossary.spec.ts', 'glossary', 'renders glossary'),
      suiteWithOneTest('unexpected.spec.ts', 'unexpected', 'unexpected extra spec'),
    ],
  });
  writeWrapperStatus(caseDir, { status: 'completed', wrapperRc: 0 });
  writeNewRunDirs(caseDir, [runDir]);
  const { rc, stdout } = runEvaluator(caseDir, 'default', { runDirToCopy: runDir });
  assert.equal(rc, 1);
  assert.match(stdout, /出現非預期的 spec 檔/);
  rmSync(caseDir, { recursive: true, force: true });
});

// --- default entry：spec 檔缺席 ---
check('default entry：預期的 spec 檔缺席 → identity 異常，exit 1', () => {
  const caseDir = freshDir('defaultmissing');
  const artifactsRoot = path.join(caseDir, 'artifacts-root');
  const runDir = makeGoodRunDir(artifactsRoot, 'run-003', {
    entry: entryById('default'), specs: [] });
  writeWrapperStatus(caseDir, { status: 'completed', wrapperRc: 0 });
  writeNewRunDirs(caseDir, [runDir]);
  const { rc, stdout } = runEvaluator(caseDir, 'default', { runDirToCopy: runDir });
  assert.equal(rc, 1);
  assert.match(stdout, /預期 spec 檔缺席/);
  rmSync(caseDir, { recursive: true, force: true });
});

// --- default entry：test 意外失敗（unexpected） ---
check('default entry：test.status=unexpected → exit 1', () => {
  const caseDir = freshDir('defaultunexpected');
  const artifactsRoot = path.join(caseDir, 'artifacts-root');
  const suite = suiteWithOneTest('glossary.spec.ts', 'glossary', 'renders glossary');
  suite.specs[0].tests[0].status = 'unexpected';
  suite.specs[0].tests[0].results[0].status = 'failed';
  const runDir = makeGoodRunDir(artifactsRoot, 'run-004', {
    entry: entryById('default'), specs: [suite] });
  writeWrapperStatus(caseDir, { status: 'completed', wrapperRc: 0 });
  writeNewRunDirs(caseDir, [runDir]);
  const { rc, stdout } = runEvaluator(caseDir, 'default', { runDirToCopy: runDir });
  assert.equal(rc, 1);
  assert.match(stdout, /status=unexpected/);
  rmSync(caseDir, { recursive: true, force: true });
});

// --- default entry：retry>0（flaky） ---
check('default entry：曾 retry 才過（flaky）→ exit 1，不允許', () => {
  const caseDir = freshDir('defaultflaky');
  const artifactsRoot = path.join(caseDir, 'artifacts-root');
  const suite = suiteWithOneTest('glossary.spec.ts', 'glossary', 'renders glossary');
  suite.specs[0].tests[0].status = 'flaky';
  const runDir = makeGoodRunDir(artifactsRoot, 'run-005', {
    entry: entryById('default'), specs: [suite] });
  writeWrapperStatus(caseDir, { status: 'completed', wrapperRc: 0 });
  writeNewRunDirs(caseDir, [runDir]);
  const { rc, stdout } = runEvaluator(caseDir, 'default', { runDirToCopy: runDir });
  assert.equal(rc, 1);
  assert.match(stdout, /flaky/);
  rmSync(caseDir, { recursive: true, force: true });
});

// --- controls：全綠 ---
check('controls entry：control A 依契約失敗＋錯誤訊息相符、B 通過、reverse skipped → exit 0', () => {
  const caseDir = freshDir('controlsgreen');
  const artifactsRoot = path.join(caseDir, 'artifacts-root');
  const runDir = makeGoodRunDir(artifactsRoot, 'run-006', {
    entry: entryById('controls'),
    rootDir: '/synthetic/frontend/e2e/controls', specs: controlsSuites() });
  writeWrapperStatus(caseDir, { status: 'completed', wrapperRc: 0 });
  writeNewRunDirs(caseDir, [runDir]);
  const { rc, stdout } = runEvaluator(caseDir, 'controls', { runDirToCopy: runDir });
  assert.equal(rc, 0, `預期 exit 0，實際 stdout:\n${stdout}`);
  rmSync(caseDir, { recursive: true, force: true });
});

// --- controls：control A 意外通過 ---
check('controls entry：control A 意外 pass（oracle 拒絕：expected failure 沒發生）→ exit 1', () => {
  const caseDir = freshDir('controlsAunexpected');
  const artifactsRoot = path.join(caseDir, 'artifacts-root');
  const runDir = makeGoodRunDir(artifactsRoot, 'run-007', {
    entry: entryById('controls'),
    rootDir: '/synthetic/frontend/e2e/controls',
    specs: controlsSuites({ controlAStatus: 'unexpected', controlAResultStatus: 'passed', controlAErrorMessage: null }),
  });
  writeWrapperStatus(caseDir, { status: 'completed', wrapperRc: 0 });
  writeNewRunDirs(caseDir, [runDir]);
  const { rc, stdout } = runEvaluator(caseDir, 'controls', { runDirToCopy: runDir });
  assert.equal(rc, 1);
  assert.match(stdout, /control A 意外 pass/);
  rmSync(caseDir, { recursive: true, force: true });
});

// --- controls：control A 失敗但錯誤原因不符（不是目標存檔斷言，例如 locator timeout） ---
check('controls entry：control A 確實失敗但錯誤訊息不是目標存檔斷言 → oracle 拒絕（不能任何錯誤都接受）', () => {
  const caseDir = freshDir('controlsAwrongerror');
  const artifactsRoot = path.join(caseDir, 'artifacts-root');
  const runDir = makeGoodRunDir(artifactsRoot, 'run-008', {
    entry: entryById('controls'),
    rootDir: '/synthetic/frontend/e2e/controls',
    specs: controlsSuites({ controlAErrorMessage: 'locator.click: Timeout 15000ms exceeded waiting for [data-test="save"]' }),
  });
  writeWrapperStatus(caseDir, { status: 'completed', wrapperRc: 0 });
  writeNewRunDirs(caseDir, [runDir]);
  const { rc, stdout } = runEvaluator(caseDir, 'controls', { runDirToCopy: runDir });
  assert.equal(rc, 1);
  assert.match(stdout, /正規化核對後不是恰好一筆目標斷言錯誤/);
  rmSync(caseDir, { recursive: true, force: true });
});

// --- controls：reverse-check 非預期執行 ---
check('controls entry：reverse-check 非預期執行（未 skip）→ oracle 拒絕', () => {
  const caseDir = freshDir('controlsReverseRan');
  const artifactsRoot = path.join(caseDir, 'artifacts-root');
  const runDir = makeGoodRunDir(artifactsRoot, 'run-009', {
    entry: entryById('controls'),
    rootDir: '/synthetic/frontend/e2e/controls',
    specs: controlsSuites({ reverseCheckSkipped: false }),
  });
  writeWrapperStatus(caseDir, { status: 'completed', wrapperRc: 0 });
  writeNewRunDirs(caseDir, [runDir]);
  const { rc, stdout } = runEvaluator(caseDir, 'controls', { runDirToCopy: runDir });
  assert.equal(rc, 1);
  assert.match(stdout, /reverse-check\.spec\.ts 狀態不一致或非預期執行/);
  rmSync(caseDir, { recursive: true, force: true });
});

// --- controls：control B 意外失敗 ---
check('controls entry：control B 應該 pass 卻 fail → exit 1', () => {
  const caseDir = freshDir('controlsBfail');
  const artifactsRoot = path.join(caseDir, 'artifacts-root');
  const runDir = makeGoodRunDir(artifactsRoot, 'run-010', {
    entry: entryById('controls'),
    rootDir: '/synthetic/frontend/e2e/controls',
    specs: controlsSuites({ controlBOk: false }),
  });
  writeWrapperStatus(caseDir, { status: 'completed', wrapperRc: 0 });
  writeNewRunDirs(caseDir, [runDir]);
  const { rc, stdout } = runEvaluator(caseDir, 'controls', { runDirToCopy: runDir });
  assert.equal(rc, 1);
  assert.match(stdout, /control B/);
  rmSync(caseDir, { recursive: true, force: true });
});

// --- run-state.json runId 與目錄名不一致（identity 異常） ---
check('run-state.json 的 runId 與目錄名不一致 → identity 異常，exit 1', () => {
  const caseDir = freshDir('runidmismatch');
  const artifactsRoot = path.join(caseDir, 'artifacts-root');
  const runDir = makeGoodRunDir(artifactsRoot, 'run-011', {
    entry: entryById('default'),
    specs: [suiteWithOneTest('glossary.spec.ts', 'glossary', 'renders glossary')],
    runStateOverrides: { runId: 'run-DIFFERENT' },
  });
  writeWrapperStatus(caseDir, { status: 'completed', wrapperRc: 0 });
  writeNewRunDirs(caseDir, [runDir]);
  const { rc, stdout } = runEvaluator(caseDir, 'default', { runDirToCopy: runDir });
  assert.equal(rc, 1);
  assert.match(stdout, /identity 異常/);
  rmSync(caseDir, { recursive: true, force: true });
});

// --- harness.log 最後一行不是 PASSED ---
check('harness.log 最後一行不是「最終結果：PASSED」→ exit 1', () => {
  const caseDir = freshDir('harnesslastline');
  const artifactsRoot = path.join(caseDir, 'artifacts-root');
  const runDir = makeGoodRunDir(artifactsRoot, 'run-012', {
    entry: entryById('default'),
    specs: [suiteWithOneTest('glossary.spec.ts', 'glossary', 'renders glossary')],
    harnessOverrides: { lastLine: '最終結果：FAILED' },
  });
  writeWrapperStatus(caseDir, { status: 'completed', wrapperRc: 0 });
  writeNewRunDirs(caseDir, [runDir]);
  const { rc, stdout } = runEvaluator(caseDir, 'default', { runDirToCopy: runDir });
  assert.equal(rc, 1);
  assert.match(stdout, /最後一行不是/);
  rmSync(caseDir, { recursive: true, force: true });
});

// --- R2 新增反例：同一支 spec（codexApproval.spec.ts）底下 scenario 互換
//     （合成 fixture——目前沒有真實 scenario JSON 樣本，見
//     __fixtures__/real/PROVENANCE.md，這裡只測 evaluator 自己的判定機
//     制，不宣稱這是真實樣本）。 ---
check('R2：scenario entry 的 run-env.scenario 與宣稱的 entry 不同（commandExecution-allow 冒充成 commandExecution-deny 的證據）→ identity 異常', () => {
  const caseDir = freshDir('scenarioswap');
  const artifactsRoot = path.join(caseDir, 'artifacts-root');
  const entry = entryById('commandExecution-allow');
  const runDir = makeGoodRunDir(artifactsRoot, 'run-013', {
    entry,
    rootDir: '/synthetic/frontend/e2e/scenarios',
    specs: [suiteWithOneTest('codexApproval.spec.ts', 'codex approval scenario matrix', 'codex approval scenario matrix: 真 App 啟動')],
    // 故意讓 run-env.json 宣稱是另一個共用同一支 spec 的 scenario。
    runEnvOverrides: { scenario: 'commandExecution-deny' },
  });
  writeWrapperStatus(caseDir, { status: 'completed', wrapperRc: 0 });
  writeNewRunDirs(caseDir, [runDir]);
  const { rc, stdout } = runEvaluator(caseDir, 'commandExecution-allow', { runDirToCopy: runDir });
  assert.equal(rc, 1);
  assert.match(stdout, /run-env\.json 的 scenario="commandExecution-deny"，預期 "commandExecution-allow"/);
  rmSync(caseDir, { recursive: true, force: true });
});

check('R2：scenario entry 的 run-env.scenario 與宣稱的 entry 一致（正例）→ exit 0', () => {
  const caseDir = freshDir('scenariomatch');
  const artifactsRoot = path.join(caseDir, 'artifacts-root');
  const entry = entryById('commandExecution-allow');
  const runDir = makeGoodRunDir(artifactsRoot, 'run-014', {
    entry,
    rootDir: '/synthetic/frontend/e2e/scenarios',
    specs: [suiteWithOneTest('codexApproval.spec.ts', 'codex approval scenario matrix', 'codex approval scenario matrix: 真 App 啟動')],
  });
  writeWrapperStatus(caseDir, { status: 'completed', wrapperRc: 0 });
  writeNewRunDirs(caseDir, [runDir]);
  const { rc, stdout } = runEvaluator(caseDir, 'commandExecution-allow', { runDirToCopy: runDir });
  assert.equal(rc, 0, `預期 exit 0，實際 stdout:\n${stdout}`);
  rmSync(caseDir, { recursive: true, force: true });
});

// ---------------------------------------------------------------------------
// review round 4（#480 R2 剩餘缺口）：可信 CI invocation envelope 反例。
// 每一條都對照 decision.md R2 明列的反例類別：envelope 缺檔／schema 錯
// 誤／同 spec 案例互換／跨 run／工具版本不一致（至少 Node）。
// ---------------------------------------------------------------------------

function makeDefaultGreenRunDir(caseDir, runId) {
  const artifactsRoot = path.join(caseDir, 'artifacts-root');
  return makeGoodRunDir(artifactsRoot, runId, {
    entry: entryById('default'),
    specs: [suiteWithOneTest('glossary.spec.ts', 'glossary', 'renders glossary')],
  });
}

// --- envelope 缺檔 ---
check('R2 envelope：envelope.json 不存在 → exit 1（成功宣稱下不得放行）', () => {
  const caseDir = freshDir('envelopemissing');
  const runDir = makeDefaultGreenRunDir(caseDir, 'run-env-001');
  writeWrapperStatus(caseDir, { status: 'completed', wrapperRc: 0 });
  writeNewRunDirs(caseDir, [runDir]);
  const { rc, stdout } = runEvaluator(caseDir, 'default', { runDirToCopy: runDir, envelope: null });
  assert.equal(rc, 1);
  assert.match(stdout, /envelope\.json 不存在/);
  rmSync(caseDir, { recursive: true, force: true });
});

// --- envelope schema 錯誤（缺欄位） ---
check('R2 envelope：envelope.json 缺 goVersionActual 欄位 → exit 1（schema 錯誤）', () => {
  const caseDir = freshDir('envelopeschema');
  const runDir = makeDefaultGreenRunDir(caseDir, 'run-env-002');
  writeWrapperStatus(caseDir, { status: 'completed', wrapperRc: 0 });
  writeNewRunDirs(caseDir, [runDir]);
  const badEnvelope = makeStampedEnvelope(entryById('default'), path.basename(runDir), {}, { repoRoot: ENVELOPE_REPO_ROOT });
  delete badEnvelope.goVersionActual;
  const { rc, stdout } = runEvaluator(caseDir, 'default', { runDirToCopy: runDir, envelope: badEnvelope });
  assert.equal(rc, 1);
  assert.match(stdout, /envelope\.json 缺欄位或型別錯誤：goVersionActual/);
  rmSync(caseDir, { recursive: true, force: true });
});

// --- envelope 的 checkoutHeadSha 與實際 checkout 不符 ---
check('R2 envelope：checkoutHeadSha 與 evaluator 現場實測的 git rev-parse HEAD 不符 → exit 1', () => {
  const caseDir = freshDir('envelopeshamismatch');
  const runDir = makeDefaultGreenRunDir(caseDir, 'run-env-003');
  writeWrapperStatus(caseDir, { status: 'completed', wrapperRc: 0 });
  writeNewRunDirs(caseDir, [runDir]);
  const fakeSha = 'deadbeefdeadbeefdeadbeefdeadbeefdeadbeef';
  const badEnvelope = makeStampedEnvelope(entryById('default'), path.basename(runDir), {
    checkoutHeadSha: fakeSha,
    testMergeSha: fakeSha, // 保持 checkoutHeadSha/testMergeSha 內部一致，單獨隔離出「與實際 checkout 不符」這條檢查
  }, { repoRoot: ENVELOPE_REPO_ROOT });
  const { rc, stdout } = runEvaluator(caseDir, 'default', { runDirToCopy: runDir, envelope: badEnvelope });
  assert.equal(rc, 1);
  assert.match(stdout, /checkoutHeadSha="deadbeef.*不符/);
  rmSync(caseDir, { recursive: true, force: true });
});

// --- envelope 的 checkoutHeadSha 與 testMergeSha 內部不一致 ---
check('R2 envelope：checkoutHeadSha 與 testMergeSha 內部不一致 → exit 1', () => {
  const caseDir = freshDir('envelopeinternal');
  const runDir = makeDefaultGreenRunDir(caseDir, 'run-env-004');
  writeWrapperStatus(caseDir, { status: 'completed', wrapperRc: 0 });
  writeNewRunDirs(caseDir, [runDir]);
  const badEnvelope = makeStampedEnvelope(entryById('default'), path.basename(runDir), {
    testMergeSha: 'cafecafecafecafecafecafecafecafecafecafe',
  }, { repoRoot: ENVELOPE_REPO_ROOT });
  const { rc, stdout } = runEvaluator(caseDir, 'default', { runDirToCopy: runDir, envelope: badEnvelope });
  assert.equal(rc, 1);
  assert.match(stdout, /checkoutHeadSha=".*" 與 testMergeSha="cafecafe.*不一致/);
  rmSync(caseDir, { recursive: true, force: true });
});

// --- 同 spec 兩個案例互換：commandExecution-allow 的證據被當成
//     commandExecution-deny 的 envelope（decision.md R2 指名的例子） ---
check('R2 envelope：commandExecution-allow 的證據配上 commandExecution-deny 的 envelope → entryId／selectedEnv 不符，exit 1', () => {
  const caseDir = freshDir('envelopeentryswap');
  const artifactsRoot = path.join(caseDir, 'artifacts-root');
  const allowEntry = entryById('commandExecution-allow');
  const runDir = makeGoodRunDir(artifactsRoot, 'run-env-005', {
    entry: allowEntry,
    rootDir: '/synthetic/frontend/e2e/scenarios',
    specs: [suiteWithOneTest('codexApproval.spec.ts', 'codex approval scenario matrix', 'codex approval scenario matrix: 真 App 啟動')],
  });
  writeWrapperStatus(caseDir, { status: 'completed', wrapperRc: 0 });
  writeNewRunDirs(caseDir, [runDir]);
  const denyEnvelope = makeStampedEnvelope(entryById('commandExecution-deny'), path.basename(runDir), {}, { repoRoot: ENVELOPE_REPO_ROOT });
  const { rc, stdout } = runEvaluator(caseDir, 'commandExecution-allow', { runDirToCopy: runDir, envelope: denyEnvelope });
  assert.equal(rc, 1);
  assert.match(stdout, /envelope\.json 的 entryId="commandExecution-deny"，與本次評估的 entry-id="commandExecution-allow" 不符/);
  rmSync(caseDir, { recursive: true, force: true });
});

// --- 跨 run：envelope 的 runId 是上一次 run（或其他 entry）的值 ---
check('R2 envelope：runId 與本次實際 run 目錄不符（跨 run 證據混用）→ exit 1', () => {
  const caseDir = freshDir('envelopecrossrun');
  const runDir = makeDefaultGreenRunDir(caseDir, 'run-env-006');
  writeWrapperStatus(caseDir, { status: 'completed', wrapperRc: 0 });
  writeNewRunDirs(caseDir, [runDir]);
  const badEnvelope = makeStampedEnvelope(entryById('default'), 'run-env-from-a-previous-run', {}, { repoRoot: ENVELOPE_REPO_ROOT });
  const { rc, stdout } = runEvaluator(caseDir, 'default', { runDirToCopy: runDir, envelope: badEnvelope });
  assert.equal(rc, 1);
  assert.match(stdout, /envelope\.json 的 runId="run-env-from-a-previous-run"，與本次實際 run 目錄名稱 "run-env-006" 不符/);
  rmSync(caseDir, { recursive: true, force: true });
});

// --- 工具版本與 envelope 宣告不一致（至少 Node，decision.md 明文要求） ---
check('R2 envelope：nodeVersionActual 與 evaluator 自身執行期的 process.version 不符 → exit 1', () => {
  const caseDir = freshDir('envelopenodeversion');
  const runDir = makeDefaultGreenRunDir(caseDir, 'run-env-007');
  writeWrapperStatus(caseDir, { status: 'completed', wrapperRc: 0 });
  writeNewRunDirs(caseDir, [runDir]);
  const badEnvelope = makeStampedEnvelope(entryById('default'), path.basename(runDir), {
    nodeVersionActual: 'v0.0.0-not-the-real-version',
  }, { repoRoot: ENVELOPE_REPO_ROOT });
  const { rc, stdout } = runEvaluator(caseDir, 'default', { runDirToCopy: runDir, envelope: badEnvelope });
  assert.equal(rc, 1);
  assert.match(stdout, /nodeVersionActual="v0\.0\.0-not-the-real-version".*不符/);
  rmSync(caseDir, { recursive: true, force: true });
});

// --- 正例：完全合法、正確蓋章的 envelope → exit 0（直接證明機制本身，不只是靠 'auto' 隱含覆蓋） ---
check('R2 envelope：完全合法、正確蓋章的 envelope（明確建構，非 auto）→ exit 0', () => {
  const caseDir = freshDir('envelopegreen');
  const runDir = makeDefaultGreenRunDir(caseDir, 'run-env-008');
  writeWrapperStatus(caseDir, { status: 'completed', wrapperRc: 0 });
  writeNewRunDirs(caseDir, [runDir]);
  const goodEnvelope = makeStampedEnvelope(entryById('default'), path.basename(runDir), {}, { repoRoot: ENVELOPE_REPO_ROOT });
  const { rc, stdout } = runEvaluator(caseDir, 'default', { runDirToCopy: runDir, envelope: goodEnvelope });
  assert.equal(rc, 0, `預期 exit 0，實際 stdout:\n${stdout}`);
  rmSync(caseDir, { recursive: true, force: true });
});

console.log(`\n${passed} passed, ${failed} failed`);
cleanupWorkRootIfPortable(SELFTEST_WORKROOT, { failed });
if (failed > 0) process.exitCode = 1;
