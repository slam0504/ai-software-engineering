// canonicalDigest.ts 的正負案例——golden 值由 /tmp Go 產生器實際呼叫
// internal/spec 的 Scope.ManifestDigest／HashBytes 算出（產生方式與輸出見
// 任務回報「Go 產生 digest 期望值的方式」一節），不是憑空編造或從 journal
// 反填。也包含 design v4 §0 精確性修正 2 記錄的兩個已知不等價案例
// （HTML 轉義字元、空 slice null vs []），證明本模組如實反映界線、不假裝
// 全面等價。
//
// 執行：node --experimental-loader=./e2e/support/selftestJsToTsLoader.mjs e2e/support/gates/canonicalDigest.selftest.ts
import assert from 'node:assert/strict';
import {
  hashBytes, manifestDigest, permissionManifestDigest, planManifestDigest,
  singleFileDigest, specManifestDigest, SPEC_SCOPE_PATTERNS, SPEC_SCOPE_VERSION,
} from './canonicalDigest.js';

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

// ---- golden 值（Go 產生器實測輸出，2026-09-24） ----
const GOLDEN_EMPTY_SPECSCOPE = 'sha256:c3eeeac5cd47e3ebfaae66e2f7714520377738b79eea704321c7ddb92e7dc472';
const GOLDEN_TWO_ASCII_SPECSCOPE = 'sha256:395bef973788c9a024d53ee53a51a8fcdb0f567effe5a9783c8a480099fae584';
const GOLDEN_ONE_FILE_PLANSCOPE = 'sha256:55486b8fd335b65208408b3db26ab83df5fa2cf4f8c64a9e9faef58f46299340';
const GOLDEN_RISK_POLICY_SINGLE_FILE = 'sha256:696e50f199e780567007611967c04cf08d39002e57432243b2ba11b15e61cbfb';
const GOLDEN_SPECIAL_CHARS_SPECSCOPE = 'sha256:9e3606313164f301967ad3ffaec5348a9928b40415795883425d59479ff8b418';

check('hashBytes 對固定內容算出的 hex 與 Go crypto/sha256 golden 值相符（純 SHA-256，無 JSON 轉義疑慮）', () => {
  assert.equal(hashBytes('scenario A content\n'), 'd83c748bad2f09fefbd33ccb7efb76bba7dbb31ebba09dba0dde36ef5f20a4b9');
  assert.equal(hashBytes('scenario B content\n'), 'b58721c647d19f49722d0635011e08caa49f0b448a48cd96b507cc7750cd2064');
});

check('已知不等價 3：specManifestDigest(空陣列) 與 Go 端 empty entries golden 值不同——TS 側空陣列序列化為 "files":[]，Go 側 nil slice 序列化為 "files":null，兩者 canonical bytes 不同，digest 因此不同（呼叫端必須確保受測 fixture 的 entries 非空，不對空陣列宣稱等價）', () => {
  assert.notEqual(specManifestDigest([]), GOLDEN_EMPTY_SPECSCOPE);
});

check('specManifestDigest 兩個 ASCII 檔案（刻意反序輸入）與 Go golden 值相符——證明排序邏輯正確', () => {
  const entries = [
    { path: 'spec/features/b.feature', sha256: hashBytes('scenario B content\n') },
    { path: 'spec/features/a.feature', sha256: hashBytes('scenario A content\n') },
  ];
  assert.equal(specManifestDigest(entries), GOLDEN_TWO_ASCII_SPECSCOPE);
});

check('manifestDigest 直接帶 scope_version/patterns 參數，結果與 specManifestDigest 便利函式一致', () => {
  const entries = [
    { path: 'spec/features/a.feature', sha256: hashBytes('scenario A content\n') },
    { path: 'spec/features/b.feature', sha256: hashBytes('scenario B content\n') },
  ];
  assert.equal(manifestDigest(SPEC_SCOPE_VERSION, SPEC_SCOPE_PATTERNS, entries), GOLDEN_TWO_ASCII_SPECSCOPE);
});

check('planManifestDigest 單一檔案與 Go PlanScope golden 值相符', () => {
  const content = 'version: 1\ndefault_tier: medium\nrules: []\n';
  const entries = [{ path: 'plan/risk-policy.yaml', sha256: hashBytes(content) }];
  assert.equal(planManifestDigest(entries), GOLDEN_ONE_FILE_PLANSCOPE);
});

check('singleFileDigest（risk_policy binding 用，非 Scope.ManifestDigest）與 Go specDigestOf golden 值相符', () => {
  const content = 'version: 1\ndefault_tier: medium\nrules: []\n';
  assert.equal(singleFileDigest(content), GOLDEN_RISK_POLICY_SINGLE_FILE);
});

check('負案例：改動任一檔案內容會讓 digest 改變（不是恆等函式）', () => {
  const entries = [
    { path: 'spec/features/a.feature', sha256: hashBytes('scenario A content\n') },
    { path: 'spec/features/b.feature', sha256: hashBytes('scenario B content — TAMPERED\n') },
  ];
  assert.notEqual(specManifestDigest(entries), GOLDEN_TWO_ASCII_SPECSCOPE);
});

check('負案例：permissionManifestDigest 與 specManifestDigest 對同一組 entries 產出不同 digest（scope 參數是 canonical 內容的一部分）', () => {
  const entries = [{ path: 'plan/permissions/T1.yaml', sha256: hashBytes('allow: []\n') }];
  assert.notEqual(permissionManifestDigest(entries), specManifestDigest(entries));
});

// ---- 已知不等價界線（design v4 §0 精確性修正 2）：如實記錄差異，不宣稱全面等價 ----
check('已知不等價 1：路徑含 <>& 時，TS 的 JSON.stringify 不做 HTML 轉義，與 Go 的 encoding/json 轉義結果不同（不得宣稱此案例等價）', () => {
  const entries = [{ path: 'spec/features/<weird>&name.feature', sha256: hashBytes('x') }];
  const tsDigest = specManifestDigest(entries);
  assert.notEqual(tsDigest, GOLDEN_SPECIAL_CHARS_SPECSCOPE,
    'TS 與 Go 對含 <>& 的路徑產出不同 digest——這是已知界線，不是 bug；judge 只在 ASCII 無特殊字元的受控 fixture 下宣稱等價');
});

check('已知不等價 2：空 entries 陣列時 JS 的 files 序列化為 []，與 Go 的 nil→null 不同字面值（此處只驗證 TS 側行為本身，不比對 Go）', () => {
  const canonicalJson = JSON.stringify({ scope_version: 1, patterns: [...SPEC_SCOPE_PATTERNS], files: [] });
  assert.match(canonicalJson, /"files":\[\]/);
  assert.doesNotMatch(canonicalJson, /"files":null/);
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exitCode = 1;
