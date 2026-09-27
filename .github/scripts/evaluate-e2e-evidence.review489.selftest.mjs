#!/usr/bin/env node
// evaluate-e2e-evidence.review489.selftest.mjs — B3a-CI-1 review round 6
// （reviewer #489 CHANGES_REQUIRED 第四次退回，F1／F2）。
//
// F1（claude-broker-audit.json 合法 array 被當成錯誤）與 F2（多份真實
// domain 資料可跨 run 混用仍 PASS）的真實反例已經用真實本機 scenario run
// 複本重現過，見
// `/Users/eason_tseng/b3a-evidence/ci-1/attempt-004/f1/real-scenario-probe-r6-full-results.json`
// 與
// `/Users/eason_tseng/b3a-evidence/ci-1/attempt-004/f2/cross-run-swap-r6-result.json`
// （唯讀 evidence，不放進本 repo）。這裡把「元素 shape／identity 反例」補
// 成永久、合成（synthetic，明確標示）的 selftest，涵蓋各個具體錯誤形狀，
// 不依賴外部真實檔案，跟既有 review483 selftest 同一套 buildCase 風格。
//
// 執行：node .github/scripts/evaluate-e2e-evidence.review489.selftest.mjs
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { cpSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { ensureSelftestWorkRoot, cleanupWorkRootIfPortable } from './__fixtures__/selftestWorkroot.mjs';
import { entryById } from './ci-e2e-entries.mjs';
import { envelopeRepoRoot, makeStampedEnvelope } from './__fixtures__/envelopeFixture.mjs';
import { makeGoodRunDir, suiteWithOneTest } from './__fixtures__/evidence-builder.mjs';

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

function buildCase(label, entryId, { domainOverrides = {} } = {}) {
  const entry = entryById(entryId);
  const caseDir = freshDir(label);
  const artifactsRoot = path.join(caseDir, 'artifacts-root');
  const runId = `${label}-run`;
  const file = path.basename(entry.expectedSpecFiles[0]);
  const sub = path.dirname(entry.expectedSpecFiles[0]);
  const rootDir = sub === '.' ? '/synthetic/frontend/e2e' : `/synthetic/frontend/e2e/${sub}`;
  const specs = [suiteWithOneTest(file, 'actual test', 'actual test')];
  const runDir = makeGoodRunDir(artifactsRoot, runId, { entry, rootDir, specs, domainOverrides });
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

// domainOverrides 走 scenarioDomainContent() 既有的合併路徑，只能碰到特定
// 欄位（見 evidence-builder.mjs 各分支）；有些反例（JSONL 的第一筆 request
// 記錄、整份陣列取代）domainOverrides 碰不到，這裡在建好「完整正確」的
// fixture 之後直接複寫檔案本身（run 目錄與 package 複本一起改，維持兩邊
// bit-identical，這樣測到的才是 identity 檢查本身，不是複製完整性檢查）。
function overwriteDomainFile(runDir, packageDir, domainFile, content) {
  const text = typeof content === 'string' ? content : `${JSON.stringify(content, null, 2)}\n`;
  writeFileSync(path.join(runDir, domainFile), text);
  writeFileSync(path.join(packageDir, 'artifacts', domainFile), text);
}

function runEval(caseDir, packageDir, entryId) {
  const r = spawnSync('node', [EVALUATOR, caseDir, packageDir, entryId], {
    encoding: 'utf8', timeout: 15_000,
    env: { ...process.env, CI_E2E_REPO_ROOT: ENVELOPE_REPO_ROOT, CI_E2E_SELFTEST_ACTUAL_NODE_VERSION: 'v26.10.0' },
  });
  return { rc: r.status, stdout: r.stdout, stderr: r.stderr };
}

// ---------------------------------------------------------------------------
// F1：claude-broker-audit.json 合法 array 的 schema／identity 反例
// （真實正例已用 claude-approval-deny 的真實 run 20260922T000918Z-c85e82
// 復現過，見本檔頭註）。
// ---------------------------------------------------------------------------

check('F1 反例：claude-broker-audit.json 是空陣列 → exit 1（不是全面放行任意陣列）', () => {
  const { caseDir, packageDir, runDir } = buildCase('f1-empty-array', 'claude-approval-deny');
  overwriteDomainFile(runDir, packageDir, 'claude-broker-audit.json', []);
  const { rc, stdout } = runEval(caseDir, packageDir, 'claude-approval-deny');
  assert.equal(rc, 1);
  assert.match(stdout, /claude-broker-audit\.json 是空陣列/);
});

check('F1 反例：claude-broker-audit.json 元素缺 kind/ts 欄位 → exit 1', () => {
  const { caseDir, packageDir, runDir } = buildCase('f1-corrupted-element', 'claude-approval-deny');
  overwriteDomainFile(runDir, packageDir, 'claude-broker-audit.json', [{ foo: 'bar' }]);
  const { rc, stdout } = runEval(caseDir, packageDir, 'claude-approval-deny');
  assert.equal(rc, 1);
  assert.match(stdout, /claude-broker-audit\.json 有元素缺 kind（字串）或 ts（字串）欄位/);
});

check('F1 反例：claude-broker-audit.json 找不到 kind="decision" 的記錄 → exit 1', () => {
  const { caseDir, packageDir, runDir } = buildCase('f1-no-decision', 'claude-approval-deny');
  overwriteDomainFile(runDir, packageDir, 'claude-broker-audit.json', [
    { kind: 'request', ts: '2026-09-28T00:00:00.000Z', data: { id: 'a1' } },
  ]);
  const { rc, stdout } = runEval(caseDir, packageDir, 'claude-approval-deny');
  assert.equal(rc, 1);
  assert.match(stdout, /claude-broker-audit\.json 找不到 kind="decision" 的記錄/);
});

check('F1 反例：claude-broker-audit.json 的 decision.data.behavior 錯 identity（deny 案混進 allow 的決策）→ exit 1', () => {
  const { caseDir, packageDir, runDir } = buildCase('f1-wrong-behavior', 'claude-approval-deny');
  overwriteDomainFile(runDir, packageDir, 'claude-broker-audit.json', [
    { kind: 'request', ts: '2026-09-28T00:00:00.000Z', data: { id: 'a1' } },
    { kind: 'decision', ts: '2026-09-28T00:00:01.000Z', data: { id: 'a1', behavior: 'allow' } },
  ]);
  const { rc, stdout } = runEval(caseDir, packageDir, 'claude-approval-deny');
  assert.equal(rc, 1);
  assert.match(stdout, /claude-broker-audit\.json 的 decision\.data\.behavior="allow"，預期 "deny"/);
});

check('F1 反例（回歸，原始 bug 形狀）：claude-broker-audit.json 是物件（selectBrokerAuditForApproval 實際回傳陣列）→ exit 1', () => {
  const { caseDir, packageDir, runDir } = buildCase('f1-object-not-array', 'claude-approval-deny');
  overwriteDomainFile(runDir, packageDir, 'claude-broker-audit.json', { decision: 'x' });
  const { rc, stdout } = runEval(caseDir, packageDir, 'claude-approval-deny');
  assert.equal(rc, 1);
  assert.match(stdout, /claude-broker-audit\.json 不是陣列/);
});

// ---------------------------------------------------------------------------
// F2：codex 五案跨 run 混用的 identity 反例（真實六檔互換已用
// commandExecution-allow／fileChange-allow 的真實 run 復現過，見本檔頭
// 註）。這裡用合成 fixture 逐檔驗證新增的 identity 檢查本身。
// ---------------------------------------------------------------------------

check('F2 反例：scenario-wire.log.manifest.json 的 scenario 欄位被換成別的 scenario → exit 1', () => {
  const { caseDir, packageDir } = buildCase('f2-manifest-scenario', 'commandExecution-allow', {
    domainOverrides: { 'scenario-wire.log.manifest.json': { scenario: 'fileChange-allow' } },
  });
  const { rc, stdout } = runEval(caseDir, packageDir, 'commandExecution-allow');
  assert.equal(rc, 1);
  assert.match(stdout, /scenario-wire\.log\.manifest\.json 的 scenario="fileChange-allow"，預期 "commandExecution-allow"/);
});

check('F2 反例：scenario-wire.log.manifest.json 的 approvalRequestId 被換成別的 run → exit 1', () => {
  const { caseDir, packageDir } = buildCase('f2-manifest-requestid', 'commandExecution-allow', {
    domainOverrides: { 'scenario-wire.log.manifest.json': { approvalRequestId: 'b3a2b2-approval-some-other-run' } },
  });
  const { rc, stdout } = runEval(caseDir, packageDir, 'commandExecution-allow');
  assert.equal(rc, 1);
  assert.match(stdout, /scenario-wire\.log\.manifest\.json 的 approvalRequestId=.*可能是別的 run 的 manifest 被冒充/);
});

check('F2 反例：app-workspace-sessions.json 的 resume_session_id 與本次 threadId 不符 → exit 1', () => {
  const { caseDir, packageDir } = buildCase('f2-session-resume', 'commandExecution-allow', {
    domainOverrides: { 'app-workspace-sessions.json': { resume_session_id: 'b3a2b2-thread-some-other-run' } },
  });
  const { rc, stdout } = runEval(caseDir, packageDir, 'commandExecution-allow');
  assert.equal(rc, 1);
  assert.match(stdout, /app-workspace-sessions\.json 沒有 provider="codex" 且 resume_session_id=.*可能是別的 run 的 session 登記被冒充/);
});

check('F2 反例：app-audit.jsonl 的 codex_approval_request.raw_params 與 scenario-config.json 的 threadId/turnId/itemId 不符 → exit 1', () => {
  const { caseDir, packageDir, runDir } = buildCase('f2-audit-rawparams', 'commandExecution-allow');
  const auditPath = path.join(runDir, 'app-audit.jsonl');
  const lines = readFileSync(auditPath, 'utf8').trim().split('\n').map((l) => JSON.parse(l));
  const requestLine = lines.find((l) => l.kind === 'codex_approval_request');
  requestLine.data.raw_params = { threadId: 'b3a2b2-thread-other-run', turnId: 'b3a2b2-turn-other-run', itemId: 'b3a2b2-item-other-run' };
  const text = `${lines.map((l) => JSON.stringify(l)).join('\n')}\n`;
  writeFileSync(auditPath, text);
  writeFileSync(path.join(packageDir, 'artifacts', 'app-audit.jsonl'), text);
  const { rc, stdout } = runEval(caseDir, packageDir, 'commandExecution-allow');
  assert.equal(rc, 1);
  assert.match(stdout, /app-audit\.jsonl 的 codex_approval_request\.raw_params=.*可能是別的 run／scenario 的 audit 原文被冒充/);
});

check('F2 反例：app-wire-log.meta.json 的 argv 沒有任何路徑含本次 run 目錄名稱 → exit 1', () => {
  const { caseDir, packageDir } = buildCase('f2-meta-argv', 'commandExecution-allow', {
    domainOverrides: { 'app-wire-log.meta.json': { argv: ['node', '/some/other/run/fake-tools/codex-cli/bin/codex', 'app-server'] } },
  });
  const { rc, stdout } = runEval(caseDir, packageDir, 'commandExecution-allow');
  assert.equal(rc, 1);
  assert.match(stdout, /app-wire-log\.meta\.json 的 argv=.*可能是別的 run 的 wire meta 被冒充/);
});

check('F2 反例：scenario-wire.log 沒有任何 frame 的 result.thread.id 對得上本次 threadId → exit 1', () => {
  const { caseDir, packageDir, runDir } = buildCase('f2-wire-log-thread', 'commandExecution-allow');
  const text = `${JSON.stringify({ seq: 1, dir: 'c2s', frame: { id: 1, method: 'initialize' } })}\n`;
  writeFileSync(path.join(runDir, 'scenario-wire.log'), text);
  writeFileSync(path.join(packageDir, 'artifacts', 'scenario-wire.log'), text);
  const { rc, stdout } = runEval(caseDir, packageDir, 'commandExecution-allow');
  assert.equal(rc, 1);
  assert.match(stdout, /scenario-wire\.log 沒有任何 frame 的 result\.thread\.id=.*可能是別的 run 的 wire log 原文被冒充/);
});

check('F2 反例：app-wire-log.jsonl 沒有任何 frame 的 wsid 對得上 app-audit.jsonl 建立的 wsid → exit 1', () => {
  const { caseDir, packageDir, runDir } = buildCase('f2-wire-log-wsid', 'commandExecution-allow');
  const text = `${JSON.stringify({ dir: 'c2s', wsid: 'some-other-wsid', raw: {} })}\n`;
  writeFileSync(path.join(runDir, 'app-wire-log.jsonl'), text);
  writeFileSync(path.join(packageDir, 'artifacts', 'app-wire-log.jsonl'), text);
  const { rc, stdout } = runEval(caseDir, packageDir, 'commandExecution-allow');
  assert.equal(rc, 1);
  assert.match(stdout, /app-wire-log\.jsonl 沒有任何 wire frame 的 wsid="w1"（本次 approval 的 wsid，見 app-audit\.jsonl 的 codex_approval_request）——可能是別的 run 的 wire log 被冒充/);
});

console.log(`\n${passed} passed, ${failed} failed`);
cleanupWorkRootIfPortable(SELFTEST_WORKROOT, { failed });
if (failed > 0) process.exitCode = 1;
