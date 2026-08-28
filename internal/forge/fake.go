package forge

import "context"

// Fake 是測試用 Forge：每個方法回傳對應欄位；Err 非 nil 時一律回傳 Err
//（模擬 forge 讀取失敗的 fail-closed 路徑）。沿 gate stubPolicy 慣例：
// production-adjacent fake，不進 _test.go 因跨套件使用（gatepolicy 測試）。
type Fake struct {
	Err            error
	PR             PRRef
	PRState        PRState
	RequiredChecks RequiredChecks
	Reviews        []Review
	Permissions    map[string]Permission // login → permission；缺項回 PermissionNone
}

var _ Forge = (*Fake)(nil)

func (f *Fake) EnsurePullRequest(_ context.Context, _ RepoID, _, _ BranchRef, _ string, _ PRMeta) (PRRef, error) {
	if f.Err != nil {
		return PRRef{}, f.Err
	}
	return f.PR, nil
}
func (f *Fake) GetPullRequest(_ context.Context, _ RepoID, _ PRRef) (PRState, error) {
	if f.Err != nil {
		return PRState{}, f.Err
	}
	return f.PRState, nil
}
func (f *Fake) GetRequiredChecks(_ context.Context, _ RepoID, _ PRRef, _ OID) (RequiredChecks, error) {
	if f.Err != nil {
		return RequiredChecks{}, f.Err
	}
	return f.RequiredChecks, nil
}
func (f *Fake) GetReviews(_ context.Context, _ RepoID, _ PRRef) ([]Review, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	return f.Reviews, nil
}
func (f *Fake) GetCollaboratorPermission(_ context.Context, _ RepoID, login string) (Permission, error) {
	if f.Err != nil {
		return PermissionNone, f.Err
	}
	if p, ok := f.Permissions[login]; ok {
		return p, nil
	}
	return PermissionNone, nil
}
