// gate2Fixture.ts 的離線 smoke selftest——在真實 mkdtemp＋git（無 App／
// browser）下驗證 builder 操作序列本身正確：single-plan-doc 紀律
// （remove-before-write）、readScopedEntriesAtCommit 的 scope 過濾與內容
// hash、與 canonicalDigest.ts 的 specManifestDigest／planManifestDigest 組合
// 後的 digest 具備「同內容 → 同 digest，改內容 → 不同 digest」的基本性質、
// commitPaths 的 pathspec 範圍（review #2 必修缺陷 2：不得把 .workbench／
// root 殘留檔一併 stage 進 commit）、以及「current」系列函式（review #2 必
// 修缺陷 3：對齊 production 的 currentSpecManifest／currentPlanManifest／
// currentRiskPolicyDigest／currentPermissionManifest 讀的是 worktree，不是
// 固定 commit）。不驗證 App／journal／UI，那些留待 browser run。
//
// 執行：node --experimental-loader=./e2e/support/selftestJsToTsLoader.mjs e2e/support/gates/gate2Fixture.selftest.ts
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { specManifestDigest, planManifestDigest, permissionManifestDigest, singleFileDigest } from './canonicalDigest.js';
import {
  commitPaths, commitStillExists, currentPermissionEntriesForRefs, findCurrentPlanDocPath, gitFor, headSha,
  permissionEntriesAtCommit, permissionInScope, permissionRefFullPath, planInScope,
  readCurrentPermissionManifestEntries, readCurrentRiskPolicyRaw, readCurrentScopedEntries,
  readScopedEntriesAtCommit, removePlanDoc, specInScope, writeFeatureFile, writePermissionRef, writePlanDoc,
  writeRiskPolicy, PLAN_SCOPE_ROOTS, SPEC_SCOPE_ROOTS,
} from './gate2Fixture.js';

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

function makeGitRoot(): string {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'gate2fixture-selftest-'));
  const root = fs.realpathSync(tmp);
  execFileSync('git', ['init', '-q'], { cwd: root });
  execFileSync('git', ['config', 'user.email', 'gate2fixture-selftest@example.invalid'], { cwd: root });
  execFileSync('git', ['config', 'user.name', 'gate2Fixture selftest'], { cwd: root });
  fs.writeFileSync(path.join(root, '.gitkeep'), '');
  execFileSync('git', ['add', '-A'], { cwd: root });
  execFileSync('git', ['commit', '-q', '-m', 'init'], { cwd: root });
  return root;
}

/** 模擬 App 執行期間會在 fixture root 底下留下的 .workbench 狀態。 */
function simulateAppState(root: string): void {
  fs.mkdirSync(path.join(root, '.workbench'), { recursive: true });
  fs.writeFileSync(path.join(root, '.workbench', 'gate.jsonl'), '{"OpID":"op-1"}\n');
  fs.writeFileSync(path.join(root, '.workbench', 'audit.jsonl'), '{"kind":"harmless"}\n');
}

// ---- scope Match 等價 ----
check('specInScope：spec/features/** 與 spec/glossary.md 命中，其他不命中', () => {
  assert.equal(specInScope('spec/features/a.feature'), true);
  assert.equal(specInScope('spec/glossary.md'), true);
  assert.equal(specInScope('spec/README.md'), false);
  assert.equal(specInScope('plan/foo.yaml'), false);
});

check('planInScope：plan/** 全部命中，非 plan/ 不命中', () => {
  assert.equal(planInScope('plan/risk-policy.yaml'), true);
  assert.equal(planInScope('plan/permissions/T1.yaml'), true);
  assert.equal(planInScope('spec/glossary.md'), false);
});

check('permissionInScope：只有 plan/permissions/** 命中', () => {
  assert.equal(permissionInScope('plan/permissions/T1.yaml'), true);
  assert.equal(permissionInScope('plan/risk-policy.yaml'), false);
});

// ---- readScopedEntriesAtCommit + canonicalDigest 組合（固定 commit，SUBMIT 期望值用） ----
check('spec_manifest：writeFeatureFile+commit 後，readScopedEntriesAtCommit 只挑出 spec/features/** 內容，digest 對「同內容再算一次」保持一致', () => {
  const root = makeGitRoot();
  writeFeatureFile(root, 'checkout', 'scenario-ok');
  const c1 = commitPaths(root, ['spec/'], 'add feature');
  const entries = readScopedEntriesAtCommit(root, c1, specInScope);
  assert.equal(entries.length, 1);
  assert.equal(entries[0].path, 'spec/features/checkout.feature');
  const d1 = specManifestDigest(entries);
  const d2 = specManifestDigest(readScopedEntriesAtCommit(root, c1, specInScope));
  assert.equal(d1, d2, '同一個 commit 重算兩次應得到相同 digest');
});

check('負案例：spec 內容改變後，重算 digest 必須不同（不是恆等函式，也沒有快取污染）', () => {
  const root = makeGitRoot();
  writeFeatureFile(root, 'checkout', 'scenario-ok');
  const c1 = commitPaths(root, ['spec/'], 'v1');
  const d1 = specManifestDigest(readScopedEntriesAtCommit(root, c1, specInScope));
  writeFeatureFile(root, 'checkout', 'scenario-changed');
  const c2 = commitPaths(root, ['spec/'], 'v2');
  const d2 = specManifestDigest(readScopedEntriesAtCommit(root, c2, specInScope));
  assert.notEqual(d1, d2);
});

// ---- 單一 plan 文件紀律：remove-before-write ----
check('removePlanDoc + writePlanDoc：任一時刻 plan/ 底下只有一份 plan_id 候選（依檔名判斷）', () => {
  const root = makeGitRoot();
  writeRiskPolicy(root, { defaultTier: 'medium' });
  writePlanDoc(root, 'plan-a', [{ id: 'T1', title: 'Task T1', scenarios: ['scenario-ok'], minimumRiskTier: 'medium', plannerRiskTier: 'medium', permissionsRef: 'permissions/T1.yaml' }], 'deadbeef');
  writePermissionRef(root, 'permissions/T1.yaml');
  const c1 = commitPaths(root, ['plan/'], 'plan-a');
  let planFiles = fs.readdirSync(path.join(root, 'plan')).filter(f => f.endsWith('.yaml') && f !== 'risk-policy.yaml');
  assert.deepEqual(planFiles, ['plan-a.yaml']);

  removePlanDoc(root, 'plan-a');
  writePlanDoc(root, 'plan-b', [{ id: 'T1', title: 'Task T1', scenarios: ['scenario-ok'], minimumRiskTier: 'medium', plannerRiskTier: 'medium', permissionsRef: 'permissions/T1.yaml' }], c1);
  commitPaths(root, ['plan/'], 'plan-b');
  planFiles = fs.readdirSync(path.join(root, 'plan')).filter(f => f.endsWith('.yaml') && f !== 'risk-policy.yaml');
  assert.deepEqual(planFiles, ['plan-b.yaml'], 'plan-a.yaml 必須已被移除，plan/ 底下只剩 plan-b.yaml 一份候選');
});

check('removePlanDoc 對不存在的檔案是 no-op，不 throw（第一個案例沒有「上一份」可刪）', () => {
  const root = makeGitRoot();
  assert.doesNotThrow(() => removePlanDoc(root, 'never-existed'));
});

// ---- commitPaths：pathspec 範圍紀律（review #2 必修缺陷 2） ----
check('commitPaths：只提交指定 pathspec，root 下其他 untracked 檔案不會被一併加入', () => {
  const root = makeGitRoot();
  fs.writeFileSync(path.join(root, 'NOTES.md'), 'probe head-move file\n');
  fs.writeFileSync(path.join(root, 'UNRELATED.tmp'), 'should not be committed\n');
  commitPaths(root, ['NOTES.md'], 'head move: add NOTES.md only');
  const git = gitFor(root);
  const status = git('status', '--porcelain');
  assert.match(status, /\?\? UNRELATED\.tmp/, 'UNRELATED.tmp 應仍是 untracked（沒有被 commitPaths 一併加入）');
  const tracked = git('ls-tree', '-r', '--name-only', 'HEAD');
  assert.match(tracked, /NOTES\.md/);
  assert.doesNotMatch(tracked, /UNRELATED\.tmp/);
});

check('負案例（review #2 必修缺陷 2 核心紅燈）：commitPaths(["spec/"]) 不得把 .workbench/ 或 root 殘留檔一併 stage', () => {
  const root = makeGitRoot();
  simulateAppState(root);
  fs.writeFileSync(path.join(root, 'RESIDUAL.tmp'), 'leftover from a previous step\n');
  writeFeatureFile(root, 'checkout', 'scenario-ok');
  const c1 = commitPaths(root, ['spec/'], 'spec only');
  const git = gitFor(root);
  const tracked = git('ls-tree', '-r', '--name-only', c1);
  assert.doesNotMatch(tracked, /\.workbench/, 'commit 內容不得含 .workbench/**');
  assert.doesNotMatch(tracked, /RESIDUAL\.tmp/, 'commit 內容不得含 root 殘留檔');
  assert.match(tracked, /spec\/features\/checkout\.feature/, 'commit 內容應含本次新增的 spec 檔');
  // .workbench／殘留檔仍應是 untracked（沒被動過）。
  const status = git('status', '--porcelain');
  assert.match(status, /\?\? \.workbench\//);
  assert.match(status, /\?\? RESIDUAL\.tmp/);
});

check('commitPaths：git add -A -- <pathspec> 連刪除也能 stage（移除 plan 候選後 commit，diff 應含刪除記錄）', () => {
  const root = makeGitRoot();
  writeRiskPolicy(root, { defaultTier: 'medium' });
  writePlanDoc(root, 'plan-del', [{ id: 'T1', title: 'Task T1', scenarios: ['scenario-ok'], minimumRiskTier: 'medium', plannerRiskTier: 'medium', permissionsRef: 'permissions/T1.yaml' }], 'deadbeef');
  writePermissionRef(root, 'permissions/T1.yaml');
  commitPaths(root, ['plan/'], 'add plan-del');
  removePlanDoc(root, 'plan-del');
  const c2 = commitPaths(root, ['plan/'], 'remove plan-del');
  const git = gitFor(root);
  const diff = git('diff', '--name-status', `${c2}~1`, c2);
  assert.match(diff, /^D\tplan\/plan-del\.yaml$/m, 'plan-del.yaml 的刪除應被 stage 並出現在 diff 裡');
});

check('C1..C2 的 git diff --name-only 只含 plan/**（review #2 必修缺陷 2：模擬 App 在期間寫 .workbench）', () => {
  const root = makeGitRoot();
  writeFeatureFile(root, 'checkout', 'scenario-ok');
  simulateAppState(root);
  const c1 = commitPaths(root, ['spec/'], 'C1: spec only');
  // 模擬「App 在 C1 之後、C2 之前」持續往 .workbench 寫入（gate 核可、audit 等）。
  fs.appendFileSync(path.join(root, '.workbench', 'gate.jsonl'), '{"OpID":"op-2"}\n');
  writeRiskPolicy(root, { defaultTier: 'medium' });
  writePlanDoc(root, 'gate2-p1', [{ id: 'T1', title: 'Task T1', scenarios: ['scenario-ok'], minimumRiskTier: 'medium', plannerRiskTier: 'medium', permissionsRef: 'permissions/T1.yaml' }], c1);
  writePermissionRef(root, 'permissions/T1.yaml');
  const c2 = commitPaths(root, ['plan/'], 'C2: plan only');
  const git = gitFor(root);
  const changed = git('diff', '--name-only', c1, c2).split('\n').filter(Boolean);
  assert.ok(changed.length > 0, '應至少有 plan/ 底下的檔案變動');
  for (const f of changed) {
    assert.match(f, /^plan\//, `C1..C2 的變動檔案應全部落在 plan/** 之下，實際看到 ${f}`);
  }
});

// ---- permission_manifest scope（固定 commit） ----
check('permission_manifest：只挑出 plan/permissions/** 底下的內容，plan/risk-policy.yaml 不計入', () => {
  const root = makeGitRoot();
  writeRiskPolicy(root, { defaultTier: 'low' });
  writePermissionRef(root, 'permissions/T1.yaml', 'allow: [read]\n');
  const c1 = commitPaths(root, ['plan/'], 'perms');
  const entries = readScopedEntriesAtCommit(root, c1, permissionInScope);
  assert.equal(entries.length, 1);
  assert.equal(entries[0].path, 'plan/permissions/T1.yaml');
  assert.doesNotThrow(() => permissionManifestDigest(entries));
});

// ---- review #3（#427）必修缺陷 3：permission ref 解析與「引用檔案，非目錄 glob」 ----
check('permissionRefFullPath：production 的 "plan/" + ref 解析式', () => {
  assert.equal(permissionRefFullPath('permissions/T1.yaml'), 'plan/permissions/T1.yaml');
  assert.equal(permissionRefFullPath('T1.yaml'), 'plan/T1.yaml', 'ref 若只是裸檔名，會解析到 plan/T1.yaml——這正是 review #427 point 3 指出的錯誤根源，舊版 fixture 曾經寫成這樣');
});

check('permissionEntriesAtCommit：依 task 實際引用的檔案重建 entries（固定 commit），不是目錄 glob', () => {
  const root = makeGitRoot();
  writeRiskPolicy(root, { defaultTier: 'medium' });
  writePermissionRef(root, 'permissions/T1.yaml', 'allow: [read]\n');
  // 刻意在 plan/permissions/ 底下多放一個「沒有被任何 task 引用」的檔案——
  // 如果實作退化成目錄 glob，這個檔案會被誤算進 entries。
  writePermissionRef(root, 'permissions/UNREFERENCED.yaml', 'allow: []\n');
  const c1 = commitPaths(root, ['plan/'], 'perms');
  const entries = permissionEntriesAtCommit(root, c1, ['permissions/T1.yaml']);
  assert.deepEqual(entries.map(e => e.path), ['plan/permissions/T1.yaml'], '只應包含實際引用的檔案，UNREFERENCED.yaml 不應出現');
});

check('負案例：permissionEntriesAtCommit 對重複的 ref 去重（多個 task 共用同一份 permission 檔）', () => {
  const root = makeGitRoot();
  writeRiskPolicy(root, { defaultTier: 'medium' });
  writePermissionRef(root, 'permissions/shared.yaml');
  const c1 = commitPaths(root, ['plan/'], 'perms');
  const entries = permissionEntriesAtCommit(root, c1, ['permissions/shared.yaml', 'permissions/shared.yaml']);
  assert.equal(entries.length, 1, '重複 ref 應去重，不是各自算一筆');
});

check('currentPermissionEntriesForRefs：worktree 版本（不需要 commit）同樣依引用檔案而非目錄 glob', () => {
  const root = makeGitRoot();
  writeRiskPolicy(root, { defaultTier: 'medium' });
  writePermissionRef(root, 'permissions/T1.yaml');
  writePermissionRef(root, 'permissions/UNREFERENCED.yaml');
  const entries = currentPermissionEntriesForRefs(root, ['permissions/T1.yaml']);
  assert.deepEqual(entries.map(e => e.path), ['plan/permissions/T1.yaml']);
});

check('headSha：回傳 40 字元 SHA-1，與 git rev-parse HEAD 一致', () => {
  const root = makeGitRoot();
  const h = headSha(root);
  assert.match(h, /^[0-9a-f]{40}$/);
  const git = gitFor(root);
  assert.equal(h, git('rev-parse', 'HEAD').trim());
});

// ---- current 系列（review #2 必修缺陷 3：對齊 production 的持續重算來源） ----
check('readCurrentScopedEntries：未 commit 的 spec 內容也算現存（worktree-scoped，跟固定 commit 版本不同）', () => {
  const root = makeGitRoot();
  writeFeatureFile(root, 'checkout', 'scenario-ok');
  // 刻意不 commit——readCurrentScopedEntries 應仍看得到（因為 git ls-files
  // --others --exclude-standard 會列出未忽略的 untracked 檔）。
  const entries = readCurrentScopedEntries(root, SPEC_SCOPE_ROOTS, specInScope);
  assert.equal(entries.length, 1);
  assert.equal(entries[0].path, 'spec/features/checkout.feature');
});

check('readCurrentScopedEntries：commit 之後内容不變，重算應與固定 commit 版本得到相同 digest', () => {
  const root = makeGitRoot();
  writeFeatureFile(root, 'checkout', 'scenario-ok');
  const c1 = commitPaths(root, ['spec/'], 'v1');
  const committedDigest = specManifestDigest(readScopedEntriesAtCommit(root, c1, specInScope));
  const currentDigest = specManifestDigest(readCurrentScopedEntries(root, SPEC_SCOPE_ROOTS, specInScope));
  assert.equal(committedDigest, currentDigest, 'worktree 與 HEAD 內容一致時，兩種重算方式應得到相同 digest');
});

check('負案例：readCurrentScopedEntries 對 plan/ roots 不會把 spec/ 底下的檔案算進來', () => {
  const root = makeGitRoot();
  writeFeatureFile(root, 'checkout', 'scenario-ok');
  writeRiskPolicy(root, { defaultTier: 'medium' });
  const entries = readCurrentScopedEntries(root, PLAN_SCOPE_ROOTS, planInScope);
  assert.ok(entries.every(e => e.path.startsWith('plan/')), '不應混入 spec/ 底下的檔案');
  assert.ok(entries.some(e => e.path === 'plan/risk-policy.yaml'));
});

check('readCurrentRiskPolicyRaw：直接讀磁碟目前內容，不需要 commit', () => {
  const root = makeGitRoot();
  writeRiskPolicy(root, { defaultTier: 'high' });
  const raw = readCurrentRiskPolicyRaw(root);
  assert.match(raw.toString('utf8'), /default_tier: high/);
  assert.equal(singleFileDigest(raw).startsWith('sha256:'), true);
});

check('findCurrentPlanDocPath + readCurrentPermissionManifestEntries：找到唯一候選並讀出其 permission refs', () => {
  const root = makeGitRoot();
  writeRiskPolicy(root, { defaultTier: 'medium' });
  writePlanDoc(root, 'gate2-p1', [
    { id: 'T1', title: 'Task T1', scenarios: ['scenario-ok'], minimumRiskTier: 'medium', plannerRiskTier: 'medium', permissionsRef: 'permissions/T1.yaml' },
    { id: 'T2', title: 'Task T2', scenarios: ['scenario-ok'], minimumRiskTier: 'medium', plannerRiskTier: 'medium', permissionsRef: 'permissions/T2.yaml' },
  ], 'deadbeef');
  writePermissionRef(root, 'permissions/T1.yaml');
  writePermissionRef(root, 'permissions/T2.yaml');
  const docPath = findCurrentPlanDocPath(root);
  assert.equal(path.basename(docPath), 'gate2-p1.yaml');
  const entries = readCurrentPermissionManifestEntries(root);
  assert.deepEqual(entries.map(e => e.path).sort(), ['plan/permissions/T1.yaml', 'plan/permissions/T2.yaml']);
});

check('負案例：findCurrentPlanDocPath 在零份或多份候選時 throw（不猜、不選第一個）', () => {
  const root = makeGitRoot();
  fs.mkdirSync(path.join(root, 'plan'), { recursive: true });
  assert.throws(() => findCurrentPlanDocPath(root), /恰好一份候選/);
  writePlanDoc(root, 'a', [{ id: 'T1', title: 'Task T1', scenarios: [], minimumRiskTier: 'low', plannerRiskTier: 'low', permissionsRef: 'x.yaml' }], 'x');
  writePlanDoc(root, 'b', [{ id: 'T1', title: 'Task T1', scenarios: [], minimumRiskTier: 'low', plannerRiskTier: 'low', permissionsRef: 'x.yaml' }], 'x');
  assert.throws(() => findCurrentPlanDocPath(root), /恰好一份候選/);
});

check('commitStillExists：commit 存在時 true，任意 40 hex 字元但不存在的 SHA 時 false', () => {
  const root = makeGitRoot();
  const c1 = headSha(root);
  assert.equal(commitStillExists(root, c1), true);
  assert.equal(commitStillExists(root, 'deadbeef00deadbeef00deadbeef00deadbeef0'), false);
});

console.log(`\n${passed} passed, ${failed} failed`);
if (failed > 0) process.exitCode = 1;
