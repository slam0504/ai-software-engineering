export default {
  app: {
    tab: {
      chat: 'Chat',
      preview: 'Preview',
      spec: 'Spec',
      diagram: 'Diagram',
    },
    timeline: {
      label: 'Timeline',
    },
    resize: {
      width: 'Drag to resize width',
      height: 'Drag to resize height',
    },
    startupError: 'startup: {error}',
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
    },
    reason: {
      placeholder: 'Reason (required to reject)',
    },
    reasonHint: 'Enter a reason before rejecting',
    degradedNotice: 'Approval journal degraded: approve/reject paused, view only',
    empty: 'No Gate 1 items',
    label: {
      approvalId: 'approval_id',
      baseCommit: 'base_commit',
      specManifest: 'spec_manifest',
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
      toolResult: 'tool result',
    },
    raw: 'raw',
    systemEvents: 'System events ×{count}',
  },
  store: {
    bindingsNotReady: 'bindings not ready',
  },
}
