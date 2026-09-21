// B3a-2b-2 F2：單一 Claude fresh-start approval allow 的 App/UI 整合檢查點。
//
// 目標流程（每一段都必須是真的，不得以替身跳過）：
//   真 App StartSession(provider=Claude) → 受控假 Claude CLI → **真 App 產生的**
//   MCP config → 真 mcp-approval 子程序 → 真 App approval.Broker／pumpApprovals
//   → 瀏覽器看見 approval、按 allow → 真 ResolveApproval → MCP allow
//   → 假 CLI 送預定完成內容
//
// 明確不做的事：
//   - **不使用 F1b 的 Go broker helper**（那是 F1b 的 App/UI 替身）
//   - **不由測試直接呼叫 ResolveApproval**——決策只能來自 UI 點擊
//   - 不執行 approval input 裡供核可的命令（它只是展示資料）
import { expect, test } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
import { captureChromeArgv } from '../support/chromeArgv.js';
import { readClaudeScenarioRunEnv } from '../support/env.js';
import { installNetworkGuard } from '../support/networkGuard.js';
import { waitForCliReady } from '../support/cliInfo.js';
import { judgeClaudeUiConsistency, parseAuditLines, selectBrokerAuditForApproval }
  from '../support/scenario/claudeAppEvidence.js';
import type { ClaudeApprovalExpectation } from '../support/scenario/claudeApprovalProtocol.js';

test.afterEach(async ({}, testInfo) => {
  if (testInfo.status !== testInfo.expectedStatus) {
    const env = readClaudeScenarioRunEnv();
    fs.writeFileSync(`${env.artifactsDir}/TEST_FAILED`, `${testInfo.title}: ${testInfo.status}\n`);
  }
});

test('claude approval allow: 真 App 啟動 → Start → approval 顯示 → UI allow → 完成內容顯示', async ({ page }) => {
  const env = readClaudeScenarioRunEnv();
  const fixture = JSON.parse(fs.readFileSync(env.claudeExpectationPath, 'utf8')) as {
    approval: ClaudeApprovalExpectation; prompt: string;
    mcp: { commandPath: string; commandSha256: string; socket: { kind: string; stateDir?: string } };
  };
  const exp = fixture.approval;
  test.info().annotations.push({ type: 'scenario', description: `${env.scenario} (claude approval allow)` });

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

  // --- UI allow（**不是**直接呼叫 ResolveApproval） -------------------------
  await page.locator('[data-test="approval-allow"]').click();
  await expect(dialog, 'allow 之後 approval dialog 應收掉').toBeHidden({ timeout: 30_000 });

  // --- 假 CLI 的預定完成內容出現在 UI --------------------------------------
  await expect(page.locator('body'), '完成內容應出現在 UI')
    .toContainText(exp.completionText, { timeout: 60_000 });
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
