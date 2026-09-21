// B3a-2b-2 F2：取得**本次執行中**的真 App binary 身分。
//
// 為什麼需要這支：核定 MCP binary identity 不能從待驗的 MCP config 反推
// （reviewer #365），也不能像我上一版那樣用一個「猜」的固定路徑——
// #372 的實際 run 證明 `wails dev` 在 macOS 不產出 `build/bin/<outputfilename>`，
// 而是 `go build -o build/bin/<name>-dev-<arch>` 之後執行
// `build/bin/<name>.app/Contents/MacOS/<name>`（run-state pid=50182）。
//
// 作法（reviewer #373 指定的最小方案）：
//   1. 依 wails.json 的 outputfilename ＋ 本機 macOS bundle 佈局算出**預期路徑**
//   2. 用**當次受控程序樹 ＋ 新鮮 ps 觀測**確認那確實是這次存活的 App
//      （pid／開始時間／command／屬於本次 root 的後代）
//   3. 再算 SHA，並保存 path/SHA/pid/startedAt 的獨立證據
//
// 明確不做：不只看歷史 run-state、不挑「command 裡含 sdlc-workbench 的第一個」、
// 不從 MCP config 反推、不做跨平台探索（非 darwin 一律明確拒絕）。
import fs from 'node:fs';
import crypto from 'node:crypto';
import path from 'node:path';
import {
  descendantsOf, isAlive, processCommand, processStartedAt,
  snapshotProcessTableWithCommand, type ProcRowWithCommand,
} from '../psUtil.js';

export interface AppBinaryIdentity {
  /** bundle 內實際被執行的檔案的 canonical path。 */
  canonicalPath: string;
  sha256: string;
  /** 本次存活 App 的 pid（來自受控程序樹）。 */
  pid: number;
  /** ps 的 lstart=，用來確認身分未變動。 */
  startedAt: string;
  command: string;
  /** 預期路徑（未 realpath），供證據對照。 */
  expectedPath: string;
}

/**
 * macOS 的 wails dev bundle 佈局：`<repoRoot>/build/bin/<name>.app/Contents/MacOS/<name>`。
 * 這是 #372 run-state 直接觀測到的實際版面，不是推測。
 */
export function expectedAppBundleBinary(repoRoot: string, outputFileName: string): string {
  return path.join(repoRoot, 'build', 'bin', `${outputFileName}.app`, 'Contents', 'MacOS', outputFileName);
}

/**
 * command 是否就是這支 binary——**只接受完全相等**。
 *
 * 為什麼不接受「後面接空白再帶參數」（reviewer #375）：`ps` 給的是扁平字串，
 * `"/…/sdlc-workbench other"` 無法分辨那是「binary ＋ 參數 other」還是
 * 「檔名本身就叫 `sdlc-workbench other`」。實測本 Wails App 的命令沒有參數，
 * 因此最小且不含糊的作法是精確相等（路徑內的空白照常保留）。
 * **不用 split(' ')**，也不另造泛用 argv parser。
 */
export function commandIsBinary(command: string, binaryPath: string): boolean {
  return command === binaryPath;
}

export interface ResolveAppBinaryOptions {
  repoRoot: string;
  outputFileName: string;
  /** 本次受控程序樹的 root pid（wails dev）。 */
  rootPid: number;
  /** 注入用：預設取新鮮的 ps 快照。 */
  table?: ProcRowWithCommand[];
  platform?: NodeJS.Platform;
  /** 注入用：身分再核對（預設走 psUtil 的單一查詢）。 */
  probe?: {
    isAlive(pid: number): boolean;
    startedAt(pid: number): string | null;
    command(pid: number): string | null;
  };
}

export function resolveAppBinaryIdentity(
  opts: ResolveAppBinaryOptions,
): { identity: AppBinaryIdentity | null; violations: string[] } {
  const platform = opts.platform ?? process.platform;
  if (platform !== 'darwin') {
    return {
      identity: null,
      violations: [`App binary identity：本批只支援 darwin 的 wails dev bundle 佈局，實際 platform=${platform}`
        + '——明確拒絕，不在此批擴成跨平台探索'],
    };
  }

  const expectedPath = expectedAppBundleBinary(opts.repoRoot, opts.outputFileName);
  const v: string[] = [];

  let st: fs.Stats;
  try { st = fs.statSync(expectedPath); }
  catch (e) {
    return { identity: null, violations: [`App binary 預期路徑不存在：${expectedPath}（${String(e)}）`] };
  }
  if (!st.isFile()) {
    return { identity: null, violations: [`App binary 預期路徑不是一般檔案：${expectedPath}`] };
  }
  if ((st.mode & 0o111) === 0) {
    return { identity: null, violations: [`App binary 預期路徑不可執行：${expectedPath}`] };
  }
  let canonicalPath: string;
  try { canonicalPath = fs.realpathSync(expectedPath); }
  catch (e) { return { identity: null, violations: [`App binary canonical path 無法解析：${String(e)}`] }; }

  // **獨立基準**：只對 repoRoot 自己做 realpath（吃掉 /tmp → /private/tmp 這種
  // 合法別名），再用**已知的完整 bundle 版面**組出唯一可接受的 canonical 路徑。
  //
  // 為什麼不能像前一版那樣把 `realpath(repoRoot/build/bin)` 當邊界（reviewer #375
  // 已重現）：若 `a/build/bin` 是指向 `b/build/bin` 的 symlink，那個 realpath 會
  // 一起跟到 b，於是 b 的 binary 也「落在邊界內」而被放行——**待檢子目錄自己
  // realpath 出來的結果不能當可信邊界**。改成完整路徑精確相等，build/bin、
  // bundle、leaf 任一段被導往別處都會不相等而被拒絕。
  let realRepoRoot: string;
  try { realRepoRoot = fs.realpathSync(opts.repoRoot); }
  catch (e) { return { identity: null, violations: [`repoRoot 無法解析：${String(e)}`] }; }
  const expectedCanonical = expectedAppBundleBinary(realRepoRoot, opts.outputFileName);
  if (canonicalPath !== expectedCanonical) {
    return {
      identity: null,
      violations: [`App binary canonical path 與本工作樹的預期 bundle 路徑不符：`
        + `實際 ${JSON.stringify(canonicalPath)} 預期 ${JSON.stringify(expectedCanonical)}`
        + '——build/bin／bundle／leaf 任一段被 symlink 導往別處都拒絕'],
    };
  }

  // **新鮮 ps 觀測 ＋ 本次受控程序樹**：候選必須是本次 root 的後代。
  let table: ProcRowWithCommand[];
  try { table = opts.table ?? snapshotProcessTableWithCommand(); }
  catch (e) { return { identity: null, violations: [`程序表觀測失敗（不得因此放行）：${String(e)}`] }; }

  const descendants = descendantsOf(opts.rootPid, table);
  const candidates = descendants.filter(r =>
    commandIsBinary(r.command, canonicalPath) || commandIsBinary(r.command, expectedPath));

  if (candidates.length === 0) {
    return {
      identity: null,
      violations: [`在本次受控程序樹（root pid=${opts.rootPid}）中找不到執行 ${expectedPath} 的程序`
        + `——後代共 ${descendants.length} 個，不得改用「command 含關鍵字的第一個」頂替`],
    };
  }
  if (candidates.length > 1) {
    return {
      identity: null,
      violations: [`候選歧義：本次程序樹中有 ${candidates.length} 個程序執行同一個 binary`
        + `（pid=${candidates.map(c => c.pid).join(',')}）——不挑任何一個`],
    };
  }
  const cand = candidates[0];

  // 身分再核對：候選必須仍存活，且 startedAt／command 與快照一致（身分變動即拒絕）。
  const probe = opts.probe ?? { isAlive, startedAt: processStartedAt, command: processCommand };
  if (!probe.isAlive(cand.pid)) {
    return { identity: null, violations: [`候選 pid=${cand.pid} 已不存活，身分無法核定`] };
  }
  const nowStarted = probe.startedAt(cand.pid);
  if (nowStarted === null) {
    return { identity: null, violations: [`候選 pid=${cand.pid} 的開始時間觀測失敗（不得因此放行）`] };
  }
  if (nowStarted !== cand.startedAt) {
    return {
      identity: null,
      violations: [`候選 pid=${cand.pid} 的開始時間變動（快照 ${JSON.stringify(cand.startedAt)}`
        + ` vs 再探 ${JSON.stringify(nowStarted)}）——pid 已被重用，拒絕核定`],
    };
  }
  const nowCommand = probe.command(cand.pid);
  if (nowCommand === null) {
    return { identity: null, violations: [`候選 pid=${cand.pid} 的 command 觀測失敗（不得因此放行）`] };
  }
  // **與第一次快照逐字相同**才算「command 未變」——前一版只重測 prefix，
  // 與宣稱不符（reviewer #375）。
  if (nowCommand !== cand.command) {
    return {
      identity: null,
      violations: [`候選 pid=${cand.pid} 的 command 與快照不一致：快照 ${JSON.stringify(cand.command)}`
        + ` vs 再探 ${JSON.stringify(nowCommand)}`],
    };
  }

  let sha256: string;
  try { sha256 = crypto.createHash('sha256').update(fs.readFileSync(canonicalPath)).digest('hex'); }
  catch (e) { return { identity: null, violations: [`App binary 讀取／雜湊失敗：${String(e)}`] }; }

  return {
    identity: { canonicalPath, sha256, pid: cand.pid, startedAt: cand.startedAt,
      command: cand.command, expectedPath },
    violations: v,
  };
}
