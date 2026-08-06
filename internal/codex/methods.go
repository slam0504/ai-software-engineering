package codex

// 方法名常數：以 schemas/codex（pinned 0.146.1 的 generate-json-schema 產物，
// 2026-08-06 覆核）為準。本表為 M0 支援子集，不宣稱完整 enum——完整集合以
// schema 產物為準，未支援的通知落 OnUnknown／KindUnknown（raw 保留）。
const (
	// client → server（request / notification）
	MethodInitialize         = "initialize"
	MethodInitialized        = "initialized"
	MethodThreadStart        = "thread/start"
	MethodThreadResume       = "thread/resume"
	MethodThreadFork         = "thread/fork"
	MethodTurnStart          = "turn/start"
	MethodTurnSteer          = "turn/steer"
	MethodTurnInterrupt      = "turn/interrupt"
	MethodAccountRead        = "account/read"
	MethodAccountLoginStart  = "account/login/start"
	MethodAccountLoginCancel = "account/login/cancel"
	MethodAccountLogout      = "account/logout"

	// server → client request（核可）
	MethodCmdExecRequestApproval    = "item/commandExecution/requestApproval"
	MethodFileChangeRequestApproval = "item/fileChange/requestApproval"

	// server → client notification
	MethodThreadStarted         = "thread/started"
	MethodTurnStarted           = "turn/started"
	MethodTurnCompleted         = "turn/completed"
	MethodTurnDiffUpdated       = "turn/diff/updated"
	MethodItemStarted           = "item/started"
	MethodItemCompleted         = "item/completed"
	MethodAgentMessageDelta     = "item/agentMessage/delta"
	MethodServerRequestResolved = "serverRequest/resolved"
	MethodAccountLoginCompleted = "account/login/completed"
	MethodAccountUpdated        = "account/updated"
)

// ClientMethods 是 M0 會送出的 c2s 方法集（replay 的 c2s 驗證依據）。
var ClientMethods = map[string]bool{
	MethodInitialize:         true,
	MethodInitialized:        true,
	MethodThreadStart:        true,
	MethodThreadResume:       true,
	MethodThreadFork:         true,
	MethodTurnStart:          true,
	MethodTurnSteer:          true,
	MethodTurnInterrupt:      true,
	MethodAccountRead:        true,
	MethodAccountLoginStart:  true,
	MethodAccountLoginCancel: true,
	MethodAccountLogout:      true,
}

// ServerNotifications 是 M0 已認得的 s2c 通知子集；不在集合的通知走 OnUnknown。
var ServerNotifications = map[string]bool{
	MethodThreadStarted:         true,
	MethodTurnStarted:           true,
	MethodTurnCompleted:         true,
	MethodTurnDiffUpdated:       true,
	MethodItemStarted:           true,
	MethodItemCompleted:         true,
	MethodAgentMessageDelta:     true,
	MethodServerRequestResolved: true,
	MethodAccountLoginCompleted: true,
	MethodAccountUpdated:        true,
}
