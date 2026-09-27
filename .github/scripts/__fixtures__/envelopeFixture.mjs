// envelopeFixture.mjs — review round 4（#480 R2 剩餘缺口）：供各
// selftest 建構「合法的可信 CI invocation envelope」測試替身，單一權威來
// 源，避免每個 selftest 檔各自維護一份容易漂移的 schema 副本。
//
// checkoutHeadSha／nodeVersionActual 用「這次跑 selftest 當下」的真實值
// （這個 repo/worktree 實際的 HEAD、目前執行的 Node），因為
// evaluate-e2e-evidence.mjs 的 R2 交叉核對就是拿這兩個值跟 evaluator 自己
// 現場重新量測的結果比對——測試用憑空捏造的值一定會被抓到不符，唯有這兩項
// 用「當下真值」才組得出「完全合法」的 green case。其餘欄位（PR SHA／
// repository／workflow run id 等）是 offline selftest 模擬的 GitHub
// context 值，跟真實遠端無關，用穩定的合成佔位字串即可。
import { execFileSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { REQUIRED_TOOL_VERSIONS } from '../ci-e2e-entries.mjs';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
// __fixtures__/ -> scripts/ -> .github/ -> repo root
const REPO_ROOT = path.resolve(__dirname, '..', '..', '..');

export function envelopeRepoRoot() {
  return REPO_ROOT;
}

export function realCheckoutHeadSha(repoRoot = REPO_ROOT) {
  return execFileSync('git', ['-C', repoRoot, 'rev-parse', 'HEAD'], { encoding: 'utf8' }).trim();
}

export function realCheckoutTreeSha(repoRoot = REPO_ROOT) {
  return execFileSync('git', ['-C', repoRoot, 'rev-parse', 'HEAD^{tree}'], { encoding: 'utf8' }).trim();
}

const SYNTHETIC_SHA_A = '1111111111111111111111111111111111111111';
const SYNTHETIC_SHA_B = '2222222222222222222222222222222222222222';

/** 合法的 job-level envelope（不含 entry 專屬戳記欄位）。 */
export function makeBaseEnvelope({ repoRoot = REPO_ROOT } = {}) {
  const headSha = realCheckoutHeadSha(repoRoot);
  return {
    testMergeSha: headSha,
    prHeadSha: SYNTHETIC_SHA_A,
    prBaseSha: SYNTHETIC_SHA_B,
    repository: 'offline-selftest/repo',
    workflowRunId: '123456789',
    workflowRunAttempt: '1',
    runnerOs: 'macOS',
    imageOs: 'macos-15',
    imageVersion: '(offline-selftest)',
    checkoutHeadSha: headSha,
    // review round 5（#483 直接裁定）：Wails／Playwright／checkout tree 補入
    // envelope 必要欄位。checkoutTreeSha 用「這次跑 selftest 當下」的真實
    // 值（跟 checkoutHeadSha 同一個理由：目前沒有 evaluator 端交叉核對，
    // 但用真值仍比合成值更貼近正式路徑）；Wails／Playwright 版本沒有交叉
    // 核對（跟 Go／Chrome 同待遇，見 evaluate-e2e-evidence.mjs 只做 schema
    // 核對），用穩定合成字串即可。
    checkoutTreeSha: realCheckoutTreeSha(repoRoot),
    // review round 6（#489 F3）：這四個欄位現在要對得上
    // ci-e2e-entries.mjs REQUIRED_TOOL_VERSIONS（正常 CI 核定版本），不再
    // 是任意合成字串——nodeVersionActual 用固定的核定常數（不是
    // process.version：本機 Node 版本可能跟正常 CI 核定版本不同，呼叫端
    // 的 selftest 需要對應設定 CI_E2E_SELFTEST_ACTUAL_NODE_VERSION 讓
    // checkEnvelopeNodeVersion 的自我一致性核對跟這裡的固定值一致，見各
    // selftest 的 runEvaluator／runBatch 共用 env）。
    nodeVersionActual: REQUIRED_TOOL_VERSIONS.node,
    goVersionActual: `go version ${REQUIRED_TOOL_VERSIONS.go} darwin/amd64 (offline-selftest)`,
    chromeVersionActual: 'Google Chrome 999.0.0.0 (offline-selftest)',
    wailsVersionActual: `${REQUIRED_TOOL_VERSIONS.wails} (offline-selftest)`,
    playwrightVersionActual: `Version ${REQUIRED_TOOL_VERSIONS.playwright} (offline-selftest)`,
    generatedAtIso: '2026-09-28T00:00:00.000Z',
  };
}

/** 合法的、蓋好 entry 專屬戳記的最終 envelope（entry 需有 id/configPath/env）。 */
export function makeStampedEnvelope(entry, runId, overrides = {}, { repoRoot = REPO_ROOT } = {}) {
  return {
    ...makeBaseEnvelope({ repoRoot }),
    entryId: entry.id,
    configPath: entry.configPath,
    selectedEnv: entry.env,
    runId,
    ...overrides,
  };
}
