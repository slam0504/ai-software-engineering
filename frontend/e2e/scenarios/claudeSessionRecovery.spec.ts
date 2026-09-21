// B3a-2b-2 E1：Claude 最小 recovery 檢查點——同一個 App、同一個 WSID／canonical
// cwd，第一輪 fresh start、第二輪由真 App 帶 `--resume S` 續聊，兩輪都 allow。
//
// 目標流程（每一段都必須是真的，不得以替身跳過）：
//   真 App StartSession(provider=Claude) → 受控假 Claude CLI（第一輪 fresh）
//   → 真 App 產生的 MCP config → 真 mcp-approval 子程序 → 真 broker／pumpApprovals
//   → 瀏覽器看見 approval、按 allow → 真 ResolveApproval → 第一輪完成內容
//   → 第一輪 CLI／MCP 自然退出（以 OS 觀測確認）、同一 WSID 的 pane 可觀察為 inactive
//   → 真的 end-session 點擊，**並等待這次點擊自己的操作結果**
//   → 同一個 pane 再次送出 → 新的假 CLI 帶 `--resume S`
//   → 第二輪 approval allow → 第二輪（不同）完成內容
//
// 明確不做、也不得被誤讀的事：
//   - **不直接呼叫 StartSession／EndSession／ResolveApproval**，一律真點擊。
//   - 不改 store／registry，不用 Go helper 取代 UI 決議。
//   - 第一輪的 CLI 送完 assistant／result 之後會解除訊號監聽並自然退出
//     （fakeClaudeCli.ts cliMain 結尾），**不是**等 UI End 才結束。因此這裡的
//     end-session 點擊在時序上可能是冪等操作，本案**不宣稱**它證明了
//     「End 能終止存活中的 CLI」——那是另一個未涵蓋的命題。但它**必須真的
//     成功**：本檔等待這次點擊產生的操作結果，失敗即判定失敗（reviewer #393 P1-3；
//     修前是 click 之後立刻比對一個點擊前就已經是 false 的屬性，等於沒等）。
//   - 期望值一律來自受版控的 `buildClaudeRecoveryExpectation(runId)`，
//     不從第二輪的待驗輸出反填。
//
// 取證時點（reviewer #393 P1-4）：第二輪的 init 會**重新** Bind／SetResume 同一個
// S，所以「最終值等於 S」證明不了第一輪已經落地。兩份 registry 與 audit 因此在
// **第一輪完成、第二輪啟動之前**先保存一份原始 JSON 並核對，第二輪之後再獨立保存
// 核對一次。原始檔位於 `<workspace>/.workbench`，而 globalTeardown 會刪整個
// fixture 目錄，所以一律複製進 artifacts；afterEach 另有盡力而為的保存，失敗只記
// 錄、不妨礙既有的 bounded teardown。
import { expect, test } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
import { captureChromeArgv } from '../support/chromeArgv.js';
import { readClaudeScenarioRunEnv } from '../support/env.js';
import { installNetworkGuard } from '../support/networkGuard.js';
import { waitForCliReady } from '../support/cliInfo.js';
import { parseAuditLines, selectBrokerAuditForApproval } from '../support/scenario/claudeAppEvidence.js';
import { buildClaudeRecoveryExpectation } from '../support/scenario/claudeApprovalProtocol.js';
import { probeChildOs } from '../support/scenario/fakeClaudeCli.js';
import {
  judgeClaudeRecovery, readClaudeRegistryBinding, readWorkspaceResume,
  type ClaudeRoundEvidence,
} from '../support/scenario/claudeRecoveryJudge.js';

/** App 端會被 teardown 連同 fixture 一起刪掉的原始檔。 */
const STATE_FILES = ['audit.jsonl', 'sessions.json', 'workspace-sessions.json'] as const;

/** 把 stateDir 的原始檔原樣複製進 artifacts 的某個階段目錄；回傳實際複製的檔名。 */
function preserveStateFiles(stateDir: string, destDir: string): string[] {
  fs.mkdirSync(destDir, { recursive: true });
  const copied: string[] = [];
  for (const f of STATE_FILES) {
    const src = path.join(stateDir, f);
    try {
      if (!fs.existsSync(src)) continue;
      fs.copyFileSync(src, path.join(destDir, f));
      copied.push(f);
    } catch { /* 盡力而為：保存失敗不得中斷判定，由呼叫端另行斷言必要檔案 */ }
  }
  return copied;
}

test.afterEach(async ({}, testInfo) => {
  const env = readClaudeScenarioRunEnv();
  if (testInfo.status !== testInfo.expectedStatus) {
    fs.writeFileSync(`${env.artifactsDir}/TEST_FAILED`, `${testInfo.title}: ${testInfo.status}\n`);
  }
  // 失敗路徑也要留下 App 端原始檔——globalTeardown 會刪整個 fixture。
  // 全程 try/catch：保存失敗只寫診斷，不得妨礙既有的 bounded teardown。
  try {
    const dest = path.join(env.artifactsDir, 'claude-recovery', 'final');
    const copied = preserveStateFiles(env.claudeStateDir, dest);
    fs.writeFileSync(path.join(dest, 'preserved.json'),
      `${JSON.stringify({ copied, status: testInfo.status }, null, 2)}\n`);
  } catch (e) {
    try { fs.appendFileSync(`${env.artifactsDir}/harness-spec-notes.log`,
      `preserveStateFiles(final) 失敗：${String(e)}\n`); } catch { /* 盡力而為 */ }
  }
});

test('claude session recovery: round1 fresh → 真 end-session → 同一 pane 再送出 → round2 --resume S → 兩輪都 allow', async ({ page }) => {
  const env = readClaudeScenarioRunEnv();
  // **期望來自受版控 builder**；落地 fixture 只是交叉核對 setup 有沒有寫對。
  const expectation = buildClaudeRecoveryExpectation(env.runId);
  const landed = JSON.parse(fs.readFileSync(env.claudeExpectationPath, 'utf8')) as { rounds?: unknown };
  expect(landed.rounds, '落地 fixture 的兩輪期望應等於受版控 builder 的輸出')
    .toEqual(expectation.rounds);
  const S = expectation.sessionId;
  const r1 = expectation.rounds[0];
  const r2 = expectation.rounds[1];
  const roundDir = env.claudeRoundDir;
  const evidenceDir = env.claudeEvidenceDir;
  const evidenceOut = path.join(env.artifactsDir, 'claude-recovery');
  fs.mkdirSync(evidenceOut, { recursive: true });
  // 先保存每次 OS 觀測再斷言，逾時時也保留 present/error 的原始結果。
  const observeExit = (round: number, role: 'cli' | 'mcp', pid: unknown): string => {
    expect(typeof pid === 'number' && Number.isInteger(pid) && pid > 0,
      `round${round} ${role} 的 pid 必須是正整數`).toBe(true);
    const observation = probeChildOs(pid as number);
    fs.appendFileSync(path.join(evidenceOut, 'exit-observations.jsonl'),
      `${JSON.stringify({ observedAt: new Date().toISOString(), round, role, pid, observation })}\n`);
    return observation.state;
  };
  test.info().annotations.push({ type: 'scenario', description: `${env.scenario} (claude recovery)` });

  await installNetworkGuard(page.context(), env.artifactsDir);
  await page.goto(env.baseUrl);
  captureChromeArgv(env.artifactsDir);

  // --- CLIInfo 隔離 ---------------------------------------------------------
  const info = await waitForCliReady(page, 60_000);
  expect(info.toolsSource, 'toolsSource 應為 env').toBe('env');
  expect(info.workspaceSource, 'workspaceSource 應為 env').toBe('env');
  expect(info.claudeVersion, '假 claude 版本應等於本次執行專屬字串').toBe(env.claudeVersion);
  expect(info.codexVersion, '假 codex 版本應等於本次執行專屬字串').toBe(env.codexVersion);
  expect(info.startupError, 'startupError 應為空').toBe('');
  fs.writeFileSync(path.join(env.artifactsDir, 'cliinfo.json'), `${JSON.stringify(info, null, 2)}\n`);
  expect(fs.realpathSync(info.workspace), 'CLIInfo.workspace canonical path 應等於本次 fixture')
    .toBe(fs.realpathSync(env.workspaceDir));
  expect(fs.realpathSync(info.toolsDir), 'CLIInfo.toolsDir canonical path 應等於本次 toolsDir')
    .toBe(fs.realpathSync(env.toolsDir));

  const dialog = page.locator('[data-test="approval-dialog"]');
  /** 每一輪的 DOM 觀測值都原樣保存（不只留最後解析出的幾個欄位）。 */
  const domObservations: Array<Record<string, unknown>> = [];
  const persistDomObservations = (): void => {
    fs.writeFileSync(path.join(evidenceOut, 'dom-observations.json'),
      `${JSON.stringify(domObservations, null, 2)}\n`);
  };

  // --- Round 1：真 StartSession(provider=Claude) ------------------------------
  await page.locator('[data-test="create-claude"]').first().click();
  const composer = page.locator('[data-test="composer"]');
  await composer.waitFor({ state: 'visible', timeout: 15_000 });
  const textarea = page.locator('[data-test="composer-textarea"]');
  await textarea.click();
  await textarea.fill(r1.prompt);
  await page.locator('[data-test="composer-send"]').click();

  await dialog.waitFor({ state: 'visible', timeout: 60_000 });
  await expect(dialog, 'approval dialog 應顯示 claude provider').toContainText('claude');
  const domWsid = await dialog.getAttribute('data-test-wsid');
  const domApprovalId1 = await dialog.getAttribute('data-test-approval-id');
  const dialogText1 = await dialog.textContent();
  expect(domWsid, 'DOM 應帶 data-test-wsid').toBeTruthy();
  expect(domApprovalId1, 'DOM 應帶 data-test-approval-id').toBeTruthy();
  const rawParams1 = await page.locator('[data-test="approval-raw-params"]').textContent();
  domObservations.push({ round: 1, domWsid, domApprovalId: domApprovalId1, rawParams: rawParams1, dialogText: dialogText1 });
  persistDomObservations();
  expect(rawParams1 ?? '', 'round1 的 DOM raw params 應含第一輪的 input marker').toContain(r1.approval.inputMarker);
  expect(rawParams1 ?? '', 'round1 的 DOM raw params **不得**含第二輪的 marker')
    .not.toContain(r2.approval.inputMarker);
  await page.screenshot({ path: `${env.artifactsDir}/ui-claude-r1-approval.png` });

  await page.locator('[data-test="approval-allow"]').click();
  await expect(dialog, 'allow 之後 approval dialog 應收掉').toBeHidden({ timeout: 30_000 });

  // 完成內容必須出現在**同一個 WSID 的 pane 的 assistant 氣泡**，不是整頁 body。
  const ownerPane = page.locator(`[data-test-wsid="${domWsid}"]`);
  await expect(ownerPane, `應存在 data-test-wsid=${domWsid} 的 pane`).toHaveCount(1);
  await expect(
    ownerPane.locator('.bubble.assistant', { hasText: r1.approval.completionText }),
    'round1 核可後，同一個 WSID 的 pane 內應顯示第一輪的 assistant 內容',
  ).toHaveCount(1, { timeout: 60_000 });
  // **第二輪的內容此時必須還不存在**——否則後面看到它也證明不了是第二輪產生的。
  await expect(
    ownerPane.locator('.bubble.assistant', { hasText: r2.approval.completionText }),
    'round2 的內容在第二輪開始之前必須不存在',
  ).toHaveCount(0);
  await page.screenshot({ path: `${env.artifactsDir}/ui-claude-r1-completion.png` });

  // --- 第一輪必須真的收完：協定完成標記 ＋ CLI／MCP 的 OS 退出觀測 -----------
  // done.json 是 CLI 在**送出 result／退出之前**寫的，只代表協定處理完成，
  // **不是 OS 層已退出的證據**（reviewer #393 P1-4）。退出必須另外觀測。
  await expect
    .poll(() => fs.existsSync(path.join(roundDir, 'round-1', 'done.json')),
      { timeout: 30_000, message: '第一輪應留下協定處理完成標記' })
    .toBe(true);
  const round1Record = JSON.parse(fs.readFileSync(path.join(evidenceDir, 'round-1', 'round.json'), 'utf8')) as
    { pid?: unknown };
  expect(typeof round1Record.pid, 'round-1/round.json 應記下本輪 CLI 的 pid').toBe('number');
  const round1Pid = round1Record.pid as number;
  // **查不成 ≠ 查不到**：只有明確的 absent 才算退出；error 一律視為尚未確認。
  await expect
    .poll(() => observeExit(1, 'cli', round1Pid),
      { timeout: 30_000, message: `第一輪的假 CLI（pid=${round1Pid}）必須在第二輪啟動前已退出` })
    .toBe('absent');
  const child1 = JSON.parse(fs.readFileSync(path.join(evidenceDir, 'round-1', 'mcp-child.json'), 'utf8')) as
    { psAfter?: { state?: string }; observedPid?: number };
  expect(child1.psAfter?.state, '第一輪的 MCP 子程序在第一輪收尾時必須已 absent').toBe('absent');
  expect(observeExit(1, 'mcp', child1.observedPid),
    '第一輪的 mcp-approval 子程序在第二輪啟動前必須已退出').toBe('absent');
  await expect(
    ownerPane,
    `同一個 data-test-wsid=${domWsid} 的 pane 應反映 data-test-active="false"（第一輪 CLI 自然退出後的既有狀態）`,
  ).toHaveAttribute('data-test-active', 'false', { timeout: 30_000 });

  // --- 第一輪的 App 端綁定：**在第二輪重新 Bind 之前**先取證並核對 -----------
  const stage1Dir = path.join(evidenceOut, 'round1');
  const copied1 = preserveStateFiles(env.claudeStateDir, stage1Dir);
  for (const f of STATE_FILES) {
    expect(copied1, `第一輪結束時應已保存 ${f}`).toContain(f);
  }
  const binding1 = readClaudeRegistryBinding(fs.readFileSync(path.join(stage1Dir, 'sessions.json'), 'utf8'), S);
  const resume1 = readWorkspaceResume(fs.readFileSync(path.join(stage1Dir, 'workspace-sessions.json'), 'utf8'), domWsid ?? '');
  expect(resume1, '第一輪結束時，workspace registry 的 resume_session_id 就必須已是 S').toBe(S);
  expect(binding1, '第一輪結束時，claude registry 就必須已綁定 S').not.toBeNull();
  expect(binding1?.wsid, '第一輪的 registry 綁定 WSID 必須等於 UI 觀察到的').toBe(domWsid);
  expect(binding1?.cwd, '第一輪的 registry 綁定 cwd 必須等於 canonical workspace')
    .toBe(fs.realpathSync(env.workspaceDir));

  // --- 真的 end-session：**等待這次點擊自己的操作結果** ----------------------
  // SettingsBar.call()：成功 → s.note(...)（Timeline 的 .row.note）；
  // 失敗 → s.pushError(...)（.row.stream_error，訊息含「結束對話失敗」）。
  const opNotes = page.locator('.timeline .row.note');
  const opFailures = page.locator('.timeline .row', { hasText: '結束對話失敗' });
  const notesBefore = await opNotes.count();
  expect(await opFailures.count(), '點擊前不應已存在結束對話失敗訊息').toBe(0);
  await page.locator('[data-test="end-session"]').click();
  await expect
    .poll(async () => (await opNotes.count()) > notesBefore || (await opFailures.count()) > 0,
      { timeout: 20_000, message: 'end-session 這次點擊必須產生可觀察的操作結果（成功 note 或失敗訊息）' })
    .toBe(true);
  expect(await opFailures.count(), 'end-session 必須成功；失敗即判定失敗').toBe(0);
  await expect(ownerPane, 'end-session 完成之後仍應是 inactive')
    .toHaveAttribute('data-test-active', 'false', { timeout: 20_000 });
  await page.screenshot({ path: `${env.artifactsDir}/ui-claude-after-end-session.png` });

  // --- Round 2：同一個 pane 再次送出 → 真的 resume ---------------------------
  await textarea.click();
  await textarea.fill(r2.prompt);
  await page.locator('[data-test="composer-send"]').click();

  await dialog.waitFor({ state: 'visible', timeout: 60_000 });
  const domWsid2 = await dialog.getAttribute('data-test-wsid');
  const domApprovalId2 = await dialog.getAttribute('data-test-approval-id');
  const dialogText2 = await dialog.textContent();
  expect(domWsid2, 'round2 的 approval 必須屬於同一個 WSID').toBe(domWsid);
  expect(domApprovalId2, 'round2 應有自己的 approval id').toBeTruthy();
  expect(domApprovalId2, 'round2 的 approval id 必須與 round1 不同').not.toBe(domApprovalId1);
  const rawParams2 = await page.locator('[data-test="approval-raw-params"]').textContent();
  domObservations.push({ round: 2, domWsid: domWsid2, domApprovalId: domApprovalId2, rawParams: rawParams2, dialogText: dialogText2 });
  persistDomObservations();
  expect(rawParams2 ?? '', 'round2 的 DOM raw params 應含第二輪的 input marker').toContain(r2.approval.inputMarker);
  expect(rawParams2 ?? '', 'round2 的 DOM raw params **不得**含第一輪的 marker')
    .not.toContain(r1.approval.inputMarker);
  await page.screenshot({ path: `${env.artifactsDir}/ui-claude-r2-approval.png` });

  await page.locator('[data-test="approval-allow"]').click();
  await expect(dialog, 'round2 allow 之後 approval dialog 應收掉').toBeHidden({ timeout: 30_000 });
  await expect(
    ownerPane.locator('.bubble.assistant', { hasText: r2.approval.completionText }),
    'round2 核可後，同一個 pane 內應顯示第二輪（與第一輪不同）的 assistant 內容',
  ).toHaveCount(1, { timeout: 60_000 });
  // 第一輪內容仍在同一個 pane：兩輪並存，不是被覆蓋
  await expect(
    ownerPane.locator('.bubble.assistant', { hasText: r1.approval.completionText }),
    'round1 的內容應仍留在同一個 pane（round2 是新增，不是取代）',
  ).toHaveCount(1);
  await page.screenshot({ path: `${env.artifactsDir}/ui-claude-r2-completion.png` });

  // --- 輪次 claim：恰兩輪，沒有第三輪；第二輪也必須真的收完 ------------------
  await expect
    .poll(() => fs.existsSync(path.join(roundDir, 'round-2', 'done.json')),
      { timeout: 30_000, message: '第二輪應留下協定處理完成標記' })
    .toBe(true);
  expect(fs.existsSync(path.join(roundDir, 'round-3')),
    '不得出現第三次對話啟動的 claim').toBe(false);
  const round2Record = JSON.parse(fs.readFileSync(path.join(evidenceDir, 'round-2', 'round.json'), 'utf8')) as
    { pid?: unknown };
  expect(typeof round2Record.pid, 'round-2/round.json 應記下本輪 CLI 的 pid').toBe('number');
  expect(round2Record.pid, '第二輪的 CLI 必須是另一個程序').not.toBe(round1Pid);
  const child2 = JSON.parse(fs.readFileSync(path.join(evidenceDir, 'round-2', 'mcp-child.json'), 'utf8')) as
    { observedPid?: number };
  await expect
    .poll(() => observeExit(2, 'cli', round2Record.pid),
      { timeout: 30_000, message: `第二輪的假 CLI（pid=${String(round2Record.pid)}）必須已退出` })
    .toBe('absent');
  expect(observeExit(2, 'mcp', child2.observedPid),
    '第二輪的 mcp-approval 子程序必須已退出').toBe('absent');

  // --- 證據彙整（逐輪獨立讀取；不共用、不回填） ------------------------------
  const stage2Dir = path.join(evidenceOut, 'round2');
  const copied2 = preserveStateFiles(env.claudeStateDir, stage2Dir);
  for (const f of STATE_FILES) {
    expect(copied2, `第二輪結束時應已保存 ${f}`).toContain(f);
  }
  persistDomObservations();

  const auditText = fs.readFileSync(path.join(stage2Dir, 'audit.jsonl'), 'utf8');
  const parsedAudit = parseAuditLines(auditText);
  expect(parsedAudit.unparsable, 'audit.jsonl 不應有無法解析的行').toEqual([]);

  const readRound = (n: number, approvalId: string | null): ClaudeRoundEvidence => {
    const d = path.join(evidenceDir, `round-${n}`);
    const j = (f: string): unknown => JSON.parse(fs.readFileSync(path.join(d, f), 'utf8'));
    return {
      roundRecord: j('round.json'),
      initRecord: j('init.json'),
      argv: j('argv.json'),
      transcript: j('mcp-transcript.json'),
      child: j('mcp-child.json'),
      mcpConfigPath: fs.readFileSync(path.join(d, 'mcp-config.path.txt'), 'utf8').trim(),
      domApprovalId: approvalId,
      brokerAudit: selectBrokerAuditForApproval(parsedAudit.records, approvalId ?? ''),
    };
  };
  const rounds = [readRound(1, domApprovalId1), readRound(2, domApprovalId2)];

  // 第二輪之後的綁定**獨立再核對一次**（第二輪的 init 會重新 Bind／SetResume）。
  const binding2 = readClaudeRegistryBinding(fs.readFileSync(path.join(stage2Dir, 'sessions.json'), 'utf8'), S);
  const resume2 = readWorkspaceResume(fs.readFileSync(path.join(stage2Dir, 'workspace-sessions.json'), 'utf8'), domWsid ?? '');

  const judged = judgeClaudeRecovery({
    expectation, rounds, domWsid, appBoundResume: resume2, registryBinding: binding2,
    canonicalCwd: fs.realpathSync(env.workspaceDir),
  });
  fs.writeFileSync(path.join(evidenceOut, 'judgement.json'),
    `${JSON.stringify({
      judged,
      round1: { appBoundResume: resume1, registryBinding: binding1, cliPid: round1Pid },
      round2: { appBoundResume: resume2, registryBinding: binding2, cliPid: round2Record.pid },
      domApprovalIds: [domApprovalId1, domApprovalId2],
    }, null, 2)}\n`);
  expect(judged.violations, '兩輪 recovery 判定不應有違規').toEqual([]);
  expect(judged.agreedApprovalIds, '兩輪應各自有三方同意的 approval id')
    .toEqual([domApprovalId1, domApprovalId2]);
  expect(judged.sharedMcpConfigPath, '兩輪應共用同一份 App 產生的 mcp config')
    .toBe(path.join(env.claudeStateDir, `mcp-${domWsid}.json`));
});
