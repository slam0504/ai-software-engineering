#!/usr/bin/env node
// gha-runtime-guard.mjs — B3a-CI-1 review round 7（#491 G2：selftest override
// 仍能進入正常 CI 入口）。
//
// reviewer 已證實：本機受控子行程模擬 `GITHUB_ACTIONS=true`／`CI=true`，同
// 時提供 `CI_E2E_OFFLINE_SELFTEST=1`＋`CI_E2E_TEST_RUNNER=<fake>`＋
// `SELFTEST_ACTUAL_NODE_VERSION` 去跑既有 smoke driver（run-batch.mjs），整
// 批仍 rc0——run-batch.mjs／evaluate-e2e-evidence.mjs／generate-ci-envelope.mjs
// 這三個「正常 GitHub Actions 入口」先前都只在程式碼註解裡宣稱「正式
// workflow 絕不設定這些覆寫」，源碼本身從未檢查 `GITHUB_ACTIONS`，靠約定當
// 隔離不是實作。
//
// 這裡是單一權威來源：三個 CLI 入口在**任何 runtime／probe spawn 及工作目
// 錄建立之前**都呼叫這個共用函式，不各自分別補一份清單、避免彼此漂移。目
// 標只是防止「正常 CI 意外使用本票測試替身」，不宣稱能防止有人惡意修改整
// 份 workflow（如果 workflow 本身被改成故意連同這些變數一起設定，那已經是
// 修改 workflow 本身，超出這裡能防的範圍——decision.md 原文）。
//
// 離線 selftest（本機明確呼叫這幾支 CLI，不設 GITHUB_ACTIONS）不受影響：
// `isGithubActions` 只在 `GITHUB_ACTIONS==='true'` 時才生效，這是 GitHub
// Actions runner 唯一會設定、且值恰好是字串 "true" 的既有環境變數，不是本
// 票新發明的旗標。
export function rejectTestControlEnvUnderGithubActions(scriptLabel, testOnlyVarNames) {
  if (process.env.GITHUB_ACTIONS !== 'true') return;
  const present = testOnlyVarNames.filter((name) => {
    const v = process.env[name];
    return typeof v === 'string' && v.length > 0;
  });
  if (present.length === 0) return;
  process.stderr.write(
    `[${scriptLabel}] 拒絕（#491 G2 正常 GHA 入口邊界）：GITHUB_ACTIONS=true 時偵測到本票測試專用 env var 仍設定：`
      + `${present.join(', ')}——正常 CI 入口不得接受測試替身／豁免／版本覆寫，即使只是為了離線模擬也一律拒絕，`
      + '在任何 runtime 或 probe spawn 之前就結束，不回退、不嘗試修正\n',
  );
  process.exit(2);
}
