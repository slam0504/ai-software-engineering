// run-state.json（每次執行專屬，落在證據目錄）＋ .active-run.json（跨執行的
// 指標，落在 .artifacts/ 根目錄，不屬於任何單一 run 的證據）。
//
// §2.3：spawn 當下、不等 ready，就要把 pid／pgid／啟動命令／啟動時間寫進
// run-state.json；之後每偵測到一個新後代（含非同 pgid）即時追加。
import fs from 'node:fs';
import path from 'node:path';
import { processCommand, processStartedAt, ToolObservationError } from './psUtil.js';

export interface TrackedProc {
  pid: number;
  ppid: number;
  pgid: number;
  command: string;
  startedAt: string;
  samePgid: boolean;
}

export interface RunState {
  runId: string;
  rootPgid: number;
  // 'interrupted'（reviewer 二次審查，2026-09-15，N8 實跑發現）：收到
  // SIGINT／SIGTERM 而中止的執行要留下自己專屬的終態，不能被之後任何路徑
  // 覆寫成 'stopped'（那看起來就是正常收尾，會讓判定誤以為是 PASSED）。
  status: 'starting' | 'ready' | 'stopping' | 'stopped' | 'failed' | 'interrupted';
  failureStage?: string;
  processes: TrackedProc[];
  ports: { wails: number; vite?: number };
  // R2：程序樹追蹤這一刻若觀測失敗（ps／lsof 本身失敗，不是「查得到但沒有
  // 符合結果」），記一筆原因——不能假裝那一刻什麼事都沒發生。globalTeardown
  // 看到這裡有內容就不能把整次執行判成乾淨。
  observationFailures: string[];
}

export class RunStateStore {
  private readonly artifactsDir: string;
  private readonly artifactsRoot: string;
  private readonly file: string;
  private readonly activeRunFile: string;
  private state: RunState;
  // R2 迴歸修正（reviewer 三次審查，2026-09-16）：記住哪個 pid 是 root，
  // `updateProcessIdentity` 才知道要不要同步 `.active-run.json`（見下方）。
  private rootPid: number | null = null;

  // 這裡刻意不用 constructor 參數屬性簡寫（`private readonly x: T` 直接寫在
  // 參數列），改成上面先宣告欄位、建構子內再賦值——N11 選型測試（P1／P2
  // 修正同一輪，2026-09-16）需要用 Node 原生 TS 支援直接 import 這個檔案，
  // 參數屬性簡寫在 strip-only 模式不支援（`SyntaxError
  // [ERR_UNSUPPORTED_TYPESCRIPT_SYNTAX]`），行為完全不變，只是語法寫法。
  constructor(artifactsDir: string, artifactsRoot: string, runId: string) {
    this.artifactsDir = artifactsDir;
    this.artifactsRoot = artifactsRoot;
    this.file = path.join(artifactsDir, 'run-state.json');
    this.activeRunFile = path.join(artifactsRoot, '.active-run.json');
    this.state = { runId, rootPgid: 0, status: 'starting', processes: [], ports: { wails: 34115 }, observationFailures: [] };
    this.persist();
  }

  private persist(): void {
    fs.writeFileSync(this.file, JSON.stringify(this.state, null, 2));
  }

  private writePointer(pointer: ActiveRunPointer): void {
    fs.writeFileSync(this.activeRunFile, JSON.stringify(pointer, null, 2));
  }

  path(): string {
    return this.file;
  }

  recordRoot(pid: number, pgid: number, command: string, startedAt: string): void {
    this.rootPid = pid;
    this.state.rootPgid = pgid;
    this.addProcess({ pid, ppid: 0, pgid, command, startedAt, samePgid: true });
    fs.mkdirSync(this.artifactsRoot, { recursive: true });
    this.writePointer({
      runId: this.state.runId,
      artifactsDir: this.artifactsDir,
      rootPid: pid,
      rootPgid: pgid,
      rootCommand: command,
      rootStartedAt: startedAt,
    });
  }

  private knownPids(): Set<number> {
    return new Set(this.state.processes.map(p => p.pid));
  }

  addProcess(p: TrackedProc): void {
    if (this.knownPids().has(p.pid)) return;
    this.state.processes.push(p);
    this.persist();
  }

  // updateProcessIdentity：R2 修正（reviewer 三次審查，2026-09-16）——
  // `recordRoot` 現在允許先用 spawn 當下已知為真的資訊（command／args、
  // 占位時間）立刻登記，事後才「盡力補強」精確的 startedAt／command（來自
  // 第一次 post-spawn 的 ps 觀測，可能失敗）。這裡負責把補強後的值寫回同一
  // 筆紀錄，不新增也不略過。找不到這個 pid（理論上不該發生，recordRoot
  // 一定先呼叫過）就不做任何事，不拋錯——呼叫端本來就是「盡力補強」的語意。
  updateProcessIdentity(pid: number, command: string, startedAt: string): void {
    const entry = this.state.processes.find(p => p.pid === pid);
    if (!entry) return;
    entry.command = command;
    entry.startedAt = startedAt;
    this.persist();
    // R2 迴歸修正（reviewer 三次審查，2026-09-16）：這個 pid 若剛好是
    // root，`.active-run.json` 裡 `recordRoot` 當時寫入的 rootCommand／
    // rootStartedAt 占位值也要同步更新——先前只更新這裡（run-state.json），
    // pointer 檔案停在舊的占位值，兩份「我們自己產生的正常紀錄」就會互相
    // 矛盾，讓 staleRun.ts 的一致性核對誤判成損毀、擋掉合法的殘留清理
    // （reviewer 用 RunStateStore＋handleStaleRun 實測重現：recordRoot →
    // updateProcessIdentity → handleStaleRun，stop 呼叫 0 次）。
    if (this.rootPid !== pid) return;
    if (!fs.existsSync(this.activeRunFile)) return; // 沒有 pointer 可同步，不用管。
    try {
      const pointer = JSON.parse(fs.readFileSync(this.activeRunFile, 'utf8')) as ActiveRunPointer;
      pointer.rootCommand = command;
      pointer.rootStartedAt = startedAt;
      this.writePointer(pointer);
    } catch (e) {
      // 讀／寫 pointer 失敗：不能留下「state 已更新、pointer 沒同步」的
      // 矛盾狀態卻假裝沒事——記一筆觀測失敗，讓這次執行不能被判成乾淨。
      this.recordObservationFailure(`updateProcessIdentity：root 身分補強完成但無法同步 .active-run.json：${String(e)}`);
    }
  }

  // 這裡是「盡力補充證據」而不是判斷關卡：單一 pid 這次補不到 command／
  // startedAt（含 ToolObservationError，工具本身觀測失敗）就記
  // "(unavailable)"，不讓這個 pid 掉出追蹤清單——它仍然要進 stop 程序的
  // 目標清單。R2 的「無法觀測不得判成乾淨」是對 isAlive／isPortListening／
  // lsofForPids 這類會影響 pass/fail 的路徑，不是這裡。
  addDescendantIfNew(pid: number, ppid: number, pgid: number): void {
    if (this.knownPids().has(pid)) return;
    let command = '(unavailable)';
    let startedAt = '(unavailable)';
    try { command = processCommand(pid) ?? '(unavailable)'; } catch (e) { if (!(e instanceof ToolObservationError)) throw e; }
    try { startedAt = processStartedAt(pid) ?? '(unavailable)'; } catch (e) { if (!(e instanceof ToolObservationError)) throw e; }
    this.addProcess({ pid, ppid, pgid, command, startedAt, samePgid: pgid === this.state.rootPgid });
  }

  setPorts(ports: { wails: number; vite?: number }): void {
    this.state.ports = ports;
    this.persist();
  }

  setStatus(status: RunState['status'], failureStage?: string): void {
    this.state.status = status;
    if (failureStage) this.state.failureStage = failureStage;
    this.persist();
  }

  recordObservationFailure(reason: string): void {
    this.state.observationFailures.push(`[${new Date().toISOString()}] ${reason}`);
    this.persist();
  }

  get(): RunState {
    return this.state;
  }

  clearActiveRunPointer(): void {
    if (fs.existsSync(this.activeRunFile)) fs.rmSync(this.activeRunFile);
  }
}

export interface ActiveRunPointer {
  runId: string;
  artifactsDir: string;
  rootPid: number;
  rootPgid: number;
  rootCommand: string;
  rootStartedAt: string;
}

export function readActiveRunPointer(artifactsRoot: string): ActiveRunPointer | null {
  const file = path.join(artifactsRoot, '.active-run.json');
  if (!fs.existsSync(file)) return null;
  return JSON.parse(fs.readFileSync(file, 'utf8')) as ActiveRunPointer;
}

export function removeActiveRunPointer(artifactsRoot: string): void {
  const file = path.join(artifactsRoot, '.active-run.json');
  if (fs.existsSync(file)) fs.rmSync(file);
}
