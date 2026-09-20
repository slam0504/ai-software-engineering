#!/usr/bin/env node
// evaluate-e2e-evidence.mjs — package-e2e-evidence.sh 的內容驗證邏輯抽出來的
// Node 版（codex-reviewer(#82) G1／G2 反例修法）。
//
// 用法：node evaluate-e2e-evidence.mjs <workdir> <package-dir>
//   <workdir>      同 package-e2e-evidence.sh 的 workdir，要有
//                  e2e-wrapper-status.json／e2e.rc／（若有的話）
//                  frontend/e2e/.artifacts/
//   <package-dir>  e2e-evidence-package/ 的路徑；本檔案只在「確認過的早期
//                  失敗／未開始」例外成立時在這裡寫 NO-RUN.txt，不動其他
//                  檔案（複製／manifest／tar 仍由 package-e2e-evidence.sh
//                  負責）
//
// 任何缺漏／不可信都用 `::error::` 印到 stdout；exit 0 代表這次證據（包含
// 合法的「啟動前失敗／未開始」例外）通過檢查，exit 1 代表缺漏或無法信任，
// 呼叫方要讓整個 packaging step 非零結束。
//
// G1 修法（invalid_json_early）：舊版用 sed 抓 e2e-wrapper-status.json 的
// "status"／"wrapperRc"（抓不到再退回舊 "rc" 欄位相容），JSON 本身是
// "not json" 這種無法解析的內容時，sed 全部抓空字串，「status=unknown
// rc=unknown」被當成『確認過的早期失敗／未開始』，套用例外、不要求任何
// harness trace，exit 0。這裡改用 `JSON.parse`：解析失敗、必要欄位缺漏／
// 型別不對、或 wrapperRc 跟 e2e.rc 檔案內容兜不起來，一律視為「無法信任」，
// 不套用例外（那個例外只能給『確認過』的早期失敗狀態），改成缺漏、非零
// 結束（呼叫方仍會照常把現有資料封存進 package 裡）。同時移除舊版讀取
// "rc" 欄位的相容分支——新版 wrapper（ci-e2e-wrapper.mjs）一定會寫
// wrapperRc，不需要、也不該再保留一條會製造模糊判定的相容路徑。
//
// G2 修法（failed_state_with_old_success）：宣稱成功
// （status==='completed' && wrapperRc===0）時要求：
//   - frontend/e2e/.artifacts/ 底下剛好一個 run 目錄（本次唯一 run，不是
//     隨便挑一個或全部核對）
//   - run-state.json 可解析、runId 與目錄名一致、status 恰好是 'stopped'
//     （不是「不等於 failed」這種較弱的判斷）、沒有 failureStage、沒有
//     非空的 observationFailures
//   - harness.log 用「最後一筆」globalTeardown 判定行核對 cleanupClean=true
//     與 artifactViolations=0，且檔案「最後一行」要是「最終結果：PASSED」
//     ——不是在檔案任何位置 grep 到字串就算數（舊版就是被這個漏洞騙過：
//     log 前面有 cleanupClean=true／overallFailed=false，但最後一行其實是
//     FAILED）。
// 這裡只解析既有的 log 輸出格式（對照
// frontend/e2e/global-teardown.ts 的 log.log(...) 呼叫），是「這支腳本自己
// 用的、版本限定的 parser」，沒有改動 harness／production 的 schema。
//
// codex-reviewer(#84) 用兩個新反例證明上面這版 G2 修法本身還有兩個缺口
// （詳見 codex-ci84-counterexamples/）：
//   - null_run_state：run-state.json 內容是 JSON 字面量 `null`。
//     `JSON.parse('null')` 不會 throw，回傳的就是 JS 的 `null`，舊版
//     `if (runState)` 對 `null`（跟陣列／原始型別一樣）一律判 falsy 直接跳
//     過 runId／status／observationFailures 檢查，宣稱成功照樣放行。修法：
//     解析成功後另外核對「是非 null 的物件、且不是陣列」，不是就當成不可
//     信、fail。
//   - contradictory_verdict：run-state.json 合法（status='stopped'、無
//     observationFailures），但 harness.log 最後一筆 globalTeardown 判定行
//     其實是 overallFailed=true（只是 cleanupClean=true／
//     artifactViolations=0 剛好也成立），最後一行仍是「最終結果：PASSED」。
//     舊版完全沒核對 overallFailed 欄位，這裡新增：最後一筆判定行必須同時
//     滿足 overallFailed=false，缺這個欄位或值不是 false 一律 fail。
// 同時新增「固定必要檔清單」（FIXED_SUCCESS_FILES）：舊版宣稱成功時只核對
// run-state.json／harness.log 兩個檔案存在，上面兩個反例的 fixture 也证明
// 「只有這兩個檔案也能通過」本身就不夠——固定清單以
// regress/fixtures/real-success-run/ 這份真實成功採證為準。
import { readFileSync, existsSync, readdirSync, statSync, writeFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import path from 'node:path';

const [, , workdir, packageDir] = process.argv;
if (!workdir || !packageDir) {
  process.stderr.write('usage: evaluate-e2e-evidence.mjs <workdir> <package-dir>\n');
  process.exit(2);
}

const ALLOWED_STATUSES = new Set([
  'completed',
  'timeout',
  'timeout-no-clean-exit',
  'interrupted',
  'producer-error',
  'watchdog-forced-exit',
]);

let hadError = false;
function fail(msg) {
  hadError = true;
  process.stdout.write(`::error::${msg}\n`);
}

function readTextIfExists(p) {
  return existsSync(p) ? readFileSync(p, 'utf8') : null;
}

// codex-reviewer(#84)：驗證對象是「輸出包的副本」，不是只驗來源存在——核對
// 複製前後的 sha256 是否一致，抓「來源存在、但 cp 失敗／損毀，副本內容跟
// 來源不一樣」這種漏洞。只在來源本身存在時才核對（來源不存在是另一條缺漏
// 檢查的責任，這裡不重複報）。
function verifyPackageCopy(label, sourcePath, copyPath) {
  if (!existsSync(sourcePath)) return;
  if (!existsSync(copyPath)) {
    fail(`${label} 來源存在，但輸出包副本（${copyPath}）不存在，複製可能失敗`);
    return;
  }
  const srcHash = createHash('sha256').update(readFileSync(sourcePath)).digest('hex');
  const dstHash = createHash('sha256').update(readFileSync(copyPath)).digest('hex');
  if (srcHash !== dstHash) {
    fail(`${label} 來源與輸出包副本內容不一致（sha256 不同），複製可能損毀`);
  }
}

// 內容判讀仍讀 workdir 的來源（package-e2e-evidence.sh 的
// frontend/e2e/.artifacts/<run>/ 底下有明確的「run 目錄」層級，可以核對
// runId 與目錄名一致；cp 到 packageDir/artifacts/ 時 macOS 內建 cp 對
// 「來源路徑帶結尾斜線」的語意是把內容直接攤平複製進目的地、不保留 run 目
// 錄那一層，複製後的結構因此沒有 run 目錄名可核對）。codex-reviewer(#84)
// 「驗證對象是輸出包的副本」這項要求改用下面的
// `verifyPackageCopy()`：針對每個判定會用到的證據檔，另外核對
// packageDir 底下的副本存不存在、sha256 是否與來源一致，不是只驗來源存
// 在就算數。
const statusFile = path.join(workdir, 'e2e-wrapper-status.json');
const rcFile = path.join(workdir, 'e2e.rc');

const statusText = readTextIfExists(statusFile);
const rcText = readTextIfExists(rcFile);

if (statusText === null) {
  // e2e-wrapper-status.json 根本不存在：交給 package-e2e-evidence.sh 既有的
  // 檔案存在性檢查去 fail，這裡不重複判斷、也不寫 NO-RUN.txt。
  process.exit(0);
}

let wrapperStatusValid = false;
let wrapperStatus = null;
let wrapperRc = null;

let parsed;
try {
  parsed = JSON.parse(statusText);
} catch {
  parsed = undefined;
}

if (
  parsed
  && typeof parsed === 'object'
  && typeof parsed.status === 'string'
  && ALLOWED_STATUSES.has(parsed.status)
  && Number.isInteger(parsed.wrapperRc)
) {
  let rcFileValue = null;
  if (rcText !== null) {
    const trimmed = rcText.trim();
    rcFileValue = /^-?\d+$/.test(trimmed) ? Number(trimmed) : NaN;
  }
  if (rcFileValue !== null && Number.isNaN(rcFileValue)) {
    fail(`e2e.rc 內容無法解析成整數（"${rcText.trim()}"），無法核對與 wrapperRc 是否一致，視為不可信`);
  } else if (rcFileValue !== null && rcFileValue !== parsed.wrapperRc) {
    fail(`e2e-wrapper-status.json 的 wrapperRc=${parsed.wrapperRc} 與 e2e.rc 內容(${rcFileValue}) 不一致，視為不可信`);
  } else {
    wrapperStatusValid = true;
    wrapperStatus = parsed;
    wrapperRc = parsed.wrapperRc;
  }
} else {
  fail(
    'e2e-wrapper-status.json 無法解析為合法 JSON，或缺少必要欄位／型別不對'
      + '（status 需為允許值之一、wrapperRc 需為整數）——不得套用「啟動前就失敗／未開始」例外，視為缺漏',
  );
}

if (!wrapperStatusValid) {
  // G1：無法信任 wrapper 狀態，不套用「未開始」例外，也不寫 NO-RUN.txt。
  process.exit(1);
}

verifyPackageCopy(
  'e2e-wrapper-status.json',
  statusFile,
  path.join(packageDir, 'e2e-wrapper-status.json'),
);
verifyPackageCopy('e2e.rc', rcFile, path.join(packageDir, 'e2e.rc'));

let artifactDirs = [];
const artifactsRoot = path.join(workdir, 'frontend', 'e2e', '.artifacts');
// packageDir/artifacts/ 是 package-e2e-evidence.sh 用
// `cp -Rf frontend/e2e/.artifacts/*/ e2e-evidence-package/artifacts/` 產生
// 的副本；macOS cp 對這個「來源結尾斜線」語意會攤平掉 run 目錄那一層，
// 所以副本裡的證據檔是直接躺在 packageDir/artifacts/ 下（不是
// packageDir/artifacts/<run>/ 下）。
const packageArtifactsRoot = path.join(packageDir, 'artifacts');
if (existsSync(artifactsRoot)) {
  artifactDirs = readdirSync(artifactsRoot).filter((name) => {
    try {
      return statSync(path.join(artifactsRoot, name)).isDirectory();
    } catch {
      return false;
    }
  });
}

if (wrapperStatus.status !== 'completed' && wrapperRc === 0) {
  fail(`wrapper status="${wrapperStatus.status}" but wrapperRc=0 is inconsistent`);
}

const claimedSuccess = wrapperStatus.status === 'completed' && wrapperRc === 0;

if (claimedSuccess) {
  if (artifactDirs.length === 0) {
    fail(
      'frontend/e2e/.artifacts/<run>/（wrapper 宣稱 completed/wrapperRc=0，但完全沒有 harness 產出的證據目錄，'
        + '視為缺漏，不套用「未開始」例外）',
    );
  } else if (artifactDirs.length > 1) {
    fail(
      `wrapper 宣稱 completed/wrapperRc=0，但 frontend/e2e/.artifacts/ 底下有 ${artifactDirs.length} 個 run 目錄`
        + '（預期本次唯一一個），無法確認要核對哪一次執行的證據，視為缺漏',
    );
  } else {
    const runDirName = artifactDirs[0];
    const runDir = path.join(artifactsRoot, runDirName);
    const runStateFile = path.join(runDir, 'run-state.json');
    const harnessLogFile = path.join(runDir, 'harness.log');

    // codex-reviewer(#84)：固定成功必要檔清單，對照
    // regress/fixtures/real-success-run/ 這份真實成功採證。run-state.json／
    // harness.log 另有專屬的內容檢查（見下方），這裡只列其餘 8 個、只核對
    // 「存在」（空的違規 log 可接受，內容不在這裡管）。
    const OTHER_REQUIRED_FILES = [
      'run-env.json',
      'wails-dev.log',
      'invocations.log',
      'preflight-invocations.log',
      'network-samples.log',
      'browser-network-violations.log',
      'artifact-integrity-baseline.json',
      'chrome-argv.txt',
    ];
    for (const name of OTHER_REQUIRED_FILES) {
      const sourcePath = path.join(runDir, name);
      if (!existsSync(sourcePath)) {
        fail(`${runDirName}/${name} 不存在，成功宣稱缺必要證據（固定必要檔清單）`);
      } else {
        verifyPackageCopy(`${runDirName}/${name}`, sourcePath, path.join(packageArtifactsRoot, name));
      }
    }

    let runState = null;
    let runStateParsed = false;
    if (!existsSync(runStateFile)) {
      fail(`${runDirName}/run-state.json 不存在，成功宣稱缺必要證據`);
    } else {
      verifyPackageCopy(
        `${runDirName}/run-state.json`,
        runStateFile,
        path.join(packageArtifactsRoot, 'run-state.json'),
      );
      try {
        runState = JSON.parse(readFileSync(runStateFile, 'utf8'));
        runStateParsed = true;
      } catch (e) {
        fail(`${runDirName}/run-state.json 無法解析（${e.message}），成功宣稱下不得信任內容`);
      }
    }

    // codex-reviewer(#84) null_run_state：`JSON.parse('null')` 不 throw，
    // 舊版 `if (runState)` 對 null／陣列／原始型別一律 falsy 跳過檢查。這裡
    // 明確核對「解析成功且是非 null、非陣列的物件」，不是就當成不可信。
    if (runStateParsed) {
      if (runState === null || typeof runState !== 'object' || Array.isArray(runState)) {
        const actualKind = runState === null ? 'null' : Array.isArray(runState) ? 'array' : typeof runState;
        fail(`${runDirName}/run-state.json 解析結果不是物件（實際型別：${actualKind}），成功宣稱下不得信任內容`);
      } else {
        if (runState.runId !== runDirName) {
          fail(`${runDirName}/run-state.json 的 runId="${runState.runId}" 與目錄名不一致，無法核對這是本次執行的證據`);
        }
        if (runState.status !== 'stopped') {
          fail(`${runDirName}/run-state.json 的 status="${runState.status}"，成功宣稱要求恰好是 'stopped'`);
        }
        if (runState.failureStage) {
          fail(`${runDirName}/run-state.json 有 failureStage="${runState.failureStage}"，跟成功宣稱矛盾`);
        }
        if (!Array.isArray(runState.observationFailures)) {
          fail(`${runDirName}/run-state.json observationFailures must be an array`);
        } else if (runState.observationFailures.length > 0) {
          fail(`${runDirName}/run-state.json 有 ${runState.observationFailures.length} 筆 observationFailures，跟成功宣稱矛盾`);
        }
      }
    }

    if (!existsSync(harnessLogFile)) {
      fail(`${runDirName}/harness.log 不存在，成功宣稱缺必要證據`);
    } else {
      verifyPackageCopy(
        `${runDirName}/harness.log`,
        harnessLogFile,
        path.join(packageArtifactsRoot, 'harness.log'),
      );
      const lines = readFileSync(harnessLogFile, 'utf8').split('\n').filter((l) => l.length > 0);
      const verdictLines = lines.filter((l) => l.includes('globalTeardown 判定'));
      const lastVerdict = verdictLines.length > 0 ? verdictLines[verdictLines.length - 1] : null;
      if (!lastVerdict) {
        fail(`${runDirName}/harness.log 找不到 globalTeardown 判定行，無法核對是否真的成功`);
      } else {
        if (!/cleanupClean=true/.test(lastVerdict)) {
          fail(`${runDirName}/harness.log 最後一筆 globalTeardown 判定行沒有 cleanupClean=true：${lastVerdict.trim()}`);
        }
        // codex-reviewer(#84) contradictory_verdict：舊版完全沒核對
        // overallFailed，log 最後一筆判定行是 overallFailed=true 也能靠
        // cleanupClean=true／artifactViolations=0 與「最終結果：PASSED」
        // 混過去。這裡要求同一筆判定行必須明確是 overallFailed=false。
        if (!/overallFailed=false/.test(lastVerdict)) {
          fail(`${runDirName}/harness.log 最後一筆 globalTeardown 判定行沒有 overallFailed=false：${lastVerdict.trim()}`);
        }
        const violationsMatch = lastVerdict.match(/artifactViolations=(\d+)/);
        if (!violationsMatch) {
          fail(`${runDirName}/harness.log 最後一筆 globalTeardown 判定行找不到 artifactViolations 欄位，無法核對：${lastVerdict.trim()}`);
        } else if (violationsMatch[1] !== '0') {
          fail(`${runDirName}/harness.log 最後一筆 globalTeardown 判定行 artifactViolations=${violationsMatch[1]}（非 0），跟成功宣稱矛盾`);
        }
      }
      const lastLine = lines.length > 0 ? lines[lines.length - 1] : '';
      if (!/最終結果：PASSED/.test(lastLine)) {
        fail(`${runDirName}/harness.log 最後一行不是「最終結果：PASSED」（實際："${lastLine.trim()}"），不得判定成功`);
      }
    }
  }
} else if (artifactDirs.length === 0) {
  // codex-reviewer(#84)：非 completed 卻 wrapperRc=0 本身就是自相矛盾
  // （wrapper 的 status 與 wrapperRc 必須一致），不得當成「啟動前就失敗／
  // 未開始」的合法早期失敗套用例外。
  if (wrapperStatus.status !== 'completed' && wrapperRc === 0) {
    fail(
      `wrapper status="${wrapperStatus.status}" 但 wrapperRc=0（非 completed 卻 rc=0 自相矛盾），`
        + '不得套用「啟動前就失敗／未開始」例外，視為不可信',
    );
  } else {
    const noRunNote =
      `wrapper status=${wrapperStatus.status} wrapperRc=${wrapperRc}，且 frontend/e2e/.artifacts/ 不存在：`
      + '判定為啟動前就失敗／未開始，套用例外，不要求 harness trace（明示：沒有 run）。';
    try {
      writeFileSync(path.join(packageDir, 'NO-RUN.txt'), `${noRunNote}\n`);
    } catch (e) {
      fail(`寫 NO-RUN.txt 失敗：${e.message}`);
    }
  }
}
// 其餘情況（宣稱非成功，但 artifacts 目錄仍存在）維持既有行為，不在本次
// G1／G2 修法範圍內，不額外檢查其內容。

process.exit(hadError ? 1 : 0);
