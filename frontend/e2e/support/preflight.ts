// 啟動前預檢（§2.2）：任何一項不符，就在 spawn `wails dev` 之前失敗，並寫入
// harness.log。**不啟動 app**。
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { checkModeAgainstHead, listScopedFiles } from './artifactIntegrity.js';
import { Fixture } from './fixture.js';
import { FakeCliSet, snapshotInvocationsLog, truncateInvocationsLog } from './fakeCli.js';
import { HarnessLogger } from './logger.js';
import { isPortListening } from './psUtil.js';

export class PreflightError extends Error {
  constructor(msg: string) {
    super(msg);
    this.name = 'PreflightError';
  }
}

function fail(log: HarnessLogger, msg: string): never {
  log.log(`PREFLIGHT FAIL: ${msg}`);
  throw new PreflightError(msg);
}

export function checkFixture(fixture: Fixture, repoRoot: string, log: HarnessLogger): void {
  log.log(`預檢 1/5：fixture ${fixture.root}`);
  const st = fs.statSync(fixture.root, { throwIfNoEntry: false });
  if (!st || !st.isDirectory()) fail(log, `fixture 根目錄不存在或不是目錄：${fixture.root}`);

  const probe = path.join(fixture.root, '.e2e-write-probe');
  try {
    fs.writeFileSync(probe, 'probe');
    fs.rmSync(probe);
  } catch (e) {
    fail(log, `fixture 根目錄不可寫：${String(e)}`);
  }

  try {
    const workbench = path.join(fixture.root, '.workbench');
    fs.mkdirSync(workbench, { recursive: true });
  } catch (e) {
    fail(log, `無法在 fixture 下建立 .workbench：${String(e)}`);
  }

  try {
    execFileSync('git', ['log', '--oneline', '-1'], { cwd: fixture.root, stdio: 'pipe' });
  } catch (e) {
    fail(log, `fixture 不是 git repo 或沒有初始 commit：${String(e)}`);
  }

  const home = process.env.HOME ?? '';
  const realRepoRoot = fs.realpathSync(repoRoot);
  if (fixture.root === home) fail(log, 'fixture realpath 不得等於 HOME');
  if (fixture.root === realRepoRoot || fixture.root.startsWith(realRepoRoot + path.sep)) {
    fail(log, `fixture realpath 不得在 repo 內（repo=${realRepoRoot}）`);
  }
  log.log('預檢 1/5：fixture 通過');
}

export function checkFakeCli(cli: FakeCliSet, artifactsDir: string, log: HarnessLogger): void {
  log.log('預檢 2/5：假 CLI 可執行且回報正確版本');
  const claudeBin = path.join(cli.toolsDir, 'claude-cli', 'node_modules', '.bin', 'claude');
  const codexBin = path.join(cli.toolsDir, 'codex-cli', 'node_modules', '.bin', 'codex');

  for (const [name, bin, expected] of [
    ['claude', claudeBin, cli.claudeVersion],
    ['codex', codexBin, cli.codexVersion],
  ] as const) {
    if (!fs.existsSync(bin)) fail(log, `假 ${name} CLI 不存在：${bin}`);
    const st = fs.statSync(bin);
    if ((st.mode & 0o111) === 0) fail(log, `假 ${name} CLI 不可執行：${bin}`);
    let out: string;
    try {
      out = execFileSync(bin, ['--version'], { encoding: 'utf8' }).trim();
    } catch (e) {
      fail(log, `假 ${name} CLI --version 執行失敗：${String(e)}`);
    }
    if (out !== expected) fail(log, `假 ${name} CLI 版本不符：got=${JSON.stringify(out)} want=${JSON.stringify(expected)}`);
  }

  // 落在證據目錄頂層（§2.9），不是 fake-tools/ 底下——跟最終的 invocations.log
  // （globalTeardown 才會從 fake-tools/ 複製出來的那份）放在同一層，方便對照。
  const preflightLog = path.join(artifactsDir, 'preflight-invocations.log');
  snapshotInvocationsLog(cli.toolsDir, preflightLog);
  truncateInvocationsLog(cli.toolsDir);
  log.log(`預檢 2/5：通過，呼叫紀錄存為 ${preflightLog}，正式 invocations.log 已清空`);
}

export function checkEnvValues(workspaceDir: string, toolsDir: string, log: HarnessLogger): void {
  log.log('預檢 3/5：WORKBENCH_WORKSPACE／WORKBENCH_TOOLS_DIR 值檢查');
  if (!workspaceDir || !path.isAbsolute(workspaceDir)) fail(log, `workspaceDir 無效：${workspaceDir}`);
  if (!toolsDir || !path.isAbsolute(toolsDir)) fail(log, `toolsDir 無效：${toolsDir}`);
  log.log('預檢 3/5：通過');
}

export function checkPortFree(port: number, log: HarnessLogger): void {
  log.log(`預檢 4/5：埠 ${port} 是否已被佔用`);
  let listening: boolean;
  try {
    listening = isPortListening(port);
  } catch (e) {
    // R2：觀測失敗（lsof 找不到執行檔／逾時／權限）不能當成「埠空閒」，
    // 必須 fail loud——不知道埠狀態就啟動 app 是更危險的選擇。
    fail(log, `無法確認埠 ${port} 是否空閒（lsof 觀測失敗，不啟動 app）：${e instanceof Error ? e.message : String(e)}`);
    return;
  }
  if (listening) {
    fail(log, `埠 ${port} 已被佔用，不啟動 app、不終止佔用者`);
  }
  log.log(`預檢 4/5：通過，埠 ${port} 空閒`);
}

// checkArtifactModeAgainstHead：Q3（reviewer 二次審查，2026-09-15）。mode 永遠
// 跟 HEAD 比——不管內容有沒有跟 HEAD 不同，意外變成 755 這件事本身永遠是
// 錯的（wailsjs 的既有事故就是這個形狀：內容沒變、mode 從 644 變 755）。這裡
// 只檢查 mode；內容的執行前後比對在 globalTeardown 做（要等執行後才有「後」
// 可以比）。查不到 HEAD 基準（例如新增中還沒 commit 的檔案）就跳過那個檔案，
// 不當成失敗——沒有基準沒辦法比。
export function checkArtifactModeAgainstHead(repoRoot: string, log: HarnessLogger): void {
  log.log('預檢 5/5：受版控產物 mode 是否符合 HEAD（frontend/wailsjs／go.mod／go.sum／frontend/package*）');
  const files = listScopedFiles(repoRoot);
  const violations = checkModeAgainstHead(repoRoot, files);
  if (violations.length > 0) {
    const detail = violations.map(v => `${v.path}：${v.detail}`).join('；');
    fail(
      log,
      `受版控產物 mode 跟 HEAD 不符，不啟動 app（這是持續性問題，不是這次執行造成的——需要先手動核對並修正，見 README「Browser E2E」段的一次性修復程序）：${detail}`,
    );
  }
  log.log('預檢 5/5：通過，受版控產物 mode 皆符合 HEAD');
}
