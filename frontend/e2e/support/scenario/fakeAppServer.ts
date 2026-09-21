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
if (!configPath || !logPath) {
  fatalArgv(
    `missing required env (SCENARIO_FAKE_CONFIG=${configPath ?? ''} SCENARIO_FAKE_LOG=${logPath ?? ''})`,
  );
}

let cfg: ScenarioConfig;
try {
  const raw = fs.readFileSync(configPath, 'utf8');
  cfg = JSON.parse(raw) as ScenarioConfig;
  if (!cfg.scenario || !cfg.threadId || !cfg.turnId || !cfg.approvalMethod) {
    throw new Error('missing required scenario fields');
  }
} catch (e) {
  fatalArgv(`malformed SCENARIO_FAKE_CONFIG (${configPath}): ${(e as Error).message}`);
}

let seq = 0;
const startedAt = nowIso();
const manifest: Manifest = {
  scenario: cfg.scenario,
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

function send(f: Frame, dir: 's2c' = 's2c'): void {
  process.stdout.write(JSON.stringify(f) + '\n');
  writeLog(dir, { frame: f });
}

function fail(msg: string): void {
  writeLog('meta', { note: `FATAL: ${msg}` });
  finish(17, msg);
}

process.on('SIGTERM', () => {
  writeLog('meta', { note: 'SIGTERM received' });
  finish(0);
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

function handleLine(line: string): void {
  let f: Frame;
  try {
    f = JSON.parse(line) as Frame;
  } catch (e) {
    writeLog('c2s', { note: `malformed json: ${(e as Error).message}` });
    fail(`malformed client frame: ${line.slice(0, 200)}`);
    return;
  }
  writeLog('c2s', { frame: f });

  switch (stage) {
    case 'awaitInitialize': {
      if (f.method !== Method.Initialize || f.id === undefined) {
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
      if ((f.method !== Method.ThreadStart && f.method !== Method.ThreadResume) || f.id === undefined) {
        manifest.unknownMethodsSeen.push(f.method ?? '(no method)');
        fail(`expected ${Method.ThreadStart}|${Method.ThreadResume} with id, got ${JSON.stringify(f)}`);
        return;
      }
      send({ id: f.id, result: { thread: { id: cfg.threadId } } });
      stage = 'awaitTurnStart';
      return;
    }
    case 'awaitTurnStart': {
      if (f.method !== Method.TurnStart || f.id === undefined) {
        manifest.unknownMethodsSeen.push(f.method ?? '(no method)');
        fail(`expected ${Method.TurnStart} with id, got ${JSON.stringify(f)}`);
        return;
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
      if (f.id === undefined || f.method !== undefined) {
        manifest.unknownMethodsSeen.push(f.method ?? '(no method, missing id)');
        fail(`expected approval response frame, got ${JSON.stringify(f)}`);
        return;
      }
      if (pendingApprovalId === null || !idsEqual(f.id, pendingApprovalId)) {
        fail(`approval response id mismatch: got ${JSON.stringify(f.id)} want ${JSON.stringify(pendingApprovalId)}`);
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
