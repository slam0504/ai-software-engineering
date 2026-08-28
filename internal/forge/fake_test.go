package forge

import (
	"context"
	"errors"
	"testing"
)

func TestFakeErrShortCircuitsAllMethods(t *testing.T) {
	f := &Fake{Err: errors.New("boom")}
	ctx := context.Background()
	if _, err := f.GetPullRequest(ctx, RepoID{}, PRRef{}); err == nil {
		t.Fatal("GetPullRequest 應回傳 Err")
	}
	if _, err := f.GetRequiredChecks(ctx, RepoID{}, PRRef{}, OID("x")); err == nil {
		t.Fatal("GetRequiredChecks 應回傳 Err")
	}
	if _, err := f.GetReviews(ctx, RepoID{}, PRRef{}); err == nil {
		t.Fatal("GetReviews 應回傳 Err")
	}
	if _, err := f.GetCollaboratorPermission(ctx, RepoID{}, "u"); err == nil {
		t.Fatal("GetCollaboratorPermission 應回傳 Err")
	}
}

func TestPermissionEligible(t *testing.T) {
	for p, want := range map[Permission]bool{
		PermissionAdmin: true, PermissionMaintain: true, PermissionWrite: true,
		PermissionRead: false, PermissionNone: false,
	} {
		if p.Eligible() != want {
			t.Fatalf("%s.Eligible()=%v, want %v", p, !want, want)
		}
	}
}

func TestFakePermissionDefaultNone(t *testing.T) {
	f := &Fake{}
	p, err := f.GetCollaboratorPermission(context.Background(), RepoID{}, "stranger")
	if err != nil || p != PermissionNone {
		t.Fatalf("缺項應回 PermissionNone, got %v %v", p, err)
	}
}
