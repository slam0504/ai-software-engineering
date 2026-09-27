// selftestWorkroot.mjs — review round 3（#480 R6）：先前每支 selftest 各自
// 寫死 `/Users/eason_tseng/b3a-evidence/ci-1/attempt-001/selftest-workdirs`
// 當預設輸出目錄，任意第三方 checkout 這個 repo（換一台機器、換一個使用
// 者）沒有這個路徑的寫入權限就無法執行 selftest。改成統一從這裡解析：
//
//   1. `CI_E2E_SELFTEST_WORKROOT`（最高優先，明確指定的暫存目錄）
//   2. `CI_E2E_PERSISTENT_EVIDENCE_ROOT`（若設定，代表呼叫端要把這次結果
//      留在持久證據目錄下的 `selftest-workdirs/` 子目錄——這是本機
//      operator 才會用的旗標，不是可攜預設值）
//   3. `os.tmpdir()/b3a-ci1-selftest-workdirs`（真正的可攜預設值，任何機
//      器、任何使用者都能寫）
//
// `cleanupWorkRootIfPortable()` 在 selftest 結尾呼叫：只有在使用第 3 種
// （os.tmpdir() 底下的可攜預設路徑）**且本次全數通過**時才清掉整個
// workroot；用了持久證據路徑（第 1／2 種）或有任何失敗時，一律保留（失敗
// 證據不能因為「跑在暫存目錄」就被自動清掉）。
import { existsSync, mkdirSync, rmSync } from 'node:fs';
import os from 'node:os';
import path from 'node:path';

export function resolveSelftestWorkRoot() {
  const explicit = process.env.CI_E2E_SELFTEST_WORKROOT;
  if (explicit) return { root: explicit, portable: false };

  const persistentBase = process.env.CI_E2E_PERSISTENT_EVIDENCE_ROOT;
  if (persistentBase) return { root: path.join(persistentBase, 'selftest-workdirs'), portable: false };

  return { root: path.join(os.tmpdir(), 'b3a-ci1-selftest-workdirs'), portable: true };
}

export function ensureSelftestWorkRoot() {
  const resolved = resolveSelftestWorkRoot();
  mkdirSync(resolved.root, { recursive: true });
  return resolved;
}

/**
 * 只有 portable（os.tmpdir() 預設路徑）且全數通過時才清掉整個 workroot；
 * 否則保留（失敗證據、或使用者明確指定的持久路徑都不自動清）。
 */
export function cleanupWorkRootIfPortable(resolved, { failed }) {
  if (!resolved.portable) return;
  if (failed > 0) return;
  if (!existsSync(resolved.root)) return;
  try {
    rmSync(resolved.root, { recursive: true, force: true });
  } catch {
    // 清理失敗不影響 selftest 本身的判定結果，不吞掉但也不讓它變成誤判。
  }
}
