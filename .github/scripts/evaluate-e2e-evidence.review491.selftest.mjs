#!/usr/bin/env node
// evaluate-e2e-evidence.review491.selftest.mjs — B3a-CI-1 review round 7
// （reviewer #491 CHANGES_REQUIRED，G1：Claude judgement 與 recovery 輪次
// 仍未和原始資料關聯）。
//
// reviewer 用真實 run 副本證實兩個反例（唯讀，不在這裡執行，見
// `/Users/eason_tseng/b3a-evidence/2026-09-28-review491/`）：
//   1. `claude-ui-judgement.json` 換成另一個 allow run 的原文（deny 案的
//      expectation／audit／run-env／CI envelope 都不動）→ evaluator 仍
//      rc0。
//   2. `claude-approval-recovery` 的 round1/round2 三個 registry/audit 檔
//      （sessions.json／workspace-sessions.json／audit.jsonl）互換 →
//      evaluator 仍 rc0。
// 這兩個真實反例的紅燈／綠燈證據（含本輪新增的 allow／recovery 單檔互換變
// 體）保存在
// `/Users/eason_tseng/b3a-evidence/ci-1/attempt-005/g1/out-red/`與
// `out-green/`（repro.mjs，唯讀 evidence，不進 repo）。這裡把「欄位形狀反
// 例」補成永久、合成（synthetic，明確標示）的 selftest，涵蓋各個具體錯誤
// 形狀，不依賴外部真實檔案，跟既有 review483/review489 selftest 同一套
// buildCase 風格。
//
// 執行：node .github/scripts/evaluate-e2e-evidence.review491.selftest.mjs
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

function buildCase(label, entryId, { domainOverrides = {}, runStateOverrides = {} } = {}) {
  const entry = entryById(entryId);
  const caseDir = freshDir(label);
  const artifactsRoot = path.join(caseDir, 'artifacts-root');
  const runId = `${label}-run`;
  const file = path.basename(entry.expectedSpecFiles[0]);
  const sub = path.dirname(entry.expectedSpecFiles[0]);
  const rootDir = sub === '.' ? '/synthetic/frontend/e2e' : `/synthetic/frontend/e2e/${sub}`;
  const specs = [suiteWithOneTest(file, 'actual test', 'actual test')];
  const runDir = makeGoodRunDir(artifactsRoot, runId, {
    entry, rootDir, specs, domainOverrides, runStateOverrides,
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

// domainOverrides 走 scenarioDomainContent() 既有的合併路徑；有些反例
// （JSONL 逐行、整份陣列取代）碰不到，直接複寫檔案本身（run 目錄與 package
// 複本一起改，維持兩邊 bit-identical，這樣測到的才是 identity 檢查本身，
// 不是複製完整性檢查）——跟 review489.selftest.mjs 同一套手法。
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
// 正例（回歸）：「正確」fixture 對 allow／deny／recovery 三個 Claude entry
// 仍然 exit 0——新增的身分關聯檢查沒有誤傷正常路徑。
// ---------------------------------------------------------------------------

for (const id of ['claude-approval-allow', 'claude-approval-deny', 'claude-approval-recovery']) {
  check(`正例：${id} 的正確 fixture（含本輪新增的 run-state processes／judgement 身分欄位）→ exit 0`, () => {
    const { caseDir, packageDir } = buildCase(`g1-good-${id}`, id);
    const { rc, stdout } = runEval(caseDir, packageDir, id);
    assert.equal(rc, 0, stdout);
  });
}

// ---------------------------------------------------------------------------
// G1／claude-ui-judgement.json（allow／deny 兩案共用同一套檢查邏輯）
// ---------------------------------------------------------------------------

check('G1 反例（reviewer 反例的合成版）：claude-ui-judgement.json.observedBrokerId 跟本次 claude-broker-audit.json 的 decision id 不符 → exit 1', () => {
  const { caseDir, packageDir } = buildCase('g1-judgement-wrong-broker-id', 'claude-approval-deny', {
    domainOverrides: { 'claude-ui-judgement.json': { observedBrokerId: 'not-this-run-a1', agreedApprovalId: 'not-this-run-a1' } },
  });
  const { rc, stdout } = runEval(caseDir, packageDir, 'claude-approval-deny');
  assert.equal(rc, 1);
  assert.match(stdout, /observedBrokerId="not-this-run-a1".*與本次 claude-broker-audit\.json 的 decision id "a1" 不符/);
});

check('G1 反例：claude-ui-judgement.json 缺 observedBrokerId → exit 1', () => {
  const { caseDir, packageDir, runDir } = buildCase('g1-judgement-missing-observed', 'claude-approval-deny');
  const parsed = JSON.parse(readFileSync(path.join(runDir, 'claude-ui-judgement.json'), 'utf8'));
  delete parsed.observedBrokerId;
  overwriteDomainFile(runDir, packageDir, 'claude-ui-judgement.json', parsed);
  const { rc, stdout } = runEval(caseDir, packageDir, 'claude-approval-deny');
  assert.equal(rc, 1);
  assert.match(stdout, /缺 observedBrokerId（非空字串）/);
});

check('G1 反例：claude-ui-judgement.json 的 agreedApprovalId 跟 observedBrokerId 不一致 → exit 1', () => {
  const { caseDir, packageDir } = buildCase('g1-judgement-agreed-mismatch', 'claude-approval-allow', {
    domainOverrides: { 'claude-ui-judgement.json': { agreedApprovalId: 'a1-but-different' } },
  });
  const { rc, stdout } = runEval(caseDir, packageDir, 'claude-approval-allow');
  assert.equal(rc, 1);
  assert.match(stdout, /agreedApprovalId="a1-but-different" 與 observedBrokerId="a1" 不一致/);
});

check('G1 反例（reviewer 反例的合成版）：claude-ui-judgement.json.configWsid 跟 run-state.json 觀察到的 WSID 不符 → exit 1', () => {
  const { caseDir, packageDir } = buildCase('g1-judgement-wrong-wsid', 'claude-approval-deny', {
    domainOverrides: { 'claude-ui-judgement.json': { configWsid: 'w-from-another-run' } },
  });
  const { rc, stdout } = runEval(caseDir, packageDir, 'claude-approval-deny');
  assert.equal(rc, 1);
  assert.match(stdout, /configWsid="w-from-another-run".*與 run-state\.json 觀察到的 WSID "w1" 不符/);
});

check('G1 反例：claude-ui-judgement.json 缺 configWsid → exit 1', () => {
  const { caseDir, packageDir, runDir } = buildCase('g1-judgement-missing-wsid', 'claude-approval-allow');
  const parsed = JSON.parse(readFileSync(path.join(runDir, 'claude-ui-judgement.json'), 'utf8'));
  delete parsed.configWsid;
  overwriteDomainFile(runDir, packageDir, 'claude-ui-judgement.json', parsed);
  const { rc, stdout } = runEval(caseDir, packageDir, 'claude-approval-allow');
  assert.equal(rc, 1);
  assert.match(stdout, /缺 configWsid（非空字串）/);
});

check('G1 反例：run-state.json 找不到 mcp-<WSID>.json 形式的 process 指令 → exit 1（configWsid 無從核對）', () => {
  const { caseDir, packageDir } = buildCase('g1-runstate-no-wsid', 'claude-approval-deny', {
    runStateOverrides: { processes: [{ command: 'wails dev -noreload' }] },
  });
  const { rc, stdout } = runEval(caseDir, packageDir, 'claude-approval-deny');
  assert.equal(rc, 1);
  assert.match(stdout, /run-state\.json 找不到 mcp-<WSID>\.json 形式的 process 指令/);
});

// ---------------------------------------------------------------------------
// G1／claude-approval-recovery：judgement.json 與 round1/round2 的身分關聯
// ---------------------------------------------------------------------------

check('G1 反例：claude-recovery/judgement.json 缺 judged.agreedApprovalIds → exit 1', () => {
  const { caseDir, packageDir } = buildCase('g1-recovery-missing-ids', 'claude-approval-recovery', {
    domainOverrides: { 'claude-recovery/judgement.json': { judged: { violations: [] } } },
  });
  const { rc, stdout } = runEval(caseDir, packageDir, 'claude-approval-recovery');
  assert.equal(rc, 1);
  assert.match(stdout, /judged\.agreedApprovalIds 不是長度 2 的非空字串陣列/);
});

check('G1 反例：claude-recovery/judgement.json 缺 round1.registryBinding.wsid → exit 1', () => {
  const { caseDir, packageDir } = buildCase('g1-recovery-missing-round1-wsid', 'claude-approval-recovery', {
    domainOverrides: { 'claude-recovery/judgement.json': { round1: { appBoundResume: 'S' } } },
  });
  const { rc, stdout } = runEval(caseDir, packageDir, 'claude-approval-recovery');
  assert.equal(rc, 1);
  assert.match(stdout, /judged\.round1 缺 appBoundResume／registryBinding\.wsid/);
});

check('G1 反例（reviewer 反例的合成版，round1/round2 audit.jsonl 互換的最小形狀）：round1/audit.jsonl 最後一筆 decision id 是 round2 的 id → exit 1', () => {
  const { caseDir, packageDir, runDir } = buildCase('g1-recovery-round1-audit-has-round2-id', 'claude-approval-recovery');
  const swapped = [
    { ts: '2026-09-28T00:00:00.000Z', kind: 'request', data: { id: 'a1' } },
    { ts: '2026-09-28T00:00:01.000Z', kind: 'decision', data: { id: 'a1', behavior: 'allow' } },
    { ts: '2026-09-28T00:00:02.000Z', kind: 'request', data: { id: 'a2' } },
    { ts: '2026-09-28T00:00:03.000Z', kind: 'decision', data: { id: 'a2', behavior: 'allow' } },
  ];
  const text = `${swapped.map((l) => JSON.stringify(l)).join('\n')}\n`;
  overwriteDomainFile(runDir, packageDir, 'claude-recovery/round1/audit.jsonl', text);
  const { rc, stdout } = runEval(caseDir, packageDir, 'claude-approval-recovery');
  assert.equal(rc, 1);
  assert.match(stdout, /round1\/audit\.jsonl 最後一筆 decision id="a2".*judged\.agreedApprovalIds\[0\]="a1" 不符/);
});

check('G1 反例（reviewer 反例的合成版）：round2/audit.jsonl 最後一筆 decision id 只剩 round1 的 id（缺 round2 的 decision）→ exit 1', () => {
  const { caseDir, packageDir, runDir } = buildCase('g1-recovery-round2-audit-only-round1-id', 'claude-approval-recovery');
  const swapped = [
    { ts: '2026-09-28T00:00:00.000Z', kind: 'request', data: { id: 'a1' } },
    { ts: '2026-09-28T00:00:01.000Z', kind: 'decision', data: { id: 'a1', behavior: 'allow' } },
  ];
  const text = `${swapped.map((l) => JSON.stringify(l)).join('\n')}\n`;
  overwriteDomainFile(runDir, packageDir, 'claude-recovery/round2/audit.jsonl', text);
  const { rc, stdout } = runEval(caseDir, packageDir, 'claude-approval-recovery');
  assert.equal(rc, 1);
  assert.match(stdout, /round2\/audit\.jsonl 最後一筆 decision id="a1".*judged\.agreedApprovalIds\[1\]="a2" 不符/);
});

check('G1 反例：round1/sessions.json 沒有 judged.round1.appBoundResume 對應的登記 → exit 1', () => {
  const { caseDir, packageDir, runDir } = buildCase('g1-recovery-round1-sessions-wrong-key', 'claude-approval-recovery');
  overwriteDomainFile(runDir, packageDir, 'claude-recovery/round1/sessions.json', { 'some-other-session-id': { wsid: 'w1' } });
  const { rc, stdout } = runEval(caseDir, packageDir, 'claude-approval-recovery');
  assert.equal(rc, 1);
  assert.match(stdout, /round1\/sessions\.json 沒有 key="b3a2b2-claude-session-[^"]+" 的登記/);
});

check('G1 反例：round2/workspace-sessions.json 的 wsid 登記 resume_session_id 跟 judged.round2.appBoundResume 不符 → exit 1', () => {
  const { caseDir, packageDir, runDir } = buildCase('g1-recovery-round2-workspace-sessions-wrong-resume', 'claude-approval-recovery');
  overwriteDomainFile(runDir, packageDir, 'claude-recovery/round2/workspace-sessions.json', { entries: { w1: { resume_session_id: 'some-other-session' } } });
  const { rc, stdout } = runEval(caseDir, packageDir, 'claude-approval-recovery');
  assert.equal(rc, 1);
  assert.match(stdout, /round2\/workspace-sessions\.json 的 entries\["w1"\]\.resume_session_id="some-other-session"，與 judged\.round2\.appBoundResume="b3a2b2-claude-session-[^"]+" 不符/);
});

// Reviewer #493: consistent foreign records must still bind to this run.
check('recovery: consistent foreign session across expectation/judgement/registries is rejected', () => {
  const { caseDir, packageDir, runDir } = buildCase('recovery-foreign-session', 'claude-approval-recovery');
  const foreign = 'b3a2b2-claude-session-OTHER-RUN';
  const expectation = JSON.parse(readFileSync(path.join(runDir, 'claude-expectation.json'), 'utf8'));
  for (const [i, round] of expectation.rounds.entries()) {
    round.approval = { ...round.approval, sessionId: foreign };
    round.resume = i === 0 ? null : foreign;
  }
  overwriteDomainFile(runDir, packageDir, 'claude-expectation.json', expectation);
  const judgement = JSON.parse(readFileSync(path.join(runDir, 'claude-recovery/judgement.json'), 'utf8'));
  for (const round of ['round1', 'round2']) {
    const old = judgement[round].appBoundResume;
    judgement[round].appBoundResume = foreign;
    const sessionsFile = `claude-recovery/${round}/sessions.json`;
    const sessions = JSON.parse(readFileSync(path.join(runDir, sessionsFile), 'utf8'));
    sessions[foreign] = sessions[old]; delete sessions[old];
    overwriteDomainFile(runDir, packageDir, sessionsFile, sessions);
    const workspaceFile = `claude-recovery/${round}/workspace-sessions.json`;
    const workspace = JSON.parse(readFileSync(path.join(runDir, workspaceFile), 'utf8'));
    for (const value of Object.values(workspace.entries)) value.resume_session_id = foreign;
    overwriteDomainFile(runDir, packageDir, workspaceFile, workspace);
  }
  overwriteDomainFile(runDir, packageDir, 'claude-recovery/judgement.json', judgement);
  const result = runEval(caseDir, packageDir, 'claude-approval-recovery');
  assert.equal(result.rc, 1, result.stdout);
});

check('recovery: consistent foreign WSID is rejected against run-state', () => {
  const { caseDir, packageDir, runDir } = buildCase('recovery-foreign-wsid', 'claude-approval-recovery');
  const judgement = JSON.parse(readFileSync(path.join(runDir, 'claude-recovery/judgement.json'), 'utf8'));
  for (const round of ['round1', 'round2']) {
    const previous = judgement[round].registryBinding.wsid;
    judgement[round].registryBinding.wsid = 'otherWsid';
    const sessionsFile = `claude-recovery/${round}/sessions.json`;
    const sessions = JSON.parse(readFileSync(path.join(runDir, sessionsFile), 'utf8'));
    for (const value of Object.values(sessions)) value.wsid = 'otherWsid';
    overwriteDomainFile(runDir, packageDir, sessionsFile, sessions);
    const workspaceFile = `claude-recovery/${round}/workspace-sessions.json`;
    const workspace = JSON.parse(readFileSync(path.join(runDir, workspaceFile), 'utf8'));
    workspace.entries.otherWsid = workspace.entries[previous]; delete workspace.entries[previous];
    overwriteDomainFile(runDir, packageDir, workspaceFile, workspace);
  }
  overwriteDomainFile(runDir, packageDir, 'claude-recovery/judgement.json', judgement);
  const result = runEval(caseDir, packageDir, 'claude-approval-recovery');
  assert.equal(result.rc, 1, result.stdout);
});

check('recovery: second-round resume must match this run session', () => {
  const { caseDir, packageDir, runDir } = buildCase('recovery-wrong-resume', 'claude-approval-recovery');
  const expectation = JSON.parse(readFileSync(path.join(runDir, 'claude-expectation.json'), 'utf8'));
  expectation.rounds[1].resume = 'foreign-session';
  overwriteDomainFile(runDir, packageDir, 'claude-expectation.json', expectation);
  const result = runEval(caseDir, packageDir, 'claude-approval-recovery');
  assert.equal(result.rc, 1, result.stdout);
});

console.log(`\n${passed} passed, ${failed} failed`);
cleanupWorkRootIfPortable(SELFTEST_WORKROOT, { failed });
if (failed > 0) process.exitCode = 1;
