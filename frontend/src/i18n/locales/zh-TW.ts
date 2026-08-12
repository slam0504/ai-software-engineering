export default {
  app: {
    tab: {
      chat: '對話',
      preview: '預覽',
      spec: '規格',
      diagram: '表示圖',
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
    },
    reason: {
      placeholder: '理由（退回時必填）',
    },
    reasonHint: '請先填理由再退回',
    degradedNotice: '核可記錄異常：核可與退回功能已暫停，目前僅供查看',
    empty: '目前沒有 Gate 1 項目',
    label: {
      approvalId: '核可編號（approval_id）',
      baseCommit: '基準 commit（base_commit）',
      specManifest: '規格 manifest（spec_manifest）',
    },
  },
  spec: {
    action: {
      draftGherkin: '草擬 Gherkin',
      detectAmbiguity: '歧義偵測',
      checkOracle: 'oracle 覆蓋檢查',
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
  },
  approval: {
    action: {
      allow: '允許',
      deny: '拒絕',
    },
    reason: {
      placeholder: '理由（拒絕時建議填寫）',
    },
  },
  diagram: {
    empty: '尚無可顯示的圖表',
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
      approvalRequest: '核可請求：{text}',
      approvalDecision: '核可決定：{text}',
      stateChange: '狀態 → {state}',
      retry: 'provider 重試',
      toolResult: '工具結果',
    },
    raw: '原始資料',
  },
  store: {
    bindingsNotReady: '綁定尚未就緒',
  },
}
