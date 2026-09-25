// B3a-2a：review #4（#430）必修缺陷 2 的離線負控制——重現 reviewer
// `hook.spec.mjs` 的情境：test body 本身沒有任何斷言失敗（Playwright 會
// 判定 testInfo.status==='passed'），但 evidence flush 因為 artifactsDir
// 已存在 `gate-evidence.json`（單一寫入者保護，見 gateEvidence.ts）而
// throw。
//
// 修正前（三支 spec 舊版邏輯：先看 testInfo.status 再呼叫 flush，見
// gateAfterEachHook.ts 開頭說明）：body 當下還是 passed，`TEST_FAILED`
// 標記從未被寫入，即使 Playwright 本身把這次 run 判定為 failed。
// 修正後（`runGateAfterEach`：先寫「未完成」標記，body 與 flush 都成功
// 才移除）：標記會留在磁碟上，讓共用 `global-teardown.ts`（只用
// `fs.existsSync(TEST_FAILED)` 判定，不看內容）抓到這次失敗。
//
// 刻意不用 page fixture、不 import global-setup／global-teardown、不起
// App／browser——完全離線，只驗證 `runGateAfterEach` 與
// `GateEvidenceRecorder` 的互動。
// `GATE_HOOK_NEGCTRL_ARTIFACTS_DIR`／`GATE_HOOK_NEGCTRL_WORKSPACE_DIR` 由
// 呼叫端（`gateAfterEachHook.selftest.ts`）指定並在執行後讀取結果。
import { test } from '@playwright/test';
import fs from 'node:fs';
import { GateEvidenceRecorder } from './gateEvidence.js';
import { runGateAfterEach } from './gateAfterEachHook.js';

const artifactsDir = process.env.GATE_HOOK_NEGCTRL_ARTIFACTS_DIR;
const workspaceDir = process.env.GATE_HOOK_NEGCTRL_WORKSPACE_DIR;
if (!artifactsDir || !workspaceDir) {
  throw new Error(
    'gateAfterEachHookNegativeControl.spec.ts: 必須設定 GATE_HOOK_NEGCTRL_ARTIFACTS_DIR／'
    + 'GATE_HOOK_NEGCTRL_WORKSPACE_DIR（呼叫端 gateAfterEachHook.selftest.ts 負責提供，避免落在 worktree）',
  );
}

let recorder: GateEvidenceRecorder | undefined;

test.afterEach(async ({}, testInfo) => {
  // 跟 gate1.spec.ts／gate2.spec.ts／stale.spec.ts 使用同一個共用 helper
  // ——這個負控制驗證的正是這個呼叫路徑本身，不是另外複刻一份邏輯。
  runGateAfterEach({ artifactsDir, workspaceDir }, testInfo.title, testInfo.status, testInfo.expectedStatus, recorder);
  recorder = undefined;
});

test('offline hook evidence failure：body 通過但 flush 因既有 evidence 檔拒寫', async () => {
  fs.mkdirSync(artifactsDir, { recursive: true });
  // 製造 flush() 的單一寫入者保護觸發（gateEvidence.ts flush()）：預先放
  // 一份既有 gate-evidence.json，flush() 會因此 throw，不吞掉、原樣往外拋。
  fs.writeFileSync(`${artifactsDir}/gate-evidence.json`, 'existing evidence (negative control)');
  recorder = new GateEvidenceRecorder('gate1', 'offline-hook-negctrl');
  // body 本身不做任何斷言——單純通過，讓 Playwright 判定
  // testInfo.status==='passed'，直到 afterEach 的 flush 才會失敗。
});
