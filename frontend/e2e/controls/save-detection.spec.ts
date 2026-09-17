// §2.5 存檔判定的受控對照（設計 rev3）。
//
// 目的：證明新判定（expect.poll 核對磁碟完整內容）能區分「寫入尚未完成」與
// 「已完成」，舊判定（等儲存鈕變 disabled）不能。**不能據此宣稱已證實歷史那次
// 失敗的根因**（設計 §2.5 明文）。
//
// A、B 各自 fresh page、各自開始前重設目標檔內容，避免互相污染。
import { expect, test } from '@playwright/test';
import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import { waitForCliReady } from '../support/cliInfo.js';
import { collectWrapperEvidence, installSpecWriteDelay } from '../support/delayWrapper.js';
import { readRunEnv, userFlags } from '../support/env.js';
import { getInMemoryGuardState, installNetworkGuard, violationsLogPath } from '../support/networkGuard.js';

const BASELINE = '# Glossary\n\n- term: definition\n';
const DELAY_MS = 2000;

function resetGlossary(glossaryPath: string): void {
  fs.writeFileSync(glossaryPath, BASELINE);
}

function sha256(content: string): string {
  return crypto.createHash('sha256').update(content, 'utf8').digest('hex');
}

async function openGlossaryAndType(page: import('@playwright/test').Page, newContent: string): Promise<void> {
  await page.locator('[data-test="tab-spec"]').click();
  const fileBtn = page.locator('[data-test="file-tree-spec/glossary.md"]');
  await fileBtn.waitFor({ state: 'visible', timeout: 15_000 });
  await fileBtn.click();

  const editorHost = page.locator('[data-test="editor-host"]');
  await editorHost.waitFor({ state: 'visible', timeout: 15_000 });
  const cmContent = editorHost.locator('.cm-content');
  await cmContent.waitFor({ state: 'visible', timeout: 15_000 });

  await cmContent.click();
  await page.keyboard.press('ControlOrMeta+A');
  await page.keyboard.insertText(newContent);
}

function writeEvidence(artifactsDir: string, name: string, data: unknown): void {
  fs.writeFileSync(path.join(artifactsDir, name), JSON.stringify(data, null, 2));
}

test.afterEach(async ({}, testInfo) => {
  if (testInfo.status !== testInfo.expectedStatus) {
    const env = readRunEnv();
    fs.appendFileSync(`${env.artifactsDir}/TEST_FAILED`, `${testInfo.title}: ${testInfo.status} (expected ${testInfo.expectedStatus})\n`);
  }
});

test('control A: old judgment (save button disabled) must fail on the target assertion', async ({ page }) => {
  const env = readRunEnv();
  resetGlossary(env.glossaryPath);

  // browser 層網路判定必須在任何 page.goto 之前對整個 context 安裝——這裡跟
  // glossary.spec.ts 同一套要求（實測撞到過：漏裝會讓證據檔不存在，
  // globalTeardown 判定 missing evidence 一律算失敗，見缺口 A）。
  await installNetworkGuard(page.context(), env.artifactsDir);
  if (userFlags.injectN14aWriteFailure()) { // R3 定點反證（controls 場景）：讓證據檔唯讀，逼 appendViolation 寫入失敗
    fs.chmodSync(violationsLogPath(env.artifactsDir), 0o444);
  }
  await page.goto(env.baseUrl);
  await waitForCliReady(page, 60_000);
  if (userFlags.injectN14aWriteFailure()) { // 製造一筆違規，逼 appendViolation 在證據檔唯讀狀態下寫入
    await page.evaluate(() => fetch('http://example.invalid/').catch(() => undefined));
  }
  await installSpecWriteDelay(page, DELAY_MS, '__wrapperEvidenceA');

  const marker = `control-a-${env.runId}`;
  const newContent = `# Glossary (${marker})\n\n- term: definition\n`;
  await openGlossaryAndType(page, newContent);

  const saveBtn = page.locator('[data-test="save"]');
  await saveBtn.click();
  // 舊判定：等儲存鈕變 disabled。busyReason 一進入 'save' 這顆鈕就已經是
  // disabled（見設計 §1「疑似競態」），所以這個等待幾乎立刻成立——遠早於
  // wrapper 注入的 2000ms 延遲與真正的寫入完成。
  await expect(saveBtn).toBeDisabled({ timeout: 5_000 });

  // 立即讀一次磁碟（舊判定的做法）——這一刻 wrapper 的延遲還沒過完，寫入
  // 尚未真的發生，讀到的必然是舊內容。
  const diskContentAtOldSignal = fs.readFileSync(env.glossaryPath, 'utf8');
  const diskSha = sha256(diskContentAtOldSignal);
  const baselineSha = sha256(BASELINE);

  // wrapper 的呼叫紀錄是在延遲（2000ms）過後、真正呼叫原函式**之前**才寫入
  // window[evidenceKey]（見 delayWrapper.ts），所以要等它出現才讀得到——
  // 不能在上面「立即讀磁碟」之後馬上讀，那時候 setTimeout 還沒觸發。
  await expect
    .poll(async () => (await collectWrapperEvidence(page, '__wrapperEvidenceA')).length, {
      message: 'wrapper 的呼叫紀錄應該在延遲過後出現',
      timeout: 5_000,
    })
    .toBe(1);
  const wrapperEvidence = await collectWrapperEvidence(page, '__wrapperEvidenceA');

  writeEvidence(env.artifactsDir, 'control-a-evidence.json', {
    diskContentAtOldSignal,
    diskSha256: diskSha,
    baselineSha256: baselineSha,
    expectedNewContent: newContent,
    wrapperEvidence,
  });

  // 這兩個斷言驗證「舊判定讀到的正是延遲注入前的舊內容」與「wrapper 確實
  // 生效且延遲 >= 2000ms」——都應該通過，不受下面 test.fail() 影響（呼叫在
  // 這兩個斷言之後）。
  expect(diskSha, '對照 A 在舊訊號當下讀到的磁碟內容，sha256 應等於延遲注入前的舊內容').toBe(baselineSha);
  expect(wrapperEvidence.length, 'wrapper 應該被呼叫過恰好一次').toBe(1);
  expect(wrapperEvidence[0].actualDelayMs, 'wrapper 實際延遲應 >= 2000ms').toBeGreaterThanOrEqual(DELAY_MS);

  // R3 修正（reviewer 三次審查，2026-09-16）：network guard 的檢查**必須**
  // 放在 `test.fail()` 之前——`test.fail()` 會讓這整個測試「預期失敗」，
  // 呼叫之後任何斷言失敗（含這裡的 guard 檢查）都會被吸收成「符合預期」，
  // 等於讓違規或證據寫入失敗被 test.fail() 的語意吞掉、完全不會被抓到。
  // 放在這裡（所有真正的頁面操作都做完、但 test.fail() 還沒呼叫）才能保證
  // guard 故障是「非預期失敗」，不會被吸收。
  const guardStateA = getInMemoryGuardState(env.artifactsDir);
  expect(guardStateA.writeFailures, 'network guard 證據寫入不應該失敗（對照 A，缺口 A 同型問題）').toEqual([]);
  expect(guardStateA.violations, 'network guard 不應該攔到任何違規（對照 A）').toEqual([]);

  // ---- test.fail() 必須緊接目標存檔斷言之前才呼叫 ----
  test.fail();
  expect(diskContentAtOldSignal, '對照 A（舊判定）預期失敗：讀到的應是舊內容而非新內容').toBe(newContent);
});

test('control B: new judgment (expect.poll full content) succeeds', async ({ page }) => {
  const env = readRunEnv();
  resetGlossary(env.glossaryPath);

  // browser 層網路判定必須在任何 page.goto 之前對整個 context 安裝——這裡跟
  // glossary.spec.ts 同一套要求（實測撞到過：漏裝會讓證據檔不存在，
  // globalTeardown 判定 missing evidence 一律算失敗，見缺口 A）。
  await installNetworkGuard(page.context(), env.artifactsDir);
  if (userFlags.injectN14aWriteFailure()) { // R3 定點反證（controls 場景）：同一輪內證據檔已經唯讀，這裡再觸發一次違規，證明對照 B 自己也抓得到
    await page.goto(env.baseUrl);
    await waitForCliReady(page, 60_000);
    await page.evaluate(() => fetch('http://example.invalid/').catch(() => undefined));
  } else {
    await page.goto(env.baseUrl);
    await waitForCliReady(page, 60_000);
  }
  await installSpecWriteDelay(page, DELAY_MS, '__wrapperEvidenceB');

  const marker = `control-b-${env.runId}`;
  const newContent = `# Glossary (${marker})\n\n- term: definition\n`;
  await openGlossaryAndType(page, newContent);

  const saveBtn = page.locator('[data-test="save"]');
  await saveBtn.click();

  await expect
    .poll(() => fs.readFileSync(env.glossaryPath, 'utf8'), {
      message: '對照 B（新判定）應能等到寫入真正完成、磁碟內容與編輯器內容完全相等',
      timeout: 10_000,
    })
    .toBe(newContent);

  const wrapperEvidence = await collectWrapperEvidence(page, '__wrapperEvidenceB');
  writeEvidence(env.artifactsDir, 'control-b-evidence.json', {
    finalDiskContent: fs.readFileSync(env.glossaryPath, 'utf8'),
    expectedNewContent: newContent,
    wrapperEvidence,
  });

  expect(wrapperEvidence.length, 'wrapper 應該被呼叫過恰好一次').toBe(1);
  expect(wrapperEvidence[0].actualDelayMs, 'wrapper 實際延遲應 >= 2000ms').toBeGreaterThanOrEqual(DELAY_MS);

  // R3 修正：對照 B 沒有 test.fail()，理論上不會被吸收，但一樣補上檢查以
  // 保持跟 glossary.spec.ts／對照 A 一致的防護。
  const guardStateB = getInMemoryGuardState(env.artifactsDir);
  expect(guardStateB.writeFailures, 'network guard 證據寫入不應該失敗（對照 B）').toEqual([]);
  expect(guardStateB.violations, 'network guard 不應該攔到任何違規（對照 B）').toEqual([]);
});
