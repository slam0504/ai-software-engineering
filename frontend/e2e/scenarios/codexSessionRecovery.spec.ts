// B3a-2b-2 Task E2：Codex browser session recovery——一個 commandExecution／
// 兩輪都 accept 的完整案例。全程真 UI：建立 codex session → 送出（round1）→
// 點真的 approval-allow → round1 內容可見 → 點真的 end-session → 等待同一
// WSID 的可觀察結束狀態 → 在同一個 pane 再次送出（round2）→ 點真的
// approval-allow → round2 的不同內容可見。
//
// 絕對禁止（同 codexApproval.spec.ts 既有裁定，逐項延續到本檔）：
//   - 不在 browser 注入 fake bindings——全程只用真的 `window.go.main.App.*`。
//   - 不直接呼叫 StartSession／EndSession／ResolveApproval 取代 UI——本檔
//     只用 `[data-test="create-codex"]`／`[data-test="composer-send"]`／
//     `[data-test="approval-allow"]`／`[data-test="end-session"]` 的真實
//     點擊，round2 的「再次送出」用的是與 round1 同一顆 composer-send
//     按鈕（真實 App 的 `submit()` 依 `m.active` 自動判斷該呼叫
//     StartSession 還是 SendMessage，見 frontend/src/stores/session.ts；
//     `m.active` 在 round1 的 session:done 事件之後變 false，因此 round2
//     的送出天然觸發 StartSession(wsid, text, m.resume, ...)，`m.resume`
//     則早在 round1 就由真實的 session_id 事件填成本輪 threadId——沒有任何
//     測試碼手動注入 resume 值）。
//   - 不沿用 codexHostOverride、不抽換 server／conn、不改 store／registry。
//
// 判定分工：wire 的完整兩輪順序（handshake＋thread/start＋thread/resume、
// 同一 threadId、兩輪 turn/item/approval ID 不同）與 manifest 的兩輪 identity
// 一律用 E1 已驗收的共用 judge（recoveryJudge.ts 的 judgeRecoverySequence／
// judgeRecoveryManifest），不在本檔另寫一份手動序列比對。DOM／audit／
// registry／App 端原始 wire 錄流則逐輪核對 identity，串成
// 「DOM ↔ 同 WSID 的 audit／registry ↔ fake wire／App 原始 wire」的獨立
// 預期鏈——預期值一律來自 `resolveScenario(env.scenario).build(env.runId)`
// （受版控的 builder），不從落地檔案回填。
//
// meta 取證（F1 既有裁定的延伸）：round1 的 fake 還活著時**不**等 finalized
// meta——round2 結束、fake process 自然 exit(0) 之後才等，且先保留當下讀到
// 的原始 bytes 再做可失敗斷言（沿用 wireEvidence.ts 的既有函式，未修改）。
import { expect, test } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
import { captureChromeArgv } from '../support/chromeArgv.js';
import { readScenarioRunEnv } from '../support/env.js';
import { getInMemoryGuardState, installNetworkGuard } from '../support/networkGuard.js';
import { waitForCliReady } from '../support/cliInfo.js';
import { parseManifest, parseRunLog } from '../support/scenario/verify.js';
import { resolveScenario } from '../support/scenario/scenarios.js';
import { judgeRecoveryManifest, judgeRecoverySequence } from '../support/scenario/recoveryJudge.js';
import { judgeAppWireRecovery, judgeConfigOnDiskAgainstBuilder, judgeGenerationUniqueness } from '../support/scenario/recoveryAppEvidence.js';
import { HarnessLogger } from '../support/logger.js';
import { assertWireEvidencePersisted, persistWireEvidence, selectGenerationsByIdentity, validateWireMeta, waitForWireMeta, WireMetaWaitError } from '../support/scenario/wireEvidence.js';

test.afterEach(async ({}, testInfo) => {
  if (testInfo.status !== testInfo.expectedStatus) {
    const env = readScenarioRunEnv();
    fs.writeFileSync(`${env.artifactsDir}/TEST_FAILED`, `${testInfo.title}: ${testInfo.status}\n`);
  }
});

test('codex session recovery: 真 App 啟動 → round1（Start→approval→內容）→ 真的 end-session → 可觀察結束 → 同一 pane 再次送出 round2（resume→approval→不同內容）', async ({ page }) => {
  const env = readScenarioRunEnv();
  const expectedScenario = resolveScenario(env.scenario);
  const expectedCfg = expectedScenario.build(env.runId);
  if (!expectedCfg.secondTurn) {
    throw new Error(`codexSessionRecovery.spec.ts 只服務有 secondTurn 的 scenario，實際 scenario=${env.scenario} 缺少 secondTurn`);
  }
  const secondTurn = expectedCfg.secondTurn;
  test.info().annotations.push({ type: 'scenario', description: `${env.scenario} (round1/round2 decision=accept)` });

  await installNetworkGuard(page.context(), env.artifactsDir);
  await page.goto(env.baseUrl);
  captureChromeArgv(env.artifactsDir);

  const info = await waitForCliReady(page, 60_000);
  expect(info.toolsSource, 'toolsSource 應為 env').toBe('env');
  expect(info.workspaceSource, 'workspaceSource 應為 env').toBe('env');
  expect(info.codexVersion, '假 codex 版本應等於本次執行的 scenario 專屬字串').toBe(env.codexVersion);
  expect(info.claudeVersion, '假 claude 版本應等於本次執行專屬字串').toBe(env.claudeVersion);
  expect(info.startupError, 'startupError 應為空').toBe('');

  const infoWorkspaceReal = fs.realpathSync(info.workspace);
  const envWorkspaceReal = fs.realpathSync(env.workspaceDir);
  expect(infoWorkspaceReal, `CLIInfo.workspace 的 canonical path 應等於 env.workspaceDir（${env.workspaceDir}）`).toBe(envWorkspaceReal);
  const infoToolsReal = fs.realpathSync(info.toolsDir);
  const envToolsReal = fs.realpathSync(env.toolsDir);
  expect(infoToolsReal, `CLIInfo.toolsDir 的 canonical path 應等於 env.toolsDir（${env.toolsDir}）`).toBe(envToolsReal);

  const harness = new HarnessLogger(env.artifactsDir);
  const workbenchDir = path.join(env.workspaceDir, '.workbench');
  const auditPath = path.join(workbenchDir, 'audit.jsonl');
  const wsRegistryPath = path.join(workbenchDir, 'workspace-sessions.json');
  interface AuditEntry { ts: string; kind: string; data: Record<string, unknown> }
  function readAuditEntries(): AuditEntry[] {
    return fs.readFileSync(auditPath, 'utf8').split('\n').map(l => l.trim()).filter(l => l.length > 0)
      .map(l => JSON.parse(l) as AuditEntry);
  }

  // ---------- Round 1：建立 session → 送出 → approval allow → 內容可見 ----------
  await page.locator('[data-test="create-codex"]').first().click();

  const composer = page.locator('[data-test="composer"]');
  await composer.waitFor({ state: 'visible', timeout: 15_000 });

  const prompt1 = `b3a2b2-recovery-r1-${env.runId}`;
  const textarea = page.locator('[data-test="composer-textarea"]');
  await textarea.click();
  await textarea.fill(prompt1);
  await page.locator('[data-test="composer-send"]').click();

  const dialog = page.locator('[data-test="approval-dialog"]');
  await dialog.waitFor({ state: 'visible', timeout: 30_000 });
  await expect(dialog, 'round1 approval dialog 應顯示 codex provider').toContainText('codex');
  await page.screenshot({ path: `${env.artifactsDir}/ui-r1-approval-dialog-visible.png` });

  const domWsid = await dialog.getAttribute('data-test-wsid');
  const domApprovalId1 = await dialog.getAttribute('data-test-approval-id');
  const domMethod1 = await dialog.getAttribute('data-test-approval-method');
  expect(domWsid, 'ApprovalDialog 應顯示非空 WSID').toBeTruthy();
  expect(domApprovalId1, 'round1 ApprovalDialog 應顯示非空 approval id').toBeTruthy();
  expect(domMethod1, 'round1 ApprovalDialog 顯示的 method 應等於本次 scenario 的 approvalMethod').toBe(env.scenarioApprovalMethod);

  const rawParamsText1 = await page.locator('[data-test="approval-raw-params"]').textContent();
  expect(rawParamsText1, 'round1 approval-raw-params 不應為空').toBeTruthy();
  const rawParams1 = JSON.parse(rawParamsText1 ?? '{}') as { threadId?: unknown; turnId?: unknown; itemId?: unknown };
  expect(rawParams1.threadId, 'round1 raw params.threadId 應等於本次 scenario threadId').toBe(env.scenarioThreadId);
  expect(rawParams1.turnId, 'round1 raw params.turnId 應等於本次 scenario turnId').toBe(env.scenarioTurnId);
  expect(rawParams1.itemId, 'round1 raw params.itemId 應等於本次 scenario itemId').toBe(env.scenarioItemId);

  await page.locator('[data-test="approval-allow"]').click();
  await expect(dialog, 'round1 approval 解決後 dialog 應關閉').toHaveCount(0, { timeout: 15_000 });

  const ownerPane = page.locator(`[data-test-wsid="${domWsid}"]`);
  await expect(ownerPane, `應存在 data-test-wsid=${domWsid} 的 pane`).toHaveCount(1);
  const round1Text = `b3a2b2-scenario-content-${env.runId}`;
  await expect(
    ownerPane.locator('.bubble.assistant', { hasText: round1Text }),
    'round1 approval 核可後，同一個 WSID 的 pane 內應顯示 round1 的 assistant 內容',
  ).toBeVisible({ timeout: 30_000 });
  await page.screenshot({ path: `${env.artifactsDir}/ui-r1-new-content.png` });

  // audit.jsonl：round1 request／decision 逐項核對，串上 DOM 讀到的 domWsid／domApprovalId1。
  let auditEntries = readAuditEntries();
  const round1RequestEntry = auditEntries.find(e => e.kind === 'codex_approval_request' && e.data.id === domApprovalId1);
  expect(round1RequestEntry, `audit.jsonl 應有 id=${domApprovalId1} 的 round1 codex_approval_request`).toBeTruthy();
  expect(round1RequestEntry?.data.wsid, 'round1 audit request 的 wsid 應等於 DOM 顯示的 WSID').toBe(domWsid);
  expect(round1RequestEntry?.data.method, 'round1 audit request 的 method 應等於 scenario approvalMethod').toBe(env.scenarioApprovalMethod);
  const round1AuditRawParams = round1RequestEntry?.data.raw_params as { threadId?: unknown; turnId?: unknown; itemId?: unknown } | undefined;
  expect(round1AuditRawParams?.threadId, 'round1 audit raw_params.threadId 應等於 scenario threadId').toBe(env.scenarioThreadId);
  expect(round1AuditRawParams?.turnId, 'round1 audit raw_params.turnId 應等於 scenario turnId').toBe(env.scenarioTurnId);
  expect(round1AuditRawParams?.itemId, 'round1 audit raw_params.itemId 應等於 scenario itemId').toBe(env.scenarioItemId);

  const round1DecisionEntry = auditEntries.find(e => e.kind === 'codex_approval_decision' && e.data.id === domApprovalId1);
  expect(round1DecisionEntry, `audit.jsonl 應有 id=${domApprovalId1} 的 round1 codex_approval_decision`).toBeTruthy();
  expect(round1DecisionEntry?.data.decision, 'round1 audit decision 應為 accept').toBe('accept');

  // ---------- 真的 end-session：等待同一 WSID 的可觀察結束狀態 ----------
  await page.locator('[data-test="end-session"]').click();
  await expect(
    ownerPane,
    `同一個 data-test-wsid=${domWsid} 的 pane 應在 end-session 之後反映 data-test-active="false"（session.ts applyDone() 對真的 session:done 事件的既有狀態，非新造欄位）`,
  ).toHaveAttribute('data-test-active', 'false', { timeout: 20_000 });
  await page.screenshot({ path: `${env.artifactsDir}/ui-after-end-session.png` });

  // ---------- Round 2：同一個 pane 再次送出 → 真的 resume → approval allow → 不同內容 ----------
  const prompt2 = `b3a2b2-recovery-r2-${env.runId}`;
  await textarea.click();
  await textarea.fill(prompt2);
  await page.locator('[data-test="composer-send"]').click();

  await dialog.waitFor({ state: 'visible', timeout: 30_000 });
  await expect(dialog, 'round2 approval dialog 應顯示 codex provider').toContainText('codex');
  await page.screenshot({ path: `${env.artifactsDir}/ui-r2-approval-dialog-visible.png` });

  const domWsid2 = await dialog.getAttribute('data-test-wsid');
  const domApprovalId2 = await dialog.getAttribute('data-test-approval-id');
  const domMethod2 = await dialog.getAttribute('data-test-approval-method');
  expect(domWsid2, 'round2 應與 round1 是同一個 WSID').toBe(domWsid);
  expect(domApprovalId2, 'round2 ApprovalDialog 應顯示非空 approval id').toBeTruthy();
  expect(domApprovalId2, 'round2 approval id 必須與 round1 不同').not.toBe(domApprovalId1);
  expect(domMethod2, 'round2 顯示的 method 應等於本次 scenario 的 approvalMethod（兩輪固定沿用同一個 method）').toBe(env.scenarioApprovalMethod);

  const rawParamsText2 = await page.locator('[data-test="approval-raw-params"]').textContent();
  expect(rawParamsText2, 'round2 approval-raw-params 不應為空').toBeTruthy();
  const rawParams2 = JSON.parse(rawParamsText2 ?? '{}') as { threadId?: unknown; turnId?: unknown; itemId?: unknown };
  expect(rawParams2.threadId, 'round2 raw params.threadId 應等於 round1 的同一個 threadId（thread/resume）').toBe(env.scenarioThreadId);
  expect(rawParams2.turnId, 'round2 raw params.turnId 應等於獨立期望 secondTurn.turnId').toBe(secondTurn.turnId);
  expect(rawParams2.itemId, 'round2 raw params.itemId 應等於獨立期望 secondTurn.itemId').toBe(secondTurn.itemId);
  expect(rawParams2.turnId, 'round2 turnId 必須與 round1 turnId 不同').not.toBe(env.scenarioTurnId);
  expect(rawParams2.itemId, 'round2 itemId 必須與 round1 itemId 不同').not.toBe(env.scenarioItemId);

  await page.locator('[data-test="approval-allow"]').click();
  await expect(dialog, 'round2 approval 解決後 dialog 應關閉').toHaveCount(0, { timeout: 15_000 });

  const round2Text = `b3a2b2-scenario-content2-${env.runId}`;
  await expect(
    ownerPane.locator('.bubble.assistant', { hasText: round2Text }),
    'round2 approval 核可後，同一個 WSID 的 pane 內應顯示 round2 的（與 round1 不同的）assistant 內容',
  ).toBeVisible({ timeout: 30_000 });
  await page.screenshot({ path: `${env.artifactsDir}/ui-r2-new-content.png` });
  // round1 內容仍應留在同一個 pane 內（兩輪內容並存，不是被覆蓋）——同時證明
  // round2 顯示的是「新增」而非「唯一」內容。
  await expect(
    ownerPane.locator('.bubble.assistant', { hasText: round1Text }),
    'round1 的內容應仍留在同一個 pane（round2 是新增，不是取代）',
  ).toBeVisible();

  auditEntries = readAuditEntries();
  const round2RequestEntry = auditEntries.find(e => e.kind === 'codex_approval_request' && e.data.id === domApprovalId2);
  expect(round2RequestEntry, `audit.jsonl 應有 id=${domApprovalId2} 的 round2 codex_approval_request`).toBeTruthy();
  expect(round2RequestEntry?.data.wsid, 'round2 audit request 的 wsid 應與 round1 相同').toBe(domWsid);
  expect(round2RequestEntry?.data.method, 'round2 audit request 的 method 應等於 scenario approvalMethod').toBe(env.scenarioApprovalMethod);
  const round2AuditRawParams = round2RequestEntry?.data.raw_params as { threadId?: unknown; turnId?: unknown; itemId?: unknown } | undefined;
  expect(round2AuditRawParams?.threadId, 'round2 audit raw_params.threadId 應等於 round1 同一個 threadId').toBe(env.scenarioThreadId);
  expect(round2AuditRawParams?.turnId, 'round2 audit raw_params.turnId 應等於獨立期望 secondTurn.turnId').toBe(secondTurn.turnId);
  expect(round2AuditRawParams?.itemId, 'round2 audit raw_params.itemId 應等於獨立期望 secondTurn.itemId').toBe(secondTurn.itemId);

  const round2DecisionEntry = auditEntries.find(e => e.kind === 'codex_approval_decision' && e.data.id === domApprovalId2);
  expect(round2DecisionEntry, `audit.jsonl 應有 id=${domApprovalId2} 的 round2 codex_approval_decision`).toBeTruthy();
  expect(round2DecisionEntry?.data.decision, 'round2 audit decision 應為 accept').toBe('accept');

  const guardState = getInMemoryGuardState(env.artifactsDir);
  expect(guardState.violations, 'network guard 不應攔到任何違規').toEqual([]);

  // workspace-sessions.json：同一個 WSID 的登記項目，provider=codex；
  // resume_session_id 讀取驗證（UI 本來就會帶 m.resume，這裡不宣稱證明了
  // backend 空 resume 的 registry fallback，只核對 registry 反映的值與本輪
  // threadId 一致）。
  const wsRegistryRaw = JSON.parse(fs.readFileSync(wsRegistryPath, 'utf8')) as {
    entries?: Record<string, { wsid?: string; provider?: string; resume_session_id?: string }>;
  };
  const wsEntry = domWsid ? wsRegistryRaw.entries?.[domWsid] : undefined;
  expect(wsEntry, `workspace-sessions.json 應有 WSID=${domWsid} 的登記項目`).toBeTruthy();
  expect(wsEntry?.provider, 'workspace-sessions.json 登記的 provider 應為 codex').toBe('codex');
  expect(wsEntry?.resume_session_id, 'workspace-sessions.json 的 resume_session_id 應等於本輪 threadId').toBe(env.scenarioThreadId);

  fs.copyFileSync(auditPath, path.join(env.artifactsDir, 'app-audit.jsonl'));
  fs.copyFileSync(wsRegistryPath, path.join(env.artifactsDir, 'app-workspace-sessions.json'));

  // ---------- App 端原始 wire 錄流：round2 結束後才等 finalized meta ----------
  const wireLogsDir = path.join(workbenchDir, 'wire-logs');
  const identityCandidates = selectGenerationsByIdentity(wireLogsDir, {
    approvalMethod: env.scenarioApprovalMethod,
    approvalRequestId: env.scenarioApprovalRequestId,
  });
  // B：generation 選擇必須唯一——candidates != 1 一律 fail loud，不依 mtime
  // 猜最新（judgeGenerationUniqueness 的歧義診斷會列出所有候選檔名＋mtime）。
  const uniquenessViolations = judgeGenerationUniqueness(identityCandidates);
  if (uniquenessViolations.length > 0) {
    // reviewer #325：先前這個分支只留路徑診斷就 throw，**歧義當下的各候選原始
    // 內容完全沒被保存**，事後無法查。改成先把可取得的每個候選 wire/meta 原始
    // bytes 存進獨立子目錄，再讓下面的 expect 失敗。缺失的照實記錄，
    // **不合成內容、不選最新**。
    const ambiguousDir = path.join(env.artifactsDir, 'generation-ambiguity');
    const saveNotes: string[] = [];
    try {
      fs.mkdirSync(ambiguousDir, { recursive: true });
      identityCandidates.forEach((c, i) => {
        const base = `candidate-${i}-${path.basename(c.jsonlPath)}`;
        try {
          fs.copyFileSync(c.jsonlPath, path.join(ambiguousDir, base));
        } catch (e) {
          saveNotes.push(`候選 ${i} 的 wire 無法複製（${c.jsonlPath}）：${String(e)}`);
        }
        const metaSrc = c.jsonlPath.replace(/\.jsonl$/, '.meta.json');
        if (fs.existsSync(metaSrc)) {
          try {
            fs.copyFileSync(metaSrc, path.join(ambiguousDir, `${base}.meta.json`));
          } catch (e) {
            saveNotes.push(`候選 ${i} 的 meta 無法複製（${metaSrc}）：${String(e)}`);
          }
        } else {
          saveNotes.push(`候選 ${i} 沒有對應的 meta 檔（${metaSrc}）——照實記錄，不合成`);
        }
      });
      fs.writeFileSync(
        path.join(ambiguousDir, 'AMBIGUITY-NOTES.txt'),
        [
          `run-id=${env.runId}`,
          `wireLogsDir=${wireLogsDir}`,
          `候選數=${identityCandidates.length}（唯一性判定要求恰為 1）`,
          ...identityCandidates.map((c, i) => `候選 ${i}: ${c.jsonlPath}`),
          ...uniquenessViolations.map(v => `violation: ${v}`),
          ...saveNotes,
        ].join('\n') + '\n',
      );
    } catch (e) {
      saveNotes.push(`歧義證據目錄建立／寫入失敗：${String(e)}`);
    }
    harness.log(
      `E2：wire-logs（${wireLogsDir}）的 generation 選擇未通過唯一性判定（run-id=${env.runId}）：`
      + `${JSON.stringify(uniquenessViolations)}——各候選原始 bytes 已保存於 ${ambiguousDir}`
      + `${saveNotes.length > 0 ? `（保存診斷：${saveNotes.join('；')}）` : ''}`
      + '。不得改用 audit.jsonl 頂替，也不合成假證據、不依 mtime 猜最新。',
    );
  }
  expect(
    uniquenessViolations,
    `App 端原始 wire 錄流應存在且唯一符合本案 round1 wire identity（${wireLogsDir}）：${JSON.stringify(uniquenessViolations)}`,
  ).toEqual([]);
  const chosenGeneration = identityCandidates[0];

  const expectedCodexBinPath = path.join(envToolsReal, 'codex-cli', 'node_modules', '.bin', 'codex');
  const expectedCodexBinRealPath = fs.realpathSync(expectedCodexBinPath);

  let waitResult: Awaited<ReturnType<typeof waitForWireMeta>> | undefined;
  let waitError: unknown;
  try {
    waitResult = await waitForWireMeta(chosenGeneration.metaPath, { deadlineMs: 15_000 });
  } catch (e) {
    waitError = e;
  }

  const waitErrorDiagnostics = waitError instanceof WireMetaWaitError ? waitError.diagnostics : undefined;
  const persistResult = persistWireEvidence(
    {
      jsonlSourcePath: chosenGeneration.jsonlPath,
      metaRaw: waitResult ? waitResult.raw : (waitErrorDiagnostics?.lastRawBytes as string | undefined),
    },
    env.artifactsDir,
  );
  if (persistResult.copyErrors.length > 0) {
    harness.log(`E2：證據保存過程有診斷訊息（不代表一定失敗）：${persistResult.copyErrors.join('；')}`);
  }

  if (!waitResult) {
    harness.log(
      `E2：等待 finalize meta 失敗（run-id=${env.runId}，generation=${chosenGeneration.file}，`
      + `metaPath=${chosenGeneration.metaPath}）：${waitErrorDiagnostics ? JSON.stringify(waitErrorDiagnostics) : String(waitError)}；`
      + `已嘗試保存當下可讀到的原始 bytes（wireLogCopied=${persistResult.wireLogCopied}／metaCopied=${persistResult.metaCopied}）`,
    );
    throw waitError;
  }
  harness.log(
    `E2：finalize meta 已就緒（run-id=${env.runId}，generation=${chosenGeneration.file}，`
    + `等待 ${waitResult.elapsedMs}ms／共 ${waitResult.attempts} 次嘗試，已保存副本 wireLogCopied=${persistResult.wireLogCopied}／metaCopied=${persistResult.metaCopied}）`,
  );

  const metaViolations = validateWireMeta(waitResult.meta, {
    provider: 'codex',
    expectedArgvTail: ['app-server'],
    expectedArgv0RealPath: expectedCodexBinRealPath,
  });
  if (metaViolations.length > 0) {
    harness.log(`E2：finalize meta 核對失敗（run-id=${env.runId}，generation=${chosenGeneration.file}）：${JSON.stringify(metaViolations)}`);
  }
  expect(metaViolations, `finalize meta 應無違規：${JSON.stringify(metaViolations)}（meta=${JSON.stringify(waitResult.meta)}）`).toEqual([]);

  assertWireEvidencePersisted(persistResult, { requireMeta: true });
  const copiedWireLogPath = persistResult.wireLogDestPath;

  const recoveryExpectation = {
    threadId: env.scenarioThreadId,
    approvalMethod: env.scenarioApprovalMethod,
    round1: {
      turnId: env.scenarioTurnId,
      itemId: env.scenarioItemId,
      approvalRequestId: env.scenarioApprovalRequestId,
      decision: 'accept' as const,
      afterApproval: expectedCfg.afterApproval,
      turnStatus: expectedCfg.turnStatus,
    },
    round2: {
      turnId: secondTurn.turnId,
      itemId: secondTurn.itemId,
      approvalRequestId: secondTurn.approvalRequestId,
      decision: 'accept' as const,
      afterApproval: secondTurn.afterApproval,
      turnStatus: secondTurn.turnStatus,
    },
  };

  // A：App 原始 wire 錄流的歸屬與完整序列——重用 E1 已驗收的
  // judgeRecoverySequence（見 recoveryAppEvidence.ts#judgeAppWireRecovery），
  // 不再只用 .find() 撈四個 approval frame（那樣既不驗 wsid 歸屬、也不驗完
  // 整序列，錯 WSID／截斷成 4 個 approval frame 兩者都能 PASS）。
  const appWireEntriesRaw: unknown[] = fs.readFileSync(copiedWireLogPath, 'utf8')
    .split('\n').map(l => l.trim()).filter(l => l.length > 0)
    .map(l => JSON.parse(l));

  const appWireViolations = judgeAppWireRecovery(appWireEntriesRaw, {
    domWsid: domWsid as string,
    recovery: recoveryExpectation,
  });
  if (appWireViolations.length > 0) {
    harness.log(`E2：App 端原始 wire 錄流判定失敗（run-id=${env.runId}，generation=${chosenGeneration.file}）：${JSON.stringify(appWireViolations)}`);
  }
  expect(appWireViolations, `judgeAppWireRecovery 應無違規：${JSON.stringify(appWireViolations)}`).toEqual([]);

  // ---------- fake 的 scenario-wire.log／manifest：直接用 E1 共用 judge ----------
  await expect
    .poll(() => {
      try {
        return parseManifest(env.scenarioManifestPath).exitCode;
      } catch (e) {
        const msg = e instanceof Error ? e.message : String(e);
        if (msg.includes('ENOENT')) return null;
        throw e;
      }
    }, { message: 'scenario fake app-server 應在 round2 turn/completed 之後乾淨收尾（exitCode=0）', timeout: 15_000 })
    .toBe(0);

  const runLog = parseRunLog(env.scenarioLogPath);
  const sequenceViolations = judgeRecoverySequence(runLog, recoveryExpectation);
  expect(sequenceViolations, `judgeRecoverySequence 應無違規：${JSON.stringify(sequenceViolations)}`).toEqual([]);

  const manifest = parseManifest(env.scenarioManifestPath);
  const manifestViolations = judgeRecoveryManifest(manifest, {
    approvalMethod: env.scenarioApprovalMethod,
    round1: { approvalRequestId: env.scenarioApprovalRequestId, decision: 'accept' },
    round2: { approvalRequestId: secondTurn.approvalRequestId, decision: 'accept' },
  });
  expect(manifestViolations, `judgeRecoveryManifest 應無違規：${JSON.stringify(manifestViolations)}`).toEqual([]);

  // C：落地 scenario-config.json 對上受版控 builder 的 expectedCfg（含
  // secondTurn）——先前只核對 env 欄位，從未讀取 scenarioConfigPath／交叉
  // 核對落地檔案本身。重用 judgeRunIdentity（round1＋manifest 欄位集合），
  // 另補 secondTurn 的獨立深比對（見 recoveryAppEvidence.ts）。
  const scenarioConfigOnDisk: unknown = JSON.parse(fs.readFileSync(env.scenarioConfigPath, 'utf8'));
  const configViolations = judgeConfigOnDiskAgainstBuilder(manifest, scenarioConfigOnDisk, {
    runId: env.runId,
    cfg: expectedCfg,
    decision: expectedScenario.decision,
  });
  if (configViolations.length > 0) {
    harness.log(`E2：落地 scenario-config.json 對上 builder 判定失敗（run-id=${env.runId}，scenarioConfigPath=${env.scenarioConfigPath}）：${JSON.stringify(configViolations)}`);
  }
  expect(configViolations, `judgeConfigOnDiskAgainstBuilder 應無違規：${JSON.stringify(configViolations)}`).toEqual([]);

  // env identity 對上獨立期望（builder，不是落地檔案）——同 codexApproval.spec.ts 既有慣例。
  expect(env.scenario, 'env.scenario 應等於獨立期望的 scenario').toBe(expectedCfg.scenario);
  expect(env.scenarioThreadId, 'env.scenarioThreadId 應等於獨立期望的 threadId').toBe(expectedCfg.threadId);
  expect(env.scenarioTurnId, 'env.scenarioTurnId 應等於獨立期望的 round1 turnId').toBe(expectedCfg.turnId);
  expect(env.scenarioItemId, 'env.scenarioItemId 應等於獨立期望的 round1 itemId').toBe(expectedCfg.itemId);
  expect(env.scenarioApprovalMethod, 'env.scenarioApprovalMethod 應等於獨立期望的 approvalMethod').toBe(expectedCfg.approvalMethod);
  expect(env.scenarioApprovalRequestId, 'env.scenarioApprovalRequestId 應等於獨立期望的 round1 approvalRequestId').toBe(expectedCfg.approvalRequestId);
  expect(env.scenarioDecision, 'env.scenarioDecision 應等於獨立期望的 decision').toBe(expectedScenario.decision);

  harness.log(`E2：兩輪判定完成——sequenceViolations=[]／manifestViolations=[]，manifest.pid=${manifest.pid}（單一 process 涵蓋兩輪，無重啟／換代）`);
});
