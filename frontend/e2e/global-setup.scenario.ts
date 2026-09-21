// B3a-2b-2 Task C：Codex 單一 approval 的 browser 整合檢查點——scenario 專屬
// globalSetup。跟 `./global-setup.ts`（default／controls 共用）分開成獨立
// 檔案，理由（票面既有裁定）：
//   - default／controls 禁止 E2E_SCENARIO（見 global-setup.ts 開頭的守門）；
//     scenario 執行需要 E2E_SCENARIO 明確有效，兩者互斥，合在一支檔案裡會
//     讓其中一條入口的「禁止」與另一條入口的「要求」彼此打架。
//   - scenario 執行的 codex CLI 要換成 scenario 專屬版本（見
//     support/scenario/scenarioCli.ts），不是 fakeCli.ts 的 tripwire。
//
// 沿用不變的既有裁定（與 global-setup.ts 相同來源）：fixture realpath／
// 可寫／非 repo、tools 執行與版本 preflight、CLIInfo 全項核對（見 spec）、
// 34115 衝突拒絕、1 worker／0 retries（見 playwright.scenario.config.ts）、
// 網路限制與 process cleanup 契約、fixture／HOME／tools／artifacts 隔離、
// 每次執行獨立 evidence、不修改已凍結 artifact、沿用既有 proc 管理（不另做
// pgid registry，只停本次擁有的程序）。
//
// 刻意不搬過來的部分：default／controls 套件的 N 系列故障注入點（N1–N14b）
// ——那些是那兩條入口自己的負控制測試設施，本檢查點的成功條件不需要它們，
// 硬搬只會放大維護面、不會提高本票的證據品質。
import fs from 'node:fs';
import http from 'node:http';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { writeRunEnv } from './support/env.js';
import { createFixture } from './support/fixture.js';
import { HarnessLogger } from './support/logger.js';
import { NetworkSampler } from './support/networkSampler.js';
import {
  checkArtifactModeAgainstHead, checkEnvValues, checkFakeCli, checkFixture, checkPortFree, PreflightError,
} from './support/preflight.js';
import { listScopedFiles, snapshotArtifacts } from './support/artifactIntegrity.js';
import { ProcessTree } from './support/processTree.js';
import { RunStateStore } from './support/runState.js';
import { runtime } from './support/runtime.js';
import { handleStaleRun, StaleRunConflictError } from './support/staleRun.js';
import { createScenarioCodexCli } from './support/scenario/scenarioCli.js';
import { resolveScenario } from './support/scenario/scenarios.js';
import { isOfflineSandboxEnabled } from './support/offlineSandbox.js';

const WAILS_PORT = 34115;

function httpReachable(port: number): Promise<boolean> {
  return new Promise(resolve => {
    const req = http.get({ host: '127.0.0.1', port, timeout: 2000, path: '/' }, res => {
      res.resume();
      resolve(true);
    });
    req.on('error', () => resolve(false));
    req.on('timeout', () => { req.destroy(); resolve(false); });
  });
}

function sleep(ms: number): Promise<void> {
  return new Promise(r => setTimeout(r, ms));
}

export default async function globalSetupScenario(): Promise<void> {
  const runId = process.env.E2E_RUN_ID;
  const artifactsDir = process.env.E2E_ARTIFACTS_DIR;
  if (!runId || !artifactsDir) {
    throw new Error(
      'globalSetupScenario: E2E_RUN_ID／E2E_ARTIFACTS_DIR 未設定——必須透過 '
      + 'npm run test:e2e:scenario（e2e/scripts/run-e2e.mjs）啟動',
    );
  }
  const __dirname = path.dirname(fileURLToPath(import.meta.url));
  const frontendRoot = path.resolve(__dirname, '..');
  const repoRoot = path.resolve(frontendRoot, '..');
  const artifactsRoot = path.resolve(artifactsDir, '..');

  // log 必須先建好——config／preflight 失敗也要留 harness.log（票面既有
  // 裁定）；resolveScenario() 的驗證失敗本質上就是一種「啟動前失敗」，不能
  // 因為它發生在 HarnessLogger 建立之前就沒有 log 可查。
  const log = new HarnessLogger(artifactsDir);
  runtime.log = log;
  runtime.artifactsDir = artifactsDir;
  runtime.artifactsRoot = artifactsRoot;
  runtime.setupPid = process.pid;
  log.log(`globalSetupScenario 開始：runId=${runId} pid=${process.pid} repoRoot=${repoRoot}`);

  // B3a-2b-2 Task C 第三輪限縮補正（缺陷 1）：入口標記——globalTeardown 的
  // `determineExecutionMode` 用這個檔案正面判定「這次執行是從哪個入口啟動
  // 的」，不再從（可能被竄改／型別錯誤／缺漏的）scenario identity 欄位反推。
  // 寫在最開頭（任何預檢／spawn 之前），即使 resolveScenario／預檢後面失敗
  // 也留得下這份標記——這次執行確實是從 globalSetupScenario 進來的，這件事
  // 本身跟 E2E_SCENARIO 值是否有效無關。
  fs.writeFileSync(path.join(artifactsDir, 'execution-entry.json'), JSON.stringify({ entry: 'scenario' }, null, 2));

  // B3a-2b-2 Task C 驗收缺口修正（缺口 5）：`playwright.scenario.config.ts`
  // 完全沒有 `isOfflineSandboxEnabled()` 處理（default／controls 兩支
  // config 都有），求值結果是普通的 `channel='chrome'`、`executablePath=
  // null`——旗標被接受卻悄悄開一般 Chrome，跟 ProcessTree 另一邊（app 子
  // 行程）仍然套用 sandbox 的保護不一致。本檢查點範圍不含 offline sandbox
  // （最小修正，不擴充能力）：偵測到旗標就在啟動前明確拒絕、留下可診斷的
  // harness.log，不讓它悄悄降級成沒有 sandbox 的一般瀏覽器。
  if (isOfflineSandboxEnabled()) {
    const message = 'globalSetupScenario: E2E_OFFLINE_SANDBOX=1 但 scenario 入口'
      + '（playwright.scenario.config.ts）尚未支援 offline sandbox 模式——'
      + 'browser 端沒有套用 sandbox wrapper／executablePath，若放行會讓旗標'
      + '被接受卻悄悄開一般 Chrome，與 app 子行程那邊仍套用 sandbox 的保護'
      + '不一致。本檢查點範圍不含 offline sandbox（B3a-2b-2 Task C 裁定），'
      + '明確拒絕、不啟動，請不要帶這個旗標執行 npm run test:e2e:scenario。';
    log.log(message);
    throw new Error(message);
  }

  // 一個 run-id 只跑指定案：E2E_SCENARIO 必須明確有效，缺失或未知一律 throw，
  // 不回退到任何「預設案」。
  let scenarioDef: ReturnType<typeof resolveScenario>;
  try {
    scenarioDef = resolveScenario(process.env.E2E_SCENARIO);
  } catch (e) {
    log.log(`scenario 解析失敗，未啟動 app：${e instanceof Error ? e.message : String(e)}`);
    throw e;
  }
  log.log(`scenario 已解析：scenario=${scenarioDef.name}`);

  try {
    const staleOutcome = await handleStaleRun(artifactsRoot, log);
    log.log(`前次殘留檢查結果：${staleOutcome.kind}`);
  } catch (e) {
    if (e instanceof StaleRunConflictError) {
      log.log(`前次殘留身分不符，中止本次執行：${e.message}`);
    }
    throw e;
  }

  const fixture = createFixture();
  runtime.fixtureRoot = fixture.root;
  log.log(`fixture 建立完成：${fixture.root}`);

  const toolsDir = path.join(artifactsDir, 'fake-tools');
  const scenarioConfigPath = path.join(artifactsDir, 'scenario-config.json');
  const scenarioLogPath = path.join(artifactsDir, 'scenario-wire.log');
  const fakeAppServerPath = path.join(frontendRoot, 'e2e', 'support', 'scenario', 'fakeAppServer.ts');
  if (!fs.existsSync(fakeAppServerPath)) {
    throw new Error(`globalSetupScenario: 找不到 fakeAppServer.ts（${fakeAppServerPath}）——B3a-2b-1 交付是否還在？`);
  }

  const scenarioConfig = scenarioDef.build(runId);
  fs.writeFileSync(scenarioConfigPath, JSON.stringify(scenarioConfig, null, 2));

  const cli = createScenarioCodexCli(toolsDir, runId, fakeAppServerPath, scenarioConfigPath, scenarioLogPath);
  log.log(`scenario codex CLI 建立完成：${toolsDir}（scenario=${scenarioConfig.scenario}）`);

  try {
    checkFixture(fixture, repoRoot, log);
    checkFakeCli(cli, artifactsDir, log);
    checkEnvValues(fixture.root, toolsDir, log);
    checkPortFree(WAILS_PORT, log);
    checkArtifactModeAgainstHead(repoRoot, log);
  } catch (e) {
    if (e instanceof PreflightError) {
      log.log(`預檢未通過，未啟動 app：${e.message}`);
    }
    try { fs.rmSync(fixture.root, { recursive: true, force: true }); } catch { /* 盡力而為 */ }
    throw e;
  }

  const artifactScopeFiles = listScopedFiles(repoRoot);
  const artifactBaselineSnapshot = snapshotArtifacts(repoRoot, artifactScopeFiles);
  fs.writeFileSync(
    path.join(artifactsDir, 'artifact-integrity-baseline.json'),
    JSON.stringify({ files: artifactScopeFiles, snapshot: artifactBaselineSnapshot }, null, 2),
  );
  runtime.artifactBaselineSnapshot = artifactBaselineSnapshot;

  const runState = new RunStateStore(artifactsDir, artifactsRoot, runId);
  runtime.runState = runState;

  const processTree = new ProcessTree(repoRoot, artifactsDir, runState, log);
  runtime.processTree = processTree;

  const networkSampler = new NetworkSampler(artifactsDir, runState, false);
  runtime.networkSampler = networkSampler;

  installSignalHandlers(log);

  let child: ReturnType<typeof processTree.spawnWailsDev>;
  try {
    child = processTree.spawnWailsDev({ workspaceDir: fixture.root, toolsDir });
    networkSampler.start();
  } catch (e) {
    const message = e instanceof Error ? e.message : String(e);
    try {
      log.log(`spawn 後、進入正常等待迴圈之前發生例外：${message}`);
    } catch (logErr) {
      console.error(`[harness] 記錄例外訊息本身失敗（不影響收尾繼續執行）：${String(logErr)}`);
    }
    try {
      runState.setStatus('failed', `post-spawn exception: ${message}`);
    } catch (statusErr) {
      console.error(`[harness] 記錄 failed 狀態本身失敗（不影響收尾繼續執行）：${String(statusErr)}`);
    }
    await teardownOnFailure(log, processTree, networkSampler, runState, 'post-spawn exception');
    throw e;
  }

  const childExitedBox: { value: { code: number | null; signal: NodeJS.Signals | null } | null } = { value: null };
  child.on('exit', (code, signal) => {
    childExitedBox.value = { code, signal };
    log.log(`wails dev 行程結束：exit code=${code} signal=${signal}`);
  });

  const startupTimeoutMs = Number(process.env.E2E_STARTUP_TIMEOUT_MS ?? 300_000);
  const deadline = Date.now() + startupTimeoutMs;
  log.log(`等待 wails dev 就緒（HTTP 可達 127.0.0.1:${WAILS_PORT}），上限 ${startupTimeoutMs}ms`);

  let ready = false;
  while (Date.now() < deadline) {
    const exitedSnapshot = childExitedBox.value;
    if (exitedSnapshot) {
      runState.setStatus('failed', 'startup failed: wails dev exited before ready');
      log.log('wails dev 在 ready 前自行結束，走「部分啟動失敗」收尾路徑');
      await teardownOnFailure(log, processTree, networkSampler, runState, 'startup failed');
      throw new Error(`wails dev 在 ready 前結束（code=${exitedSnapshot.code} signal=${exitedSnapshot.signal}），見 wails-dev.log`);
    }
    if (await httpReachable(WAILS_PORT)) { ready = true; break; }
    await sleep(500);
  }

  if (!ready) {
    runState.setStatus('failed', 'startup timeout');
    log.log(`啟動逾時（${startupTimeoutMs}ms），執行停止程序`);
    await teardownOnFailure(log, processTree, networkSampler, runState, 'startup timeout');
    throw new Error(`wails dev 在 ${startupTimeoutMs}ms 內未就緒（HTTP 未可達），見 wails-dev.log`);
  }

  const vitePort = processTree.discoverVitePort() ?? undefined;
  runState.setPorts({ wails: WAILS_PORT, vite: vitePort });
  runState.setStatus('ready');
  log.log(`wails dev 就緒：HTTP 可達，vite port=${vitePort ?? '(未解析到)'}`);

  writeRunEnv({
    runId,
    artifactsDir,
    workspaceDir: fixture.root,
    toolsDir,
    glossaryPath: fixture.glossaryPath,
    claudeVersion: cli.claudeVersion,
    codexVersion: cli.codexVersion,
    baseUrl: `http://127.0.0.1:${WAILS_PORT}`,
    vitePort,
    scenario: scenarioConfig.scenario,
    scenarioThreadId: scenarioConfig.threadId,
    scenarioTurnId: scenarioConfig.turnId,
    scenarioItemId: scenarioConfig.itemId,
    scenarioApprovalMethod: scenarioConfig.approvalMethod,
    scenarioApprovalRequestId: scenarioConfig.approvalRequestId,
    scenarioDecision: 'accept',
    scenarioConfigPath,
    scenarioLogPath,
    scenarioManifestPath: cli.scenarioManifestPath,
  });

  log.log('globalSetupScenario 完成');
}

async function teardownOnFailure(
  log: HarnessLogger, processTree: ProcessTree, networkSampler: NetworkSampler,
  runState: RunStateStore, stage: string,
): Promise<void> {
  networkSampler.stop();
  const result = await processTree.stop();
  const cleanupClean = result.clean && result.portsReleased;
  log.log(
    `失敗收尾（${stage}）完成：clean=${result.clean} residualPids=${result.residualPids.join(',')} `
    + `abandonedPids=${result.abandonedPids.join(',')} unconfirmedPids=${result.unconfirmedPids.join(',')} `
    + `observationFailed=${result.observationFailed} diagnosticWriteFailed=${result.diagnosticWriteFailed} `
    + `portsReleased=${result.portsReleased}`,
  );
  if (result.observationFailed) {
    runState.recordObservationFailure(`teardownOnFailure（${stage}）：停止程序期間批次觀測失敗（詳見上方 harness.log 對應行）`);
  }
  if (result.diagnosticWriteFailed) {
    runState.recordObservationFailure(`teardownOnFailure（${stage}）：停止程序期間至少一次診斷寫入失敗（詳見上方 harness.log／stderr）`);
  }
  if (cleanupClean) {
    runState.clearActiveRunPointer();
    log.log('清理乾淨，已移除 .active-run.json 指標');
  } else {
    log.log('清理未完全成功，保留 .active-run.json 指標供下次執行的前次殘留檢查接手');
  }
}

let signalHandlersInstalled = false;
function installSignalHandlers(log: HarnessLogger): void {
  if (signalHandlersInstalled) return;
  signalHandlersInstalled = true;
  for (const sig of ['SIGINT', 'SIGTERM'] as const) {
    process.on(sig, () => {
      void (async () => {
        log.log(`收到 ${sig}，寫入 interrupted 並執行停止程序`);
        runtime.runState?.setStatus('interrupted', `interrupted: ${sig}`);
        runtime.networkSampler?.stop();
        if (runtime.processTree) {
          const result = await runtime.processTree.stop();
          const cleanupClean = result.clean && result.portsReleased;
          log.log(
            `中斷收尾完成：clean=${result.clean} residualPids=${result.residualPids.join(',')} `
            + `abandonedPids=${result.abandonedPids.join(',')} unconfirmedPids=${result.unconfirmedPids.join(',')} `
            + `observationFailed=${result.observationFailed} diagnosticWriteFailed=${result.diagnosticWriteFailed} `
            + `portsReleased=${result.portsReleased}`,
          );
          if (result.observationFailed) {
            runtime.runState?.recordObservationFailure('中斷收尾：停止程序期間批次觀測失敗（詳見上方 harness.log 對應行）');
          }
          if (result.diagnosticWriteFailed) {
            runtime.runState?.recordObservationFailure('中斷收尾：停止程序期間至少一次診斷寫入失敗（詳見上方 harness.log／stderr）');
          }
          runtime.runState?.setStatus('interrupted', cleanupClean ? `interrupted: ${sig}` : `interrupted: ${sig} (cleanup incomplete)`);
          if (cleanupClean) {
            runtime.runState?.clearActiveRunPointer();
            log.log('清理乾淨，已移除 .active-run.json 指標');
          } else {
            log.log('清理未完全成功，保留 .active-run.json 指標供下次執行的前次殘留檢查接手');
          }
        }
        const artifactsDirForLog = runtime.artifactsDir;
        console.error(`[e2e] 執行被中斷（interrupted: ${sig}），證據目錄：${artifactsDirForLog ?? '(未知)'}`);
        log.log(`最終結果：INTERRUPTED（interrupted: ${sig}），保留證據目錄 ${artifactsDirForLog ?? '(未知)'}`);
        process.exit(130);
      })();
    });
  }
}
