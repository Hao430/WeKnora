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
	db, err := gorm.Open(sqlite.Open("file:user_identity?mode=memory&cache=shared"), &gorm.Config{})
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
	ctx := context.Background()

	user := newDingTalkUser("u-ding-1", "13800000000@jgjl.cn")
	require.NoError(t, repo.CreateUserWithIdentity(ctx, user, newDingTalkIdentity("", "union-1")))

	got, err := repo.GetBySubject(ctx, types.IdentityProviderDingTalk, "corp-1", "union-1")
	require.NoError(t, err)
	assert.Equal(t, user.ID, got.UserID)

	// A different provider tenant must not resolve: the same unionId in
	// another corp is another account.
	_, err = repo.GetBySubject(ctx, types.IdentityProviderDingTalk, "corp-2", "union-1")
	assert.ErrorIs(t, err, ErrIdentityNotFound)

	// Tenantless provisioning keeps users.tenant_id NULL.
	var nullTenant int64
	require.NoError(t, db.Table("users").Where("id = ? AND tenant_id IS NULL", user.ID).Count(&nullTenant).Error)
	assert.EqualValues(t, 1, nullTenant)

	identities, err := repo.ListByUserID(ctx, user.ID)
	require.NoError(t, err)
	require.Len(t, identities, 1)
	assert.Equal(t, "union-1", identities[0].Subject)
}

func TestUserIdentityRepositoryRejectsDuplicateBinding(t *testing.T) {
	db := newUserIdentityTestDB(t)
	repo := NewUserIdentityRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.CreateUserWithIdentity(
		ctx, newDingTalkUser("u-a", "a@jgjl.cn"), newDingTalkIdentity("", "union-dup")))
	require.NoError(t, repo.CreateUserWithIdentity(
		ctx, newDingTalkUser("u-b", "b@jgjl.cn"), newDingTalkIdentity("", "union-other")))

	err := repo.Create(ctx, newDingTalkIdentity("u-b", "union-dup"))
	assert.ErrorIs(t, err, ErrIdentityAlreadyBound)
}

// A failed binding insert must roll the account back: otherwise the retry
// finds no identity and re-creates the user, ending on a duplicate email
// instead of recovering.
func TestUserIdentityRepositoryRollsBackUserOnBindingFailure(t *testing.T) {
	db := newUserIdentityTestDB(t)
	repo := NewUserIdentityRepository(db)
	ctx := context.Background()

	require.NoError(t, repo.CreateUserWithIdentity(
		ctx, newDingTalkUser("u-orig", "orig@jgjl.cn"), newDingTalkIdentity("", "union-taken")))

	err := repo.CreateUserWithIdentity(
		ctx, newDingTalkUser("u-new", "new@jgjl.cn"), newDingTalkIdentity("", "union-taken"))
	assert.ErrorIs(t, err, ErrIdentityAlreadyBound)

	var users int64
	require.NoError(t, db.Model(&types.User{}).Where("id = ?", "u-new").Count(&users).Error)
	assert.Zero(t, users, "rolled back account must not survive the failed binding")

	// A duplicate account insert rolls back too, leaving no orphan binding.
	err = repo.CreateUserWithIdentity(
		ctx, newDingTalkUser("u-dup-email", "orig@jgjl.cn"), newDingTalkIdentity("", "union-fresh"))
	require.Error(t, err)
	var identities int64
	require.NoError(t, db.Model(&types.UserIdentity{}).Where("subject = ?", "union-fresh").Count(&identities).Error)
	assert.Zero(t, identities)
}
