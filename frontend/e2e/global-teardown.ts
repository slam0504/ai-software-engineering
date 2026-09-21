// B3a-1 Browser E2E globalTeardown（§2.3 收尾、§2.6 tripwire 判定、§2.7 網路
// 判定、§2.9 證據目錄整理）。
//
// 不依賴一定與 globalSetup 同一個 Node 行程：runtime singleton 有值就直接用
// （同行程時的快路徑），沒有值就完全靠落地檔案（run-env.json／run-state.json）
// 重建，兩條路徑都會做同一組判定與收尾。
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import {
  checkModeAgainstHead, diffContentBeforeAfter, listScopedFiles, snapshotArtifacts,
} from './support/artifactIntegrity.js';
import type { ArtifactViolation, FileState } from './support/artifactIntegrity.js';
import { readRunEnv, userFlags } from './support/env.js';
import { HarnessLogger } from './support/logger.js';
import { judgeBrowserNetworkGuard } from './support/networkGuard.js';
import { isPortListening } from './support/psUtil.js';
import { readActiveRunPointer, removeActiveRunPointer } from './support/runState.js';
import type { ActiveRunPointer, RunState } from './support/runState.js';
import { runtime } from './support/runtime.js';
import { loadValidatedPreviousRunState } from './support/staleRun.js';
import { stopProcessGroup } from './support/stopProcedure.js';
import { judgeScenarioCliCalls } from './support/scenario/scenarioTripwire.js';
import { buildClaudeRecoveryExpectation } from './support/scenario/claudeApprovalProtocol.js';
import { determineExecutionMode } from './support/executionMode.js';

const TRIPWIRE_LINE_RE = /^\[(?<ts>[^\]]+)\] name=(?<name>\S+) argv=\((?<argv>.*)\) cwd=(?<cwd>\S+) ppid=(?<ppid>\d+)$/;

// assertPointerBelongsToCurrentRun：codex-review54 重現（`pointer-to-other-run`
// 隔離案例）修正。`loadValidatedPreviousRunState` 只核對 pointer 與「它自己
// 指向的」run-state.json 是否自洽，從未核對這個 pointer 是不是屬於「當前這次
// teardown」——如果 .active-run.json 剛好指向另一個（自洽的）run 的證據
// 目錄，純檔案分支會照樣讀入那個 run 的程序清單／埠、可能送信號、甚至清掉
// 它的 pointer，等於處理錯執行。
//
// 兩個純檔案分支（env 存在／env 不可用）都要在呼叫
// `loadValidatedPreviousRunState`／`stopProcessGroup`／清 pointer 之前，先
// 核對 canonical 化後的 artifactsDir 是否等於「當前 teardown 自己的」
// artifactsDir；canonical 化用 fs.realpathSync 處理 symlink／`/var` 與
// `/private/var` 等價路徑。如果同時拿得到「本次 runId」（env 存在時用
// env.runId；env 不可用時用當前 artifactsDir 的 run-state.json 自己的
// runId），一併核對；拿不到本次 runId 時，至少要核對 artifactsDir，不能因為
// 「拿不到就跳過核對」而放行。任何一項不符，直接拒絕、不動 pointer、不送
// 任何信號。
function canonicalizePath(p: string): string {
  try {
    return fs.realpathSync(p);
  } catch {
    // 目錄本身讀不到（理論上不該發生，呼叫端已經確認 artifactsDir 存在）：
    // 退回單純路徑正規化，仍然可以做字串比對，不會因此拋例外中斷核對。
    return path.resolve(p);
  }
}

function assertPointerBelongsToCurrentRun(
  pointer: ActiveRunPointer,
  currentArtifactsDir: string,
  currentRunId: string | null,
  log: HarnessLogger,
): void {
  const pointerDirReal = canonicalizePath(pointer.artifactsDir);
  const currentDirReal = canonicalizePath(currentArtifactsDir);
  const dirMatches = pointerDirReal === currentDirReal;
  const runIdMatches = currentRunId === null || pointer.runId === currentRunId;
  if (dirMatches && runIdMatches) return;
  log.log(
    `.active-run.json 指標歸屬核對失敗：pointer.artifactsDir=${pointer.artifactsDir}（canonical=${pointerDirReal}）`
    + ` vs 當前 teardown artifactsDir=${currentArtifactsDir}（canonical=${currentDirReal}）；`
    + `pointer.runId=${pointer.runId} vs 當前 runId=${currentRunId ?? '(未知，僅核對 artifactsDir)'}。`
    + `這個 pointer 不屬於當前 teardown，不核對其內容、不送任何信號、pointer 保留不變。`,
  );
  throw new Error(
    `.active-run.json 指標（artifactsDir=${pointer.artifactsDir}，runId=${pointer.runId}）不屬於當前 teardown`
    + `（artifactsDir=${currentArtifactsDir}${currentRunId ? `，runId=${currentRunId}` : ''}），無法核對歸屬，`
    + '不送任何信號，pointer 保留不變',
  );
}

// B3a-2b-2 Task C 第三輪限縮補正（缺陷 1）：執行模式判定移到獨立模組
// `support/executionMode.ts`（型別／實作／判定依據見該檔開頭註解——改用
// execution-entry.json 入口標記，不再只靠 `env.scenario` truthy 推斷，也不再
// 只核對單一 `scenario` 欄位；十個 identity 欄位逐一驗型別＋跨來源一致性）。
// 獨立成模組的原因：`determineExecutionMode` 需要能被 `node xxx.selftest.ts`
// 直接 import 驗證負控制，Node 原生 TS stripping 解析不了本檔慣用的 `.js`
// 副檔名 import 指向 `.ts` 檔。

export default async function globalTeardown(): Promise<void> {
  // globalSetup 有好幾條失敗路徑會讓 writeRunEnv 從未執行、process.env 與
  // run-env.json 都不存在：預檢階段失敗（可能尚未 spawn）、啟動逾時、部分啟動
  // 失敗、ready 前被 SIGINT／SIGTERM 中斷（這幾種都可能已經 spawn 過）。
  let env: ReturnType<typeof readRunEnv> | null;
  let readEnvError: unknown = null;
  try {
    env = readRunEnv();
  } catch (e) {
    env = null;
    readEnvError = e;
  }
  if (!env) {
    // I1 修正（reviewer 六次審查，2026-09-16）：先前這裡看到 run-state.json
    // 存在，就直接假設「停止程序已經在 global-setup.ts 的 teardownOnFailure
    // 內完成」然後印一句 log 就 return——從未真的呼叫、也沒等過
    // `processTree.stop()` 的 single-flight promise。reviewer 用可控延遲的
    // 假 stop() 重現：這裡會在 stop() 連呼叫都還沒被呼叫的情況下就返回
    // （見 `/tmp/i1-repro.mjs` 與本輪證據）。ready 前中斷時，真正執行收尾的
    // 是 global-setup.ts 的 signal handler（不是 teardownOnFailure），它跟
    // Playwright 自己觸發的這個 globalTeardown 是併發關係，不能假設誰先做完
    // ——改成跟主路徑一樣，真的呼叫並等待同一套（single-flight／有界）停止
    // 程序，讓兩邊共用同一個 in-flight promise、都等到它真的 resolve。
    await teardownWithoutEnv(readEnvError);
    return;
  }
  const log = runtime.log ?? new HarnessLogger(env.artifactsDir);
  log.log(`globalTeardown 開始：pid=${process.pid}（globalSetup pid=${runtime.setupPid ?? '未知，可能不同行程'}）`);

  // ---- 1. 停止取樣、停止 wails dev 程序樹 ----
  if (runtime.networkSampler) {
    runtime.networkSampler.stop();
  }

  let stopResult: {
    clean: boolean; residualPids: number[]; abandonedPids: number[]; unconfirmedPids: number[];
    observationFailed: boolean; diagnosticWriteFailed: boolean;
    portsReleased: boolean; residualPorts: number[];
  };
  if (runtime.processTree) {
    stopResult = await runtime.processTree.stop();
  } else {
    log.log('runtime.processTree 為空，退回純檔案重建停止程序');
    const runStateFile = path.join(env.artifactsDir, 'run-state.json');
    const artifactsRoot = path.resolve(env.artifactsDir, '..');
    // 缺陷 B 修正（reviewer 複核 #52）：不再直接 `JSON.parse` 後把
    // `st.processes` 餵給 `stopProcessGroup`——改成跟 handleStaleRun 共用
    // 同一套 pointer/state 結構與一致性驗證（staleRun.ts 的
    // `loadValidatedPreviousRunState`），驗證不過就直接拋出，不送任何信號、
    // 不清指標（連 stopProcessGroup 都不會被呼叫到）。
    const pointer = readActiveRunPointer(artifactsRoot);
    if (!pointer) {
      throw new Error(`B3a-1 e2e globalTeardown：runtime.processTree 為空，且找不到 .active-run.json 指標（${artifactsRoot}），無法核對歸屬，不送任何信號`);
    }
    try {
      assertPointerBelongsToCurrentRun(pointer, env.artifactsDir, env.runId, log);
    } catch (e) {
      throw new Error(`B3a-1 e2e globalTeardown：${e instanceof Error ? e.message : String(e)}`);
    }
    const st = loadValidatedPreviousRunState(pointer, log);
    const killResult = await stopProcessGroup(st.processes, log);
    const portsToCheck = [st.ports.wails, ...(st.ports.vite ? [st.ports.vite] : [])];
    // R2：isPortListening 觀測失敗不能當成「埠已釋放」，一律當殘留處理。
    const residualPorts = portsToCheck.filter(port => {
      try {
        return isPortListening(port);
      } catch (e) {
        log.log(`退回路徑核對埠 ${port} 時觀測失敗，視為殘留：${e instanceof Error ? e.message : String(e)}`);
        return true;
      }
    });
    stopResult = { ...killResult, portsReleased: residualPorts.length === 0, residualPorts };
    if (killResult.observationFailed || killResult.diagnosticWriteFailed || killResult.unconfirmedPids.length > 0) {
      // 這條退回路徑沒有 RunStateStore 物件可用，直接把觀測失敗附加寫回
      // run-state.json，跟主路徑（processTree.doStop()）一樣不讓這幾種失敗
      // 只留在 log 文字裡、run-state 卻看不到。
      try {
        const notes: string[] = [];
        if (killResult.observationFailed) {
          notes.push('globalTeardown 退回路徑：停止程序期間批次觀測失敗（詳見上方 harness.log 對應行）');
        }
        if (killResult.unconfirmedPids.length > 0) {
          notes.push(`globalTeardown 退回路徑：有 ${killResult.unconfirmedPids.length} 個目標身分無法確認：${killResult.unconfirmedPids.join(',')}`);
        }
        if (killResult.diagnosticWriteFailed) {
          notes.push('globalTeardown 退回路徑：停止程序期間至少一次診斷寫入失敗（詳見 stderr）');
        }
        st.observationFailures = [
          ...(st.observationFailures ?? []),
          ...notes.map(n => `[${new Date().toISOString()}] ${n}`),
        ];
        fs.writeFileSync(runStateFile, JSON.stringify(st, null, 2));
      } catch (e) {
        log.log(`退回路徑記錄觀測失敗本身也失敗（不影響已經印出的 log）：${String(e)}`);
      }
    }
  }
  // R1 追加修正（reviewer 五次審查，2026-09-16，N13 重跑抓到：`clean=false`
  // 但 residualPids 是空的，log 完全看不出原因）：把 `abandonedPids`／
  // `observationFailed` 一併印出來。複核 #34：再加上 `unconfirmedPids`／
  // `diagnosticWriteFailed` 兩個新原因。
  log.log(
    `停止程序結果：clean=${stopResult.clean} residualPids=${stopResult.residualPids.join(',')} `
    + `abandonedPids=${stopResult.abandonedPids.join(',')} unconfirmedPids=${stopResult.unconfirmedPids.join(',')} `
    + `observationFailed=${stopResult.observationFailed} diagnosticWriteFailed=${stopResult.diagnosticWriteFailed} `
    + `portsReleased=${stopResult.portsReleased} residualPorts=${stopResult.residualPorts.join(',')}`,
  );

  // ---- R2：程序樹追蹤／網路取樣途中若有觀測失敗，不能判成乾淨 ----
  // 缺口（reviewer 二次審查，2026-09-15，N8 實跑發現）：這裡同時要讀出
  // run-state.json 目前的 status／failureStage，判斷這次執行是不是被
  // SIGINT／SIGTERM 中斷過——中斷是終態，不能被下面的 `setStatus` 覆寫成
  // 看起來像正常收尾的 'stopped'（那會讓判定誤以為是 PASSED，即使
  // run-state.json 裡其實記著 `failureStage=interrupted: SIGINT`）。
  let observationFailures: string[] = [];
  let wasInterrupted = false;
  let interruptedFailureStage: string | undefined;
  // R3 修正（reviewer 三次審查，2026-09-16）：先前這裡讀失敗只記一句 log
  // 就繼續，`observationFailures` 停在預設值 `[]`——等於「讀不到證據」被
  // 當成「沒有觀測失敗」，跟上面 network-samples.log 是同一種錯誤形狀。
  // run-state.json 這個時間點理論上一定存在（預檢通過就會建立），讀不到／
  // 解析不出來本身就是異常，要讓整次執行失敗，不能只是記錄然後假裝沒事。
  let runStateReadFailed = false;
  try {
    const runStateFile = path.join(env.artifactsDir, 'run-state.json');
    const st = JSON.parse(fs.readFileSync(runStateFile, 'utf8')) as RunState;
    observationFailures = st.observationFailures ?? [];
    wasInterrupted = st.status === 'interrupted' || (st.failureStage ?? '').startsWith('interrupted');
    if (wasInterrupted) interruptedFailureStage = st.failureStage;
  } catch (e) {
    runStateReadFailed = true;
    log.log(`讀取 run-state.json 檢查 observationFailures／中斷狀態時失敗，判定失敗（不能假裝沒有觀測失敗）：${String(e)}`);
  }
  if (observationFailures.length > 0) {
    log.log(`偵測到 ${observationFailures.length} 筆觀測失敗（ps／lsof 本身執行失敗），不得判成乾淨：\n${observationFailures.join('\n')}`);
  }
  if (wasInterrupted) {
    log.log(`偵測到這次執行被中斷過（run-state.json：failureStage=${interruptedFailureStage}），最終判定不得為 PASSED`);
  }

  const cleanupClean = stopResult.clean && stopResult.portsReleased;
  // 中斷是終態，不再改寫成 'stopped'／'failed'——runtime.runState（同行程）
  // 這裡多半就是觸發中斷的那個 SIGINT／SIGTERM handler 自己設的，不要蓋掉。
  if (runtime.runState && !wasInterrupted) {
    runtime.runState.setStatus(cleanupClean ? 'stopped' : 'failed', cleanupClean ? undefined : 'teardown: cleanup incomplete');
  }
  // 只有完全清乾淨才拿掉 .active-run.json 指標——沒清乾淨時留著，讓下一次
  // 執行的前次殘留檢查（§2.3）能接手核對身分並補做清理，而不是無聲遺失。
  if (cleanupClean) {
    if (runtime.runState) runtime.runState.clearActiveRunPointer();
    else removeActiveRunPointer(path.resolve(env.artifactsDir, '..'));
  } else {
    log.log('清理未完全成功，保留 .active-run.json 供下次執行的前次殘留檢查接手');
  }

  // 執行模式：明確判定（見 determineExecutionMode 上方註解），不是靠
  // `env.scenario` truthy 推斷。
  //
  // 位置更正（B3a-2b-2 Task C 阻擋缺陷修正）：這裡原本寫在第 1 節「停止
  // 取樣、停止 wails dev 程序樹」之前，註解卻宣稱「無論判定結果為何，第 1
  // 節一律先做完」——那句話跟實際程式順序不符：`determineExecutionMode`
  // 若拋出未捕捉例外（例如 execution-entry.json 落地內容是 JSON `null`），
  // 會讓整個 globalTeardown 在還沒呼叫 `runtime.processTree.stop()`／純檔案
  // 退回路徑之前就中止，已擁有的程序反而不會被停。`executionMode` 唯一的
  // 使用點在下面的 tripwire 判定（§2.6），跟第 1 節的停止程序、跟上面的
  // pointer 清理都無關，所以直接把判定移到這兩者都做完之後——即使
  // `determineExecutionMode` 本身出狀況，也不會擋到已經做完的 bounded stop
  // 與 pointer 清理。
  const executionMode = determineExecutionMode(env, env.artifactsDir, log);
  log.log(`執行模式判定：${executionMode.kind}${executionMode.kind === 'scenario-broken' ? `（${executionMode.reason}）` : ''}`);

  // ---- 2. tripwire 判定（§2.6） ----
  const toolsDir = env.toolsDir;
  const finalInvocationsLog = path.join(env.artifactsDir, 'invocations.log');
  let tripwireViolations: string[] = [];
  try {
    fs.copyFileSync(path.join(toolsDir, 'invocations.log'), finalInvocationsLog);
    // B3a-2b-2 Task C 驗收缺口修正（缺口 4）：改用上面明確判定的
    // `executionMode`（不是 `env.scenario` truthy）決定要走哪一套 tripwire
    // 判定。`scenario-broken`（identity 遺失／型別錯誤／來源不一致）一律判
    // 失敗，不得回退成 default 的 `judgeTripwire`（那套假設 codex 永遠只會
    // 收到 `--version`，對 scenario 執行一定會誤判；反過來也不能誤用
    // scenario 判定去查一個其實是 default 的 run）。
    if (executionMode.kind === 'scenario') {
      // F2：provider 來自**已驗證的 executionMode identity**（determineExecutionMode
      // 已確認 scenario 名稱在 env 與檔案兩側一致且可由登記表解析）。
      // 不使用「解析失敗就當 codex」的 fallback——那會在 identity 壞掉時
      // 悄悄套用另一個 provider 的判定（reviewer #367）。
      // E1：**核定輪數與核定 resume 都由已驗證的 scenario identity 決定**
      // （determineExecutionMode 已核對 env 與 run-env.json 兩份來源一致，
      // 並由登記表解析出 provider／kind），不從觀測到的呼叫數或待驗 argv 反推。
      const isRecovery = executionMode.scenarioKind === 'recovery';
      tripwireViolations = judgeScenarioCliCalls(finalInvocationsLog, log, {
        provider: executionMode.provider,
        toolsDir: env.toolsDir,
        expectedConversationCalls: isRecovery ? 2 : 1,
        expectedResume: isRecovery ? buildClaudeRecoveryExpectation(env.runId).sessionId : null,
      });
    } else if (executionMode.kind === 'default') {
      tripwireViolations = judgeTripwire(finalInvocationsLog, env.claudeVersion, env.codexVersion, log);
    } else {
      tripwireViolations = [`scenario identity 判定失敗，無法安全選擇 tripwire 判定方式：${executionMode.reason}`];
      log.log(`tripwire 判定略過（identity 壞掉，fail closed）：${executionMode.reason}`);
    }
  } catch (e) {
    tripwireViolations = [`invocations.log 缺失或無法讀取：${String(e)}`];
    log.log(`tripwire 判定失敗：${String(e)}`);
  }

  // ---- 3. 網路判定（§2.7 取樣部分） ----
  // R3 修正（reviewer 三次審查，2026-09-16）：先前證據檔不存在時預設
  // `networkViolations = 0`——「看不到證據」被當成「沒有違規」，跟缺口 A
  // 修好之前的 browser guard 是同一種錯誤形狀。NetworkSampler 的建構子一定
  // 會同步建立這個檔案（就算全程沒有取樣到任何東西也會是空檔案），所以檔案
  // 不存在只有兩種可能：取樣器根本沒被建立過（R2 那類 spawn 後例外），或
  // 檔案在判定前被動過手腳——不管哪一種，都不能算「沒有違規」。
  const networkLogPath = path.join(env.artifactsDir, 'network-samples.log');
  let networkViolations = 0;
  let networkLogMissingOrUnreadable = false;
  if (!fs.existsSync(networkLogPath)) {
    networkLogMissingOrUnreadable = true;
    log.log(`network-samples.log 不存在（${networkLogPath}）——無法證明網路取樣有執行過，判定失敗`);
  } else {
    try {
      const text = fs.readFileSync(networkLogPath, 'utf8');
      networkViolations = (text.match(/^NETWORK-VIOLATION:/gm) ?? []).length;
    } catch (e) {
      networkLogMissingOrUnreadable = true;
      log.log(`network-samples.log 讀取失敗：${String(e)}`);
    }
  }
  if (networkViolations > 0) log.log(`網路判定失敗：偵測到 ${networkViolations} 筆非 loopback 連線紀錄`);

  // ---- 3.5 browser 層網路判定（reviewer 必修項目：HTTP／WebSocket route） ----
  // 缺口 A：證據檔不存在／讀取失敗都算失敗，不能當成「沒有違規」（N14b）；
  // 「檔案存在但因為寫入失敗而維持空白」這種情況要靠 worker 行程內的
  // in-memory 檢查抓（見 glossary.spec.ts 收尾的 expect），這裡只能看檔案。
  const browserGuardJudgement = judgeBrowserNetworkGuard(env.artifactsDir);
  const browserNetworkViolations = browserGuardJudgement.failed;
  if (browserNetworkViolations) {
    log.log(`browser 層網路判定失敗：${browserGuardJudgement.reason}`);
  }

  // ---- 3.6 受版控產物執行前後檢查（Q3） ----
  // mode 永遠跟 HEAD 比；內容只比對「執行前」跟「執行後」（見 artifactIntegrity.ts
  // 頂端註解）。找不到執行前快照本身也算失敗——沒有基準沒辦法證明內容沒被動過。
  const __dirname = path.dirname(fileURLToPath(import.meta.url));
  const repoRootForArtifacts = path.resolve(__dirname, '..', '..');
  const artifactViolations: ArtifactViolation[] = [];
  try {
    const scopeFiles = listScopedFiles(repoRootForArtifacts);
    artifactViolations.push(...checkModeAgainstHead(repoRootForArtifacts, scopeFiles));
    let baseline: FileState[] | null = runtime.artifactBaselineSnapshot;
    if (!baseline) {
      const baselineFile = path.join(env.artifactsDir, 'artifact-integrity-baseline.json');
      if (fs.existsSync(baselineFile)) {
        const parsed = JSON.parse(fs.readFileSync(baselineFile, 'utf8')) as { snapshot: FileState[] };
        baseline = parsed.snapshot;
      }
    }
    if (baseline) {
      const afterSnapshot = snapshotArtifacts(repoRootForArtifacts, scopeFiles);
      artifactViolations.push(...diffContentBeforeAfter(baseline, afterSnapshot));
    } else {
      log.log('找不到執行前的受版控產物快照（artifact-integrity-baseline.json 與 runtime 都沒有），無法證明內容在執行期間沒被動過');
      artifactViolations.push({ path: '(baseline missing)', kind: 'content-changed-during-run', detail: '找不到執行前快照' });
    }
  } catch (e) {
    log.log(`受版控產物執行後檢查本身失敗：${String(e)}`);
    artifactViolations.push({ path: '(check itself failed)', kind: 'content-changed-during-run', detail: String(e) });
  }
  if (artifactViolations.length > 0) {
    log.log(`受版控產物檢查失敗：\n${artifactViolations.map(v => `${v.path}（${v.kind}）：${v.detail}`).join('\n')}`);
  }

  // ---- 4. fixture 快照（§2.9） ----
  captureFixtureSnapshot(env.workspaceDir, env.glossaryPath, env.artifactsDir, log);
  try {
    fs.rmSync(env.workspaceDir, { recursive: true, force: true });
  } catch (e) {
    log.log(`清除 fixture 失敗（不影響判定）：${String(e)}`);
  }

  // ---- 5. 測試本體是否失敗（glossary.spec.ts 的 afterEach 標記） ----
  const testFailedMarker = path.join(env.artifactsDir, 'TEST_FAILED');
  const testFailed = fs.existsSync(testFailedMarker);

  // R3 定點反證抓到的真實 bug（2026-09-16）：`networkLogMissingOrUnreadable`
  // 與 `runStateReadFailed` 這兩個旗標先前只算出來、寫進 log 文字，卻沒有
  // 真的併進 `overallFailed`——log 印著「判定失敗」，但整次執行最後還是
  // PASSED。用定點反證（刪除 network-samples.log／run-state.json）實測
  // 才發現：`network-samples.log 不存在...判定失敗` 這行印出來了，
  // `overallFailed` 卻是 `false`。這裡把兩個旗標補進判斷式，不能只有
  // log 文字說失敗、判斷式沒跟著算。
  const overallFailed = wasInterrupted || testFailed || !stopResult.clean || !stopResult.portsReleased
    || tripwireViolations.length > 0 || networkViolations > 0 || browserNetworkViolations
    || observationFailures.length > 0 || artifactViolations.length > 0
    || networkLogMissingOrUnreadable || runStateReadFailed;

  log.log(
    `globalTeardown 判定：interrupted=${wasInterrupted} testFailed=${testFailed} cleanupClean=${stopResult.clean && stopResult.portsReleased} `
    + `tripwireViolations=${tripwireViolations.length} networkViolations=${networkViolations} `
    + `networkLogMissingOrUnreadable=${networkLogMissingOrUnreadable} runStateReadFailed=${runStateReadFailed} `
    + `browserNetworkViolations=${browserNetworkViolations} observationFailures=${observationFailures.length} `
    + `artifactViolations=${artifactViolations.length} → overallFailed=${overallFailed}`,
  );

  if (wasInterrupted) {
    // 中斷是自己的終態，不是「FAILED」的其中一種原因——分開標示，讀 log 的
    // 人不用去猜「FAILED」到底是判定失敗還是被打斷。
    console.error(`[e2e] 執行被中斷（${interruptedFailureStage}），證據目錄：${env.artifactsDir}`);
    log.log(`最終結果：INTERRUPTED（${interruptedFailureStage}），保留證據目錄 ${env.artifactsDir}`);
  } else if (overallFailed) {
    console.error(`[e2e] 執行失敗，證據目錄：${env.artifactsDir}`);
    log.log(`最終結果：FAILED，保留證據目錄 ${env.artifactsDir}`);
  } else if (userFlags.keepArtifacts()) {
    console.log(`[e2e] E2E_KEEP_ARTIFACTS=1，保留證據目錄：${env.artifactsDir}`);
    log.log(`最終結果：PASSED，依 E2E_KEEP_ARTIFACTS 保留證據目錄 ${env.artifactsDir}`);
  } else {
    log.log(`最終結果：PASSED，清除證據目錄 ${env.artifactsDir}`);
    fs.rmSync(env.artifactsDir, { recursive: true, force: true });
  }

  if (overallFailed) {
    const reasons = [
      wasInterrupted && `run interrupted (${interruptedFailureStage})`,
      testFailed && 'test failed',
      !(stopResult.clean && stopResult.portsReleased) && 'cleanup incomplete',
      tripwireViolations.length > 0 && `tripwire violations: ${tripwireViolations.join('; ')}`,
      networkViolations > 0 && `network violations: ${networkViolations}`,
      browserNetworkViolations && 'browser-level network violations (see browser-network-violations.log)',
      observationFailures.length > 0 && `tool observation failures: ${observationFailures.length} (see run-state.json observationFailures)`,
      artifactViolations.length > 0 && `tracked artifact integrity violations: ${artifactViolations.length} (see harness.log, not auto-restored)`,
      networkLogMissingOrUnreadable && 'network-samples.log missing or unreadable (cannot prove no violations occurred)',
      runStateReadFailed && 'run-state.json missing or unreadable (cannot prove no observation failures occurred)',
    ].filter(Boolean);
    throw new Error(`B3a-1 e2e globalTeardown：${reasons.join(' | ')}`);
  }
}

type StopOutcome = {
  clean: boolean; residualPids: number[]; abandonedPids: number[]; unconfirmedPids: number[];
  observationFailed: boolean; diagnosticWriteFailed: boolean;
  portsReleased: boolean; residualPorts: number[];
};

function logStopResult(log: HarnessLogger, context: string, stopResult: StopOutcome): void {
  log.log(
    `停止程序結果 ${context}：clean=${stopResult.clean} residualPids=${stopResult.residualPids.join(',')} `
    + `abandonedPids=${stopResult.abandonedPids.join(',')} unconfirmedPids=${stopResult.unconfirmedPids.join(',')} `
    + `observationFailed=${stopResult.observationFailed} diagnosticWriteFailed=${stopResult.diagnosticWriteFailed} `
    + `portsReleased=${stopResult.portsReleased} residualPorts=${stopResult.residualPorts.join(',')}`,
  );
}

// R2 沿用：isPortListening 觀測失敗不能當成「埠已釋放」，一律當殘留處理。
function checkResidualPorts(ports: number[], log: HarnessLogger, context: string): number[] {
  return ports.filter(port => {
    try {
      return isPortListening(port);
    } catch (e) {
      log.log(`${context} 核對埠 ${port} 時觀測失敗，視為殘留：${e instanceof Error ? e.message : String(e)}`);
      return true;
    }
  });
}

// teardownWithoutEnv：I1 修正（reviewer 六次審查，2026-09-16）。readRunEnv()
// 失敗只代表 app 沒有走到 ready（run-env.json 是 ready 之後才寫），不代表
// 「已經有人收尾過、這裡什麼都不用做」。這裡跟主路徑（globalTeardown 本體）
// 共用同一套停止程序——同行程時呼叫 `runtime.processTree.stop()`（跟
// signal handler／teardownOnFailure 共用 single-flight in-flight promise，
// 真的等它 resolve，不用「猜測」代替）；跨行程時用既有的 `stopProcessGroup`
// （跟 handleStaleRun 同一套四要素身分核對，不是重新發明一套無界的停止
// 程序——需求 4／6）。env 從未寫出**只代表 setup 未走完**——B3a-2b-2 F2 的
// 實測（#372）證明 app 可以已經 ready、HTTP 可達，卻在 ready 之後的 setup
// 步驟失敗而沒寫出 env；因此不能一律說「app 從未 ready」。這條分支的判定與
// 收尾行為不變（仍然 fail closed），只是措辭不再冒稱知道 app 有沒有就緒。
// PASSED，一律非零結束（不管停止程序本身乾不乾淨），不讓 Playwright 誤判
// 成功；只有真的完成且乾淨才清 `.active-run.json` 指標，不完整時保留（需求
// 3）。
async function teardownWithoutEnv(readEnvError: unknown): Promise<void> {
  const fallbackDir = runtime.artifactsDir ?? process.env.E2E_ARTIFACTS_DIR;
  if (!fallbackDir) {
    // 連 artifacts 目錄本身都不知道在哪——沒有 HarnessLogger 可寫，也沒有
    // run-state.json 可讀，理論上不該發生（globalSetup 一開始就會設定
    // runtime.artifactsDir／E2E_ARTIFACTS_DIR）。印到 stderr（唯一還能用的
    // 管道），不虛構任何停止工作。
    console.error(`[e2e] globalTeardown：讀不到 run-env.json，且找不到 artifacts 目錄，無法判斷或收尾：${String(readEnvError)}`);
    return;
  }
  const log = runtime.log ?? new HarnessLogger(fallbackDir);
  const runStateFile = path.join(fallbackDir, 'run-state.json');
  const artifactsRoot = path.resolve(fallbackDir, '..');
  const stateExists = fs.existsSync(runStateFile);

  // 缺陷 A 修正（reviewer 複核 #52，`missing-state-live-runtime` 隔離案例）：
  // `runtime.processTree` 存在＝這個行程還持有活的追蹤狀態，代表 spawn
  // 過、甚至可能還在跑——不管 run-state.json 存不存在、解不解析得出來，都
  // 不能因此跳過停止與等待。這個判斷排在「state 是否存在」之前：真正
  // 「預檢從未 spawn」的結論只能由「沒有 runtime.processTree 這個活證據」
  // ＋「也讀不到 run-state.json」共同支持，不能只用「檔案不存在」單一條件
  // 推定（見下方）。
  if (runtime.processTree) {
    const stopResult = await runtime.processTree.stop();
    logStopResult(log, '(env 不可用＝setup 未走完，同行程 runtime.processTree)', stopResult);
    const cleanupClean = stopResult.clean && stopResult.portsReleased;
    if (cleanupClean) {
      if (runtime.runState) runtime.runState.clearActiveRunPointer();
      else removeActiveRunPointer(artifactsRoot);
    } else {
      log.log('清理未完全成功，保留 .active-run.json 指標供下次執行的前次殘留檢查接手');
    }

    // run-state.json 在這裡只用來補充 log／錯誤訊息的脈絡（中斷階段／
    // 狀態）——讀不到或解析不出來不影響已經完成的停止判定，停止與等待已經
    // 真的做過了，不會因為讀檔失敗又退回「什麼都不用做」。
    let st: RunState | null = null;
    if (stateExists) {
      try {
        st = JSON.parse(fs.readFileSync(runStateFile, 'utf8')) as RunState;
      } catch (e) {
        log.log(`（僅供脈絡）run-state.json 讀取／解析失敗，不影響已完成的停止判定：${String(e)}`);
      }
    }
    const wasInterrupted = !!st && (st.status === 'interrupted' || (st.failureStage ?? '').startsWith('interrupted'));
    if (wasInterrupted) {
      console.error(`[e2e] 執行被中斷（${st!.failureStage}），證據目錄：${fallbackDir}`);
      log.log(`最終結果：INTERRUPTED（${st!.failureStage}），env 從未寫出（setup 未走完；app 是否曾就緒需看 harness.log），保留證據目錄 ${fallbackDir}`);
    } else {
      console.error(`[e2e] 執行失敗，證據目錄：${fallbackDir}`);
      log.log(
        `最終結果：FAILED（env 從未寫出＝setup 未走完；app 是否曾就緒需看 harness.log${st ? `；run-state 記錄 status=${st.status} failureStage=${st.failureStage ?? '(未設定)'}` : '；run-state.json 缺失或無法解析'}），`
        + `保留證據目錄 ${fallbackDir}`,
      );
    }
    // 缺乏執行證據（env 從未寫出）代表 **setup 未走完**（不等於 app 未曾就緒），不管停止程序
    // 乾不乾淨、run-state.json 讀不讀得到，這次執行都不能算 PASSED。
    const reasons = [
      wasInterrupted
        ? `run interrupted (${st!.failureStage})`
        : `run failed before ready (${st ? `status=${st.status}, failureStage=${st.failureStage ?? '(未設定)'}` : 'run-state.json missing or unreadable'})`,
      !cleanupClean && 'cleanup incomplete',
    ].filter(Boolean);
    throw new Error(`B3a-1 e2e globalTeardown（env 不可用＝setup 未走完）：${reasons.join(' | ')}`);
  }

  // 沒有同行程 runtime.processTree 可用。以下分支只處理「未取得本次程序
  // 追蹤紀錄」的情況：不虛構任何停止工作，也不宣稱已證明未曾 spawn——
  // 單看檔案不存在不足以推定「未曾 spawn」（缺陷 A）。
  if (!stateExists) {
    // codex-review54 重現（`missing-state-with-pointer` 隔離案例）修正：先前
    // 只看「沒有同行程 runtime」＋「讀不到 run-state.json」這兩項「不存在的
    // 證據」，就直接判定「app 從未啟動，無須收尾」——完全沒理會
    // `.active-run.json` 指標是否仍然存在。跨行程本來就沒有 runtime，這條
    // 「不存在」不能拿來抵銷「pointer 仍存在」這個「存在」的證據：pointer
    // 存在代表曾經有人（這次或前次執行）記錄過 spawn，只是 run-state.json
    // 這份落地證據不見了（可能是還沒來得及寫、也可能是被清過但 pointer 沒
    // 跟著清）——兩種情況都不能被當成「已確認從未啟動」。
    //
    // 加上「也沒有 .active-run.json 指標」這第三個條件之後，本分支才不執行
    // 停止程序；但三種紀錄皆不存在僅代表「未取得本次程序追蹤紀錄及殘留
    // 指標」，不等於已證明從未啟動（codex-review56 措辭收斂）——執行結果
    // 一律依 setup 失敗回報，不由這裡下「從未啟動」的結論。
    const pointer = readActiveRunPointer(artifactsRoot);
    if (pointer) {
      log.log(
        `globalTeardown：讀不到 run-env.json、沒有同行程 runtime.processTree、也讀不到 run-state.json，`
        + `但 .active-run.json 指標仍然存在（run=${pointer.runId} rootPid=${pointer.rootPid}）——`
        + `「兩個不存在的證據」不能抵銷「仍存在的 active pointer」，不能判定為「從未啟動」。`
        + `fail closed：不送任何信號、保留指標，留給下次執行的前次殘留檢查接手：${String(readEnvError)}`,
      );
      throw new Error(
        `B3a-1 e2e globalTeardown：讀不到 run-state.json（${runStateFile}），但 .active-run.json 指標仍存在`
        + `（${artifactsRoot}，run=${pointer.runId}），無法確認「從未啟動」，fail closed，不送任何信號，pointer 保留不變`,
      );
    }
    log.log(`globalTeardown：未取得本次程序追蹤紀錄（沒有同行程 runtime.processTree、讀不到 run-env.json 與 run-state.json）及殘留指標（沒有 .active-run.json），本分支未執行停止；執行結果依 setup 失敗回報，不能據此證明從未啟動：${String(readEnvError)}`);
    return;
  }

  let st: RunState;
  try {
    st = JSON.parse(fs.readFileSync(runStateFile, 'utf8')) as RunState;
  } catch (parseErr) {
    // run-state.json 壞掉：不知道有哪些 pid，需求 4——不得直接相信檔案內容
    // 去 kill。沒有同行程 runtime 可用，「無法安全收尾」，不動任何殘留程序，
    // 留給下次執行的前次殘留檢查（handleStaleRun，本來就會做完整的身分核對）
    // 接手。
    log.log(`globalTeardown：run-state.json 存在但讀取／解析失敗，不信任其內容：${String(parseErr)}`);
    throw new Error(`B3a-1 e2e globalTeardown（env 不可用）：run-state.json 損毀且沒有同行程 runtime 可用，無法安全核對身分，不動任何殘留程序，留給下次執行的前次殘留檢查：${String(parseErr)}`);
  }

  const wasInterrupted = st.status === 'interrupted' || (st.failureStage ?? '').startsWith('interrupted');

  // 缺陷 B 修正（reviewer 複核 #52，`empty-processes-no-runtime` 隔離案例）：
  // 跨行程 fallback 不能直接把 `JSON.parse` 出來的 `st.processes` 餵給
  // `stopProcessGroup`——改成跟 handleStaleRun／global-teardown.ts 主路徑
  // 純檔案分支共用同一套 pointer/state 結構與一致性驗證（staleRun.ts 的
  // `loadValidatedPreviousRunState`）。驗證不過就直接失敗、不送任何信號、
  // 不清指標——不先呼叫任何會清 pointer 的 helper 再核對埠。
  const pointer = readActiveRunPointer(artifactsRoot);
  if (!pointer) {
    throw new Error(`B3a-1 e2e globalTeardown（env 不可用）：run-state.json 存在但找不到 .active-run.json 指標（${artifactsRoot}），無法核對歸屬，不送任何信號`);
  }
  // codex-review54 重現（`pointer-to-other-run` 隔離案例）修正：在信任這個
  // pointer 之前，先核對它是不是屬於當前這次 teardown——不然
  // `loadValidatedPreviousRunState` 只會核對 pointer 跟「它自己指向的」
  // run-state.json 是否自洽，另一個自洽的 run 的 pointer 一樣會通過。當前
  // runId 從這次 teardown 自己的 artifactsDir（fallbackDir）下的
  // run-state.json（即上面已經解析成功的 `st`）取得。
  let validated: RunState;
  try {
    assertPointerBelongsToCurrentRun(pointer, fallbackDir, st.runId, log);
    validated = loadValidatedPreviousRunState(pointer, log);
  } catch (e) {
    throw new Error(`B3a-1 e2e globalTeardown（env 不可用，跨行程 fallback）：${e instanceof Error ? e.message : String(e)}`);
  }

  const killResult = await stopProcessGroup(validated.processes, log);
  const portsToCheck = [validated.ports.wails, ...(validated.ports.vite ? [validated.ports.vite] : [])];
  const residualPorts = checkResidualPorts(portsToCheck, log, '(env 不可用，跨行程退回路徑)');
  const stopResult: StopOutcome = { ...killResult, portsReleased: residualPorts.length === 0, residualPorts };
  if (killResult.observationFailed || killResult.diagnosticWriteFailed || killResult.unconfirmedPids.length > 0) {
    try {
      const notes: string[] = [];
      if (killResult.observationFailed) {
        notes.push('globalTeardown（env 不可用）退回路徑：停止程序期間批次觀測失敗（詳見上方 harness.log 對應行）');
      }
      if (killResult.unconfirmedPids.length > 0) {
        notes.push(`globalTeardown（env 不可用）退回路徑：有 ${killResult.unconfirmedPids.length} 個目標身分無法確認：${killResult.unconfirmedPids.join(',')}`);
      }
      if (killResult.diagnosticWriteFailed) {
        notes.push('globalTeardown（env 不可用）退回路徑：停止程序期間至少一次診斷寫入失敗（詳見 stderr）');
      }
      validated.observationFailures = [
        ...(validated.observationFailures ?? []),
        ...notes.map(n => `[${new Date().toISOString()}] ${n}`),
      ];
      fs.writeFileSync(runStateFile, JSON.stringify(validated, null, 2));
    } catch (e) {
      log.log(`退回路徑記錄觀測失敗本身也失敗（不影響已經印出的 log）：${String(e)}`);
    }
  }
  logStopResult(log, '(env 不可用)', stopResult);

  // 只有程序與埠都確認乾淨，才清 pointer——驗證、停止、核對埠都走完才走到
  // 這裡，不先呼叫會清 pointer 的 helper 再核對埠。
  const cleanupClean = stopResult.clean && stopResult.portsReleased;
  if (cleanupClean) {
    if (runtime.runState) runtime.runState.clearActiveRunPointer();
    else removeActiveRunPointer(artifactsRoot);
  } else {
    log.log('清理未完全成功，保留 .active-run.json 指標供下次執行的前次殘留檢查接手');
  }

  if (wasInterrupted) {
    console.error(`[e2e] 執行被中斷（${st.failureStage}），證據目錄：${fallbackDir}`);
    log.log(`最終結果：INTERRUPTED（${st.failureStage}），env 從未寫出（app 未就緒），保留證據目錄 ${fallbackDir}`);
  } else {
    console.error(`[e2e] 執行失敗，證據目錄：${fallbackDir}`);
    log.log(`最終結果：FAILED（env 從未寫出，app 未就緒；run-state 記錄 status=${st.status} failureStage=${st.failureStage ?? '(未設定)'}），保留證據目錄 ${fallbackDir}`);
  }

  const reasons = [
    wasInterrupted ? `run interrupted (${st.failureStage})` : `run failed before ready (status=${st.status}, failureStage=${st.failureStage ?? '(未設定)'})`,
    !cleanupClean && 'cleanup incomplete',
  ].filter(Boolean);
  throw new Error(`B3a-1 e2e globalTeardown（env 不可用，app 從未就緒）：${reasons.join(' | ')}`);
}

function judgeTripwire(logFile: string, claudeVersion: string, codexVersion: string, log: HarnessLogger): string[] {
  const violations: string[] = [];
  const text = fs.readFileSync(logFile, 'utf8');
  const lines = text.split('\n').map(l => l.trim()).filter(Boolean);
  if (lines.length === 0) violations.push('invocations.log 為空（缺失呼叫紀錄不算通過）');

  let claudeCalls = 0;
  let codexCalls = 0;
  for (const line of lines) {
    const m = TRIPWIRE_LINE_RE.exec(line);
    if (!m || !m.groups) {
      violations.push(`invocations.log 出現無法解析的行：${line}`);
      continue;
    }
    const name = m.groups.name;
    const argv = m.groups.argv.trim();
    if (argv !== '--version') {
      violations.push(`${name} 收到非 --version 的呼叫：argv=(${argv})`);
    }
    if (name === 'claude') claudeCalls++;
    else if (name === 'codex') codexCalls++;
    else violations.push(`未知的假 CLI 名稱：${name}`);
  }
  if (claudeCalls < 1) violations.push('claude 從未被呼叫（CLIInfo 應各呼叫一次 --version）');
  if (codexCalls < 1) violations.push('codex 從未被呼叫（CLIInfo 應各呼叫一次 --version）');
  log.log(`tripwire 判定：claude 呼叫 ${claudeCalls} 次、codex 呼叫 ${codexCalls} 次、違規 ${violations.length} 筆`);
  void claudeVersion; void codexVersion; // 版本字串已在啟動後核對階段（cliInfo.ts）驗證，這裡只判定呼叫紀律。
  return violations;
}

function captureFixtureSnapshot(workspaceDir: string, glossaryPath: string, artifactsDir: string, log: HarnessLogger): void {
  try {
    const gitLog = execFileSync('git', ['log', '--oneline', '-n', '20'], { cwd: workspaceDir, encoding: 'utf8' });
    fs.writeFileSync(path.join(artifactsDir, 'fixture-git-log.txt'), gitLog);
    const gitStatus = execFileSync('git', ['status', '--short'], { cwd: workspaceDir, encoding: 'utf8' });
    fs.writeFileSync(path.join(artifactsDir, 'fixture-git-status.txt'), gitStatus);
  } catch (e) {
    log.log(`擷取 fixture git log／status 失敗：${String(e)}`);
  }
  try {
    const content = fs.readFileSync(glossaryPath, 'utf8');
    fs.writeFileSync(path.join(artifactsDir, 'glossary-final-content.md'), content);
  } catch (e) {
    log.log(`擷取 glossary.md 最終內容失敗：${String(e)}`);
  }
}
