// B3a-2a：三支 gates spec 共用的 `test.afterEach` 邏輯（review #4（#430）
// 必修缺陷 2）。
//
// **背景**：舊版三支 spec 各自在 `afterEach` 裡先檢查
// `testInfo.status !== testInfo.expectedStatus`（此時只反映 test body 本
// 身的結果）寫 `TEST_FAILED` 標記，然後才呼叫 `recorder.flush()`。若
// body 通過但 `flush()` 因既有 evidence 檔案拒寫（例如 review #427 必修
// 缺陷 6 的單一寫入者保護）而 throw，Playwright 會把整個 test 判定為
// failed（reviewer 的 `hook.log` 證實：`1 failed`），但 `TEST_FAILED` 標
// 記從未被寫入——因為那個判斷發生在 flush 之前，當下 body 還是 passed。
// 共用 `global-teardown.ts` 只讀這個標記檔案，因此可能對外印出 PASSED，
// 即使這次 run 的 evidence 其實是壞的。
//
// **修法**：先寫一個「未完成」標記（不論 body 當下狀態），只有在 body
// 本身也通過**且** evidence flush 也成功後才移除；body 本身失敗時把標
// 記內容改回原本慣例的訊息格式。`flush()` 的例外原樣往外拋（不吞），讓
// Playwright 把這次 run 判定為 failed——這時標記已經留在磁碟上，teardown
// 讀得到。
import fs from 'node:fs';
import type { GateEvidenceRecorder } from './gateEvidence.js';

export interface GateAfterEachEnv {
  artifactsDir: string;
  workspaceDir: string;
}

export type PlaywrightTestStatus = 'passed' | 'failed' | 'timedOut' | 'interrupted' | 'skipped' | undefined;

/**
 * 執行共用的 afterEach 邏輯。`recorder` 為 `undefined` 時（test body 在
 * 建立 recorder 之前就已經失敗）只處理標記，不呼叫 flush。
 *
 * 呼叫端寫法：
 * ```ts
 * test.afterEach(async ({}, testInfo) => {
 *   const env = readRunEnv();
 *   runGateAfterEach(env, testInfo.title, testInfo.status, testInfo.expectedStatus, recorder);
 *   recorder = undefined;
 * });
 * ```
 */
export function runGateAfterEach(
  env: GateAfterEachEnv,
  testTitle: string,
  status: PlaywrightTestStatus,
  expectedStatus: PlaywrightTestStatus,
  recorder: GateEvidenceRecorder | undefined,
): void {
  const markerPath = `${env.artifactsDir}/TEST_FAILED`;
  const bodyFailed = status !== expectedStatus;

  // 先寫「未完成」標記——即使 body 目前看起來是 passed，也要假設這次 run
  // 尚未真正完成，直到 evidence flush 也確認成功為止。
  fs.writeFileSync(markerPath, `${testTitle}: pending（body status=${String(status)}，等待 evidence flush 確認）\n`);

  if (recorder) {
    // evidence 錯誤原樣往外拋——不吞掉，不 catch。呼叫端（Playwright 的
    // afterEach 執行器）會把這次 run 判定為 failed，且此時標記還留著。
    recorder.flush(env.artifactsDir, env.workspaceDir, status);
  }

  if (!bodyFailed) {
    fs.rmSync(markerPath, { force: true });
  } else {
    fs.writeFileSync(markerPath, `${testTitle}: ${String(status)}\n`);
  }
}
