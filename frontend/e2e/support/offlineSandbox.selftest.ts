// offlineSandbox.ts 的純函式測試（P1，reviewer 複核 #34 第八次，2026-09-16）。
// 用 Node 原生 TS 支援直接執行：
//   node frontend/e2e/support/offlineSandbox.selftest.ts
// 用真正建立、可控權限的隔離暫存檔案（不是 fs mock 框架，延續專案既有的
// 「真實隔離 fixture」慣例），驗證 profile 不可讀／sandbox-exec 不可執行／
// Chrome 不存在／wrapper 不可執行這四種情境都會在
// `validateOfflineSandboxPrereqs` 這一步就直接拋錯——不需要真的啟動任何
// Wails／Chrome 行程就能證明。**不修改任何系統檔案**，全部指向 /tmp 底下
// 自己建立的隔離路徑。
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { validateOfflineSandboxPrereqs } from './offlineSandbox.ts';

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

const scratch = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a1-offlinesandbox-selftest-'));

// 建一組「全部正常」的基準路徑（真實可讀／可執行的暫存檔），每個測項各自
// 覆寫其中一項成壞掉的版本，其餘維持正常——這樣才能確認是「那一項」壞了
// 導致拋錯，不是連帶被其他項目影響。
const goodProfile = path.join(scratch, 'good.sb');
fs.writeFileSync(goodProfile, '(version 1)\n(allow default)\n');
fs.chmodSync(goodProfile, 0o644);

const goodExec = path.join(scratch, 'good-exec.sh');
fs.writeFileSync(goodExec, '#!/bin/bash\nexit 0\n');
fs.chmodSync(goodExec, 0o755);

function basePaths() {
  return {
    profile: goodProfile,
    wrapper: goodExec,
    sandboxExec: goodExec,
    chrome: goodExec,
  };
}

check('基準路徑（全部正常）：validateOfflineSandboxPrereqs 不拋錯', () => {
  validateOfflineSandboxPrereqs('chrome', basePaths());
});

check('P1-1：profile 不可讀（chmod 000）→ 在任何 Wails／Chrome 啟動前直接拋錯', () => {
  const unreadable = path.join(scratch, 'unreadable.sb');
  fs.writeFileSync(unreadable, '(version 1)\n');
  fs.chmodSync(unreadable, 0o000);
  try {
    assert.throws(
      () => validateOfflineSandboxPrereqs('chrome', { ...basePaths(), profile: unreadable }),
      /sandbox profile|不可讀|ENOENT|EACCES/,
    );
  } finally {
    fs.chmodSync(unreadable, 0o644); // 還原權限才能在 finally 清理暫存目錄時成功刪除。
  }
});

check('P1-2：profile 不存在 → 直接拋錯（不是 X_OK 檢查被空 catch 吞掉）', () => {
  const missing = path.join(scratch, 'does-not-exist.sb');
  assert.throws(() => validateOfflineSandboxPrereqs('chrome', { ...basePaths(), profile: missing }));
});

check('P1-3：profile 不是一般檔案（是目錄）→ 直接拋錯', () => {
  const dirAsProfile = path.join(scratch, 'a-directory.sb');
  fs.mkdirSync(dirAsProfile);
  assert.throws(() => validateOfflineSandboxPrereqs('chrome', { ...basePaths(), profile: dirAsProfile }));
});

check('P1-4：sandbox-exec 不可執行（存在但沒有 X 位元）→ 直接拋錯', () => {
  const notExecutable = path.join(scratch, 'sandbox-exec-no-x');
  fs.writeFileSync(notExecutable, '#!/bin/bash\nexit 0\n');
  fs.chmodSync(notExecutable, 0o644); // 只有讀寫，沒有執行位元。
  assert.throws(
    () => validateOfflineSandboxPrereqs('chrome', { ...basePaths(), sandboxExec: notExecutable }),
    /sandbox-exec|不可執行|EACCES/,
  );
});

check('P1-5：系統 Chrome 不存在 → 在任何 Wails／Chrome 啟動前直接拋錯（不是等 wrapper 真的執行才發現）', () => {
  const missingChrome = path.join(scratch, 'no-such-chrome-binary');
  assert.throws(
    () => validateOfflineSandboxPrereqs('chrome', { ...basePaths(), chrome: missingChrome }),
    /Chrome|ENOENT/,
  );
});

check('P1-6：Chrome sandbox wrapper 不可執行 → 直接拋錯', () => {
  const wrapperNoX = path.join(scratch, 'wrapper-no-x.sh');
  fs.writeFileSync(wrapperNoX, '#!/bin/bash\nexit 0\n');
  fs.chmodSync(wrapperNoX, 0o644);
  assert.throws(
    () => validateOfflineSandboxPrereqs('chrome', { ...basePaths(), wrapper: wrapperNoX }),
    /wrapper|不可執行|EACCES/,
  );
});

check('不支援的瀏覽器組合（chromium）→ 直接拋錯，不嘗試執行', () => {
  assert.throws(() => validateOfflineSandboxPrereqs('chromium', basePaths()), /只支援已驗證的系統 Chrome/);
});

console.log(`\n${passed} 項通過`);
if (process.exitCode) {
  console.error('有測項失敗');
} else {
  console.log('全部通過');
}

fs.rmSync(scratch, { recursive: true, force: true });
