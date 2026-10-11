package main

import (
	"errors"
	"fmt"
	"sync"
)

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

// ---- 確認狀態（spec §3.1、§5.2）----

type workspaceConfirmState int

const (
	// wcUnresolved：零值——workspace 還沒解析過。fail closed：閘門拒絕、ConfirmWorkspace
	// 也不接受（否則空字串的 expectedPath 會「符合」空的快照）。
	wcUnresolved workspaceConfirmState = iota
	wcNotRequired
	wcPending
	wcConfirmed
)

const wsReasonUnresolved = "workspace_unresolved"

// 三種拒絕原因都以固定前綴開頭，呼叫端（前端、audit、測試）可以用前綴辨識。
var (
	errWorkspaceConfirmationRequired = errors.New("workspace_confirmation_required")
	errWorkspaceConfirmPathMismatch  = errors.New("workspace_confirmation_path_mismatch")
	errWorkspaceConfirmUnavailable   = errors.New("workspace_confirmation_unavailable")
)

// workspaceConfirmation：確認狀態＋對應的 workspace 快照，受自己的 mutex 保護。
// 鎖內只有欄位讀寫與字串／slice 複製，不做 I/O、不呼叫 a.audit（audit 一律在鎖外寫，
// 避免鎖序問題，也讓「是否為第一次確認」只由 confirm 的單一臨界區決定）。
type workspaceConfirmation struct {
	mu        sync.Mutex
	state     workspaceConfirmState
	workspace string
	source    string
	reasons   []string
}

type workspaceConfirmSnapshot struct {
	state     workspaceConfirmState
	workspace string
	source    string
	reasons   []string
}

// resolve：workspace 決定的當下計算 policy 並寫入（與 publishWorkspace 同一個時機）。
func (c *workspaceConfirmation) resolve(r workspaceResolution) {
	required, reasons := workspacePolicy(r.Source, r.Provenance)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.workspace, c.source, c.reasons = r.Workspace, r.Source, reasons
	if required {
		c.state = wcPending
	} else {
		c.state = wcNotRequired
	}
}

func (c *workspaceConfirmation) snapshot() workspaceConfirmSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return workspaceConfirmSnapshot{state: c.state, workspace: c.workspace, source: c.source,
		reasons: append([]string{}, c.reasons...)}
}

// confirm：R3。路徑核對與狀態轉換在同一個臨界區內完成：
//  1. 尚未解析 → 拒絕；
//  2. expectedPath 與快照的 workspace 字串不完全相同 → 拒絕（不論目前狀態），狀態不變；
//  3. 路徑相同：Pending → Confirmed（transitioned=true，只有這一次呼叫會拿到 true）；
//     Confirmed／NotRequired → 成功，狀態不變（transitioned=false）。
func (c *workspaceConfirmation) confirm(expectedPath string) (transitioned bool, snap workspaceConfirmSnapshot, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	snap = workspaceConfirmSnapshot{state: c.state, workspace: c.workspace, source: c.source,
		reasons: append([]string{}, c.reasons...)}
	if c.state == wcUnresolved {
		return false, snap, fmt.Errorf("%w：workspace 尚未解析完成", errWorkspaceConfirmUnavailable)
	}
	if expectedPath != c.workspace {
		return false, snap, fmt.Errorf("%w：畫面上的目錄與後端實際的 workspace（%s）不一致",
			errWorkspaceConfirmPathMismatch, c.workspace)
	}
	if c.state == wcPending {
		c.state = wcConfirmed
		snap.state = wcConfirmed
		return true, snap, nil
	}
	return false, snap, nil
}

// WorkspaceConfirmationInfo：WorkspaceConfirmationStatus 的回傳（Wails 會產生對應的 TS 型別）。
// NotRequired：required=false、confirmed=false；Pending：required=true、confirmed=false；
// Confirmed：required=true、confirmed=true。前端的停用條件是 required && !confirmed。
type WorkspaceConfirmationInfo struct {
	Required  bool     `json:"required"`
	Confirmed bool     `json:"confirmed"`
	Workspace string   `json:"workspace"`
	Source    string   `json:"source"`
	Reasons   []string `json:"reasons"`
}

func (s workspaceConfirmSnapshot) info() WorkspaceConfirmationInfo {
	reasons := s.reasons
	if s.state == wcUnresolved {
		reasons = []string{wsReasonUnresolved}
	}
	return WorkspaceConfirmationInfo{
		Required:  s.state != wcNotRequired,
		Confirmed: s.state == wcConfirmed,
		Workspace: s.workspace,
		Source:    s.source,
		Reasons:   append([]string{}, reasons...),
	}
}

func (a *App) workspaceConfirmationStatus() (WorkspaceConfirmationInfo, error) {
	return a.workspaceConfirm.snapshot().info(), nil
}

func (a *App) confirmWorkspace(expectedPath string) error {
	_, _, err := a.workspaceConfirm.confirm(expectedPath)
	return err
}
