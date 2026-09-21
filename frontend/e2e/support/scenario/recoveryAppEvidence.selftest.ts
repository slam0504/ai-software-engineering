// recoveryAppEvidence.ts 的共用判定測試（B3a-2b-2 Task E2 補正）。
//
// codexSessionRecovery.spec.ts 與本檔呼叫同一組匯出函式
// （judgeAppWireRecovery／judgeGenerationUniqueness／
// judgeConfigOnDiskAgainstBuilder），不各寫一份判定邏輯——這裡只是換一種
// 輸入來源（落地檔案／合成資料）驅動同一批函式。
//
// 「真實成功資料」用的是本次 E2 補正驗證時實際留下的第一份成功 run
// （20260921T113808Z-c12056，KEEP 事先開啟、reviewer 裁定不得覆寫）的原始
// app-wire-log.jsonl／scenario-config.json／manifest——不是另外手刻的合成
// 資料，用意是讓「正控制」直接對到真實 App 產生的原始位元組，不是只對自己
// 手寫的假資料自圓其說。
//
// 執行：node frontend/e2e/support/scenario/recoveryAppEvidence.selftest.ts
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { judgeAppWireRecovery, judgeConfigOnDiskAgainstBuilder, judgeGenerationUniqueness } from './recoveryAppEvidence.ts';
import type { AppWireRow } from './recoveryAppEvidence.ts';
import { resolveScenario } from './scenarios.ts';
import { parseManifest } from './verify.ts';
import type { GenerationCandidate } from './wireEvidence.ts';
import type { RecoveryExpectation } from './recoveryJudge.ts';

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

// ---------------------------------------------------------------------------
// 真實成功資料：第一份成功 run 的原始證據（只讀，不覆寫）
// ---------------------------------------------------------------------------
const here = path.dirname(fileURLToPath(import.meta.url));
// reviewer #325 缺陷 3：先前直接讀 `.artifacts/20260921T113808Z-c12056`——那是
// gitignored 的本機 run 產物，而本檔又掛進 `selftest:all`，於是**乾淨 checkout
// 必然 rc=1**（reviewer 複製 source 到沒有 artifacts 的目錄實測過）。改讀受版控
// 的最小 fixture；provenance（來源 run-id／原始檔 hash／有無轉換）見該目錄的
// `PROVENANCE.json`。**不用「缺檔就 skip」掩蓋。**
const fixtureDir = path.join(here, '..', '..', 'fixtures', 'recovery-app-wire');

interface RunEnvShape {
  runId: string;
  scenario: string;
  scenarioThreadId: string;
  scenarioTurnId: string;
  scenarioItemId: string;
  scenarioApprovalMethod: string;
  scenarioApprovalRequestId: string;
}

if (!fs.existsSync(fixtureDir)) {
  console.error(`FAIL - 前置條件：找不到受版控的 recovery App wire fixture（${fixtureDir}）`);
  process.exit(1);
}
interface ProvenanceShape { sourceRunId: string; expectedDomWsid: string; transformed: boolean }
const provenance = JSON.parse(fs.readFileSync(path.join(fixtureDir, 'PROVENANCE.json'), 'utf8')) as ProvenanceShape;

// 期望的 thread／turn／item／approval 一律由**受版控 builder ＋ fixture 記錄的
// runId** 產生，不從待驗資料回填。
const env = { runId: provenance.sourceRunId, scenario: 'commandExecution-recovery' } as unknown as RunEnvShape;
const cfg = resolveScenario(env.scenario).build(env.runId);
const decision = resolveScenario(env.scenario).decision;
if (!cfg.secondTurn) {
  console.error('FAIL - 前置條件：本次 scenario builder 缺少 secondTurn，與第一份成功 run 的既有事實不符');
  process.exit(1);
}
const secondTurn = cfg.secondTurn;

const realAppWireRows: AppWireRow[] = fs.readFileSync(path.join(fixtureDir, 'app-wire-log.jsonl'), 'utf8')
  .split('\n').map(l => l.trim()).filter(l => l.length > 0)
  .map(l => JSON.parse(l) as AppWireRow);

// reviewer #325：expected WSID 必須來自**獨立的 fixture metadata**，
// **不得從待驗 row 反推出自身期望**（先前是取第一列非空 wsid，等於用待驗資料
// 產生自己的期望，錯 WSID 的反例因此形同虛設）。
const realDomWsid = provenance.expectedDomWsid;
assert.ok(realDomWsid && realDomWsid.length > 0, '前置條件：PROVENANCE.json 必須提供非空的 expectedDomWsid');

const recoveryExpectation: RecoveryExpectation = {
  threadId: cfg.threadId,
  approvalMethod: cfg.approvalMethod,
  round1: {
    turnId: cfg.turnId,
    itemId: cfg.itemId,
    approvalRequestId: cfg.approvalRequestId,
    decision: 'accept',
    afterApproval: cfg.afterApproval,
    turnStatus: cfg.turnStatus,
  },
  round2: {
    turnId: secondTurn.turnId,
    itemId: secondTurn.itemId,
    approvalRequestId: secondTurn.approvalRequestId,
    decision: 'accept',
    afterApproval: secondTurn.afterApproval,
    turnStatus: secondTurn.turnStatus,
  },
};

// ---- A：judgeAppWireRecovery ----------------------------------------------

check('judgeAppWireRecovery 正控制：第一份成功 run 的真實 App wire 錄流＋真實 domWsid 應無違規', () => {
  const violations = judgeAppWireRecovery(realAppWireRows, { domWsid: realDomWsid, recovery: recoveryExpectation });
  assert.deepEqual(violations, []);
});

check('judgeAppWireRecovery 反例：所有 row.wsid 改成 ANOTHER-WSID（reviewer 重現案 a）必須判失敗', () => {
  const tampered = realAppWireRows.map(r => ({ ...r, wsid: 'ANOTHER-WSID' }));
  const violations = judgeAppWireRecovery(tampered, { domWsid: realDomWsid, recovery: recoveryExpectation });
  assert.ok(violations.length > 0, '應有違規');
  assert.ok(violations.some(v => v.includes('ANOTHER-WSID') && v.includes(realDomWsid)), `violations 應點名 wsid 不符，實際：${JSON.stringify(violations)}`);
});

check('judgeAppWireRecovery 反例：截斷到只剩 4 個 approval frame（reviewer 重現案 b）必須判失敗', () => {
  const truncated = realAppWireRows.filter(r => {
    const id = (r.raw as { id?: unknown }).id;
    return id === cfg.approvalRequestId || id === secondTurn.approvalRequestId;
  });
  assert.equal(truncated.length, 4, `前置條件：截斷後應剩 4 個 frame，實際 ${truncated.length}`);
  const violations = judgeAppWireRecovery(truncated, { domWsid: realDomWsid, recovery: recoveryExpectation });
  assert.ok(violations.length > 0, '應有違規（截斷不能被 judgeRecoverySequence 當成合法完整序列）');
});

check('judgeAppWireRecovery 反例：frame 順序被打亂（不得用排序掩蓋錯序）必須判失敗', () => {
  const shuffled = [...realAppWireRows].reverse();
  const violations = judgeAppWireRecovery(shuffled, { domWsid: realDomWsid, recovery: recoveryExpectation });
  assert.ok(violations.length > 0, '應有違規');
  assert.ok(violations.some(v => v.includes('frame 順序錯亂')), `violations 應點名順序錯亂，實際：${JSON.stringify(violations)}`);
});

check('judgeAppWireRecovery 正控制：只有握手三筆可以空 wsid，其後全部非空且等於 domWsid', () => {
  // reviewer #325 更正：先前這條寫成「wsid 全空允許」，前提本身就是被推翻的
  // 那一個（空字串不得當成握手辨識依據）。改成確認真實資料的實際形狀——
  // 前三筆（initialize / initialize response / initialized）wsid 為空，
  // 第 4 筆起全部非空且等於 domWsid。
  const violations = judgeAppWireRecovery(realAppWireRows, { domWsid: realDomWsid, recovery: recoveryExpectation });
  assert.deepEqual(violations, []);
  assert.deepEqual(realAppWireRows.slice(0, 3).map(r => r.wsid), ['', '', ''], '前三筆握手應為空 wsid');
  const rest = realAppWireRows.slice(3);
  assert.ok(rest.length > 0, '前置條件：握手之後應有 session frames');
  assert.ok(rest.every(r => r.wsid === realDomWsid), '握手之後每一筆都必須等於 domWsid');
});

// reviewer #325 新增反例：WSID 判定改成依握手「位置」而非空字串之後，
// 下面三種先前會漏掉的情況都必須被擋。
check('judgeAppWireRecovery 反例（#325-1a）：所有 row.wsid 清空必須判失敗（空字串不得當成握手辨識依據）', () => {
  const tampered = realAppWireRows.map(r => ({ ...r, wsid: '' }));
  const violations = judgeAppWireRecovery(tampered, { domWsid: realDomWsid, recovery: recoveryExpectation });
  assert.ok(violations.length > 0, `全部 wsid 清空必須判失敗，實際 violations=${JSON.stringify(violations)}`);
  assert.ok(violations.some(v => v.includes('空字串')), `violation 應指出空字串問題：${JSON.stringify(violations)}`);
});

check('judgeAppWireRecovery 反例（#325-1b）：只清空第二輪的 wsid 必須判失敗', () => {
  const half = Math.floor(realAppWireRows.length / 2);
  const tampered = realAppWireRows.map((r, i) => (i >= half ? { ...r, wsid: '' } : r));
  const violations = judgeAppWireRecovery(tampered, { domWsid: realDomWsid, recovery: recoveryExpectation });
  assert.ok(violations.length > 0, `只清空第二輪 wsid 必須判失敗，實際 violations=${JSON.stringify(violations)}`);
});

check('judgeAppWireRecovery 反例（#325-1c）：domWsid 本身為空必須判失敗', () => {
  const violations = judgeAppWireRecovery(realAppWireRows, { domWsid: '', recovery: recoveryExpectation });
  assert.ok(violations.some(v => v.includes('domWsid')), `domWsid 空必須判失敗：${JSON.stringify(violations)}`);
});

check('judgeAppWireRecovery 反例（#325-2）：initialize request 與 response 的 id 改成 null 必須判失敗', () => {
  const tampered = realAppWireRows.map((r, i) => (i <= 1 ? { ...r, raw: { ...r.raw, id: null } } : r));
  const violations = judgeAppWireRecovery(tampered, { domWsid: realDomWsid, recovery: recoveryExpectation });
  assert.ok(violations.length > 0, `null id 必須判失敗，實際 violations=${JSON.stringify(violations)}`);
  assert.ok(violations.some(v => v.includes('基本型別')), `應由 adapter 的型別核對擋下：${JSON.stringify(violations)}`);
});

// ---- B：judgeGenerationUniqueness ------------------------------------------

function fakeCandidate(file: string, mtimeMs: number): GenerationCandidate {
  return { file, jsonlPath: `/fake/${file}`, metaPath: `/fake/${file.replace(/\.jsonl$/, '.meta.json')}`, mtimeMs };
}

check('judgeGenerationUniqueness 正控制：恰好一筆候選時無違規', () => {
  const violations = judgeGenerationUniqueness([fakeCandidate('gen-a.jsonl', 1000)]);
  assert.deepEqual(violations, []);
});

check('judgeGenerationUniqueness 反例：零候選必須判失敗', () => {
  const violations = judgeGenerationUniqueness([]);
  assert.ok(violations.length > 0);
});

check('judgeGenerationUniqueness 反例：多個候選 generation 必須判失敗，且保留每個候選的歧義診斷（不得依 mtime 猜最新）', () => {
  const candidates = [fakeCandidate('gen-newer.jsonl', 2000), fakeCandidate('gen-older.jsonl', 1000)];
  const violations = judgeGenerationUniqueness(candidates);
  assert.ok(violations.length > 0, '應有違規');
  assert.ok(violations.some(v => v.includes('gen-newer.jsonl') && v.includes('gen-older.jsonl')), `violations 應同時列出兩個候選檔名，實際：${JSON.stringify(violations)}`);
  assert.ok(!violations.some(v => /選最新|挑.*最新/.test(v)), 'violations 不應暗示「已經挑了最新的那個」——多候選必須直接判失敗，不是警告後仍選一個');
});

// ---- C：judgeConfigOnDiskAgainstBuilder ------------------------------------

const realManifest = parseManifest(path.join(fixtureDir, 'scenario-wire.log.manifest.json'));
const realScenarioConfigOnDisk: unknown = JSON.parse(fs.readFileSync(path.join(fixtureDir, 'scenario-config.json'), 'utf8'));

check('judgeConfigOnDiskAgainstBuilder 正控制：第一份成功 run 落地的 scenario-config.json（含 secondTurn）對上 builder 應無違規', () => {
  const violations = judgeConfigOnDiskAgainstBuilder(realManifest, realScenarioConfigOnDisk, { runId: env.runId, cfg, decision });
  assert.deepEqual(violations, []);
});

check('judgeConfigOnDiskAgainstBuilder 反例：落地 config 的 secondTurn.turnId 錯配 builder 必須判失敗', () => {
  const tampered = { ...(realScenarioConfigOnDisk as Record<string, unknown>) };
  tampered.secondTurn = { ...(tampered.secondTurn as Record<string, unknown>), turnId: 'wrong-turn-id' };
  const violations = judgeConfigOnDiskAgainstBuilder(realManifest, tampered, { runId: env.runId, cfg, decision });
  assert.ok(violations.length > 0, '應有違規');
  assert.ok(violations.some(v => v.includes('secondTurn')), `violations 應點名 secondTurn 不符，實際：${JSON.stringify(violations)}`);
});

check('judgeConfigOnDiskAgainstBuilder 反例：落地 config 整段缺少 secondTurn 必須判失敗（不能被 env 欄位核對頂替）', () => {
  const tampered = { ...(realScenarioConfigOnDisk as Record<string, unknown>) };
  delete tampered.secondTurn;
  const violations = judgeConfigOnDiskAgainstBuilder(realManifest, tampered, { runId: env.runId, cfg, decision });
  assert.ok(violations.length > 0, '應有違規');
  assert.ok(violations.some(v => v.includes('secondTurn')), `violations 應點名 secondTurn 不符，實際：${JSON.stringify(violations)}`);
});

check('judgeConfigOnDiskAgainstBuilder 反例：round1 欄位錯配（沿用凍結的 judgeRunIdentity）仍要判失敗', () => {
  const tampered = { ...(realScenarioConfigOnDisk as Record<string, unknown>), threadId: 'wrong-thread-id' };
  const violations = judgeConfigOnDiskAgainstBuilder(realManifest, tampered, { runId: env.runId, cfg, decision });
  assert.ok(violations.length > 0, '應有違規');
  assert.ok(violations.some(v => v.includes('threadId')), `violations 應點名 threadId 不符，實際：${JSON.stringify(violations)}`);
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exit(1);
