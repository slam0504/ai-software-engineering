// B3a-2b-2 Task C：Codex 單一 approval 的 browser 整合檢查點——第一案
// `commandExecution-allow`。
//
// 真 Wails／App 正式啟動（globalSetup.scenario.ts spawn 真的 `wails dev`）→
// 真瀏覽器操作 Start（建立 codex session → 送出第一則訊息）→ UI 顯示
// approval（ApprovalDialog，真的 `approval:request` 事件、真的
// `window.go.main.App.ResolveApproval` binding）→ 點 allow → 新內容顯示
// （assistant 訊息泡泡）。
//
// 絕對禁止（reviewer 明訂，逐項對齊）：
//   - 不在 browser 注入 fake bindings——全程只用真的 `window.go.main.App.*`。
//   - 不直接呼叫 ResolveApproval 取代按鈕點擊——本檔只用
//     `page.locator('[data-test="approval-allow"]').click()`。
//   - 不沿用 codexHostOverride——App 走 `a.ensureAppServer()` 真正 spawn
//     `a.codexCLIPath()`（見 support/scenario/scenarioCli.ts 的裁定說明）。
//   - App 事件接收器不算 UI 證據——approval 是否出現一律以 DOM
//     （`[data-test="approval-dialog"]`）為準，不是讀 EventsOn 的 payload。
//
// B3a-2b-2 驗收缺口修正（codex-reviewer 複核裁定，逐條對齊，見下方對應段落）：
//   缺口 1：WSID 三段串接核對——DOM（ApprovalDialog／PaneView 的
//     data-test-wsid）↔ App 端 audit.jsonl／workspace-sessions.json ↔ 原始
//     wire（scenario-wire.log）。approval DOM 額外核對 method 與
//     thread/turn/item；新內容泡泡限定在同一個 WSID 的 pane 內尋找；App 端
//     原始事件複製進證據目錄。
//   缺口 2：CLIInfo `info.workspace`／`info.toolsDir` 與
//     `env.workspaceDir`／`env.toolsDir` 的 canonical path（realpath）核對。
//   缺口 3：完整 protocol 判定（scenarioProtocolJudge.ts）＋manifest／
//     scenario-config／run identity 交叉核對；`String(id)` 寬鬆比對移除；
//     manifest 輪詢只容忍「檔案尚未出現」，其餘錯誤直接失敗。
import { expect, test } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
import { captureChromeArgv } from '../support/chromeArgv.js';
import { readScenarioRunEnv } from '../support/env.js';
import { getInMemoryGuardState, installNetworkGuard } from '../support/networkGuard.js';
import { waitForCliReady } from '../support/cliInfo.js';
import { parseManifest, parseRunLog, judgeApproval } from '../support/scenario/verify.js';
import { judgeFullProtocol, judgeRunIdentity } from '../support/scenario/scenarioProtocolJudge.js';
import { resolveScenario } from '../support/scenario/scenarios.js';
import type { ScenarioConfig } from '../support/scenario/protocol.js';

test.afterEach(async ({}, testInfo) => {
  if (testInfo.status !== testInfo.expectedStatus) {
    const env = readScenarioRunEnv();
    fs.writeFileSync(`${env.artifactsDir}/TEST_FAILED`, `${testInfo.title}: ${testInfo.status}\n`);
  }
});

test('codex commandExecution-allow: 真 App 啟動 → Start → approval 顯示 → allow → 新內容顯示', async ({ page }) => {
  const env = readScenarioRunEnv();

  await installNetworkGuard(page.context(), env.artifactsDir);
  await page.goto(env.baseUrl);
  captureChromeArgv(env.artifactsDir);

  const info = await waitForCliReady(page, 60_000);
  expect(info.toolsSource, 'toolsSource 應為 env').toBe('env');
  expect(info.workspaceSource, 'workspaceSource 應為 env').toBe('env');
  expect(info.codexVersion, '假 codex 版本應等於本次執行的 scenario 專屬字串').toBe(env.codexVersion);
  expect(info.claudeVersion, '假 claude 版本應等於本次執行專屬字串').toBe(env.claudeVersion);
  expect(info.startupError, 'startupError 應為空').toBe('');

  // 缺口 2：`toolsSource`/`workspaceSource === 'env'` 只證明「App 認為它讀了
  // env」，不足以證明讀到的**是本次執行準備的那個目錄**（例如另一個殘留的
  // env 也能讓 source 顯示 'env'）。這裡額外核對 canonical path（realpath）
  // 完全相等——不能只信 source 標籤。
  const infoWorkspaceReal = fs.realpathSync(info.workspace);
  const envWorkspaceReal = fs.realpathSync(env.workspaceDir);
  expect(infoWorkspaceReal, `CLIInfo.workspace 的 canonical path 應等於 env.workspaceDir（${env.workspaceDir}）`).toBe(envWorkspaceReal);
  const infoToolsReal = fs.realpathSync(info.toolsDir);
  const envToolsReal = fs.realpathSync(env.toolsDir);
  expect(infoToolsReal, `CLIInfo.toolsDir 的 canonical path 應等於 env.toolsDir（${env.toolsDir}）`).toBe(envToolsReal);

  // Start：建立一個 codex session（左欄 SessionList「create-codex」），把它
  // 釘進 focused pane，composer 才會出現（見 PaneView.vue `v-if="focused"`）。
  await page.locator('[data-test="create-codex"]').first().click();

  const composer = page.locator('[data-test="composer"]');
  await composer.waitFor({ state: 'visible', timeout: 15_000 });

  const prompt = `b3a2b2-prompt-${env.runId}`;
  const textarea = page.locator('[data-test="composer-textarea"]');
  await textarea.click();
  await textarea.fill(prompt);

  // 送出第一則訊息＝真正的 StartSession（app.go），觸發
  // ensureAppServer()→codex.StartAppServer(a.codexCLIPath())→真的
  // spawn scenario codex CLI→fakeAppServer.ts 的 initialize/thread-start/
  // turn-start/commandExecution requestApproval 整條 wire。
  await page.locator('[data-test="composer-send"]').click();

  // UI 顯示 approval：真的 `approval:request` 事件驅動 ApprovalDialog 出現，
  // 不是讀 App 事件接收器內部狀態——以 DOM 可見性為準。
  const dialog = page.locator('[data-test="approval-dialog"]');
  await dialog.waitFor({ state: 'visible', timeout: 30_000 });
  await expect(dialog, 'approval dialog 應顯示 codex provider').toContainText('codex');
  await page.screenshot({ path: `${env.artifactsDir}/ui-approval-dialog-visible.png` });

  // 缺口 1：WSID 三段串接的第一段——UI 實際顯示的 WSID／approval id／
  // method（ApprovalDialog.vue 新增的純測試用 data-test-* 屬性，既有欄位，
  // 不是新造的假欄位）。approval DOM 也核對 method 與 thread/turn/item（後者
  // 從既有的 `<pre data-test="approval-raw-params">` 內容解析，那份內容本來
  // 就是 codexApproval() 送給 UI 的 raw params 原文，未經測試碼改造）。
  const domWsid = await dialog.getAttribute('data-test-wsid');
  const domApprovalId = await dialog.getAttribute('data-test-approval-id');
  const domMethod = await dialog.getAttribute('data-test-approval-method');
  expect(domWsid, 'ApprovalDialog 應顯示非空 WSID').toBeTruthy();
  expect(domApprovalId, 'ApprovalDialog 應顯示非空 approval id').toBeTruthy();
  expect(domMethod, 'ApprovalDialog 顯示的 method 應等於本次 scenario 的 approvalMethod').toBe(env.scenarioApprovalMethod);

  const rawParamsText = await page.locator('[data-test="approval-raw-params"]').textContent();
  expect(rawParamsText, 'approval-raw-params 不應為空').toBeTruthy();
  const rawParams = JSON.parse(rawParamsText ?? '{}') as { threadId?: unknown; turnId?: unknown; itemId?: unknown };
  expect(rawParams.threadId, 'approval DOM 顯示的 raw params.threadId 應等於本次 scenario threadId').toBe(env.scenarioThreadId);
  expect(rawParams.turnId, 'approval DOM 顯示的 raw params.turnId 應等於本次 scenario turnId').toBe(env.scenarioTurnId);
  expect(rawParams.itemId, 'approval DOM 顯示的 raw params.itemId 應等於本次 scenario itemId').toBe(env.scenarioItemId);

  // 點 allow：真的按鈕點擊，走 ApprovalDialog.vue 的
  // `ResolveApproval(r.id, true, reason)` wails binding，不是測試直接呼叫。
  await page.locator('[data-test="approval-allow"]').click();

  await expect(dialog, 'approval 解決後 dialog 應關閉').toHaveCount(0, { timeout: 15_000 });

  // 新內容顯示：fakeAppServer.ts 在收到 decision 之後送出的
  // item/completed（agentMessage）應該映射成一則 assistant 訊息泡泡。
  //
  // 缺口 1：搜尋範圍限定在「顯示 domWsid 的那個 pane」內
  // （PaneView.vue 新增的 `data-test-wsid`），不是全頁搜尋任何一個符合文字
  // 的泡泡——否則跨 pane／跨 session 的泡泡混進來也會被誤判為通過。
  const expectedText = `b3a2b2-scenario-content-${env.runId}`;
  const ownerPane = page.locator(`[data-test-wsid="${domWsid}"]`);
  await expect(ownerPane, `應存在 data-test-wsid=${domWsid} 的 pane`).toHaveCount(1);
  await expect(
    ownerPane.locator('.bubble.assistant', { hasText: expectedText }),
    'approval 核可後，同一個 WSID 的 pane 內應顯示新的 assistant 內容',
  ).toBeVisible({ timeout: 30_000 });
  await page.screenshot({ path: `${env.artifactsDir}/ui-new-content-after-allow.png` });

  const guardState = getInMemoryGuardState(env.artifactsDir);
  expect(guardState.violations, 'network guard 不應攔到任何違規').toEqual([]);

  // 缺口 1（第二段）：App 端原始證據——audit.jsonl（`a.stateDir/audit.jsonl`
  // ＝ `<workspaceDir>/.workbench/audit.jsonl`，App 正式的 audit 落點，不是
  // 測試碼寫的）與 workspace-sessions.json（wsregistry 的正式落點）。
  // codexApproval() 送給 UI 的 `id` 與寫進 audit.jsonl 的 `data.id` 是
  // 同一個值（app.go：`a.audit("codex_approval_request", map[string]any{
  // "id": id, ...})` 與 `a.emit("approval:request", map[string]any{"id": id,
  // ...})` 用的是同一個區域變數），因此可以拿 DOM 讀到的 approval id 直接去
  // audit.jsonl 找對應行，建立 UI ↔ App 端證據的串接。
  const workbenchDir = path.join(env.workspaceDir, '.workbench');
  const auditPath = path.join(workbenchDir, 'audit.jsonl');
  const wsRegistryPath = path.join(workbenchDir, 'workspace-sessions.json');

  const auditRaw = fs.readFileSync(auditPath, 'utf8');
  const auditLines = auditRaw.split('\n').map(l => l.trim()).filter(l => l.length > 0);
  interface AuditEntry { ts: string; kind: string; data: Record<string, unknown> }
  const auditEntries: AuditEntry[] = auditLines.map(l => JSON.parse(l) as AuditEntry);

  const approvalRequestEntry = auditEntries.find(
    e => e.kind === 'codex_approval_request' && e.data.id === domApprovalId,
  );
  expect(approvalRequestEntry, `audit.jsonl 應有 id=${domApprovalId} 的 codex_approval_request 紀錄`).toBeTruthy();
  expect(approvalRequestEntry?.data.wsid, 'audit.jsonl 的 codex_approval_request.wsid 應等於 DOM 顯示的 WSID').toBe(domWsid);
  expect(approvalRequestEntry?.data.method, 'audit.jsonl 的 codex_approval_request.method 應等於本次 scenario approvalMethod').toBe(env.scenarioApprovalMethod);
  const auditRawParams = approvalRequestEntry?.data.raw_params as { threadId?: unknown; turnId?: unknown; itemId?: unknown } | undefined;
  expect(auditRawParams?.threadId, 'audit.jsonl 的 raw_params.threadId 應等於本次 scenario threadId').toBe(env.scenarioThreadId);
  expect(auditRawParams?.turnId, 'audit.jsonl 的 raw_params.turnId 應等於本次 scenario turnId').toBe(env.scenarioTurnId);
  expect(auditRawParams?.itemId, 'audit.jsonl 的 raw_params.itemId 應等於本次 scenario itemId').toBe(env.scenarioItemId);

  const approvalDecisionEntry = auditEntries.find(
    e => e.kind === 'codex_approval_decision' && e.data.id === domApprovalId,
  );
  expect(approvalDecisionEntry, `audit.jsonl 應有 id=${domApprovalId} 的 codex_approval_decision 紀錄`).toBeTruthy();
  expect(approvalDecisionEntry?.data.decision, 'audit.jsonl 的 codex_approval_decision.decision 應為 accept').toBe('accept');

  const wsRegistryRaw = JSON.parse(fs.readFileSync(wsRegistryPath, 'utf8')) as {
    entries?: Record<string, { wsid?: string; provider?: string }>;
  };
  const wsEntry = domWsid ? wsRegistryRaw.entries?.[domWsid] : undefined;
  expect(wsEntry, `workspace-sessions.json 應有 WSID=${domWsid} 的登記項目`).toBeTruthy();
  expect(wsEntry?.provider, 'workspace-sessions.json 登記的 provider 應為 codex').toBe('codex');

  // 保留 App 端原始事件與 wire 的必要副本進證據目錄——不能只留 fake 自己寫
  // 的 scenario-wire.log，audit.jsonl／workspace-sessions.json 是 App 正式
  // 產出的原文複本（不是摘要／改寫），供事後獨立覆核。
  fs.copyFileSync(auditPath, path.join(env.artifactsDir, 'app-audit.jsonl'));
  fs.copyFileSync(wsRegistryPath, path.join(env.artifactsDir, 'app-workspace-sessions.json'));

  // B3a-2b-2 Task C 第三輪限縮補正（證據缺漏）：audit.jsonl／
  // workspace-sessions.json 不是「App 原始 wire」——真正的原始 wire 是 codex
  // app-server 的 wirelog.Generation 錄流（`a.wireLogDir()` ＝
  // `<stateDir>/wire-logs/<id>.jsonl`，`a.stateDir` 即 `.workbench`＝
  // `workbenchDir`；見 internal/wirelog/wirelog.go 的 always-on generation。
  // 不是 app.go:7455 那個 `recorder.New`——那個只在 `startClaude`（Claude
  // session）路徑掛，是 Claude 專屬，跟本案的 codex commandExecution 無關）。
  // fixture 目錄會在 teardown 被清掉，必須在這裡（run 進行中、清理前）就
  // 複製，比照上面 audit.jsonl 的複製點；找不到就直接讓測試失敗並回報實際
  // 路徑與原因，不得把 audit 改稱 wire。
  const wireLogsDir = path.join(workbenchDir, 'wire-logs');
  const wireLogFiles = fs.existsSync(wireLogsDir)
    ? fs.readdirSync(wireLogsDir).filter(f => f.endsWith('.jsonl'))
    : [];
  expect(
    wireLogFiles.length,
    `App 端原始 wire 錄流應存在（${wireLogsDir}），找不到任何 .jsonl generation 檔——不得改用 audit.jsonl 頂替`,
  ).toBeGreaterThan(0);
  const wireLogWithMtime = wireLogFiles
    .map(f => ({ f, mtime: fs.statSync(path.join(wireLogsDir, f)).mtimeMs }))
    .sort((a, b) => b.mtime - a.mtime);
  const chosenWireLogFile = wireLogWithMtime[0].f;
  const chosenWireLogPath = path.join(wireLogsDir, chosenWireLogFile);
  fs.copyFileSync(chosenWireLogPath, path.join(env.artifactsDir, 'app-wire-log.jsonl'));
  const chosenMetaPath = chosenWireLogPath.replace(/\.jsonl$/, '.meta.json');
  if (fs.existsSync(chosenMetaPath)) {
    fs.copyFileSync(chosenMetaPath, path.join(env.artifactsDir, 'app-wire-log.meta.json'));
  }
  if (wireLogWithMtime.length > 1) {
    fs.writeFileSync(
      path.join(env.artifactsDir, 'app-wire-log-note.txt'),
      `wire-logs 目錄內有 ${wireLogWithMtime.length} 個 generation，已複製最新（mtime）一個：${chosenWireLogFile}；`
      + `其餘未複製：${wireLogWithMtime.slice(1).map(w => w.f).join('、')}`,
    );
  }

  // 核對 approval 的原始 provider ID 與 decision，與 fake log
  // （scenario-wire.log）相符——App 端 wire log 是獨立於 fake app-server 另一
  // 份原始證據，兩邊看到的應是同一組 JSON-RPC frame。
  interface AppWireRow { frame: number; dir: 'c2s' | 's2c'; wsid: string; raw: { id?: unknown; method?: string; result?: { decision?: string } } }
  const appWireEntries: AppWireRow[] = fs.readFileSync(chosenWireLogPath, 'utf8')
    .split('\n').map(l => l.trim()).filter(l => l.length > 0)
    .map(l => JSON.parse(l) as AppWireRow);
  const appApprovalRequest = appWireEntries.find(
    e => e.dir === 's2c' && e.raw.method === env.scenarioApprovalMethod && e.raw.id === env.scenarioApprovalRequestId,
  );
  expect(
    appApprovalRequest,
    `App 端原始 wire 錄流應有 id=${env.scenarioApprovalRequestId} 的 ${env.scenarioApprovalMethod} request（provider 原始 ID 核對）`,
  ).toBeTruthy();
  const appApprovalDecision = appWireEntries.find(
    e => e.dir === 'c2s' && e.raw.id === env.scenarioApprovalRequestId && e.raw.result !== undefined,
  );
  expect(appApprovalDecision, `App 端原始 wire 錄流應有 id=${env.scenarioApprovalRequestId} 的 decision response`).toBeTruthy();
  expect(
    appApprovalDecision?.raw.result?.decision,
    'App 端原始 wire 錄流記錄的 decision 應為 accept，與 fake log（scenario-wire.log）相符',
  ).toBe('accept');

  // 缺口 3（第一段）：manifest 輪詢只容忍「檔案尚未出現」（fake 收尾前，
  // manifest 檔案根本不存在），其餘任何錯誤（malformed json、欄位缺漏／型別
  // 錯誤）一律直接失敗，不再靜默吞掉繼續輪詢等到逾時才報一個語意含糊的
  // timeout。
  await expect
    .poll(() => {
      try {
        return parseManifest(env.scenarioManifestPath).exitCode;
      } catch (e) {
        const msg = e instanceof Error ? e.message : String(e);
        if (msg.includes('ENOENT')) return null; // 檔案尚未出現，繼續等待。
        throw e; // 其他錯誤（malformed／欄位缺漏）直接讓 poll 失敗，不再吞掉。
      }
    }, { message: 'scenario fake app-server 應在 turn/completed 之後乾淨收尾（exitCode=0）', timeout: 15_000 })
    .toBe(0);

  const manifest = parseManifest(env.scenarioManifestPath);
  const violations = judgeApproval(manifest, {
    requestId: env.scenarioApprovalRequestId,
    method: env.scenarioApprovalMethod,
    decision: env.scenarioDecision as 'accept' | 'decline',
  });
  expect(violations, `manifest 核對應無違規：${JSON.stringify(manifest)}`).toEqual([]);

  // 缺陷 2 修正（B3a-2b-2 Task C 第三輪限縮補正）：期望值不得從待驗資料
  // （scenarioConfigOnDisk）回填——先前這裡把同一份落地檔案同時當「待驗
  // 資料」與 `exp.cfg`（期望值），整個 disk-vs-expected 比對變成跟自己比
  // 對、恆真（controller 反例：取真跑 cfg 把 afterApproval 清空、wire 刪掉
  // item/started／item/completed，仍然 0 violations）。改成從受版控的
  // `resolveScenario(env.scenario).build(env.runId)` 取得獨立期望——純函式，
  // 只依賴 scenarios.ts 原始碼與本次 runId，不讀任何本次執行落地的檔案。
  const expectedCfg = resolveScenario(env.scenario).build(env.runId);

  // 用獨立期望核對 run-env 的所有 identity 欄位（env.scenarioXxx 系列）——
  // 這些欄位一樣可能被竄改／corrupt，不能只驗 disk config。
  expect(env.scenario, 'env.scenario 應等於獨立期望的 scenario').toBe(expectedCfg.scenario);
  expect(env.scenarioThreadId, 'env.scenarioThreadId 應等於獨立期望的 threadId').toBe(expectedCfg.threadId);
  expect(env.scenarioTurnId, 'env.scenarioTurnId 應等於獨立期望的 turnId').toBe(expectedCfg.turnId);
  expect(env.scenarioItemId, 'env.scenarioItemId 應等於獨立期望的 itemId').toBe(expectedCfg.itemId);
  expect(env.scenarioApprovalMethod, 'env.scenarioApprovalMethod 應等於獨立期望的 approvalMethod').toBe(expectedCfg.approvalMethod);
  expect(env.scenarioApprovalRequestId, 'env.scenarioApprovalRequestId 應等於獨立期望的 approvalRequestId').toBe(expectedCfg.approvalRequestId);

  // 缺口 3（第二段）：完整 protocol 判定——不是只 `find` approval
  // request／response 兩筆訊息，而是核對本案完整必要步驟序列＋方向
  // （initialize→initialized→thread/start→turn/start→requestApproval→
  // decision→afterApproval→turn/completed），任何缺失／錯序／未知
  // method／id 型別或值不符都會被抓到（見 scenarioProtocolJudge.selftest.ts
  // 的負控制：截斷、錯序、錯 ID、錯 decision、未知 method、frame 形狀）。
  // exp.cfg 用獨立期望（expectedCfg），不是待驗的 scenarioConfigOnDisk。
  const scenarioConfigOnDisk = JSON.parse(fs.readFileSync(env.scenarioConfigPath, 'utf8')) as ScenarioConfig;
  const runLog = parseRunLog(env.scenarioLogPath);
  const protocolViolations = judgeFullProtocol(runLog, { cfg: expectedCfg, decision: 'accept' });
  expect(protocolViolations, `完整 protocol 序列應無違規：${JSON.stringify(protocolViolations)}`).toEqual([]);

  // manifest／落地的 scenario-config.json（含 afterApproval）／run identity
  // 一併對上獨立期望——不得讀到跨執行殘留的證據卻被誤判為本次通過，也不得
  // 讓落地檔案本身被竄改卻因為「期望值來自同一份檔案」而恆真通過。
  const identityViolations = judgeRunIdentity(manifest, scenarioConfigOnDisk, {
    runId: env.runId,
    cfg: expectedCfg,
    decision: 'accept',
  });
  expect(identityViolations, `manifest／scenario-config／run identity 交叉核對應無違規：${JSON.stringify(identityViolations)}`).toEqual([]);
});
