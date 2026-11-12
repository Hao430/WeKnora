package repository

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newUserIdentityTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.Tenant{}, &types.User{}, &types.UserIdentity{}))
	return db
}

func newDingTalkUser(id, email string) *types.User {
	return &types.User{
		ID:           id,
		Username:     id,
		Email:        email,
		PasswordHash: "hashed",
		IsActive:     true,
	}
}

func newDingTalkIdentity(userID, subject string) *types.UserIdentity {
	return &types.UserIdentity{
		UserID:         userID,
		Provider:       types.IdentityProviderDingTalk,
		ProviderTenant: "corp-1",
		Subject:        subject,
	}
}

func TestUserIdentityRepositoryCreateAndResolve(t *testing.T) {
	db := newUserIdentityTestDB(t)
	repo := NewUserIdentityRepository(db)
	users := NewUserRepository(db)
	ctx := context.Background()

	user := newDingTalkUser("u-ding-1", "13800000000@jgjl.cn")
	require.NoError(t, users.CreateUser(ctx, user))
	require.NoError(t, repo.Create(ctx, newDingTalkIdentity(user.ID, "union-1")))

	got, err := repo.GetBySubject(ctx, types.IdentityProviderDingTalk, "corp-1", "union-1")
	require.NoError(t, err)
	assert.Equal(t, user.ID, got.UserID)

	// A different provider tenant must not resolve: the same unionId in
	// another corp is another account.
	_, err = repo.GetBySubject(ctx, types.IdentityProviderDingTalk, "corp-2", "union-1")
	assert.ErrorIs(t, err, ErrIdentityNotFound)

	identities, err := repo.ListByUserID(ctx, user.ID)
	require.NoError(t, err)
	require.Len(t, identities, 1)
	assert.Equal(t, "union-1", identities[0].Subject)
}

func TestUserIdentityRepositoryRejectsDuplicateBinding(t *testing.T) {
	db := newUserIdentityTestDB(t)
	repo := NewUserIdentityRepository(db)
	users := NewUserRepository(db)
	ctx := context.Background()

	require.NoError(t, users.CreateUser(ctx, newDingTalkUser("u-a", "a@jgjl.cn")))
	require.NoError(t, users.CreateUser(ctx, newDingTalkUser("u-b", "b@jgjl.cn")))
	require.NoError(t, repo.Create(ctx, newDingTalkIdentity("u-a", "union-dup")))

	// One DingTalk account can only ever resolve to one local account.
	err := repo.Create(ctx, newDingTalkIdentity("u-b", "union-dup"))
	assert.ErrorIs(t, err, ErrIdentityAlreadyBound)

	count, err := repo.ListByUserID(ctx, "u-b")
	require.NoError(t, err)
	assert.Empty(t, count)
}

func TestUserIdentityRepositoryExternalID(t *testing.T) {
	db := newUserIdentityTestDB(t)
	repo := NewUserIdentityRepository(db)
	users := NewUserRepository(db)
	ctx := context.Background()

	user := newDingTalkUser("u-ding-ext", "13800000001@jgjl.cn")
	require.NoError(t, users.CreateUser(ctx, user))

	identity := newDingTalkIdentity(user.ID, "union-ext")
	identity.ExternalID = "dt-userid-1"
	require.NoError(t, repo.Create(ctx, identity))

	got, err := repo.GetByExternalID(ctx, types.IdentityProviderDingTalk, "corp-1", "dt-userid-1")
	require.NoError(t, err)
	assert.Equal(t, "union-ext", got.Subject)

	// An empty userid must never match: it would resolve an arbitrary row
	// whose external_id was never backfilled.
	_, err = repo.GetByExternalID(ctx, types.IdentityProviderDingTalk, "corp-1", "  ")
	require.ErrorIs(t, err, ErrIdentityNotFound)

	// Login-bound identities start without a userid; the directory sync
	// backfills it, and the leave event then resolves through it.
	legacy := newDingTalkIdentity(user.ID, "union-legacy")
	require.NoError(t, repo.Create(ctx, legacy))
	_, err = repo.GetByExternalID(ctx, types.IdentityProviderDingTalk, "corp-1", "dt-userid-2")
	require.ErrorIs(t, err, ErrIdentityNotFound)

	legacy.ExternalID = "dt-userid-2"
	require.NoError(t, repo.Update(ctx, legacy))
	got, err = repo.GetByExternalID(ctx, types.IdentityProviderDingTalk, "corp-1", "dt-userid-2")
	require.NoError(t, err)
	assert.Equal(t, "union-legacy", got.Subject)
}

func TestUserIdentityRepositoryDisplayName(t *testing.T) {
	db := newUserIdentityTestDB(t)
	repo := NewUserIdentityRepository(db)
	users := NewUserRepository(db)
	ctx := context.Background()

	user := newDingTalkUser("u-ding-name", "13800000002@jgjl.cn")
	require.NoError(t, users.CreateUser(ctx, user))

	identity := newDingTalkIdentity(user.ID, "union-name")
	identity.DisplayName = "张三"
	require.NoError(t, repo.Create(ctx, identity))

	got, err := repo.GetBySubject(ctx, types.IdentityProviderDingTalk, "corp-1", "union-name")
	require.NoError(t, err)
	assert.Equal(t, "张三", got.DisplayName)

	// The directory sync backfills both external_id and display_name through
	// the same Update; the projection shown in the profile must follow.
	got.ExternalID = "dt-userid-name"
	got.DisplayName = "张三（改）"
	require.NoError(t, repo.Update(ctx, got))

	reloaded, err := repo.GetBySubject(ctx, types.IdentityProviderDingTalk, "corp-1", "union-name")
	require.NoError(t, err)
	assert.Equal(t, "dt-userid-name", reloaded.ExternalID)
	assert.Equal(t, "张三（改）", reloaded.DisplayName)
}
