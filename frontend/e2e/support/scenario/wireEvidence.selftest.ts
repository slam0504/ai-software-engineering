// B3a-2b-2 Task D 限縮補正（F1）：wireEvidence.ts 的純函式／bounded 輪詢邏輯
// 負控制——不需要真 App／真瀏覽器，用真實 /tmp 目錄＋真實檔案（不 mock fs）
// 模擬 wire-logs 目錄與 generation 的 finalize meta 各種時序與內容。
//
// 執行：npm --prefix frontend run selftest:scenario-wire-evidence
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {
  assertWireEvidencePersisted,
  persistWireEvidence,
  selectGenerationsByIdentity,
  validateWireMeta,
  waitForWireMeta,
  WireMetaWaitError,
  type WireMeta,
} from './wireEvidence.ts';
import { HarnessLogger } from '../logger.ts';

let passed = 0;
function check(name: string, fn: () => void | Promise<void>): Promise<void> {
  return Promise.resolve()
    .then(fn)
    .then(() => {
      passed += 1;
      console.log(`ok - ${name}`);
    })
    .catch(e => {
      console.error(`FAIL - ${name}`);
      console.error(e);
      process.exitCode = 1;
    });
}

function freshWireLogsDir(): string {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a2b2-taskD-wireEvidence-'));
  const dir = path.join(root, 'wire-logs');
  fs.mkdirSync(dir, { recursive: true });
  return dir;
}

function writeGenerationJsonl(dir: string, id: string, frames: unknown[]): string {
  const p = path.join(dir, `${id}.jsonl`);
  fs.writeFileSync(p, frames.map(f => JSON.stringify(f)).join('\n') + '\n');
  return p;
}

const METHOD = 'codex/commandExecution/requestApproval';
const REQUEST_ID = 'b3a2b2-approval-wireEvidenceSelftest';

function approvalFrames(): unknown[] {
  return [
    { frame: 0, dir: 's2c', wsid: 'ws1', raw: { id: REQUEST_ID, method: METHOD, params: {} } },
    { frame: 1, dir: 'c2s', wsid: 'ws1', raw: { id: REQUEST_ID, result: { decision: 'accept' } } },
  ];
}

// 準備一個「可被 realpath 出來」的假 codex wrapper 執行檔，供 argv[0] 核對用。
function fakeCodexBin(root: string): string {
  const p = path.join(root, 'codex');
  fs.writeFileSync(p, '#!/usr/bin/env bash\nexit 0\n');
  fs.chmodSync(p, 0o755);
  return fs.realpathSync(p);
}

function goodMeta(argv0: string, overrides: Partial<WireMeta> = {}): WireMeta {
  return {
    provider: 'codex',
    cli_version: '',
    argv: [argv0, 'app-server'],
    cwd: '/tmp',
    recorded_at: new Date().toISOString(),
    exit_code: 0,
    finalize_cause: 'codex: app-server exited',
    ...overrides,
  };
}

async function main(): Promise<void> {
  // --- selectGenerationsByIdentity：以內容而非 mtime 挑對 generation -------
  await check('selectGenerationsByIdentity 挑出內容含本案 approval request 的 generation，忽略 mtime 更新但不相符的檔案', () => {
    const dir = freshWireLogsDir();
    // 較舊、但相符的正確案。
    const correctPath = writeGenerationJsonl(dir, 'codex-wire-correct', approvalFrames());
    // 較新（刻意晚一點寫、mtime 較新），但內容是另一案，不該被選到。
    const staleNewerPath = writeGenerationJsonl(dir, 'codex-wire-stale-newer', [
      { frame: 0, dir: 's2c', wsid: 'ws2', raw: { id: 'other-request', method: METHOD, params: {} } },
    ]);
    fs.utimesSync(staleNewerPath, new Date(Date.now() + 5000), new Date(Date.now() + 5000));

    const matches = selectGenerationsByIdentity(dir, { approvalMethod: METHOD, approvalRequestId: REQUEST_ID });
    assert.equal(matches.length, 1, `expected exactly 1 identity match, got ${matches.length}`);
    assert.equal(matches[0].jsonlPath, correctPath, 'should select the generation whose content matches identity, not the newer-mtime unrelated one');
  });

  await check('selectGenerationsByIdentity 零候選：目錄存在但沒有任何 generation 內容相符時回傳空陣列', () => {
    const dir = freshWireLogsDir();
    writeGenerationJsonl(dir, 'codex-wire-unrelated', [
      { frame: 0, dir: 's2c', wsid: 'ws3', raw: { id: 'unrelated', method: METHOD, params: {} } },
    ]);
    const matches = selectGenerationsByIdentity(dir, { approvalMethod: METHOD, approvalRequestId: REQUEST_ID });
    assert.equal(matches.length, 0);
  });

  // --- waitForWireMeta：正控制——延後產生的 meta 能成功 --------------------
  await check('waitForWireMeta：meta 延後出現（模擬 finalize 較慢）仍能在期限內成功', async () => {
    const dir = freshWireLogsDir();
    const argv0 = fakeCodexBin(dir);
    const jsonlPath = writeGenerationJsonl(dir, 'codex-wire-delayed', approvalFrames());
    const metaPath = jsonlPath.replace(/\.jsonl$/, '.meta.json');
    setTimeout(() => {
      fs.writeFileSync(metaPath, JSON.stringify(goodMeta(argv0)));
    }, 300);
    const result = await waitForWireMeta(metaPath, { deadlineMs: 3000, pollIntervalMs: 50 });
    assert.equal(result.meta.provider, 'codex');
    assert.ok(result.attempts > 1, `expected multiple poll attempts before success, got ${result.attempts}`);
  });

  // --- waitForWireMeta：正控制——bounded retry 撐過非原子寫入的暫態不完整內容 ---
  await check('waitForWireMeta：短暫讀到不完整 JSON（非原子寫入）會重試，最終讀到完整內容仍成功', async () => {
    const dir = freshWireLogsDir();
    const argv0 = fakeCodexBin(dir);
    const jsonlPath = writeGenerationJsonl(dir, 'codex-wire-partial', approvalFrames());
    const metaPath = jsonlPath.replace(/\.jsonl$/, '.meta.json');
    const full = JSON.stringify(goodMeta(argv0));
    fs.writeFileSync(metaPath, full.slice(0, Math.floor(full.length / 2))); // 模擬寫到一半
    setTimeout(() => {
      fs.writeFileSync(metaPath, full); // 補完整
    }, 200);
    const result = await waitForWireMeta(metaPath, { deadlineMs: 3000, pollIntervalMs: 50 });
    assert.equal(result.meta.provider, 'codex');
  });

  // --- waitForWireMeta：負控制——永遠缺檔必失敗 -----------------------------
  await check('waitForWireMeta：meta 永遠不出現，逾時後必須 throw 並保留最後一次錯誤（不吞）', async () => {
    const dir = freshWireLogsDir();
    const metaPath = path.join(dir, 'codex-wire-never.meta.json');
    await assert.rejects(
      () => waitForWireMeta(metaPath, { deadlineMs: 300, pollIntervalMs: 50 }),
      (err: unknown) => {
        assert.ok(err instanceof WireMetaWaitError, `expected WireMetaWaitError, got ${err}`);
        assert.ok(err.message.includes('逾時'), `expected timeout message, got: ${err.message}`);
        assert.ok((err.diagnostics.lastError as string).includes('尚未出現'), `expected lastError diagnostic, got: ${JSON.stringify(err.diagnostics)}`);
        return true;
      },
    );
  });

  await check('waitForWireMeta：meta 一直是損毀 JSON（非暫態，持續到 deadline），逾時後必須 throw 並保留 parse 錯誤', async () => {
    const dir = freshWireLogsDir();
    const metaPath = path.join(dir, 'codex-wire-corrupt.meta.json');
    fs.writeFileSync(metaPath, '{not valid json');
    await assert.rejects(
      () => waitForWireMeta(metaPath, { deadlineMs: 300, pollIntervalMs: 50 }),
      (err: unknown) => {
        assert.ok(err instanceof WireMetaWaitError);
        assert.ok(err.message.includes('解析失敗'), `expected parse-failure message, got: ${err.message}`);
        return true;
      },
    );
  });

  // --- validateWireMeta：正控制——完整合法 meta 應無違規 --------------------
  await check('validateWireMeta：provider=codex／exit_code=0／argv 相符／無異常欄位時應無違規', () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a2b2-taskD-wireEvidence-validate-'));
    const argv0 = fakeCodexBin(root);
    const meta = goodMeta(argv0);
    const violations = validateWireMeta(meta, { provider: 'codex', expectedArgvTail: ['app-server'], expectedArgv0RealPath: argv0 });
    assert.deepEqual(violations, []);
  });

  // --- validateWireMeta：負控制——錯身分（argv 不對應本次 wrapper） --------
  await check('validateWireMeta：argv[0] 指向另一個執行檔（非本次 codex wrapper）必須判定違規', () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a2b2-taskD-wireEvidence-validate-'));
    const realArgv0 = fakeCodexBin(root);
    const otherRoot = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a2b2-taskD-wireEvidence-other-'));
    const otherArgv0 = fakeCodexBin(otherRoot); // 另一個「身分不符」的 wrapper
    const meta = goodMeta(otherArgv0);
    const violations = validateWireMeta(meta, { provider: 'codex', expectedArgvTail: ['app-server'], expectedArgv0RealPath: realArgv0 });
    assert.ok(violations.length > 0);
    assert.ok(violations.some(v => v.includes('argv[0]')), `expected argv[0] identity violation, got: ${JSON.stringify(violations)}`);
  });

  // --- validateWireMeta：負控制——非零 exit_code ----------------------------
  await check('validateWireMeta：exit_code 非 0 必須判定違規', () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a2b2-taskD-wireEvidence-validate-'));
    const argv0 = fakeCodexBin(root);
    const meta = goodMeta(argv0, { exit_code: 17 });
    const violations = validateWireMeta(meta, { provider: 'codex', expectedArgvTail: ['app-server'], expectedArgv0RealPath: argv0 });
    assert.ok(violations.some(v => v.includes('exit_code')), `expected exit_code violation, got: ${JSON.stringify(violations)}`);
  });

  await check('validateWireMeta：exit_code 缺失（undefined）必須判定違規，不得當成 0', () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a2b2-taskD-wireEvidence-validate-'));
    const argv0 = fakeCodexBin(root);
    const meta = goodMeta(argv0);
    delete meta.exit_code;
    const violations = validateWireMeta(meta, { provider: 'codex', expectedArgvTail: ['app-server'], expectedArgv0RealPath: argv0 });
    assert.ok(violations.some(v => v.includes('exit_code')), `expected exit_code-missing violation, got: ${JSON.stringify(violations)}`);
  });

  // --- validateWireMeta：負控制——recorder error ----------------------------
  await check('validateWireMeta：recorder_error 存在必須判定違規', () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a2b2-taskD-wireEvidence-validate-'));
    const argv0 = fakeCodexBin(root);
    const meta = goodMeta(argv0, { recorder_error: 'write: no space left on device' });
    const violations = validateWireMeta(meta, { provider: 'codex', expectedArgvTail: ['app-server'], expectedArgv0RealPath: argv0 });
    assert.ok(violations.some(v => v.includes('recorder_error')), `expected recorder_error violation, got: ${JSON.stringify(violations)}`);
  });

  await check('validateWireMeta：cleanup_incomplete=true 必須判定違規', () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a2b2-taskD-wireEvidence-validate-'));
    const argv0 = fakeCodexBin(root);
    const meta = goodMeta(argv0, { cleanup_incomplete: true });
    const violations = validateWireMeta(meta, { provider: 'codex', expectedArgvTail: ['app-server'], expectedArgv0RealPath: argv0 });
    assert.ok(violations.some(v => v.includes('cleanup_incomplete')), `expected cleanup_incomplete violation, got: ${JSON.stringify(violations)}`);
  });

  await check('validateWireMeta：finalize_cause 含 drain timeout 訊號時，即使 exit_code=0 仍必須判定違規（不得因它含錯誤原因卻仍判成功）', () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a2b2-taskD-wireEvidence-validate-'));
    const argv0 = fakeCodexBin(root);
    const meta = goodMeta(argv0, { finalize_cause: 'codex: app-server exited; codex: wire log drain timed out before stdout EOF' });
    const violations = validateWireMeta(meta, { provider: 'codex', expectedArgvTail: ['app-server'], expectedArgv0RealPath: argv0 });
    assert.ok(violations.some(v => v.includes('finalize_cause')), `expected finalize_cause violation, got: ${JSON.stringify(violations)}`);
  });

  await check('validateWireMeta：finalize_cause 為正常的「app-server 自然退出」收尾原因時不算異常，不應因此判定違規', () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a2b2-taskD-wireEvidence-validate-'));
    const argv0 = fakeCodexBin(root);
    const meta = goodMeta(argv0, { finalize_cause: 'codex: app-server exited' });
    const violations = validateWireMeta(meta, { provider: 'codex', expectedArgvTail: ['app-server'], expectedArgv0RealPath: argv0 });
    assert.deepEqual(violations, []);
  });

  // --- validateWireMeta：負控制——provider 不符 ------------------------------
  await check('validateWireMeta：provider 不是 codex 必須判定違規', () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a2b2-taskD-wireEvidence-validate-'));
    const argv0 = fakeCodexBin(root);
    const meta = goodMeta(argv0, { provider: 'claude' });
    const violations = validateWireMeta(meta, { provider: 'codex', expectedArgvTail: ['app-server'], expectedArgv0RealPath: argv0 });
    assert.ok(violations.some(v => v.includes('provider')), `expected provider violation, got: ${JSON.stringify(violations)}`);
  });

  // ===========================================================================
  // B3a-2b-2 Task D 限縮補正（F1 失敗取證）：對準實際 orchestration 的整合
  // 覆蓋——下面這個 helper 直接呼叫 `codexApproval.spec.ts` 實際使用的同一組
  // 共用函式（`waitForWireMeta`＋`persistWireEvidence`＋`validateWireMeta`＋
  // `HarnessLogger`），呼叫順序與該 spec 的 F1 段落一致（先 wait、不論成敗
  // 都先 persist、再視情況 throw／validate），**不是把 spec 流程複製一份**
  // ——這裡只是用隔離的 tmp 檔案驅動同一批函式，驗證「失敗時原始 bytes 真
  // 的有被保留」與「成功時保留副本＝被驗證的 bytes」。
  // ===========================================================================
  interface CaptureFlowResult {
    waitResult: Awaited<ReturnType<typeof waitForWireMeta>> | undefined;
    waitError: unknown;
    persistResult: ReturnType<typeof persistWireEvidence>;
    harnessLogText: string;
  }

  async function runCaptureFlow(
    metaPath: string,
    jsonlSourcePath: string,
    destDir: string,
    opts: { deadlineMs?: number; pollIntervalMs?: number } = {},
    expectedArgv0RealPath = '(selftest 不核對 argv identity，僅驗證形狀與保存流程)',
  ): Promise<CaptureFlowResult> {
    const harness = new HarnessLogger(destDir);
    let waitResult: Awaited<ReturnType<typeof waitForWireMeta>> | undefined;
    let waitError: unknown;
    try {
      waitResult = await waitForWireMeta(metaPath, opts);
    } catch (e) {
      waitError = e;
    }
    const waitErrorDiagnostics = waitError instanceof WireMetaWaitError ? waitError.diagnostics : undefined;
    // 呼叫實際的共用保存函式——與 codexApproval.spec.ts F1 段落同一個呼叫
    // 順序：不論等待成敗，先保存當下已讀到的 bytes，再讓呼叫端決定要不要
    // throw。
    const persistResult = persistWireEvidence(
      { jsonlSourcePath, metaRaw: waitResult ? waitResult.raw : (waitErrorDiagnostics?.lastRawBytes as string | undefined) },
      destDir,
    );
    if (!waitResult) {
      harness.log(`F1 selftest：等待 finalize meta 失敗：${waitErrorDiagnostics ? JSON.stringify(waitErrorDiagnostics) : String(waitError)}；` +
        `已嘗試保存（wireLogCopied=${persistResult.wireLogCopied}／metaCopied=${persistResult.metaCopied}）`);
    } else {
      const violations = validateWireMeta(waitResult.meta, {
        provider: 'codex',
        expectedArgvTail: ['app-server'],
        expectedArgv0RealPath,
      });
      if (violations.length > 0) {
        harness.log(`F1 selftest：finalize meta 核對失敗：${JSON.stringify(violations)}`);
      } else {
        harness.log(`F1 selftest：finalize meta 已就緒且核對通過（等待 ${waitResult.elapsedMs}ms／共 ${waitResult.attempts} 次嘗試）`);
      }
    }
    return { waitResult, waitError, persistResult, harnessLogText: fs.readFileSync(harness.path(), 'utf8') };
  }

  // --- 缺口 1：meta 永遠缺檔——保留「不存在」診斷，不補造，wire 仍保存 ----
  await check('F1 orchestration：meta 永遠不出現時，persistWireEvidence 不寫任何 meta 檔（不補造），harness.log 留下「不存在」診斷，wire jsonl 仍被保存', async () => {
    const dir = freshWireLogsDir();
    const jsonlPath = writeGenerationJsonl(dir, 'codex-wire-missing-meta', approvalFrames());
    const metaPath = jsonlPath.replace(/\.jsonl$/, '.meta.json');
    const destDir = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a2b2-taskD-wireEvidence-persist-missing-'));

    const result = await runCaptureFlow(metaPath, jsonlPath, destDir, { deadlineMs: 200, pollIntervalMs: 50 });

    assert.equal(result.waitResult, undefined, 'waitForWireMeta 應失敗（meta 永遠不出現）');
    assert.ok(result.waitError instanceof WireMetaWaitError);
    assert.equal(result.persistResult.metaCopied, false, 'meta 從未讀到任何內容，不應標記 metaCopied');
    assert.ok(!fs.existsSync(path.join(destDir, 'app-wire-log.meta.json')), '不得補造任何 meta 檔案');
    assert.ok(
      result.persistResult.copyErrors.some(e => e.includes('不存在')),
      `copyErrors 應記錄「不存在」，實際：${JSON.stringify(result.persistResult.copyErrors)}`,
    );
    assert.equal(result.persistResult.wireLogCopied, true, 'wire jsonl 來源存在，即使 meta 缺失也應被保存');
    assert.ok(fs.existsSync(path.join(destDir, 'app-wire-log.jsonl')));
    assert.ok(result.harnessLogText.includes('等待 finalize meta 失敗'), 'harness.log 應留下失敗診斷');
  });

  // --- 缺口 1：meta 持續是損毀 JSON——原始（invalid JSON）原文必須被保留 ---
  await check('F1 orchestration：meta 持續是損毀 JSON（逾時），persistWireEvidence 把「invalid JSON 原文」原樣保存，不遺失、不補造合法內容', async () => {
    const dir = freshWireLogsDir();
    const jsonlPath = writeGenerationJsonl(dir, 'codex-wire-corrupt-meta', approvalFrames());
    const metaPath = jsonlPath.replace(/\.jsonl$/, '.meta.json');
    const corruptRaw = '{not valid json, but this is the actual bytes that existed at failure time';
    fs.writeFileSync(metaPath, corruptRaw);
    const destDir = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a2b2-taskD-wireEvidence-persist-corrupt-'));

    const result = await runCaptureFlow(metaPath, jsonlPath, destDir, { deadlineMs: 200, pollIntervalMs: 50 });

    assert.equal(result.waitResult, undefined, 'waitForWireMeta 應失敗（持續損毀 JSON）');
    assert.ok(result.waitError instanceof WireMetaWaitError);
    assert.equal(result.persistResult.metaCopied, true, '雖然 parse 失敗，但曾經成功讀到原始 bytes，應保存');
    const savedMetaRaw = fs.readFileSync(path.join(destDir, 'app-wire-log.meta.json'), 'utf8');
    assert.equal(savedMetaRaw, corruptRaw, '保留下來的 meta 副本必須是當時實際存在的原始 bytes（含 invalid JSON 原文），不是補造的合法內容');
    assert.ok(result.harnessLogText.includes('等待 finalize meta 失敗'), 'harness.log 應留下失敗診斷');
  });

  // --- 缺口 3：meta 為 null（合法 JSON、錯誤形狀）——不 TypeError，走同一條保存流程 ---
  await check('F1 orchestration：meta 內容為 JSON literal null 時，waitForWireMeta 視為成功讀到（合法 JSON），但 validateWireMeta 必須回傳明確 violation 而非 TypeError；保留副本＝被驗證的 bytes', async () => {
    const dir = freshWireLogsDir();
    const jsonlPath = writeGenerationJsonl(dir, 'codex-wire-null-meta', approvalFrames());
    const metaPath = jsonlPath.replace(/\.jsonl$/, '.meta.json');
    fs.writeFileSync(metaPath, 'null');
    const destDir = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a2b2-taskD-wireEvidence-persist-null-'));

    const result = await runCaptureFlow(metaPath, jsonlPath, destDir, { deadlineMs: 200, pollIntervalMs: 50 });

    assert.ok(result.waitResult, 'waitForWireMeta 應成功讀到內容（null 是合法 JSON）');
    assert.equal(result.waitResult?.meta, null);
    // 呼叫 validateWireMeta 本身不得 TypeError（這是 reviewer 實測抓到的缺陷）。
    assert.doesNotThrow(() => validateWireMeta(result.waitResult!.meta, {
      provider: 'codex', expectedArgvTail: ['app-server'], expectedArgv0RealPath: '/nonexistent',
    }));
    const violations = validateWireMeta(result.waitResult!.meta, {
      provider: 'codex', expectedArgvTail: ['app-server'], expectedArgv0RealPath: '/nonexistent',
    });
    assert.ok(violations.length > 0, 'meta=null 必須判定為明確違規，不是空陣列（false PASS）');
    assert.ok(violations[0].includes('null'), `violation 訊息應說明實際型別，實際：${JSON.stringify(violations)}`);
    assert.equal(result.persistResult.metaCopied, true);
    const savedMetaRaw = fs.readFileSync(path.join(destDir, 'app-wire-log.meta.json'), 'utf8');
    assert.equal(savedMetaRaw, 'null', '保留副本必須與 waitForWireMeta 讀到、且被 validateWireMeta 拿去核對的 raw bytes 完全相同');
    assert.equal(savedMetaRaw, result.waitResult!.raw);
    assert.ok(result.harnessLogText.includes('finalize meta 核對失敗'), 'harness.log 應留下核對失敗診斷（不是略過）');
  });

  // --- 缺口 3：meta 為 array／primitive 時同樣不得 TypeError -----------------
  await check('F1 orchestration：meta 內容為 JSON array 或 primitive（number）時，validateWireMeta 同樣回傳明確 violation 而非 TypeError', () => {
    const root = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a2b2-taskD-wireEvidence-shape-'));
    const argv0 = fakeCodexBin(root);
    const exp = { provider: 'codex', expectedArgvTail: ['app-server'], expectedArgv0RealPath: argv0 };

    const arrayMeta = [] as unknown as WireMeta;
    assert.doesNotThrow(() => validateWireMeta(arrayMeta, exp));
    const arrayViolations = validateWireMeta(arrayMeta, exp);
    assert.ok(arrayViolations.length > 0 && arrayViolations[0].includes('array'), `array 應判定違規並說明型別，實際：${JSON.stringify(arrayViolations)}`);

    const primitiveMeta = 42 as unknown as WireMeta;
    assert.doesNotThrow(() => validateWireMeta(primitiveMeta, exp));
    const primitiveViolations = validateWireMeta(primitiveMeta, exp);
    assert.ok(primitiveViolations.length > 0 && primitiveViolations[0].includes('number'), `primitive 應判定違規並說明型別，實際：${JSON.stringify(primitiveViolations)}`);
  });

  // --- 缺口 1／2：nonzero exit／錯 argv（內容違規）——先保存才判定，保留副本＝被驗證 bytes ---
  await check('F1 orchestration：meta 內容違規（exit_code!=0）時，persistWireEvidence 已在 validateWireMeta 判定之前把 bytes 存進證據目錄；保留副本＝被驗證的 bytes', async () => {
    const dir = freshWireLogsDir();
    const jsonlPath = writeGenerationJsonl(dir, 'codex-wire-bad-exit', approvalFrames());
    const metaPath = jsonlPath.replace(/\.jsonl$/, '.meta.json');
    const argv0 = fakeCodexBin(dir);
    fs.writeFileSync(metaPath, JSON.stringify(goodMeta(argv0, { exit_code: 17 })));
    const destDir = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a2b2-taskD-wireEvidence-persist-badexit-'));

    const result = await runCaptureFlow(metaPath, jsonlPath, destDir, { deadlineMs: 3000, pollIntervalMs: 50 });

    assert.ok(result.waitResult, 'waitForWireMeta 本身應成功（meta 是合法 JSON object，只是內容違規）');
    // 保存必須已經發生——即使後續 validateWireMeta 判定違規，也不影響已經
    // 保存的事實（模擬 spec 裡「先 persist 再 expect」的順序）。
    assert.equal(result.persistResult.metaCopied, true);
    const savedMetaRaw = fs.readFileSync(path.join(destDir, 'app-wire-log.meta.json'), 'utf8');
    assert.equal(savedMetaRaw, result.waitResult!.raw, '保留副本必須與被驗證的 bytes 相同');
    const violations = validateWireMeta(JSON.parse(savedMetaRaw) as WireMeta, {
      provider: 'codex', expectedArgvTail: ['app-server'], expectedArgv0RealPath: argv0,
    });
    assert.ok(violations.some(v => v.includes('exit_code')), 'exit_code=17 應判定違規');
    assert.ok(result.harnessLogText.includes('finalize meta 核對失敗'), 'harness.log 應留下核對失敗診斷');
  });

  // --- 正控制：成功路徑——保留副本必須與被驗證的 bytes 完全相同 -------------
  await check('F1 orchestration：正常成功路徑下，persistWireEvidence 保存的 meta 副本與 waitForWireMeta 回傳、且被 validateWireMeta 驗證的 raw bytes 逐位元組相同', async () => {
    const dir = freshWireLogsDir();
    const jsonlPath = writeGenerationJsonl(dir, 'codex-wire-good', approvalFrames());
    const metaPath = jsonlPath.replace(/\.jsonl$/, '.meta.json');
    const argv0 = fakeCodexBin(dir);
    fs.writeFileSync(metaPath, JSON.stringify(goodMeta(argv0)));
    const destDir = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a2b2-taskD-wireEvidence-persist-good-'));

    const result = await runCaptureFlow(metaPath, jsonlPath, destDir, { deadlineMs: 3000, pollIntervalMs: 50 }, argv0);

    assert.ok(result.waitResult);
    const violations = validateWireMeta(result.waitResult!.meta, {
      provider: 'codex', expectedArgvTail: ['app-server'], expectedArgv0RealPath: argv0,
    });
    assert.deepEqual(violations, []);
    assert.equal(result.persistResult.metaCopied, true);
    assert.equal(result.persistResult.wireLogCopied, true);
    const savedMetaRaw = fs.readFileSync(path.join(destDir, 'app-wire-log.meta.json'), 'utf8');
    assert.equal(savedMetaRaw, result.waitResult!.raw, '成功路徑保留副本必須與被驗證的 bytes 完全相同（不得另外留一份未驗證的副本）');
    const savedWireRaw = fs.readFileSync(path.join(destDir, 'app-wire-log.jsonl'), 'utf8');
    assert.equal(savedWireRaw, fs.readFileSync(jsonlPath, 'utf8'));
    assert.ok(result.harnessLogText.includes('finalize meta 已就緒且核對通過'), 'harness.log 應留下成功診斷');
  });

  // reviewer #285 的三條共用判定測試也要計入同一份總數，否則 summary 會少報。
  await persistGuardChecks();
  console.log(`\n${passed} passed, 0 failed (any FAIL above sets process.exitCode=1)`);
}

// reviewer #285 補：必要證據保存的 fail 判定必須是 **spec 與 selftest 共用的同一個
// 函式**（`assertWireEvidencePersisted`）。先前 selftest 自行編排 wait→persist→
// validate、且不執行 spec 的成功／失敗判定，所以 21 項綠燈抓不到 spec 漏檢
// `metaCopied`。下面三條直接呼叫真正的判定：**移除 `metaCopied` guard 時第 1 條
// 必紅、移除 `wireLogCopied` guard 時第 2 條必紅。**
async function persistGuardChecks(): Promise<void> {
  await check('F1 保存判定（共用）：meta-only 寫入失敗（目的位置已存在同名目錄）時 assertWireEvidencePersisted 必須 reject——移除 metaCopied guard 此條必紅', () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'f1-guard-meta-'));
    const src = path.join(dir, 'g.jsonl');
    fs.writeFileSync(src, '{"a":1}\n');
    const dest = path.join(dir, 'dest');
    fs.mkdirSync(dest);
    // 預建同名目錄 → meta 寫入必定失敗（EISDIR），wire 仍可複製成功
    fs.mkdirSync(path.join(dest, 'app-wire-log.meta.json'));
    const r = persistWireEvidence({ jsonlSourcePath: src, metaRaw: '{"provider":"codex"}' }, dest);
    assert.equal(r.wireLogCopied, true, 'wire 應複製成功（重現 reviewer 的反例形狀）');
    assert.equal(r.metaCopied, false, 'meta 應寫入失敗');
    assert.throws(
      () => assertWireEvidencePersisted(r, { requireMeta: true }),
      /必要證據未能保存/,
      'meta 未保存時共用判定必須 throw，不得只 warning',
    );
  });

  await check('F1 保存判定（共用）：wire-only 複製失敗時 assertWireEvidencePersisted 必須 reject——移除 wireLogCopied guard 此條必紅', () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'f1-guard-wire-'));
    const src = path.join(dir, 'g.jsonl');
    fs.writeFileSync(src, '{"a":1}\n');
    const dest = path.join(dir, 'dest');
    fs.mkdirSync(dest);
    fs.mkdirSync(path.join(dest, 'app-wire-log.jsonl'));
    const r = persistWireEvidence({ jsonlSourcePath: src, metaRaw: '{"provider":"codex"}' }, dest);
    assert.equal(r.wireLogCopied, false, 'wire 應複製失敗');
    assert.equal(r.metaCopied, true, 'meta 仍應保存成功');
    assert.throws(
      () => assertWireEvidencePersisted(r, { requireMeta: true }),
      /必要證據未能保存/,
      'wire 未保存時共用判定必須 throw',
    );
  });

  await check('F1 保存判定（共用）：wire 與 meta 都保存成功時不得 throw（正控制）', () => {
    const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'f1-guard-ok-'));
    const src = path.join(dir, 'g.jsonl');
    fs.writeFileSync(src, '{"a":1}\n');
    const dest = path.join(dir, 'dest');
    fs.mkdirSync(dest);
    const r = persistWireEvidence({ jsonlSourcePath: src, metaRaw: '{"provider":"codex"}' }, dest);
    assert.equal(r.wireLogCopied, true);
    assert.equal(r.metaCopied, true);
    assert.doesNotThrow(() => assertWireEvidencePersisted(r, { requireMeta: true }));
  });
}

main().catch(e => {
  console.error(e);
  process.exitCode = 1;
});
