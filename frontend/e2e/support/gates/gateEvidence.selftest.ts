// gateEvidence.ts 的正負案例（review #427 必修缺陷 6／review #4（#430）
// 必修缺陷 1：gate 證據會被 teardown 刪光，且讀檔失敗曾被無條件吞成
// null、finalStatus 仍可寫成 passed）。
//
// **review #4 必修缺陷 1 核心修正證據**：`EISDIR` 案逐一對應 reviewer
// `probe.mjs` 的 `deterministic EISDIR read failure`（把 audit.jsonl 換成
// 目錄）——舊版探針顯示 `evidence_read_error_flush passed
// {...,"audit_jsonl":null,...}`，本檔的對應案例驗證新版會如實記錄
// `status:'read_error'` 並在 `finalStatus==='passed'` 時 throw。
//
// 執行：node --experimental-loader=./e2e/support/selftestJsToTsLoader.mjs e2e/support/gates/gateEvidence.selftest.ts
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { GateEvidenceRecorder } from './gateEvidence.js';

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

function tmpDir(): string {
  return fs.mkdtempSync(path.join(os.tmpdir(), 'gate-evidence-selftest-'));
}

function readEvidence(artifactsDir: string): { flow: string; runId: string; bodyStatus: string; finalStatus: string; steps: Array<{ label: string; data: unknown }> } {
  return JSON.parse(fs.readFileSync(path.join(artifactsDir, 'gate-evidence.json'), 'utf8'));
}

function journalsOf(evidence: ReturnType<typeof readEvidence>): Record<string, { status: string; content?: string; error?: string }> {
  const step = evidence.steps.find(s => s.label === 'journals-snapshot');
  return step!.data as Record<string, { status: string; content?: string; error?: string }>;
}

check('正例：flush 寫出包含 steps／journal 快照／finalStatus 的 gate-evidence.json，三個 journal 皆 status=ok', () => {
  const artifactsDir = tmpDir();
  const workspaceDir = tmpDir();
  fs.mkdirSync(path.join(workspaceDir, '.workbench'), { recursive: true });
  fs.writeFileSync(path.join(workspaceDir, '.workbench', 'gate.jsonl'), '{"op_id":"op-1","at":"t","records":[]}\n');
  fs.writeFileSync(path.join(workspaceDir, '.workbench', 'audit.jsonl'), '{"ts":"t","kind":"x","data":{}}\n');
  fs.writeFileSync(path.join(workspaceDir, '.workbench', 'events.jsonl'), '');

  const rec = new GateEvidenceRecorder('gate1', 'run-abc');
  rec.record('baseline-bindings', { spec_manifest: 'sha256:aaa' });
  rec.record('commit', { c1: 'deadbeef' });
  rec.flush(artifactsDir, workspaceDir, 'passed');

  const evidence = readEvidence(artifactsDir);
  assert.equal(evidence.flow, 'gate1');
  assert.equal(evidence.runId, 'run-abc');
  assert.equal(evidence.finalStatus, 'passed');
  const labels = evidence.steps.map(s => s.label);
  assert.ok(labels.includes('baseline-bindings'));
  assert.ok(labels.includes('commit'));
  assert.ok(labels.includes('journals-snapshot'));
  assert.ok(labels.includes('final-status'));
  const journals = journalsOf(evidence);
  assert.equal(journals.gate_jsonl.status, 'ok');
  assert.match(journals.gate_jsonl.content!, /op_id/);
  assert.equal(journals.audit_jsonl.status, 'ok');
  assert.match(journals.audit_jsonl.content!, /"kind":"x"/);
  assert.equal(journals.events_jsonl.status, 'ok');
});

check('負案例：同一個 recorder flush 兩次必須 throw（單一寫入者）', () => {
  const artifactsDir = tmpDir();
  const workspaceDir = tmpDir();
  fs.mkdirSync(path.join(workspaceDir, '.workbench'), { recursive: true });
  fs.writeFileSync(path.join(workspaceDir, '.workbench', 'gate.jsonl'), '{"op_id":"op-1","at":"t","records":[]}\n');
  fs.writeFileSync(path.join(workspaceDir, '.workbench', 'audit.jsonl'), '');
  fs.writeFileSync(path.join(workspaceDir, '.workbench', 'events.jsonl'), '');
  const rec = new GateEvidenceRecorder('gate2', 'run-xyz');
  rec.flush(artifactsDir, workspaceDir, 'passed');
  assert.throws(() => rec.flush(artifactsDir, workspaceDir, 'passed'), /已經 flush 過一次/);
});

check('負案例：artifactsDir 已存在 gate-evidence.json 時必須 throw，不覆寫（可能是失敗或舊的判定）', () => {
  const artifactsDir = tmpDir();
  const workspaceDir = tmpDir();
  fs.mkdirSync(path.join(workspaceDir, '.workbench'), { recursive: true });
  fs.writeFileSync(path.join(artifactsDir, 'gate-evidence.json'), '{"stale":"leftover from a previous run"}');
  const rec = new GateEvidenceRecorder('stale', 'run-new');
  assert.throws(() => rec.flush(artifactsDir, workspaceDir, 'passed'), /已存在/);
  // 確認舊內容真的沒被覆寫。
  const content = fs.readFileSync(path.join(artifactsDir, 'gate-evidence.json'), 'utf8');
  assert.match(content, /leftover from a previous run/);
});

check('正例：finalStatus="failed" 且 journal 缺失時仍可正常 flush（失敗流程允許不完整證據，但要如實記錄 status=missing）', () => {
  const artifactsDir = tmpDir();
  const workspaceDir = tmpDir();
  fs.mkdirSync(path.join(workspaceDir, '.workbench'), { recursive: true }); // 三個 journal 都不建立
  const rec = new GateEvidenceRecorder('gate1', 'run-fail');
  rec.record('some-assertion', { ok: false, reason: 'digest mismatch' });
  assert.doesNotThrow(() => rec.flush(artifactsDir, workspaceDir, 'failed'));
  const evidence = readEvidence(artifactsDir);
  assert.equal(evidence.finalStatus, 'failed');
  const journals = journalsOf(evidence);
  assert.equal(journals.gate_jsonl.status, 'missing');
  assert.equal(journals.audit_jsonl.status, 'missing');
  assert.equal(journals.events_jsonl.status, 'missing');
});

check('負案例（review #4 必修缺陷 1 核心：對應舊版把「workspace 不存在」當合法 passed 案例——現在必須 throw）：workspaceDir 完全不存在時，finalStatus=passed 必須 throw，不得放行', () => {
  const artifactsDir = tmpDir();
  const workspaceDir = path.join(os.tmpdir(), `gate-evidence-selftest-never-created-${Date.now()}`);
  const rec = new GateEvidenceRecorder('gate2', 'run-no-workspace');
  assert.throws(() => rec.flush(artifactsDir, workspaceDir, 'passed'), /finalStatus=passed 但以下 journal 無法完整讀取/);
  // 證據仍應被寫出（供事後鑑識），即使因為不完整而判定失敗。
  const evidence = readEvidence(artifactsDir);
  assert.equal(evidence.finalStatus, 'failed', 'journal 缺失時，保存的結果也必須失敗');
  assert.equal(evidence.bodyStatus, 'passed', '分開保存 test body 原始結果');
  assert.equal(evidence.steps.find(s => s.label === 'final-status')?.data, 'failed');
  const journals = journalsOf(evidence);
  assert.equal(journals.gate_jsonl.status, 'missing');
  assert.equal(journals.audit_jsonl.status, 'missing');
  assert.equal(journals.events_jsonl.status, 'missing');
});

check('負案例（reviewer probe.mjs 的 EISDIR 重現）：audit.jsonl 是目錄（讀取失敗，不是缺檔）時，finalStatus=passed 必須 throw，且錯誤訊息含 read_error 而非把它當成 missing', () => {
  const artifactsDir = tmpDir();
  const workspaceDir = tmpDir();
  const wb = path.join(workspaceDir, '.workbench');
  fs.mkdirSync(wb, { recursive: true });
  fs.writeFileSync(path.join(wb, 'gate.jsonl'), '{"op_id":"op-1","at":"t","records":[]}\n');
  fs.writeFileSync(path.join(wb, 'events.jsonl'), '');
  fs.mkdirSync(path.join(wb, 'audit.jsonl')); // 決定性製造 EISDIR（reviewer 的手法）

  const rec = new GateEvidenceRecorder('stale', 'run-eisdir');
  assert.throws(() => rec.flush(artifactsDir, workspaceDir, 'passed'), /finalStatus=passed 但以下 journal 無法完整讀取/);
  const evidence = readEvidence(artifactsDir);
  assert.equal(evidence.finalStatus, 'failed', '讀取失敗不能留下 finalStatus=passed');
  assert.equal(evidence.bodyStatus, 'passed', '分開保存 test body 原始結果');
  assert.equal(evidence.steps.find(s => s.label === 'final-status')?.data, 'failed');
  const journals = journalsOf(evidence);
  assert.equal(journals.audit_jsonl.status, 'read_error', 'EISDIR 必須被記為 read_error，不是 missing 或 null');
  assert.ok(journals.audit_jsonl.error && journals.audit_jsonl.error.length > 0, 'read_error 必須保留具體錯誤訊息');
});

check('正例：EISDIR 但 finalStatus=failed 時不 throw——失敗流程允許保存部分證據，但仍要如實記錄 read_error（不是 null）', () => {
  const artifactsDir = tmpDir();
  const workspaceDir = tmpDir();
  const wb = path.join(workspaceDir, '.workbench');
  fs.mkdirSync(wb, { recursive: true });
  fs.writeFileSync(path.join(wb, 'gate.jsonl'), '{"op_id":"op-1","at":"t","records":[]}\n');
  fs.writeFileSync(path.join(wb, 'events.jsonl'), '');
  fs.mkdirSync(path.join(wb, 'audit.jsonl'));

  const rec = new GateEvidenceRecorder('stale', 'run-eisdir-failed');
  assert.doesNotThrow(() => rec.flush(artifactsDir, workspaceDir, 'failed'));
  const evidence = readEvidence(artifactsDir);
  const journals = journalsOf(evidence);
  assert.equal(journals.audit_jsonl.status, 'read_error');
  assert.ok(journals.audit_jsonl.error);
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exitCode = 1;
