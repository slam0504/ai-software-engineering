// B3a-1 Browser E2E globalSetup（§2.2、§2.3、§2.6、§2.7 取樣部分）。
//
// 執行順序：前次殘留處理 → fixture／假 CLI 建立 → 啟動前預檢（任一項不符即
// 不 spawn）→ spawn wails dev（含網路取樣、後代樹追蹤自 spawn 起）→ 等待
// HTTP 可達（啟動逾時上限）→ 把本次執行的路徑寫回 env 供 spec 使用。
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import http from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { createFakeCliSet, injectBadVersionAfterPreflight } from './support/fakeCli.js';
import { createFixture } from './support/fixture.js';
import { userFlags, writeRunEnv } from './support/env.js';
import { HarnessLogger } from './support/logger.js';
import { NetworkSampler } from './support/networkSampler.js';
import {
  checkArtifactModeAgainstHead, checkEnvValues, checkFakeCli, checkFixture, checkPortFree, PreflightError,
} from './support/preflight.js';
import { listScopedFiles, snapshotArtifacts } from './support/artifactIntegrity.js';
import { ProcessTree } from './support/processTree.js';
import {
  descendantsOf, processCommand, processPgid, processStartedAt, snapshotProcessTableWithCommand,
} from './support/psUtil.js';
import { RunStateStore } from './support/runState.js';
import { runtime } from './support/runtime.js';
import { handleStaleRun, StaleRunConflictError } from './support/staleRun.js';

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

export default async function globalSetup(): Promise<void> {
  // B3a-2b-2 Task C 裁定：default（test:e2e）／controls（test:e2e:controls）
  // 這兩條入口共用本檔，一律禁止 E2E_SCENARIO——scenario 專屬情境只走
  // playwright.scenario.config.ts（global-setup.scenario.ts），不得靜默
  // 忽略、也不得在這兩條入口下被意外啟用。
  // B3a-2b-2 Task C 驗收缺口修正（缺口 4）：先前用 `if (process.env.E2E_SCENARIO)`
  // truthy 判斷——`E2E_SCENARIO=''`（空字串）會被 truthy 檢查放過，靜默流入
  // default／controls 入口。改成用「這個變數是否存在」判斷，空字串也算「設定
  // 了但無效」，一律拒絕，不回退成「當作沒設定」。
  if ('E2E_SCENARIO' in process.env) {
    throw new Error(
      `globalSetup: E2E_SCENARIO=${JSON.stringify(process.env.E2E_SCENARIO)} 對 default／controls 入口無效且被禁止——`
      + 'scenario 執行請改用 npm run test:e2e:scenario（playwright.scenario.config.ts）',
    );
  }
  const runId = process.env.E2E_RUN_ID;
  const artifactsDir = process.env.E2E_ARTIFACTS_DIR;
  if (!runId || !artifactsDir) {
    throw new Error('globalSetup: E2E_RUN_ID／E2E_ARTIFACTS_DIR 未設定——必須透過 npm run test:e2e（e2e/scripts/run-e2e.mjs）啟動');
  }
  const __dirname = path.dirname(fileURLToPath(import.meta.url));
  const frontendRoot = path.resolve(__dirname, '..');
  const repoRoot = path.resolve(frontendRoot, '..');
  const artifactsRoot = path.resolve(artifactsDir, '..');

  const log = new HarnessLogger(artifactsDir);
  runtime.log = log;
  runtime.artifactsDir = artifactsDir;
  runtime.artifactsRoot = artifactsRoot;
  runtime.setupPid = process.pid;
  log.log(`globalSetup 開始：runId=${runId} pid=${process.pid} repoRoot=${repoRoot}`);

  // B3a-2b-2 Task C 第三輪限縮補正（缺陷 1）：入口標記——globalTeardown 的
  // `determineExecutionMode` 用這個檔案正面判定「這次執行是從哪個入口啟動
  // 的」，不再從（可能被竄改／型別錯誤／缺漏的）scenario identity 欄位反推。
  // 寫在最開頭（任何預檢／spawn 之前），即使後面失敗也留得下這份標記。
  fs.writeFileSync(path.join(artifactsDir, 'execution-entry.json'), JSON.stringify({ entry: 'default' }, null, 2));

  if (userFlags.injectPsFailure()) { // N12 注入點：讓 ps／lsof 指向會失敗的替身
    const fakeBinDir = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a1-fake-ps-'));
    for (const tool of ['ps', 'lsof']) {
      const p = path.join(fakeBinDir, tool);
      fs.writeFileSync(p, '#!/bin/sh\nexit 2\n');
      fs.chmodSync(p, 0o755);
    }
    process.env.PATH = `${fakeBinDir}:${process.env.PATH ?? ''}`;
    log.log(`E2E_INJECT_PS_FAILURE=1：ps／lsof 已指向會失敗的替身（${fakeBinDir}，永遠 exit 2），驗證觀測失敗不會被誤判成通過`);
  }

  if (userFlags.injectPsExit1Stderr()) { // N12 擴充注入點（缺口 B）：exit 1 但有 stderr 輸出，不是合法空結果的慣例
    const fakeBinDir = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a1-fake-ps-exit1-'));
    for (const tool of ['ps', 'lsof']) {
      const p = path.join(fakeBinDir, tool);
      fs.writeFileSync(p, '#!/bin/sh\necho "permission denied" 1>&2\nexit 1\n');
      fs.chmodSync(p, 0o755);
    }
    process.env.PATH = `${fakeBinDir}:${process.env.PATH ?? ''}`;
    log.log(`E2E_INJECT_PS_EXIT1_STDERR=1：ps／lsof 已指向會失敗的替身（${fakeBinDir}，exit 1 且印 "permission denied" 到 stderr），驗證 exit 1 不會被無條件當成合法空結果`);
  }

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
  const cli = createFakeCliSet(toolsDir, runId);
  log.log(`假 CLI 建立完成：${toolsDir}`);

  if (userFlags.injectFixtureReadOnly()) { // N4 注入點：fixture 不可寫
    fs.chmodSync(fixture.root, 0o555);
    log.log('E2E_INJECT_FIXTURE_READONLY=1：已把 fixture 根目錄改成唯讀，模擬不可寫');
  }

  try {
    checkFixture(fixture, repoRoot, log);
    checkFakeCli(cli, artifactsDir, log);
    // N3 注入點：模擬「缺少 WORKBENCH_TOOLS_DIR 設定」——只影響這裡的檢查用值，
    // 不影響 toolsDir 本身（假 CLI 仍建在真正的 toolsDir，只是這個檢查要看到
    // 空字串才會如預期在 spawn 之前失敗）。
    const toolsDirForCheck = userFlags.injectMissingToolsDirEnv() ? '' : toolsDir;
    if (userFlags.injectMissingToolsDirEnv()) log.log('E2E_INJECT_MISSING_TOOLS_DIR_ENV=1：模擬 WORKBENCH_TOOLS_DIR 未設定');
    checkEnvValues(fixture.root, toolsDirForCheck, log);
    checkPortFree(WAILS_PORT, log);
    checkArtifactModeAgainstHead(repoRoot, log);
  } catch (e) {
    if (e instanceof PreflightError) {
      log.log(`預檢未通過，未啟動 app：${e.message}`);
    }
    // 預檢失敗時 app 從未啟動，沒有程序要清理；但 fixture 已經建出來了（含
    // N4 可能改過的權限），這裡負責還原權限並清掉，避免每次負控制跑完都在
    // /tmp 留下垃圾或唯讀目錄。
    try { fs.chmodSync(fixture.root, 0o755); } catch { /* 可能本來就沒改過權限 */ }
    try { fs.rmSync(fixture.root, { recursive: true, force: true }); } catch { /* 盡力而為 */ }
    throw e;
  }

  // Q3：預檢全部通過（含 mode 對 HEAD）之後，落地「執行前」的內容快照——
  // globalTeardown 會拿這份跟「執行後」比對，抓這次執行本身有沒有動到這些
  // 檔案的內容（不管它們原本是不是已經跟 HEAD 不同）。落成檔案（不只留在
  // runtime 記憶體）是為了跟 run-env.json 同一套「同行程優先、檔案是保險」
  // 的既有模式一致。
  const artifactScopeFiles = listScopedFiles(repoRoot);
  const artifactBaselineSnapshot = snapshotArtifacts(repoRoot, artifactScopeFiles);
  fs.writeFileSync(
    path.join(artifactsDir, 'artifact-integrity-baseline.json'),
    JSON.stringify({ files: artifactScopeFiles, snapshot: artifactBaselineSnapshot }, null, 2),
  );
  runtime.artifactBaselineSnapshot = artifactBaselineSnapshot;

  if (userFlags.fakeCliBadVersionAfterPreflight()) { // N5 注入點：必須在預檢全部通過之後才生效
    injectBadVersionAfterPreflight(toolsDir);
    log.log('E2E_FAKE_CLI_BAD_VERSION_AFTER_PREFLIGHT=1：預檢已通過，現在才把假版本字串改成不符——後續啟動後核對應失敗');
  }

  const runState = new RunStateStore(artifactsDir, artifactsRoot, runId);
  runtime.runState = runState;

  const processTree = new ProcessTree(repoRoot, artifactsDir, runState, log);
  runtime.processTree = processTree;

  const networkSampler = new NetworkSampler(artifactsDir, runState, userFlags.treatLoopbackAsExternal());
  runtime.networkSampler = networkSampler;

  installSignalHandlers(log);

  // N10 測試專用協調點（reviewer 二次審查，2026-09-15）：真的 wails dev 從
  // 「vite 子行程出現」到「HTTP 就緒」之間的窗口長短受系統負載影響，沒辦法
  // 保證每次都能在「還沒 ready」那個時間點命中；改用可控的假啟動器（同一套
  // spawn／追蹤／停止程序，只換掉實際執行的指令）——假啟動器會立刻 spawn
  // 一個 vite-like 的非同 pgid 後代，自己永遠不 bind 34115，所以「還沒
  // ready」這個條件永遠成立，只剩「vite-like 後代有沒有出現」這一個決定性
  // 條件要等。是否用假啟動器、還是用真的 wails dev，都清楚寫進 harness.log
  // （見 spawnWailsDev 內的 spawn 那一行），證據不會混在一起。
  const n10Override = userFlags.n10FakeStarter()
    ? {
      command: process.execPath,
      args: [
        '-e',
        'const cp=require("child_process");'
          + 'const c=cp.spawn(process.execPath,["-e","setInterval(()=>{},600000)","--","vite-fake-descendant"],{detached:true,stdio:"ignore"});'
          + 'c.unref();'
          + 'setInterval(()=>{},600000);',
      ],
    }
    : undefined;
  if (userFlags.injectPostSpawnPsFailure()) { // R2 注入點：spawn 成功後、第一次 post-spawn 的 ps 呼叫失敗
    const fakeBinDir = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a1-fake-ps-postspawn-'));
    const markerFile = path.join(fakeBinDir, '.fired');
    // macOS 固定路徑；這條 harness 本來就是 macOS 專用（見 psUtil.ts 開頭註解）。
    const realPsPath = '/bin/ps';
    const script = `#!/bin/sh
if [ -f "${markerFile}" ]; then
  exec "${realPsPath}" "\$@"
fi
touch "${markerFile}"
exit 2
`;
    const p = path.join(fakeBinDir, 'ps');
    fs.writeFileSync(p, script);
    fs.chmodSync(p, 0o755);
    process.env.PATH = `${fakeBinDir}:${process.env.PATH ?? ''}`;
    log.log(`E2E_INJECT_POST_SPAWN_PS_FAILURE=1：ps 已指向自動 disarm 的替身（${fakeBinDir}），第一次呼叫會 exit 2，之後恢復呼叫真正的 ps——只用來驗證「spawn 之後第一次身分觀測失敗」這條路徑，不是對真實 Wails 做故障注入`);
  }

  // R2 修正（reviewer 三次審查，2026-09-16）：spawnWailsDev 本身已經改成
  // 拿到 pid 就立刻登記（見 processTree.ts），身分補強失敗不會再讓它拋出；
  // 這裡的 try/catch 是最後一道防線，涵蓋「取得 pid 都失敗」（`!pid` 那個
  // Error）與 `networkSampler.start()` 的同步寫檔失敗這類 spawn 之後、還沒
  // 進入正常等待迴圈之前就可能發生的例外——一旦真的走到這裡，代表
  // spawnWailsDev 可能已經真的啟動了一個行程（`this.child` 早就設好了），
  // 不能讓例外直接往上炸穿、留下沒人收尾的行程。single-flight 的
  // `processTree.stop()`（透過 teardownOnFailure）在這裡執行，不管呼叫
  // 幾次都只會真的動作一次。
  let child: ReturnType<typeof processTree.spawnWailsDev>;
  try {
    child = processTree.spawnWailsDev({ workspaceDir: fixture.root, toolsDir }, n10Override);
    networkSampler.start(); // 從 spawn 起（含啟動期間）取樣，見 §2.7。
  } catch (e) {
    // R1/R2 追加修正（reviewer 三次審查，2026-09-16）：`log.log`／
    // `setStatus` 本身也可能失敗（例如 harness.log 所在磁碟已滿），先前
    // 這兩行沒有保護，一旦其中之一拋出，下面真正做清理的
    // `teardownOnFailure` 就永遠不會被呼叫到——變成「以為有收尾保證，
    // 實際上收尾這一步本身可能被跳過」。改成各自包一層 try/catch（記錄
    // 失敗頂多印到 stderr，不讓它擋住真正的清理），保證不管記錄動作成不
    // 成功，`teardownOnFailure` 都會被呼叫到。
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

  if (userFlags.injectIgnoreTerm()) { // N13 注入點：自家 fixture 程序忽略 SIGTERM，驗證會升級到 SIGKILL
    const ignoreTermChild = spawn('bash', ['-c', 'trap "" TERM; sleep 300'], { detached: true, stdio: 'ignore' });
    const itPid = ignoreTermChild.pid;
    if (itPid) {
      const startedAt = processStartedAt(itPid) ?? new Date().toString();
      const command = processCommand(itPid) ?? 'bash -c trap "" TERM; sleep 300';
      const itPgid = processPgid(itPid) ?? itPid;
      runState.addProcess({ pid: itPid, ppid: process.pid, pgid: itPgid, command, startedAt, samePgid: false });
      log.log(`E2E_INJECT_IGNORE_TERM=1：已啟動忽略 SIGTERM 的 fixture 程序 pid=${itPid}（pgid=${itPgid}），已登記為追蹤中的非同 pgid 後代`);
    }
  }

  // R4 修正（reviewer 三次審查，2026-09-16，tsc 實跑抓到 TS2339，另外用最小
  // 案例單獨重現確認過）：`let childExited: X | null = null` 這種寫法，只要
  // 之後在 `while` 迴圈裡對它做 truthy narrowing，TS 的 control flow
  // analysis 會把它 narrow 成 `never`——這是 TS 對「只透過 closure 重新賦值
  // 的 let 變數，在迴圈裡做 narrowing」這個特定組合的已知限制，跟這裡的
  // 邏輯本身是否正確無關（拿掉迴圈、拿掉 closure 分別試過，只有兩者同時
  // 出現才會 narrow 成 never）。改成用一個裝箱物件（box）存這個值——物件的
  // 屬性讀取不會踩到同一個 narrowing 限制，經最小案例單獨驗證過確實能正常
  // narrow，不需要 `any`／`as`／`ts-ignore`。
  const childExitedBox: { value: { code: number | null; signal: NodeJS.Signals | null } | null } = { value: null };
  child.on('exit', (code, signal) => {
    childExitedBox.value = { code, signal };
    log.log(`wails dev 行程結束：exit code=${code} signal=${signal}`);
  });

  let forceFailTimer: NodeJS.Timeout | null = null;
  if (userFlags.forceFailBeforeReady()) { // N10 注入點
    // R4 二次審查（2026-09-15）：先前用固定延遲猜「vite 應該已經冒出來、
    // 但還沒 ready」的時間點，系統負載一快就會命中「ready 之後」而不是
    // 「ready 之前」，不是決定性的觸發條件。改成實際觀測，且**每一輪直接
    // 重新查即時的行程樹**（不靠 run-state.json 的 1s 週期快照，那份快照
    // 本身就可能讓判斷晚最多 1s）——查到非同 pgid 的 vite／npm run dev 後代
    // （沿用 networkSampler.ts 對這個分類的判斷方式），**且**這時 34115
    // 還沒可達，才送 SIGKILL；只要偵測到已經 ready 就不觸發（改成該次不算
    // 數而不是硬殺，因為目的是驗證「ready 前」這條路徑，不是「隨便找個時間
    // 點殺」）。搭配 `E2E_N10_FAKE_STARTER=1` 時，假啟動器永遠不會 ready，
    // 這個條件只剩「vite-like 後代出現」是變數，變成決定性的。
    log.log('E2E_FORCE_FAIL_BEFORE_READY=1：等待非同 pgid 的 vite／npm run dev 後代出現、且 34115 尚未可達時，才強制中止（每 200ms 直接查即時行程樹）');
    forceFailTimer = setInterval(() => {
      void (async () => {
        if (childExitedBox.value || !forceFailTimer) return;
        let viteDescendant: { pid: number; command: string } | undefined;
        try {
          const table = snapshotProcessTableWithCommand();
          const descendants = descendantsOf(child.pid!, table);
          viteDescendant = descendants.find(d => {
            const cmd = d.command.toLowerCase();
            return cmd.includes('vite') || cmd.includes('npm run dev') || cmd.includes('esbuild');
          });
        } catch {
          return; // 這一輪觀測失敗就跳過，下一輪再試——不是這個負控制要驗證的重點路徑。
        }
        if (!viteDescendant) return;
        const reachable = await httpReachable(WAILS_PORT);
        if (!forceFailTimer) return; // 這次非同步檢查跑的時候，可能已經被別處清掉了。
        if (reachable) {
          // 已經 ready 了，不是我們要驗證的「ready 前」窗口——停止繼續嘗試，
          // 讓它走正常 ready 路徑，不要硬殺一個已經成功啟動的執行。
          clearInterval(forceFailTimer);
          forceFailTimer = null;
          log.log('E2E_FORCE_FAIL_BEFORE_READY=1：偵測到 vite 後代出現時 34115 已經可達，判定沒有命中「ready 前」窗口，不觸發強制中止');
          return;
        }
        clearInterval(forceFailTimer);
        forceFailTimer = null;
        log.log(`E2E_FORCE_FAIL_BEFORE_READY=1：偵測到非同 pgid 後代 pid=${viteDescendant.pid}（${viteDescendant.command}）已出現、且 34115 仍未可達，強制送出 SIGKILL`);
        try { process.kill(-child.pid!, 'SIGKILL'); } catch { /* 可能已結束 */ }
      })();
    }, 200);
  }

  const startupTimeoutMs = userFlags.startupTimeoutMs();
  const deadline = Date.now() + startupTimeoutMs;
  log.log(`等待 wails dev 就緒（HTTP 可達 127.0.0.1:${WAILS_PORT}），上限 ${startupTimeoutMs}ms`);

  let ready = false;
  while (Date.now() < deadline) {
    // 讀一次快照（見上面 childExitedBox 宣告處的說明：為什麼用裝箱物件）。
    const exitedSnapshot = childExitedBox.value;
    if (exitedSnapshot) {
      if (forceFailTimer) { clearInterval(forceFailTimer); forceFailTimer = null; }
      runState.setStatus('failed', 'startup failed: wails dev exited before ready');
      log.log('wails dev 在 ready 前自行結束，走「部分啟動失敗」收尾路徑');
      await teardownOnFailure(log, processTree, networkSampler, runState, 'startup failed');
      throw new Error(`wails dev 在 ready 前結束（code=${exitedSnapshot.code} signal=${exitedSnapshot.signal}），見 wails-dev.log`);
    }
    if (await httpReachable(WAILS_PORT)) { ready = true; break; }
    await sleep(500);
  }
  if (forceFailTimer) { clearInterval(forceFailTimer); forceFailTimer = null; } // 保險：不管走到哪條路徑，離開這個函式前都不留計時器。

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
  });

  log.log('globalSetup 完成');
}

// teardownOnFailure：涵蓋「部分啟動失敗」與「啟動逾時」兩條路徑。這裡就是
// 停止程序真正執行、也真正知道最終是否 clean 的地方——不能只靠 global-
// teardown.ts 事後判斷（那邊在這兩種情況下常常連 run-env.json 都讀不到，
// 見 readRunEnv 的失敗退路），所以 .active-run.json 指標的去留就地決定：
// clean 就清掉，不 clean 就留著給下次執行的前次殘留檢查接手。
async function teardownOnFailure(
  log: HarnessLogger, processTree: ProcessTree, networkSampler: NetworkSampler,
  runState: RunStateStore, stage: string,
): Promise<void> {
  networkSampler.stop();
  const result = await processTree.stop();
  const cleanupClean = result.clean && result.portsReleased;
  // R1 追加修正（reviewer 五次審查，2026-09-16，N13 重跑抓到：`clean=false`
  // 但 `residualPids` 是空的，log 完全看不出原因）：把 `observationFailed`／
  // `abandonedPids` 一併印出來，`clean` 不再是一個沒人能追查原因的布林值。
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
        // 同步、在任何 await 之前就寫入——globalTeardown 若剛好同時被
        // Playwright 自己的 SIGINT 處理觸發，讀到這個狀態時一定已經是
        // interrupted（不會是還沒更新的舊狀態），才能正確跳過 PASSED 判定。
        runtime.runState?.setStatus('interrupted', `interrupted: ${sig}`);
        runtime.networkSampler?.stop();
        if (runtime.processTree) {
          // single-flight：這裡呼叫的 `stop()` 如果 globalTeardown 也同時
          // 呼叫到，兩邊會拿到同一個 in-flight promise，只送一次信號。
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
          // 中斷本身就是終態，這裡只補記清理是否完整，不要把 status 改回
          // 別的值——'interrupted' 要保留到最後（見 global-teardown.ts 的
          // wasInterrupted 判定，不會被覆寫成 'stopped'）。
          runtime.runState?.setStatus('interrupted', cleanupClean ? `interrupted: ${sig}` : `interrupted: ${sig} (cleanup incomplete)`);
          if (cleanupClean) {
            runtime.runState?.clearActiveRunPointer();
            log.log('清理乾淨，已移除 .active-run.json 指標');
          } else {
            log.log('清理未完全成功，保留 .active-run.json 指標供下次執行的前次殘留檢查接手');
          }
        }
        // 這裡自己印出最終結果，不能只賭 globalTeardown 有機會跑到它自己的
        // 判定與「最終結果」那一行——這個 handler 接下來就要強制
        // `process.exit(130)`，globalTeardown 就算同時被 Playwright 自己的
        // SIGINT 處理觸發，也可能還沒跑到那一行就被這裡的強制結束打斷（不是
        // 每次都會，屬於行程排程的競態）。這裡是唯一保證會執行到、而且確實
        // 知道「這次執行被中斷」的地方，所以由這裡負責留下這行不會遺漏的紀錄。
        const artifactsDirForLog = runtime.artifactsDir;
        console.error(`[e2e] 執行被中斷（interrupted: ${sig}），證據目錄：${artifactsDirForLog ?? '(未知)'}`);
        log.log(`最終結果：INTERRUPTED（interrupted: ${sig}），保留證據目錄 ${artifactsDirForLog ?? '(未知)'}`);
        process.exit(130);
      })();
    });
  }
}
