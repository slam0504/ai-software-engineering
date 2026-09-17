// spawn `wails dev -noreload`（獨立程序群組）＋ 從 spawn 起持續走訪整棵後代樹
// （含非同 pgid 的），每偵測到新的就寫進 run-state.json（§2.3）。
import { ChildProcess, spawn } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { descendantsOf, isPortListening, processCommand, processStartedAt, snapshotProcessTable, ToolObservationError } from './psUtil.js';
import { HarnessLogger } from './logger.js';
import { RunStateStore } from './runState.js';
import { userFlags } from './env.js';
import { isOfflineSandboxEnabled, SANDBOX_EXEC_PATH, sandboxProfilePath } from './offlineSandbox.js';
import { StopResult, stopProcessGroup } from './stopProcedure.js';

export interface SpawnEnv {
  workspaceDir: string;
  toolsDir: string;
}

export class ProcessTree {
  private child: ChildProcess | null = null;
  private trackTimer: NodeJS.Timeout | null = null;
  private wailsLogFd: number | null = null;
  // single-flight（reviewer 二次審查，2026-09-15，N8 實跑發現）：Playwright
  // 收到 SIGINT 時，我們自己註冊的訊號處理常式跟 Playwright 自己觸發的
  // globalTeardown 會**同時**呼叫這裡的 `stop()`——先前兩邊各自跑一次完整的
  // `stopProcessGroup`，對同一批 tracked pid 重複送信號（第二套送出時 pid
  // 可能已經死了甚至被系統重用給不相關的行程，屬於審查補充 A 提醒的風險）。
  // 改成整個 `stop()` 只允許真正執行一次：第一個呼叫者觸發實際的停止程序，
  // 後面所有呼叫者（不管是哪個路徑）都拿到同一個 in-flight promise、等它
  // resolve，共用同一份結果，不會再送第二次信號。
  private stopPromise: Promise<StopResult & { portsReleased: boolean; residualPorts: number[]; portCheckMs: number }> | null = null;

  constructor(
    private readonly repoRoot: string,
    private readonly artifactsDir: string,
    private readonly state: RunStateStore,
    private readonly log: HarnessLogger,
  ) {}

  // spawnWailsDev：detached:true → 新的 session／process group（pgid = pid，
  // 見 Node child_process 文件對 POSIX 的說明），供後續 group kill 使用。
  //
  // `override`：**只給 N10 這個負控制用**（見 global-setup.ts 的
  // `E2E_N10_FAKE_STARTER=1`）——真的 wails dev 從「vite 子行程出現」到
  // 「HTTP 就緒」之間的窗口長短會被系統負載影響，沒辦法穩定命中「ready 前」
  // 這個時間點；用一個可控的假啟動器（同樣走這支函式，一樣會被登記
  // root／同一套後代樹追蹤與停止程序）取代真正的 `wails dev` 執行檔，讓
  // 「vite-like 後代出現、但永遠不 ready」這件事變成決定性的，不用再猜
  // 時間。這是測試骨架的替身，不是修改 production 的啟動流程；證據會清楚
  // 標示是 fake 還是真的 wails（見 harness.log 的 spawn 那一行）。
  spawnWailsDev(env: SpawnEnv, override?: { command: string; args: string[] }): ChildProcess {
    const wailsLogPath = path.join(this.artifactsDir, 'wails-dev.log');
    this.wailsLogFd = fs.openSync(wailsLogPath, 'a');

    const childEnv: NodeJS.ProcessEnv = {
      ...process.env,
      WORKBENCH_WORKSPACE: env.workspaceDir,
      WORKBENCH_TOOLS_DIR: env.toolsDir,
      GOFLAGS: '-mod=readonly',
      GOPROXY: 'off',
      npm_config_offline: 'true',
    };
    // StartHidden（owner 授權的限定 production 修改，2026-09-16）：E2E 啟動
    // wails dev 預設帶這個旗標，避免雙螢幕環境下原生視窗跳出來干擾作業。
    // `main.go` 的 `e2eStartHidden()` 只認值恰好是 `"1"`。**只有這裡
    // （E2E 啟動器要給 app 子程序的 env）會設這個變數**，不寫進任何全域
    // shell／使用者設定／一般開發用的設定檔。`E2E_VERIFY_NO_START_HIDDEN=1`
    // 是驗證專用旗標：連 `process.env` 繼承下來的同名變數都會被清掉，確保
    // 子程序的 env 裡真的看不到它（不是只有呼叫端「沒設」）。
    if (userFlags.e2eVerifyNoStartHidden()) {
      delete childEnv.WORKBENCH_E2E_START_HIDDEN;
    } else {
      childEnv.WORKBENCH_E2E_START_HIDDEN = '1';
    }

    const command = override?.command ?? 'wails';
    // `-skipbindings`（reviewer 有條件核准，2026-09-16）：wailsjs mode 事故
    // 的根因是 `wails` v2.13.0 的 `generateBindings()` 在重新產生 bindings
    // 後，最後一步對整個 `frontend/wailsjs` 遞迴 `chmod 755`
    // （`internal/app/app_bindings.go:123`／`internal/fs/fs.go:272`，見
    // node3-part1-summary.md 第九輪的原碼追蹤）。`-skipbindings` 正是關掉
    // 這整條路徑的官方旗標（`cmd/wails/flags/buildcommon.go`／`dev.go:139`
    // 皆已確認 `wails dev` 支援）。
    //
    // **採用前已完成 reviewer 要求的驗證程序**：在隔離暫存副本（rsync 排除
    // `.git`／`node_modules`／`build`，執行完即刪，沒有動到這個 repo）用
    // 同一個 Wails v2.13.0 執行 `wails generate module`，把產生出來的
    // `frontend/wailsjs/**` 跟目前工作樹逐檔 `diff -rq`＋逐檔 sha256 交叉
    // 核對——**內容完全相同**（只有已知、另有協議處理的 mode 差異）。證據
    // 存於 `frontend/e2e/.artifacts/skipbindings-content-verification-*.txt`。
    //
    // **重要限制（不是一次性保證）**：這只證明「現在」committed 的
    // bindings 跟「現在」的 `app.go` `Bind` 曝露方法一致。**未來只要
    // `app.go` 的 `Bind` 方法簽章改變，就必須重新跑一次
    // `wails generate module` 並重新做上述內容比對**——`typecheck:e2e`
    // 通過或 `git diff` 內容沒變，都不能證明 bindings 跟目前的 Go API
    // 表面同步（`tsc` 只檢查型別語法本身正確，不知道 Go 那邊實際曝露了
    // 什麼）。既有的 Q3 mode／內容前後檢查（`artifactIntegrity.ts`）維持
    // 不變，沒有放寬。
    let args = override?.args ?? ['dev', '-noreload', '-skipbindings'];
    let finalCommand = command;
    // E2E offline 驗證模式（reviewer 授權實作，複核 #34 第六／七次，
    // 2026-09-16）：opt-in（`E2E_OFFLINE_SANDBOX=1`）才會把這裡實際 spawn 的
    // 指令包成 `sandbox-exec -f <profile> <command> <args...>`——harness 自己
    // （這個 Node 行程）維持在 sandbox 外，只有 `wails dev`／N10 假啟動器與
    // 它們的後代在 sandbox 內。`-viteservertimeout 60` 只在真正的 `wails
    // dev`（非 N10 override）加上，這是 reviewer 併案核准的候選值，**不影響
    // 預設路徑**、不改 TERM 10s／KILL 5s／啟動逾時等既有上限。
    if (isOfflineSandboxEnabled()) {
      const withTimeout = override ? args : [...args, '-viteservertimeout', '60'];
      // P1 修正：用 SANDBOX_EXEC_PATH（絕對路徑常數，跟
      // `validateOfflineSandboxPrereqs` 的預檢用同一個值），不是裸字串
      // `'sandbox-exec'`（那會交給呼叫端當下的 PATH 解析，跟預檢驗證的
      // `/usr/bin/sandbox-exec` 不是同一個解析契約，理論上可能命中 PATH
      // 上的另一支同名執行檔）。
      finalCommand = SANDBOX_EXEC_PATH;
      args = ['-f', sandboxProfilePath(), command, ...withTimeout];
    }
    this.log.log(`spawn: ${finalCommand} ${args.join(' ')}（cwd=${this.repoRoot}）${override ? '【N10 測試專用假啟動器，非真的 wails dev】' : ''}`);
    const child = spawn(finalCommand, args, {
      cwd: this.repoRoot,
      env: childEnv,
      detached: true,
      stdio: ['ignore', this.wailsLogFd, this.wailsLogFd],
    });
    this.child = child;

    const pid = child.pid;
    if (!pid) throw new Error('spawn wails dev 失敗：沒有拿到 pid');
    const pgid = pid; // detached:true 在 POSIX 上使子行程成為新 process group 的 leader。

    // R2 修正（reviewer 三次審查，2026-09-16，實測重現：spawn 成功後第一次
    // post-spawn 的 ps 觀測失敗，會讓已經真實啟動的行程完全沒有
    // state／tracker／.active-run.json，globalTeardown 在缺 env 的分支只會
    // 記一句話就 return，不執行停止程序——變成真的行程洩漏，且 log 還印出
    // 「已在 teardownOnFailure 內完成」這種誤導訊息，實際上從沒執行過）。
    // 拿到 pid 之後**立刻**用已知為真的資訊（spawn 用的 command／args、當下
    // 時間占位）登記——pgid 本身不需要 ps 也已知為真，是停止程序最關鍵的
    // 依據（R1 修正後，group kill 的目標完全來自這裡登記的 pgid，不再信任
    // 外部傳入值）。之後才「盡力補強」精確的 startedAt／command；補強失敗
    // 只記一筆 observationFailure，不讓整個 spawnWailsDev 因為這個補強步驟
    // 失敗而拋出、也不用假資料冒充「已取得的精確身分」。
    // 用 finalCommand（實際 spawn 的執行檔，offline sandbox 模式下是
    // `sandbox-exec`，不是 `wails`）＋wrapped 後的 args 當占位字串，比用
    // 還沒包 sandbox 的 `command` 變數更接近「這一刻真正 spawn 的東西」。
    // **但這只是暫時的占位，不是最終定案**：`sandbox-exec` 之後會
    // `execve()` 成真正的 `wails`（或 N10 的假啟動器），那個 exec 完成後
    // `ps` 量到的命令列會再變一次（從 `sandbox-exec -f ... wails dev
    // ...` 變成 `wails dev ...` 本身）——**不能宣稱這裡改用
    // `finalCommand` 就已經解決所有 A3 身分誤判**，真正的身分判定仍然要
    // 依賴下面緊接著的「盡力補強」（`processStartedAt`／`processCommand`
    // 實測）與 `heldChild`（Node 自己追蹤的 `ChildProcess`，不受 exec 後
    // 命令列改變影響）這兩個既有契約，這裡只是讓占位字串本身不要一開始
    // 就是明顯錯的。
    const placeholderCommand = `${finalCommand} ${args.join(' ')}`;
    const placeholderStartedAt = new Date().toString();
    this.state.recordRoot(pid, pgid, placeholderCommand, placeholderStartedAt);
    this.log.log(`wails dev spawned：pid=${pid} pgid=${pgid}（先以 spawn 資訊占位登記，state／tracker／.active-run.json 已就緒）`);

    try {
      const startedAt = processStartedAt(pid) ?? placeholderStartedAt;
      const commandLine = processCommand(pid) ?? placeholderCommand;
      this.state.updateProcessIdentity(pid, commandLine, startedAt);
      this.log.log(`wails dev 身分補強完成：pid=${pid} pgid=${pgid} startedAt=${startedAt}`);
    } catch (e) {
      const reason = e instanceof ToolObservationError ? e.message : String(e);
      this.log.log(`wails dev spawn 後第一次身分觀測失敗，沿用 spawn 占位資訊繼續（pid=${pid} 仍然完整登記、可被收尾）：${reason}`);
      this.state.recordObservationFailure(`post-spawn identity read for root pid=${pid}: ${reason}`);
    }

    this.startTracking(pid);
    return child;
  }

  // startTracking：每 1s 走訪一次整棵後代樹（不只等 ready 後才收集），任何新
  // 出現的 pid（含非同 pgid）都寫進 run-state.json。
  //
  // R2：`setInterval` 的 callback 裡如果丟出未捕捉的例外，會直接讓整個
  // Node 行程當掉（globalSetup／globalTeardown 都跑在這個行程裡）——不能
  // 讓「這一次 tick 觀測失敗」變成「整條 harness 連 log 都來不及寫就死掉」。
  // 改成在這裡攔下，記一筆 observationFailure 並繼續下一個 tick；這筆記錄
  // 會讓 globalTeardown 判定不能算乾淨（見 RunState.observationFailures）。
  private startTracking(rootPid: number): void {
    this.trackTimer = setInterval(() => {
      try {
        const table = snapshotProcessTable();
        const descendants = descendantsOf(rootPid, table);
        for (const d of descendants) {
          this.state.addDescendantIfNew(d.pid, d.ppid, d.pgid);
        }
      } catch (e) {
        const reason = e instanceof ToolObservationError ? e.message : String(e);
        this.log.log(`程序樹追蹤這一輪觀測失敗：${reason}`);
        this.state.recordObservationFailure(`process-tree tracking: ${reason}`);
      }
    }, 1000);
  }

  stopTracking(): void {
    if (this.trackTimer) {
      clearInterval(this.trackTimer);
      this.trackTimer = null;
    }
  }

  // recordVitePort：從 wails-dev.log 解析「Vite Server URL: http://localhost:PORT」。
  discoverVitePort(): number | null {
    const wailsLogPath = path.join(this.artifactsDir, 'wails-dev.log');
    if (!fs.existsSync(wailsLogPath)) return null;
    // eslint-disable-next-line no-control-regex
    const text = fs.readFileSync(wailsLogPath, 'utf8').replace(/\x1b\[[0-9;]*m/g, '');
    const m = text.match(/Vite Server URL:\s*http:\/\/localhost:(\d+)/);
    return m ? Number(m[1]) : null;
  }

  // stop：所有結束路徑共用的停止程序（§2.3）。single-flight——不管被呼叫
  // 幾次（SIGINT 處理常式、globalTeardown 的正常路徑、teardownOnFailure 都
  // 可能各自呼叫到這裡），只有第一次呼叫真的執行；後面的呼叫者拿到同一個
  // in-flight promise，等它 resolve 後共用同一份結果。
  async stop(): Promise<StopResult & { portsReleased: boolean; residualPorts: number[]; portCheckMs: number }> {
    if (this.stopPromise) return this.stopPromise;
    this.stopPromise = this.doStop();
    return this.stopPromise;
  }

  private async doStop(): Promise<StopResult & { portsReleased: boolean; residualPorts: number[]; portCheckMs: number }> {
    this.stopTracking();
    const st = this.state.get();
    // R1 修正（reviewer 三次審查，2026-09-16）：`st.processes` 只是歷史追蹤
    // 清單，不是「已驗證存活」的結果——`stopProcessGroup` 現在會自己重新
    // 驗證，這裡不用（也不能）先幫它濾過。root 這個 pid 我們仍直接持有
    // `this.child`（Node 的 ChildProcess 物件），一併傳進去，讓 root 的
    // 存活判斷改用 Node 自己的 exitCode／signalCode（比 ps 更可靠），其餘
    // 目標一律走 ps 身分驗證。
    const result = await stopProcessGroup(st.processes, this.log, this.child);
    this.log.log(
      `停止程序耗時：TERM ${result.termPhaseMs}ms、KILL ${result.killPhaseMs}ms`
      + `（escalatedToKill=${result.escalatedToKill}）`,
    );
    // R1 追加修正（reviewer 五次審查，2026-09-16，N13 重跑抓到）：先前
    // `clean=false` 但 `residualPids` 剛好是空的時候，log 完全沒說原因
    // （`observationFailed`／`abandonedPids` 都沒印出來）。這裡把 `clean`
    // 是怎麼算出來的講清楚，並把觀測失敗記進 run-state（不是只留在
    // log 文字裡）。
    if (result.observationFailed) {
      this.log.log('停止程序期間至少有一輪批次觀測本身失敗（不是查無結果），clean 判定為 false 的原因之一');
      this.state.recordObservationFailure('stopProcedure: 停止程序期間批次觀測失敗（詳見上方 harness.log 對應行）');
    }
    if (result.abandonedPids.length > 0) {
      this.log.log(`停止程序期間有 ${result.abandonedPids.length} 個目標身分確認不符、放棄追蹤，clean 判定為 false 的原因之一：${result.abandonedPids.join(',')}`);
    }
    // 複核 #34／A2／A4 新增：跟 abandonedPids（已確認不符）不同，這些是「無法
    // 確認」（觀測失敗當下沒有可靠持有依據，或 argv 取不到）——一樣不算乾淨，
    // 但沒有被送過任何信號，跟殘存／放棄追蹤要能分開看。
    if (result.unconfirmedPids.length > 0) {
      this.log.log(`停止程序期間有 ${result.unconfirmedPids.length} 個目標身分始終無法確認，clean 判定為 false 的原因之一：${result.unconfirmedPids.join(',')}`);
      this.state.recordObservationFailure(`stopProcedure: 有 ${result.unconfirmedPids.length} 個目標身分無法確認（詳見上方 harness.log 對應行）：${result.unconfirmedPids.join(',')}`);
    }
    if (result.diagnosticWriteFailed) {
      this.log.log('停止程序期間至少有一次 harness.log 寫入失敗，clean 判定為 false 的原因之一（不影響已送出的信號本身）');
      this.state.recordObservationFailure('stopProcedure: 停止程序期間至少有一次診斷寫入失敗（不影響已送出的信號，但這部分紀錄不完整，見 stderr）');
    }
    if (!result.clean && !result.observationFailed && !result.diagnosticWriteFailed
      && result.abandonedPids.length === 0 && result.residualPids.length === 0 && result.unconfirmedPids.length === 0) {
      // 理論上不該發生（clean 的算法只有這五個原因），留一行防禦性訊息，
      // 避免未來改動悄悄多了第六個「沒人記錄」的判 false 原因。
      this.log.log('警告：clean=false 但 residualPids／abandonedPids／unconfirmedPids／observationFailed／diagnosticWriteFailed 都看不出原因，這是 stopProcessGroup 判定邏輯本身的缺口，需要追查');
    }

    // R2：isPortListening 觀測失敗時不能當成「埠已釋放」——一律當成殘留
    // （portsReleased=false），並把原因記進 observationFailures。R4：埠核對
    // 耗時獨立計時，不要混進上面 stopProcessGroup 的 TERM／KILL grace time。
    const portCheckStart = process.hrtime.bigint();
    const portsToCheck = [st.ports.wails, ...(st.ports.vite ? [st.ports.vite] : [])];
    const residualPorts = portsToCheck.filter(port => {
      try {
        return isPortListening(port);
      } catch (e) {
        const reason = e instanceof ToolObservationError ? e.message : String(e);
        this.log.log(`收尾核對埠 ${port} 是否釋放時觀測失敗，視為殘留：${reason}`);
        this.state.recordObservationFailure(`port check for ${port}: ${reason}`);
        return true;
      }
    });
    const portCheckMs = Number((process.hrtime.bigint() - portCheckStart) / 1_000_000n);
    this.log.log(`埠核對耗時 ${portCheckMs}ms（獨立於 TERM／KILL grace time）`);

    if (this.wailsLogFd !== null) {
      try { fs.closeSync(this.wailsLogFd); } catch { /* already closed */ }
      this.wailsLogFd = null;
    }

    return { ...result, portsReleased: residualPorts.length === 0, residualPorts, portCheckMs };
  }

  getChild(): ChildProcess | null {
    return this.child;
  }
}
