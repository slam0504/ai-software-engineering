// §2.5 反證：證明 test.fail() 不會吸收「目標存檔斷言之前」的 setup／locator
// 失敗——這種失敗必須照常紅燈，不能因為測試裡有呼叫 test.fail() 就被吞掉。
//
// 預設跳過（不影響 `npm run test:e2e:controls` 平常的綠燈）；只在
// E2E_CONTROLS_REVERSE_CHECK=1 時執行，且**預期整個 controls suite 會紅**——
// 這是刻意的，用來證明「失敗不會被吸收」，不是要交付一個常態通過的測試。
import { expect, test } from '@playwright/test';
import fs from 'node:fs';
import { waitForCliReady } from '../support/cliInfo.js';
import { readRunEnv, userFlags } from '../support/env.js';
import { getInMemoryGuardState, installNetworkGuard, violationsLogPath } from '../support/networkGuard.js';

test.afterEach(async ({}, testInfo) => {
  if (testInfo.status !== testInfo.expectedStatus) {
    const env = readRunEnv();
    fs.appendFileSync(`${env.artifactsDir}/TEST_FAILED`, `${testInfo.title}: ${testInfo.status} (expected ${testInfo.expectedStatus})\n`);
  }
});

test('reverse-check: broken locator before test.fail() must NOT be absorbed', async ({ page }) => {
  test.skip(process.env.E2E_CONTROLS_REVERSE_CHECK !== '1', '只在 E2E_CONTROLS_REVERSE_CHECK=1 時執行（反證，預期紅燈）');

  const env = readRunEnv();
  await installNetworkGuard(page.context(), env.artifactsDir);
  if (userFlags.injectN14aWriteFailure()) { // R3 定點反證（controls 場景）：讓證據檔唯讀，逼 appendViolation 寫入失敗
    fs.chmodSync(violationsLogPath(env.artifactsDir), 0o444);
  }
  await page.goto(env.baseUrl);
  await waitForCliReady(page, 60_000);
  if (userFlags.injectN14aWriteFailure()) {
    // 製造一筆違規並在這裡（比壞掉的 locator 早）就檢查一次——正常情況下
    // reverse-check 會在下面的 locator 那行先逾時失敗，guard 檢查永遠執行
    // 不到；這裡刻意提前檢查一次，才能證明「reverse-check 也不會漏掉 guard
    // 故障」，不是只靠正常流程「剛好走不到」來掩蓋沒驗證到的事實。
    await page.evaluate(() => fetch('http://example.invalid/').catch(() => undefined));
    const guardStateEarly = getInMemoryGuardState(env.artifactsDir);
    expect(guardStateEarly.writeFailures, 'network guard 證據寫入不應該失敗（reverse-check，注入點提前檢查）').toEqual([]);
    expect(guardStateEarly.violations, 'network guard 不應該攔到任何違規（reverse-check，注入點提前檢查）').toEqual([]);
  }

  // 故意用一個不存在的 data-test：這一步應該在還沒呼叫 test.fail() 之前就
  // 逾時失敗，證明它不會被之後才會出現的 test.fail() 吸收。
  await page.locator('[data-test="this-data-test-does-not-exist"]').click({ timeout: 3_000 });

  // 不應該執行到這裡；如果真的執行到，代表上面那行沒有如預期失敗。
  // R3 修正：guard 檢查一樣要放在 test.fail() 之前，理由同 save-detection.spec.ts
  // 的對照 A（雖然正常情況下這裡執行不到，但如果 reverse-check 本身出問題、
  // 真的走到這裡，guard 故障不該被 test.fail() 的語意吞掉）。
  const guardState = getInMemoryGuardState(env.artifactsDir);
  expect(guardState.writeFailures, 'network guard 證據寫入不應該失敗（reverse-check）').toEqual([]);
  expect(guardState.violations, 'network guard 不應該攔到任何違規（reverse-check）').toEqual([]);
  test.fail();
  expect(true, '不應該執行到這裡——上面的 locator 應該已經逾時失敗').toBe(false);
});
