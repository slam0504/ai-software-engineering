// B3a-2b-2 F2：單一 Claude fresh-start approval 的 App/UI 整合檢查點。
// **同一支 spec 服務 allow 與 deny 兩案**（reviewer #403）——差別只在核定決策。
//
// 目標流程（每一段都必須是真的，不得以替身跳過）：
//   真 App StartSession(provider=Claude) → 受控假 Claude CLI → **真 App 產生的**
//   MCP config → 真 mcp-approval 子程序 → 真 App approval.Broker／pumpApprovals
//   → 瀏覽器看見 approval →（deny 案先在**既有**的 reason 輸入框填入本次核定理由）
//   → 實際點 allow 或 deny → 真 ResolveApproval → MCP 回覆對應 behavior
//   → 假 CLI 送該案預定的完成內容
//
// 決策來源：**已驗證的 scenario identity**（`resolveScenario(env.scenario).decision`）
// 經 `protocolDecisionFor()` 明確映射成協定層的 allow／deny，再與落地 fixture
// 交叉核對；**不從待驗的 MCP／audit／DOM 回填**。
//
// 明確不做的事：
//   - **不使用 F1b 的 Go broker helper**（那是 F1b 的 App/UI 替身）
//   - **不由測試直接呼叫 ResolveApproval**——決策只能來自 UI 點擊
//   - 不執行 approval input 裡供核可的命令（它只是展示資料）
//   - deny 案只證明 App／MCP／UI 把「使用者拒絕」正確傳遞，**不證明**真實
//     Claude 收到 deny 之後不會執行該命令——那是 provider 行為，本案沒有驗
import { expect, test } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
import { captureChromeArgv } from '../support/chromeArgv.js';
import { readClaudeScenarioRunEnv } from '../support/env.js';
import { installNetworkGuard } from '../support/networkGuard.js';
import { waitForCliReady } from '../support/cliInfo.js';
import { judgeClaudeUiConsistency, parseAuditLines, selectBrokerAuditForApproval }
  from '../support/scenario/claudeAppEvidence.js';
import {
  buildClaudeApprovalExpectation, buildClaudeDenyExpectation, protocolDecisionFor,
  type ClaudeApprovalExpectation,
} from '../support/scenario/claudeApprovalProtocol.js';
import { resolveScenario } from '../support/scenario/scenarios.js';

test.afterEach(async ({}, testInfo) => {
  const env = readClaudeScenarioRunEnv();
  // fixture 會在 teardown 刪除；先保存完整 audit，讓失敗也能事後複核。
  try {
    fs.copyFileSync(path.join(env.claudeStateDir, 'audit.jsonl'),
      path.join(env.artifactsDir, 'claude-broker-audit.raw.jsonl'));
  } catch (error) {
    await testInfo.attach('audit-preservation-error', {
      body: String(error), contentType: 'text/plain',
    });
    if (testInfo.status === 'passed') {
      fs.writeFileSync(`${env.artifactsDir}/TEST_FAILED`, `audit preservation: ${String(error)}\n`);
      throw error;
    }
  }
  if (testInfo.status !== testInfo.expectedStatus) {
    const env = readClaudeScenarioRunEnv();
    fs.writeFileSync(`${env.artifactsDir}/TEST_FAILED`, `${testInfo.title}: ${testInfo.status}\n`);
  }
});

test('claude approval: 真 App 啟動 → Start → approval 顯示 → UI 依核定決策點擊 → 對應完成內容顯示', async ({ page }) => {
  const env = readClaudeScenarioRunEnv();
  const fixture = JSON.parse(fs.readFileSync(env.claudeExpectationPath, 'utf8')) as {
    approval: ClaudeApprovalExpectation; prompt: string;
    mcp: { commandPath: string; commandSha256: string; socket: { kind: string; stateDir?: string } };
  };
  const exp = fixture.approval;
  // **核定決策來自已驗證的 scenario identity**，不看待驗輸出；再與落地 fixture
  // 雙向核對——兩邊矛盾即判失敗（reviewer #403 第 3 點）。
  const scenarioDef = resolveScenario(env.scenario);
  const decision = protocolDecisionFor(scenarioDef.decision);
  expect(exp.decision, '落地 fixture 的 decision 必須等於 scenario identity 映射出的決策').toBe(decision);
  const expectedFromBuilder = decision === 'deny'
    ? buildClaudeDenyExpectation(env.runId)
    : buildClaudeApprovalExpectation(env.runId);
  expect(exp, '落地 fixture 的 approval 期望應逐欄等於受版控 builder 的輸出').toEqual(expectedFromBuilder);
  if (decision === 'deny') {
    expect(exp.denyReason, 'deny 案必須有 run 專屬的核定理由').toBeTruthy();
    expect(exp.denyReason ?? '', '核定理由不得含 fail closed 字樣').not.toContain('fail closed');
  } else {
    expect(exp.denyReason, 'allow 案不得帶 denyReason').toBeUndefined();
  }
  test.info().annotations.push({ type: 'scenario', description: `${env.scenario} (claude approval ${decision})` });

  await installNetworkGuard(page.context(), env.artifactsDir);
  await page.goto(env.baseUrl);
  captureChromeArgv(env.artifactsDir);

  // --- CLIInfo 隔離 ---------------------------------------------------------
  const info = await waitForCliReady(page, 60_000);
  expect(info.toolsSource, 'toolsSource 應為 env').toBe('env');
  expect(info.workspaceSource, 'workspaceSource 應為 env').toBe('env');
  expect(info.claudeVersion, '假 claude 版本應等於本次執行專屬字串').toBe(env.claudeVersion);
  // tripwire 的 codex --version 計數只證明「有被呼叫」，不能代替值比對（reviewer #375）。
  expect(info.codexVersion, '假 codex 版本應等於本次執行專屬字串').toBe(env.codexVersion);
  expect(info.startupError, 'startupError 應為空').toBe('');
  // 本次 CLIInfo 回傳存成證據，與上述各項檢查一起保留。
  fs.writeFileSync(path.join(env.artifactsDir, 'cliinfo.json'), `${JSON.stringify(info, null, 2)}\n`);
  expect(fs.realpathSync(info.workspace), 'CLIInfo.workspace canonical path 應等於本次 fixture')
    .toBe(fs.realpathSync(env.workspaceDir));
  expect(fs.realpathSync(info.toolsDir), 'CLIInfo.toolsDir canonical path 應等於本次 toolsDir')
    .toBe(fs.realpathSync(env.toolsDir));

  // --- Start：真 StartSession(provider=Claude) -------------------------------
  await page.locator('[data-test="create-claude"]').first().click();
  const composer = page.locator('[data-test="composer"]');
  await composer.waitFor({ state: 'visible', timeout: 15_000 });
  const textarea = page.locator('[data-test="composer-textarea"]');
  await textarea.click();
  // prompt 必須與獨立 fixture 一致——假 CLI 會逐字比對 stdin 首行。
  await textarea.fill(fixture.prompt);
  await page.locator('[data-test="composer-send"]').click();

  // --- UI 看見 approval（以 DOM 為準，不讀事件接收器內部狀態） --------------
  const dialog = page.locator('[data-test="approval-dialog"]');
  await dialog.waitFor({ state: 'visible', timeout: 60_000 });
  await expect(dialog, 'approval dialog 應顯示 claude provider').toContainText('claude');
  await page.screenshot({ path: `${env.artifactsDir}/ui-claude-approval-visible.png` });

  const domWsid = await dialog.getAttribute('data-test-wsid');
  const domApprovalId = await dialog.getAttribute('data-test-approval-id');
  expect(domWsid, 'DOM 應帶 data-test-wsid').toBeTruthy();
  expect(domApprovalId, 'DOM 應帶 data-test-approval-id').toBeTruthy();

  // DOM 上的 tool／input marker 必須對得上本次 runId 綁定的獨立 fixture
  const rawParamsText = await page.locator('[data-test="approval-raw-params"]').textContent();
  expect(rawParamsText ?? '', 'DOM raw params 應含本次 runId 的 input marker').toContain(exp.inputMarker);
  await expect(dialog, 'DOM 應顯示本次的 tool 名稱').toContainText(exp.toolName);

  // --- UI 決策（**不是**直接呼叫 ResolveApproval） --------------------------
  // 兩顆按鈕都必須在場：這樣「點了哪一顆」才是真的選擇，而不是只剩一個可點。
  await expect(page.locator('[data-test="approval-allow"]'), 'allow 按鈕應存在').toHaveCount(1);
  await expect(page.locator('[data-test="approval-deny"]'), 'deny 按鈕應存在').toHaveCount(1);
  if (decision === 'deny') {
    // reason 輸入框是 ApprovalDialog.vue 既有節點（`v-model="reason"`，無
    // data-test 屬性），這裡用**既有 locator** 定位，未新增 production selector。
    const reasonInput = dialog.locator('input');
    await expect(reasonInput, 'dialog 內應恰有一個 reason 輸入框').toHaveCount(1);
    await reasonInput.fill(exp.denyReason ?? '');
    await expect(reasonInput, 'reason 輸入框應已填入本次核定理由').toHaveValue(exp.denyReason ?? '');
    const actionPath = path.join(env.artifactsDir, 'claude-ui-action.json');
    const action = { domWsid, domApprovalId, rawParamsText, decision,
      observedReason: await reasonInput.inputValue(),
      button: 'approval-deny', clickResolved: false };
    fs.writeFileSync(actionPath, `${JSON.stringify(action, null, 2)}\n`);
    await page.screenshot({ path: `${env.artifactsDir}/ui-claude-deny-ready.png` });
    await page.locator('[data-test="approval-deny"]').click();
    // clickResolved 只表示 Playwright 的點擊操作完成；決策仍由 MCP/audit 核對。
    fs.writeFileSync(actionPath, `${JSON.stringify({ ...action, clickResolved: true }, null, 2)}\n`);
    await expect(dialog, 'deny 之後 approval dialog 應收掉').toBeHidden({ timeout: 30_000 });
  } else {
    await page.locator('[data-test="approval-allow"]').click();
    await expect(dialog, 'allow 之後 approval dialog 應收掉').toBeHidden({ timeout: 30_000 });
  }

  // --- 假 CLI 的預定完成內容出現在 UI --------------------------------------
  const ownerPane = page.locator(`[data-test-wsid="${domWsid}"]`);
  await expect(ownerPane, '應有一個對應 WSID 的 pane').toHaveCount(1);
  await expect(ownerPane.locator('.bubble.assistant', { hasText: exp.completionText }),
    '完成內容應出現在對應 session 的 assistant 氣泡').toHaveCount(1, { timeout: 60_000 });
  await page.screenshot({ path: `${env.artifactsDir}/ui-claude-completion.png` });

  // --- 證據：CLI 側 transcript ---------------------------------------------
  const transcriptPath = path.join(env.claudeEvidenceDir, 'mcp-transcript.json');
  expect(fs.existsSync(transcriptPath), `假 CLI 應留下 MCP transcript：${transcriptPath}`).toBe(true);
  const transcript = JSON.parse(fs.readFileSync(transcriptPath, 'utf8')) as unknown;
  const cfgPathSeen = fs.readFileSync(path.join(env.claudeEvidenceDir, 'mcp-config.path.txt'), 'utf8').trim();
  // 假 CLI 消費的必須是**真 App 產生的** config（<stateDir>/mcp-<WSID>.json）
  expect(cfgPathSeen.startsWith(`${env.claudeStateDir}${path.sep}`),
    `config 路徑應落在 App stateDir 之下：${cfgPathSeen}`).toBe(true);

  // --- 證據：真 App 的 broker audit ----------------------------------------
  const auditPath = path.join(env.claudeStateDir, 'audit.jsonl');
  expect(fs.existsSync(auditPath), `App 應寫出 audit.jsonl：${auditPath}`).toBe(true);
  const parsed = parseAuditLines(fs.readFileSync(auditPath, 'utf8'));
  expect(parsed.unparsable, 'audit.jsonl 不應有無法解析的行').toEqual([]);
  const brokerAudit = selectBrokerAuditForApproval(parsed.records, domApprovalId ?? '');
  fs.writeFileSync(path.join(env.artifactsDir, 'claude-broker-audit.json'),
    `${JSON.stringify(brokerAudit, null, 2)}\n`);

  // --- 三方一致（transcript ↔ App audit ↔ UI observed id） ------------------
  const judged = judgeClaudeUiConsistency({
    transcript, brokerAudit, domWsid, domApprovalId, mcpConfigPath: cfgPathSeen, exp,
  });
  fs.writeFileSync(path.join(env.artifactsDir, 'claude-ui-judgement.json'),
    `${JSON.stringify(judged, null, 2)}\n`);
  expect(judged.violations, '三方一致性與協定判定不應有違規').toEqual([]);
  expect(judged.agreedApprovalId, '三方應同意同一個非空 approval id').toBeTruthy();
  expect(judged.configWsid, 'config 檔名的 WSID 應等於 DOM 的 wsid').toBe(domWsid);
});
