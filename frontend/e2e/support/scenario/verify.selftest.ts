// verify.ts 的純函式測試（B3a-2b-1）——重點是**負控制**：故意灌錯 id／decision／
// method 與壞掉的 log／manifest，證明判定函式真的會抓到（而不是一個永遠回
// []／永遠不 throw 的空殼）。
//
// 執行：node frontend/e2e/support/scenario/verify.selftest.ts
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import type { Manifest } from './protocol.ts';
import { judgeApproval, parseManifest, parseRunLog } from './verify.ts';

let passed = 0;
let failed = 0;
function check(name: string, fn: () => void): void {
  try {
    fn();
    passed += 1;
    console.log(`ok - ${name}`);
  } catch (e) {
    failed += 1;
    console.error(`FAIL - ${name}`);
    console.error(e);
  }
}

function goodManifest(overrides: Partial<Manifest> = {}): Manifest {
  return {
    scenario: 'x',
    argv: ['app-server'],
    pid: 123,
    startedAt: '2026-09-21T00:00:00.000Z',
    endedAt: '2026-09-21T00:00:01.000Z',
    exitCode: 0,
    approvalMethod: 'item/commandExecution/requestApproval',
    approvalRequestId: 'appr-1',
    decisionReceived: 'accept',
    unknownMethodsSeen: [],
    fatalError: null,
    ...overrides,
  };
}

const expected = { requestId: 'appr-1', method: 'item/commandExecution/requestApproval', decision: 'accept' as const };

check('judgeApproval 正控制：完全符合時回傳空陣列', () => {
  const violations = judgeApproval(goodManifest(), expected);
  assert.deepEqual(violations, []);
});

// --- 負控制：id 不符 ---
check('judgeApproval 負控制：id 不符會被抓到（不是空殼 checker）', () => {
  const bad = goodManifest({ approvalRequestId: 'appr-WRONG' });
  const violations = judgeApproval(bad, expected);
  assert.ok(violations.length > 0, 'expected at least one violation for id mismatch');
  assert.ok(violations.some(v => v.includes('approvalRequestId')), `violations did not mention id: ${violations}`);
});

check('judgeApproval 負控制：id 型別不同（number vs string）視為不符，非寬鬆比較', () => {
  // approvalRequestId 存的是字串 "1"，expected 用 number 1 ——嚴格比較必須
  // 判定不符，證明本函式沒有偷用 `==`／String() 掩蓋型別轉換。
  const bad = goodManifest({ approvalRequestId: '1' });
  const violations = judgeApproval(bad, { ...expected, requestId: 1 as unknown as string });
  assert.ok(violations.length > 0, 'expected type-strict id comparison to flag mismatch');
});

// --- 負控制：decision 不符 ---
check('judgeApproval 負控制：decision 不符會被抓到', () => {
  const bad = goodManifest({ decisionReceived: 'decline' });
  const violations = judgeApproval(bad, expected);
  assert.ok(violations.length > 0, 'expected at least one violation for decision mismatch');
  assert.ok(violations.some(v => v.includes('decisionReceived')));
});

// --- 負控制：確認「不檢查判定結果」會讓整個 harness fail-loud，不是靜靜過關 ---
check('負控制的意義：若呼叫端忘記檢查 violations，用 assert 逼出失敗（demonstration）', () => {
  const bad = goodManifest({ decisionReceived: 'decline' });
  const violations = judgeApproval(bad, expected);
  // 模擬呼叫端「應該」要做的事：非空 violations 必須讓執行失敗。這裡刻意用
  // assert.equal(violations.length, 0) 來證明——如果 judgeApproval 是空殼
  // （永遠回 []），這個 assert 就不會丟，selftest 本身也就抓不到問題；
  // 现在它確實丟出來，代表負控制鏈路是通的。
  assert.throws(() => assert.equal(violations.length, 0));
});

// --- manifest／run log 的缺失與 malformed 偵測 ---
const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a2b1-verify-selftest-'));

check('parseManifest：缺檔案時 throw', () => {
  assert.throws(() => parseManifest(path.join(tmpDir, 'nonexistent.manifest.json')));
});

check('parseManifest：JSON 壞掉時 throw', () => {
  const p = path.join(tmpDir, 'bad.manifest.json');
  fs.writeFileSync(p, '{not json');
  assert.throws(() => parseManifest(p));
});

check('parseManifest：缺必要欄位時 throw（不是靜默回傳半成品物件）', () => {
  const p = path.join(tmpDir, 'incomplete.manifest.json');
  fs.writeFileSync(p, JSON.stringify({ scenario: 'x' })); // 缺 argv/pid/startedAt
  assert.throws(() => parseManifest(p));
});

check('parseManifest：exitCode 仍是 null（fake 尚未收尾）時 throw', () => {
  const p = path.join(tmpDir, 'unfinished.manifest.json');
  fs.writeFileSync(p, JSON.stringify(goodManifest({ exitCode: null })));
  assert.throws(() => parseManifest(p));
});

check('parseManifest：正常 manifest 時成功解析', () => {
  const p = path.join(tmpDir, 'ok.manifest.json');
  fs.writeFileSync(p, JSON.stringify(goodManifest()));
  const m = parseManifest(p);
  assert.equal(m.scenario, 'x');
});

check('parseRunLog：空檔案時 throw', () => {
  const p = path.join(tmpDir, 'empty.jsonl');
  fs.writeFileSync(p, '');
  assert.throws(() => parseRunLog(p));
});

check('parseRunLog：某一行 JSON 壞掉時 throw（不是跳過壞行繼續）', () => {
  const p = path.join(tmpDir, 'badline.jsonl');
  fs.writeFileSync(p, '{"seq":1,"ts":"t","dir":"meta"}\nnot json at all\n');
  assert.throws(() => parseRunLog(p));
});

check('parseRunLog：seq 不遞增（重複／倒退）時 throw', () => {
  const p = path.join(tmpDir, 'outoforder.jsonl');
  fs.writeFileSync(
    p,
    '{"seq":1,"ts":"t1","dir":"meta"}\n{"seq":1,"ts":"t2","dir":"meta"}\n',
  );
  assert.throws(() => parseRunLog(p));
});

check('parseRunLog：正常檔案時成功解析且保留筆數', () => {
  const p = path.join(tmpDir, 'ok.jsonl');
  fs.writeFileSync(
    p,
    '{"seq":1,"ts":"t1","dir":"meta"}\n{"seq":2,"ts":"t2","dir":"c2s","frame":{"id":1,"method":"initialize","params":{}}}\n',
  );
  const entries = parseRunLog(p);
  assert.equal(entries.length, 2);
});

// --- R1 缺陷修正回歸：直接重現 codex-reviewer 205 輪的反例輸入 ---
// （/tmp/b3a2b1-repro205-gc_r3e4d/parser-repro.mjs 與 invalid-log.jsonl）
const baseManifestFields = {
  scenario: 'x',
  argv: ['app-server'],
  pid: 1,
  startedAt: 't',
  approvalRequestId: 'id',
  approvalMethod: 'item/commandExecution/requestApproval',
  decisionReceived: 'accept',
};

check('parseManifest：完全省略 exitCode／endedAt 欄位時 throw（先前會靜默通過）', () => {
  const p = path.join(tmpDir, 'missing-exit.manifest.json');
  fs.writeFileSync(p, JSON.stringify(baseManifestFields));
  assert.throws(() => parseManifest(p));
});

check('judgeApproval：identity 相符但 exitCode/fatalError/unknownMethodsSeen 顯示失敗時必須回報違規（不是 []）', () => {
  const failed = goodManifest({
    exitCode: 17,
    fatalError: 'failed',
    unknownMethodsSeen: ['bad'],
  });
  const violations = judgeApproval(failed, expected);
  assert.ok(violations.length > 0, 'expected violations for a failed manifest with matching identity');
  assert.ok(violations.some(v => v.includes('exitCode')));
  assert.ok(violations.some(v => v.includes('fatalError')));
  assert.ok(violations.some(v => v.includes('unknownMethodsSeen')));
});

check('parseRunLog：seq=1.5／dir="bogus"／無 frame 的紀錄時 throw（先前會靜默通過）', () => {
  const p = path.join(tmpDir, 'invalid.jsonl');
  fs.writeFileSync(p, JSON.stringify({ seq: 1.5, ts: 'bad-time', dir: 'bogus' }) + '\n');
  assert.throws(() => parseRunLog(p));
});

check('parseRunLog：dir=c2s 但缺 frame 時 throw', () => {
  const p = path.join(tmpDir, 'c2s-no-frame.jsonl');
  fs.writeFileSync(p, '{"seq":1,"ts":"t","dir":"c2s"}\n');
  assert.throws(() => parseRunLog(p));
});

// --- 測試與交付補正 #1：最小的實際子程序呼叫（不只證明 assert 會 throw，
// 而是證明 caller 真的用非零 exit code 反映判定結果，不會 catch 後吞掉）。
// 只證明本包 caller；不宣稱 browser runner 已涵蓋。
const verifyModuleUrl = new URL('./verify.ts', import.meta.url).href;

function runCallerSubprocess(manifestObj: unknown, expectedObj: unknown): { code: number | null; stdout: string } {
  const script = `
    import { parseManifest, judgeApproval } from '${verifyModuleUrl}';
    import fs from 'node:fs';
    const path = process.argv[2];
    try {
      const m = parseManifest(path);
      const violations = judgeApproval(m, ${JSON.stringify(expectedObj)});
      if (violations.length > 0) {
        console.error('VIOLATIONS: ' + JSON.stringify(violations));
        process.exit(1);
      }
      console.log('ACCEPTED');
      process.exit(0);
    } catch (e) {
      console.error('REJECTED: ' + e.message);
      process.exit(1);
    }
  `;
  const scriptPath = path.join(tmpDir, `caller-${Math.random().toString(36).slice(2)}.mjs`);
  fs.writeFileSync(scriptPath, script);
  const manifestPath = path.join(tmpDir, `caller-manifest-${Math.random().toString(36).slice(2)}.json`);
  fs.writeFileSync(manifestPath, JSON.stringify(manifestObj));
  const res = spawnSync(process.execPath, [scriptPath, manifestPath], { encoding: 'utf8' });
  return { code: res.status, stdout: res.stdout + res.stderr };
}

check('實際子程序 caller：正確證據 rc0', () => {
  const { code, stdout } = runCallerSubprocess(goodManifest(), expected);
  assert.equal(code, 0, `expected rc0, got ${code}, output=${stdout}`);
});

check('實際子程序 caller：wrong expected id 時非零 exit（不是 catch 後回成功）', () => {
  const { code } = runCallerSubprocess(goodManifest(), { ...expected, requestId: 'appr-WRONG' });
  assert.notEqual(code, 0, 'expected nonzero exit for wrong expected id');
});

check('實際子程序 caller：failed manifest（exitCode!=0）時非零 exit', () => {
  const { code } = runCallerSubprocess(goodManifest({ exitCode: 17, fatalError: 'boom' }), expected);
  assert.notEqual(code, 0, 'expected nonzero exit for failed manifest');
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exitCode = 1;
