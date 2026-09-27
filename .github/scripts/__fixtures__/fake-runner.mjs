#!/usr/bin/env node
// fake-runner.mjs — 僅供 run-batch.selftest.mjs 透過 CI_E2E_TEST_RUNNER 使用
// 的假子程序，取代真實的 `node frontend/e2e/scripts/run-e2e.mjs`。完全不碰
// run-e2e.mjs／globalSetup／App／browser；自己決定要不要在
// CI_E2E_ARTIFACTS_ROOT 底下建立 run 目錄、寫什麼內容、以什麼 rc 結束。
//
// 行為由環境變數 FAKE_RUNNER_BEHAVIOR（JSON，key 是 entry id）決定，讓同一
// 支腳本能在一次批次裡對不同 entry 表現不同行為（例如第一案成功、第二案
// 失敗，驗證 stop-on-first-failure）。
//
// 支援的 behavior 值：
//   'pass'      建立一個完整合法的 run 目錄（含 run-state/harness.log/
//               playwright-results.json/8 個固定必要檔），exit 0。
//   'fail-rc'   不建立任何 run 目錄，exit 3（模擬子行程真的失敗、且沒有
//               harness 產出）。
//   'no-evidence-pass' exit 0 但不建立任何 run 目錄（wrapper 會判定
//               claimed-success 卻缺證據，不是 NO-RUN，是矛盾）。
//   'duplicate' 建立兩個 run 目錄，exit 0（identity 異常反例）。
//   'ignore-term' 忽略 SIGTERM、跑很久（用來測 wrapper 逾時串接進
//               run-batch 的行為，需搭配小 deadline/grace）。
import { cpSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { entryById } from '../ci-e2e-entries.mjs';
import { makeGoodRunDir, suiteWithOneTest, controlsSuites } from './evidence-builder.mjs';

const __dirname = path.dirname(fileURLToPath(import.meta.url));

const entryId = process.argv[2];
const artifactsRoot = process.env.CI_E2E_ARTIFACTS_ROOT;
const behaviorMap = JSON.parse(process.env.FAKE_RUNNER_BEHAVIOR ?? '{}');
const behavior = behaviorMap[entryId] ?? 'pass';
const entry = entryById(entryId);

// entry.expectedSpecFiles 是「相對於 frontend/e2e 的路徑」（例如
// 'gates/gate1.spec.ts'）；真實 Playwright JSON reporter 的 spec.file 不含
// 這個前綴，前綴其實來自 config.rootDir 的 basename（見
// evaluate-e2e-evidence.mjs 的 reconstructFrontendRelativePath()）。這裡反
// 過來拆解：dirname 當 rootDir 的最後一段，basename 當 spec.file，確保
// fake-runner 產生的證據跟真實格式同一套結構，不是另外發明的假格式。
function splitExpectedSpecPath(expectedSpecFile) {
  const dir = path.dirname(expectedSpecFile); // '.' 代表沒有子目錄（default）
  const base = path.basename(expectedSpecFile);
  const rootDir = dir === '.' ? '/synthetic/frontend/e2e' : `/synthetic/frontend/e2e/${dir}`;
  return { rootDir, bareFile: base };
}

// review round 3 修法：先前這裡自己複製一份「必要檔清單＋playwright-results
// 骨架」的邏輯，round 3 新增的 run-env.json／execution-entry.json／領域證
// 據檔（gate-evidence.json 等）又要再複製一次——改成直接呼叫
// evidence-builder.mjs 的 makeGoodRunDir()（唯一權威來源），避免兩處各自
//维護容易漂移（round 2 結束時就因為兩處字段不同步，被 run-env.json 的
// 「先寫真實內容、後被泛用佔位迴圈覆寫」這個 bug 咬到一次，見
// evidence-builder.mjs 同一輪的修法說明）。
function writeGoodRunDir(runId) {
  // 依實際 entry 的預期 spec 集合組出「剛好符合」的 playwright-results.json
  // （controls 用真正的 control A/B/reverse-check 結構，其餘用單一 spec／
  // 單一 test）——不是隨便寫一支 glossary.spec.ts 就想矇混過 evaluator 的
  // identity／oracle 核對，這樣 'pass' 這個 behavior 才是真正代表「這個
  // entry 的證據完全符合預期」，'duplicate'／'no-evidence-pass' 等其他
  // behavior 的對照組才有意義。
  const { rootDir, bareFile } = splitExpectedSpecPath(entry.expectedSpecFiles[0]);
  const suites = entry.id === 'controls'
    ? controlsSuites()
    : [suiteWithOneTest(bareFile, 'suite', 'fake test')];
  makeGoodRunDir(artifactsRoot, runId, {
    entry,
    rootDir: entry.id === 'controls' ? '/synthetic/frontend/e2e/controls' : rootDir,
    specs: suites,
  });
}

switch (behavior) {
  case 'pass':
    writeGoodRunDir(`fake-run-${entryId}-${Date.now()}`);
    process.exit(0);
    break;
  case 'fail-rc':
    process.exit(3);
    break;
  case 'no-evidence-pass':
    process.exit(0);
    break;
  case 'duplicate':
    writeGoodRunDir(`fake-run-${entryId}-a-${Date.now()}`);
    writeGoodRunDir(`fake-run-${entryId}-b-${Date.now()}`);
    process.exit(0);
    break;
  case 'pass-then-test-failed': {
    // review round 3（R1）：對照 reviewer #480 的
    // `runner-with-test-failed.mjs`——正常建立一份合法 run 目錄（含所有必
    // 要證據），但額外補一個 TEST_FAILED（模擬「test body 實際失敗，但
    // 其餘證據都寫得很完整」的情境）。用來驗證 run-batch 全鏈路是否真的會
    // 因為 TEST_FAILED 而讓這個 entry 判定失敗、批次停止——不是只在
    // evaluator 單獨呼叫時才抓得到。
    const runId = `fake-run-${entryId}-${Date.now()}`;
    writeGoodRunDir(runId);
    writeFileSync(path.join(artifactsRoot, runId, 'TEST_FAILED'), 'late evidence persistence failure\n');
    process.exit(0);
    break;
  }
  case 'ignore-term': {
    process.on('SIGTERM', () => {});
    setTimeout(() => process.exit(0), 3000);
    break;
  }
  case 'real-gate2-failed': {
    // review round 2：用真實的 gate2 首跑失敗樣本
    // （__fixtures__/real/gate2-failed-20260925T071019Z-146267，harness.log
    // 最終結果：FAILED、overallFailed=true）驗證「失敗由 run-batch 的
    // wrapperSpawnRc 判定，evaluator 對『非成功宣稱但仍有證據』的案例不深
    // 度核對」這個既有設計——不是 evaluator 沒抓到問題，是這種情境本來就
    // 不歸 evaluator 的成功宣稱檢查管，見 evaluate-e2e-evidence.mjs 最後一
    // 個 else 分支的註解。
    const realFixtureDir = path.join(__dirname, 'real', 'gate2-failed-20260925T071019Z-146267');
    const runDir = path.join(artifactsRoot, `fake-run-${entryId}-real-failed-${Date.now()}`);
    cpSync(realFixtureDir, runDir, { recursive: true });
    process.exit(1); // 真實那次 run 的 harness 判定為 FAILED，child 對應非零 exit
    break;
  }
  default:
    process.stderr.write(`fake-runner.mjs: 未知 behavior "${behavior}"\n`);
    process.exit(9);
}
