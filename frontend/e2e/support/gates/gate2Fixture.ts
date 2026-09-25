// B3a-2a：Gate 2 browser fixture builder（design v4 §7）。在既有
// support/fixture.ts 建立的 fixture root 之上，追加 spec/plan 內容並操作
// git commit——鏡射 support/fixture.ts 的 git helper 寫法（execFileSync +
// cwd 綁定），純 fs／git 操作，不依賴 Playwright、不從待驗 App 輸出反填
// 期望值。
//
// **單一 plan 文件紀律**（design v4 §7.4，對照 app.go:4210-4247
// worktreePlanDoc）：呼叫端在寫下一案的 plan/<planID>.yaml 之前，必須先
// removePlanDoc() 移除上一案的候選文件，任一時刻 `plan/` 底下最多一份可
// 解析出非空 PlanID 的候選——這裡只提供操作原語，順序紀律由呼叫端
// （gate2.spec.ts）依 design v4 §7.1 的固定順序執行。
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { hashBytes } from './canonicalDigest.js';

export function gitFor(root: string): (...args: string[]) => string {
  return (...args: string[]) => execFileSync('git', args, { cwd: root, stdio: 'pipe' }).toString('utf8');
}

/** SpecScope.Match 等價（對照 internal/spec/manifest.go specInScope）。 */
export function specInScope(rel: string): boolean {
  const r = rel.replace(/^\.\//, '');
  if (r === 'spec/glossary.md') return true;
  return ['spec/features/', 'spec/nfr/', 'spec/context-map/'].some(dir => r.startsWith(dir));
}

/** PlanScope.Match 等價（對照 internal/spec/scope.go:32-35）。 */
export function planInScope(rel: string): boolean {
  const r = rel.replace(/^\.\//, '');
  return r === 'plan' || r.startsWith('plan/');
}

/** permissionManifestScope.Match 等價（對照 app.go:4264 附近）。 */
export function permissionInScope(rel: string): boolean {
  const r = rel.replace(/^\.\//, '');
  return r.startsWith('plan/permissions/');
}

export interface CommittedEntry { path: string; sha256: string; }

/**
 * 對指定 commit 下、符合 match 的檔案，用 `git ls-tree -r --name-only` +
 * `git show <commit>:<path>` 重建 FileEntry 陣列（對照 internal/spec/gitrepo.go
 * ReadScopedHeadTree 的步驟，design v4 §7.3）。
 *
 * **只用於 SUBMIT 當下的期望值重算**（例如 §7.1 的 C1／C2 binding 期望
 * 值）。**不要**拿它來驗證 S-N1「持續重算後 digest 是否不變」——那個持續
 * 重算的 production 端來源不是固定 commit，是下面 `readCurrentScopedEntries`
 * 對應的**worktree**（review #2 必修缺陷 3：用固定 commit 重算永遠得到同一
 * 個結果，驗不到任何東西）。
 */
export function readScopedEntriesAtCommit(root: string, commit: string, match: (rel: string) => boolean): CommittedEntry[] {
  const git = gitFor(root);
  const names = git('ls-tree', '-r', '--name-only', commit).split('\n').filter(Boolean);
  const out: CommittedEntry[] = [];
  for (const rel of names) {
    if (!match(rel)) continue;
    const content = execFileSync('git', ['show', `${commit}:${rel}`], { cwd: root });
    out.push({ path: rel, sha256: hashBytes(content) });
  }
  return out;
}

/** SpecScope.Roots 等價（對照 internal/spec/commit.go:29 managedScopeRoots）。 */
export const SPEC_SCOPE_ROOTS = ['spec/features', 'spec/nfr', 'spec/glossary.md', 'spec/context-map'];
/** PlanScope.Roots 等價（對照 internal/spec/scope.go:31）。 */
export const PLAN_SCOPE_ROOTS = ['plan'];

/**
 * `internal/spec/gitrepo.go` `GitRepo.ReadScopedWorktree` 的 TS 等價
 * ——production 的 `currentSpecManifest`／`currentPlanManifest`（app.go
 * `ensureGate()` 建立的閉包，約 3880 行：`spec.BuildCurrentManifest`／
 * `BuildCurrentManifestScoped`）持續重算時讀的就是這個函式，**不是**固定
 * commit 的 blob（review #2 必修缺陷 3）。用 `git ls-files --cached
 * --others --exclude-standard -- <roots>` 列舉「git 認為在範圍內」的檔案
 * （tracked 加上未忽略的 untracked），再從磁碟讀取內容。
 */
export function readCurrentScopedEntries(root: string, roots: string[], match: (rel: string) => boolean): CommittedEntry[] {
  const git = gitFor(root);
  const out = git('ls-files', '--cached', '--others', '--exclude-standard', '-z', '--', ...roots);
  const names = out.split('\0').filter(Boolean);
  const entries: CommittedEntry[] = [];
  for (const rel of names) {
    if (!match(rel)) continue;
    const abs = path.join(root, rel);
    if (!fs.existsSync(abs)) continue; // tracked（--cached）但已從 worktree 刪除——不算現存內容
    entries.push({ path: rel, sha256: hashBytes(fs.readFileSync(abs)) });
  }
  return entries;
}

/**
 * `worktreeRiskPolicyDigest`（app.go，`currentRiskPolicyDigest` 閉包）的 TS
 * 等價：直接從磁碟讀 `plan/risk-policy.yaml`（不經 git blob）。
 */
export function readCurrentRiskPolicyRaw(root: string): Buffer {
  return fs.readFileSync(path.join(root, 'plan', 'risk-policy.yaml'));
}

/**
 * `worktreePlanDoc`（app.go:4210-4247）的簡化 TS 等價——只服務本 fixture
 * 受控情境（§7.4 單一文件紀律保證任一時刻 `plan/` 下最多一份候選），不是
 * 通用 YAML 解析器：用檔名排除 `risk-policy.yaml`，斷言恰好一份候選。
 */
export function findCurrentPlanDocPath(root: string): string {
  const dir = path.join(root, 'plan');
  const candidates = fs.readdirSync(dir).filter(f => f.endsWith('.yaml') && f !== 'risk-policy.yaml');
  if (candidates.length !== 1) {
    throw new Error(`findCurrentPlanDocPath: plan/ 底下應恰好一份候選 plan 文件，實際 ${candidates.length} 份：${candidates.join(', ')}`);
  }
  return path.join(dir, candidates[0]);
}

/**
 * production 的 `full := "plan/" + ref` 解析式（`app.go` `permissionRefEntries`，
 * 約 4271-4297 行）——**review #3 必修缺陷 3 的契約來源**：`ref` 已經是相
 * 對於 `plan/` 的完整路徑（例如 `"permissions/T1.yaml"`，見
 * `PlanTaskYaml.permissionsRef` 的契約），不需要（也不能）再另外拼一層
 * `permissions/`。
 */
export function permissionRefFullPath(ref: string): string {
  return `plan/${ref}`;
}

/**
 * 依「task 實際引用的檔案」重建 permission entries——鏡射 `app.go`
 * `permissionRefEntries`：去重（用 `full` 路徑當 key）、`path` 欄位固定是
 * `"plan/"+ref`、內容經 `readBlob` 取得後 hash。**不是目錄 glob**（review #3
 * 必修缺陷 3：舊版用 `plan/permissions/**` glob，恰好在單純 fixture 下跟
 * 「實際引用檔案」重合，但這不是 production 實際算法，換一個目錄底下有
 * 多餘檔案的情境就會算錯）。
 */
function entriesFromPermissionRefs(refs: readonly string[], readBlob: (fullPathRelToRoot: string) => Buffer): CommittedEntry[] {
  const seen = new Set<string>();
  const out: CommittedEntry[] = [];
  for (const ref of refs) {
    const full = permissionRefFullPath(ref);
    if (seen.has(full)) continue;
    seen.add(full);
    out.push({ path: full, sha256: hashBytes(readBlob(full)) });
  }
  return out;
}

/** `permissionRefEntries` 的「固定 commit」版本——供 SUBMIT-time（C2）期望值重算使用。 */
export function permissionEntriesAtCommit(root: string, commit: string, refs: readonly string[]): CommittedEntry[] {
  return entriesFromPermissionRefs(refs, full => execFileSync('git', ['show', `${commit}:${full}`], { cwd: root }));
}

/** `permissionRefEntries` 的「current worktree」版本——供 S-N1 持續重算比對使用。 */
export function currentPermissionEntriesForRefs(root: string, refs: readonly string[]): CommittedEntry[] {
  return entriesFromPermissionRefs(refs, full => fs.readFileSync(path.join(root, full)));
}

/**
 * `worktreePermissionManifestDigest`（app.go:4304-4316）的 TS 等價：找出
 * `findCurrentPlanDocPath` 選出的當前候選 plan 文件、抽出每個 task 的
 * `permissions_ref`，交給 `currentPermissionEntriesForRefs`（review #3
 * 必修缺陷 3：不再自己多拼一層 `permissions/`）。
 */
export function readCurrentPermissionManifestEntries(root: string): CommittedEntry[] {
  const planDocPath = findCurrentPlanDocPath(root);
  const content = fs.readFileSync(planDocPath, 'utf8');
  const refs = [...content.matchAll(/permissions_ref:\s*"([^"]+)"/g)].map(m => m[1]);
  return currentPermissionEntriesForRefs(root, refs);
}

/**
 * `gate2.go ReconcileBindings` 對 `base_commit` binding 的持續檢查等價
 * （internal/gatepolicy/gate2.go 約 231-244 行）：不是比對 digest 相等，是
 * 用 `git rev-parse --verify --quiet <sha>^{commit}` 確認該 commit 物件仍
 * 存在——HEAD 前移不影響它（「歷史錨點」語意）。回傳 true 表示仍存在（不
 * 會被判 stale）。
 */
export function commitStillExists(root: string, sha: string): boolean {
  try {
    execFileSync('git', ['rev-parse', '--verify', '--quiet', `${sha}^{commit}`], { cwd: root, stdio: 'pipe' });
    return true;
  } catch {
    return false;
  }
}

/** 新增 spec/features/<name>.feature，含一個帶 @tag 的 scenario（供 plan 的 scenarios 欄位引用）。 */
export function writeFeatureFile(root: string, name: string, scenarioTag: string): void {
  const dir = path.join(root, 'spec', 'features');
  fs.mkdirSync(dir, { recursive: true });
  const content = `Feature: ${name}\n\n  @${scenarioTag}\n  Scenario: ${scenarioTag}\n    Given a precondition\n    When an action happens\n    Then an outcome is observed\n`;
  fs.writeFileSync(path.join(dir, `${name}.feature`), content);
}

export interface RiskPolicyYaml {
  defaultTier: 'low' | 'medium' | 'high';
}

export function writeRiskPolicy(root: string, policy: RiskPolicyYaml): void {
  fs.mkdirSync(path.join(root, 'plan'), { recursive: true });
  const content = `version: 1\ndefault_tier: ${policy.defaultTier}\nrules: []\n`;
  fs.writeFileSync(path.join(root, 'plan', 'risk-policy.yaml'), content);
}

export interface PlanTaskYaml {
  id: string;
  /**
   * review #3 必修缺陷 2：`internal/plan/validate.go:42-43`
   * （`if t.Title == "" { errs = append(errs, ...title must not be empty...) }`）
   * 規定 title 不可為空——builder 先前漏掉這個必填欄位，讓正式 Go
   * `plan.Validate` 對「成功」fixture 也回報錯誤，污染所有依賴它的案例。
   */
  title: string;
  scenarios: string[];
  dependsOn?: string[];
  minimumRiskTier: string;
  plannerRiskTier: string;
  /**
   * review #3 必修缺陷 3：`app.go`（`permissionRefEntries`，約
   * 4271-4297 行）用 `full := "plan/" + ref` 解析路徑——`ref` 必須是
   * **相對於 `plan/` 的完整路徑**（例如 `"permissions/T1.yaml"`），不是
   * 檔名本身（例如 `"T1.yaml"`，那樣會解析成 `plan/T1.yaml`，不是實際檔案
   * 所在的 `plan/permissions/T1.yaml`）。
   */
  permissionsRef: string;
}

export function writePlanDoc(root: string, planId: string, tasks: PlanTaskYaml[], analysisBaseCommit: string): void {
  fs.mkdirSync(path.join(root, 'plan'), { recursive: true });
  const taskYaml = tasks.map(t => [
    `  - id: ${t.id}`,
    `    title: ${JSON.stringify(t.title)}`,
    `    scenarios: [${t.scenarios.map(s => JSON.stringify(s)).join(', ')}]`,
    `    depends_on: [${(t.dependsOn ?? []).map(s => JSON.stringify(s)).join(', ')}]`,
    `    minimum_risk_tier: ${JSON.stringify(t.minimumRiskTier)}`,
    `    planner_risk_tier: ${JSON.stringify(t.plannerRiskTier)}`,
    `    permissions_ref: ${JSON.stringify(t.permissionsRef)}`,
  ].join('\n')).join('\n');
  const content = `plan_id: ${JSON.stringify(planId)}\nanalysis_base_commit: ${JSON.stringify(analysisBaseCommit)}\ntasks:\n${taskYaml}\n`;
  fs.writeFileSync(path.join(root, 'plan', `${planId}.yaml`), content);
}

/**
 * `ref` 是相對於 `plan/` 的完整路徑（例如 `"permissions/T1.yaml"`，對齊
 * `PlanTaskYaml.permissionsRef` 的契約，review #3 必修缺陷 3）——呼叫端不
 * 再只傳檔名，寫入位置是 `plan/<ref>`，與 production 的 `"plan/" + ref`
 * 解析式一致。
 */
export function writePermissionRef(root: string, ref: string, content = 'allow: []\n'): void {
  const full = path.join(root, 'plan', ref);
  fs.mkdirSync(path.dirname(full), { recursive: true });
  fs.writeFileSync(full, content);
}

/**
 * 依 design v4 §7.4 單一文件紀律移除上一案的候選 plan 文件——存在才刪，
 * 不存在（例如第一個案例）是 no-op，不 throw。
 */
export function removePlanDoc(root: string, planId: string): void {
  const p = path.join(root, 'plan', `${planId}.yaml`);
  if (fs.existsSync(p)) fs.rmSync(p);
}

/**
 * `git add -A -- <pathspecs> && git commit`——**review #2 必修缺陷 2 修
 * 正**：原本的 `commitAll` 用不帶 pathspec 的 `git add -A`，會把整個
 * worktree（含 App 執行期間寫入的 `.workbench/`——`gate.jsonl`／
 * `audit.jsonl`／`events.jsonl`／lock 檔等，App 的 stateDir 就在 fixture
 * root 下、fixture repo 沒有 `.gitignore` 也沒設 `info/exclude`）一併提
 * 交，讓 Gate 2 的 `plan.VerifyLineage(analysis_base_commit..plan_commit)`
 * （只允許 `plan/**` 範圍內的變動，`internal/plan/lineage.go:40` 起）在
 * C1→C2 之間偵測到 `.workbench/` 的變動而拒絕——G2-P1 與 stale 的 P1 前置
 * 都會因此失敗。
 *
 * 改為要求呼叫端明確傳入 pathspec（例如 `['spec/']`、`['plan/']`、
 * `['NOTES.md']`），`-A -- <pathspec>` 只在該 pathspec 範圍內偵測新增／
 * 修改／刪除並全部 stage（含刪除，覆蓋原本 `commitOnly` 需要的能力），不
 * 觸碰範圍外的任何路徑（含 `.workbench/` 與任何殘留檔）。**不使用**
 * `.gitignore`／`info/exclude`（會改變 fixture repo 設定，reviewer 明確排
 * 除的做法）。
 */
export function commitPaths(root: string, pathspecs: string[], message: string): string {
  if (pathspecs.length === 0) throw new Error('commitPaths: pathspecs 不得為空——每次 commit 都必須明確指定範圍，不允許隱含 -A 全庫');
  const git = gitFor(root);
  git('add', '-A', '--', ...pathspecs);
  git('commit', '-q', '-m', message);
  return git('rev-parse', 'HEAD').trim();
}

export function headSha(root: string): string {
  return gitFor(root)('rev-parse', 'HEAD').trim();
}
