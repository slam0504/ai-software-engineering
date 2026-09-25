// gateAfterEachHook.ts 的離線 Playwright 負控制——review #4（#430）必修
// 缺陷 2：body 通過但 evidence flush 失敗時，`TEST_FAILED` 標記必須留在
// 磁碟上（共用 `global-teardown.ts` 只用 `fs.existsSync` 判定，見該檔
// 372-373 行）。
//
// 跟其餘 `*.selftest.ts` 不同：這裡需要真實 Playwright test runner 判定
// `testInfo.status`／`expectedStatus` 的互動（afterEach 拋出例外後
// Playwright 本身如何回報 rc／test 結果），純 Node 呼叫函式無法覆蓋這段
// 行為，因此改用 child_process 執行
// `node_modules/.bin/playwright test`（跟 `e2e/scripts/run-e2e.mjs`
// spawn playwright 子行程的既有慣例一致），對象是
// `gateAfterEachHookNegativeControl.spec.ts`／`.config.ts`，跑完後檢查
// 產出的 `TEST_FAILED` 標記與 rc。
//
// 每次執行都用全新的暫存目錄（不重用上一次殘留）。outputDir／artifacts
// 全部導到 `GATE_HOOK_NEGCTRL_OUTPUT_DIR` 底下（未設定時退回
// `os.tmpdir()`），不落在 worktree。
//
// 執行：node e2e/support/gates/gateAfterEachHook.selftest.ts
// （可選：GATE_HOOK_NEGCTRL_OUTPUT_DIR=/tmp/... node e2e/support/gates/gateAfterEachHook.selftest.ts）
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
// e2e/support/gates/ -> frontend/
const frontendRoot = path.resolve(__dirname, '..', '..', '..');

let passed = 0;
let failed = 0;
function check(name: string, ok: boolean, detail?: string): void {
  if (ok) {
    passed += 1;
    console.log(`ok - ${name}`);
  } else {
    failed += 1;
    console.error(`FAIL - ${name}${detail ? `：${detail}` : ''}`);
  }
}

const baseOutputDir = process.env.GATE_HOOK_NEGCTRL_OUTPUT_DIR
  ?? fs.mkdtempSync(path.join(os.tmpdir(), 'gate-hook-negctrl-'));
fs.mkdirSync(baseOutputDir, { recursive: true });

const runDir = fs.mkdtempSync(path.join(baseOutputDir, 'run-'));
const artifactsDir = path.join(runDir, 'artifacts');
const workspaceDir = path.join(runDir, 'workspace');
const pwOutputDir = path.join(runDir, 'pw-output');
fs.mkdirSync(artifactsDir, { recursive: true });
fs.mkdirSync(workspaceDir, { recursive: true });
fs.mkdirSync(pwOutputDir, { recursive: true });

const configPath = path.join(__dirname, 'gateAfterEachHookNegativeControl.config.ts');
const playwrightBin = path.join(frontendRoot, 'node_modules', '.bin', 'playwright');

console.log(`spawn: ${playwrightBin} test --config ${configPath}`);
const result = spawnSync(playwrightBin, ['test', '--config', configPath], {
  cwd: frontendRoot,
  env: {
    ...process.env,
    GATE_HOOK_NEGCTRL_ARTIFACTS_DIR: artifactsDir,
    GATE_HOOK_NEGCTRL_WORKSPACE_DIR: workspaceDir,
    GATE_HOOK_NEGCTRL_OUTPUT_DIR: pwOutputDir,
  },
  encoding: 'utf8',
});

const logPath = path.join(runDir, 'playwright-run.log');
fs.writeFileSync(
  logPath,
  `--- cmd ---\n${playwrightBin} test --config ${configPath}\n`
  + `--- stdout ---\n${result.stdout ?? ''}\n--- stderr ---\n${result.stderr ?? ''}\n--- status ---\n${String(result.status)}\n`,
);
console.log(`(playwright 執行 log：${logPath}）`);

check(
  'Playwright 執行本身因 afterEach flush 失敗而回報非 0 rc（body 通過，flush 才失敗）',
  result.status !== 0,
  `實際 status=${String(result.status)}`,
);

const markerPath = path.join(artifactsDir, 'TEST_FAILED');
const markerExists = fs.existsSync(markerPath);
check(
  '修正後：TEST_FAILED 標記必須留在磁碟上（即使 body 當下是 passed，flush 失敗也要讓共用 teardown 判定為失敗——'
  + '這是 review #4（#430）必修缺陷 2 的核心斷言）',
  markerExists,
);
if (markerExists) {
  console.log(`  TEST_FAILED 內容：${JSON.stringify(fs.readFileSync(markerPath, 'utf8'))}`);
}

const evidencePath = path.join(artifactsDir, 'gate-evidence.json');
const evidencePreserved = fs.existsSync(evidencePath) && fs.readFileSync(evidencePath, 'utf8').includes('existing evidence');
check('gate-evidence.json 保留呼叫端預先放置的既有內容（flush 因單一寫入者保護拒寫，不覆寫）', evidencePreserved);

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exitCode = 1;
