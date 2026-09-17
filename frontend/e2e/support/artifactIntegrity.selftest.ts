// artifactIntegrity.ts 的純函式測試（Q3，reviewer 二次審查，2026-09-15）。
// 跟 networkLineParser.selftest.ts 同一個理由：不放進 vitest 預設 suite
// （vitest.config.ts 排除 `e2e/**`），改用 Node 原生 TS 支援直接執行：
//   node frontend/e2e/support/artifactIntegrity.selftest.ts
//
// 這裡刻意**不**對真正的 repo 做任何 mode／內容變更（不能留下髒的工作
// 目錄）——改在 /tmp 建一個獨立的假 git repo 當 fixture，驗證：
//   1. mode 跟 HEAD 不符時，checkModeAgainstHead 抓得到（Q3 的核心案例，
//      對應 wailsjs 那次「內容沒變、mode 從 644 變 755」的實際事故）。
//   2. mode 跟 HEAD 相符時，沒有違規。
//   3. 內容在「執行前」跟「執行後」之間改變時，diffContentBeforeAfter 抓
//      得到——即使這個檔案原本就已經跟 HEAD 不同（模擬這張票本身的正當
//      改動，例如 package.json）。
//   4. 內容前後不變時，沒有違規。
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { checkModeAgainstHead, diffContentBeforeAfter, snapshotArtifacts } from './artifactIntegrity.ts';

let passed = 0;
function check(name: string, fn: () => void): void {
  try {
    fn();
    passed += 1;
    console.log(`ok - ${name}`);
  } catch (e) {
    console.error(`FAIL - ${name}`);
    console.error(e);
    process.exitCode = 1;
  }
}

function git(cwd: string, args: string[]): string {
  return execFileSync('git', args, { cwd, encoding: 'utf8', stdio: 'pipe' });
}

const tmpRepo = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a1-artifact-integrity-selftest-'));
try {
  git(tmpRepo, ['init', '-q']);
  git(tmpRepo, ['config', 'user.email', 'test@example.invalid']);
  git(tmpRepo, ['config', 'user.name', 'selftest']);

  fs.writeFileSync(path.join(tmpRepo, 'go.mod'), 'module example\n');
  fs.writeFileSync(path.join(tmpRepo, 'package.json'), '{"name":"x"}\n');
  fs.chmodSync(path.join(tmpRepo, 'go.mod'), 0o644);
  fs.chmodSync(path.join(tmpRepo, 'package.json'), 0o644);
  git(tmpRepo, ['add', 'go.mod', 'package.json']);
  git(tmpRepo, ['commit', '-q', '-m', 'init']);

  check('mode 跟 HEAD 相符時，沒有違規', () => {
    const violations = checkModeAgainstHead(tmpRepo, ['go.mod', 'package.json']);
    assert.deepEqual(violations, []);
  });

  check('mode 跟 HEAD 不符時（644→755，內容沒變），checkModeAgainstHead 抓得到（對應 wailsjs 事故）', () => {
    fs.chmodSync(path.join(tmpRepo, 'go.mod'), 0o755);
    const violations = checkModeAgainstHead(tmpRepo, ['go.mod', 'package.json']);
    assert.equal(violations.length, 1);
    assert.equal(violations[0].path, 'go.mod');
    assert.equal(violations[0].kind, 'mode-vs-head');
    fs.chmodSync(path.join(tmpRepo, 'go.mod'), 0o644); // 還原，避免影響後面的案例。
  });

  const testFiles = ['go.mod', 'package.json'];

  check('內容在執行前後不變時，diffContentBeforeAfter 沒有違規', () => {
    const before = snapshotArtifacts(tmpRepo, testFiles);
    const after = snapshotArtifacts(tmpRepo, testFiles);
    assert.deepEqual(diffContentBeforeAfter(before, after), []);
  });

  check('內容在執行期間被改動時（即使檔案本來就已經跟 HEAD 不同），diffContentBeforeAfter 抓得到', () => {
    const before = snapshotArtifacts(tmpRepo, testFiles);
    // 模擬「這張票本身的正當改動」：package.json 內容先改一次（跟 HEAD 不同，
    // 是合法的工作樹狀態），再模擬「執行期間又被動了一次」。
    fs.writeFileSync(path.join(tmpRepo, 'package.json'), '{"name":"x","legit-change":true}\n');
    const midBaseline = snapshotArtifacts(tmpRepo, testFiles); // 這才是「執行前」該用的基準。
    fs.writeFileSync(path.join(tmpRepo, 'package.json'), '{"name":"x","legit-change":true,"unexpected":true}\n');
    const after = snapshotArtifacts(tmpRepo, testFiles);
    assert.equal(diffContentBeforeAfter(before, midBaseline).length > 0, true); // 跟最初的 HEAD 快照比，這裡本來就該有差異（正當改動）。
    const violations = diffContentBeforeAfter(midBaseline, after);
    assert.equal(violations.length, 1);
    assert.equal(violations[0].path, 'package.json');
    assert.equal(violations[0].kind, 'content-changed-during-run');
  });
} finally {
  fs.rmSync(tmpRepo, { recursive: true, force: true });
}

console.log(`\n${passed} 項通過`);
if (process.exitCode) {
  console.error('有測項失敗');
} else {
  console.log('全部通過');
}
