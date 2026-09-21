// B3a-2b-2 Task D 限縮補正（F1）：App wire 的 `<id>.meta.json` 從
// `codexApproval.spec.ts` 原本的 optional（`if (fs.existsSync(...))`，缺了不
// 失敗）升格為必要證據。純函式／bounded 輪詢邏輯抽在這裡，讓正負控制可以
// 獨立跑 selftest（不需要真 App／真瀏覽器）——`codexApproval.spec.ts` 只呼叫
// 這裡的函式，不重複判定邏輯。
//
// 背景（實證）：首輪四案中 `commandExecution-deny`
// （run `20260921T084322Z-8037b2`）缺 `app-wire-log.meta.json`，其餘三案有。
// 讀碼佐證的時序風險：`internal/wirelog/wirelog.go` 的 `Generation.Finalize`
// 先 close wire 檔再寫 meta；`internal/codex/owner.go` 的
// `WatchGeneration`／`FinalizeWith` 是在 fake app-server 子程序**自然退出**
// 後才**非同步**觸發收尾——不是每次都能在測試碼讀到 wire-logs 目錄之前完
// 成。這不足以確定本次缺檔的唯一原因，故用「有上限的等待」而不是假設某個
// 固定延遲一定夠。
import fs from 'node:fs';
import path from 'node:path';

// ---------------------------------------------------------------------------
// generation 選擇：以「內容含本案 approval request」為準，不是只挑 mtime 最新
// ---------------------------------------------------------------------------

export interface WireFrame {
  frame: number;
  dir: 'c2s' | 's2c';
  wsid: string;
  raw: { id?: unknown; method?: string; result?: { decision?: string } };
}

export function readWireFrames(jsonlPath: string): WireFrame[] {
  return fs
    .readFileSync(jsonlPath, 'utf8')
    .split('\n')
    .map(l => l.trim())
    .filter(l => l.length > 0)
    .map(l => JSON.parse(l) as WireFrame);
}

export interface WireIdentity {
  approvalMethod: string;
  approvalRequestId: string;
}

export interface GenerationCandidate {
  file: string;
  jsonlPath: string;
  metaPath: string;
  mtimeMs: number;
}

// selectGenerationsByIdentity：掃 wireLogsDir 底下的 `.jsonl` generation
// 檔案，回傳「內容含本案 approval request（s2c、method＋id 相符）」的那些
// 候選（依 mtime 新到舊排序）。刻意不用 mtime 當首要判準——mtime 最新可能
// 是同目錄殘留自另一次執行或另一個尚未送出 approval 的 generation。讀不到
// ／解析不出來的檔案略過（不算候選，也不是本函式要回報的失敗——那類檔案
// 不是本案的錄流，交給呼叫端決定「零候選」時如何處理）。
export function selectGenerationsByIdentity(wireLogsDir: string, identity: WireIdentity): GenerationCandidate[] {
  const files = fs.existsSync(wireLogsDir) ? fs.readdirSync(wireLogsDir).filter(f => f.endsWith('.jsonl')) : [];
  const matches: GenerationCandidate[] = [];
  for (const f of files) {
    const jsonlPath = path.join(wireLogsDir, f);
    let frames: WireFrame[];
    try {
      frames = readWireFrames(jsonlPath);
    } catch {
      continue;
    }
    const hasRequest = frames.some(
      fr => fr.dir === 's2c' && fr.raw.method === identity.approvalMethod && fr.raw.id === identity.approvalRequestId,
    );
    if (hasRequest) {
      matches.push({
        file: f,
        jsonlPath,
        metaPath: jsonlPath.replace(/\.jsonl$/, '.meta.json'),
        mtimeMs: fs.statSync(jsonlPath).mtimeMs,
      });
    }
  }
  return matches.sort((a, b) => b.mtimeMs - a.mtimeMs);
}

// ---------------------------------------------------------------------------
// meta 的有上限等待：不固定 sleep，deadline 到仍失敗就保留最後一次錯誤、不吞
// ---------------------------------------------------------------------------

export interface WireMeta {
  provider?: string;
  cli_version?: string;
  argv?: string[];
  cwd?: string;
  recorded_at?: string;
  exit_code?: number;
  process_still_running?: boolean;
  stderr_tail?: string;
  recorder_error?: string;
  finalize_cause?: string;
  cleanup_incomplete?: boolean;
}

export class WireMetaWaitError extends Error {
  readonly diagnostics: Record<string, unknown>;
  constructor(message: string, diagnostics: Record<string, unknown>) {
    super(message);
    this.name = 'WireMetaWaitError';
    this.diagnostics = diagnostics;
  }
}

export interface WireMetaWaitResult {
  meta: WireMeta;
  // 成功讀到並解析出 `meta` 的那份原始位元組（未經任何轉換）——呼叫端用這份
  // `raw` 直接落地保存，保證「被驗證的內容」與「保留下來的副本」是同一份
  // bytes，不再另外重新讀一次來源路徑（B3a-2b-2 Task D F1 逐條 2）。
  raw: string;
  attempts: number;
  elapsedMs: number;
}

// waitForWireMeta：對「同一個本次 generation」的 meta 做有上限的輪詢等待
// （F1 逐條 1，沿用本案合理的 15 秒級上限，呼叫端可覆寫）。允許 bounded
// retry 處理 `os.WriteFile` 非原子寫入造成的短暫不完整內容（存在但
// `JSON.parse` 失敗，視為 transient）；檔案尚未出現同樣視為 transient；
// deadline 到仍未成功讀到有效 meta，丟出 `WireMetaWaitError`，帶最後一次
// 錯誤與嘗試次數，不吞、不合成假 meta。
export async function waitForWireMeta(
  metaPath: string,
  opts: { deadlineMs?: number; pollIntervalMs?: number } = {},
): Promise<WireMetaWaitResult> {
  const deadlineMs = opts.deadlineMs ?? 15_000;
  const pollIntervalMs = opts.pollIntervalMs ?? 100;
  const start = Date.now();
  let attempts = 0;
  let lastErrorMessage = `${metaPath} 尚未出現`;
  // 最後一次成功讀到的原始位元組（不論解析／型別是否合法）——供逾時失敗時
  // 一併回報，讓呼叫端能把「當下可讀到的原始內容（含 invalid JSON 原文）」
  // 存進證據目錄，不因為 JSON.parse 失敗或最終沒等到有效 meta 就整段遺失
  // （B3a-2b-2 Task D F1 逐條 1）。檔案從未存在過時維持 undefined，呼叫端據
  // 此只記錄「不存在」，不得補造內容。
  let lastRawBytes: string | undefined;
  for (;;) {
    attempts += 1;
    if (fs.existsSync(metaPath)) {
      try {
        const raw = fs.readFileSync(metaPath, 'utf8');
        lastRawBytes = raw;
        const meta = JSON.parse(raw) as WireMeta;
        return { meta, raw, attempts, elapsedMs: Date.now() - start };
      } catch (e) {
        lastErrorMessage = `讀到 ${metaPath} 但解析失敗（可能是非原子寫入的暫態不完整內容）：${e instanceof Error ? e.message : String(e)}`;
      }
    } else {
      lastErrorMessage = `${metaPath} 尚未出現`;
    }
    const elapsed = Date.now() - start;
    if (elapsed >= deadlineMs) {
      throw new WireMetaWaitError(
        `waitForWireMeta: 逾時 ${deadlineMs}ms（實際等待 ${elapsed}ms，共嘗試 ${attempts} 次）仍未讀到有效 meta（${metaPath}），最後錯誤：${lastErrorMessage}`,
        { metaPath, deadlineMs, attempts, elapsedMs: elapsed, lastError: lastErrorMessage, lastRawBytes },
      );
    }
    await new Promise(resolve => setTimeout(resolve, pollIntervalMs));
  }
}

// ---------------------------------------------------------------------------
// meta 內容核對：provider／exit_code／argv／無 recorder_error 或
// cleanup_incomplete 之類異常；finalize_cause 保留但不得放過其中的異常訊號
// ---------------------------------------------------------------------------

export interface WireMetaExpectation {
  provider: string;
  // argv[0] 之外的其餘段落（例如 `['app-server']`）。
  expectedArgvTail: string[];
  // argv[0] 的 canonical path（呼叫端先 `fs.realpathSync` 過的本次 codex
  // wrapper 路徑）。
  expectedArgv0RealPath: string;
}

// validateWireMeta：核對 meta 是否符合本次 generation 的獨立期望。回傳
// violation 字串陣列（空陣列＝通過）。
//   - provider 必須等於 exp.provider。
//   - exit_code 必須是數字且為 0；缺少／非數字一律算違規（不得當成 0）。
//   - recorder_error／cleanup_incomplete 不得存在／不得為 true。
//   - finalize_cause 保留在回傳的 meta 裡（呼叫端可原樣記錄／複製），但如果
//     其中含有 drain timeout 之類的額外收尾異常訊號，仍判定違規——正常的
//     「app-server 自然退出」收尾原因（例如 `codex: app-server exited`）不算
//     異常，不在這裡擋。
//   - argv 必須存在，argv[0] 的 canonical path 須等於本次 codex wrapper，
//     其餘段落須等於 exp.expectedArgvTail。
export function validateWireMeta(meta: WireMeta, exp: WireMetaExpectation): string[] {
  // B3a-2b-2 Task D F1 逐條 3：`waitForWireMeta` 對 `<id>.meta.json` 只做
  // `JSON.parse`（`wireEvidence.ts` 原本的 `JSON.parse(raw) as WireMeta`），
  // 沒有任何 runtime 型別檢查——`null`／array／primitive 都是合法 JSON，會
  // 被當成「成功讀到 meta」直接回傳。下面存取 `meta.provider` 等欄位在這些
  // 情況下會丟 TypeError，讓呼叫端（codexApproval.spec.ts）的失敗診斷與證據
  // 保存整段被略過——這與 `executionMode.ts` 先前被抓到的是同一類缺陷（缺
  // runtime object／non-null／non-array 檢查）。這裡在存取任何欄位之前先
  // 檢查形狀，不合法就回傳明確 violation 字串（不 throw），讓呼叫端可以照
  // 一般失敗路徑處理（fail loud＋照樣走保存流程），不是「看起來通過」的
  // false PASS。
  if (typeof meta !== 'object' || meta === null || Array.isArray(meta)) {
    const actualShape = meta === null ? 'null' : Array.isArray(meta) ? 'array' : typeof meta;
    return [`meta 不是有效的 JSON object（實際型別：${actualShape}，內容：${JSON.stringify(meta)}），無法核對欄位`];
  }

  const violations: string[] = [];

  if (meta.provider !== exp.provider) {
    violations.push(`provider 應為 ${JSON.stringify(exp.provider)}，實際 ${JSON.stringify(meta.provider)}`);
  }

  if (typeof meta.exit_code !== 'number') {
    violations.push(`exit_code 缺失或非數字（不得當成 0）：${JSON.stringify(meta.exit_code)}`);
  } else if (meta.exit_code !== 0) {
    violations.push(`exit_code 應為 0，實際 ${meta.exit_code}`);
  }

  if (meta.recorder_error) {
    violations.push(`recorder_error 不應存在：${meta.recorder_error}`);
  }
  if (meta.cleanup_incomplete) {
    violations.push('cleanup_incomplete 不應為 true');
  }
  if (meta.finalize_cause && /timed out/i.test(meta.finalize_cause)) {
    violations.push(`finalize_cause 顯示異常收尾（含逾時訊號，不因 exit_code=0 就放過）：${meta.finalize_cause}`);
  }

  if (!Array.isArray(meta.argv) || meta.argv.length === 0) {
    violations.push(`argv 缺失或格式錯誤：${JSON.stringify(meta.argv)}`);
    return violations;
  }

  const tail = meta.argv.slice(1);
  if (JSON.stringify(tail) !== JSON.stringify(exp.expectedArgvTail)) {
    violations.push(`argv 尾段應為 ${JSON.stringify(exp.expectedArgvTail)}，實際 ${JSON.stringify(tail)}`);
  }
  try {
    const argv0Real = fs.realpathSync(meta.argv[0]);
    if (argv0Real !== exp.expectedArgv0RealPath) {
      violations.push(`argv[0] 的 canonical path 應等於本次 codex wrapper（${exp.expectedArgv0RealPath}），實際 ${argv0Real}`);
    }
  } catch (e) {
    violations.push(`argv[0]（${meta.argv[0]}）無法 realpath：${e instanceof Error ? e.message : String(e)}`);
  }

  return violations;
}

// ---------------------------------------------------------------------------
// persistWireEvidence：失敗／成功共用的唯一保存函式（B3a-2b-2 Task D F1 逐
// 條 1／2 的共同修正點）
// ---------------------------------------------------------------------------
//
// 背景（reviewer 裁定的三個缺口）：原本 `codexApproval.spec.ts` 把「複製 wire
// ＋meta 到證據目錄」放在 `expect(metaViolations).toEqual([])` **之後**——
// `exit_code!=0`／錯 argv／`recorder_error` 等內容違規會在複製前就先
// throw，wire 與 meta 都沒存進 artifacts；逾時的 catch block 也只複製了
// wire，沒有一併保存已經讀到（即使是損毀／不完整）的 meta 原始內容。而
// `global-teardown.ts` 稍後會刪除 fixture 目錄，原始失敗內容就此永久消失。
//
// 這個函式把「保存」與「判定」拆開，讓呼叫端可以在任何 expect 可能丟出之前
// 先呼叫一次，保證：
//   - wire jsonl：只要來源檔存在就嘗試複製（不論後續內容判定是否通過）。
//   - meta：呼叫端傳入「已經讀到的原始位元組」（`waitForWireMeta` 成功時的
//     `raw`，或逾時失敗時 `WireMetaWaitError.diagnostics.lastRawBytes`），本
//     函式**只把這份 bytes 原樣寫出**，不重新讀一次來源路徑——這樣「被驗證
//     的內容」與「保留下來的副本」保證是同一份 bytes（逐條 2）。
//   - 缺 meta（`metaRaw` 為 `undefined`，代表整段等待期間從未成功讀到任何
//     內容）時只在 `copyErrors` 記一筆「不存在」，不寫任何 meta 檔案、不
//     補造內容（逐條 1 的「絕對不得補造」）。
//   - 複製過程本身的失敗（例如磁碟寫入錯誤）記進 `copyErrors`，用
//     `try/catch` 包住、不 throw，不會掩蓋呼叫端原本要拋出的錯誤（逾時／
//     內容違規），也不會擋住既有的 bounded teardown。
export interface WireEvidencePersistInput {
  // wire jsonl 的來源路徑（`GenerationCandidate.jsonlPath`）。
  jsonlSourcePath: string;
  // 已經讀到的 meta 原始位元組；`undefined` 代表整段等待期間從未成功讀到
  // 任何內容（含目錄一路不存在、檔案一路不存在的情況）。
  metaRaw: string | undefined;
}

export interface WireEvidencePersistResult {
  wireLogCopied: boolean;
  metaCopied: boolean;
  wireLogDestPath: string;
  metaDestPath: string;
  // 保存過程的診斷（複製失敗／meta 不存在），供 harness.log 記錄；不代表
  // 呼叫端一定要因此判定測試失敗——那是呼叫端（判定內容／等待結果）的責任。
  copyErrors: string[];
}

export function persistWireEvidence(
  input: WireEvidencePersistInput,
  destDir: string,
  filenames: { wireLog: string; meta: string } = { wireLog: 'app-wire-log.jsonl', meta: 'app-wire-log.meta.json' },
): WireEvidencePersistResult {
  const wireLogDestPath = path.join(destDir, filenames.wireLog);
  const metaDestPath = path.join(destDir, filenames.meta);
  const copyErrors: string[] = [];
  let wireLogCopied = false;
  let metaCopied = false;

  try {
    if (fs.existsSync(input.jsonlSourcePath)) {
      fs.copyFileSync(input.jsonlSourcePath, wireLogDestPath);
      wireLogCopied = true;
    } else {
      copyErrors.push(`wire jsonl 來源不存在，無法保存：${input.jsonlSourcePath}`);
    }
  } catch (e) {
    // 複製本身失敗要另記診斷，不得掩蓋呼叫端原本要拋出的錯誤——這裡只記錄
    // 不 throw。
    copyErrors.push(`wire jsonl 複製失敗（${input.jsonlSourcePath} → ${wireLogDestPath}）：${e instanceof Error ? e.message : String(e)}`);
  }

  if (input.metaRaw === undefined) {
    copyErrors.push('meta 整段等待期間未曾成功讀到任何內容，僅記錄「不存在」，不補造內容');
  } else {
    try {
      fs.writeFileSync(metaDestPath, input.metaRaw);
      metaCopied = true;
    } catch (e) {
      copyErrors.push(`meta 寫入失敗（${metaDestPath}）：${e instanceof Error ? e.message : String(e)}`);
    }
  }

  return { wireLogCopied, metaCopied, wireLogDestPath, metaDestPath, copyErrors };
}

// B3a-2b-2 Task D F1 補正（reviewer #285）：**spec 與 selftest 共用的必要證據
// 保存判定**。
//
// 先前 `codexApproval.spec.ts` 只在成功路徑檢查 `wireLogCopied`，`metaCopied`
// 僅寫進 harness.log。reviewer 以「目的位置預建同名目錄」重現：來源 meta 合法、
// wire 複製成功、但 `app-wire-log.meta.json` 寫入失敗（EISDIR）時
// `metaViolations=[]`＋`wireLogCopied=true`，測試照樣通過，**必要的 meta 證據卻
// 沒保存**——這是 false PASS。
//
// 判定寫成這個函式而不是留在 spec 裡，是因為 selftest 若自行再編排一次
// wait→persist→validate，就抓不到 spec 漏檢；spec 與 selftest 必須呼叫**同一個**
// 判定，移除下面任一 guard 時兩邊都要紅。
//
// 錯誤優先序：本函式只在「等待與內容驗證都已通過」之後才由呼叫端執行，因此
// 不會掩蓋原始的 wait／validation 錯誤（失敗路徑仍是先保存、再 rethrow 原錯誤）。
export interface WireEvidenceRequirement {
  // 本次是否要求 meta 副本。成功路徑（已讀到合法 meta）一律為 true。
  requireMeta: boolean;
}

export function assertWireEvidencePersisted(
  result: WireEvidencePersistResult,
  req: WireEvidenceRequirement = { requireMeta: true },
): void {
  const missing: string[] = [];
  if (!result.wireLogCopied) missing.push(`wire jsonl 未保存（${result.wireLogDestPath}）`);
  if (req.requireMeta && !result.metaCopied) missing.push(`meta 未保存（${result.metaDestPath}）`);
  if (missing.length === 0) return;
  throw new Error(
    `F1：必要證據未能保存，不得以警告帶過：${missing.join('；')}`
    + (result.copyErrors.length > 0 ? `；保存診斷：${result.copyErrors.join('；')}` : ''),
  );
}
