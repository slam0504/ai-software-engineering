// B3a-1 smoke flow（§2.4）：規格分頁 → glossary.md → 全選輸入完整預期字串
// （含本次執行的 run-id 標記）→ 儲存 → 核對磁碟**完整內容相等**。
//
// 刻意不以「儲存鈕變 disabled」當完成訊號（PoC 已知的判定缺陷，見設計
// §1「疑似競態」）；改用 expect.poll 直接讀磁碟核對完整內容。
import { execFileSync } from 'node:child_process';
import { expect, test } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
import { waitForCliReady } from './support/cliInfo.js';
import { captureChromeArgv } from './support/chromeArgv.js';
import { readRunEnv, userFlags } from './support/env.js';
import { getInMemoryGuardState, installNetworkGuard, violationsLogPath } from './support/networkGuard.js';

// env 必須在 test／hook 的 callback 內才讀（不放模組頂層）：globalSetup 在
// worker 匯入這支 spec 檔「之前」就已完成、env 已備妥，但 `playwright test
// --list` 這類只解析不執行的模式不會跑 globalSetup，模組頂層讀取會在那種
// 模式下先炸掉。

test.afterEach(async ({}, testInfo) => {
  if (testInfo.status !== testInfo.expectedStatus) {
    const env = readRunEnv();
    fs.writeFileSync(`${env.artifactsDir}/TEST_FAILED`, `${testInfo.title}: ${testInfo.status}\n`);
  }
});

test('spec tab → glossary.md → edit → save → disk content matches exactly', async ({ page }) => {
  const env = readRunEnv();
  // browser 層網路判定必須在任何 page.goto 之前對整個 context 安裝。
  await installNetworkGuard(page.context(), env.artifactsDir);
  if (userFlags.injectN14aWriteFailure()) { // N14a 注入點（缺口 A）：讓證據檔寫入失敗，驗證不會被靜默吞掉
    fs.chmodSync(violationsLogPath(env.artifactsDir), 0o444);
  }
  await page.goto(env.baseUrl);
  captureChromeArgv(env.artifactsDir); // reviewer 要求：Chrome 真正帶了哪些旗標，以這份 ps 擷取的完整命令列為準

  const info = await waitForCliReady(page, 60_000);
  expect(info.workspace, 'CLIInfo().workspace 應等於 fixture realpath').toBe(env.workspaceDir);
  expect(info.workspaceSource, 'workspaceSource 應為 env').toBe('env');
  expect(info.toolsDir, 'CLIInfo().toolsDir 應等於假 tools 目錄').toBe(env.toolsDir);
  expect(info.toolsSource, 'toolsSource 應為 env').toBe('env');
  expect(info.claudeVersion, '假 claude 版本應等於本次執行專屬字串').toBe(env.claudeVersion);
  expect(info.codexVersion, '假 codex 版本應等於本次執行專屬字串').toBe(env.codexVersion);
  expect(info.startupError, 'startupError 應為空').toBe('');

  if (userFlags.injectBadCliCall()) { // N1 注入點：假 CLI 被以非 --version 參數呼叫
    const claudeBin = path.join(env.toolsDir, 'claude-cli', 'node_modules', '.bin', 'claude');
    try {
      execFileSync(claudeBin, ['--bogus-arg']); // 假 CLI 對非 --version 呼叫會以非零碼結束，這裡預期擲出
    } catch {
      // 預期會失敗（假 CLI 以碼 17 結束）；重點是這筆呼叫已經寫進 invocations.log，
      // 收尾的 tripwire 判定應該抓到它。
    }
  }
  if (userFlags.injectDeleteInvocationsLog()) { // N2 注入點：刪除 invocations.log
    fs.rmSync(path.join(env.toolsDir, 'invocations.log'), { force: true });
  }
  if (userFlags.injectN9bFetch()) { // N9b 注入點：頁面對保留測試網域發出 fetch，應被 network guard 擋下
    await page.evaluate(() => fetch('http://example.invalid/').catch(() => undefined));
  }
  if (userFlags.injectN9bWebSocket()) { // N9b 注入點：頁面對保留測試網域開 WebSocket，應被 network guard 擋下
    await page.evaluate(() => new Promise<void>(resolve => {
      const ws = new WebSocket('ws://test.example/');
      const done = () => resolve();
      ws.addEventListener('error', done);
      ws.addEventListener('close', done);
      setTimeout(done, 1500);
    }));
  }
  if (userFlags.injectN14aWriteFailure()) { // N14a 注入點：製造一筆違規，逼 appendViolation 在證據檔唯讀的狀態下寫入
    await page.evaluate(() => fetch('http://example.invalid/').catch(() => undefined));
  }

  await page.locator('[data-test="tab-spec"]').click();

  const fileBtn = page.locator('[data-test="file-tree-spec/glossary.md"]');
  await fileBtn.waitFor({ state: 'visible', timeout: 15_000 });
  await fileBtn.click();

  const editorHost = page.locator('[data-test="editor-host"]');
  await editorHost.waitFor({ state: 'visible', timeout: 15_000 });
  const cmContent = editorHost.locator('.cm-content');
  await cmContent.waitFor({ state: 'visible', timeout: 15_000 });

  const marker = `b3a1-e2e-${env.runId}`;
  const expectedContent = `# Glossary (${marker})\n\n- term: definition\n`;

  await cmContent.click();
  await page.keyboard.press('ControlOrMeta+A');
  await page.keyboard.insertText(expectedContent);

  const saveBtn = page.locator('[data-test="save"]');
  await saveBtn.waitFor({ state: 'visible', timeout: 15_000 });
  await saveBtn.click();

  await expect
    .poll(() => fs.readFileSync(env.glossaryPath, 'utf8'), {
      message: '磁碟上的 glossary.md 應在儲存完成後與編輯器內容完全相等',
      timeout: 10_000,
    })
    .toBe(expectedContent);

  await expect(page.locator('[data-test="save-error"]'), '不應出現 save-error').toHaveCount(0);
  await expect(page.locator('[data-test="external-abort"]'), '不應出現 external-abort').toHaveCount(0);

  // 缺口 A：network guard 的違規／寫入失敗要在**這個 worker 行程內**直接反映
  // 到測試結果——globalTeardown 跑在另一個行程，只能看跨行程的證據檔，「檔案
  // 存在但因為寫入失敗而維持空白」這種情況單靠那一層看不出來（N14a 專門驗證
  // 這一點）。
  const guardState = getInMemoryGuardState(env.artifactsDir);
  expect(guardState.writeFailures, 'network guard 證據寫入不應該失敗（缺口 A／N14a）').toEqual([]);
  expect(guardState.violations, 'network guard 不應該攔到任何違規（in-memory 記錄）').toEqual([]);

  if (userFlags.injectN14bDeleteEvidence()) { // N14b 注入點：判定前刪除證據檔，globalTeardown 讀不到不能當成沒有違規
    fs.rmSync(violationsLogPath(env.artifactsDir), { force: true });
  }
  // 注意：network-samples.log 的刪除改到 NetworkSampler.stop()（見該檔案），
  // 在 spec 這裡刪會跟取樣器自己的下一輪 tick 競態（tick 會用
  // appendFileSync 把檔案重新生出來），這裡不重複刪除。
  if (userFlags.injectDeleteRunStateForTeardown()) { // R3 定點反證：run-state.json 判定前被刪除，observationFailures 讀取失敗不能當成空陣列
    fs.rmSync(path.join(env.artifactsDir, 'run-state.json'), { force: true });
  }
});
