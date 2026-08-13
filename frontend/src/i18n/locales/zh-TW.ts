export default {
  app: {
    tab: {
      chat: '對話',
      preview: '預覽',
      spec: '規格',
      plan: '計畫',
      diagram: '表示圖',
      dag: '任務相依圖',
      tca: '測試契約核可',
    },
    sideTab: {
      gate: '核可',
      escalation: '升級收件匣',
    },
    timeline: {
      label: '執行時間軸',
    },
    resize: {
      width: '拖曳調整寬度',
      height: '拖曳調整高度',
    },
    startupError: '啟動：{error}',
  },
  settings: {
    action: {
      new: '開新對話',
      terminate: '強制終止',
      end: '結束',
      authStatus: '登入狀態',
      login: '登入',
      cancelLogin: '取消登入',
      logout: '登出',
      b1Probe: 'B1',
    },
    operationAction: {
      new: '開新對話',
      terminate: '強制終止',
      end: '結束對話',
      authStatus: '查詢登入狀態',
      login: '登入',
      cancelLogin: '取消登入',
      logout: '登出',
      b1Probe: 'B1 探測',
    },
    operation: {
      success: '{action}成功',
      successDetail: '{action}成功：{detail}',
      failure: '{action}失敗：{error}',
    },
    taskId: {
      placeholder: '任務標籤（task id）',
    },
    recordCase: {
      placeholder: '{provider}-case（錄流，可空）',
    },
    resumeId: {
      placeholder: 'resume id（可空）',
    },
    approvalPolicy: {
      tooltip: 'codex approvalPolicy',
      untrusted: 'untrusted（每次核可）',
      onRequest: 'on-request',
      never: 'never（不核可，風險自負）',
    },
    newSession: {
      tooltip: '結束目前 session，等待舊 provider 收尾後開新對話',
    },
    awaitingApproval: {
      tooltip: '等待核可',
    },
  },
  chat: {
    thinking: '思考過程',
    input: {
      placeholder: '輸入訊息，Enter 送出（Shift+Enter 換行）',
    },
    action: {
      send: '送出',
    },
  },
  gate: {
    action: {
      approve: '核可',
      reject: '退回',
    },
    state: {
      pending: '待核可',
      active: '已生效',
      stale: '已失效',
      superseded: '已取代',
      rejected: '已退回',
    },
    reason: {
      placeholder: '理由（退回時必填）',
    },
    reasonHint: '請先填理由再退回',
    degradedNotice: '核可記錄異常：核可與退回功能已暫停，目前僅供查看',
    empty: '目前沒有核可項目',
    label: {
      approvalId: '核可編號（approval_id）',
      baseCommit: '基準 commit（base_commit）',
      specManifest: '規格 manifest（spec_manifest）',
    },
    risk: {
      minimum: '最低風險層級',
      planner: '規劃風險層級',
      overrideReasonPlaceholder: '覆寫理由（selected 低於 planner 時必填）',
    },
    tca: {
      gate2Link: '對應 Gate 2：{id}',
      viewEvidence: '查看證據',
      mutationDigest: 'mutation digest',
    },
  },
  spec: {
    action: {
      draftGherkin: '產生驗收情境草稿',
      detectAmbiguity: '檢查規格歧義',
      checkOracle: '檢查驗收條件涵蓋度',
      acceptDraft: '套用草稿',
      submit: '送核',
      previewCommit: '預覽 commit',
      confirmCommit: '建立 commit',
    },
    assist: {
      drafting: 'AI 產生中…',
    },
    commitMessage: {
      placeholder: 'commit 訊息',
    },
    submittedApprovalId: '核可編號（approval_id）：{id}',
  },
  planWorkspace: {
    action: {
      generateDraft: '產生計畫草稿',
      applyDraft: '套用草稿',
      save: '儲存',
      submit: '送核 Gate 2',
      previewCommit: '預覽 commit',
      confirmCommit: '建立 commit',
    },
    assist: {
      drafting: 'AI 產生中…',
    },
    provider: {
      label: 'AI provider',
    },
    prompt: {
      placeholder: '描述要草擬的計畫內容',
    },
    planId: {
      placeholder: '計畫編號（plan_id）',
    },
    commitMessage: {
      placeholder: 'commit 訊息',
    },
    submittedApprovalId: '核可編號（approval_id）：{id}',
  },
  tcaWorkspace: {
    empty: '目前沒有已生效的 Gate 2 計畫可操作',
    testCommit: {
      pick: '選擇近期 commit…',
      placeholder: 'test commit（手動輸入）',
      precheckOk: 'commit 版本關係預檢通過',
    },
    action: {
      precheck: '預檢',
      registerMutation: '登記 mutation',
      runExpectedRed: '執行 expected-red',
      runNegativeControl: '執行 negative-control',
      retry: '重跑',
      submit: '送核 TCA',
    },
    mutationPatch: {
      placeholder: 'negative-control 用的 unified diff patch',
    },
    mutationId: 'mutation 編號（mutation_id）：{id}',
    submittedApprovalId: '核可編號（approval_id）：{id}',
  },
  evidence: {
    title: 'evidence：{id}',
    loading: '載入中…',
    result: {
      passed: '通過',
      failed: '失敗',
      error: '錯誤',
    },
    run: {
      running: '執行中…',
    },
    action: {
      close: '關閉',
    },
    label: {
      evidenceId: 'evidence_id',
      kind: 'kind',
      result: 'result',
      baseCommit: 'base_commit',
      testCommit: 'test_commit',
      oracleSurfaceDigest: 'oracle_surface_digest',
      mutationDigest: 'mutation_digest',
      command: 'command',
      cwd: 'cwd',
      startedAt: 'started_at',
      finishedAt: 'finished_at',
      exitCode: 'exit_code',
      expectedFailure: 'expected_failure',
      observedFailure: 'observed_failure',
      stdoutDigest: 'stdout_digest',
      stderrDigest: 'stderr_digest',
      recordingRef: 'recording_ref',
      runnerVersion: 'runner_version',
    },
  },
  approval: {
    action: {
      allow: '允許',
      deny: '拒絕',
    },
    reason: {
      placeholder: '理由（拒絕時建議填寫）',
    },
    toolRequest: '工具權限請求：{tool}',
    pendingCount: '＋{n} 筆等待中',
  },
  diagram: {
    empty: '尚無可顯示的圖表',
  },
  dag: {
    empty: '尚無可顯示的任務相依圖',
    parseError: '無法解析目前的 plan 內容',
  },
  risk: {
    tier: {
      low: '低',
      medium: '中',
      high: '高',
    },
  },
  preview: {
    empty: '在左側選擇檔案以預覽',
  },
  session: {
    state: {
      idle: '待命',
      waiting: '等待回覆',
      streaming: '回覆中',
      toolRunning: '工具執行中',
      awaitingApproval: '等待核可',
      retrying: '重試中',
      done: '完成',
      failed: '失敗',
    },
  },
  statusbar: {
    task: '任務：{id}',
    session: 'session：{id}',
    usage: {
      providerLatest: 'provider 最新回報值',
      sessionTotal: '本 session 累計',
    },
  },
  timeline: {
    result: {
      completed: '完成',
      failed: '失敗',
    },
    toolStatus: {
      completed: '已完成',
      inProgress: '執行中',
      failed: '失敗',
    },
    summary: {
      toolCall: '工具呼叫',
      toolWithStatus: '{label}（{status}）',
      approvalRequest: '核可請求：{text}',
      approvalDecision: '核可決定：{text}',
      stateChange: '狀態 → {state}',
      retry: 'provider 重試',
      toolResult: '工具結果',
    },
    raw: '原始資料',
    systemEvents: '系統事件 ×{count}',
  },
  store: {
    bindingsNotReady: '綁定尚未就緒',
  },
  escalation: {
    section: {
      open: '待處理',
      acknowledged: '已知悉',
      resolved: '已解除（{n}）',
    },
    empty: {
      open: '目前沒有待處理的升級項目',
      acknowledged: '目前沒有已知悉的升級項目',
      resolved: '目前沒有已解除的升級項目',
    },
    action: {
      retry: '重試',
      ack: '標記為已知悉',
    },
    badge: {
      source: {
        system: '系統',
        manual: '手動',
      },
      hard: '系統強制',
      occurrence: '第 {n} 次',
    },
    label: {
      blockScope: '阻擋範圍（block_scope）',
      sourceRef: '來源參照（source_ref）',
      noScope: '（不阻擋）',
    },
    hardNotice: '系統強制項目：僅能由系統解除',
    resolve: {
      resolutionPlaceholder: '選擇解除方式…',
      resolution: {
        fixed: '已修復（fixed）',
        accepted_risk: '接受風險（accepted_risk）',
        other: '其他（other）',
      },
      reasonPlaceholder: '理由（必填）',
      submit: '解除',
    },
    create: {
      title: '建立升級項目',
      sourceRefPlaceholder: '來源 ref（必填，例如 plan_id/task_id）',
      scope: {
        none: '（不阻擋）',
        workspace: 'workspace',
        gate2: 'gate2:<id>',
        tca: 'tca:<plan>/<task>',
        custom: '自由輸入',
      },
      scopeIdPlaceholder: 'ID',
      scopeCustomPlaceholder: '完整阻擋範圍（block_scope）',
      scopeIdRequired: '請填寫阻擋範圍的 ID 或完整值',
      summaryPlaceholder: '摘要（必填）',
      submit: '建立',
      buttonFrom: '建立升級項目',
    },
  },
}
