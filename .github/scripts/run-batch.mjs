#!/usr/bin/env node
// run-batch.mjs — B3a-CI-1 批次執行器。依 decision470 收斂實作選擇 #2／#3：
// 單次事件只跑一個所選批次，批次內串行，每案獨立 run-id／輸出目錄；任一案
// 非預期失敗就停止同批次後續 invocation，已完成案例的證據保留、不回溯作廢。
//
// review round 3（#480 CHANGES_REQUIRED R4／R5）修法摘要：
//   - **單一持久 verdict**：package-e2e-evidence.sh 現在是唯一的判定權威
//     （內部呼叫一次 evaluate-e2e-evidence.mjs，寫出 verdict.json），這裡
//     不再「打包完再重跑一次 evaluator」——先前兩次獨立呼叫理論上可能得到
//     不同結論（NO-RUN.txt 只在第一次呼叫寫入等），現在只信任 package
//     script 寫出的 verdict.json。
//   - **有界執行**：package／evaluate 的 spawnSync 先前沒有 timeout（只有
//     wrapper 自己內部的 deadline/grace）。現在三段各自有明確上限，加總可
//     證明 ≤30 分鐘／entry（wrapper 25+2=27min、package 2min、
//     evaluator（package 內部呼叫，不再外部重跑）已含在 package 的 2min
//     預算內）——這是「其他能直接證明等價的有界實作」，不是逐案 GH step，
//     但邊界一樣是可核算、非拍腦袋的數字。
//   - **不覆寫 attempt**：工作目錄現在包含 `CI_E2E_ATTEMPT_ID`（未設定時自
//     動產生 timestamp+random），caseDir 若已存在且非空一律拒絕，不靜默
//     沿用/覆寫前一次 attempt 的證據。
//   - **候選 run 目錄安全保存**：0 或 >1 個候選時，仍把候選清單寫進
//     candidate-dirs.txt，package script 會逐一複製保留（不只是「拒絕驗
//     收」，原始資料也留著供診斷）。
//   - **spawn 前的 env 邊界**：不再直接 `...process.env` 整包帶入 child；
//     檢查 ambient env 有沒有汙染這個 entry 不該有的 E2E_*
//     controlled 變數，有就拒絕 spawn（見 sanitizeEnvForEntry()）。
//   - **offline 測試鉤子的邊界**：`CI_E2E_TEST_RUNNER` 現在必須同時搭配
//     `CI_E2E_OFFLINE_SELFTEST=1` 才會生效，避免這個測試鉤子被正常 CI 環
//     境的雜散 env 意外觸發。
//
// 用法：node run-batch.mjs <smoke|gates|scenarios>
//   環境變數：
//     CI_E2E_REPO_ROOT       repo root（預設 process.cwd()）
//     CI_E2E_WORK_ROOT       本次批次的工作目錄根（預設
//                            <repo>/.github/.ci-e2e-work/<batch>）
//     CI_E2E_ATTEMPT_ID      本次 attempt 的唯一 id（預設自動產生）
//     CI_E2E_ARTIFACTS_ROOT  harness 產出 run 目錄的根目錄（預設
//                            <repo>/frontend/e2e/.artifacts；測試模式建議另
//                            指一個獨立目錄，不要污染真正的 harness 輸出）
//     CI_E2E_OFFLINE_SELFTEST=1 + CI_E2E_TEST_RUNNER=<path>
//                            僅供離線 selftest：兩者都設才會用假子程序取代
//                            真實 run-e2e.mjs，production workflow 絕不設定。
'use strict';
import { randomBytes } from 'node:crypto';
import { existsSync, mkdirSync, readFileSync, readdirSync, statSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import {
  entriesForBatch,
  realCommandForEntry,
  WRAPPER_DEADLINE_SECONDS,
  WRAPPER_GRACE_SECONDS,
  CONTROLLED_ENV_KEYS,
  ENTRY_HARD_BUDGET_SECONDS,
} from './ci-e2e-entries.mjs';
import { checkEnvelopeBaseAll } from './envelope-checks.mjs';
import { boundedSpawn, isAbnormalBoundedSpawnResult } from './bounded-spawn.mjs';
import { rejectTestControlEnvUnderGithubActions } from './gha-runtime-guard.mjs';

const __dirname = path.dirname(fileURLToPath(import.meta.url));

// review round 7（#491 G2）：三個正常 GitHub Actions 入口之一（另兩個是
// evaluate-e2e-evidence.mjs／generate-ci-envelope.mjs）。在任何 runtime／
// probe spawn 及工作目錄建立之前，最早可能的時機就拒絕本票的測試替身／豁
// 免／版本覆寫——見 gha-runtime-guard.mjs 頭註。
rejectTestControlEnvUnderGithubActions('run-batch.mjs', [
  'CI_E2E_TEST_RUNNER',
  'CI_E2E_OFFLINE_SELFTEST',
  'CI_E2E_SELFTEST_ACTUAL_NODE_VERSION',
  'E2E_WRAPPER_DEADLINE_SECONDS_OVERRIDE',
  'E2E_WRAPPER_GRACE_SECONDS_OVERRIDE',
]);

const batchName = process.argv[2];
if (!batchName) {
  process.stderr.write('usage: run-batch.mjs <smoke|gates|scenarios>\n');
  process.exit(2);
}

let entries;
try {
  entries = entriesForBatch(batchName);
} catch (e) {
  process.stderr.write(`${e.message}\n`);
  process.exit(2);
}

const REPO_ROOT = process.env.CI_E2E_REPO_ROOT ?? process.cwd();
const ATTEMPT_ID = process.env.CI_E2E_ATTEMPT_ID ?? `${new Date().toISOString().replace(/[-:]/g, '').replace(/\.\d+Z$/, 'Z')}-${randomBytes(3).toString('hex')}`;
const WORK_ROOT_BASE = process.env.CI_E2E_WORK_ROOT ?? path.join(REPO_ROOT, '.github', '.ci-e2e-work', batchName);
const WORK_ROOT = path.join(WORK_ROOT_BASE, ATTEMPT_ID);
const ARTIFACTS_ROOT = process.env.CI_E2E_ARTIFACTS_ROOT ?? path.join(REPO_ROOT, 'frontend', 'e2e', '.artifacts');

// review round 5（#483 S4）：offline 測試鉤子的邊界——先前只設
// CI_E2E_TEST_RUNNER、缺 CI_E2E_OFFLINE_SELFTEST=1 時只印警告，把
// TEST_RUNNER 設回 null，接著 buildCommand() 會回退成
// realCommandForEntry()（真正的 run-e2e.mjs，會啟動 App／browser）——這正
// 是決定文件點名的缺口：「本來要離線跑的操作變成啟動 App／browser」，與
// 失敗時停止的契約相反。反過來（只設 offline、缺 fake runner 路徑）也可
// 能走到真實入口（TEST_RUNNER 為 null，buildCommand() 一樣回退）。
//
// 修法：兩種不完整／互相矛盾的組合都必須在**任何 spawn 之前**非零結束，不
// 回退、不嘗試修正——這個判定發生在讀 entries、建立 WORK_ROOT 之前，比
// entries 迴圈本身還早，結構上不可能有任何 entry 走到 spawn。
const rawTestRunnerPath = process.env.CI_E2E_TEST_RUNNER;
const rawOfflineFlag = process.env.CI_E2E_OFFLINE_SELFTEST;
const offlineSelftestRequested = rawOfflineFlag === '1';
const offlineTestRunnerRequested = typeof rawTestRunnerPath === 'string' && rawTestRunnerPath.length > 0;
if (offlineTestRunnerRequested !== offlineSelftestRequested) {
  const reason = offlineTestRunnerRequested
    ? 'CI_E2E_TEST_RUNNER 已設定但缺 CI_E2E_OFFLINE_SELFTEST=1——不得回退成真正的 run-e2e.mjs（會啟動 App／browser）'
    : 'CI_E2E_OFFLINE_SELFTEST=1 但缺 CI_E2E_TEST_RUNNER（無假 runner 路徑）——不得回退成真正的 run-e2e.mjs（會啟動 App／browser）';
  process.stderr.write(`[run-batch] 拒絕（S4 offline 鉤子邊界，spawn 之前）：${reason}\n`);
  process.exit(2);
}
const OFFLINE_SELFTEST = offlineSelftestRequested;
const TEST_RUNNER = OFFLINE_SELFTEST ? rawTestRunnerPath : null;

const WRAPPER_PATH = path.join(__dirname, 'ci-e2e-wrapper.mjs');
const PACKAGE_SCRIPT = path.join(__dirname, 'package-e2e-evidence.sh');

// review round 5（#483 S3）：spawnSync(timeout) 不是硬性返回上限——reviewer
// 用 timeout-probe.mjs 實測：子行程接住 SIGTERM、800ms 後才自行退出，
// spawnSync(timeout:250) 直到子行程真的退出才返回（897ms，遠超過
// 250ms），且同時是 `error=ETIMEDOUT`、`status=0`、`signal=null`——
// 這種合法的 Node 回傳形狀先前會被 `passed = wrapperSpawnRc===0 &&
// packageRc===0 && verdictOk` 誤判成功（error 只印出，不影響判定）。改用
// `bounded-spawn.mjs` 的 `boundedSpawn()`（非同步 spawn() + 自己實作的
// SIGTERM→SIGKILL 兩段式逾時升級，只對自己持有的 ChildProcess handle 送
// 訊號），worst-case 總時長可核算＝softTimeoutMs+killGraceMs+reapGraceMs，
// 這是「真正有界」的定義（S3 點 2），不是寄望 spawnSync 自己的 timeout 語
// 意。獨立成模組是為了讓 bounded-spawn.selftest.mjs 能用極小 ms 值直接單
// 元測試 SIGTERM→SIGKILL 升級路徑（見該檔）。

// entry 總預算（含異常路徑）：wrapper 階段 worst-case ＋ package 階段
// worst-case 必須 ≤ ENTRY_HARD_BUDGET_SECONDS（30min，見
// ci-e2e-entries.mjs）——這是結構性檢查，不是註解宣稱；算式錯就直接在
// import 當下炸開，不讓下游默默超支（呼應 decision.md「現行外層28.5分鐘
// 再加package2分鐘已為30.5，不能以只計正常路徑排除」的教訓）。
const WRAPPER_KILL_GRACE_MS = 15_000;
const WRAPPER_REAP_GRACE_MS = 5_000;
const PACKAGE_SOFT_TIMEOUT_MS = 90_000;
const PACKAGE_KILL_GRACE_MS = 20_000;
const PACKAGE_REAP_GRACE_MS = 10_000;

// review round 3（R5）：不覆寫 attempt——WORK_ROOT 已經帶 ATTEMPT_ID，正常
// 情況下不會撞到既有內容；仍多一層防禦，若這個 attempt 目錄已經存在且非
// 空，直接拒絕，不靜默沿用/覆寫。
if (existsSync(WORK_ROOT) && readdirSync(WORK_ROOT).length > 0) {
  process.stderr.write(`[run-batch] 拒絕：WORK_ROOT=${WORK_ROOT} 已存在且非空，不覆寫既有 attempt 的證據。請用新的 CI_E2E_ATTEMPT_ID。\n`);
  process.exit(2);
}
mkdirSync(WORK_ROOT, { recursive: true });

// review round 4（#480 R2 剩餘缺口）：job 開頭的可信 CI invocation
// envelope 是這個 batch 全部 entry 共用的前提——workflow 用獨立 step
// （generate-ci-envelope.mjs）從 GitHub context／實際探測指令產生
// envelope.json，寫在 WORK_ROOT_BASE（不含 ATTEMPT_ID 這層，因為這是 job
// 級、比任何一次 attempt 都更早存在的前提，run-batch.mjs 自己也不知道
// ATTEMPT_ID 會是什麼直到執行到這裡才生成）。這裡讀進來驗證完整性；缺檔／
// 型別錯誤就整個 batch 判定失敗，不嘗試跑任何 entry——envelope 都不可信，
// 逐 entry 再驗也沒意義。
const ENVELOPE_PATH = process.env.CI_E2E_ENVELOPE_PATH ?? path.join(WORK_ROOT_BASE, 'envelope.json');
let jobEnvelope = null;
let envelopeError = null;
if (!existsSync(ENVELOPE_PATH)) {
  envelopeError = `CI invocation envelope 不存在（${ENVELOPE_PATH}）——job 開頭的 generate-ci-envelope.mjs step 應該已經產生這個檔案`;
} else {
  try {
    const parsed = JSON.parse(readFileSync(ENVELOPE_PATH, 'utf8'));
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      envelopeError = `CI invocation envelope（${ENVELOPE_PATH}）不是物件`;
    } else {
      // review round 5（#483 直接裁定）：「job preflight目前只查非空字串，
      // SHA/checkout/Node不符到entry跑完才驗...能在啟動前判定的錯誤，移
      // 到共用驗證並先拒絕，不花runtime後才失敗」——先前這裡只查
      // ENVELOPE_BASE_FIELDS 是不是非空字串，SHA 格式／checkoutHeadSha
      // 與 testMergeSha 內部一致性／現場 `git rev-parse HEAD` 交叉核對／
      // Node 版本交叉核對，只在 evaluate-e2e-evidence.mjs 的
      // validateEnvelope() 裡做，那要等每個 entry 真的跑完才會驗到。這些
      // 檢查其實完全不依賴任何一次 entry 的執行結果，job 開始、envelope
      // 剛讀進來就能判定——改用 envelope-checks.mjs 的共用函式，
      // run-batch.mjs（這裡）與 evaluate-e2e-evidence.mjs（entry 專屬的
      // 最終 envelope）呼叫同一份實作，不重複維護。
      // review round 6（#489 F3）：actualNodeVersion 只在離線 selftest 需要
      // 在非核定版本的本機 Node 下模擬「envelope 對得上正常 CI 的 Node」時
      // 才會設定這個變數——正式 workflow 絕不設定，一律用預設的
      // process.version（見 envelope-checks.mjs checkEnvelopeNodeVersion()
      // 頭註，明確隔離的測試邊界，不混入正常 CI runtime）。
      const baseViolations = checkEnvelopeBaseAll(parsed, {
        repoRootForGitCheck: REPO_ROOT,
        actualNodeVersion: process.env.CI_E2E_SELFTEST_ACTUAL_NODE_VERSION || undefined,
      });
      if (baseViolations.length > 0) {
        envelopeError = `CI invocation envelope（${ENVELOPE_PATH}）preflight 未通過：${baseViolations.join('；')}`;
      } else {
        jobEnvelope = parsed;
      }
    }
  } catch (e) {
    envelopeError = `CI invocation envelope（${ENVELOPE_PATH}）無法解析（${e.message}）`;
  }
}

if (envelopeError) {
  process.stdout.write(
    `::error::[run-batch] ${envelopeError}——整個 batch 判定失敗，不嘗試跑任何 entry（envelope 都不可信，逐 entry 驗也沒意義）\n`,
  );
  const failResults = entries.map((entry, i) => (
    i === 0
      ? { id: entry.id, status: 'failed', reason: 'envelope-invalid', detail: envelopeError }
      : { id: entry.id, status: 'not-run', reason: `batch stopped earlier at "${entries[0].id}"（envelope-invalid）` }
  ));
  const envelopeFailSummary = {
    batch: batchName,
    attemptId: ATTEMPT_ID,
    entries: failResults,
    stoppedEarly: true,
    stoppedAt: entries[0]?.id ?? null,
    overall: 'failed',
    envelopeError,
  };
  writeFileSync(path.join(WORK_ROOT, 'batch-summary.json'), `${JSON.stringify(envelopeFailSummary, null, 2)}\n`);
  process.stdout.write(`\n${JSON.stringify(envelopeFailSummary, null, 2)}\n`);
  process.exit(1);
}

function listDirNames(root) {
  if (!existsSync(root)) return new Set();
  return new Set(
    readdirSync(root).filter((name) => {
      try {
        return statSync(path.join(root, name)).isDirectory();
      } catch {
        return false;
      }
    }),
  );
}

function buildCommand(entry) {
  if (TEST_RUNNER) {
    return { cmd: 'node', args: [TEST_RUNNER, entry.id] };
  }
  const { cmd, args } = realCommandForEntry(entry);
  return { cmd, args };
}

// review round 3（R5）：spawn 前的 env 邊界。先前直接 `...process.env` 整包
// 帶入 child——任何呼叫端（或殘留的前一個 entry）不小心留下的
// E2E_GATE／E2E_SCENARIO 等變數都會被無條件沿用。這裡明確核對：
// CONTROLLED_ENV_KEYS 清單裡的變數，ambient env 只允許「未設定」或「值跟
// 這個 entry 自己要設的值一致」，其他情況（設了這個 entry 不該有的值、或
// 值衝突）一律視為污染，拒絕 spawn（回傳 null，呼叫端把這個 entry 判定為
// 失敗，不嘗試修正）。
function sanitizeEnvForEntry(entry) {
  const violations = [];
  const sanitized = { ...process.env };
  for (const key of CONTROLLED_ENV_KEYS) {
    const ambient = process.env[key];
    const expected = entry.env[key];
    if (ambient !== undefined && ambient !== expected) {
      violations.push(`${key}=${JSON.stringify(ambient)}（ambient env 污染；本 entry 預期 ${JSON.stringify(expected ?? '(未設定)')}）`);
    }
    // 不論 ambient 是否乾淨，一律先清掉再由 entry.env 決定——確保這個 key
    // 只可能來自 entry.env 這個單一來源，不是「ambient 剛好沒污染就沿
    // 用」的僥倖。
    delete sanitized[key];
  }
  if (violations.length > 0) {
    return { ok: false, violations };
  }
  return { ok: true, env: { ...sanitized, ...entry.env } };
}

const results = [];
let stoppedEarly = false;
let stoppedAt = null;

for (const entry of entries) {
  if (stoppedEarly) {
    results.push({ id: entry.id, status: 'not-run', reason: `batch stopped earlier at "${stoppedAt}"` });
    continue;
  }

  process.stdout.write(`\n=== [${batchName}] entry=${entry.id} (attempt=${ATTEMPT_ID}) ===\n`);
  const caseDir = path.join(WORK_ROOT, entry.id);
  mkdirSync(caseDir, { recursive: true });

  const envCheck = sanitizeEnvForEntry(entry);
  if (!envCheck.ok) {
    process.stdout.write(`::error::[${entry.id}] env 污染，拒絕 spawn：${envCheck.violations.join('；')}\n`);
    writeFileSync(path.join(caseDir, 'env-pollution.txt'), `${envCheck.violations.join('\n')}\n`);
    results.push({ id: entry.id, status: 'failed', reason: 'env-pollution', violations: envCheck.violations });
    stoppedEarly = true;
    stoppedAt = entry.id;
    continue;
  }

  const statusFile = path.join(caseDir, 'e2e-wrapper-status.json');
  const outFile = path.join(caseDir, 'e2e.out');
  const tsOutFile = path.join(caseDir, 'e2e.timestamped.out');
  const rcFile = path.join(caseDir, 'e2e.rc');

  const before = listDirNames(ARTIFACTS_ROOT);

  const { cmd, args } = buildCommand(entry);
  const wrapperArgs = [WRAPPER_PATH, statusFile, outFile, tsOutFile, rcFile, '--', cmd, ...args];
  const deadlineSeconds = Number(process.env.E2E_WRAPPER_DEADLINE_SECONDS_OVERRIDE ?? WRAPPER_DEADLINE_SECONDS);
  const graceSeconds = Number(process.env.E2E_WRAPPER_GRACE_SECONDS_OVERRIDE ?? WRAPPER_GRACE_SECONDS);
  const env = {
    ...envCheck.env,
    E2E_KEEP_ARTIFACTS: '1',
    E2E_WRAPPER_DEADLINE_SECONDS: String(deadlineSeconds),
    E2E_WRAPPER_GRACE_SECONDS: String(graceSeconds),
    CI_E2E_ARTIFACTS_ROOT: ARTIFACTS_ROOT,
    CI_E2E_CASE_DIR: caseDir,
  };

  // review round 3（R4）：wrapper 自己有內部 deadline/grace/watchdog（合計
  // deadline+grace+watchdogBuffer，預設 25+2+0.5=27.5min），這裡的外層有界
  // 等待是安全網（防禦 wrapper 本身卡死、watchdog 都失效的極端情況），設
  // 在 watchdog 上限之上留 40 秒緩衝，不是主要的有界機制。
  //
  // review round 5（#483 S3）：外層不再用 spawnSync(timeout)（非硬性返回
  // 上限，見上方 boundedSpawn() 頭註），改用 boundedSpawn()——軟逾時後送
  // SIGTERM，再等 15s 送 SIGKILL，再等 5s 放棄等待（只對這個 child handle
  // 送訊號）。worst-case = wrapperSoftTimeoutMs + 15s + 5s。
  const wrapperSoftTimeoutMs = (deadlineSeconds + graceSeconds + 40) * 1000;
  const wrapperWorstCaseMs = wrapperSoftTimeoutMs + WRAPPER_KILL_GRACE_MS + WRAPPER_REAP_GRACE_MS
    + PACKAGE_SOFT_TIMEOUT_MS + PACKAGE_KILL_GRACE_MS + PACKAGE_REAP_GRACE_MS;
  if (wrapperWorstCaseMs > ENTRY_HARD_BUDGET_SECONDS * 1000) {
    // 結構性自我檢查（S3 點 3：「每entry30分鐘的上限必須連異常路徑一起算
    // 進去」）：算式本身若因為未來調整 deadline/grace 而超出預算，寧可在
    // 這裡直接炸開，不讓下游默默超支。
    throw new Error(
      `run-batch.mjs: entry="${entry.id}" 的最壞情況總時長 ${wrapperWorstCaseMs}ms 超過 `
        + `ENTRY_HARD_BUDGET_SECONDS=${ENTRY_HARD_BUDGET_SECONDS}s（含 wrapper／package 兩階段的 SIGTERM→SIGKILL 升級全部算入）`,
    );
  }
  const wrapperRun = await boundedSpawn('node', wrapperArgs, {
    cwd: REPO_ROOT, env, stdio: 'inherit', softTimeoutMs: wrapperSoftTimeoutMs,
    killGraceMs: WRAPPER_KILL_GRACE_MS, reapGraceMs: WRAPPER_REAP_GRACE_MS,
  });
  const wrapperSpawnRc = wrapperRun.status;
  const wrapperAbnormal = isAbnormalBoundedSpawnResult(wrapperRun);
  if (wrapperAbnormal) {
    process.stdout.write(
      `::error::[${entry.id}] wrapper boundedSpawn 異常：error=${wrapperRun.error?.message ?? 'none'} `
        + `signal=${wrapperRun.signal ?? 'none'} timedOut=${wrapperRun.timedOut} hardKilled=${wrapperRun.hardKilled} `
        + `status=${wrapperRun.status} elapsedMs=${wrapperRun.elapsedMs}（error／timeout／signal 一律計入最終失敗，不論 status 是否為 0）\n`,
    );
  }

  const after = listDirNames(ARTIFACTS_ROOT);
  const newDirNames = [...after].filter((name) => !before.has(name)).sort();
  const newRunDirs = newDirNames.map((name) => path.join(ARTIFACTS_ROOT, name));
  writeFileSync(path.join(caseDir, 'new-run-dirs.json'), `${JSON.stringify(newRunDirs, null, 2)}\n`);
  // review round 3（R5）：候選目錄安全保存——即使數量不是預期的 1，仍把清
  // 單留給 package script 逐一複製保存（見 package-e2e-evidence.sh 的
  // candidate-dirs.txt 處理），不是「判定拒絕就等於原始資料可以不管」。
  if (newRunDirs.length !== 1) {
    writeFileSync(path.join(caseDir, 'candidate-dirs.txt'), `${newRunDirs.join('\n')}\n`);
  }

  // review round 4（#480 R2 剩餘缺口）：把 job-level envelope 蓋上這個
  // entry 專屬的 identity 戳記（entryId／configPath／selectedEnv／
  // runId），寫進這個 entry 自己的 caseDir，供 package script 打包、
  // evaluator 交叉核對（見 evaluate-e2e-evidence.mjs 的 validateEnvelope()）。
  // 只有 newRunDirs 剛好 1 個（claimed-success 的唯一合法情境）才蓋
  // 章——0 或 >1 個候選目錄的案例，evaluator 本來就不會走進
  // evaluateClaimedSuccessRun() 去讀這個檔案，蓋一個對應不到單一 run 的戳
  // 記沒有意義。
  if (newRunDirs.length === 1) {
    const stampedEnvelope = {
      ...jobEnvelope,
      entryId: entry.id,
      configPath: entry.configPath,
      selectedEnv: entry.env,
      runId: newDirNames[0],
    };
    writeFileSync(path.join(caseDir, 'envelope.json'), `${JSON.stringify(stampedEnvelope, null, 2)}\n`);
  }

  const packageArgs = [PACKAGE_SCRIPT, caseDir];
  if (newRunDirs.length === 1) packageArgs.push(newRunDirs[0]);
  else packageArgs.push('');
  packageArgs.push(entry.id);
  // review round 3（R4）：package script 內部只呼叫一次 evaluator（R5 單一
  // verdict），本身的有界等待涵蓋「wrapper 完成後、封裝＋內容驗證＋
  // readback」這整段。
  //
  // review round 5（#483 S3）：同 wrapper 階段，改用 boundedSpawn()，不再
  // 用 spawnSync(timeout)。90s 軟逾時＋20s SIGKILL 等待＋10s 放棄等待＝
  // 120s worst-case，跟 wrapper 階段的 worst-case 合計就是上面已經核算過
  // 的 wrapperWorstCaseMs（≤ ENTRY_HARD_BUDGET_SECONDS）。
  //
  // review round 4（#480 R2 剩餘缺口）：明確把 REPO_ROOT 傳給 package
  // script（進而傳給它內部呼叫的 evaluate-e2e-evidence.mjs），讓
  // envelope.json 的 checkoutHeadSha 交叉核對（`git -C <repo-root>
  // rev-parse HEAD`）不必依賴「package script cd 進 caseDir 之後，cwd 剛好
  // 還是巢狀在 repo 樹裡」這種隱性巧合——caseDir 在正式 CI 下確實巢狀在
  // REPO_ROOT 底下，但離線 selftest 的 caseDir 通常建在系統暫存目錄，不巢
  // 狀在任何 repo 裡，顯式傳遞才可靠。
  const packageRun = await boundedSpawn('bash', packageArgs, {
    cwd: REPO_ROOT,
    stdio: 'inherit',
    softTimeoutMs: PACKAGE_SOFT_TIMEOUT_MS,
    killGraceMs: PACKAGE_KILL_GRACE_MS,
    reapGraceMs: PACKAGE_REAP_GRACE_MS,
    env: { ...process.env, CI_E2E_REPO_ROOT: REPO_ROOT },
  });
  const packageRc = packageRun.status;
  const packageAbnormal = isAbnormalBoundedSpawnResult(packageRun);
  if (packageAbnormal) {
    process.stdout.write(
      `::error::[${entry.id}] package script boundedSpawn 異常：error=${packageRun.error?.message ?? 'none'} `
        + `signal=${packageRun.signal ?? 'none'} timedOut=${packageRun.timedOut} hardKilled=${packageRun.hardKilled} `
        + `status=${packageRun.status} elapsedMs=${packageRun.elapsedMs}（error／timeout／signal 一律計入最終失敗，不論 status 是否為 0）\n`,
    );
  }

  // review round 3（R5）：讀 package script 寫出的單一 verdict.json，不再
  // 重新呼叫 evaluate-e2e-evidence.mjs——避免兩次獨立呼叫可能得到不同結論。
  const verdictFile = path.join(caseDir, 'verdict.json');
  let verdict = null;
  let verdictOk = false;
  if (existsSync(verdictFile)) {
    try {
      verdict = JSON.parse(readFileSync(verdictFile, 'utf8'));
      verdictOk = verdict && typeof verdict === 'object' && verdict.overall === 'passed';
    } catch (e) {
      process.stdout.write(`::error::[${entry.id}] verdict.json 無法解析（${e.message}），視為不可信\n`);
    }
  } else {
    process.stdout.write(`::error::[${entry.id}] verdict.json 不存在（package script 可能提早中止），視為缺漏\n`);
  }

  // review round 5（#483 S3）：先前 `passed` 只看 `wrapperSpawnRc===0 &&
  // packageRc===0 && verdictOk`——reviewer 已證實 spawnSync 的
  // timeout／error／signal 可能跟 status=0 同時出現，只印出來、不影響這個
  // 布林條件，因此誤判成功。現在 wrapperAbnormal／packageAbnormal
  // （error／signal／timedOut／hardKilled 任一為真）一律計入失敗，不論
  // status 最後是不是 0。
  //
  // review round 8（decision497 §B-2）：verdict.json 現在多了
  // executionOutcome／packageStatus 兩個欄位（package-e2e-evidence.sh 分開
  // 算「執行結果」與「打包/readback結果」）——verdictOk 沿用 verdict.overall
  // 即可（overall 本身現在已經是兩者的合取，語意已修正，不需要在這裡重複
  // 拆開判斷），這裡只是把 verdict.executionOutcome 也印進診斷訊息，讓
  // wrapperSpawnRc（run-batch 自己觀察到的 wrapper 行程 rc）跟
  // verdict.executionOutcome（package script 從 e2e-wrapper-status.json 獨
  // 立算出來的執行結果）方便並排核對，不是新增判定依據。
  const passed = !wrapperAbnormal && !packageAbnormal && wrapperSpawnRc === 0 && packageRc === 0 && verdictOk;
  const result = {
    id: entry.id,
    status: passed ? 'passed' : 'failed',
    wrapperRc: wrapperSpawnRc,
    wrapperAbnormal,
    wrapperError: wrapperRun.error?.message ?? null,
    wrapperSignal: wrapperRun.signal,
    wrapperTimedOut: wrapperRun.timedOut,
    wrapperHardKilled: wrapperRun.hardKilled,
    packageRc,
    packageAbnormal,
    packageError: packageRun.error?.message ?? null,
    packageSignal: packageRun.signal,
    packageTimedOut: packageRun.timedOut,
    packageHardKilled: packageRun.hardKilled,
    verdict,
    newRunDirs: newDirNames,
  };
  results.push(result);
  process.stdout.write(
    `=== [${batchName}] entry=${entry.id} result=${result.status} wrapperRc=${wrapperSpawnRc} `
      + `wrapperAbnormal=${wrapperAbnormal} packageRc=${packageRc} packageAbnormal=${packageAbnormal} `
      + `verdict.overall=${verdict?.overall ?? '(none)'} `
      + `verdict.executionOutcome=${verdict?.executionOutcome ?? '(none)'} ===\n`,
  );

  if (!passed) {
    stoppedEarly = true;
    stoppedAt = entry.id;
  }
}

const summary = {
  batch: batchName,
  attemptId: ATTEMPT_ID,
  entries: results,
  stoppedEarly,
  stoppedAt,
  overall: results.every((r) => r.status === 'passed') ? 'passed' : 'failed',
};
writeFileSync(path.join(WORK_ROOT, 'batch-summary.json'), `${JSON.stringify(summary, null, 2)}\n`);
process.stdout.write(`\n${JSON.stringify(summary, null, 2)}\n`);

process.exit(summary.overall === 'passed' ? 0 : 1);
