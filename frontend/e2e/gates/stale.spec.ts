// B3a-2a STALE flow（design v4 §9／§11.3；review #2／#3（#427）修正版）。
//
// 硬性順序：S-N1 必須排在 S-P1 之前——S-P1 會讓 Gate 1（進而 Gate 2(P1)）
// 轉為 stale，`activeGate1Binding` 之後找不到 active Gate 1，S-N1 需要的
// 「active Gate 2」前提就不成立。
//
// S-N1 五點方案（design v4 §9.1／review #2 修正）：
//   1. 鎖定 active Gate2 的 approval_id、C2、四個原始 digest（逐一比對 journal
//      bindings，不只比 base_commit）；
//   2. HEAD 前移到 C3（只 add NOTES.md，不用 -A）＋用 current 系列重算四個
//      digest 確認不變，並核對 C2..C3 的 diff 只含 NOTES.md；
//   3. 先建 listener 再寫帶 run-id／nonce 的唯一探針路徑，只接受本次 probe 對應事件；
//   4. audit.jsonl／events.jsonl 窗口式讀取，必要斷言沒有 spec_watch_error／workspace stream_error；
//   5. gate.jsonl before/after 確認沒有新增 stale transition；**明確刷新
//      畫面（page.reload）再核對 P1 仍 active**（review #427 additional
//      corrections：不能只看之前就已經是 active 的 badge，那不能證明刷新
//      後仍然正確），用 current 系列再次確認四個 digest 未變；listener
//      **在 try/finally 的 finally 解除**（review #427 additional
//      corrections），避免任何提前 return／throw 讓 listener 洩漏到 S-P1。
//
// review #427 point 1 修正：journal 欄位改用正式 op_id／at／records（經
// journalReader.ts 集中處理，不再手刻 JSON.parse 迴圈）。
//
// review #427 point 3 修正：`recomputeAtSubmitCommits` 的 `risk_policy`
// 改讀 C2 當下的 git blob（不是目前磁碟檔案）；`permission_manifest` 改用
// `permissionEntriesAtCommit`（引用檔案，不是 `plan/permissions/**` 目錄
// glob）。`writePlanDoc`／`writePermissionRef` 補上 title／完整 ref 路徑。
//
// review #427 point 5 修正：S-N1／S-P1 的判定邏輯改呼叫
// `gateStaleJudge.ts` 的純函式（`judgeBindingsUnchanged`／
// `judgeStaleTransition`），該檔已有獨立 selftest 涵蓋負控制。
//
// **待 browser run 驗證**：全部 selector、有界輪詢的實際命中時間、
// window.runtime 事件掛鉤在真實頁面下的行為、`page.reload()` 後 CLIInfo
// 重新就緒的實際時序。
import { expect, test } from '@playwright/test';
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { readRunEnv } from '../support/env.js';
import { installNetworkGuard } from '../support/networkGuard.js';
import { waitForCliReady } from '../support/cliInfo.js';
import { verifyGateFlowDescriptor } from '../support/gates/gateFlowDescriptor.js';
import {
  commitPaths, commitStillExists, permissionEntriesAtCommit, planInScope,
  readCurrentPermissionManifestEntries, readCurrentRiskPolicyRaw, readCurrentScopedEntries,
  readScopedEntriesAtCommit, specInScope, writeFeatureFile, writePermissionRef, writePlanDoc,
  writeRiskPolicy, type PlanTaskYaml, PLAN_SCOPE_ROOTS, SPEC_SCOPE_ROOTS,
} from '../support/gates/gate2Fixture.js';
import {
  permissionManifestDigest, planManifestDigest, singleFileDigest, specManifestDigest,
} from '../support/gates/canonicalDigest.js';
import {
  assertNoAuditSpecWatchError, assertNoWorkspaceStreamError, bindingsOfGateRequest, captureAuditBaseline,
  captureEventsBaseline, diffNewTransitions, hasNewStaleTransitionFor, latestGateRequestApprovalId,
  readAllTransitionsRequireExists,
} from '../support/gates/journalReader.js';
import { judgeBindingsUnchanged, judgeStaleTransition } from '../support/gates/gateStaleJudge.js';
import { probeAbsolutePath, registerProbeListener, waitForProbeMatch, writeProbeFileAt } from '../support/gates/gateProbe.js';
import { GateEvidenceRecorder } from '../support/gates/gateEvidence.js';
import { runGateAfterEach } from '../support/gates/gateAfterEachHook.js';
import { observeSubmitWait } from '../support/gates/gateSubmitWait.js';

// review #427 必修缺陷 6：見 gate1.spec.ts 同一段註解。
let recorder: GateEvidenceRecorder | undefined;

test.afterEach(async ({}, testInfo) => {
  const env = readRunEnv();
  // review #4（#430）必修缺陷 2：共用 helper——先寫「未完成」標記，body
  // 與 evidence flush 都成功才移除；evidence 錯誤原樣往外拋。
  runGateAfterEach(env, testInfo.title, testInfo.status, testInfo.expectedStatus, recorder);
  recorder = undefined;
});

/** `git diff --name-only <a> <b>`（用於核對 C2..C3 只含 NOTES.md）。 */
function gitDiffNameOnly(root: string, a: string, b: string): string[] {
  return execFileSync('git', ['diff', '--name-only', a, b], { cwd: root }).toString('utf8').split('\n').filter(Boolean);
}

/** 用「submit-time」的固定 commit 方法重算四個 binding（供 baseline 鎖定用，對齊 BuildCommittedSnapshotScoped 的來源）。 */
function recomputeAtSubmitCommits(root: string, c1: string, c2: string, tasks: PlanTaskYaml[]): Record<string, string> {
  const riskPolicyRaw = execFileSync('git', ['show', `${c2}:plan/risk-policy.yaml`], { cwd: root });
  return {
    spec_manifest: specManifestDigest(readScopedEntriesAtCommit(root, c1, specInScope)),
    plan: planManifestDigest(readScopedEntriesAtCommit(root, c2, planInScope)),
    permission_manifest: permissionManifestDigest(permissionEntriesAtCommit(root, c2, tasks.map(t => t.permissionsRef))),
    risk_policy: singleFileDigest(riskPolicyRaw),
  };
}

/**
 * 用「current」方法（worktree-scoped，對齊 production `currentSpecManifest`／
 * `currentPlanManifest`／`currentRiskPolicyDigest`／`currentPermissionManifest`
 * 實際讀取的來源）重算四個 binding。
 */
function recomputeCurrent(root: string): Record<string, string> {
  return {
    spec_manifest: specManifestDigest(readCurrentScopedEntries(root, SPEC_SCOPE_ROOTS, specInScope)),
    plan: planManifestDigest(readCurrentScopedEntries(root, PLAN_SCOPE_ROOTS, planInScope)),
    permission_manifest: permissionManifestDigest(readCurrentPermissionManifestEntries(root)),
    risk_policy: singleFileDigest(readCurrentRiskPolicyRaw(root)),
  };
}

test('STALE：S-N1（探針不造成 stale）必須先於 S-P1（真實 stale）', async ({ page }) => {
  // review #448 reviewer 裁定：Gate2 前置送核與 gate2.spec.ts G2-P1 同一
  // 診斷結論（native 端約 13.9 秒），連同 S-N1 matching probe 的 3→15 秒
  // 調整，240 秒 test body 避免流程外層先截斷。
  test.setTimeout(240_000);
  const env = readRunEnv();
  verifyGateFlowDescriptor(env, 'stale');
  const root = env.workspaceDir;
  const gatePath = path.join(root, '.workbench', 'gate.jsonl');
  const auditPath = path.join(root, '.workbench', 'audit.jsonl');
  const eventsPath = path.join(root, '.workbench', 'events.jsonl');
  recorder = new GateEvidenceRecorder('stale', env.runId);

  await installNetworkGuard(page.context(), env.artifactsDir);
  await page.goto(env.baseUrl);
  const info = await waitForCliReady(page, 60_000);
  expect(info.startupError, 'startupError 應為空').toBe('');

  // ---- 前置：Gate 1 核可 + Gate 2(P1) 核可（單一 task，簡化版） ----
  writeFeatureFile(root, 'checkout', 'scenario-ok');
  const c1 = commitPaths(root, ['spec/'], `stale e2e: add checkout.feature (${env.runId})`);
  recorder?.record('C1', { sha: c1 });

  await page.locator('[data-test="tab-spec"]').click();
  await page.locator('[data-test="submit-for-approval"]').click();
  await expect.poll(() => latestGateRequestApprovalId(gatePath, 'gate1'), { timeout: 15_000 }).not.toBeNull();
  const gate1ApprovalId = latestGateRequestApprovalId(gatePath, 'gate1');
  expect(gate1ApprovalId).not.toBeNull();
  if (gate1ApprovalId === null) throw new Error('unreachable');
  await page.locator(`[data-test="entry-${gate1ApprovalId}"]`).locator('[data-test="approve"]').click();
  await expect(page.locator(`[data-test="badge-${gate1ApprovalId}"]`)).toHaveClass(/badge-active/, { timeout: 15_000 });

  const tasks: PlanTaskYaml[] = [
    { id: 'T1', title: 'Task T1', scenarios: ['scenario-ok'], minimumRiskTier: 'medium', plannerRiskTier: 'medium', permissionsRef: 'permissions/T1.yaml' },
  ];
  writeRiskPolicy(root, { defaultTier: 'medium' });
  writePlanDoc(root, 'stale-p1', tasks, c1);
  writePermissionRef(root, 'permissions/T1.yaml');
  const c2 = commitPaths(root, ['plan/'], 'stale-p1: valid plan fixture');
  recorder?.record('C2', { sha: c2, tasks });

  await page.getByRole('button', { name: '計畫', exact: true }).click();
  await page.locator('[data-test="plan-id"]').fill('stale-p1');
  await page.locator('[data-test="submit-gate2"]').click();
  // review #448 reviewer 裁定：與 gate2.spec.ts G2-P1 同一調整——只送一次
  // （上面 submit-gate2 的 click()），等待上限由 15 秒放寬為 60 秒，記錄
  // 「觀察到 request 的耗時」與 15 秒時是否已出現的觀察（只記錄不讓測試
  // 失敗），60 秒仍無 gate2 gate_request 才讓測試失敗。
  const gate2SubmitWait = await observeSubmitWait(() => latestGateRequestApprovalId(gatePath, 'gate2'));
  recorder?.record('Gate2-precondition-submit-wait', {
    observedDurationMs: gate2SubmitWait.observedDurationMs,
    observedAt15sMark: gate2SubmitWait.observedAt15sMark,
    found: gate2SubmitWait.found,
  });
  expect(gate2SubmitWait.found, '60 秒內應出現 gate2 的 gate_request（觀察到 request 的耗時，見 recorder）').toBe(true);
  const gate2ApprovalId = gate2SubmitWait.value;
  expect(gate2ApprovalId).not.toBeNull();
  if (gate2ApprovalId === null) throw new Error('unreachable');
  const p1Entry = page.locator(`[data-test="entry-${gate2ApprovalId}"]`);
  await p1Entry.waitFor({ state: 'visible', timeout: 15_000 });
  await p1Entry.locator('[data-test="selected-T1"]').selectOption('medium');
  await p1Entry.locator('[data-test="approve"]').click();
  await expect(page.locator(`[data-test="badge-${gate2ApprovalId}"]`)).toHaveClass(/badge-active/, { timeout: 15_000 });

  // ================= S-N1（五點方案） =================

  // 1. 鎖定基準：獨立重算 submit-time 的四個 digest，逐一比對 journal 的
  //    gate_request.bindings（不只比 base_commit）。
  const baselineBindings = bindingsOfGateRequest(gatePath, gate2ApprovalId);
  expect(baselineBindings.base_commit, 'baseline base_commit 應等於 C2').toBe(`git:sha1:${c2}`);
  const baselineRecomputedAtSubmit = recomputeAtSubmitCommits(root, c1, c2, tasks);
  const lockResult = judgeBindingsUnchanged(baselineBindings, baselineRecomputedAtSubmit);
  recorder?.record('S-N1-baseline-lock', { baselineBindings, baselineRecomputedAtSubmit, lockResult });
  expect(lockResult.ok, `baseline 應與 journal bindings 相符：${lockResult.mismatches.join('; ')}`).toBe(true);
  // current 系列在此刻（HEAD 尚未前移，worktree 與 C1/C2 內容一致）應與 submit-time 重算相符。
  const baselineCurrent = recomputeCurrent(root);
  const baselineConsistency = judgeBindingsUnchanged(baselineCurrent, baselineRecomputedAtSubmit);
  expect(baselineConsistency.ok, `baseline：current 系列與 submit-time 系列在內容未變時應相符：${baselineConsistency.mismatches.join('; ')}`).toBe(true);

  // 2. HEAD 前移到 C3，只 add 指定的 NOTES.md（不用 -A，避免帶進 .workbench／殘留檔）。
  fs.writeFileSync(path.join(root, 'NOTES.md'), `S-N1 probe head-move (${env.runId})\n`);
  const c3 = commitPaths(root, ['NOTES.md'], `stale e2e: S-N1 head move (${env.runId})`);
  recorder?.record('C3', { sha: c3 });
  expect(c3, 'HEAD 應確實前移到新的 commit').not.toBe(c2);

  const changedC2C3 = gitDiffNameOnly(root, c2, c3);
  expect(changedC2C3, 'C2..C3 的變動檔案應恰好只有 NOTES.md').toEqual(['NOTES.md']);

  const afterMoveCurrent = recomputeCurrent(root);
  const afterMoveResult = judgeBindingsUnchanged(afterMoveCurrent, baselineCurrent);
  recorder?.record('S-N1-after-head-move', { afterMoveCurrent, afterMoveResult });
  expect(afterMoveResult.ok, `HEAD 前移後（current 系列）四個 digest 應與基準相符：${afterMoveResult.mismatches.join('; ')}`).toBe(true);
  expect(commitStillExists(root, c2), 'HEAD 前移後 C2 仍應存在（base_commit 的「歷史錨點」語意）').toBe(true);

  // 3.-5.：探針、audit/events 檢查、gate.jsonl 前後比對——listener 用
  // try/finally 確保任何提前 return／throw 都會解除（review #427 additional
  // corrections），**且 review #4（#430）必修缺陷 4 要求：無論成功、逾
  // 時、或斷言失敗，都要把已經收到的中間原始證據（baseline 位移與快照、
  // 實際新增的 audit／events 記錄、gate transitions 的 before／after／
  // new、probe 實際收到的 payload 序列、判定結果）送進 recorder——不能只
  // 靠 afterEach 最後的整輪 journal 快照，那時 S-P1 已經把 Gate1／P1 都
  // 改成 stale，重建不出 S-N1 判定當下的區間。**
  const auditBaseline = captureAuditBaseline(auditPath);
  const eventsBaseline = captureEventsBaseline(eventsPath);
  const gateTransitionsBaseline = readAllTransitionsRequireExists(gatePath);
  recorder?.record('S-N1-baselines', {
    auditBaselineOffset: auditBaseline.offset, auditBaselineSnapshot: auditBaseline.records,
    eventsBaselineOffset: eventsBaseline.offset, eventsBaselineSnapshot: eventsBaseline.records,
    gateTransitionsBaseline,
  });

  const probeAbsPath = probeAbsolutePath(root, env.runId);
  const handle = await registerProbeListener(page, probeAbsPath);
  try {
    writeProbeFileAt(probeAbsPath);

    // review #448 reviewer 裁定：S-N1 的等待不只是 200ms debounce，
    // Gate1／Gate2 的 durable reconcile 會做多次 git 查詢，3 秒的工作量估
    // 計不足，放寬為 15 秒並記錄實際耗時；匹配 path、無 error、
    // journal/digest 前後比對等既有斷言全部維持不變。
    const probeWaitStart = Date.now();
    const matched = await waitForProbeMatch(handle, 15_000, 100);
    const probeWaitElapsedMs = Date.now() - probeWaitStart;
    // review #4 必修缺陷 4：保存探針**實際收到**的完整 payload 序列（不是
    // 期望路徑＋布林值＋數量），不論後面的斷言是否通過都要保留這份序列。
    const probeEvents = await handle.allEvents();
    recorder?.record('S-N1-probe-events', { probeAbsPath, matched, matchedCount: await handle.matchedCount(), events: probeEvents, probeWaitElapsedMs });
    expect(matched, 'S-N1 探針應在 15 秒內收到對應的 spec:changed 事件').toBe(true);

    // 4. audit.jsonl／events.jsonl 窗口式必要斷言——實際新增的記錄也存進 recorder。
    const newAuditRecords = assertNoAuditSpecWatchError(auditPath, auditBaseline.offset);
    const newEventRecords = assertNoWorkspaceStreamError(eventsPath, eventsBaseline.offset);
    recorder?.record('S-N1-audit-events-new', { newAuditRecords, newEventRecords });

    // 5. gate.jsonl before/after：沒有新增 stale transition。
    const gateTransitionsAfter = readAllTransitionsRequireExists(gatePath);
    const newTransitions = diffNewTransitions(gateTransitionsBaseline, gateTransitionsAfter);
    recorder?.record('S-N1-gate-transitions-diff', { gateTransitionsAfter, newTransitions });
    expect(hasNewStaleTransitionFor(newTransitions, gate1ApprovalId), 'S-N1 不應讓 Gate 1 出現新的 stale transition').toBe(false);
    expect(hasNewStaleTransitionFor(newTransitions, gate2ApprovalId), 'S-N1 不應讓 Gate 2(P1) 出現新的 stale transition').toBe(false);
  } finally {
    // review #4 必修缺陷 4：即使上面任何一步 throw（逾時／斷言失敗），
    // 都要在 listener 解除之前把當下已經拿得到的 probe 事件序列保存下
    // 來——`allEvents()` 讀的是 browser 端累積到目前為止的狀態，不會因
    // 為外層 throw 而消失，只要在 dispose 之前讀都拿得到。
    try {
      const eventsAtFinally = await handle.allEvents();
      recorder?.record('S-N1-probe-events-at-finally', { events: eventsAtFinally });
    } catch (e) {
      recorder?.record('S-N1-probe-events-at-finally-read-error', { error: e instanceof Error ? e.message : String(e) });
    }
    // review #427 additional corrections：dispose 放在 finally，且用
    // EventsOnMultiple 回傳的取消函式（gateProbe.ts 內部已修正），不影響
    // App 自己掛在 spec:changed 上的其他 listener。
    await handle.dispose();
  }

  // 明確刷新畫面再核對 P1 仍 active（review #427 additional corrections：
  // 不能只看之前就已經是 active 的 badge，那不能證明「刷新後」仍然正確）。
  await page.reload();
  await waitForCliReady(page, 60_000);
  await expect(page.locator(`[data-test="badge-${gate2ApprovalId}"]`), 'S-N1 後、明確刷新畫面，P1 應仍是 active').toHaveClass(/badge-active/, { timeout: 15_000 });

  const finalCurrent = recomputeCurrent(root);
  const finalResult = judgeBindingsUnchanged(finalCurrent, baselineCurrent);
  recorder?.record('S-N1-final', { finalCurrent, finalResult });
  expect(finalResult.ok, `S-N1 後（current 系列）四個 digest 應仍與基準相符：${finalResult.mismatches.join('; ')}`).toBe(true);

  // ================= S-P1（review #2／#3 修正） =================
  // harness 直接改寫 fixture 內 in-scope 的 spec/glossary.md（改變 spec_manifest digest）。
  // **commit 只 stage spec/glossary.md**——避免把 S-N1 留下的探針檔
  // （spec/.e2e-reconcile-probe-*，未提交、仍是 untracked）一併帶進來。
  const glossaryPath = path.join(root, 'spec', 'glossary.md');
  fs.writeFileSync(glossaryPath, `# Glossary (S-P1, ${env.runId})\n\n- term: definition\n`);
  commitPaths(root, ['spec/glossary.md'], `stale e2e: S-P1 in-scope change (${env.runId})`);

  await expect(page.locator(`[data-test="badge-${gate1ApprovalId}"]`), 'S-P1 後 Gate 1 應轉為 stale').toHaveClass(/badge-stale/, { timeout: 15_000 });

  await expect
    .poll(() => {
      const transitions = readAllTransitionsRequireExists(gatePath);
      const gate1Stale = transitions.some(t => t.approval_id === gate1ApprovalId && t.to === 'stale');
      const gate2Stale = transitions.some(t => t.approval_id === gate2ApprovalId && t.to === 'stale');
      return gate1Stale && gate2Stale;
    }, { message: 'gate.jsonl 應同時出現 Gate 1 與 Gate 2(P1) 各自的 stale transition', timeout: 15_000 })
    .toBe(true);

  // review #427 point 5：cause／evidence_ref 逐一核對，透過正式判定函式
  // （gateStaleJudge.judgeStaleTransition，已有負控制 selftest 覆蓋）。
  const postStaleSpecManifest = specManifestDigest(readCurrentScopedEntries(root, SPEC_SCOPE_ROOTS, specInScope));
  const transitionsFinal = readAllTransitionsRequireExists(gatePath);
  const gate1Transition = transitionsFinal.find(t => t.approval_id === gate1ApprovalId && t.to === 'stale');
  const gate2Transition = transitionsFinal.find(t => t.approval_id === gate2ApprovalId && t.to === 'stale');
  const expectation = { cause: 'spec_manifest changed', evidenceRef: postStaleSpecManifest };
  const gate1Judged = judgeStaleTransition(gate1Transition, expectation);
  const gate2Judged = judgeStaleTransition(gate2Transition, expectation);
  recorder?.record('S-P1', { expectation, gate1Transition, gate2Transition, gate1Judged, gate2Judged });
  expect(gate1Judged.ok, `Gate 1 的 stale transition 應符合預期：${gate1Judged.reason ?? ''}`).toBe(true);
  expect(gate2Judged.ok, `Gate 2(P1) 的 stale transition 應符合預期：${gate2Judged.reason ?? ''}`).toBe(true);
});
