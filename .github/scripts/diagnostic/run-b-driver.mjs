#!/usr/bin/env node
// run-b-driver.mjs — b3a-ci2-platform-negative Run B 診斷 driver（新增診斷
// 檔，非 91226366 既有內容）。依 review525 decision.md「Run B：合併元件與
// artifact 邊界」第 1–4 點執行。
//
// 這支腳本從**原路徑**呼叫 91226366 既有的原始模組（不複製這些模組進 repo，
// 只在候選 workflow 裡新增這一支 driver 與少數自帶的良性 fixture）：
//   - frontend/e2e/support/stopProcedure.ts（C 兩個受控分支情境）
//   - .github/scripts/ci-e2e-wrapper.mjs（wrapper obedient/stubborn/watchdog
//     三情境）
//   - .github/scripts/package-e2e-evidence.sh ＋ evaluate-e2e-evidence.mjs
//    （NO-RUN 情境）
//
// 執行前先核對這些原始模組相對於 91226366 的 sha256（golden-source-hashes.json），
// 任一筆不符就在任何 spawn 之前非零結束，不嘗試繼續往下跑。
//
// 用法：node run-b-driver.mjs <repo-root> <out-dir>
//   <repo-root>  這次候選 workflow checkout 出來的 repo 根目錄（或離線驗證用
//                的 git archive 複本樹＋候選檔疊加後的根目錄）
//   <out-dir>    這次執行所有子案輸出與最終 receipt.json 的根目錄（呼叫端
//                負責確保這是唯一、乾淨的新目錄）
//
// exit 0：全部子案（含 golden source hash 核對）都通過；exit 非 0：任一項
// 失敗（缺 log/manifest/receipt 也視為失敗，見 writeReceiptOrDie()）。
import { createHash } from 'node:crypto';
import { existsSync, mkdirSync, readFileSync, writeFileSync, readdirSync, lstatSync, realpathSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const DIAGNOSTIC_ROOT = __dirname; // .../.github/scripts/diagnostic

const [, , repoRootArg, outDirArg] = process.argv;
if (!repoRootArg || !outDirArg) {
  process.stderr.write('usage: node run-b-driver.mjs <repo-root> <out-dir>\n');
  process.exit(2);
}
const REPO_ROOT = path.resolve(repoRootArg);
const OUT_DIR = path.resolve(outDirArg);
if (realpathSync(REPO_ROOT) !== realpathSync(path.resolve(DIAGNOSTIC_ROOT, '../../..'))) {
  throw new Error('repo-root must match the tree containing this diagnostic driver and its C imports');
}
if (existsSync(OUT_DIR) && (!lstatSync(OUT_DIR).isDirectory() || lstatSync(OUT_DIR).isSymbolicLink() || readdirSync(OUT_DIR).length !== 0)) {
  throw new Error('diagnostic output must be a fresh, empty, non-symlink directory');
}
mkdirSync(OUT_DIR, { recursive: true });

const startedAtIso = new Date().toISOString();
const subcases = [];
let hadFatal = false;

function sha256OfFile(p) {
  return createHash('sha256').update(readFileSync(p)).digest('hex');
}

// ---------------------------------------------------------------------------
// 步驟 1：核對 91226366 golden source hash（在任何子案 spawn 之前）。
// ---------------------------------------------------------------------------
function checkGoldenSourceHashes() {
  const tablePath = path.join(DIAGNOSTIC_ROOT, 'golden-source-hashes.json');
  const table = JSON.parse(readFileSync(tablePath, 'utf8'));
  if (table.goldenCommit !== '91226366d70b8e2f7e949d010b597b269db43183'
      || !Array.isArray(table.files) || table.files.length !== 14
      || new Set(table.files.map((e) => e.path)).size !== 14
      || !table.files.every((e) => typeof e.path === 'string' && !path.isAbsolute(e.path)
        && !e.path.split('/').includes('..') && /^[a-f0-9]{64}$/.test(e.sha256))) {
    throw new Error('invalid golden source table');
  }
  const results = [];
  let allOk = true;
  for (const entry of table.files) {
    const abs = path.join(REPO_ROOT, entry.path);
    let actual = null;
    let ok = false;
    let error = null;
    if (!existsSync(abs)) {
      error = 'file-missing';
    } else {
      try {
        actual = sha256OfFile(abs);
        ok = actual === entry.sha256;
      } catch (e) {
        error = e.message;
      }
    }
    if (!ok) allOk = false;
    results.push({ path: entry.path, expected: entry.sha256, actual, ok, error });
  }
  return { goldenCommit: table.goldenCommit, allOk, results };
}

let goldenSourceCheck;
try {
  goldenSourceCheck = checkGoldenSourceHashes();
} catch (e) {
  goldenSourceCheck = { allOk: false, results: [], error: String(e) };
}
if (!goldenSourceCheck.allOk) {
  process.stderr.write(
    `[run-b-driver] golden source hash 核對失敗，拒絕執行任何子案（見 receipt.json）：\n${
      JSON.stringify(goldenSourceCheck.results.filter((r) => !r.ok), null, 2)
    }\n`,
  );
  hadFatal = true;
}

// ---------------------------------------------------------------------------
// 共用小工具
// ---------------------------------------------------------------------------
function psCheckOwnPid(pid) {
  if (!Number.isInteger(pid) || pid <= 0) {
    return { pid, state: 'observation-error', error: 'invalid fixture PID' };
  }
  const r = spawnSync('ps', ['-p', String(pid), '-o', 'pid=,ppid=,pgid=,stat=,lstart=,command='], {
    encoding: 'utf8', timeout: 2_000, killSignal: 'SIGKILL',
    env: { ...process.env, LC_ALL: 'C' },
  });
  const stdout = r.stdout ?? '';
  const stderr = r.stderr ?? '';
  const absent = !r.error && r.signal === null && r.status === 1
    && stdout.trim() === '' && stderr.trim() === '';
  const present = !r.error && r.signal === null && r.status === 0
    && stdout.trim() !== '' && stderr.trim() === '';
  return { pid, exitCode: r.status, signal: r.signal, error: r.error ? String(r.error) : null,
    stdout, stderr, state: absent ? 'absent' : present ? 'present' : 'observation-error' };
}

function record(id, expected, actual, passed, detail) {
  subcases.push({ id, expected, actual, passed, detail: detail ?? null });
  if (!passed) hadFatal = true;
}

// ---------------------------------------------------------------------------
// 步驟 2：C 類受控分支證據（兩個情境）
// ---------------------------------------------------------------------------
async function runCProbe(probeName, caseId) {
  const caseOutDir = path.join(OUT_DIR, caseId);
  mkdirSync(caseOutDir, { recursive: true });
  const loaderPath = path.join(REPO_ROOT, 'frontend/e2e/support/selftestJsToTsLoader.mjs');
  const probePath = path.join(DIAGNOSTIC_ROOT, 'fixtures', probeName);
  const r = spawnSync(
    process.execPath,
    [`--experimental-loader=${loaderPath}`, probePath, caseOutDir],
    { encoding: 'utf8', cwd: caseOutDir, timeout: 25_000, killSignal: 'SIGKILL' },
  );
  writeFileSync(path.join(caseOutDir, 'stdout.log'), r.stdout ?? '');
  writeFileSync(path.join(caseOutDir, 'stderr.log'), r.stderr ?? '');
  writeFileSync(path.join(caseOutDir, 'rc'), `${r.status}\n`);
  const resultPath = path.join(caseOutDir, 'result.json');
  let resultJson = null;
  if (existsSync(resultPath)) {
    try {
      resultJson = JSON.parse(readFileSync(resultPath, 'utf8'));
    } catch {
      resultJson = null;
    }
  }
  const passed = r.status === 0 && resultJson?.assertions?.allPassed === true;
  return { caseOutDir, rc: r.status, resultJson, passed };
}

try {
if (!hadFatal) {
  const c1 = await runCProbe('c-persistent-to-deadline-probe.mjs', 'c-persistent-to-deadline');
  record(
    'C-persistent-to-deadline',
    { rc: 0, 'result.assertions.allPassed': true, unconfirmedPids: [990001], escalatedToKill: false },
    { rc: c1.rc, result: c1.resultJson },
    c1.passed,
    { outDir: c1.caseOutDir },
  );

}

if (!hadFatal) {
  const c2 = await runCProbe('c-unconfirmed-then-gone-probe.mjs', 'c-unconfirmed-then-gone');
  record(
    'C-unconfirmed-then-gone',
    { rc: 0, 'result.assertions.allPassed': true, clean: true, unconfirmedPidsFinal: [] },
    { rc: c2.rc, result: c2.resultJson },
    c2.passed,
    { outDir: c2.caseOutDir },
  );
}

// ---------------------------------------------------------------------------
// 步驟 3：原版 wrapper 的 obedient／stubborn／watchdog 三種情境
// ---------------------------------------------------------------------------
function runWrapperScenario({ caseId, fixtureFile, deadlineSeconds, graceSeconds, bufferSeconds, extraEnv }) {
  const caseOutDir = path.join(OUT_DIR, caseId);
  mkdirSync(caseOutDir, { recursive: true });
  const wrapperPath = path.join(REPO_ROOT, '.github/scripts/ci-e2e-wrapper.mjs');
  const fixturePath = path.join(DIAGNOSTIC_ROOT, 'fixtures', fixtureFile);
  const statusFile = path.join(caseOutDir, 'e2e-wrapper-status.json');
  const outFile = path.join(caseOutDir, 'e2e.out');
  const tsOutFile = path.join(caseOutDir, 'e2e.timestamped.out');
  const rcFile = path.join(caseOutDir, 'e2e.rc');
  const env = {
    ...process.env,
    E2E_WRAPPER_DEADLINE_SECONDS: String(deadlineSeconds),
    E2E_WRAPPER_GRACE_SECONDS: String(graceSeconds),
    E2E_WRAPPER_WATCHDOG_BUFFER_SECONDS: String(bufferSeconds),
    GRANDCHILD_EVIDENCE_DIR: caseOutDir,
    ...extraEnv,
  };
  const args = [wrapperPath, statusFile, outFile, tsOutFile, rcFile, '--', process.execPath, fixturePath];
  const startedAt = Date.now();
  const r = spawnSync(process.execPath, args, { encoding: 'utf8', env, cwd: caseOutDir, timeout: 12_000, killSignal: 'SIGKILL' });
  const elapsedMs = Date.now() - startedAt;
  writeFileSync(path.join(caseOutDir, 'wrapper-invoke-stdout.log'), r.stdout ?? '');
  writeFileSync(path.join(caseOutDir, 'wrapper-invoke-stderr.log'), r.stderr ?? '');
  writeFileSync(path.join(caseOutDir, 'wrapper-invoke-rc.txt'), `${r.status}\n`);
  let statusJson = null;
  if (existsSync(statusFile)) {
    try {
      statusJson = JSON.parse(readFileSync(statusFile, 'utf8'));
    } catch {
      statusJson = null;
    }
  }
  return { caseOutDir, invokeRc: r.status, statusJson, elapsedMs };
}

if (!hadFatal) {
  // obedient：2/1/1s，fixture 收 SIGTERM 立即退出（30s 自我上限，遠大於
  // wrapper 觀測窗，確保時序上不會提早自行結束搶在 wrapper 之前）。
  const obedient = runWrapperScenario({
    caseId: 'wrapper-obedient',
    fixtureFile: 'fixture-obedient.mjs',
    deadlineSeconds: 2,
    graceSeconds: 1,
    bufferSeconds: 1,
  });
  let obedientPassed = obedient.invokeRc === 124
    && obedient.statusJson?.status === 'timeout'
    && obedient.statusJson?.signalSent === 'SIGTERM'
    && obedient.statusJson?.childConfirmedGone === 'true'
    && obedient.statusJson?.childRc === 0;
  const obedientChildCheck = obedient.statusJson?.childPid
    ? psCheckOwnPid(obedient.statusJson.childPid)
    : null;
  if (obedientChildCheck) {
    writeFileSync(
      path.join(obedient.caseOutDir, 'proc-check-receipt.json'),
      `${JSON.stringify({ note: 'wrapper 原本的 status 不改；這是後續獨立觀測，另存 receipt，不覆寫 e2e-wrapper-status.json', check: obedientChildCheck }, null, 2)}\n`,
    );
    // 讀 receipt 而不是覆寫 wrapper 自己的判定：wrapper 已經透過
    // childConfirmedGone='true' 宣稱子行程已退出；這裡額外核對 ps 現在也
    // 查不到這個 pid（進一步佐證，不是唯一依據）。
    obedientPassed = obedientPassed && obedientChildCheck.state === 'absent';
  }
  record(
    'wrapper-obedient',
    { invokeRc: 124, status: 'timeout', signalSent: 'SIGTERM', childConfirmedGone: 'true', childRc: 0, psAfterExit: 'not-found' },
    { invokeRc: obedient.invokeRc, status: obedient.statusJson, psCheck: obedientChildCheck },
    obedientPassed,
    { outDir: obedient.caseOutDir },
  );

}

if (!hadFatal) {
  // stubborn：2/1/1s，fixture 忽略 SIGTERM，自己 6s 自我上限（wrapper 在
  // ~3s 時就已經判定 timeout-no-clean-exit，fixture 3s 後才自行退出，時間
  // 差 3s，避免競速）。
  const stubborn = runWrapperScenario({
    caseId: 'wrapper-stubborn',
    fixtureFile: 'fixture-stubborn.mjs',
    deadlineSeconds: 2,
    graceSeconds: 1,
    bufferSeconds: 1,
  });
  let stubbornPassed = stubborn.invokeRc === 124
    && stubborn.statusJson?.status === 'timeout-no-clean-exit'
    && stubborn.statusJson?.signalSent === 'SIGTERM'
    && stubborn.statusJson?.childConfirmedGone === 'false';
  let stubbornProcCheck = null;
  if (stubbornPassed && stubborn.statusJson?.childPid) {
    // fixture 有 6s 自我上限；wrapper 在 ~3s 就已經結束觀測並回傳，這裡再等
    // 到 fixture 自己的自我上限之後才做唯讀 ps -p 核對，只查這個記下的 pid。
    const remainingMs = 6_000 - stubborn.elapsedMs + 1_500;
    if (remainingMs > 0) {
      await new Promise((resolve) => setTimeout(resolve, remainingMs));
    }
    stubbornProcCheck = psCheckOwnPid(stubborn.statusJson.childPid);
    writeFileSync(
      path.join(stubborn.caseOutDir, 'proc-check-receipt.json'),
      `${JSON.stringify({ note: '獨立觀測 receipt，不覆寫 wrapper 原本的 status（childConfirmedGone 維持 wrapper 自己回報的 false）', check: stubbornProcCheck }, null, 2)}\n`,
    );
    stubbornPassed = stubbornPassed && stubbornProcCheck.state === 'absent';
  }
  record(
    'wrapper-stubborn',
    { invokeRc: 124, status: 'timeout-no-clean-exit', signalSent: 'SIGTERM', childConfirmedGone: 'false', psAfterSelfDeadline: 'not-found' },
    { invokeRc: stubborn.invokeRc, status: stubborn.statusJson, psCheck: stubbornProcCheck },
    stubbornPassed,
    { outDir: stubborn.caseOutDir },
  );

}

if (!hadFatal) {
  // watchdog：1/1/1s，wrapper 直接子行程立即 spawn 孫行程後退出，孫行程繼承
  // stdio pipe，'close' 事件卡住，watchdog 到期強制結束。孫行程有自己的 4s
  // 自我上限。
  const watchdog = runWrapperScenario({
    caseId: 'wrapper-watchdog',
    fixtureFile: 'fixture-grandchild-holds-pipe.mjs',
    deadlineSeconds: 1,
    graceSeconds: 1,
    bufferSeconds: 1,
  });
  let watchdogPassed = watchdog.invokeRc === 125
    && watchdog.statusJson?.status === 'watchdog-forced-exit'
    && watchdog.statusJson?.signalSent === null;
  let watchdogProcCheck = null;
  if (watchdogPassed) {
    // 孫行程有自己的 4s 自我上限；wrapper watchdog 大約在 3s（1+1+1）到期，
    // 補等到 4s 之後再唯讀核對，只查這次自己 spawn 出來、記在
    // grandchild-evidence 檔名裡的 pid。
    const remainingMs = 4_000 - watchdog.elapsedMs + 1_500;
    if (remainingMs > 0) {
      await new Promise((resolve) => setTimeout(resolve, remainingMs));
    }
    const fs2 = await import('node:fs');
    const evidenceFiles = fs2.readdirSync(watchdog.caseOutDir).filter((n) => n.startsWith('grandchild-exit-'));
    let grandchildEvidence = null;
    let grandchildPid = null;
    if (evidenceFiles.length === 1) {
      grandchildEvidence = JSON.parse(fs2.readFileSync(path.join(watchdog.caseOutDir, evidenceFiles[0]), 'utf8'));
      grandchildPid = grandchildEvidence.pid;
    }
    const childProcCheck = watchdog.statusJson?.childPid ? psCheckOwnPid(watchdog.statusJson.childPid) : null;
    const grandchildProcCheck = grandchildPid ? psCheckOwnPid(grandchildPid) : null;
    watchdogProcCheck = { childProcCheck, grandchildEvidence, grandchildProcCheck };
    writeFileSync(
      path.join(watchdog.caseOutDir, 'proc-check-receipt.json'),
      `${JSON.stringify({ note: '獨立觀測 receipt，不覆寫 wrapper 原本 status（childConfirmedGone 對直接子行程而言可能是 true，但孫行程另外核對）', check: watchdogProcCheck }, null, 2)}\n`,
    );
    watchdogPassed = watchdogPassed
      && evidenceFiles.length === 1
      && childProcCheck?.state === 'absent'
      && grandchildProcCheck?.state === 'absent';
  }
  record(
    'wrapper-watchdog',
    { invokeRc: 125, status: 'watchdog-forced-exit', signalSent: null, grandchildEvidenceFileCount: 1, psAfterSelfDeadline: 'not-found (both direct child and grandchild)' },
    { invokeRc: watchdog.invokeRc, status: watchdog.statusJson, procCheck: watchdogProcCheck },
    watchdogPassed,
    { outDir: watchdog.caseOutDir },
  );
}

// ---------------------------------------------------------------------------
// 步驟 4：NO-RUN——原版 package-e2e-evidence.sh ＋ evaluate-e2e-evidence.mjs，
// candidate run 集合為空，wrapper 的原始檔來自真實良性 fixture 的呼叫。
// 不偽造版本或成功 envelope，不清 GITHUB_ACTIONS，不用任何 CI_E2E_* 測試豁免。
// ---------------------------------------------------------------------------
if (!hadFatal) {
  const caseId = 'no-run';
  const caseOutDir = path.join(OUT_DIR, caseId);
  mkdirSync(caseOutDir, { recursive: true });

  // 用跟 wrapper-obedient 完全相同手法的真實良性 fixture 呼叫，取得真實的
  // e2e.out／e2e.rc／e2e-wrapper-status.json（wrapper 內部 deadline 觸發，
  // status=timeout／wrapperRc=124，不是 completed/success，不需要偽造）。
  const nr = runWrapperScenario({
    caseId: `${caseId}/wrapper-invocation`,
    fixtureFile: 'fixture-obedient.mjs',
    deadlineSeconds: 2,
    graceSeconds: 1,
    bufferSeconds: 1,
  });
  // 候選 run 目錄集合為空——這是事實陳述，不是偽造：這次呼叫完全沒有經過
  // run-batch.mjs、沒有動過任何 ARTIFACTS_ROOT，因此 before/after diff 的
  // 候選集合本來就是空集合。
  writeFileSync(path.join(nr.caseOutDir, 'new-run-dirs.json'), '[]\n');

  const packageScript = path.join(REPO_ROOT, '.github/scripts/package-e2e-evidence.sh');
  const packageArgs = [packageScript, nr.caseOutDir, '', 'default'];
  const pkgEnv = { ...process.env }; // 明確不新增／清除任何 CI_E2E_*、GITHUB_ACTIONS 變數
  const pkgResult = spawnSync('bash', packageArgs, { encoding: 'utf8', env: pkgEnv, cwd: REPO_ROOT, timeout: 15_000, killSignal: 'SIGKILL' });
  writeFileSync(path.join(caseOutDir, 'package-stdout.log'), pkgResult.stdout ?? '');
  writeFileSync(path.join(caseOutDir, 'package-stderr.log'), pkgResult.stderr ?? '');
  writeFileSync(path.join(caseOutDir, 'package-rc.txt'), `${pkgResult.status}\n`);

  const verdictPath = path.join(nr.caseOutDir, 'verdict.json');
  const noRunTxtPath = path.join(nr.caseOutDir, 'e2e-evidence-package', 'NO-RUN.txt');
  let verdict = null;
  if (existsSync(verdictPath)) {
    try {
      verdict = JSON.parse(readFileSync(verdictPath, 'utf8'));
    } catch {
      verdict = null;
    }
  }
  const noRunTxtExists = existsSync(noRunTxtPath);
  const passed = nr.invokeRc === 124 && nr.statusJson?.status === 'timeout'
    && nr.statusJson?.childConfirmedGone === 'true'
    && pkgResult.error === undefined && pkgResult.signal === null && pkgResult.status === 1 // package script 本身也非零（缺漏視為失敗，符合契約）
    && verdict !== null
    && verdict.executionOutcome === 'no-run'
    && verdict.overall === 'failed' && verdict.contentValidationScope === 'no-run'
    && verdict.readbackOk === true && verdict.missingWrapperFiles === 0
    && noRunTxtExists;
  record(
    'NO-RUN',
    {
      note: '不得偽造版本或成功 envelope，不清 GITHUB_ACTIONS，不用 CI 測試豁免；wrapper 原始檔來自真實良性 fixture 呼叫',
      packageScriptRc: 'non-zero',
      'verdict.executionOutcome': 'no-run',
      'verdict.overall': '!= passed',
      noRunTxtExists: true,
    },
    {
      packageScriptRc: pkgResult.status,
      verdict,
      noRunTxtExists,
      wrapperInvocation: { invokeRc: nr.invokeRc, status: nr.statusJson },
    },
    passed,
    { workdir: nr.caseOutDir, packageDir: path.join(nr.caseOutDir, 'e2e-evidence-package') },
  );
}

} catch (e) {
  record('driver-exception', { exception: false }, { error: String(e), stack: e?.stack }, false);
}

// ---------------------------------------------------------------------------
// 寫 receipt.json（整體判定）；缺 log/manifest/receipt 本身也要讓呼叫端（工
// 作流程 job）失敗——這裡用 driver 自己的 exit code 傳遞。
// ---------------------------------------------------------------------------
const requiredFilesByCase = {
  'C-persistent-to-deadline': ['c-persistent-to-deadline/stdout.log', 'c-persistent-to-deadline/stderr.log', 'c-persistent-to-deadline/rc', 'c-persistent-to-deadline/result.json', 'c-persistent-to-deadline/harness.log'],
  'C-unconfirmed-then-gone': ['c-unconfirmed-then-gone/stdout.log', 'c-unconfirmed-then-gone/stderr.log', 'c-unconfirmed-then-gone/rc', 'c-unconfirmed-then-gone/result.json', 'c-unconfirmed-then-gone/harness.log'],
};
for (const name of ['wrapper-obedient', 'wrapper-stubborn', 'wrapper-watchdog']) {
  requiredFilesByCase[name] = ['e2e-wrapper-status.json', 'e2e.out', 'e2e.timestamped.out', 'e2e.rc',
    'wrapper-invoke-stdout.log', 'wrapper-invoke-stderr.log', 'wrapper-invoke-rc.txt', 'proc-check-receipt.json'].map((f) => `${name}/${f}`);
}
requiredFilesByCase['NO-RUN'] = ['no-run/package-stdout.log', 'no-run/package-stderr.log', 'no-run/package-rc.txt',
  ...['e2e-wrapper-status.json', 'e2e.out', 'e2e.timestamped.out', 'e2e.rc', 'new-run-dirs.json', 'verdict.json',
    'e2e-evidence-package/NO-RUN.txt', 'e2e-evidence-manifest.txt', 'e2e-evidence-manifest.sha256', 'e2e-evidence.tar.gz']
    .map((f) => `no-run/wrapper-invocation/${f}`)];
const expectedCaseIds = Object.keys(requiredFilesByCase);
const missingEvidence = [];
for (const item of subcases.filter((s) => s.passed)) {
  for (const rel of requiredFilesByCase[item.id] ?? []) {
    const p = path.join(OUT_DIR, rel);
    if (!existsSync(p) || !lstatSync(p).isFile()) missingEvidence.push(rel);
  }
}
if (missingEvidence.length) hadFatal = true;
const fileManifest = [];
function collectFiles(dir) {
  for (const name of readdirSync(dir).sort()) {
    const p = path.join(dir, name);
    const stat = lstatSync(p);
    if (stat.isDirectory()) collectFiles(p);
    else if (stat.isFile()) fileManifest.push({ path: path.relative(OUT_DIR, p), sha256: sha256OfFile(p), bytes: stat.size });
    else throw new Error(`unexpected evidence file type: ${p}`);
  }
}
collectFiles(OUT_DIR);
const manifest = { schema: 1, excludes: ['evidence-manifest.json', 'receipt.json'], files: fileManifest };
writeFileSync(path.join(OUT_DIR, 'evidence-manifest.json'), `${JSON.stringify(manifest, null, 2)}\n`, { flag: 'wx' });
const endedAtIso = new Date().toISOString();
const complete = expectedCaseIds.every((id, i) => subcases[i]?.id === id) && subcases.length === expectedCaseIds.length;
const overall = !hadFatal && complete && subcases.every((s) => s.passed) ? 'passed' : 'failed';
const receipt = {
  driver: 'run-b-driver.mjs', startedAtIso, endedAtIso, repoRoot: REPO_ROOT, outDir: OUT_DIR,
  nodeVersion: process.version,
  githubContext: { actions: process.env.GITHUB_ACTIONS ?? null, runId: process.env.GITHUB_RUN_ID ?? null,
    runAttempt: process.env.GITHUB_RUN_ATTEMPT ?? null, sha: process.env.GITHUB_SHA ?? null, job: process.env.GITHUB_JOB ?? null },
  goldenSourceCheck, subcases, expectedCaseIds,
  notRun: expectedCaseIds.filter((id) => !subcases.some((s) => s.id === id)),
  missingEvidence, evidenceManifest: 'evidence-manifest.json',
  evidenceManifestSha256: sha256OfFile(path.join(OUT_DIR, 'evidence-manifest.json')), overall,
};
writeFileSync(path.join(OUT_DIR, 'receipt.json'), `${JSON.stringify(receipt, null, 2)}\n`, { flag: 'wx' });
process.stdout.write(`${JSON.stringify(receipt, null, 2)}\n`);
process.exit(overall === 'passed' ? 0 : 1);
