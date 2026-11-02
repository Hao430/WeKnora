package service

import (
	"context"
	"testing"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/infrastructure/dingtalk"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type fakeDingTalkClient struct {
	profile *dingtalk.Profile
	err     error
	calls   int
}

func (f *fakeDingTalkClient) GetUserByInnerCode(_ context.Context, _ string) (*dingtalk.Profile, error) {
	f.calls++
	return f.profile, f.err
}

func (f *fakeDingTalkClient) GetUserByOAuthCode(_ context.Context, _ string) (*dingtalk.Profile, error) {
	f.calls++
	return f.profile, f.err
}

type fakeUserStore struct {
	users     map[string]*types.User
	register  []*types.RegisterRequest
	regErr    error
	deleted   []string
	updated   []*types.User
	tokenFail error
}

func newFakeUserStore() *fakeUserStore {
	return &fakeUserStore{users: map[string]*types.User{}}
}

func (f *fakeUserStore) seed(u *types.User) {
	f.users[u.ID] = u
}

func (f *fakeUserStore) GetUserByID(_ context.Context, id string) (*types.User, error) {
	if u, ok := f.users[id]; ok {
		return u, nil
	}
	return nil, apprepo.ErrUserNotFound
}

func (f *fakeUserStore) GetUserByEmail(_ context.Context, email string) (*types.User, error) {
	for _, u := range f.users {
		if u.Email == email {
			return u, nil
		}
	}
	return nil, apprepo.ErrUserNotFound
}

func (f *fakeUserStore) GetUserByUsername(_ context.Context, username string) (*types.User, error) {
	for _, u := range f.users {
		if u.Username == username {
			return u, nil
		}
	}
	return nil, apprepo.ErrUserNotFound
}

func (f *fakeUserStore) Register(_ context.Context, req *types.RegisterRequest) (*types.User, error) {
	f.register = append(f.register, req)
	if f.regErr != nil {
		return nil, f.regErr
	}
	u := &types.User{
		ID:       "user-" + req.Username,
		Username: req.Username,
		Email:    req.Email,
		IsActive: true,
	}
	f.users[u.ID] = u
	return u, nil
}

func (f *fakeUserStore) UpdateUser(_ context.Context, user *types.User) error {
	f.updated = append(f.updated, user)
	f.users[user.ID] = user
	return nil
}

func (f *fakeUserStore) DeleteUser(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	delete(f.users, id)
	return nil
}

func (f *fakeUserStore) GenerateTokens(_ context.Context, _ *types.User) (string, string, error) {
	if f.tokenFail != nil {
		return "", "", f.tokenFail
	}
	return "access", "refresh", nil
}

type fakeInviteStore struct {
	inv        *types.TenantInvitation
	lookupErr  error
	acceptErr  error
	acceptedBy []string
}

func (f *fakeInviteStore) LookupByToken(_ context.Context, _ string) (*types.TenantInvitation, error) {
	if f.lookupErr != nil {
		return nil, f.lookupErr
	}
	return f.inv, nil
}

func (f *fakeInviteStore) AcceptByToken(_ context.Context, _ string, userID string) (*types.TenantMember, error) {
	if f.acceptErr != nil {
		return nil, f.acceptErr
	}
	f.acceptedBy = append(f.acceptedBy, userID)
	return &types.TenantMember{UserID: userID}, nil
}

func newTestIdentityRepo(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.UserIdentity{}))
	return db
}

type harness struct {
	svc      *dingTalkAuthService
	users    *fakeUserStore
	invites  *fakeInviteStore
	client   *fakeDingTalkClient
	identity interfaces.UserIdentityRepository
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	users := newFakeUserStore()
	invites := &fakeInviteStore{inv: &types.TenantInvitation{ID: 1, TenantID: 42, Role: types.TenantRoleViewer}}
	client := &fakeDingTalkClient{profile: &dingtalk.Profile{UnionID: "union-1", UserID: "dt-1", Nick: "张三", Mobile: "13800000000"}}
	repo := apprepo.NewUserIdentityRepository(newTestIdentityRepo(t))
	svc := &dingTalkAuthService{
		identityRepo: repo,
		users:        users,
		invitations:  invites,
		client:       client,
		corpID:       "corp-1",
	}
	return &harness{svc: svc, users: users, invites: invites, client: client, identity: repo}
}

func (h *harness) login(t *testing.T, mode, token string) (*types.DingTalkLoginResult, error) {
	t.Helper()
	return h.svc.Resolve(context.Background(), types.DingTalkLoginInput{
		Code:             "code-1",
		Source:           types.DingTalkSourceInner,
		InviteToken:      token,
		RegistrationMode: mode,
	})
}

func TestDingTalkResolveProvisionsSelfServeAccount(t *testing.T) {
	h := newHarness(t)
	result, err := h.login(t, config.AuthRegistrationModeSelfServe, "")
	require.NoError(t, err)

	assert.True(t, result.IsNewUser)
	assert.Equal(t, "access", result.AccessToken)
	require.Len(t, h.users.register, 1)
	assert.Equal(t, "13800000000@jgjl.cn", h.users.register[0].Email)
	assert.Equal(t, "13800000000", h.users.register[0].Username, "the mobile number is the readable username")
	assert.Empty(t, h.users.register[0].TenantProvisioning, "self-serve keeps Register's default tenant mode")

	identity, err := h.identity.GetBySubject(context.Background(), types.IdentityProviderDingTalk, "corp-1", "union-1")
	require.NoError(t, err)
	assert.Equal(t, result.User.ID, identity.UserID)

	// The account is SSO-only: nobody received its random password.
	require.NotNil(t, result.User.Preferences.OidcOnlyLogin)
	assert.True(t, *result.User.Preferences.OidcOnlyLogin)
}

func TestDingTalkResolveRequiresInviteWhenRegistrationIsClosed(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		token   string
		wantErr error
	}{
		{"invite_only without link", config.AuthRegistrationModeInviteOnly, "", ErrDingTalkInviteRequired},
		{"invite_only with link", config.AuthRegistrationModeInviteOnly, "invite-1", ErrDingTalkRegistrationDisabled},
		{"invite_register without link", config.AuthRegistrationModeInviteRegister, "", ErrDingTalkInviteRequired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			_, err := h.login(t, tt.mode, tt.token)
			assert.ErrorIs(t, err, tt.wantErr)
			assert.Empty(t, h.users.register, "no account may be created when准入 fails")
		})
	}
}

func TestDingTalkResolveJoinsInvitedTenant(t *testing.T) {
	for _, mode := range []string{config.AuthRegistrationModeSelfServe, config.AuthRegistrationModeInviteRegister} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t)
			result, err := h.login(t, mode, "invite-1")
			require.NoError(t, err)

			require.Len(t, h.users.register, 1)
			assert.Equal(t, types.TenantProvisioningTenantless, h.users.register[0].TenantProvisioning,
				"invited accounts join through AcceptByToken, not Register's own tenant")
			assert.EqualValues(t, 42, result.User.TenantID)
			assert.Equal(t, []string{result.User.ID}, h.invites.acceptedBy)
		})
	}
}

func TestDingTalkResolveRejectsIncompleteProfiles(t *testing.T) {
	t.Run("missing mobile", func(t *testing.T) {
		h := newHarness(t)
		h.client.profile = &dingtalk.Profile{UnionID: "union-1", Active: boolPtr(true)}
		_, err := h.login(t, config.AuthRegistrationModeSelfServe, "")
		assert.ErrorIs(t, err, ErrDingTalkMobileRequired)
	})

	t.Run("disabled dingtalk account", func(t *testing.T) {
		h := newHarness(t)
		h.client.profile = &dingtalk.Profile{UnionID: "union-1", Mobile: "13800000000", Active: boolPtr(false)}
		_, err := h.login(t, config.AuthRegistrationModeSelfServe, "")
		assert.ErrorIs(t, err, ErrDingTalkAccountDisabled)
	})

	t.Run("placeholder email already taken", func(t *testing.T) {
		h := newHarness(t)
		h.users.seed(&types.User{ID: "local", Email: "13800000000@jgjl.cn", IsActive: true})
		_, err := h.login(t, config.AuthRegistrationModeSelfServe, "")
		assert.ErrorIs(t, err, ErrDingTalkEmailTaken)
		assert.Empty(t, h.users.register)
	})
}

func TestDingTalkResolveReusesBoundIdentity(t *testing.T) {
	h := newHarness(t)
	first, err := h.login(t, config.AuthRegistrationModeSelfServe, "")
	require.NoError(t, err)

	second, err := h.login(t, config.AuthRegistrationModeInviteOnly, "")
	require.NoError(t, err, "an existing binding logs in regardless of registration mode")
	assert.False(t, second.IsNewUser)
	assert.Equal(t, first.User.ID, second.User.ID)
	assert.Len(t, h.users.register, 1, "no second account")
}

func TestDingTalkResolveReusesWinnerOfConcurrentFirstLogin(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// Simulate the winner: an account plus its binding already exist, but our
	// earlier GetBySubject ran before that row was committed.
	winner := &types.User{ID: "winner", Email: "13800000000@jgjl.cn", IsActive: true}
	h.users.seed(winner)
	require.NoError(t, h.identity.Create(ctx, &types.UserIdentity{
		UserID: "winner", Provider: types.IdentityProviderDingTalk, ProviderTenant: "corp-1", Subject: "union-1",
	}))
	h.users.regErr = ErrUserEmailExists

	result, err := h.login(t, config.AuthRegistrationModeSelfServe, "")
	require.NoError(t, err)
	assert.False(t, result.IsNewUser)
	assert.Equal(t, "winner", result.User.ID)
}

func TestDingTalkResolveDropsDuplicateAccountWhenBindingRaces(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// The identity row exists but GetBySubject answered "not found" first; the
	// duplicate account created in between must be dropped, not kept.
	require.NoError(t, h.identity.Create(ctx, &types.UserIdentity{
		UserID: "winner", Provider: types.IdentityProviderDingTalk, ProviderTenant: "corp-1", Subject: "union-1",
	}))
	h.users.seed(&types.User{ID: "winner", Email: "someone@jgjl.cn", IsActive: true})
	h.svc.identityRepo = &racingIdentityRepo{UserIdentityRepository: h.identity, stale: true}

	_, err := h.login(t, config.AuthRegistrationModeSelfServe, "")
	require.NoError(t, err)
	require.Len(t, h.users.deleted, 1, "the duplicate account must be removed")
	assert.NotEqual(t, "winner", h.users.deleted[0])
}

func TestDingTalkBindRejectsDuplicateIdentity(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.users.seed(&types.User{ID: "me", IsActive: true})
	require.NoError(t, h.svc.Bind(ctx, "me", types.DingTalkBindInput{Code: "c", Source: types.DingTalkSourceOAuth}))

	require.NoError(t, h.identity.Create(ctx, &types.UserIdentity{
		UserID: "other", Provider: types.IdentityProviderDingTalk, ProviderTenant: "corp-1", Subject: "union-2",
	}))
	h.users.seed(&types.User{ID: "other", IsActive: true})
	h.client.profile = &dingtalk.Profile{UnionID: "union-2", Active: boolPtr(true)}
	err := h.svc.Bind(ctx, "me", types.DingTalkBindInput{Code: "c", Source: types.DingTalkSourceOAuth})
	assert.ErrorIs(t, err, apprepo.ErrIdentityAlreadyBound)
}

// racingIdentityRepo hides an existing binding from the first lookup, the way
// a concurrent transaction's uncommitted row would.
type racingIdentityRepo struct {
	interfaces.UserIdentityRepository
	stale bool
}

func (r *racingIdentityRepo) GetBySubject(
	ctx context.Context, provider, providerTenant, subject string,
) (*types.UserIdentity, error) {
	if r.stale {
		r.stale = false
		return nil, apprepo.ErrIdentityNotFound
	}
	return r.UserIdentityRepository.GetBySubject(ctx, provider, providerTenant, subject)
}
