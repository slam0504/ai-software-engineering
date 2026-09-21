#!/usr/bin/env node
// B3a-2b-1：Codex app-server fake（stdio JSONL）。
//
// **只用於本包的隔離測試／probe**，絕不呼叫真 codex／claude（票面禁止事項）。
// argv 與 spawn 契約對齊 internal/codex/session.go StartAppServer：
// `proc.Start` 固定以 `Args: []string{"app-server"}` 呼叫 cfg.Binary，因此本檔
// 直接以 shebang 可執行、只接受單一 argv `app-server` 或 `--version`，其餘一律
// 記錄後 exit 17（未知 argv 的 fail-loud 要求）。
//
// 場景設定與 per-run log 各自一個 env（缺一即 fail）：
//   SCENARIO_FAKE_CONFIG  單一場景 JSON 檔路徑（ScenarioConfig，protocol.ts）
//   SCENARIO_FAKE_LOG     per-run JSONL log 路徑；manifest 落在 `${LOG}.manifest.json`
//
// Wire 順序（凍結，對齊 internal/codex 與 app.go 的 M3b §3.3 實據）：
//   initialize → initialized → thread/start|resume → turn/start →
//   server→client requestApproval（method 依 config）→ 等 client 回
//   {decision} → afterApproval 內容事件 → turn/completed。
import fs from 'node:fs';
import { Method, nowIso, splitFrames } from './protocol.ts';
import type { Frame, Manifest, RawId, ScenarioConfig } from './protocol.ts';

const argv = process.argv.slice(2);

function fatalArgv(msg: string): never {
  // 沒有 log 路徑可用時（argv 錯得太早），退而求其次寫 stderr——仍然是「記錄後
  // 失敗」，只是證據落在 stderr 而非 per-run log。
  process.stderr.write(`FAKE codex app-server: ${msg}\n`);
  process.exit(17);
}

if (argv.length !== 1 || (argv[0] !== 'app-server' && argv[0] !== '--version')) {
  fatalArgv(`unexpected argv ${JSON.stringify(argv)} (want exactly "app-server" or "--version")`);
}

const runId = process.env.SCENARIO_FAKE_RUN_ID ?? '0';

if (argv[0] === '--version') {
  process.stdout.write(`fake-codex-scenario 0.0.0-e2e+${runId}\n`);
  process.exit(0);
}

const configPath = process.env.SCENARIO_FAKE_CONFIG;
const logPath = process.env.SCENARIO_FAKE_LOG;
// C4 修正：先前 `if (!configPath || !logPath)` 把兩種情況合在一起處理——log
// 路徑存在、只缺 config 時，也被當成「沒有 log 路徑可用」直接 fatalArgv，
// 完全略過 manifest，跟下面這段註解（R4 邊界只涵蓋「連 SCENARIO_FAKE_LOG 都
// 沒給」）不符。這裡只在 log 路徑本身缺失時才走 fatalArgv（唯一允許「沒有
// manifest」的邊界）；log 路徑存在時即使 config 缺失，也要繼續往下開
// log／manifest，讓 config 驗證失敗走 finish(17, msg) 留下可診斷的 manifest。
if (!logPath) {
  // R4 邊界：根本沒有 log 路徑可用（連 SCENARIO_FAKE_LOG 都沒給），無法留下
  // 可解析的 evidence，只能 stderr＋非零收尾——這是唯一允許「沒有 manifest」
  // 的邊界，明列於此，不擴大到其他失敗路徑。
  fatalArgv(`missing required env (SCENARIO_FAKE_LOG=${logPath ?? ''})`);
}

// R4 修正：log 路徑已知（可寫），所以從這裡開始的任何失敗（包含 config 壞掉／
// 不合法）都要留下可解析的 manifest，而不是像先前那樣整段走 fatalArgv、
// 完全不建立 manifest。manifest.scenario 先以空字串佔位，config 解析成功後
// 才覆寫成真正的 scenario 名稱。
let seq = 0;
const startedAt = nowIso();
const manifest: Manifest = {
  scenario: '',
  argv,
  pid: process.pid,
  startedAt,
  endedAt: null,
  exitCode: null,
  approvalMethod: null,
  approvalRequestId: null,
  decisionReceived: null,
  unknownMethodsSeen: [],
  fatalError: null,
};

const logFd = fs.openSync(logPath, 'a');
const manifestPath = `${logPath}.manifest.json`;

function writeLog(dir: 'c2s' | 's2c' | 'meta', extra: { note?: string; frame?: Frame }): void {
  seq += 1;
  const entry = { seq, ts: nowIso(), dir, ...extra };
  fs.writeSync(logFd, JSON.stringify(entry) + '\n');
}

function writeManifest(): void {
  fs.writeFileSync(manifestPath, JSON.stringify(manifest, null, 2));
}

let exited = false;
function finish(code: number, fatalError?: string): void {
  if (exited) return;
  exited = true;
  manifest.endedAt = nowIso();
  manifest.exitCode = code;
  if (fatalError) manifest.fatalError = fatalError;
  writeManifest();
  try {
    fs.closeSync(logFd);
  } catch {
    /* already closed */
  }
  process.exit(code);
}

// validateScenarioConfig：R4 修正——先前只檢查四個欄位 truthy，完全沒驗證
// approvalMethod 是否落在白名單內（`not-approved/method` 這種偽方法會被真的
// 送出去，違反「只允許兩個 method」的契約），也沒驗證其餘實際會用到的欄位
// 型別。這裡逐一檢查，缺少或型別不對一律在協定開始前回傳錯誤訊息（呼叫端
// finish(17, msg) 收尾，manifest 留下可診斷的 fatalError）。
function validateScenarioConfig(raw: unknown): string | null {
  if (typeof raw !== 'object' || raw === null || Array.isArray(raw)) {
    return 'config is not a JSON object';
  }
  const c = raw as Record<string, unknown>;
  if (typeof c.scenario !== 'string' || c.scenario.length === 0) return 'scenario must be a non-empty string';
  if (typeof c.threadId !== 'string' || c.threadId.length === 0) return 'threadId must be a non-empty string';
  if (typeof c.turnId !== 'string' || c.turnId.length === 0) return 'turnId must be a non-empty string';
  if (typeof c.itemId !== 'string' || c.itemId.length === 0) return 'itemId must be a non-empty string';
  if (c.threadMode !== 'start' && c.threadMode !== 'resume') {
    return `threadMode must be "start" or "resume", got ${JSON.stringify(c.threadMode)}`;
  }
  if (c.approvalMethod !== Method.CmdExecRequestApproval && c.approvalMethod !== Method.FileChangeRequestApproval) {
    return `approvalMethod must be one of [${Method.CmdExecRequestApproval}, ${Method.FileChangeRequestApproval}], got ${JSON.stringify(c.approvalMethod)}`;
  }
  if (typeof c.approvalRequestId !== 'string' || c.approvalRequestId.length === 0) {
    return 'approvalRequestId must be a non-empty string';
  }
  if (!Array.isArray(c.afterApproval)) return 'afterApproval must be an array';
  for (const ev of c.afterApproval) {
    if (typeof ev !== 'object' || ev === null) return 'afterApproval entries must be objects';
    const e = ev as Record<string, unknown>;
    if (e.type !== 'itemStarted' && e.type !== 'itemCompleted') {
      return `afterApproval[].type must be "itemStarted" or "itemCompleted", got ${JSON.stringify(e.type)}`;
    }
    if (typeof e.text !== 'string') return 'afterApproval[].text must be a string';
  }
  if (c.turnStatus !== 'completed' && c.turnStatus !== 'failed') {
    return `turnStatus must be "completed" or "failed", got ${JSON.stringify(c.turnStatus)}`;
  }
  return null;
}

let cfg: ScenarioConfig;
{
  let raw: unknown;
  if (!configPath) {
    // C4 修正：log 路徑可寫時，缺 config 也要走 finish(17, msg)（有 manifest
    // 可診斷），不是像 fatalArgv 那樣完全跳過 manifest。
    finish(17, `missing required env (SCENARIO_FAKE_CONFIG=${configPath ?? ''})`);
    throw new Error('unreachable: finish() exits the process');
  }
  try {
    const text = fs.readFileSync(configPath, 'utf8');
    raw = JSON.parse(text);
  } catch (e) {
    finish(17, `malformed SCENARIO_FAKE_CONFIG (${configPath}): ${(e as Error).message}`);
    throw new Error('unreachable: finish() exits the process');
  }
  const violation = validateScenarioConfig(raw);
  if (violation !== null) {
    finish(17, `invalid SCENARIO_FAKE_CONFIG (${configPath}): ${violation}`);
    throw new Error('unreachable: finish() exits the process');
  }
  cfg = raw as ScenarioConfig;
  manifest.scenario = cfg.scenario;
}

function send(f: Frame, dir: 's2c' = 's2c'): void {
  process.stdout.write(JSON.stringify(f) + '\n');
  writeLog(dir, { frame: f });
}

function fail(msg: string): void {
  writeLog('meta', { note: `FATAL: ${msg}` });
  finish(17, msg);
}

// R5 修正：先前無條件 finish(0)——即使場景根本沒跑完（例如 initialize
// response 剛送出就被 SIGTERM），也會被記成「成功」manifest（exitCode 0、
// fatalError null）。現在只有 stage 已經是 'done'（turn/completed 已送出、
// 正常收尾）才視為成功；其餘一律記為 interrupted、非零收尾，manifest 留下
// 可診斷的 fatalError。finish() 內部的 `exited` guard 保證真正已經 finish(0)
// 過的 run 不會被這裡的呼叫覆寫。
process.on('SIGTERM', () => {
  writeLog('meta', { note: 'SIGTERM received' });
  if (stage === 'done') {
    finish(0);
    return;
  }
  finish(17, `terminated by SIGTERM before scenario finished (stage=${stage})`);
});

type Stage =
  | 'awaitInitialize'
  | 'awaitInitialized'
  | 'awaitThreadStart'
  | 'awaitTurnStart'
  | 'awaitApprovalResponse'
  | 'done';

let stage: Stage = 'awaitInitialize';
let pendingApprovalId: RawId | null = null;

function idsEqual(a: RawId, b: RawId): boolean {
  // 型別保留檢查的正面用法：只有「原始值完全相等」才算符合，number 1 跟字串
  // "1" 不視為相等（不用 == 寬鬆比較，避免掩蓋型別被悄悄轉掉的錯誤）。
  return typeof a === typeof b && a === b;
}

// isValidRequestId：C4 缺陷修正——先前各 stage 只用 `f.id === undefined` 判斷
// 「有沒有 id」，`null` 滿足 `!== undefined` 卻不是合法 RequestId，會被當成
// 「有 id」放行，讓 initialize／thread/start／turn/start 全用 id:null 也能
// 走完整個 approval 流程並 rc0。這裡對齊 schemas/codex/RequestId.json（string |
// integer union）：只接受字串或整數，null／object／boolean 一律視為「沒有合法
// id」。
function isValidRequestId(id: unknown): id is RawId {
  if (typeof id === 'string') return true;
  if (typeof id === 'number') return Number.isInteger(id);
  return false;
}

function handleLine(line: string): void {
  let parsed: unknown;
  try {
    parsed = JSON.parse(line);
  } catch (e) {
    writeLog('c2s', { note: `malformed json: ${(e as Error).message}` });
    fail(`malformed client frame: ${line.slice(0, 200)}`);
    return;
  }
  // R3 修正：先前直接 `as Frame` 就存取 `.id`／`.method`，遇到 `null`／array／
  // 其他非物件 JSON 值會丟未捕捉的 TypeError（process 直接崩潰，沒有
  // manifest）。這裡先守門，非物件一律走 fail()，留下可診斷的證據。
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
    writeLog('c2s', { note: `non-object frame: ${line.slice(0, 200)}` });
    fail(`expected a JSON object frame, got: ${line.slice(0, 200)}`);
    return;
  }
  const f = parsed as Frame;
  writeLog('c2s', { frame: f });

  switch (stage) {
    case 'awaitInitialize': {
      if (f.method !== Method.Initialize || !isValidRequestId(f.id)) {
        manifest.unknownMethodsSeen.push(f.method ?? '(no method)');
        fail(`expected ${Method.Initialize} with id, got ${JSON.stringify(f)}`);
        return;
      }
      send({ id: f.id, result: {} });
      stage = 'awaitInitialized';
      return;
    }
    case 'awaitInitialized': {
      if (f.method !== Method.Initialized || f.id !== undefined) {
        manifest.unknownMethodsSeen.push(f.method ?? '(no method)');
        fail(`expected notification ${Method.Initialized}, got ${JSON.stringify(f)}`);
        return;
      }
      stage = 'awaitThreadStart';
      return;
    }
    case 'awaitThreadStart': {
      // R2 修正：先前 thread/start 與 thread/resume 兩個方法無條件都接受，也
      // 完全不核對 params——送 `thread/resume(threadId="WRONG")` 一樣 rc0 並
      // 拿到正確 thread，會讓未來錯接的 resume 測試假通過。現在 config 明確
      // 指定 threadMode，本次協定只允許對應的那個方法；resume 額外核對
      // params.threadId 是否等於 cfg.threadId。
      const expectedMethod = cfg.threadMode === 'resume' ? Method.ThreadResume : Method.ThreadStart;
      if (f.method !== expectedMethod || !isValidRequestId(f.id)) {
        manifest.unknownMethodsSeen.push(f.method ?? '(no method)');
        fail(`expected ${expectedMethod} (threadMode=${cfg.threadMode}) with id, got ${JSON.stringify(f)}`);
        return;
      }
      if (cfg.threadMode === 'resume') {
        const params = f.params as { threadId?: unknown } | undefined;
        if (typeof params?.threadId !== 'string' || params.threadId !== cfg.threadId) {
          fail(
            `${Method.ThreadResume} threadId mismatch: got ${JSON.stringify(params?.threadId)} want ${JSON.stringify(cfg.threadId)}`,
          );
          return;
        }
      }
      send({ id: f.id, result: { thread: { id: cfg.threadId } } });
      stage = 'awaitTurnStart';
      return;
    }
    case 'awaitTurnStart': {
      if (f.method !== Method.TurnStart || !isValidRequestId(f.id)) {
        manifest.unknownMethodsSeen.push(f.method ?? '(no method)');
        fail(`expected ${Method.TurnStart} with id, got ${JSON.stringify(f)}`);
        return;
      }
      // R2 修正：turn/start 先前完全不核對 params，錯接到別的 thread 一樣會
      // rc0 通過。這裡要求 params.threadId 嚴格等於本次協定已確立的
      // cfg.threadId。
      {
        const params = f.params as { threadId?: unknown } | undefined;
        if (typeof params?.threadId !== 'string' || params.threadId !== cfg.threadId) {
          fail(
            `${Method.TurnStart} threadId mismatch: got ${JSON.stringify(params?.threadId)} want ${JSON.stringify(cfg.threadId)}`,
          );
          return;
        }
      }
      send({ id: f.id, result: { turn: { id: cfg.turnId, status: 'inProgress' } } });
      // turn/start response 立即回（同 internal/codex/turns.go 註記）；approval
      // request 在 response 之後才送，避免 client 端 pending map 還沒登記好。
      pendingApprovalId = cfg.approvalRequestId;
      manifest.approvalMethod = cfg.approvalMethod;
      manifest.approvalRequestId = pendingApprovalId;
      send({
        id: pendingApprovalId,
        method: cfg.approvalMethod,
        params: {
          threadId: cfg.threadId,
          turnId: cfg.turnId,
          itemId: cfg.itemId,
          startedAtMs: Date.now(),
        },
      });
      stage = 'awaitApprovalResponse';
      return;
    }
    case 'awaitApprovalResponse': {
      if (!isValidRequestId(f.id) || f.method !== undefined) {
        manifest.unknownMethodsSeen.push(f.method ?? '(no method, missing id)');
        fail(`expected approval response frame, got ${JSON.stringify(f)}`);
        return;
      }
      if (pendingApprovalId === null || !idsEqual(f.id, pendingApprovalId)) {
        fail(`approval response id mismatch: got ${JSON.stringify(f.id)} want ${JSON.stringify(pendingApprovalId)}`);
        return;
      }
      // R3 修正：先前完全不看 `f.error`——即使 frame 同時帶
      // `result.decision="accept"` 與 `error={code:-1,...}`（JSON-RPC 規範互斥
      // 的兩個欄位同時出現）一樣被當成合法 accept 收下並 rc0。現在明確核對
      // result／error 互斥，且「error response」本身必須讓這個成功場景判定為
      // 未完成（fail，非 0 收尾），不能被當成 decision 處理。
      const hasResult = f.result !== undefined;
      const hasError = f.error !== undefined;
      if (hasResult && hasError) {
        fail(`approval response has both result and error (violates JSON-RPC mutual exclusivity): ${JSON.stringify(f)}`);
        return;
      }
      if (hasError) {
        fail(`approval response is an error, not a decision: ${JSON.stringify(f.error)}`);
        return;
      }
      const result = f.result as { decision?: string } | undefined;
      const decision = result?.decision;
      if (decision !== 'accept' && decision !== 'decline') {
        fail(`approval response missing/invalid decision: ${JSON.stringify(f.result)}`);
        return;
      }
      manifest.decisionReceived = decision;
      for (const ev of cfg.afterApproval) {
        send({
          method: ev.type === 'itemStarted' ? Method.ItemStarted : Method.ItemCompleted,
          params: {
            threadId: cfg.threadId,
            turnId: cfg.turnId,
            item: { type: 'agentMessage', id: cfg.itemId, text: ev.text },
          },
        });
      }
      send({
        method: Method.TurnCompleted,
        params: { threadId: cfg.threadId, turn: { id: cfg.turnId, status: cfg.turnStatus } },
      });
      stage = 'done';
      writeLog('meta', { note: 'turn/completed sent; scenario finished' });
      finish(0);
      return;
    }
    case 'done':
      // 已收尾後收到的任何 frame 都是未預期的多餘輸入，記錄但不視為致命
      // （client 端可能在 turn/completed 後仍送 TerminateSession 之類的收尾）。
      writeLog('meta', { note: `frame after done: ${JSON.stringify(f)}` });
      return;
  }
}

let buf = '';
process.stdin.setEncoding('utf8');
process.stdin.on('data', (chunk: string) => {
  buf += chunk;
  const { lines, rest } = splitFrames(buf);
  buf = rest;
  for (const line of lines) {
    if (exited) return;
    handleLine(line);
  }
});
process.stdin.on('end', () => {
  if (!exited && stage !== 'done') {
    fail(`stdin closed before scenario finished (stage=${stage})`);
  }
});
