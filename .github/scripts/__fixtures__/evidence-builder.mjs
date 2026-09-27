// evidence-builder.mjs — 供 evaluate-e2e-evidence.selftest.mjs／
// fake-runner.mjs 使用的合成證據建構器（非 controls/gate1/gate2/stale 這四
// 個已有真實樣本的 entry 才需要用到）。純寫檔，不 spawn 任何行程、不碰真實
// run-e2e/globalSetup/App/browser。
//
// review round 2 修法（主 agent 用真實 run 目錄複核發現）：先前這裡的
// `config: {}` 沒有 `rootDir`，且 `suiteWithOneTest`／`controlsSuites` 的
// `file` 值帶了 `gates/`／`controls/` 前綴——這跟真實 Playwright JSON
// reporter 的格式不符（真實格式是 `config.rootDir` 存 testDir 絕對路徑、
// `spec.file` 相對於 rootDir、不含批次子目錄前綴，見
// `__fixtures__/real/PROVENANCE.md` 與 evaluate-e2e-evidence.mjs 的
// `reconstructFrontendRelativePath()`）。現在改成：呼叫端傳入
// `rootDir`（預設 `/synthetic/frontend/e2e`，模擬 default 的 testDir），
// `suiteWithOneTest`／`controlsSuites` 的 `file` 一律用**不含前綴**的裸檔
// 名，交給 evaluator 自己用 `rootDir` 的 basename 還原前綴——這樣合成
// fixture 才是在測「跟真實格式相同的結構」，不是另外發明一套只有 selftest
// 自己看得懂的格式。
import { mkdirSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import {
  otherRequiredFilesForEntry,
  requiredDomainFilesForEntry,
  expectedRunEnvScenarioForEntry,
  expectedGateFlowForEntry,
  expectedScenarioConfigIdentity,
  expectedScenarioSecondTurnIdentity,
  expectedScenarioApprovalMethodForEntry,
  expectedCodexScenarioDecisionForEntry,
  expectedClaudeProtocolDecisionForEntry,
} from '../ci-e2e-entries.mjs';

/**
 * review round 5（#483 S1）：8 個 scenario entry 的「正確」領域證據內容——
 * 對照 evaluate-e2e-evidence.mjs 的 checkJsonlDomainFile()／
 * checkJsonDomainFile() 逐一產生會通過檢查的 fixture 內容，讓 makeGoodRunDir
 * 的綠燈路徑對 scenario entry 也代表「證據齊全且正確」，不再是空的（round
 * 3-4 遺留的錯誤：scenario/default 沒有領域證據檔）。
 */
function scenarioDomainContent(domainFile, resolvedEntry, runId, overrides) {
  if (domainFile === 'scenario-config.json') {
    const identity = expectedScenarioConfigIdentity(resolvedEntry, runId);
    const payload = { ...identity, approvalMethod: expectedScenarioApprovalMethodForEntry(resolvedEntry), threadMode: 'start', afterApproval: [], turnStatus: 'completed' };
    if (resolvedEntry.id === 'commandExecution-recovery') payload.secondTurn = expectedScenarioSecondTurnIdentity(runId);
    return { kind: 'json', content: { ...payload, ...(overrides ?? {}) } };
  }
  if (domainFile === 'scenario-wire.log') {
    // review round 6（#489 F2）：真實格式是 `{seq, ts?, dir, frame}`，
    // `frame` 是實際 JSON-RPC payload（見真實 run 樣本
    // scenario-wire.log 的 thread/start 回覆
    // `frame:{"id":2,"result":{"thread":{"id":"b3a2b2-thread-<runId>"}}}`）。
    // 帶上這筆回覆，對得上 scenario-config.json 的 threadId。
    const identity = expectedScenarioConfigIdentity(resolvedEntry, runId);
    return {
      kind: 'jsonl',
      lines: [
        { seq: 1, dir: 'c2s', frame: { id: 1, method: 'initialize' } },
        { seq: 2, dir: 's2c', frame: { id: 2, result: { thread: { id: identity.threadId } } } },
      ],
    };
  }
  if (domainFile === 'scenario-wire.log.manifest.json') {
    const identity = expectedScenarioConfigIdentity(resolvedEntry, runId);
    return {
      kind: 'json',
      content: {
        exitCode: 0,
        scenario: resolvedEntry.env.E2E_SCENARIO,
        approvalMethod: expectedScenarioApprovalMethodForEntry(resolvedEntry),
        approvalRequestId: identity.approvalRequestId,
        decisionReceived: expectedCodexScenarioDecisionForEntry(resolvedEntry),
        ...(overrides ?? {}),
      },
    };
  }
  if (domainFile === 'app-audit.jsonl') {
    const decision = expectedCodexScenarioDecisionForEntry(resolvedEntry);
    const identity = expectedScenarioConfigIdentity(resolvedEntry, runId);
    return {
      kind: 'jsonl',
      lines: [
        {
          ts: '2026-09-28T00:00:00.000Z',
          kind: 'codex_approval_request',
          data: {
            id: 'a1',
            wsid: 'w1',
            method: expectedScenarioApprovalMethodForEntry(resolvedEntry),
            // review round 6（#489 F2）：真實 producer 把 threadId/turnId/
            // itemId 放進 raw_params（見 checkJsonlDomainFile 的
            // app-audit.jsonl 分支），不是 data 頂層。
            raw_params: { threadId: identity.threadId, turnId: identity.turnId, itemId: identity.itemId },
          },
        },
        { ts: '2026-09-28T00:00:01.000Z', kind: 'codex_approval_decision', data: { id: 'a1', decision, ...(overrides ?? {}) } },
      ],
    };
  }
  if (domainFile === 'app-workspace-sessions.json') {
    // review round 6（#489 F2）：resume_session_id 對得上 threadId（真實
    // producer 既有欄位）。
    const identity = expectedScenarioConfigIdentity(resolvedEntry, runId);
    return {
      kind: 'json',
      content: {
        entries: { w1: { wsid: 'w1', provider: 'codex', resume_session_id: identity.threadId, ...(overrides ?? {}) } },
      },
    };
  }
  if (domainFile === 'app-wire-log.jsonl') {
    // review round 6（#489 F2）：至少一筆 frame 帶 app-audit.jsonl 同一個
    // wsid，供跨檔 identity 交叉核對。
    return { kind: 'jsonl', lines: [{ dir: 'c2s', raw: {} }, { dir: 's2c', wsid: 'w1', raw: {} }] };
  }
  if (domainFile === 'app-wire-log.meta.json') {
    // review round 6（#489 F2）：argv 路徑要含本次 run 目錄名稱（真實
    // producer 的 fake codex-cli 是從這次 run 專屬 toolsDir 底下啟動）。
    return {
      kind: 'json',
      content: { provider: 'codex', exit_code: 0, argv: ['node', `/synthetic/${runId}/fake-tools/codex-cli/bin/codex`, 'app-server'], ...(overrides ?? {}) },
    };
  }
  if (domainFile === 'claude-expectation.json') {
    if (resolvedEntry.id === 'claude-approval-recovery') {
      const sessionId = `b3a2b2-claude-session-${runId}`;
      return {
        kind: 'json',
        content: {
          rounds: [1, 2].map((round) => ({
            round, prompt: `r${round}`, resume: round === 1 ? null : sessionId,
            approval: { sessionId, decision: 'allow' },
          })),
          ...(overrides ?? {}),
        },
      };
    }
    return { kind: 'json', content: { approval: { decision: expectedClaudeProtocolDecisionForEntry(resolvedEntry) }, prompt: 'p', ...(overrides ?? {}) } };
  }
  if (domainFile === 'app-binary-identity.json') {
    return { kind: 'json', content: { pid: 123, sha256: 'a'.repeat(64), canonicalPath: '/synthetic/App', startedAt: '2026-09-28T00:00:00.000Z', ...(overrides ?? {}) } };
  }
  if (domainFile === 'cliinfo.json') {
    return {
      kind: 'json',
      content: {
        toolsSource: 'env', workspaceSource: 'env', startupError: '', claudeVersion: 'claude-fake', codexVersion: 'codex-fake', workspace: '/synthetic', toolsDir: '/synthetic/tools', ...(overrides ?? {}),
      },
    };
  }
  if (domainFile === 'claude-broker-audit.raw.jsonl') {
    return { kind: 'jsonl', lines: [{ ts: '2026-09-28T00:00:00.000Z', kind: 'mcp_approval_decision', data: {} }] };
  }
  if (domainFile === 'claude-broker-audit.json') {
    // review round 6（#489 F1）：真實 producer（selectBrokerAuditForApproval，
    // claudeAppEvidence.ts:26）回傳的是陣列，不是物件——先前這裡合成一個物
    // 件 `{decision:'x'}`，跟真實格式不符（evaluate-e2e-evidence.mjs 先前
    // 對這個檔案完全沒有 schema 檢查，所以沒被抓到）。改成產生會通過新
    // checkJsonDomainFile() 分支的「request＋decision」兩筆最小陣列，
    // decision.data.behavior 對上這個 entry 的核定決策。
    return {
      kind: 'json',
      isArray: true,
      content: [
        { kind: 'request', ts: '2026-09-28T00:00:00.000Z', data: { id: 'a1', tool_name: 'Bash' } },
        {
          kind: 'decision',
          ts: '2026-09-28T00:00:01.000Z',
          data: { id: 'a1', behavior: expectedClaudeProtocolDecisionForEntry(resolvedEntry), ...(overrides ?? {}) },
        },
      ],
    };
  }
  if (domainFile === 'claude-ui-judgement.json') {
    // review round 7（#491 G1）：observedBrokerId 對上 claude-broker-audit.json
    // 的 decision id（'a1'，見上面的 claude-broker-audit.json 分支），
    // configWsid 對上 run-state.json 的 processes[].command（'w1'，見下面
    // makeGoodRunDir() 的預設 runState.processes）——不然新的身分關聯檢查
    // 會讓這個「正確」fixture 本身變成反例。
    return {
      kind: 'json',
      content: {
        violations: [], observedBrokerId: 'a1', agreedApprovalId: 'a1', configWsid: 'w1', ...(overrides ?? {}),
      },
    };
  }
  if (domainFile === 'claude-recovery/exit-observations.jsonl') {
    return { kind: 'jsonl', lines: [{ observedAt: '2026-09-28T00:00:00.000Z', round: 1, role: 'cli', pid: 1, observation: { state: 'absent' } }] };
  }
  if (domainFile === 'claude-recovery/dom-observations.json') {
    return { kind: 'json', content: [{ round: 1, domWsid: 'w1' }], isArray: true };
  }
  if (domainFile === 'claude-recovery/judgement.json') {
    // review round 7（#491 G1）：round1/round2 是跟 judged 同層的頂層欄位
    // （真實 producer 既有結構，見真實 claude-approval-recovery run 的
    // claude-recovery/judgement.json 樣本），appBoundResume／
    // registryBinding.wsid 供 evaluate-e2e-evidence.mjs 跟下面
    // round1/round2 的 sessions.json／workspace-sessions.json／audit.jsonl
    // 交叉核對。
    return {
      kind: 'json',
      content: {
        judged: { violations: [], agreedApprovalIds: ['a1', 'a2'] },
        round1: { appBoundResume: `b3a2b2-claude-session-${runId}`, registryBinding: { wsid: 'w1' } },
        round2: { appBoundResume: `b3a2b2-claude-session-${runId}`, registryBinding: { wsid: 'w1' } },
        ...(overrides ?? {}),
      },
    };
  }
  if (domainFile === 'claude-recovery/round1/sessions.json' || domainFile === 'claude-recovery/round2/sessions.json') {
    // key is the run-specific appBoundResume; its wsid agrees with
    // 上 registryBinding.wsid（'w1'）——兩輪同一個 App/workspace，本來就該
    // 一致。
    return { kind: 'json', content: { [`b3a2b2-claude-session-${runId}`]: { wsid: 'w1' }, ...(overrides ?? {}) } };
  }
  if (domainFile === 'claude-recovery/round1/workspace-sessions.json' || domainFile === 'claude-recovery/round2/workspace-sessions.json') {
    return { kind: 'json', content: { entries: { w1: { resume_session_id: `b3a2b2-claude-session-${runId}` } }, ...(overrides ?? {}) } };
  }
  if (domainFile === 'claude-recovery/round1/audit.jsonl') {
    // review round 7（#491 G1）：round1 的快照只該有 round1 自己的
    // decision（'a1'，對上 judged.agreedApprovalIds[0]）——preserveStateFiles()
    // 是在 round1 結束、round2 開始前拍的快照，不該看得到 round2 的內容。
    return {
      kind: 'jsonl',
      lines: [
        { ts: '2026-09-28T00:00:00.000Z', kind: 'request', data: { id: 'a1' } },
        { ts: '2026-09-28T00:00:01.000Z', kind: 'decision', data: { id: 'a1', behavior: 'allow' } },
      ],
    };
  }
  if (domainFile === 'claude-recovery/round2/audit.jsonl') {
    // round2 的快照是同一份持續累加的 audit.jsonl，該同時看得到 round1／
    // round2 兩輪——最後一筆 decision 是 'a2'（對上
    // judged.agreedApprovalIds[1]）。
    return {
      kind: 'jsonl',
      lines: [
        { ts: '2026-09-28T00:00:00.000Z', kind: 'request', data: { id: 'a1' } },
        { ts: '2026-09-28T00:00:01.000Z', kind: 'decision', data: { id: 'a1', behavior: 'allow' } },
        { ts: '2026-09-28T00:00:02.000Z', kind: 'request', data: { id: 'a2' } },
        { ts: '2026-09-28T00:00:03.000Z', kind: 'decision', data: { id: 'a2', behavior: 'allow' } },
      ],
    };
  }
  return null; // 不是 scenario 專屬檔（gate/controls 走既有路徑，不會呼叫到這裡）
}

export function makeGoodRunDir(root, runId, {
  specs,
  entry,
  rootDir = '/synthetic/frontend/e2e',
  skipOtherFile = null,
  skipDomainFile = null,
  runStateOverrides = {},
  harnessOverrides = {},
  runEnvOverrides = {},
  executionEntryOverride = undefined,
  domainOverrides = {},
  includeTestFailed = false,
}) {
  const runDir = path.join(root, runId);
  mkdirSync(runDir, { recursive: true });
  const resolvedEntry = entry ?? { id: 'default', batch: 'smoke' };

  const runState = {
    runId,
    status: 'stopped',
    observationFailures: [],
    // review round 7（#491 G1）：evaluate-e2e-evidence.mjs 新增
    // extractClaudeWsidFromRunState()，從 processes[].command 找
    // `mcp-<WSID>.json`（跟 claudeAppEvidence.ts wsidFromMcpConfigPath()
    // 同一種檔名慣例）核對 claude-ui-judgement.json 的 configWsid——這裡預
    // 設帶一筆假 fakeClaudeCli 啟動指令，wsid 固定為 'w1'（跟
    // claude-ui-judgement.json／claude-broker-audit.json 的合成 fixture 同
    // 一個值），讓「正確」fixture 本身不會被新檢查誤判成反例。
    processes: [{ command: `node fakeClaudeCli.ts --mcp-config /synthetic/${runId}/.workbench/mcp-w1.json` }],
    ...runStateOverrides,
  };
  writeFileSync(path.join(runDir, 'run-state.json'), JSON.stringify(runState, null, 2));

  // review round 3（R1／R2）：identity／領域證據檔——先前的合成 fixture 完
  // 全沒有這些檔案，equivalent 於 reviewer 反例「拿掉這些檔案」的起始狀
  // 態。現在預設就要生出「正確」版本，讓 selftest 的綠燈路徑真的代表
  // 「證據齊全且正確」，反例再各自覆寫成錯的。
  const expectedExecutionEntry = resolvedEntry.batch === 'scenarios' ? 'scenario' : 'default';
  const executionEntryPayload = executionEntryOverride ?? { entry: expectedExecutionEntry };
  writeFileSync(path.join(runDir, 'execution-entry.json'), JSON.stringify(executionEntryPayload, null, 2));

  const expectedScenario = expectedRunEnvScenarioForEntry(resolvedEntry);
  const runEnvPayload = {
    runId,
    artifactsDir: runDir,
    ...(expectedScenario !== null ? { scenario: expectedScenario } : {}),
    ...runEnvOverrides,
  };
  writeFileSync(path.join(runDir, 'run-env.json'), JSON.stringify(runEnvPayload, null, 2));

  for (const domainFile of requiredDomainFilesForEntry(resolvedEntry)) {
    if (domainFile === skipDomainFile) continue;
    const destPath = path.join(runDir, domainFile);
    mkdirSync(path.dirname(destPath), { recursive: true });
    if (domainFile === 'gate-flow.json') {
      const specFile = resolvedEntry.expectedSpecFiles?.[0]?.split('/').pop();
      const payload = { runId, flow: expectedGateFlowForEntry(resolvedEntry), specFile, ...(domainOverrides[domainFile] ?? {}) };
      writeFileSync(destPath, JSON.stringify(payload, null, 2));
    } else if (domainFile === 'gate-evidence.json') {
      const payload = {
        flow: expectedGateFlowForEntry(resolvedEntry), runId, bodyStatus: 'passed', finalStatus: 'passed', steps: [],
        ...(domainOverrides[domainFile] ?? {}),
      };
      writeFileSync(destPath, JSON.stringify(payload, null, 2));
    } else if (domainFile === 'control-a-evidence.json' || domainFile === 'control-b-evidence.json') {
      const payload = { wrapperEvidence: [{ actualDelayMs: 2000 }], ...(domainOverrides[domainFile] ?? {}) };
      writeFileSync(destPath, JSON.stringify(payload, null, 2));
    } else {
      // review round 5（#483 S1）：8 個 scenario entry 的領域證據檔——用
      // scenarioDomainContent() 產生對得上 evaluate-e2e-evidence.mjs
      // checkJsonlDomainFile()／checkJsonDomainFile() 的「正確」內容。
      const built = scenarioDomainContent(domainFile, resolvedEntry, runId, domainOverrides[domainFile]);
      if (built === null) {
        // 未知的領域證據檔名（理論上不該發生，requiredDomainFilesForEntry
        // 只會回傳已知清單）——寫一個明顯不完整的佔位，讓呼叫端在
        // selftest 裡容易發現，不悄悄假裝成功。
        writeFileSync(destPath, JSON.stringify({ FIXME_UNKNOWN_DOMAIN_FILE: domainFile }, null, 2));
      } else if (built.kind === 'jsonl') {
        writeFileSync(destPath, `${built.lines.map((l) => JSON.stringify(l)).join('\n')}\n`);
      } else {
        writeFileSync(destPath, JSON.stringify(built.content, null, 2));
      }
    }
  }

  // claude-approval-deny 專屬的 optional 領域證據檔（不在
  // requiredDomainFilesForEntry() 清單內，但 evaluator 對這個 entry 仍會
  // 核對，見 ci-e2e-entries.mjs optionalDomainFilesForEntry()）。
  if (resolvedEntry.id === 'claude-approval-deny' && skipDomainFile !== 'claude-ui-action.json') {
    writeFileSync(
      path.join(runDir, 'claude-ui-action.json'),
      JSON.stringify({ domWsid: 'w1', domApprovalId: 'a1', decision: 'deny', clickResolved: true, ...(domainOverrides['claude-ui-action.json'] ?? {}) }, null, 2),
    );
  }

  if (includeTestFailed) {
    writeFileSync(path.join(runDir, 'TEST_FAILED'), 'synthetic TEST_FAILED for negative-case testing\n');
  }

  const cleanupClean = harnessOverrides.cleanupClean ?? true;
  const overallFailed = harnessOverrides.overallFailed ?? false;
  const artifactViolations = harnessOverrides.artifactViolations ?? 0;
  const lastLine = harnessOverrides.lastLine ?? '最終結果：PASSED';
  const verdictLine = `[2026-09-28T00:00:00.000Z] globalTeardown 判定：interrupted=false testFailed=false `
    + `cleanupClean=${cleanupClean} artifactViolations=${artifactViolations} → overallFailed=${overallFailed}`;
  const lines = [
    '[2026-09-28T00:00:00.000Z] harness started',
    verdictLine,
    `[2026-09-28T00:00:01.000Z] ${lastLine}`,
  ];
  writeFileSync(path.join(runDir, 'harness.log'), `${lines.join('\n')}\n`);

  // 必要檔清單依 entry 決定（chrome-argv.txt 只有 default／scenario 才
  // 有，見 ci-e2e-entries.mjs 的 otherRequiredFilesForEntry() 頭註）；沒有
  // 傳 entry 時（例如測「identity 異常」場景不在乎必要檔清單）預設沿用
  // default 的清單（含 chrome-argv.txt），維持既有呼叫端相容。
  // review round 3 修法：真實 bug（不是猜測）——`UNIVERSAL_REQUIRED_FILES`
  // 包含 `'run-env.json'`（只驗存在的共用檔清單），這個迴圈先前對它也寫一
  // 份 `'{}'` 佔位內容，發生在上面「寫真實 run-env.json 內容」**之後**，
  // 會把剛寫好的 identity 內容整個覆寫回空物件——第一次串所有 selftest 時
  // 就是被這裡覆寫，導致 evaluator 讀到 `runId=undefined`。`run-env.json`
  // 已經在上面用真實 schema 寫過，這裡要跳過，不能再用泛用佔位邏輯覆寫。
  const requiredFiles = entry ? otherRequiredFilesForEntry(entry) : otherRequiredFilesForEntry({ id: 'default' });
  for (const name of requiredFiles) {
    if (name === skipOtherFile || name === 'run-env.json') continue;
    writeFileSync(path.join(runDir, name), '{}');
  }

  const report = {
    config: { rootDir },
    errors: [],
    stats: { startTime: '2026-09-28T00:00:00.000Z', duration: 1000, expected: 0, unexpected: 0, flaky: 0, skipped: 0 },
    suites: specs,
  };
  writeFileSync(path.join(runDir, 'playwright-results.json'), JSON.stringify(report, null, 2));

  return runDir;
}

/** 建一個「單一 spec、單一 test、passed」的 suite 節點（給 default／gates／單一 scenario 用）。 */
export function suiteWithOneTest(file, specTitle, testTitle) {
  return {
    title: path.basename(file, '.spec.ts'),
    file,
    column: 1,
    line: 1,
    specs: [
      {
        tags: [],
        title: specTitle,
        ok: true,
        id: 's1',
        file,
        line: 1,
        column: 1,
        tests: [
          {
            timeout: 60000,
            annotations: [],
            expectedStatus: 'passed',
            projectName: '',
            projectId: '',
            status: 'expected',
            results: [
              {
                workerIndex: 0,
                parallelIndex: 0,
                status: 'passed',
                duration: 100,
                error: undefined,
                errors: [],
                stdout: [],
                stderr: [],
                retry: 0,
                startTime: '2026-09-28T00:00:00.000Z',
                attachments: [],
                annotations: [],
              },
            ],
          },
        ],
      },
    ],
  };
}

/** controls 套件的 save-detection.spec.ts + reverse-check.spec.ts 節點。 */
export function controlsSuites({
  controlAStatus = 'expected',
  controlAResultStatus = 'failed',
  controlAErrorMessage = '對照 A（舊判定）預期失敗：讀到的應是舊內容而非新內容',
  controlARetry = 0,
  controlAExpectedStatus = 'failed',
  controlBOk = true,
  reverseCheckSkipped = true,
} = {}) {
  const controlATest = {
    timeout: 60000,
    annotations: [],
    expectedStatus: controlAExpectedStatus,
    projectName: '',
    projectId: '',
    status: controlAStatus,
    results: [
      {
        workerIndex: 0,
        parallelIndex: 0,
        status: controlAResultStatus,
        duration: 100,
        error: controlAErrorMessage ? { message: controlAErrorMessage } : undefined,
        errors: controlAErrorMessage ? [{ message: controlAErrorMessage }] : [],
        stdout: [],
        stderr: [],
        retry: controlARetry,
        startTime: '2026-09-28T00:00:00.000Z',
        attachments: [],
        annotations: [],
      },
    ],
  };
  const controlBTest = {
    timeout: 60000,
    annotations: [],
    expectedStatus: 'passed',
    projectName: '',
    projectId: '',
    status: controlBOk ? 'expected' : 'unexpected',
    results: [
      {
        workerIndex: 0,
        parallelIndex: 0,
        status: controlBOk ? 'passed' : 'failed',
        duration: 100,
        error: undefined,
        errors: [],
        stdout: [],
        stderr: [],
        retry: 0,
        startTime: '2026-09-28T00:00:00.000Z',
        attachments: [],
        annotations: [],
      },
    ],
  };
  const saveDetectionSuite = {
    title: 'save-detection',
    file: 'save-detection.spec.ts',
    column: 1,
    line: 1,
    specs: [
      {
        tags: [], title: 'control A: old judgment (save button disabled) must fail on the target assertion', ok: false,
        id: 'a', file: 'save-detection.spec.ts', line: 55, column: 1, tests: [controlATest],
      },
      {
        tags: [], title: 'control B: new judgment (expect.poll full content) succeeds', ok: controlBOk,
        id: 'b', file: 'save-detection.spec.ts', line: 131, column: 1, tests: [controlBTest],
      },
    ],
  };

  const reverseTest = {
    timeout: 60000,
    annotations: [],
    expectedStatus: 'skipped',
    projectName: '',
    projectId: '',
    status: reverseCheckSkipped ? 'skipped' : 'unexpected',
    results: [
      {
        workerIndex: 0,
        parallelIndex: 0,
        status: reverseCheckSkipped ? 'skipped' : 'passed',
        duration: 0,
        error: undefined,
        errors: [],
        stdout: [],
        stderr: [],
        retry: 0,
        startTime: '2026-09-28T00:00:00.000Z',
        attachments: [],
        annotations: [],
      },
    ],
  };
  const reverseCheckSuite = {
    title: 'reverse-check',
    file: 'reverse-check.spec.ts',
    column: 1,
    line: 1,
    specs: [
      { tags: [], title: 'reverse check', ok: true, id: 'r', file: 'reverse-check.spec.ts', line: 20, column: 1, tests: [reverseTest] },
    ],
  };

  return [saveDetectionSuite, reverseCheckSuite];
}
