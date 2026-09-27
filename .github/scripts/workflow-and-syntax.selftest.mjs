#!/usr/bin/env node
// workflow-and-syntax.selftest.mjs — decision470 第 37 行要求：「node
// --check、既有可用解析工具檢查workflow語法與trigger/timeout/permissions，
// 必要時使用現有依賴；不為驗證安裝套件」。
//
// 兩部分：
//   1. `node --check` 對本輪新增的所有 .mjs 檔做語法檢查（不執行，純語法）。
//   2. workflow YAML 的結構檢查：本機沒有 npm 套件形式的 YAML parser
//      （node_modules 沒有 yaml/js-yaml，且本輪不安裝任何套件），但系統內建
//      的 Ruby（/usr/bin/ruby，macOS 系統自帶，不是本輪安裝的）標準庫含
//      Psych YAML parser，用它做真正的 YAML 結構解析，而不是拿 regex 硬解
//      ——比純文字比對可靠，但仍要老實承認限制：Psych 只驗證這是合法
//      YAML、能照結構讀出欄位，**不驗證** GitHub Actions 自己的 schema
//      （例如 `${{ }}` expression 語法是否合法、action 版本是否存在），那
//      部分「不能做到的檢查」明列在下方輸出，不假裝通過。
//
// 執行：node .github/scripts/workflow-and-syntax.selftest.mjs
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { readdirSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const REPO_ROOT = path.resolve(__dirname, '..', '..');
const WORKFLOW = path.join(REPO_ROOT, '.github', 'workflows', 'b3a-e2e-ci.yml');

let passed = 0;
let failed = 0;
function check(name, fn) {
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

// --- 1. node --check：本輪新增／修改的所有 .mjs（含 __fixtures__，排除 selftest 自己執行中這支） ---
const mjsFiles = readdirSync(__dirname)
  .filter((f) => f.endsWith('.mjs'))
  .map((f) => path.join(__dirname, f));
const fixturesDir = path.join(__dirname, '__fixtures__');
const fixtureFiles = readdirSync(fixturesDir).filter((f) => f.endsWith('.mjs')).map((f) => path.join(fixturesDir, f));

for (const file of [...mjsFiles, ...fixtureFiles]) {
  check(`node --check ${path.relative(REPO_ROOT, file)}`, () => {
    const result = spawnSync('node', ['--check', file], { encoding: 'utf8' });
    assert.equal(result.status, 0, result.stderr);
  });
}

// --- 2. workflow YAML：Ruby Psych 解析 + 結構斷言 ---
function rubyEval(expr) {
  const script = `
require 'yaml'
y = YAML.load_file(${JSON.stringify(WORKFLOW)})
result = (${expr})
if result.is_a?(String) || result.is_a?(Numeric) || result == true || result == false || result.nil?
  puts result.inspect
else
  require 'json'
  puts result.to_json
end
`;
  const result = spawnSync('ruby', ['-e', script], { encoding: 'utf8' });
  if (result.status !== 0) {
    throw new Error(`ruby 解析失敗：${result.stderr}`);
  }
  return result.stdout.trim();
}

check('workflow YAML 本身是合法 YAML（Ruby Psych 可解析）', () => {
  const out = rubyEval('y["name"]');
  assert.equal(out, '"b3a-e2e-ci"');
});

check('trigger 是 pull_request types:[labeled]（不是 pull_request_target／workflow_dispatch／schedule）', () => {
  // 真實發現、非猜測：YAML 1.1（Ruby Psych 預設語意）把裸字 `on` 解析成
  // boolean `true` 這個 key，不是字串 "on"——GitHub Actions 工作流檔案的
  // `on:` 頂層 key 因此在通用 YAML 1.1 parser 下要用 `y[true]` 存取，不能
  // 用 `y["on"]`（第一次執行時 `y["on"]` 回傳 nil，見
  // workflow-syntax-selftest-first.log）。GitHub 自己的 workflow parser 不
  // 受這個語意影響（它認得 `on:` 是保留字），這裡只是記錄「用通用 YAML
  // parser 讀 GHA 檔案」本身的已知陷阱。
  assert.equal(rubyEval('y[true].keys'), '["pull_request"]');
  assert.equal(rubyEval('y[true]["pull_request"]["types"]'), '["labeled"]');
});

check('concurrency cancel-in-progress=false', () => {
  assert.equal(rubyEval('y["concurrency"]["cancel-in-progress"]'), 'false');
});

check('permissions 只有 contents: read（最小權限）', () => {
  assert.equal(rubyEval('y["permissions"]'), '{"contents":"read"}');
});

check('三個 job（smoke/gates/scenarios）都存在，且各自 timeout-minutes 精確等於 109/139/289（review round 4：新增 generate-ci-envelope step 把 setup 從 33min 推到 34min，round 3 的 33min 更正值＋entry 30min 上限重算基礎不變）', () => {
  assert.equal(rubyEval('y["jobs"].keys.sort'), '["gates","scenarios","smoke"]');
  assert.equal(rubyEval('y["jobs"]["smoke"]["timeout-minutes"]'), '109');
  assert.equal(rubyEval('y["jobs"]["gates"]["timeout-minutes"]'), '139');
  assert.equal(rubyEval('y["jobs"]["scenarios"]["timeout-minutes"]'), '289');
});

check('review round 4（#480 R2 剩餘缺口）：三個 job 都有剛好一個 "generate CI invocation envelope" step，run 腳本指向 generate-ci-envelope.mjs 且輸出路徑對應各自的批次', () => {
  for (const [jobName, batch] of [['smoke', 'smoke'], ['gates', 'gates'], ['scenarios', 'scenarios']]) {
    const steps = JSON.parse(
      rubyEval(`y["jobs"]["${jobName}"]["steps"].select { |s| s["name"] == "generate CI invocation envelope" }`),
    );
    assert.equal(steps.length, 1, `job=${jobName} 應該剛好一個 envelope 產生 step`);
    const step = steps[0];
    assert.match(step.run, /generate-ci-envelope\.mjs/);
    assert.match(step.run, new RegExp(`\\.github/\\.ci-e2e-work/${batch}/envelope\\.json`));
    assert.deepEqual(Object.keys(step.env).sort(), ['CI_ENVELOPE_PR_BASE_SHA', 'CI_ENVELOPE_PR_HEAD_SHA']);
    assert.equal(step.env.CI_ENVELOPE_PR_HEAD_SHA, "${{ github.event.pull_request.head.sha }}");
    assert.equal(step.env.CI_ENVELOPE_PR_BASE_SHA, "${{ github.event.pull_request.base.sha }}");
  }
});

check('review round 4：envelope 產生 step 在三個 job 裡都排在「record tool versions」之後、批次執行 step 之前（Node/Go/Chrome 都已就緒才探測版本，且 envelope 要先於任何 entry 執行存在）', () => {
  for (const jobName of ['smoke', 'gates', 'scenarios']) {
    const names = JSON.parse(rubyEval(`y["jobs"]["${jobName}"]["steps"].map { |s| s["name"] }.compact`));
    const toolVersionsIdx = names.indexOf('record tool versions');
    const envelopeIdx = names.indexOf('generate CI invocation envelope');
    const runBatchIdx = names.findIndex((n) => n.startsWith('run ') && n.includes('batch'));
    assert.ok(toolVersionsIdx >= 0 && envelopeIdx >= 0 && runBatchIdx >= 0, `job=${jobName} 缺少預期的 step 名稱：${JSON.stringify(names)}`);
    assert.ok(toolVersionsIdx < envelopeIdx, `job=${jobName}：envelope step 應該在 record tool versions 之後`);
    assert.ok(envelopeIdx < runBatchIdx, `job=${jobName}：envelope step 應該在批次執行 step 之前`);
  }
});

check('review round 3（R5）：三個 setup-node 一律精確釘 26.10.0（不是 major 範圍 "26"）', () => {
  for (const jobName of ['smoke', 'gates', 'scenarios']) {
    const versions = JSON.parse(rubyEval(`y["jobs"]["${jobName}"]["steps"].select { |s| (s["uses"]||"").include?("setup-node") }.map { |s| s["with"]["node-version"] }`));
    assert.deepEqual(versions, ['26.10.0']);
  }
});

check('三個 job 的 if 條件各自精確比對對應 label（不是用「掛著哪些 label」的集合判斷）', () => {
  const smokeIf = rubyEval('y["jobs"]["smoke"]["if"]');
  const gatesIf = rubyEval('y["jobs"]["gates"]["if"]');
  const scenariosIf = rubyEval('y["jobs"]["scenarios"]["if"]');
  assert.match(smokeIf, /b3a-ci-smoke/);
  assert.match(gatesIf, /b3a-ci-gates/);
  assert.match(scenariosIf, /b3a-ci-scenarios/);
  // 每個 if 都要核對 base、同 repo、專用分支三項——不是只看 label。
  for (const cond of [smokeIf, gatesIf, scenariosIf]) {
    assert.match(cond, /base\.ref == 'main'/);
    assert.match(cond, /head\.repo\.full_name == github\.repository/);
    assert.match(cond, /head_ref == 'ci\/b3a-e2e-integration'/);
  }
});

check('runs-on 都是 macos-15-intel（與 ci.yml 現行選擇一致）', () => {
  assert.equal(rubyEval('y["jobs"]["smoke"]["runs-on"]'), '"macos-15-intel"');
  assert.equal(rubyEval('y["jobs"]["gates"]["runs-on"]'), '"macos-15-intel"');
  assert.equal(rubyEval('y["jobs"]["scenarios"]["runs-on"]'), '"macos-15-intel"');
});

check('action 版本沿用 ci.yml 既有 pinned full SHA（checkout/setup-node/setup-go/upload-artifact）', () => {
  const usesList = rubyEval('y["jobs"]["smoke"]["steps"].map { |s| s["uses"] }.compact');
  const uses = JSON.parse(usesList);
  assert.ok(uses.some((u) => u.startsWith('actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1')));
  assert.ok(uses.some((u) => u.startsWith('actions/setup-node@820762786026740c76f36085b0efc47a31fe5020')));
  assert.ok(uses.some((u) => u.startsWith('actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e')));
  assert.ok(uses.some((u) => u.startsWith('actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a')));
});

check('事件字串（github.sha／head.sha／base.sha）一律經 env: 傳遞，run: 腳本裡不直接插入 ${{ }}', () => {
  for (const jobName of ['smoke', 'gates', 'scenarios']) {
    const steps = JSON.parse(rubyEval(`y["jobs"]["${jobName}"]["steps"].select { |s| s["run"] }.map { |s| s["run"] }`));
    for (const runScript of steps) {
      assert.ok(!runScript.includes('${{'), `job=${jobName} 的 run 腳本不應直接內嵌 \${{ }}（找到：${runScript.slice(0, 80)}）`);
    }
  }
});

check('upload-artifact 都有 if: always() 且 if-no-files-found: error（不得用 warn 靜默放過）', () => {
  for (const jobName of ['smoke', 'gates', 'scenarios']) {
    const uploadSteps = JSON.parse(
      rubyEval(`y["jobs"]["${jobName}"]["steps"].select { |s| (s["uses"] || "").include?("upload-artifact") }`),
    );
    assert.equal(uploadSteps.length, 1, `job=${jobName} 應該剛好一個 upload-artifact step`);
    assert.equal(uploadSteps[0]['if'], 'always()');
    assert.equal(uploadSteps[0]['with']['if-no-files-found'], 'error');
  }
});

console.log(`\n${passed} passed, ${failed} failed`);
console.log('\n--- 不能做到的檢查（明列，不假裝通過）---');
console.log('- 未驗證 GitHub Actions 自己的 expression／schema 語意（例如 ${{ }} 運算式文法、action input 是否合法、on.pull_request.types 是否為 GitHub 認可的列舉值）——Ruby Psych 只做 YAML 語法／結構層級的解析。');
console.log('- 未驗證 pinned action SHA 對應的 tag/版本是否仍然真實存在於對應 repo（只核對本檔案引用的 SHA 字串與 ci.yml 既有引用相同）。');
console.log('- 未經任何遠端 dry-run 或 `act` 之類工具驗證 workflow 實際可執行；本輪未貼過 label、未觸發過任何 run。');
console.log('- review round 4（R2 envelope）：未驗證 GitHub-hosted runner 上 GITHUB_SHA／GITHUB_REPOSITORY／GITHUB_RUN_ID／GITHUB_RUN_ATTEMPT／RUNNER_OS／ImageOS／ImageVersion 這些「預設環境變數」在 pull_request labeled 事件下的實際值與型別；未驗證 generate-ci-envelope.mjs 在真實 runner 上探測到的 Node/Go/Chrome 版本字串格式與離線 selftest 的假指令輸出是否一致；未驗證 actions/checkout 預設簽出 test-merge commit 這個假設（checkoutHeadSha==testMergeSha）在這個 pinned checkout action 版本下是否成立。');
if (failed > 0) process.exitCode = 1;
