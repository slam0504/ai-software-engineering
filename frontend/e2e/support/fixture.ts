// Fixture workspace：每次執行用 mkdtemp 建一個全新目錄，模擬 WORKBENCH_WORKSPACE
// 指到的使用者工作區（app.go resolveWorkspace 的 "env" 候選）。
//
// **只作用於 mkdtemp 出來的目錄**（§2.10）：不動任何 repo 內或 HOME 下的檔案。
import { execFileSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

export interface Fixture {
  root: string; // realpath
  glossaryPath: string;
}

const GLOSSARY_TEMPLATE = '# Glossary\n\n- term: definition\n';

export function createFixture(): Fixture {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'b3a1-e2e-'));
  const root = fs.realpathSync(tmp); // macOS /tmp 實際是 /private/tmp（F 系列教訓，見設計 §2.2）

  const specDir = path.join(root, 'spec');
  fs.mkdirSync(specDir, { recursive: true });
  const glossaryPath = path.join(specDir, 'glossary.md');
  fs.writeFileSync(glossaryPath, GLOSSARY_TEMPLATE);

  const git = (...args: string[]) => execFileSync('git', args, { cwd: root, stdio: 'pipe' });
  git('init', '-q');
  git('config', 'user.email', 'b3a1-e2e@example.invalid');
  git('config', 'user.name', 'B3a-1 E2E Harness');
  git('add', '-A');
  git('commit', '-q', '-m', 'b3a-1 e2e fixture: initial commit');

  return { root, glossaryPath };
}

export function removeFixture(root: string): void {
  fs.rmSync(root, { recursive: true, force: true });
}
