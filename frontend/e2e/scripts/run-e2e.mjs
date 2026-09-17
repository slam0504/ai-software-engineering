#!/usr/bin/env node
// B3a-1 Browser E2E 進入點。負責在 `playwright test` 啟動「之前」決定本次
// 執行的 run-id 與證據目錄路徑，並以環境變數方式往下游（globalSetup、
// spec、playwright.config.ts）傳遞——playwright.config.ts 的 `outputDir`
// 在載入當下（globalSetup 執行之前）就要讀得到 E2E_ARTIFACTS_DIR，這個值
// 必須在 spawn playwright 子行程之前就準備好，故獨立成這支 wrapper。
//
// 用法：node e2e/scripts/run-e2e.mjs <path-to-playwright-config-relative-to-frontend>
// P2 修正（reviewer 複核 #34 第八次，2026-09-16）：`E2E_OFFLINE_SANDBOX=1`
// 但前置條件不滿足時，原本的失敗發生在 playwright.config.ts 載入當下——
// 那個時間點 globalSetup 根本還沒執行，不會有任何證據目錄或 harness.log，
// 只有 stderr 上的例外訊息，不符合設計 §2.2「啟動前失敗也要留下 log」的
// 要求。這裡在 spawn playwright **之前**，用跟 `offlineSandbox.ts` 相同的
// 四項檢查（純 JS 版本，重複一份而不是 import `.ts`——這支腳本刻意維持
// 不依賴任何 TS 檔案，才能在 Playwright 自己的 esbuild loader 接手之前，
// 用最陽春的方式先跑；兩邊路徑常數要保持同步，改一邊記得改另一邊）先驗證
// 一次，不滿足就直接把原因寫進本次 run-id 對應的 harness.log、非零結束，
// **不 spawn playwright**（這個階段本來就不該有 trace，也不為了取得 log
// 硬啟動 app）。**支援邊界**：這只涵蓋透過這支腳本（也就是
// `npm run test:e2e`／`test:e2e:controls`）的入口；如果直接呼叫
// `playwright test --config ...`，一樣會在 config 載入時失敗，但不會有這裡
// 產生的早期 harness.log——那個入口目前沒有涵蓋，不宣稱已涵蓋所有入口。
'use strict';
import { spawn } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const frontendRoot = path.resolve(__dirname, '..', '..');

const configArg = process.argv[2];
if (!configArg) {
  console.error('usage: run-e2e.mjs <playwright-config-path>');
  process.exit(2);
}

function runId() {
  const ts = new Date().toISOString().replace(/[-:]/g, '').replace(/\.\d+Z$/, 'Z');
  const rand = randomBytes(3).toString('hex');
  return `${ts}-${rand}`;
}

const id = runId();
const artifactsDir = path.join(frontendRoot, 'e2e', '.artifacts', id);

const env = {
  ...process.env,
  E2E_RUN_ID: id,
  E2E_ARTIFACTS_DIR: artifactsDir,
};

// 跟 offlineSandbox.ts 的 SANDBOX_EXEC_PATH／SYSTEM_CHROME_PATH／
// sandboxProfilePath()／chromeWrapperPath() 保持同步的純 JS 版本。
function offlineSandboxPrecheckPaths() {
  return {
    profile: path.join(__dirname, '..', 'support', 'sandboxProfiles', 'loopback-only.sb'),
    wrapper: path.join(__dirname, '..', 'support', 'sandboxProfiles', 'chromeWrapper.sh'),
    sandboxExec: '/usr/bin/sandbox-exec',
    chrome: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  };
}

function checkReadableFile(p, label) {
  let stat;
  try {
    stat = fs.statSync(p);
  } catch (e) {
    return `找不到${label}：${p}（${String(e)}）`;
  }
  if (!stat.isFile()) return `${label}不是一般檔案：${p}`;
  try {
    fs.accessSync(p, fs.constants.R_OK);
  } catch (e) {
    return `${label}不可讀：${p}（${String(e)}）`;
  }
  return null;
}

function checkExecutableFile(p, label) {
  let stat;
  try {
    stat = fs.statSync(p);
  } catch (e) {
    return `找不到${label}：${p}（${String(e)}）`;
  }
  if (!stat.isFile()) return `${label}不是一般檔案：${p}`;
  try {
    fs.accessSync(p, fs.constants.X_OK);
  } catch (e) {
    return `${label}不可執行：${p}（${String(e)}）`;
  }
  return null;
}

function writeEarlyHarnessLog(message) {
  fs.mkdirSync(artifactsDir, { recursive: true });
  const line = `[${new Date().toISOString()}] ${message}\n`;
  fs.appendFileSync(path.join(artifactsDir, 'harness.log'), line);
}

if (env.E2E_OFFLINE_SANDBOX === '1') {
  const useChromium = env.E2E_BROWSER === 'chromium';
  const paths = offlineSandboxPrecheckPaths();
  const reasons = [];
  if (useChromium) {
    reasons.push("E2E_OFFLINE_SANDBOX=1 目前只支援已驗證的系統 Chrome，不支援 E2E_BROWSER=chromium 這個組合，不嘗試執行");
  } else {
    const profileErr = checkReadableFile(paths.profile, 'sandbox profile');
    if (profileErr) reasons.push(profileErr);
    const wrapperErr = checkExecutableFile(paths.wrapper, 'Chrome sandbox wrapper');
    if (wrapperErr) reasons.push(wrapperErr);
    const sandboxExecErr = checkExecutableFile(paths.sandboxExec, 'sandbox-exec');
    if (sandboxExecErr) reasons.push(sandboxExecErr);
    const chromeErr = checkExecutableFile(paths.chrome, '系統 Chrome 執行檔');
    if (chromeErr) reasons.push(chromeErr);
  }
  if (reasons.length > 0) {
    writeEarlyHarnessLog(`啟動前失敗（run-e2e 入口，config 載入與 playwright 啟動之前）：E2E_OFFLINE_SANDBOX 前置條件不滿足，不 spawn playwright，不建立 trace：${reasons.join('；')}`);
    console.error(`E2E_OFFLINE_SANDBOX=1 前置條件不滿足，啟動前失敗：${reasons.join('；')}`);
    console.error(`原因已寫入 ${path.join(artifactsDir, 'harness.log')}`);
    process.exit(1);
  }
  writeEarlyHarnessLog('run-e2e 入口：E2E_OFFLINE_SANDBOX 前置條件檢查通過，繼續 spawn playwright（globalSetup 會接手同一份 harness.log 繼續寫）。');
}

const playwrightBin = path.join(frontendRoot, 'node_modules', '.bin', 'playwright');
const child = spawn(playwrightBin, ['test', '--config', configArg], {
  cwd: frontendRoot,
  env,
  stdio: 'inherit',
});

// 轉發中斷訊號給子行程；子行程（playwright test 行程）內的 globalSetup 會
// 註冊自己的 SIGINT/SIGTERM 處理常式執行停止程序（§2.3）。這裡不重複收尾，
// 只確保訊號真的送得到子行程（同一個前景 process group 時 OS 本來就會一併
// 送達，這裡是防禦性補送，涵蓋被以非互動方式呼叫的情形）。
for (const sig of ['SIGINT', 'SIGTERM']) {
  process.on(sig, () => {
    if (!child.killed) child.kill(sig);
  });
}

child.on('exit', (code, signal) => {
  if (signal) {
    process.kill(process.pid, signal);
    return;
  }
  process.exit(code ?? 1);
});
