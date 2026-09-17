// 網路取樣（§2.7 取樣部分——不含 sandbox-exec 阻斷驗證，那是另一項施工任務）。
//
// 從 `wails dev` spawn 起（含啟動期間）到停止程序開始為止，每 1s 對自家程序樹
// 執行一次 `lsof -nP -iTCP -iUDP`。
//
// **觀測範圍**（F2 review 後維持不變）：Playwright `playwright test` 主行程的
// 後代（globalSetup／globalTeardown 與 worker 同行程，已實測確認）∪
// run-state.json 記錄的程序（涵蓋 ps 樹上可能因雙 fork 脫離主行程後代關係、
// 但仍是我們自己 spawn 出來的程序）。
//
// **F2 修正**（reviewer 2026-09-15 指出）：先前把「觀測範圍內不在 run-state.json
// 的程序」一律標成 chrome，導致兩類誤判：(a) 剛冒出來、run-state 的追蹤 tick
// 還沒寫入的 wails 子程序；(b) 取樣器自己這一輪呼叫的 ps／lsof 子行程（它們也是
// 主行程的後代）。改為依命令列分類：
//   - wails／go 建置：wails dev 本體與其建置期子行程（go build/list/vet、
//     clang、dsymutil、codesign、link 等）——用「這一刻」重新算一次
//     wails root 的後代樹（不靠 run-state 的落後快照），才抓得到剛冒出來的。
//   - app 本體：打包後的 sdlc-workbench 執行檔（開發模式建置產物，不是
//     `go build ... -o .../sdlc-workbench-dev-...` 那個建置指令本身）。
//   - vite／node：`npm run dev`／`vite`／其子行程（esbuild 等）。
//   - Chrome：命令列含 "Google Chrome"（涵蓋主行程與各 Helper）。
//   - 取樣器自己的工具程序：這一輪呼叫的 `ps`／`lsof`——**不列入受觀測範圍**
//     （不送進 lsof 目標、不計入任何類別的 pid 清單，只在取樣當下短暫存在）。
//   - 其他：以上都對不上的，仍在觀測範圍內、仍會被 lsof 查，只是分類欄位
//     顯示「其他」，方便之後追查是不是又冒出新的未分類程序。
//
// 限制：兩次取樣之間的短暫連線可能漏掉，只能證明「取樣時點沒有外連」。
//
// Q1（reviewer 二次審查，2026-09-15）：先前對整行套 loopback regex 判斷，會
// 讓「本機端位址剛好含 127、但遠端其實是外部位址」的連線被誤判成 loopback
// 而放行。改用 `networkLineParser.ts` 的純函式，依「已連線（看遠端）／
// LISTEN（看本機 bind 位址，wildcard 一律違規）／兩者都不是（只記診斷，不
// 能宣稱沒外連過）／無法解析（保留原文、判觀測失敗）」四種情況分別判斷。
import fs from 'node:fs';
import path from 'node:path';
import { parseNetworkLine } from './networkLineParser.js';
import { descendantsOf, lsofForPids, ProcRowWithCommand, snapshotProcessTableWithCommand, ToolObservationError } from './psUtil.js';
import { RunStateStore } from './runState.js';
import { userFlags } from './env.js';

function isSelfToolCommand(cmd: string): boolean {
  // 取樣器（psUtil.ts）都是用相對／絕對指令名稱直接 exec，不經 shell，所以
  // 命令列一定以 "ps " 或 "lsof " 開頭（沒有前置路徑包裝）。
  return /^(ps|lsof)\s/.test(cmd);
}

function isChromeCommand(cmd: string): boolean {
  return cmd.includes('Google Chrome');
}

function isAppBinaryCommand(cmd: string): boolean {
  // 開發模式建置出的執行檔路徑固定是 build/bin/sdlc-workbench.app/.../sdlc-workbench
  // （見 wails-dev.log 的 codesign／啟動那幾行）；建置指令本身雖然也含
  // "sdlc-workbench" 字樣（-o .../sdlc-workbench-dev-...），但那是 "go build"
  // 開頭，用「不是 go 開頭」排除。
  return cmd.includes('sdlc-workbench') && !cmd.startsWith('go ');
}

function isViteNodeCommand(cmd: string): boolean {
  return cmd.includes('vite') || cmd.includes('npm run dev') || cmd.includes('esbuild');
}

interface Categorized {
  wailsBuild: number[];
  app: number[];
  viteNode: number[];
  chrome: number[];
  selfTool: number[];
  other: number[];
}

export class NetworkSampler {
  private timer: NodeJS.Timeout | null = null;
  private readonly logFile: string;
  private violationCount = 0;
  private readonly ownRootPid = process.pid;

  constructor(
    private readonly artifactsDir: string,
    private readonly state: RunStateStore,
    private readonly treatLoopbackAsExternal: boolean,
  ) {
    this.logFile = path.join(artifactsDir, 'network-samples.log');
    fs.writeFileSync(this.logFile, '');
  }

  start(): void {
    this.sampleOnce();
    this.timer = setInterval(() => this.sampleOnce(), 1000);
  }

  stop(): void {
    if (this.timer) {
      clearInterval(this.timer);
      this.timer = null;
    }
    // R3 定點反證專用：先前試過在 spec 結尾直接刪 network-samples.log，結果
    // 被取樣器自己下一輪的 `fs.appendFileSync`（找不到檔案就會自動建立）
    // 重新生出來，等 globalTeardown 讀的時候檔案早就「復活」了，反證失敗。
    // 改成在這裡（已經 `clearInterval`，保證不會再有下一輪取樣）刪除，
    // 沒有競態視窗，globalTeardown 接下來讀到的才是真的「檔案缺失」。
    if (userFlags.injectDeleteNetworkLog()) {
      try { fs.rmSync(this.logFile, { force: true }); } catch { /* 盡力而為 */ }
    }
  }

  violations(): number {
    return this.violationCount;
  }

  private categorize(table: ProcRowWithCommand[]): { categorized: Categorized; commandOf: Map<number, string> } {
    const commandOf = new Map<number, string>(table.map(r => [r.pid, r.command]));

    const rootPgid = this.state.get().rootPgid;
    // 這一刻重新算 wails 後代樹（不靠 run-state.json 的落後快照），才抓得到
    // 剛冒出來、tracker 還沒來得及寫入的子行程。
    const wailsTreePidsFresh = new Set<number>(
      rootPgid > 0 ? [rootPgid, ...descendantsOf(rootPgid, table).map(p => p.pid)] : [],
    );
    const runStatePids = new Set(this.state.get().processes.map(p => p.pid));
    for (const p of this.state.get().processes) {
      if (!commandOf.has(p.pid)) commandOf.set(p.pid, p.command); // 程序已消失，退回落地紀錄的命令列
    }

    const ownTreePids = descendantsOf(this.ownRootPid, table).map(p => p.pid);
    const observedPids = [...new Set([...runStatePids, ...ownTreePids])];

    const categorized: Categorized = { wailsBuild: [], app: [], viteNode: [], chrome: [], selfTool: [], other: [] };
    for (const pid of observedPids) {
      const cmd = commandOf.get(pid) ?? '';
      if (isSelfToolCommand(cmd)) { categorized.selfTool.push(pid); continue; }
      if (isChromeCommand(cmd)) { categorized.chrome.push(pid); continue; }
      if (wailsTreePidsFresh.has(pid) || runStatePids.has(pid)) {
        if (isAppBinaryCommand(cmd)) categorized.app.push(pid);
        else if (isViteNodeCommand(cmd)) categorized.viteNode.push(pid);
        else categorized.wailsBuild.push(pid);
        continue;
      }
      categorized.other.push(pid);
    }
    return { categorized, commandOf };
  }

  // R2：這一輪 ps／lsof 本身觀測失敗（不是「查得到但沒有符合結果」）時，
  // 不能靜默跳過當作「這輪沒有違規」——那等於用「看不到」冒充「乾淨」。
  // 改成把這一輪記成觀測失敗（寫進 network-samples.log 與 run-state.json
  // 的 observationFailures），並讓 violationCount 一起增加，確保整次執行
  // 不會因為某幾輪剛好觀測失敗就被判成通過。
  private sampleOnce(): void {
    const ts = new Date().toISOString();
    let table: ReturnType<typeof snapshotProcessTableWithCommand>;
    let categorized: Categorized;
    let raw: string;
    try {
      table = snapshotProcessTableWithCommand();
      categorized = this.categorize(table).categorized;
      const targetPids = [
        ...categorized.wailsBuild, ...categorized.app, ...categorized.viteNode,
        ...categorized.chrome, ...categorized.other,
      ];
      raw = lsofForPids(targetPids);
    } catch (e) {
      const reason = e instanceof ToolObservationError ? e.message : String(e);
      fs.appendFileSync(this.logFile, `=== ${ts} OBSERVATION-FAILURE: ${reason} ===\n`);
      this.violationCount += 1;
      this.state.recordObservationFailure(`network sampler: ${reason}`);
      return;
    }
    const lines = raw.split('\n').filter(l => l.trim().length > 0 && !l.startsWith('COMMAND'));

    // reviewer 必修項目：Chrome 背景網路照樣記錄（診斷用），但不列入預設
    // suite 的失敗判定——只有「確實辨識為本次 Playwright 啟動的 Chrome」
    // 才排除；未辨識出來源（other／取樣失敗）一律照嚴格判定。N9a
    // （E2E_TREAT_LOOPBACK_AS_EXTERNAL）驗證的是判定機制本身，對所有分類
    // 一視同仁（含 Chrome），才能證明旗標真的有作用。
    //
    // Q1：改用 `parseNetworkLine` 逐行判斷（見該檔頂端註解），不再對整行套
    // loopback regex。四種結果分開處理：
    //   - unparseable：保留原文，算觀測失敗（不能跳過當沒事發生）。
    //   - no-remote：只記診斷，不計入 violationCount（沒有遠端可比對，不能
    //     宣稱「沒有外連過」）。
    //   - connected／listen 且 violation=true：依是否為已辨識 Chrome 分流到
    //     「背景診斷」或「違規」。
    const chromePidSet = new Set(categorized.chrome);
    const violatingLines: string[] = [];
    const chromeBackgroundLines: string[] = [];
    const noRemoteLines: string[] = [];
    const unparseableLines: string[] = [];
    for (const line of lines) {
      const parsed = parseNetworkLine(line, this.treatLoopbackAsExternal);
      if (parsed.kind === 'unparseable') { unparseableLines.push(line); continue; }
      if (parsed.kind === 'no-remote') { noRemoteLines.push(line); continue; }
      if (!parsed.violation) continue; // 乾淨的 connected／listen，原始行已經整份寫進 log，不用再另外標記。
      const pid = Number(line.trim().split(/\s+/)[1]);
      const isChromeForced = !this.treatLoopbackAsExternal && chromePidSet.has(pid);
      if (isChromeForced) chromeBackgroundLines.push(line);
      else violatingLines.push(line);
    }

    const header = `=== ${ts} `
      + `wails=[${categorized.wailsBuild.join(',')}] `
      + `app=[${categorized.app.join(',')}] `
      + `viteNode=[${categorized.viteNode.join(',')}] `
      + `chrome=[${categorized.chrome.join(',')}] `
      + `selfTool(excluded)=[${categorized.selfTool.join(',')}] `
      + `other=[${categorized.other.join(',')}] ===\n`;
    fs.appendFileSync(this.logFile, header);
    if (lines.length > 0) fs.appendFileSync(this.logFile, lines.join('\n') + '\n');
    if (unparseableLines.length > 0) {
      // 無法解析＝觀測失敗，不能跳過；讓整次執行不能被判成乾淨。
      this.violationCount += unparseableLines.length;
      this.state.recordObservationFailure(
        `network sampler: ${unparseableLines.length} 行 lsof 輸出無法解析（見 network-samples.log UNPARSEABLE 標記，時間戳 ${ts}）`,
      );
      fs.appendFileSync(this.logFile, unparseableLines.map(l => `UNPARSEABLE (observation failure): ${l}`).join('\n') + '\n');
    }
    if (noRemoteLines.length > 0) {
      // 診斷用：只能證明「這一刻沒觀察到遠端」，不代表「沒有外連過」，不計入 violationCount。
      fs.appendFileSync(
        this.logFile,
        noRemoteLines.map(l => `NO-REMOTE (diagnostic only, not counted — 沒有觀察到遠端，不代表沒有外連過): ${l}`).join('\n') + '\n',
      );
    }
    if (chromeBackgroundLines.length > 0) {
      // 診斷用，不計入 violationCount、不影響預設 suite 的失敗判定。
      fs.appendFileSync(this.logFile, chromeBackgroundLines.map(l => `CHROME-BACKGROUND (not counted): ${l}`).join('\n') + '\n');
    }
    if (violatingLines.length > 0) {
      this.violationCount += violatingLines.length;
      fs.appendFileSync(this.logFile, violatingLines.map(l => `NETWORK-VIOLATION: ${l}`).join('\n') + '\n');
    }
  }
}
