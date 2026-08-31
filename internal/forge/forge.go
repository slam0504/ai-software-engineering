// Package forge 定義 Workbench 對 forge（GitHub-first）的最小 port——
// 全部唯讀＋EnsurePullRequest，不含 merge（B5 spec §6）。實作為 C1b 的
// GitHub adapter；本套件僅型別與契約，零外部依賴。
package forge

import "context"

// RepoID／BranchRef／OID 型別明確區分：repo identity、branch ref、commit OID
//（B5 §6——防止字串混用）。
type RepoID struct {
	Owner string
	Repo  string
}
type BranchRef string // refs/heads/... 全名
type OID string       // git commit OID（hex）

type PRRef struct {
	Number int
}
type PRState struct {
	HeadOID OID
	BaseOID OID
	State   string // "open"／"closed"／"merged"
}
type PRMeta struct {
	Title string
	Body  string
}

// RequiredCheckRef 是 required check 的權威 key（B5 §5.1(5)）：
// AppID 為 nil ＝ 不限來源（對齊 branch protection API 的 {context, app_id}）。
type RequiredCheckRef struct {
	Context string
	AppID   *int64
}
type CheckRun struct {
	Name       string
	AppID      int64
	RunID      int64 // repo 範圍內唯一識別一次 check run；Build／VerifyRequiredCheckManifest 據此判定「一 run 至多歸屬一 required check」——adapter 實作必須保證此唯一性
	HeadOID    OID
	Status     string // "queued"／"in_progress"／"completed"
	Conclusion string // completed 時："success"／"failure"／...
	StartedAt  string // RFC3339
}
type RequiredChecks struct {
	Required []RequiredCheckRef
	Runs     []CheckRun
}

type Review struct {
	ReviewID        int64
	ReviewerLogin   string
	State           string // "APPROVED"／"CHANGES_REQUESTED"／"DISMISSED"／"COMMENTED"／"PENDING"
	ReviewedHeadOID OID
	SubmittedAt     string // RFC3339
}

// Permission 是 collaborator permission（B5 §5.1(6) eligibility）。
type Permission string

const (
	PermissionAdmin    Permission = "admin"
	PermissionMaintain Permission = "maintain"
	PermissionWrite    Permission = "write"
	PermissionRead     Permission = "read"
	PermissionNone     Permission = "none"
)

// Eligible 回傳該 permission 是否具 review 效力（write／maintain／admin）。
func (p Permission) Eligible() bool {
	return p == PermissionWrite || p == PermissionMaintain || p == PermissionAdmin
}

// Forge 錯誤語意 fail closed：讀取失敗＝無法決議，不得當作
// checks 未設定或 review 不存在（B5 §6）。
type Forge interface {
	// EnsurePullRequest 以 (repo, headRef, baseRef, taskRunID marker) 確定性收斂：
	// 既有 open PR 同 head/base 且 marker 相符→回傳之；marker 不符或同
	// head/base 多筆→fail loud；不存在→建立（body/label 帶 taskrun:<ULID>）。
	EnsurePullRequest(ctx context.Context, repo RepoID, headRef, baseRef BranchRef, taskRunID string, meta PRMeta) (PRRef, error)
	GetPullRequest(ctx context.Context, repo RepoID, pr PRRef) (PRState, error)
	GetRequiredChecks(ctx context.Context, repo RepoID, pr PRRef, head OID) (RequiredChecks, error)
	GetReviews(ctx context.Context, repo RepoID, pr PRRef) ([]Review, error)
	GetCollaboratorPermission(ctx context.Context, repo RepoID, login string) (Permission, error)
}
