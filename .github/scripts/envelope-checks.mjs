#!/usr/bin/env node
// envelope-checks.mjs — B3a-CI-1 review round 5（#483 直接裁定：「job
// preflight目前只查非空字串，SHA/checkout/Node不符到entry跑完才驗...能在
// 啟動前判定的錯誤，移到共用驗證並先拒絕，不花runtime後才失敗」）。
//
// 先前 SHA 格式／checkout HEAD 交叉核對／Node 版本交叉核對只寫在
// evaluate-e2e-evidence.mjs 的 validateEnvelope() 內，只在每個 entry 真的
// 跑完之後才會被驗到——envelope 本身在 job 開始時就已經固定（不依賴任何一
// 個 entry 的執行結果），這些檢查其實可以、也應該在 run-batch.mjs 進入
// entries 迴圈之前就做一次。這裡抽成共用函式，run-batch.mjs（job-level
// preflight）與 evaluate-e2e-evidence.mjs（per-entry stamped envelope）都呼
// 叫同一份邏輯，避免兩處各自維護、逐漸漂移。
//
// 只做「不需要任何一次 entry 實際執行結果」的檢查；entry 專屬的
// entryId／configPath／selectedEnv／runId 核對留在
// evaluate-e2e-evidence.mjs（那些必須等這次 entry 執行完、有了 runDirName
// 之後才能核對）。
import { spawnSync } from 'node:child_process';
import { ENVELOPE_BASE_FIELDS, ENVELOPE_SHA_FIELDS, REQUIRED_TOOL_VERSIONS } from './ci-e2e-entries.mjs';

const TOOL_VERSION_ENVELOPE_FIELDS = {
  node: 'nodeVersionActual',
  go: 'goVersionActual',
  wails: 'wailsVersionActual',
  playwright: 'playwrightVersionActual',
};

// review round 7（#491 G3）：`actual.includes(requiredSubstring)` 會放行
// `go1.26.50`／`v2.13.00`／`Version1.63.01` 這類前綴碰撞（reviewer 已證
// 實）。這裡依各工具實際輸出格式，精確取出版本 token 再做**完全相等**比
// 對，不再用 includes。只正規化已知的 ANSI escape／前後空白，不引入新依
// 賴、不寫通用 semver 解析框架。
function stripAnsiAndTrim(raw) {
  if (typeof raw !== 'string') return raw;
  // eslint-disable-next-line no-control-regex
  return raw.replace(/\x1b\[[0-9;]*m/g, '').trim();
}

const TOOL_VERSION_EXTRACTORS = {
  // 固定格式：`go version go1.26.5 darwin/amd64`（Go 官方輸出）。
  go: (raw) => {
    const m = /^go version (\S+)/.exec(stripAnsiAndTrim(raw) ?? '');
    return m ? m[1] : null;
  },
  // Wails CLI 沒有查證過的固定包裝文字（REQUIRED_TOOL_VERSIONS 四個版本號
  // 本身也還沒有遠端核對過，見 attempt-004 progress.md 偏離事項）——用搜尋
  // 而非錨定開頭找第一個 `v<digits>.<digits>...` token，並把緊接在後面
  // （不含空白分隔）的字元一起收進來，避免「v2.13.00」「v2.13.0-rc1」被截
  // 斷成看似等於「v2.13.0」。
  wails: (raw) => {
    const m = /v\d+(?:\.\d+)+[A-Za-z0-9.+-]*/.exec(stripAnsiAndTrim(raw) ?? '');
    return m ? m[0] : null;
  },
  // Playwright CLI 固定格式：`Version 1.63.0`。
  playwright: (raw) => {
    const m = /^Version (\S+)/.exec(stripAnsiAndTrim(raw) ?? '');
    return m ? m[1] : null;
  },
  // generate-ci-envelope.mjs 的 nodeVersionActual 直接是 process.version
  // （見該檔 `const nodeVersionActual = process.version;`），本身就是純版
  // 號字串，不含其他包裝文字，不需要正則抽取。
  node: (raw) => {
    const s = stripAnsiAndTrim(raw);
    return s === '' ? null : s;
  },
};

export function checkEnvelopeSchema(envelope) {
  if (!envelope || typeof envelope !== 'object' || Array.isArray(envelope)) {
    return ['envelope 不是物件'];
  }
  const violations = [];
  for (const field of ENVELOPE_BASE_FIELDS) {
    if (typeof envelope[field] !== 'string' || envelope[field].length === 0) {
      violations.push(`缺欄位或型別錯誤：${field}（需為非空字串，實際：${JSON.stringify(envelope[field])}）`);
    }
  }
  return violations;
}

export function checkEnvelopeShaFormat(envelope) {
  const violations = [];
  for (const field of ENVELOPE_SHA_FIELDS) {
    if (typeof envelope[field] !== 'string' || !/^[0-9a-f]{40}$/i.test(envelope[field])) {
      violations.push(`${field}="${envelope[field]}" 不是合法 40-hex SHA`);
    }
  }
  return violations;
}

// pull_request 事件下 actions/checkout 預設簽出 test-merge commit
// （refs/pull/<PR>/merge），其 SHA 就是 github.sha／testMergeSha——checkout
// 之後的 HEAD 應該跟它相同，這是內部一致性檢查（不需要額外探測，兩個欄位
// 彼此就該相符）。
export function checkEnvelopeInternalConsistency(envelope) {
  const violations = [];
  if (
    typeof envelope.checkoutHeadSha === 'string'
    && typeof envelope.testMergeSha === 'string'
    && envelope.checkoutHeadSha.toLowerCase() !== envelope.testMergeSha.toLowerCase()
  ) {
    violations.push(
      `checkoutHeadSha="${envelope.checkoutHeadSha}" 與 testMergeSha="${envelope.testMergeSha}" 不一致`
        + '——pull_request 事件下 checkout 應該簽出 test-merge commit 本身，兩者應該相同',
    );
  }
  return violations;
}

// 與實際 checkout 交叉核對：現場重新查一次這個 repo 的 HEAD，不能只信
// envelope 自己宣稱的值。
export function checkEnvelopeAgainstRepoHead(envelope, repoRootForGitCheck) {
  const gitResult = spawnSync('git', ['-C', repoRootForGitCheck, 'rev-parse', 'HEAD'], { encoding: 'utf8' });
  if (gitResult.error || gitResult.status !== 0) {
    return [
      `自行執行 git rev-parse HEAD 失敗（-C ${repoRootForGitCheck}），無法核對 envelope.checkoutHeadSha 與實際 checkout 是否相符：`
        + `${gitResult.error?.message ?? (gitResult.stderr ?? '').trim()}`,
    ];
  }
  const actualHead = gitResult.stdout.trim();
  if (
    !/^[0-9a-f]{40}$/i.test(actualHead)
    || actualHead.toLowerCase() !== String(envelope.checkoutHeadSha).toLowerCase()
  ) {
    return [
      `envelope 的 checkoutHeadSha="${envelope.checkoutHeadSha}" 與實測的 git rev-parse HEAD="${actualHead}"`
        + `（-C ${repoRootForGitCheck}）不符——checkout 身分不可信，不能只信 envelope 自己宣稱的值`,
    ];
  }
  return [];
}

// Node 版本交叉核對：呼叫端就是在這個 job 的同一個 Node runtime 下執行，
// process.version 就是「當下實際生效的 Node 版本」，不需要另外 spawn 子行
// 程探測。
//
// review round 6（#489 F3）：`actualNodeVersion` 參數只給離線 selftest 用
// （明確、不會混入正常 CI 的測試邊界）——production 呼叫端（run-batch.mjs
// job-level preflight／evaluate-e2e-evidence.mjs 的 validateEnvelope()）一律
// 不傳這個參數，永遠用預設值（真正的 process.version）。selftest 需要在本
// 機不同 Node 版本下模擬「envelope 對得上正常 CI 的 Node」時，才會明確傳入
// 這個參數（見 __fixtures__/envelopeFixture.mjs 與各 selftest 的
// CI_E2E_SELFTEST_ACTUAL_NODE_VERSION 用法）。
export function checkEnvelopeNodeVersion(envelope, { actualNodeVersion = process.version } = {}) {
  if (envelope.nodeVersionActual !== actualNodeVersion) {
    return [
      `envelope 的 nodeVersionActual="${envelope.nodeVersionActual}"，與比對基準="${actualNodeVersion}" 不符`
        + '——記錄的 Node 版本與實際生效版本不一致',
    ];
  }
  return [];
}

// review round 6（#489 F3）：checkout tree 內容雜湊交叉核對——比對象是
// `HEAD^{tree}`（涵蓋全部檔案內容與路徑），不只是 commit SHA。reviewer 已
// 證實把 checkoutTreeSha 換成另一個合法 40-hex 值，先前完全沒有等效
// checkEnvelopeAgainstRepoHead 的交叉核對，一律放行。
export function checkEnvelopeAgainstRepoTree(envelope, repoRootForGitCheck) {
  const gitResult = spawnSync('git', ['-C', repoRootForGitCheck, 'rev-parse', 'HEAD^{tree}'], { encoding: 'utf8' });
  if (gitResult.error || gitResult.status !== 0) {
    return [
      `自行執行 git rev-parse HEAD^{tree} 失敗（-C ${repoRootForGitCheck}），無法核對 envelope.checkoutTreeSha 與實際 checkout 是否相符：`
        + `${gitResult.error?.message ?? (gitResult.stderr ?? '').trim()}`,
    ];
  }
  const actualTree = gitResult.stdout.trim();
  if (
    !/^[0-9a-f]{40}$/i.test(actualTree)
    || actualTree.toLowerCase() !== String(envelope.checkoutTreeSha).toLowerCase()
  ) {
    return [
      `envelope 的 checkoutTreeSha="${envelope.checkoutTreeSha}" 與實測的 git rev-parse HEAD^{tree}="${actualTree}"`
        + `（-C ${repoRootForGitCheck}）不符——checkout 內容不可信，不能只信 envelope 自己宣稱的值`,
    ];
  }
  return [];
}

// review round 6（#489 F3）：Go／Wails／Playwright／Node 對「正常 CI 核定
// 版本」做子字串核對（見 ci-e2e-entries.mjs REQUIRED_TOOL_VERSIONS 頭註）。
// reviewer 已證實把這幾個欄位換成任意版本字串（Wails v9.9.9、Go
// go1.24.0、Playwright Version 1.0.0），先前只驗「非空字串」（schema 檢
// 查），完全沒有核對是不是核定版本，一律放行。Chrome 依決定文件明文「只
// 記錄有效的實測版本，不額外釘版」，不在這份檢查裡。版本只在 job 開始量
// 一次（envelope 產生時），這裡只是核對「envelope 記的值」，不重新 spawn
// 探測指令。
export function checkEnvelopeToolVersions(envelope, requiredToolVersions = REQUIRED_TOOL_VERSIONS) {
  const violations = [];
  for (const [tool, requiredVersion] of Object.entries(requiredToolVersions ?? {})) {
    const field = TOOL_VERSION_ENVELOPE_FIELDS[tool];
    if (!field) continue;
    const actual = envelope[field];
    const extractor = TOOL_VERSION_EXTRACTORS[tool];
    const extracted = typeof actual === 'string' && extractor ? extractor(actual) : null;
    if (extracted === null || extracted !== requiredVersion) {
      violations.push(
        `${field}="${actual}"，精確解析出的版本 token="${extracted}"，與正常 CI 核定版本 "${requiredVersion}" 不是精確相符`
          + `（工具：${tool}；不再用 includes 子字串比對，避免 go1.26.50／v2.13.00／Version 1.63.01 這類前綴碰撞被誤判為相符）`,
      );
    }
  }
  return violations;
}

/**
 * 一次跑完所有「不依賴任何一次 entry 實際執行結果」的檢查——job 一開始、
 * envelope.json 剛產生時就能判定，供 run-batch.mjs 的 job-level preflight
 * 使用（在進入 entries 迴圈之前就能擋下 SHA／版本錯誤，不必等全部
 * entry 跑完）。
 */
export function checkEnvelopeBaseAll(envelope, { repoRootForGitCheck, actualNodeVersion, requiredToolVersions } = {}) {
  const schemaViolations = checkEnvelopeSchema(envelope);
  // 基本型別都不對時不再做衍生的交叉核對——那些檢查本來就得先讀到正確型
  // 別的值才有意義，強行往下做只會對同一個根因重複噴一堆連鎖錯誤訊息。
  if (schemaViolations.length > 0) return schemaViolations;
  return [
    ...checkEnvelopeShaFormat(envelope),
    ...checkEnvelopeInternalConsistency(envelope),
    ...checkEnvelopeAgainstRepoHead(envelope, repoRootForGitCheck),
    ...checkEnvelopeAgainstRepoTree(envelope, repoRootForGitCheck),
    ...checkEnvelopeNodeVersion(envelope, actualNodeVersion !== undefined ? { actualNodeVersion } : {}),
    ...checkEnvelopeToolVersions(envelope, requiredToolVersions),
  ];
}
