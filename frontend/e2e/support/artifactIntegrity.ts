// Q3（reviewer 二次審查，2026-09-15）：受版控產物的執行前後檢查。
//
// 背景：`wails dev` 會在重新產生 bindings／runtime 時把
// `frontend/wailsjs/runtime/` 底下幾個受版控的檔案 mode 從 644 改成 755
// （內容不變）。先前只記錄成「已知限制」，reviewer 這次要求改成主動檢查：
// 每次執行的**執行前**與**執行後**都要核對受版控產物的內容與可執行位元
// （mode），有任何變動就讓這次執行失敗、保留差異並提示——**不**在 teardown
// 自動還原（那是修改受版控檔案的行為，還原與否交給人／reviewer 決定）。
//
// 規則（reviewer 明確指定）：
//   - mode：一律跟 HEAD 記錄的 mode 比較（git 只存兩種：100644／100755），
//     不管內容有沒有跟 HEAD 不同——mode 意外變成 755 這件事本身永遠是錯的，
//     不能因為檔案本來就有正當的內容改動就放過 mode 檢查。
//   - 內容：如果檔案本來就跟 HEAD 不同（這張票本身的正當改動，例如
//     `frontend/package.json`），不能拿 HEAD 當基準（那樣會一直誤判）；改成
//     只比對「這次執行前」跟「這次執行後」是否相同——執行本身不該去動這些
//     檔案的內容，不管它們原本是不是已經跟 HEAD 不同。
//
// 檢查範圍（README「Browser E2E」段與這裡的 SCOPE_PATHS 常數要保持一致）：
// `frontend/wailsjs/**`（受版控的部分）、`go.mod`、`go.sum`、
// `frontend/package.json`、`frontend/package-lock.json`、
// `frontend/package.json.md5`。
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';

export const SCOPE_PATHS = [
  'frontend/wailsjs',
  'go.mod',
  'go.sum',
  'frontend/package.json',
  'frontend/package-lock.json',
  'frontend/package.json.md5',
];

export interface FileState {
  path: string; // repo-relative（git 慣用的正斜線）
  mode: string | null; // 三碼 8 進位（'644'／'755'），null＝working tree 找不到這個檔案
  contentHash: string | null; // `git hash-object` 的 blob hash，null＝working tree 找不到這個檔案
}

export interface ArtifactViolation {
  path: string;
  kind: 'mode-vs-head' | 'content-changed-during-run' | 'file-missing';
  detail: string;
}

function git(repoRoot: string, args: string[]): string {
  return execFileSync('git', args, { cwd: repoRoot, encoding: 'utf8', stdio: 'pipe' });
}

// listScopedFiles：用 `git ls-files` 展開 SCOPE_PATHS（含目錄），只列受版控
// 的檔案——未受版控／已經被刪除的不在檢查範圍內（沒有 git 基準可比）。
export function listScopedFiles(repoRoot: string): string[] {
  const out = git(repoRoot, ['ls-files', '--', ...SCOPE_PATHS]);
  return out.split('\n').map(l => l.trim()).filter(Boolean);
}

function headMode(repoRoot: string, relPath: string): string | null {
  // `git ls-tree HEAD -- <path>` → "100644 blob <hash>\t<path>"；沒有這個檔案
  // 時輸出是空字串。
  const out = git(repoRoot, ['ls-tree', 'HEAD', '--', relPath]).trim();
  if (!out) return null;
  const m = /^(\d+)\s+blob\s/.exec(out);
  return m ? m[1].slice(-3) : null;
}

function currentMode(repoRoot: string, relPath: string): string | null {
  const full = path.join(repoRoot, relPath);
  if (!fs.existsSync(full)) return null;
  return (fs.statSync(full).mode & 0o777).toString(8).padStart(3, '0');
}

function currentContentHash(repoRoot: string, relPath: string): string | null {
  const full = path.join(repoRoot, relPath);
  if (!fs.existsSync(full)) return null;
  return git(repoRoot, ['hash-object', full]).trim();
}

export function snapshotArtifacts(repoRoot: string, files: string[]): FileState[] {
  return files.map(f => ({ path: f, mode: currentMode(repoRoot, f), contentHash: currentContentHash(repoRoot, f) }));
}

// checkModeAgainstHead：mode 永遠跟 HEAD 比，不管內容跟 HEAD 是否一致。
export function checkModeAgainstHead(repoRoot: string, files: string[]): ArtifactViolation[] {
  const violations: ArtifactViolation[] = [];
  for (const f of files) {
    const head = headMode(repoRoot, f);
    if (head === null) continue; // HEAD 沒有這個檔案（例如尚未 commit 的新檔），mode 沒有基準可比。
    const cur = currentMode(repoRoot, f);
    if (cur === null) {
      violations.push({ path: f, kind: 'file-missing', detail: `working tree 找不到這個檔案（HEAD mode=${head}）` });
      continue;
    }
    if (cur !== head) {
      violations.push({ path: f, kind: 'mode-vs-head', detail: `目前 mode=${cur}，HEAD 記錄的 mode=${head}` });
    }
  }
  return violations;
}

// diffContentBeforeAfter：內容只比對「執行前」跟「執行後」，不管跟 HEAD 是否
// 一致——這次執行本身不該去動這些檔案的內容。
export function diffContentBeforeAfter(before: FileState[], after: FileState[]): ArtifactViolation[] {
  const violations: ArtifactViolation[] = [];
  const afterByPath = new Map(after.map(s => [s.path, s]));
  for (const b of before) {
    const a = afterByPath.get(b.path);
    if (!a) { violations.push({ path: b.path, kind: 'file-missing', detail: '執行後這個檔案在快照裡消失了' }); continue; }
    if (b.contentHash !== a.contentHash) {
      violations.push({
        path: b.path,
        kind: 'content-changed-during-run',
        detail: `content hash 在這次執行期間從 ${b.contentHash ?? '(不存在)'} 變成 ${a.contentHash ?? '(不存在)'}`,
      });
    }
  }
  return violations;
}
