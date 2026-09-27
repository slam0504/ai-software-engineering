#!/usr/bin/env node
// generate-ci-envelope.mjs — B3a-CI-1 review round 4（#480 CHANGES_REQUIRED
// R2 剩餘缺口）：workflow job 開頭產生一份「可信 CI invocation envelope」，
// 記錄不可被 PR 內容竄改來源的 identity／provenance 事實——PR head／base／
// test-merge SHA、checkout HEAD、runner os/image、實際解析到的 Node／Go／
// Chrome 版本——寫成 JSON 檔，之後隨每個 entry 的證據一起打包上傳，供
// evaluate-e2e-evidence.mjs 逐 entry 交叉核對（見該檔 validateEnvelope()）。
//
// R2 原文重點：「不能只echo metadata到job log」——先前 workflow 的
// 「record SHAs」／「record tool versions」兩個 step 只 echo 到 job log，
// 沒有落地成檔案，也沒有交叉核對過。這支腳本把同樣的事實**落地成檔案**，
// 而且 Node/Go/Chrome 版本是這支腳本自己重新探測的實際值（不是沿用先前
// echo step 的輸出字串），checkout HEAD 是自己執行 `git rev-parse HEAD`
// 量出來的，不是單純複製 GitHub context 的宣稱值。
//
// 這支腳本本身「只讀 env + 執行固定的版本探測指令」，不接受、也不插值任何
// 來自 PR 內容的字串進 shell——上游 workflow 用 `env:` 把 GitHub context 的
// 值映射成環境變數，這裡讀取端一律當純字串處理（写進 JSON 的字串欄位），
// 不 eval、不組字串命令、不插進 shell（decision.md：「任何來自事件的字串經
// env/結構化參數傳遞，不插入shell程式碼」）。
//
// 用法：node generate-ci-envelope.mjs <output-path>
//
// 讀取的環境變數（GitHub Actions 對 pull_request 事件的預設環境變數 + 兩個
// 用 workflow `env:` 從 GitHub context 映射進來的 PR SHA）：
//   GITHUB_SHA              test-merge commit（GH 預設環境變數）
//   GITHUB_REPOSITORY       "<owner>/<repo>"（GH 預設環境變數）
//   GITHUB_RUN_ID           workflow run id（GH 預設環境變數）
//   GITHUB_RUN_ATTEMPT      workflow run attempt（GH 預設環境變數）
//   RUNNER_OS               例如 "macOS"（GH 預設環境變數）
//   ImageOS / ImageVersion  GitHub-hosted runner image 資訊（GH 預設環境
//                           變數，只有 hosted runner 才會設定，self-hosted
//                           沒有——本 workflow 一律用 hosted runner，要求存
//                           在；offline selftest 用
//                           CI_ENVELOPE_ALLOW_MISSING_IMAGE_INFO=1 明確放寬）
//   CI_ENVELOPE_PR_HEAD_SHA workflow 用 `env:` 從
//                           `${{ github.event.pull_request.head.sha }}` 映射
//   CI_ENVELOPE_PR_BASE_SHA 同上，`base.sha`
//
// 探測用命令（可用下列 env 覆寫成假指令陣列，僅供離線 selftest 使用，正式
// workflow 絕不設定這些覆寫；未設定時一律用真實指令）：
//   CI_ENVELOPE_REPO_ROOT        `git -C <repo-root> rev-parse HEAD` 的
//                                repo-root（預設 process.cwd()）
//   CI_ENVELOPE_GO_CMD_OVERRIDE     JSON 字串陣列 `["cmd","arg1",...]`，
//                                   預設 `["go","version"]`
//   CI_ENVELOPE_CHROME_CMD_OVERRIDE 同上，預設
//                                   `["/Applications/Google Chrome.app/Contents/MacOS/Google Chrome","--version"]`
//   CI_ENVELOPE_WAILS_CMD_OVERRIDE  同上，預設 `["wails","version"]`
//   CI_ENVELOPE_PLAYWRIGHT_CMD_OVERRIDE 同上，預設
//                                   `["node","<repo-root>/frontend/node_modules/.bin/playwright","--version"]`
//
// 任何一項必要事實探測失敗，這支腳本本身非零結束、不寫出殘缺/捏造的
// envelope——decision.md R2：「無相應欄位時不要invent欄位或宣稱已驗」。
import { spawnSync } from 'node:child_process';
import { mkdirSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { rejectTestControlEnvUnderGithubActions } from './gha-runtime-guard.mjs';

// review round 7（#491 G2）：三個正常 GitHub Actions 入口之一（另兩個是
// run-batch.mjs／evaluate-e2e-evidence.mjs）。見 gha-runtime-guard.mjs 頭
// 註——在任何 runtime／probe spawn（下面的 `git`／版本探測 spawnSync）之前
// 最早拒絕。
rejectTestControlEnvUnderGithubActions('generate-ci-envelope.mjs', [
  'CI_ENVELOPE_ALLOW_MISSING_IMAGE_INFO',
  'CI_ENVELOPE_GO_CMD_OVERRIDE',
  'CI_ENVELOPE_CHROME_CMD_OVERRIDE',
  'CI_ENVELOPE_WAILS_CMD_OVERRIDE',
  'CI_ENVELOPE_PLAYWRIGHT_CMD_OVERRIDE',
]);

const outputPath = process.argv[2];
if (!outputPath) {
  process.stderr.write('usage: generate-ci-envelope.mjs <output-path>\n');
  process.exit(2);
}

const errors = [];

function requireEnv(name) {
  const v = process.env[name];
  if (!v) errors.push(`必要環境變數 ${name} 未設定或為空`);
  return v ?? null;
}

const testMergeSha = requireEnv('GITHUB_SHA');
const prHeadSha = requireEnv('CI_ENVELOPE_PR_HEAD_SHA');
const prBaseSha = requireEnv('CI_ENVELOPE_PR_BASE_SHA');
const repository = requireEnv('GITHUB_REPOSITORY');
const workflowRunId = requireEnv('GITHUB_RUN_ID');
const workflowRunAttempt = requireEnv('GITHUB_RUN_ATTEMPT');
const runnerOs = requireEnv('RUNNER_OS');

// ImageOS／ImageVersion 只有 GitHub-hosted runner 才會設定；本機/selftest
// 環境不是 hosted runner，取不到是預期行為，不是缺陷——用明確旗標放寬，不
// 是悄悄用空字串或猜測值頂替。
const allowMissingImageInfo = process.env.CI_ENVELOPE_ALLOW_MISSING_IMAGE_INFO === '1';
let imageOs = process.env.ImageOS || null;
let imageVersion = process.env.ImageVersion || null;
if (!allowMissingImageInfo) {
  if (!imageOs) errors.push('必要環境變數 ImageOS 未設定或為空（GitHub-hosted runner 應該有；若在非 hosted runner 環境離線測試，設 CI_ENVELOPE_ALLOW_MISSING_IMAGE_INFO=1 明確放寬）');
  if (!imageVersion) errors.push('必要環境變數 ImageVersion 未設定或為空（同上）');
} else {
  imageOs = imageOs ?? '(offline-selftest-unavailable)';
  imageVersion = imageVersion ?? '(offline-selftest-unavailable)';
}

// checkout HEAD：自己現場執行 git rev-parse HEAD，不是複製 GitHub context
// 的宣稱值——這是「不能只 echo metadata」的核心：checkout 之後 repo 實際的
// HEAD 是量出來的事實，不是 PR 內容可以直接控制的字串。
const repoRoot = process.env.CI_ENVELOPE_REPO_ROOT ?? process.cwd();
const gitResult = spawnSync('git', ['-C', repoRoot, 'rev-parse', 'HEAD'], { encoding: 'utf8' });
let checkoutHeadSha = null;
if (gitResult.error || gitResult.status !== 0) {
  errors.push(`git rev-parse HEAD 失敗（-C ${repoRoot}）：${gitResult.error?.message ?? (gitResult.stderr ?? '').trim()}`);
} else {
  checkoutHeadSha = gitResult.stdout.trim();
  if (!/^[0-9a-f]{40}$/i.test(checkoutHeadSha)) {
    errors.push(`git rev-parse HEAD 輸出不是合法 40-hex SHA："${checkoutHeadSha}"`);
    checkoutHeadSha = null;
  }
}

// review round 5（#483 直接裁定）：checkout tree 的內容雜湊——比單一 commit
// SHA 更直接核對「這次 checkout 出來的檔案內容」本身（tree 物件雜湊涵蓋全
// 部檔案內容與路徑，不是 commit metadata）。`HEAD^{tree}` 用陣列參數傳給
// spawnSync（不經 shell），`^{...}` 不會被殼層展開／插值。
const treeResult = spawnSync('git', ['-C', repoRoot, 'rev-parse', 'HEAD^{tree}'], { encoding: 'utf8' });
let checkoutTreeSha = null;
if (treeResult.error || treeResult.status !== 0) {
  errors.push(`git rev-parse HEAD^{tree} 失敗（-C ${repoRoot}）：${treeResult.error?.message ?? (treeResult.stderr ?? '').trim()}`);
} else {
  checkoutTreeSha = treeResult.stdout.trim();
  if (!/^[0-9a-f]{40}$/i.test(checkoutTreeSha)) {
    errors.push(`git rev-parse HEAD^{tree} 輸出不是合法 40-hex SHA："${checkoutTreeSha}"`);
    checkoutTreeSha = null;
  }
}

// Node 版本：這支腳本自己就是在「這次 job 要用的那個 Node」底下執行，
// process.version 就是實際生效版本，不需要另外 spawn 探測。
const nodeVersionActual = process.version;

function resolveOverrideCommand(envVarName, defaultCmd, defaultArgs) {
  const override = process.env[envVarName];
  if (!override) return { cmd: defaultCmd, args: defaultArgs };
  try {
    const parsed = JSON.parse(override);
    if (Array.isArray(parsed) && parsed.length >= 1 && parsed.every((x) => typeof x === 'string')) {
      return { cmd: parsed[0], args: parsed.slice(1) };
    }
  } catch {
    // 往下走進統一的錯誤處理，不悄悄吞掉、也不悄悄退回預設值。
  }
  errors.push(`${envVarName} 設定了但不是合法的 JSON 字串陣列：${JSON.stringify(override)}`);
  return null;
}

let goVersionActual = null;
const goCommand = resolveOverrideCommand('CI_ENVELOPE_GO_CMD_OVERRIDE', 'go', ['version']);
if (goCommand) {
  const goResult = spawnSync(goCommand.cmd, goCommand.args, { encoding: 'utf8' });
  if (goResult.error || goResult.status !== 0) {
    errors.push(`探測 Go 版本失敗（${goCommand.cmd} ${goCommand.args.join(' ')}）：${goResult.error?.message ?? (goResult.stderr ?? '').trim()}`);
  } else {
    goVersionActual = goResult.stdout.trim();
  }
}

let chromeVersionActual = null;
const chromeCommand = resolveOverrideCommand(
  'CI_ENVELOPE_CHROME_CMD_OVERRIDE',
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  ['--version'],
);
if (chromeCommand) {
  const chromeResult = spawnSync(chromeCommand.cmd, chromeCommand.args, { encoding: 'utf8' });
  if (chromeResult.error || chromeResult.status !== 0) {
    errors.push(`探測 Chrome 版本失敗（${chromeCommand.cmd} ${chromeCommand.args.join(' ')}）：${chromeResult.error?.message ?? (chromeResult.stderr ?? '').trim()}`);
  } else {
    chromeVersionActual = chromeResult.stdout.trim();
  }
}

// review round 5（#483 直接裁定）：「原完整契約一直要求toolchain版本，不能
// 因某次複核條列沒重複Wails名稱就刪掉」——補上 Wails（job 已 `go install`
// 過 CLI）與 Playwright（`frontend/` 實際安裝的版本，不是 package.json 宣
// 告值）的現場探測，落地成檔案而不是只 echo 到 job log。
let wailsVersionActual = null;
const wailsCommand = resolveOverrideCommand('CI_ENVELOPE_WAILS_CMD_OVERRIDE', 'wails', ['version']);
if (wailsCommand) {
  const wailsResult = spawnSync(wailsCommand.cmd, wailsCommand.args, { encoding: 'utf8' });
  if (wailsResult.error || wailsResult.status !== 0) {
    errors.push(`探測 Wails 版本失敗（${wailsCommand.cmd} ${wailsCommand.args.join(' ')}）：${wailsResult.error?.message ?? (wailsResult.stderr ?? '').trim()}`);
  } else {
    wailsVersionActual = wailsResult.stdout.trim();
  }
}

let playwrightVersionActual = null;
const playwrightCommand = resolveOverrideCommand(
  'CI_ENVELOPE_PLAYWRIGHT_CMD_OVERRIDE',
  'node',
  [path.join(repoRoot, 'frontend', 'node_modules', '.bin', 'playwright'), '--version'],
);
if (playwrightCommand) {
  const playwrightResult = spawnSync(playwrightCommand.cmd, playwrightCommand.args, { encoding: 'utf8' });
  if (playwrightResult.error || playwrightResult.status !== 0) {
    errors.push(`探測 Playwright 版本失敗（${playwrightCommand.cmd} ${playwrightCommand.args.join(' ')}）：${playwrightResult.error?.message ?? (playwrightResult.stderr ?? '').trim()}`);
  } else {
    playwrightVersionActual = playwrightResult.stdout.trim();
  }
}

if (errors.length > 0) {
  process.stderr.write(
    `generate-ci-envelope.mjs：無法產生可信 envelope，拒絕寫出殘缺內容：\n${errors.map((e) => `  - ${e}`).join('\n')}\n`,
  );
  process.exit(1);
}

const envelope = {
  testMergeSha,
  prHeadSha,
  prBaseSha,
  repository,
  workflowRunId,
  workflowRunAttempt,
  runnerOs,
  imageOs,
  imageVersion,
  checkoutHeadSha,
  checkoutTreeSha,
  nodeVersionActual,
  goVersionActual,
  chromeVersionActual,
  wailsVersionActual,
  playwrightVersionActual,
  generatedAtIso: new Date().toISOString(),
};

mkdirSync(path.dirname(outputPath), { recursive: true });
writeFileSync(outputPath, `${JSON.stringify(envelope, null, 2)}\n`);
process.stdout.write(`[generate-ci-envelope] 已寫出 ${outputPath}\n`);
process.exit(0);
