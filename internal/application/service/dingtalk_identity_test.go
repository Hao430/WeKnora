package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
)

func TestDingTalkListIdentitiesProjectsDisplayName(t *testing.T) {
	users := newDirFakeUsers()
	identities := newDirFakeIdentities()
	ctx := context.Background()

	if err := identities.Create(ctx, &types.UserIdentity{
		UserID: "u1", Provider: types.IdentityProviderDingTalk, ProviderTenant: "corp1",
		Subject: "union1", ExternalID: "dt1", DisplayName: "张三",
	}); err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	if err := identities.Create(ctx, &types.UserIdentity{
		UserID: "u2", Provider: types.IdentityProviderDingTalk, ProviderTenant: "corp1",
		Subject: "union2",
	}); err != nil {
		t.Fatalf("seed identity: %v", err)
	}

	svc := NewDingTalkAuthService(identities, users, nil, nil, nil,
		&config.Config{DingTalkAuth: &config.DingTalkAuthConfig{CorpID: "corp1"}}).(*dingTalkAuthService)

	rows, err := svc.ListIdentities(ctx, "u1")
	if err != nil {
		t.Fatalf("ListIdentities: %v", err)
	}
	if len(rows) != 1 || rows[0].Provider != types.IdentityProviderDingTalk || rows[0].DisplayName != "张三" {
		t.Fatalf("unexpected rows: %+v", rows)
	}

	// Other users' bindings must never leak into the caller's list.
	other, err := svc.ListIdentities(ctx, "u2")
	if err != nil {
		t.Fatalf("ListIdentities: %v", err)
	}
	if len(other) != 1 || other[0].DisplayName != "" {
		t.Fatalf("unexpected rows for u2: %+v", other)
	}

	empty, err := svc.ListIdentities(ctx, "")
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty user id must return no rows, got %+v err=%v", empty, err)
	}
}
