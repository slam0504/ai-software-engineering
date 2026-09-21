// scenarios.ts 的純函式測試（B3a-2b-2 Task D：四案 matrix 的負控制）——不需要
// 真跑 browser。重點覆蓋成功條件 §6：
//   (a) 案例／decision 錯配會被既有判定函式（verify.ts 的 judgeApproval、
//       scenarioProtocolJudge.ts 的 judgeRunIdentity，兩者皆非本輪新增，
//       這裡只是證明登記表擴充後兩者仍然抓得到錯配）抓到；
//   (b) 一個 run 只會選到一案——resolveScenario 對明確名稱只回傳該案本身，
//       四案各自獨立、不會被合併／模糊比對成另一案，未知或前綴子字串一律
//       throw、不回退到任何一案。
//
// 執行：npm --prefix frontend run selftest:scenarios-matrix
// （實際指令見 frontend/package.json 的 `selftest:scenarios-matrix`——本檔
// 內部用 `.ts` specifier import 其他 support 模組，裸 `node` 呼叫會
// ERR_MODULE_NOT_FOUND，必須經該 npm script 掛
// `--experimental-loader=./e2e/support/selftestJsToTsLoader.mjs`。）
import assert from 'node:assert/strict';
import type { Manifest } from './protocol.ts';
import { Method } from './protocol.ts';
import { judgeApproval } from './verify.ts';
import { judgeRunIdentity } from './scenarioProtocolJudge.ts';
import { resolveScenario } from './scenarios.ts';

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

const ALL_NAMES = ['commandExecution-allow', 'commandExecution-deny', 'fileChange-allow', 'fileChange-deny'] as const;
const runId = 'run789';

// --- 正控制：四案各自的 name／decision／method 符合登記表期望 ---------------
check('四案登記表：name／decision／approvalMethod 逐案符合期望', () => {
  const expectTable: Record<(typeof ALL_NAMES)[number], { decision: 'accept' | 'decline'; method: string }> = {
    'commandExecution-allow': { decision: 'accept', method: Method.CmdExecRequestApproval },
    'commandExecution-deny': { decision: 'decline', method: Method.CmdExecRequestApproval },
    'fileChange-allow': { decision: 'accept', method: Method.FileChangeRequestApproval },
    'fileChange-deny': { decision: 'decline', method: Method.FileChangeRequestApproval },
  };
  for (const name of ALL_NAMES) {
    const def = resolveScenario(name);
    assert.equal(def.name, name, `${name}: ScenarioDef.name 應等於登記表 key`);
    assert.equal(def.decision, expectTable[name].decision, `${name}: decision 應為 ${expectTable[name].decision}`);
    const cfg = def.build(runId);
    assert.equal(cfg.scenario, name, `${name}: build(runId).scenario 應等於登記表 key`);
    assert.equal(cfg.approvalMethod, expectTable[name].method, `${name}: approvalMethod 應為 ${expectTable[name].method}`);
  }
});

// --- 負控制：未指定／未知一律 throw，不回退到任何一案 -----------------------
check('resolveScenario(undefined) 必須 throw，不回退 default', () => {
  assert.throws(() => resolveScenario(undefined), /E2E_SCENARIO 未設定/);
});

check('resolveScenario("") 必須 throw（空字串視同未設定）', () => {
  assert.throws(() => resolveScenario(''), /E2E_SCENARIO 未設定/);
});

check('resolveScenario 未知 scenario 名稱必須 throw', () => {
  assert.throws(() => resolveScenario('commandExecution-timeout'), /未知 scenario/);
});

// --- F2（B3a-2b-2 Task D 限縮補正）：resolver 不得漏掉 Object.prototype
//     成員——`SCENARIOS[name]` 是一般 object，name 若剛好是原型鏈上的成員
//     名稱（不限 reviewer 原先列的三個，實測 Object.prototype 的成員全部
//     會漏），會從原型鏈拿到值、不 throw，不符合「未知一律在 resolver
//     拒絕」的契約。這裡逐一驗證五個代表性成員都必須 throw。 -----------
for (const protoName of ['toString', 'constructor', '__proto__', 'valueOf', 'hasOwnProperty']) {
  check(`resolveScenario("${protoName}")（Object.prototype 成員）必須 throw，不得從原型鏈取值`, () => {
    assert.throws(() => resolveScenario(protoName), /未知 scenario/, `resolveScenario("${protoName}") 應 throw「未知 scenario」，不應回傳原型鏈上的值`);
  });
}

// --- 負控制：一個 run 只會選到一案（不模糊比對、不合併多案）----------------
check('resolveScenario 對前綴子字串（非完整案名）必須 throw，不模糊比對成完整案名', () => {
  // "commandExecution"、"commandExecution-allow-x" 都不是登記表的完整 key，
  // 必須被當成未知 scenario 拒絕——不能因為字首相符就悄悄選到
  // commandExecution-allow 或 commandExecution-deny 其中一案。
  assert.throws(() => resolveScenario('commandExecution'), /未知 scenario/);
  assert.throws(() => resolveScenario('commandExecution-allow-x'), /未知 scenario/);
});

check('四案各自 resolveScenario 回傳獨立、互不相同的單一案（同一 runId 也不會撞名或互相污染）', () => {
  const scenarios = ALL_NAMES.map(name => resolveScenario(name).build(runId).scenario);
  assert.equal(new Set(scenarios).size, ALL_NAMES.length, `四案 build() 的 scenario 欄位應兩兩不同，實際：${JSON.stringify(scenarios)}`);
  // 除了 scenario／approvalMethod 之外，四案在同一 runId 下的 identity 字串
  // （threadId／turnId／itemId／approvalRequestId）刻意共用同一套產生規則
  // （buildConfig），這是登記表內部的既有設計、不是本次負控制要抓的缺陷；
  // 這裡只驗證「呼叫哪個名字就精準拿到那個名字的 scenario／method」。
  for (const name of ALL_NAMES) {
    const def1 = resolveScenario(name);
    const def2 = resolveScenario(name);
    assert.deepEqual(def1.build(runId), def2.build(runId), `${name}: build() 應為純函式，同 runId 兩次呼叫結果應相同`);
  }
});

// --- 負控制：案例／decision 錯配會被既有判定函式抓到 -----------------------
function goodManifestFor(cfg: ReturnType<ReturnType<typeof resolveScenario>['build']>, decision: 'accept' | 'decline'): Manifest {
  return {
    scenario: cfg.scenario,
    argv: ['app-server'],
    pid: 4242,
    startedAt: '2026-09-21T00:00:00.000Z',
    endedAt: '2026-09-21T00:00:01.000Z',
    exitCode: 0,
    approvalMethod: cfg.approvalMethod,
    approvalRequestId: cfg.approvalRequestId,
    decisionReceived: decision,
    unknownMethodsSeen: [],
    fatalError: null,
  };
}

check('案例錯配（decision）：commandExecution-deny 的實際 manifest 拿 commandExecution-allow 的期望核對，judgeApproval 必須抓到', () => {
  const actualDef = resolveScenario('commandExecution-deny');
  const actualCfg = actualDef.build(runId);
  const manifest = goodManifestFor(actualCfg, actualDef.decision); // 實際 run：decisionReceived='decline'

  const wrongExpectedDef = resolveScenario('commandExecution-allow'); // 錯配：expected decision='accept'
  const wrongExpectedCfg = wrongExpectedDef.build(runId);
  const violations = judgeApproval(manifest, {
    requestId: wrongExpectedCfg.approvalRequestId,
    method: wrongExpectedCfg.approvalMethod,
    decision: wrongExpectedDef.decision,
  });
  assert.ok(violations.length > 0, 'expected violations for case/decision mismatch, got none');
  assert.ok(violations.some(v => v.includes('decisionReceived')), `expected decisionReceived violation, got: ${JSON.stringify(violations)}`);
});

check('案例錯配（method）：fileChange-allow 的實際 manifest 拿 commandExecution-allow 的期望核對，judgeApproval 必須抓到 approvalMethod 不符', () => {
  const actualDef = resolveScenario('fileChange-allow');
  const actualCfg = actualDef.build(runId);
  const manifest = goodManifestFor(actualCfg, actualDef.decision);

  const wrongExpectedDef = resolveScenario('commandExecution-allow'); // 同 decision，但 method 不同案
  const wrongExpectedCfg = wrongExpectedDef.build(runId);
  const violations = judgeApproval(manifest, {
    requestId: wrongExpectedCfg.approvalRequestId,
    method: wrongExpectedCfg.approvalMethod,
    decision: wrongExpectedDef.decision,
  });
  assert.ok(violations.length > 0, 'expected violations for cross-method case mismatch, got none');
  assert.ok(violations.some(v => v.includes('approvalMethod')), `expected approvalMethod violation, got: ${JSON.stringify(violations)}`);
});

check('案例錯配（run identity 交叉核對）：fileChange-deny 的落地 config 拿 fileChange-allow 的期望核對，judgeRunIdentity 必須抓到 scenario／decision 不符', () => {
  const actualDef = resolveScenario('fileChange-deny');
  const actualCfg = actualDef.build(runId);
  const manifest = goodManifestFor(actualCfg, actualDef.decision);

  const wrongExpectedDef = resolveScenario('fileChange-allow');
  const wrongExpectedCfg = wrongExpectedDef.build(runId);
  const violations = judgeRunIdentity(manifest, actualCfg, {
    runId,
    cfg: wrongExpectedCfg,
    decision: wrongExpectedDef.decision,
  });
  assert.ok(violations.length > 0, 'expected identity violations for scenario mismatch, got none');
  assert.ok(violations.some(v => v.includes('manifest.decisionReceived')), `expected manifest.decisionReceived violation, got: ${JSON.stringify(violations)}`);
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exitCode = 1;
