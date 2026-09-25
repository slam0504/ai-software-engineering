// B3a-2a Gate 1 flow（design v4 §7.1 步驟 1、§8 G1-P1／G1-N1、§11.1；
// review #3（#427）修正版）。
//
// 時間軸（硬性順序，design v4 §11.1）：
//   1. G1-N1：spec/ dirty（save 未 commit）時點 submit-for-approval → 失敗，
//      無新 gate_request，錯誤文字須含 spec.BuildCommittedSnapshot 的原始
//      dirty-tree 原因（"scoped tree dirty"，app.go:3932-3941 附近呼叫
//      鏈：submitForApproval → spec.BuildCommittedSnapshot）。必須排在 C1
//      commit 之前，否則沒有 dirty tree 可測。
//   2. preview-commit → confirm-commit（提交同一份編輯內容）→ HEAD=C1。
//   3. G1-P1：submit-for-approval → approve → badge-active，並用受控
//      fixture 獨立重算 spec_manifest／base_commit 與 journal 的
//      gate_request.bindings 逐一比對（review #427 point 5：不能只驗
//      「有核可」，要驗證核可的 binding 值本身正確）。
//
// review #427 point 4 修正：`await expect.poll(fn).not.toBeNull()` 的回傳
// 值是 matcher 呼叫的結果（`undefined`），不是 fn() 的解析值——舊版把它
// 指派給 approvalId 導致後續全部用 `entry-undefined` 選取。改成先用
// `expect.poll` 純粹當「等待條件成立」的斷言，再另外呼叫同一個 predicate
// 取得實際值並驗證非 null。
//
// review #427 point 1 修正：改用 journalReader.ts 的
// `latestGateRequestApprovalId`／`latestApprovalRecordFor`（正式
// op_id/at/records 欄位名稱），不再手刻 JSON.parse 迴圈。
//
// **待 browser run 驗證的項目**（本輪不啟動 App／browser，見任務書禁止事
// 項）：實際 timeout／CodeMirror 互動時序、下方所有 selector 是否與真實
// DOM 相符、debounce／等待收斂的實際秒數。這支 spec 目前只完成離線
// typecheck，未曾在真實 browser 執行過。
import { expect, test } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
import { readRunEnv } from '../support/env.js';
import { installNetworkGuard } from '../support/networkGuard.js';
import { waitForCliReady } from '../support/cliInfo.js';
import { verifyGateFlowDescriptor } from '../support/gates/gateFlowDescriptor.js';
import { headSha, readScopedEntriesAtCommit, specInScope } from '../support/gates/gate2Fixture.js';
import { specManifestDigest } from '../support/gates/canonicalDigest.js';
import {
  bindingsOfGateRequest, latestApprovalRecordFor, latestGateRequestApprovalId, readNewRecordsByType,
} from '../support/gates/journalReader.js';
import { GateEvidenceRecorder } from '../support/gates/gateEvidence.js';
import { runGateAfterEach } from '../support/gates/gateAfterEachHook.js';

// review #427 必修缺陷 6：gate.jsonl／audit.jsonl／events.jsonl 活在
// `workspaceDir/.workbench/`，會被共用 global-teardown.ts:364-366 的
// `fs.rmSync(env.workspaceDir, ...)` 整個刪掉（不論成功或失敗、不論
// E2E_KEEP_ARTIFACTS）。test 內建立 recorder、afterEach 無條件 flush
// （teardown 之前執行，fixture 還沒被刪）。
let recorder: GateEvidenceRecorder | undefined;

test.afterEach(async ({}, testInfo) => {
  const env = readRunEnv();
  // review #4（#430）必修缺陷 2：共用 helper——先寫「未完成」標記，body
  // 與 evidence flush 都成功才移除；evidence 錯誤原樣往外拋。
  runGateAfterEach(env, testInfo.title, testInfo.status, testInfo.expectedStatus, recorder);
  recorder = undefined;
});

test('Gate 1：dirty tree 送核被拒（G1-N1），兩階段 commit 後送核核可（G1-P1）', async ({ page }) => {
  const env = readRunEnv();
  verifyGateFlowDescriptor(env, 'gate1');
  const root = env.workspaceDir;
  recorder = new GateEvidenceRecorder('gate1', env.runId);

  await installNetworkGuard(page.context(), env.artifactsDir);
  await page.goto(env.baseUrl);
  const info = await waitForCliReady(page, 60_000);
  expect(info.startupError, 'startupError 應為空').toBe('');

  const gatePath = path.join(root, '.workbench', 'gate.jsonl');

  await page.locator('[data-test="tab-spec"]').click();

  // 新增 spec/features/checkout.feature，內容含一個可被 plan 引用的 scenario。
  const newFilePath = page.locator('[data-test="new-file-path"]');
  await newFilePath.fill('spec/features/checkout.feature');
  await page.locator('[data-test="new-file-submit"]').click();

  const editorHost = page.locator('[data-test="editor-host"]');
  await editorHost.waitFor({ state: 'visible', timeout: 15_000 });
  const cmContent = editorHost.locator('.cm-content');
  await cmContent.waitFor({ state: 'visible', timeout: 15_000 });

  const featureContent = 'Feature: checkout\n\n  @scenario-ok\n  Scenario: scenario-ok\n'
    + '    Given a precondition\n    When an action happens\n    Then an outcome is observed\n';
  await cmContent.click();
  await page.keyboard.press('ControlOrMeta+A');
  await page.keyboard.insertText(featureContent);

  await page.locator('[data-test="save"]').click();
  await expect
    .poll(() => {
      try {
        return fs.readFileSync(path.join(root, 'spec', 'features', 'checkout.feature'), 'utf8');
      } catch {
        return '';
      }
    }, {
      message: '磁碟內容應在儲存後與編輯器一致（尚未 commit，仍是 dirty tree）',
      timeout: 10_000,
    })
    .toBe(featureContent);

  // ---- G1-N1：dirty tree 送核，必須被拒（核對具體原因），且無新 gate_request ----
  const lenBeforeN1 = fs.existsSync(gatePath) ? fs.statSync(gatePath).size : 0;

  await page.locator('[data-test="submit-for-approval"]').click();
  const submitError = page.locator('.spec-workspace .approval + p.err');
  await expect(submitError, 'dirty tree 送核應顯示錯誤（G1-N1）').toBeVisible({ timeout: 10_000 });
  await expect(submitError, 'G1-N1 錯誤應含 spec.BuildCommittedSnapshot 的 dirty-tree 原因').toContainText('scoped tree dirty');

  expect(readNewRecordsByType(gatePath, lenBeforeN1, 'gate_request'), 'G1-N1 不應產生任何新的 gate_request').toEqual([]);
  recorder?.record('G1-N1', { errorText: await submitError.textContent(), newGateRequests: readNewRecordsByType(gatePath, lenBeforeN1, 'gate_request') });

  // ---- 兩階段 commit：preview-commit → confirm-commit ----
  await page.locator('[data-test="preview-commit"]').click();
  const commitDiff = page.locator('[data-test="commit-diff"]');
  await expect(commitDiff, 'preview-commit 後應顯示 diff').toBeVisible({ timeout: 10_000 });

  await page.locator('[data-test="commit-message"]').fill(`b3a-2a gate1 e2e: add checkout.feature (${env.runId})`);
  await page.locator('[data-test="confirm-commit"]').click();
  await expect(commitDiff, 'confirm-commit 後 diff 應清空').toHaveCount(0, { timeout: 10_000 });

  // confirm-commit 是 production 自己的 git commit（ConfirmSpecCommit binding），
  // harness 讀出實際產生的 commit SHA，供下面獨立重算 spec_manifest／base_commit 用。
  const c1 = headSha(root);
  recorder?.record('C1', { sha: c1 });

  // ---- G1-P1：commit 後送核 → GateConsole 出現 pending → approve → badge-active ----
  await page.locator('[data-test="submit-for-approval"]').click();
  const submitResultOk = page.locator('.spec-workspace .approval .ok');
  await expect(submitResultOk, 'submit-for-approval 成功後應顯示 submittedApprovalId').toBeVisible({ timeout: 15_000 });

  // review #427 point 4 修正：expect.poll 只當「等待條件成立」的斷言，
  // 實際值另外用同一個 predicate 讀出並驗證非 null，不從 matcher 回傳值取值。
  await expect
    .poll(() => latestGateRequestApprovalId(gatePath, 'gate1'), { message: 'gate.jsonl 應出現一筆 gate1 的 gate_request', timeout: 15_000 })
    .not.toBeNull();
  const approvalId = latestGateRequestApprovalId(gatePath, 'gate1');
  expect(approvalId, 'approvalId 必須是實際讀出的字串，不是 matcher 的回傳值').not.toBeNull();
  if (approvalId === null) throw new Error('unreachable: asserted not null above');

  const entry = page.locator(`[data-test="entry-${approvalId}"]`);
  await entry.waitFor({ state: 'visible', timeout: 15_000 });
  // approve 動作限定在該 entry 內點擊，不是頁面上第一個 approve 按鈕
  // （review #427 point 4：同一原則——不得依賴「目前只有一筆」的偶然巧合）。
  await entry.locator('[data-test="approve"]').click();

  const badge = page.locator(`[data-test="badge-${approvalId}"]`);
  await expect(badge, 'approve 後 badge 應轉為 badge-active').toHaveClass(/badge-active/, { timeout: 15_000 });

  // journal 權威核對：approval_record 應存在且 decision=approved（不只信任 UI class）。
  await expect
    .poll(() => latestApprovalRecordFor(gatePath, approvalId)?.decision ?? null, {
      message: 'gate.jsonl 應出現該 approval_id 的 approval_record{decision:approved}',
      timeout: 15_000,
    })
    .toBe('approved');

  // ---- review #427 point 5：獨立重算 spec_manifest／base_commit，逐一比對 journal bindings ----
  const expectedSpecManifest = specManifestDigest(readScopedEntriesAtCommit(root, c1, specInScope));
  const expectedBaseCommit = `git:sha1:${c1}`;
  const actualBindings = bindingsOfGateRequest(gatePath, approvalId);
  expect(actualBindings.spec_manifest, 'gate_request.bindings 的 spec_manifest 應等於獨立重算值').toBe(expectedSpecManifest);
  expect(actualBindings.base_commit, 'gate_request.bindings 的 base_commit 應等於獨立重算值').toBe(expectedBaseCommit);

  // approval_record 本身也應帶有相同的 bindings（不是只有 gate_request 對）。
  const approvalRecord = latestApprovalRecordFor(gatePath, approvalId);
  const approvalBindings = ((approvalRecord?.bindings as Array<{ kind: string; digest: string }> | undefined) ?? [])
    .reduce<Record<string, string>>((acc, b) => { acc[b.kind] = b.digest; return acc; }, {});
  expect(approvalBindings.spec_manifest, 'approval_record.bindings 的 spec_manifest 應等於獨立重算值').toBe(expectedSpecManifest);
  expect(approvalBindings.base_commit, 'approval_record.bindings 的 base_commit 應等於獨立重算值').toBe(expectedBaseCommit);

  // Gate1 的 approval_record.metadata 因 omitempty 應整個省略（design v4 §0 精確性修正）。
  expect('metadata' in (approvalRecord ?? {}), 'Gate1 的 approval_record 不應出現 metadata 鍵').toBe(false);

  recorder?.record('G1-P1-bindings', {
    approvalId, expected: { spec_manifest: expectedSpecManifest, base_commit: expectedBaseCommit },
    actualGateRequest: actualBindings, actualApprovalRecord: approvalBindings,
  });
});
