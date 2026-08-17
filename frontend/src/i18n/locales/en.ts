export default {
  app: {
    tab: {
      chat: 'Chat',
      preview: 'Preview',
      spec: 'Spec',
      plan: 'Plan',
      diagram: 'Diagram',
      dag: 'Task DAG',
      tca: 'TCA',
    },
    sideTab: {
      gate: 'Gate',
      escalation: 'Escalation Inbox',
    },
    timeline: {
      label: 'Timeline',
      unreadTooltip: 'Unread errors arrived while the timeline was collapsed — expand to read them',
    },
    resize: {
      width: 'Drag to resize width',
      height: 'Drag to resize height',
    },
    startupError: 'startup: {error}',
    goResubmit: {
      integrityError: 'Data integrity error: the resubmit target could not be parsed — navigation cancelled',
    },
  },
  settings: {
    action: {
      new: 'New',
      terminate: 'Terminate',
      end: 'End',
      authStatus: 'Auth',
      login: 'Login',
      cancelLogin: 'Cancel',
      logout: 'Logout',
      b1Probe: 'B1',
    },
    operationAction: {
      new: 'new session',
      create: 'create session',
      terminate: 'terminate',
      end: 'end session',
      authStatus: 'auth status query',
      login: 'login',
      cancelLogin: 'cancel login',
      logout: 'logout',
      b1Probe: 'B1 probe',
    },
    operation: {
      success: '{action} ok',
      successDetail: '{action} ok: {detail}',
      failure: '{action} failed: {error}',
    },
    taskId: {
      placeholder: 'Task label (task id)',
    },
    recordCase: {
      placeholder: '{provider}-case (recording, optional)',
      codexLabelOnly: 'codex recording is connection-wide (§3.4.4): this field is an audit label only',
    },
    resumeId: {
      placeholder: 'resume id (optional)',
    },
    approvalPolicy: {
      tooltip: 'codex approvalPolicy',
      untrusted: 'untrusted (approve each)',
      onRequest: 'on-request',
      never: 'never (no approval, at your own risk)',
    },
    createSession: {
      tooltip: 'create a new {provider} session and pin it to the focused pane',
    },
    newSession: {
      tooltip: 'End current session, wait for the old provider to wind down, then start a new chat',
    },
    awaitingApproval: {
      tooltip: 'Awaiting approval',
    },
  },
  chat: {
    thinking: 'thinking',
    input: {
      placeholder: 'Type a message, Enter to send (Shift+Enter for newline)',
    },
    action: {
      send: 'Send',
    },
  },
  dualPane: {
    empty: 'No session pinned to this pane yet — pin one from the list on the left',
  },
  gate: {
    action: {
      approve: 'Approve',
      reject: 'Reject',
    },
    state: {
      pending: 'PENDING',
      active: 'ACTIVE',
      stale: 'STALE',
      superseded: 'SUPERSEDED',
      rejected: 'REJECTED',
    },
    reason: {
      placeholder: 'Reason (required to reject)',
    },
    reasonHint: 'Enter a reason before rejecting',
    degradedNotice: 'Approval journal degraded: approve/reject paused, view only',
    empty: 'No approval items',
    label: {
      approvalId: 'approval_id',
      baseCommit: 'base_commit',
      specManifest: 'spec_manifest',
    },
    risk: {
      minimum: 'Minimum risk tier',
      planner: 'Planner risk tier',
      overrideReasonPlaceholder: 'Override reason (required when selected is below planner)',
    },
    tca: {
      gate2Link: 'Gate 2: {id}',
      viewEvidence: 'View evidence',
      mutationDigest: 'mutation digest',
      evidenceIntegrityError: 'Data integrity error: evidence binding roles are incomplete, evidence cannot be shown',
      evidenceIntegrityMissing: 'missing: {roles}',
      evidenceIntegrityDuplicate: 'duplicate: {roles}',
      evidenceIntegrityUnknown: 'unknown role: {roles}',
    },
    stale: {
      goResubmit: 'Go to resubmit',
      navError: 'Data integrity error: the resubmit target could not be determined',
    },
  },
  spec: {
    action: {
      draftGherkin: 'Draft Gherkin',
      detectAmbiguity: 'Detect ambiguity',
      checkOracle: 'Oracle coverage check',
      acceptDraft: 'Accept',
      submit: 'Submit for Approval',
      previewCommit: 'Preview commit',
      confirmCommit: 'Confirm commit',
    },
    assist: {
      drafting: 'Generating…',
    },
    commitMessage: {
      placeholder: 'commit message',
    },
    submittedApprovalId: 'approval_id: {id}',
  },
  planWorkspace: {
    action: {
      generateDraft: 'Generate plan draft',
      applyDraft: 'Apply draft',
      save: 'Save',
      submit: 'Submit for Gate 2',
      previewCommit: 'Preview commit',
      confirmCommit: 'Confirm commit',
    },
    assist: {
      drafting: 'Generating…',
    },
    provider: {
      label: 'AI provider',
    },
    prompt: {
      placeholder: 'Describe the plan to draft',
    },
    planId: {
      placeholder: 'plan ID',
    },
    commitMessage: {
      placeholder: 'commit message',
    },
    submittedApprovalId: 'approval_id: {id}',
  },
  tcaWorkspace: {
    empty: 'No active Gate 2 plan to work on',
    testCommit: {
      pick: 'Pick a recent commit…',
      placeholder: 'test commit (manual entry)',
      precheckOk: 'Lineage precheck passed',
    },
    action: {
      precheck: 'Precheck',
      registerMutation: 'Register mutation',
      runExpectedRed: 'Run expected-red',
      runNegativeControl: 'Run negative-control',
      retry: 'Retry',
      submit: 'Submit TCA',
    },
    mutationPatch: {
      placeholder: 'unified diff patch for negative-control',
    },
    mutationId: 'mutation_id: {id}',
    submittedApprovalId: 'approval_id: {id}',
    staleGeneration: {
      hint: 'State has changed — please re-check before running again',
    },
  },
  evidence: {
    title: 'evidence: {id}',
    loading: 'Loading…',
    result: {
      passed: 'passed',
      failed: 'failed',
      error: 'error',
    },
    run: {
      running: 'Running…',
    },
    action: {
      close: 'Close',
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
      allow: 'Allow',
      deny: 'Deny',
    },
    reason: {
      placeholder: 'Reason (suggested when denying)',
    },
    toolRequest: 'Tool permission request: {tool}',
    pendingCount: '+{n} pending',
  },
  diagram: {
    empty: 'No diagram to display',
  },
  dag: {
    empty: 'No task DAG to display',
    parseError: 'Could not parse the current plan content',
  },
  risk: {
    tier: {
      low: 'low',
      medium: 'medium',
      high: 'high',
    },
  },
  preview: {
    empty: 'Select a file on the left to preview',
  },
  session: {
    state: {
      idle: 'idle',
      waiting: 'waiting',
      streaming: 'streaming',
      toolRunning: 'tool running',
      awaitingApproval: 'awaiting approval',
      retrying: 'retrying',
      done: 'done',
      failed: 'failed',
    },
  },
  statusbar: {
    task: 'Task: {id}',
    session: 'session: {id}',
    usage: {
      providerLatest: 'provider latest',
      sessionTotal: 'session total',
    },
  },
  timeline: {
    result: {
      completed: 'ok',
      failed: 'ERROR',
    },
    toolStatus: {
      completed: 'completed',
      inProgress: 'in progress',
      failed: 'failed',
    },
    summary: {
      toolCall: 'tool call',
      toolWithStatus: '{label} ({status})',
      approvalRequest: 'Approval request: {text}',
      approvalDecision: 'Approval decision: {text}',
      stateChange: 'State → {state}',
      retry: 'provider retry',
      codexBroadcast: 'codex broadcast: {method}',
      toolResult: 'tool result',
    },
    raw: 'raw',
    systemEvents: 'System events ×{count}',
  },
  store: {
    bindingsNotReady: 'bindings not ready',
    noFocusedSession: 'no session is pinned to the focused pane — create or pin one first',
    sessionUnavailable: 'session {wsid} is recorded but not addressable (no working slot); restart the app or remove it',
  },
  sessionList: {
    empty: 'No sessions yet — use the create buttons above to start one',
    action: {
      create: 'Create',
      pin: 'Pin',
      remove: 'Remove',
    },
    busyTooltip: 'Waiting on a reply',
    create: {
      failed: 'Create {provider} session failed: {error}',
    },
    remove: {
      confirmText: 'The session will disappear from this list, but audit events and recordings are retained permanently (not deleted). Remove it?',
      confirmSubmit: 'Confirm removal',
      cancel: 'Cancel',
      failed: 'Remove session {wsid} failed: {error}',
    },
  },
  escalation: {
    section: {
      open: 'Open',
      acknowledged: 'Acknowledged',
      resolved: 'Resolved ({n})',
    },
    empty: {
      open: 'No open escalation items',
      acknowledged: 'No acknowledged escalation items',
      resolved: 'No resolved escalation items',
    },
    action: {
      retry: 'Retry',
      ack: 'Acknowledge',
    },
    badge: {
      source: {
        system: 'system',
        manual: 'manual',
      },
      hard: 'hard',
      occurrence: 'occurrence {n}',
    },
    label: {
      blockScope: 'block scope',
      sourceRef: 'source',
      noScope: '(non-blocking)',
    },
    hardNotice: 'Hard item: only the system may resolve it',
    resolve: {
      resolutionPlaceholder: 'Pick a resolution…',
      resolution: {
        fixed: 'Fixed',
        accepted_risk: 'Accepted risk',
        other: 'Other',
      },
      reasonPlaceholder: 'Reason (required)',
      submit: 'Resolve',
    },
    create: {
      title: 'Create escalation item',
      sourceRefPlaceholder: 'Source ref (required, e.g. plan_id/task_id)',
      scope: {
        none: '(non-blocking)',
        workspace: 'workspace',
        gate2: 'gate2:<id>',
        tca: 'tca:<plan>/<task>',
        custom: 'Custom',
      },
      scopeIdPlaceholder: 'ID',
      scopeCustomPlaceholder: 'Full block scope',
      scopeIdRequired: 'A scope is selected but its ID/value is empty — this block scope will not take effect',
      summaryPlaceholder: 'Summary (required)',
      submit: 'Create',
      buttonFrom: 'Create escalation item',
    },
    staleNav: {
      goResubmit: 'Go to resubmit',
      integrityError: 'Data integrity error: the resubmit target could not be parsed',
    },
  },
  newFile: {
    path: {
      placeholder: 'Path (e.g. spec/features/foo.feature or plan/my-plan.yaml)',
    },
    action: {
      create: 'Create file',
    },
    scopeHint: 'Path is outside the managed scope',
    singlePlanBlocked: 'A primary plan file already exists — only one primary plan is allowed at a time',
  },
  bump: {
    banner: {
      message: 'analysis_base_commit is behind the current HEAD — an update is recommended',
    },
    noBumpNeeded: 'No update needed',
    action: {
      viewDiff: 'View diff',
      rerunAssist: 'Re-run PlannerAssist',
      confirm: 'Confirm update',
    },
    warning: 'Confirming means you have reviewed this code change and confirm the current plan still applies',
  },
}
