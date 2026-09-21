// B3a-2b-2 F2：App binary identity 取得的受控測試。
import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {
  commandIsBinary, expectedAppBundleBinary, resolveAppBinaryIdentity,
} from './appBinaryIdentity.ts';
import type { ProcRowWithCommand } from '../psUtil.ts';

let passed = 0;
const failures: string[] = [];
function check(name: string, fn: () => void): void {
  try { fn(); console.log(`ok - ${name}`); passed += 1; }
  catch (e) { console.log(`FAIL - ${name}`); console.error(e); failures.push(name); process.exitCode = 1; }
}

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'f2-appid-'));
const NAME = 'sdlc-workbench';

/** 建一個 macOS bundle 佈局的假工作樹，回傳 repoRoot 與 bundle 內 binary 路徑。 */
function mkWorktree(dirName: string): { repoRoot: string; binary: string; sha: string } {
  const repoRoot = path.join(tmp, dirName);
  const macos = path.join(repoRoot, 'build', 'bin', `${NAME}.app`, 'Contents', 'MacOS');
  fs.mkdirSync(macos, { recursive: true });
  const binary = path.join(macos, NAME);
  fs.writeFileSync(binary, `#!/bin/sh\n# fake app for ${dirName}\nexit 0\n`);
  fs.chmodSync(binary, 0o755);
  return { repoRoot, binary, sha: crypto.createHash('sha256').update(fs.readFileSync(binary)).digest('hex') };
}

const W = mkWorktree('worktree-a');
const OTHER = mkWorktree('worktree-b');

const ROOT_PID = 1000;
const row = (pid: number, ppid: number, command: string, startedAt = 'Mon Sep 21 22:42:00 2026'): ProcRowWithCommand =>
  ({ pid, ppid, pgid: ROOT_PID, stat: 'S', startedAt, command });
const baseTable = (appCmd: string, appPid = 1050): ProcRowWithCommand[] => [
  row(ROOT_PID, 1, 'wails dev -noreload -skipbindings'),
  row(1010, ROOT_PID, 'npm run dev'),
  row(appPid, ROOT_PID, appCmd),
];
const okProbe = (startedAt = 'Mon Sep 21 22:42:00 2026', command?: string) => ({
  isAlive: () => true,
  startedAt: () => startedAt,
  command: () => command ?? null,
});

check('正控制（darwin）：bundle 內 binary ＋ 本次程序樹後代 → 核定成功', () => {
  const r = resolveAppBinaryIdentity({
    repoRoot: W.repoRoot, outputFileName: NAME, rootPid: ROOT_PID,
    table: baseTable(W.binary), platform: 'darwin',
    probe: okProbe(undefined, W.binary),
  });
  assert.deepEqual(r.violations, [], JSON.stringify(r.violations));
  assert.ok(r.identity);
  assert.equal(r.identity?.canonicalPath, fs.realpathSync(W.binary));
  assert.equal(r.identity?.sha256, W.sha);
  assert.equal(r.identity?.pid, 1050);
  assert.equal(r.identity?.startedAt, 'Mon Sep 21 22:42:00 2026');
});
check('預期路徑就是 macOS bundle 佈局（不是裸的 build/bin/<name>）', () => {
  assert.equal(expectedAppBundleBinary('/r', NAME),
    `/r/build/bin/${NAME}.app/Contents/MacOS/${NAME}`);
});
check('**舊的裸路徑不可用**：build/bin/<name> 不存在 → 拒絕', () => {
  const bare = path.join(tmp, 'bare');
  fs.mkdirSync(path.join(bare, 'build', 'bin'), { recursive: true });
  fs.writeFileSync(path.join(bare, 'build', 'bin', NAME), 'x');
  const r = resolveAppBinaryIdentity({
    repoRoot: bare, outputFileName: NAME, rootPid: ROOT_PID,
    table: baseTable(path.join(bare, 'build', 'bin', NAME)), platform: 'darwin', probe: okProbe(),
  });
  assert.equal(r.identity, null);
  assert.ok(r.violations.some(x => x.includes('預期路徑不存在')), JSON.stringify(r.violations));
});
check('**錯工作樹**：程序跑的是另一個工作樹的 bundle → 找不到候選', () => {
  const r = resolveAppBinaryIdentity({
    repoRoot: W.repoRoot, outputFileName: NAME, rootPid: ROOT_PID,
    table: baseTable(OTHER.binary), platform: 'darwin', probe: okProbe(),
  });
  assert.equal(r.identity, null);
  assert.ok(r.violations.some(x => x.includes('找不到執行')), JSON.stringify(r.violations));
});
check('**不屬於本次程序樹**的同名程序不得被核定', () => {
  const table: ProcRowWithCommand[] = [
    row(ROOT_PID, 1, 'wails dev'),
    { pid: 9000, ppid: 1, pgid: 9000, stat: 'S', startedAt: 'x', command: W.binary },
  ];
  const r = resolveAppBinaryIdentity({
    repoRoot: W.repoRoot, outputFileName: NAME, rootPid: ROOT_PID,
    table, platform: 'darwin', probe: okProbe(),
  });
  assert.equal(r.identity, null);
  assert.ok(r.violations.some(x => x.includes('找不到執行')));
});
check('**候選歧義**：同一 binary 有兩個程序 → 不挑任何一個', () => {
  const table = [...baseTable(W.binary), row(1060, ROOT_PID, W.binary)];
  const r = resolveAppBinaryIdentity({
    repoRoot: W.repoRoot, outputFileName: NAME, rootPid: ROOT_PID,
    table, platform: 'darwin', probe: okProbe(),
  });
  assert.equal(r.identity, null);
  assert.ok(r.violations.some(x => x.includes('候選歧義')), JSON.stringify(r.violations));
});
check('**程序已死** → 拒絕', () => {
  const r = resolveAppBinaryIdentity({
    repoRoot: W.repoRoot, outputFileName: NAME, rootPid: ROOT_PID,
    table: baseTable(W.binary), platform: 'darwin',
    probe: { isAlive: () => false, startedAt: () => 'x', command: () => W.binary },
  });
  assert.equal(r.identity, null);
  assert.ok(r.violations.some(x => x.includes('已不存活')));
});
check('**身分變動（pid 重用）**：startedAt 不一致 → 拒絕', () => {
  const r = resolveAppBinaryIdentity({
    repoRoot: W.repoRoot, outputFileName: NAME, rootPid: ROOT_PID,
    table: baseTable(W.binary), platform: 'darwin',
    probe: { isAlive: () => true, startedAt: () => 'Tue Sep 22 00:00:00 2026', command: () => W.binary },
  });
  assert.equal(r.identity, null);
  assert.ok(r.violations.some(x => x.includes('開始時間變動')), JSON.stringify(r.violations));
});
check('**command 變動** → 拒絕', () => {
  const r = resolveAppBinaryIdentity({
    repoRoot: W.repoRoot, outputFileName: NAME, rootPid: ROOT_PID,
    table: baseTable(W.binary), platform: 'darwin',
    probe: { isAlive: () => true, startedAt: () => 'Mon Sep 21 22:42:00 2026', command: () => '/bin/sh' },
  });
  assert.equal(r.identity, null);
  // 訊息文字隨實作改為「與快照不一致」（判定語意從「還是不是這支 binary」
  // 收緊成「與第一次快照逐字相同」）；**行為斷言 identity===null 不變**。
  assert.ok(r.violations.some(x => x.includes('command 與快照不一致')),
    JSON.stringify(r.violations));
});
check('**觀測失敗不得放行**：startedAt／command 取不到', () => {
  const r1 = resolveAppBinaryIdentity({
    repoRoot: W.repoRoot, outputFileName: NAME, rootPid: ROOT_PID,
    table: baseTable(W.binary), platform: 'darwin',
    probe: { isAlive: () => true, startedAt: () => null, command: () => W.binary },
  });
  assert.equal(r1.identity, null);
  assert.ok(r1.violations.some(x => x.includes('開始時間觀測失敗')));
  const r2 = resolveAppBinaryIdentity({
    repoRoot: W.repoRoot, outputFileName: NAME, rootPid: ROOT_PID,
    table: baseTable(W.binary), platform: 'darwin',
    probe: { isAlive: () => true, startedAt: () => 'Mon Sep 21 22:42:00 2026', command: () => null },
  });
  assert.equal(r2.identity, null);
  assert.ok(r2.violations.some(x => x.includes('command 觀測失敗')));
});
check('**非 darwin 明確拒絕**，不擴成跨平台探索', () => {
  const r = resolveAppBinaryIdentity({
    repoRoot: W.repoRoot, outputFileName: NAME, rootPid: ROOT_PID,
    table: baseTable(W.binary), platform: 'linux', probe: okProbe(),
  });
  assert.equal(r.identity, null);
  assert.ok(r.violations.some(x => x.includes('只支援 darwin')));
});
check('binary 不可執行 → 拒絕', () => {
  const w = mkWorktree('worktree-noexec');
  fs.chmodSync(w.binary, 0o644);
  const r = resolveAppBinaryIdentity({
    repoRoot: w.repoRoot, outputFileName: NAME, rootPid: ROOT_PID,
    table: baseTable(w.binary), platform: 'darwin', probe: okProbe(),
  });
  assert.equal(r.identity, null);
  assert.ok(r.violations.some(x => x.includes('不可執行')));
});
check('**路徑含空白**：不得用 split(\' \') 判斷 command', () => {
  const w = mkWorktree('work tree with spaces');
  const r = resolveAppBinaryIdentity({
    repoRoot: w.repoRoot, outputFileName: NAME, rootPid: ROOT_PID,
    table: baseTable(w.binary), platform: 'darwin',
    probe: okProbe(undefined, w.binary),
  });
  assert.deepEqual(r.violations, [], JSON.stringify(r.violations));
  assert.equal(r.identity?.canonicalPath, fs.realpathSync(w.binary));
});
check('commandIsBinary：**只接受完全相等**（ps 扁平字串無法分辨參數與含空白檔名）', () => {
  assert.ok(commandIsBinary('/a/b', '/a/b'));
  assert.ok(!commandIsBinary('/a/b --flag', '/a/b'), '後接空白的字串不得被當成同一支');
  assert.ok(!commandIsBinary('/a/bc', '/a/b'), '不得把 /a/bc 當成 /a/b');
  assert.ok(!commandIsBinary('/x /a/b', '/a/b'), '不得把它出現在參數裡當成執行它');
});
check('command 後面多帶字串的程序**不得**被核定（可能是含空白的別的檔名）', () => {
  const r = resolveAppBinaryIdentity({
    repoRoot: W.repoRoot, outputFileName: NAME, rootPid: ROOT_PID,
    table: baseTable(`${W.binary} other`), platform: 'darwin',
    probe: okProbe(undefined, `${W.binary} other`),
  });
  assert.equal(r.identity, null);
  assert.ok(r.violations.some(x => x.includes('找不到執行')), JSON.stringify(r.violations));
});
check('**二次觀測的 command 必須與第一次快照逐字相同**', () => {
  const r = resolveAppBinaryIdentity({
    repoRoot: W.repoRoot, outputFileName: NAME, rootPid: ROOT_PID,
    table: baseTable(W.binary), platform: 'darwin',
    probe: { isAlive: () => true, startedAt: () => 'Mon Sep 21 22:42:00 2026',
      command: () => `${W.binary} appended-later` },
  });
  assert.equal(r.identity, null);
  assert.ok(r.violations.some(x => x.includes('與快照不一致')), JSON.stringify(r.violations));
});

// --- canonical 邊界：symlink 逃逸（reviewer #375 重現的缺口） ----------------
check('**build/bin 是指向別的工作樹的 symlink → 必須拒絕**（不得把待檢子目錄的 realpath 當邊界）', () => {
  const a = path.join(tmp, 'wt-symlink-a');
  fs.mkdirSync(path.join(a, 'build'), { recursive: true });
  // a/build/bin → OTHER(worktree-b)/build/bin
  const link = path.join(a, 'build', 'bin');
  if (!fs.existsSync(link)) fs.symlinkSync(path.join(OTHER.repoRoot, 'build', 'bin'), link);
  const viaLink = path.join(a, 'build', 'bin', `${NAME}.app`, 'Contents', 'MacOS', NAME);
  assert.ok(fs.existsSync(viaLink), '前提：透過 symlink 看得到 b 的 binary');
  const r = resolveAppBinaryIdentity({
    repoRoot: a, outputFileName: NAME, rootPid: ROOT_PID,
    table: baseTable(viaLink), platform: 'darwin', probe: okProbe(undefined, viaLink),
  });
  assert.equal(r.identity, null, '不得回傳 identity');
  assert.ok(r.violations.some(x => x.includes('預期 bundle 路徑不符')), JSON.stringify(r.violations));
});
check('**bundle 目錄是 symlink → 拒絕**', () => {
  const a = path.join(tmp, 'wt-bundlelink');
  fs.mkdirSync(path.join(a, 'build', 'bin'), { recursive: true });
  const link = path.join(a, 'build', 'bin', `${NAME}.app`);
  if (!fs.existsSync(link)) fs.symlinkSync(path.join(OTHER.repoRoot, 'build', 'bin', `${NAME}.app`), link);
  const viaLink = path.join(link, 'Contents', 'MacOS', NAME);
  const r = resolveAppBinaryIdentity({
    repoRoot: a, outputFileName: NAME, rootPid: ROOT_PID,
    table: baseTable(viaLink), platform: 'darwin', probe: okProbe(undefined, viaLink),
  });
  assert.equal(r.identity, null);
  assert.ok(r.violations.some(x => x.includes('預期 bundle 路徑不符')), JSON.stringify(r.violations));
});
check('**leaf 是 symlink 指到別的工作樹 → 拒絕**', () => {
  const w = mkWorktree('wt-leaflink');
  fs.rmSync(w.binary);
  fs.symlinkSync(OTHER.binary, w.binary);
  const r = resolveAppBinaryIdentity({
    repoRoot: w.repoRoot, outputFileName: NAME, rootPid: ROOT_PID,
    table: baseTable(w.binary), platform: 'darwin', probe: okProbe(undefined, w.binary),
  });
  assert.equal(r.identity, null);
  assert.ok(r.violations.some(x => x.includes('預期 bundle 路徑不符')), JSON.stringify(r.violations));
});
check('正控制：repoRoot 自身是 canonical alias（/var → /private/var）仍應通過', () => {
  // tmp 本身就位於 /var/folders/... 其 realpath 是 /private/var/folders/...
  assert.notEqual(W.repoRoot, fs.realpathSync(W.repoRoot), '前提：本測試環境的 repoRoot 確實有 canonical alias');
  const r = resolveAppBinaryIdentity({
    repoRoot: W.repoRoot, outputFileName: NAME, rootPid: ROOT_PID,
    table: baseTable(W.binary), platform: 'darwin', probe: okProbe(undefined, W.binary),
  });
  assert.deepEqual(r.violations, [], JSON.stringify(r.violations));
  assert.equal(r.identity?.canonicalPath, fs.realpathSync(W.binary));
});

console.log(`\n${passed} passed, ${failures.length} failed`);
if (failures.length > 0) console.log(`failed: ${failures.join(' | ')}`);
