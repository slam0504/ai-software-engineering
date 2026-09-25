// B3a-2a Gate 2 flow（design v4 §7.1／§8／§11.2；review #3（#427）修正版）。
//
// 固定順序（design v4 §7.1，reviewer #424 (a) 裁定）：
//   C0 → G2-N1（尚無 active Gate 1）→ C1+Gate1 核可 →
//   G2-N2..N5（各自獨立 planID，先移除上一案候選再寫下一案，全程沒有
//   active Gate2 entry）→ G2-P1（正例，三個合法風險組合併入同一份 plan
//   的三個 task，放最後）。
//
// plan/spec 內容一律由 support/gates/gate2Fixture.ts 的 builder 直接
// fs／git 操作寫入（不透過 UI 編輯器）——UI 只負責 plan-id 輸入／
// submit-gate2／risk 選擇／approve，鏡射 design v4 §7 對「builder 受版控，
// 不從待驗輸出反填期望值」的定性。
//
// review #427 point 2／3 修正：每個 task 補上 `title`（`plan.Validate`
// 硬性要求）；`permissionsRef` 一律寫成相對於 `plan/` 的完整路徑
// （`"permissions/T1.yaml"`），對齊 `app.go` `permissionRefEntries` 的
// `"plan/"+ref` 解析式。
//
// review #427 point 1 修正：全面改用 journalReader.ts 的集中化函式
// （`latestGateRequestApprovalId`／`latestApprovalRecordFor`／
// `readNewRecordsByType`），不再各自手刻 JSON.parse 迴圈。
//
// review #427 point 5 修正：G2-P1 新增（a）risk_decisions 逐欄位比對、
// （b）override_reason 缺填時 approve 按鈕 disabled、（c）tier 選單選項符
// 合 `tierOptions()` 預期、（d）獨立重算五個 bindings 並逐一比對 journal。
//
// **待 browser run 驗證**（本輪不啟動 App／browser）：全部 selector、
// timeout、逐步時序，以及 plan-errors 的確切 DOM 結構是否與下方假設相符。
import { expect, test } from '@playwright/test';
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { readRunEnv } from '../support/env.js';
import { installNetworkGuard } from '../support/networkGuard.js';
import { waitForCliReady } from '../support/cliInfo.js';
import { verifyGateFlowDescriptor } from '../support/gates/gateFlowDescriptor.js';
import {
  commitPaths, permissionEntriesAtCommit, planInScope, readScopedEntriesAtCommit, removePlanDoc,
  specInScope, writeFeatureFile, writePermissionRef, writePlanDoc, writeRiskPolicy, type PlanTaskYaml,
} from '../support/gates/gate2Fixture.js';
import { permissionManifestDigest, planManifestDigest, singleFileDigest, specManifestDigest } from '../support/gates/canonicalDigest.js';
import {
  bindingsOfGateRequest, latestApprovalRecordFor, latestGateRequestApprovalId, readNewRecordsByType,
} from '../support/gates/journalReader.js';
import { GateEvidenceRecorder } from '../support/gates/gateEvidence.js';
import { runGateAfterEach } from '../support/gates/gateAfterEachHook.js';
import { observeSubmitWait } from '../support/gates/gateSubmitWait.js';

// review #427 必修缺陷 6：見 gate1.spec.ts 同一段註解——共用 teardown 會把
// gate.jsonl／audit.jsonl／events.jsonl 連同整個 fixture 一起刪掉，這裡在
// afterEach（teardown 之前）無條件 flush 保存的證據。
let recorder: GateEvidenceRecorder | undefined;

test.afterEach(async ({}, testInfo) => {
  const env = readRunEnv();
  // review #4（#430）必修缺陷 2：共用 helper——先寫「未完成」標記，body
  // 與 evidence flush 都成功才移除；evidence 錯誤原樣往外拋。
  runGateAfterEach(env, testInfo.title, testInfo.status, testInfo.expectedStatus, recorder);
  recorder = undefined;
});

test('Gate 2：G2-N1 → Gate1 核可 → G2-N2..N5 → G2-P1（三個合法風險組合）', async ({ page }) => {
  // review #448 reviewer 裁定：診斷 run 量到 Gate2 送核 native 端約
  // 13.9 秒（29 次依序 git wrapper 呼叫），原本 15 秒的功能等待沒有餘裕。
  // 這是保留餘裕的操作上限（240 秒 test body），避免流程外層先截斷；不是
  // 由單一樣本得到的可靠最壞時間，Gate1 的等待與 test body 不受影響。
  test.setTimeout(240_000);
  const env = readRunEnv();
  verifyGateFlowDescriptor(env, 'gate2');
  const root = env.workspaceDir;
  const gatePath = path.join(root, '.workbench', 'gate.jsonl');
  recorder = new GateEvidenceRecorder('gate2', env.runId);

  await installNetworkGuard(page.context(), env.artifactsDir);
  await page.goto(env.baseUrl);
  const info = await waitForCliReady(page, 60_000);
  expect(info.startupError, 'startupError 應為空').toBe('');

  const planTab = page.getByRole('button', { name: '計畫', exact: true });
  const planIdInput = page.locator('[data-test="plan-id"]');
  const submitGate2 = page.locator('[data-test="submit-gate2"]');
  const planErrors = page.locator('[data-test="plan-errors"] li');

  /**
   * review #2 必修缺陷 5：不能只用 `.filter({hasText}).toHaveCount(1)`——
   * 那只確認「至少有一筆符合」，不排除舊案例留下的其他錯誤字串同時存在。
   * 改成先斷言 `plan-errors` 總筆數恰好 1，再核對那唯一一筆的文字。
   */
  async function expectSinglePlanError(substring: string, label: string): Promise<void> {
    await expect(planErrors, `${label}：plan-errors 應恰好一筆（不得殘留前一案的錯誤）`).toHaveCount(1, { timeout: 10_000 });
    // review #4（#430）必修缺陷 5（P2）：textContent() 要在 toContainText
    // 成功「之後」才讀——readCode 風險：先讀 textContent 再斷言的話，若
    // 文字在兩個動作之間才完成渲染，會把舊文字存進 recorder（跟斷言比對
    // 的其實不是同一份快照）。改成斷言先過，再讀一次當下內容存證。
    await expect(planErrors.first(), `${label}：唯一一筆錯誤應含「${substring}」`).toContainText(substring);
    const text = await planErrors.first().textContent();
    recorder?.record(label, { expectedSubstring: substring, actualText: text });
  }

  function gateJournalLen(): number {
    return fs.existsSync(gatePath) ? fs.statSync(gatePath).size : 0;
  }

  // ---- G2-N1：尚無 active Gate 1 ----
  await planTab.click();
  const lenBeforeN1 = gateJournalLen();
  await planIdInput.fill('gate2-n1-no-gate1');
  await submitGate2.click();
  await expectSinglePlanError('no active Gate 1 approval', 'G2-N1');
  expect(readNewRecordsByType(gatePath, lenBeforeN1, 'gate_request'), 'G2-N1 不應產生任何新的 gate_request').toEqual([]);
  expect(readNewRecordsByType(gatePath, lenBeforeN1, 'approval_record'), 'G2-N1 不應產生任何新的 approval_record').toEqual([]);

  // ---- C1 + Gate 1 核可 ----
  writeFeatureFile(root, 'checkout', 'scenario-ok');
  const c1 = commitPaths(root, ['spec/'], `gate2 e2e: add checkout.feature (${env.runId})`);
  recorder?.record('C1', { sha: c1 });

  await page.locator('[data-test="tab-spec"]').click();
  await page.locator('[data-test="submit-for-approval"]').click();
  await expect
    .poll(() => latestGateRequestApprovalId(gatePath, 'gate1'), { message: '應出現 gate1 的 gate_request', timeout: 15_000 })
    .not.toBeNull();
  const gate1ApprovalId = latestGateRequestApprovalId(gatePath, 'gate1');
  expect(gate1ApprovalId).not.toBeNull();
  if (gate1ApprovalId === null) throw new Error('unreachable');
  await page.locator(`[data-test="entry-${gate1ApprovalId}"]`).locator('[data-test="approve"]').click();
  await expect(page.locator(`[data-test="badge-${gate1ApprovalId}"]`), 'Gate 1 應轉為 active').toHaveClass(/badge-active/, { timeout: 15_000 });

  await planTab.click();

  // ---- G2-N2：plan_id 與檔名不符 ----
  {
    writeRiskPolicy(root, { defaultTier: 'medium' });
    writePlanDoc(root, 'gate2-n2', [
      { id: 'T1', title: 'Task T1', scenarios: ['scenario-ok'], minimumRiskTier: 'medium', plannerRiskTier: 'medium', permissionsRef: 'permissions/T1.yaml' },
    ], c1);
    // 刻意讓內容的 plan_id 與檔名不符（仍是合法 YAML、plan.Parse 仍可解析出非空 PlanID）。
    const p = path.join(root, 'plan', 'gate2-n2.yaml');
    fs.writeFileSync(p, fs.readFileSync(p, 'utf8').replace('plan_id: "gate2-n2"', 'plan_id: "gate2-n2-mismatch"'));
    writePermissionRef(root, 'permissions/T1.yaml');
    commitPaths(root, ['plan/'], 'gate2-n2: plan_id mismatch fixture');

    const lenBefore = gateJournalLen();
    await planIdInput.fill('gate2-n2');
    await submitGate2.click();
    await expectSinglePlanError('does not match filename-derived plan ID', 'G2-N2');
    expect(readNewRecordsByType(gatePath, lenBefore, 'gate_request'), 'G2-N2 不應產生新的 gate_request').toEqual([]);
    expect(readNewRecordsByType(gatePath, lenBefore, 'approval_record'), 'G2-N2 不應產生新的 approval_record').toEqual([]);

    removePlanDoc(root, 'gate2-n2');
    commitPaths(root, ['plan/'], 'gate2-n2: cleanup');
  }

  // ---- G2-N3：scenario 不存在於 spec ----
  {
    writePlanDoc(root, 'gate2-n3', [
      { id: 'T1', title: 'Task T1', scenarios: ['scenario-not-exist'], minimumRiskTier: 'medium', plannerRiskTier: 'medium', permissionsRef: 'permissions/T1.yaml' },
    ], c1);
    writePermissionRef(root, 'permissions/T1.yaml');
    commitPaths(root, ['plan/'], 'gate2-n3: unknown scenario fixture');

    const lenBefore = gateJournalLen();
    await planIdInput.fill('gate2-n3');
    await submitGate2.click();
    await expectSinglePlanError('not found in spec scenarios', 'G2-N3');
    expect(readNewRecordsByType(gatePath, lenBefore, 'gate_request'), 'G2-N3 不應產生新的 gate_request').toEqual([]);
    expect(readNewRecordsByType(gatePath, lenBefore, 'approval_record'), 'G2-N3 不應產生新的 approval_record').toEqual([]);

    removePlanDoc(root, 'gate2-n3');
    commitPaths(root, ['plan/'], 'gate2-n3: cleanup');
  }

  // ---- G2-N4：minimum_risk_tier 與重算值不符 ----
  {
    writeRiskPolicy(root, { defaultTier: 'high' }); // 重算值＝high
    writePlanDoc(root, 'gate2-n4', [
      { id: 'T1', title: 'Task T1', scenarios: ['scenario-ok'], minimumRiskTier: 'low', plannerRiskTier: 'high', permissionsRef: 'permissions/T1.yaml' }, // committed=low ≠ 重算 high
    ], c1);
    writePermissionRef(root, 'permissions/T1.yaml');
    commitPaths(root, ['plan/'], 'gate2-n4: minimum mismatch fixture');

    const lenBefore = gateJournalLen();
    await planIdInput.fill('gate2-n4');
    await submitGate2.click();
    await expectSinglePlanError('does not match recomputed', 'G2-N4');
    expect(readNewRecordsByType(gatePath, lenBefore, 'gate_request'), 'G2-N4 不應產生新的 gate_request').toEqual([]);
    expect(readNewRecordsByType(gatePath, lenBefore, 'approval_record'), 'G2-N4 不應產生新的 approval_record').toEqual([]);

    removePlanDoc(root, 'gate2-n4');
    commitPaths(root, ['plan/'], 'gate2-n4: cleanup');
  }

  // ---- G2-N5：planner_risk_tier 低於 minimum（fixture 缺口修正，design v4 §7.1）----
  {
    writeRiskPolicy(root, { defaultTier: 'medium' }); // 重算值＝medium，與下方 committed minimum 一致，只命中 planner-below-minimum
    writePlanDoc(root, 'gate2-n5', [
      { id: 'T1', title: 'Task T1', scenarios: ['scenario-ok'], minimumRiskTier: 'medium', plannerRiskTier: 'low', permissionsRef: 'permissions/T1.yaml' },
    ], c1);
    writePermissionRef(root, 'permissions/T1.yaml');
    commitPaths(root, ['plan/'], 'gate2-n5: planner below minimum fixture');

    const lenBefore = gateJournalLen();
    await planIdInput.fill('gate2-n5');
    await submitGate2.click();
    await expectSinglePlanError('below minimum_risk_tier', 'G2-N5');
    expect(readNewRecordsByType(gatePath, lenBefore, 'gate_request'), 'G2-N5 不應產生新的 gate_request').toEqual([]);
    expect(readNewRecordsByType(gatePath, lenBefore, 'approval_record'), 'G2-N5 不應產生新的 approval_record').toEqual([]);

    removePlanDoc(root, 'gate2-n5');
    commitPaths(root, ['plan/'], 'gate2-n5: cleanup');
  }

  // ---- G2-P1：正例，三個合法風險組合併入同一份 plan 的三個 task ----
  const tasks: PlanTaskYaml[] = [
    { id: 'T1', title: 'Task T1', scenarios: ['scenario-ok'], minimumRiskTier: 'medium', plannerRiskTier: 'medium', permissionsRef: 'permissions/T1.yaml' }, // selected==planner
    { id: 'T2', title: 'Task T2', scenarios: ['scenario-ok'], minimumRiskTier: 'medium', plannerRiskTier: 'high', permissionsRef: 'permissions/T2.yaml' }, // selected<planner，需 override reason
    { id: 'T3', title: 'Task T3', scenarios: ['scenario-ok'], minimumRiskTier: 'medium', plannerRiskTier: 'medium', permissionsRef: 'permissions/T3.yaml' }, // selected>planner
  ];
  {
    writeRiskPolicy(root, { defaultTier: 'medium' });
    writePlanDoc(root, 'gate2-p1', tasks, c1);
    writePermissionRef(root, 'permissions/T1.yaml');
    writePermissionRef(root, 'permissions/T2.yaml');
    writePermissionRef(root, 'permissions/T3.yaml');
    const c2 = commitPaths(root, ['plan/'], 'gate2-p1: valid plan fixture');
    recorder?.record('C2', { sha: c2, tasks });

    await planIdInput.fill('gate2-p1');
    await submitGate2.click();
    // review #448 reviewer 裁定：只送一次（上面 submitGate2.click()），
    // 不重送請求；等待上限由 15 秒放寬為 60 秒，pollStart 是 click 之後、
    // 呼叫 observeSubmitWait 當下（見 gateSubmitWait.ts）。同時記錄「觀察
    // 到 request 的耗時」（讀 journal 的時間，不是 callback／
    // send-to-response latency）與 15 秒時是否已出現的觀察，只記錄不讓
    // 測試失敗；60 秒仍沒有對應的 gate2 gate_request 才讓測試失敗。
    const g2p1SubmitWait = await observeSubmitWait(() => latestGateRequestApprovalId(gatePath, 'gate2'));
    recorder?.record('G2-P1-submit-wait', {
      observedDurationMs: g2p1SubmitWait.observedDurationMs,
      observedAt15sMark: g2p1SubmitWait.observedAt15sMark,
      found: g2p1SubmitWait.found,
    });
    expect(g2p1SubmitWait.found, '60 秒內應出現 gate2 的 gate_request（觀察到 request 的耗時，見 recorder）').toBe(true);
    const gate2ApprovalId = g2p1SubmitWait.value;
    expect(gate2ApprovalId).not.toBeNull();
    if (gate2ApprovalId === null) throw new Error('unreachable');

    const entry = page.locator(`[data-test="entry-${gate2ApprovalId}"]`);
    await entry.waitFor({ state: 'visible', timeout: 15_000 });

    // review #427 point 5（c）：T2 的 tier 選單選項應等於 tierOptions(task)
    // 的預期集合——minimum=medium 時只會出現 medium／high，不會有 low
    // （GateConsole.vue tierOptions：`allTiers.filter(tier => tierOrder[tier] >= minRank)`）。
    //
    // review #4（#430）必修缺陷 5（P2，讀碼確認的風險，尚未在 browser 重
    // 現）：`GateConsole.vue` 的 `ensureRiskContext` 是 async，entry 元素
    // 可能先於 tier 選單選項實際 render 出來就已經 visible——若在 entry
    // visible 後立即 `evaluateAll` 讀選項，可能讀到還沒填好的空集合。改
    // 成先用 `toHaveCount`（Playwright 內建的有界、可重試斷言）等選項數
    // 量到位，再讀值，不用固定 sleep。
    const t2Select = entry.locator('[data-test="selected-T2"]');
    const t2OptionsLocator = t2Select.locator('option');
    await expect(t2OptionsLocator, 'T2 的 tier 選單應渲染出 2 個選項（medium／high）').toHaveCount(2, { timeout: 10_000 });
    const t2Options = await t2OptionsLocator.evaluateAll(els => els.map(el => (el as HTMLOptionElement).value));
    expect(t2Options.sort(), 'T2（minimum=medium）的 tier 選單不應出現 low').toEqual(['high', 'medium'].sort());

    await t2Select.selectOption('medium'); // < planner(high)，需填 override reason

    // review #427 point 5（b）：override_reason 未填時，approve 按鈕必須 disabled。
    const t1Select = entry.locator('[data-test="selected-T1"]');
    const t3Select = entry.locator('[data-test="selected-T3"]');
    await t1Select.selectOption('medium'); // == planner，空 reason
    await t3Select.selectOption('high'); // > planner(medium)，空 reason
    const approveButton = entry.locator('[data-test="approve"]');
    await expect(approveButton, 'T2 override_reason 未填時 approve 應 disabled').toBeDisabled({ timeout: 5_000 });

    const t2OverrideReason = `b3a-2a gate2 e2e override reason (${env.runId})`;
    await entry.locator('[data-test="override-reason-T2"]').fill(t2OverrideReason);
    await expect(approveButton, '填妥 override_reason 後 approve 應可點擊').toBeEnabled({ timeout: 5_000 });

    await approveButton.click();
    await expect(page.locator(`[data-test="badge-${gate2ApprovalId}"]`), 'G2-P1 應轉為 active').toHaveClass(/badge-active/, { timeout: 15_000 });

    await expect
      .poll(() => latestApprovalRecordFor(gatePath, gate2ApprovalId)?.decision ?? null, { message: 'approval_record 應為 approved', timeout: 15_000 })
      .toBe('approved');
    const approvalRecord = latestApprovalRecordFor(gatePath, gate2ApprovalId);

    // review #427 point 5（a）：risk_decisions 逐欄位比對，不只看筆數。
    const metadata = approvalRecord?.metadata as { risk_decisions?: Array<Record<string, unknown>> } | undefined;
    const decisions = metadata?.risk_decisions ?? [];
    expect(decisions.length, 'risk_decisions 應有三筆').toBe(3);
    const byTaskId = Object.fromEntries(decisions.map(d => [d.task_id, d]));
    expect(byTaskId.T1.minimum_risk_tier).toBe('medium');
    expect(byTaskId.T1.planner_risk_tier).toBe('medium');
    expect(byTaskId.T1.selected_risk_tier).toBe('medium');
    expect('override_reason' in byTaskId.T1, 'T1 selected==planner，override_reason 應因 omitempty 不存在').toBe(false);

    expect(byTaskId.T2.minimum_risk_tier).toBe('medium');
    expect(byTaskId.T2.planner_risk_tier).toBe('high');
    expect(byTaskId.T2.selected_risk_tier).toBe('medium');
    expect(byTaskId.T2.override_reason, 'T2 的 override_reason 應等於填入的完整文字').toBe(t2OverrideReason);

    expect(byTaskId.T3.minimum_risk_tier).toBe('medium');
    expect(byTaskId.T3.planner_risk_tier).toBe('medium');
    expect(byTaskId.T3.selected_risk_tier).toBe('high');
    expect('override_reason' in byTaskId.T3, 'T3 selected>planner，override_reason 應因 omitempty 不存在').toBe(false);

    // review #427 point 5（d）：獨立重算五個 bindings，逐一比對 journal。
    const expectedSpecManifest = specManifestDigest(readScopedEntriesAtCommit(root, c1, specInScope));
    const expectedPlan = planManifestDigest(readScopedEntriesAtCommit(root, c2, planInScope));
    const expectedRiskPolicy = singleFileDigest(execFileSync('git', ['show', `${c2}:plan/risk-policy.yaml`], { cwd: root }));
    const expectedPermissionManifest = permissionManifestDigest(permissionEntriesAtCommit(root, c2, tasks.map(t => t.permissionsRef)));
    const expectedBaseCommit = `git:sha1:${c2}`;

    const actualBindings = bindingsOfGateRequest(gatePath, gate2ApprovalId);
    expect(actualBindings.spec_manifest, 'spec_manifest binding 應等於獨立重算值（來自 C1）').toBe(expectedSpecManifest);
    expect(actualBindings.plan, 'plan binding 應等於獨立重算值（來自 C2）').toBe(expectedPlan);
    expect(actualBindings.risk_policy, 'risk_policy binding 應等於獨立重算值（來自 C2）').toBe(expectedRiskPolicy);
    expect(actualBindings.permission_manifest, 'permission_manifest binding 應等於獨立重算值（引用檔案，非目錄 glob）').toBe(expectedPermissionManifest);
    expect(actualBindings.base_commit, 'base_commit binding 應等於獨立重算值（C2）').toBe(expectedBaseCommit);

    recorder?.record('G2-P1-bindings', {
      gate2ApprovalId,
      expected: { spec_manifest: expectedSpecManifest, plan: expectedPlan, risk_policy: expectedRiskPolicy, permission_manifest: expectedPermissionManifest, base_commit: expectedBaseCommit },
      actual: actualBindings,
      riskDecisions: byTaskId,
    });
  }
});
