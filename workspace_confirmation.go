package main

// ---- B4：workspace 確認閘（spec docs/superpowers/specs/2026-10-11-b4-workspace-confirmation-design.md）----

// env 候選被拒的有限原因分類（spec §5.1）。
const (
	envRejectNormalizeFailed = "normalize_failed"
	envRejectMkdirFailed     = "mkdir_failed"
)

// workspaceProvenance：解析過程的附帶事實（spec §2.1）。
type workspaceProvenance struct {
	EnvSet            bool   // WORKBENCH_WORKSPACE 非空
	EnvRejected       bool   // env 候選被拒絕而退回其他候選
	EnvRejectReason   string // envReject*；EnvRejected 為 false 時為空
	IsHome            bool   // 實際 workspace 與家目錄是同一個目錄（os.SameFile）
	HomeIdentityKnown bool   // 家目錄與 workspace 的身分都能判定
}

// noteRejected：只有 env 候選被拒才記錄（其他來源被拒是既有的正常退回）。
func (p *workspaceProvenance) noteRejected(source, reason string) {
	if source != "env" {
		return
	}
	p.EnvRejected = true
	p.EnvRejectReason = reason
}

// workspaceResolution：resolveWorkspace 的結果。前四個欄位與 B4 之前的四個回傳值一一
// 對應；Provenance 是 B4 新增。
type workspaceResolution struct {
	Workspace  string
	State      string
	Source     string
	Err        error
	Provenance workspaceProvenance
}

// 確認原因代碼（spec R1 (a)–(d)）。前端以這些字串查 i18n，新增或改名要同步
// frontend/src/i18n/stateKeys.ts 的 workspaceReasonKeys。
const (
	wsReasonSourceHome           = "source_home"
	wsReasonSourceTmp            = "source_tmp"
	wsReasonEnvRejected          = "env_rejected"
	wsReasonEnvRejectedNormalize = "env_rejected_normalize_failed"
	wsReasonEnvRejectedMkdir     = "env_rejected_mkdir_failed"
	wsReasonIsHome               = "is_home"
	wsReasonHomeUnknown          = "home_identity_unknown"
)

// workspacePolicy：R1 的純函式——輸入 source 與 provenance，輸出是否需要確認與所有成立
// 的原因（固定順序 a→b→c→d）。不碰檔案系統、不讀全域狀態，可單獨測試。
func workspacePolicy(source string, p workspaceProvenance) (required bool, reasons []string) {
	reasons = []string{}
	switch source {
	case "home":
		reasons = append(reasons, wsReasonSourceHome)
	case "tmp":
		reasons = append(reasons, wsReasonSourceTmp)
	}
	if p.EnvSet && p.EnvRejected {
		switch p.EnvRejectReason {
		case envRejectNormalizeFailed:
			reasons = append(reasons, wsReasonEnvRejectedNormalize)
		case envRejectMkdirFailed:
			reasons = append(reasons, wsReasonEnvRejectedMkdir)
		default:
			reasons = append(reasons, wsReasonEnvRejected)
		}
	}
	if p.IsHome {
		reasons = append(reasons, wsReasonIsHome)
	}
	if !p.HomeIdentityKnown {
		reasons = append(reasons, wsReasonHomeUnknown)
	}
	return len(reasons) > 0, reasons
}
