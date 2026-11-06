package service

import (
	"context"
	"fmt"
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

func (f *fakeDingTalkClient) GetUserByMobile(_ context.Context, _ string) (*dingtalk.Profile, error) {
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

type fakeMemberStore struct {
	added      []string
	err        error
	membership map[string]*types.TenantMember // key: actorID:tenantID
}

func (f *fakeMemberStore) AddMember(_ context.Context, userID string, tenantID uint64, role types.TenantRole, _ *string) (*types.TenantMember, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.added = append(f.added, fmt.Sprintf("%s:%d:%s", userID, tenantID, role))
	return &types.TenantMember{UserID: userID, TenantID: tenantID, Role: role}, nil
}

func (f *fakeMemberStore) GetMembership(_ context.Context, userID string, tenantID uint64) (*types.TenantMember, error) {
	if m, ok := f.membership[fmt.Sprintf("%s:%d", userID, tenantID)]; ok {
		return m, nil
	}
	return nil, ErrMembershipNotFound
}

type harness struct {
	svc      *dingTalkAuthService
	users    *fakeUserStore
	invites  *fakeInviteStore
	members  *fakeMemberStore
	client   *fakeDingTalkClient
	identity interfaces.UserIdentityRepository
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	users := newFakeUserStore()
	invites := &fakeInviteStore{inv: &types.TenantInvitation{ID: 1, TenantID: 42, Role: types.TenantRoleViewer}}
	client := &fakeDingTalkClient{profile: &dingtalk.Profile{
		UnionID: "union-1", UserID: "dt-1", Nick: "张三", Mobile: "13800000000",
	}}
	repo := apprepo.NewUserIdentityRepository(newTestIdentityRepo(t))
	members := &fakeMemberStore{}
	svc := &dingTalkAuthService{
		identityRepo: repo,
		users:        users,
		invitations:  invites,
		members:      members,
		client:       client,
		corpID:       "corp-1",
	}
	return &harness{svc: svc, users: users, invites: invites, members: members, client: client, identity: repo}
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
	assert.Equal(t, "张三", h.users.register[0].Username, "the dingtalk display name is the readable username")
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

func TestDingTalkResolveRejectsLocallyDisabledAccount(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.users.seed(&types.User{ID: "blocked", IsActive: false})
	require.NoError(t, h.identity.Create(ctx, &types.UserIdentity{
		UserID: "blocked", Provider: types.IdentityProviderDingTalk, ProviderTenant: "corp-1", Subject: "union-1",
	}))

	_, err := h.login(t, config.AuthRegistrationModeSelfServe, "")
	assert.ErrorIs(t, err, ErrDingTalkAccountDisabled)
}

// A link that dies between lookup and accept must not leave a home tenant the
// user is not a member of: the next login with a valid link retries the join.
func TestDingTalkResolveRestoresTenantlessWhenInviteAcceptFails(t *testing.T) {
	h := newHarness(t)
	h.invites.acceptErr = ErrInvitationTokenInvalid

	_, err := h.login(t, config.AuthRegistrationModeInviteRegister, "invite-1")
	assert.ErrorIs(t, err, ErrInvitationTokenInvalid)

	created := h.users.users["user-张三"]
	require.NotNil(t, created)
	assert.Zero(t, created.TenantID, "failed invite must not leave a home tenant behind")
	assert.Len(t, h.users.register, 1, "the account itself is kept so a valid link can heal it")
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
	// Re-binding the same account is idempotent: the settings page has no
	// status query, so a second click must not surface a conflict.
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

func TestDingTalkUsernameFollowsDisplayName(t *testing.T) {
	tests := []struct {
		name   string
		nick   string
		mobile string
		want   string
	}{
		{"chinese name", "张三", "13800000000", "张三"},
		{"latin name", "Zhang San", "13800000000", "Zhang-San"},
		{"punctuation collapses", "  李  四（研发） ", "13800000000", "李-四-研发"},
		{"missing nick falls back to mobile", "", "13800000000", "13800000000"},
		{"blank nick falls back to mobile", "   ", "13800000000", "13800000000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.client.profile = &dingtalk.Profile{UnionID: "union-1", Nick: tt.nick, Mobile: tt.mobile}
			_, err := h.login(t, config.AuthRegistrationModeSelfServe, "")
			require.NoError(t, err)
			require.Len(t, h.users.register, 1)
			assert.Equal(t, tt.want, h.users.register[0].Username)
			assert.Equal(t, tt.mobile+"@jgjl.cn", h.users.register[0].Email,
				"the placeholder email keeps deriving from the mobile number")
		})
	}
}

func TestDingTalkUsernameSuffixesOnCollision(t *testing.T) {
	h := newHarness(t)
	h.users.seed(&types.User{ID: "first", Username: "张三", IsActive: true})

	_, err := h.login(t, config.AuthRegistrationModeSelfServe, "")
	require.NoError(t, err)
	require.Len(t, h.users.register, 1)
	assert.Equal(t, "张三-1", h.users.register[0].Username)
}

// Accounts provisioned while the mobile number was the username are renamed on
// their next login; repeated logins are stable and do not rename again.
func TestDingTalkResolveRenamesExistingAccountOnLogin(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.users.seed(&types.User{ID: "old", Username: "13800000000", Email: "13800000000@jgjl.cn", IsActive: true})
	require.NoError(t, h.identity.Create(ctx, &types.UserIdentity{
		UserID: "old", Provider: types.IdentityProviderDingTalk, ProviderTenant: "corp-1", Subject: "union-1",
	}))

	result, err := h.login(t, config.AuthRegistrationModeSelfServe, "")
	require.NoError(t, err)
	assert.Equal(t, "old", result.User.ID)
	assert.Equal(t, "张三", result.User.Username)
	assert.Len(t, h.users.updated, 1, "the rename is persisted once")

	_, err = h.login(t, config.AuthRegistrationModeSelfServe, "")
	require.NoError(t, err)
	assert.Len(t, h.users.updated, 1, "an already-synced name is not renamed again")
}

// A user who already holds the suffixed name must keep it instead of chasing
// the name another account took first.
func TestDingTalkUsernameKeepsExistingSuffix(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.users.seed(&types.User{ID: "other", Username: "张三", IsActive: true})
	h.users.seed(&types.User{ID: "me", Username: "张三-1", Email: "13800000000@jgjl.cn", IsActive: true})
	require.NoError(t, h.identity.Create(ctx, &types.UserIdentity{
		UserID: "me", Provider: types.IdentityProviderDingTalk, ProviderTenant: "corp-1", Subject: "union-1",
	}))

	result, err := h.login(t, config.AuthRegistrationModeSelfServe, "")
	require.NoError(t, err)
	assert.Equal(t, "张三-1", result.User.Username)
	assert.Empty(t, h.users.updated, "a stable name needs no write")
}

// 绑定钉钉的本地账号（例如使用自己邮箱注册的超管）保留原用户名，
// 钉钉昵称只用于钉钉自己开出来的占位账号。
func TestDingTalkResolveKeepsLocalUsernameForBoundEmailAccount(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.users.seed(&types.User{ID: "admin", Username: "admin", Email: "admin@jgjl.cn", IsActive: true})
	require.NoError(t, h.identity.Create(ctx, &types.UserIdentity{
		UserID: "admin", Provider: types.IdentityProviderDingTalk, ProviderTenant: "corp-1", Subject: "union-1",
	}))

	result, err := h.login(t, config.AuthRegistrationModeSelfServe, "")
	require.NoError(t, err)
	assert.Equal(t, "admin", result.User.ID)
	assert.Equal(t, "admin", result.User.Username)
	assert.Empty(t, h.users.updated, "a bound local account must not be renamed")
}

func TestAdminProvisionCreatesBoundAccount(t *testing.T) {
	h := newHarness(t)
	user, idempotent, err := h.svc.AdminProvision(context.Background(), &types.AdminProvisionDingTalkUserRequest{
		Mobile: "13800000000",
	})
	require.NoError(t, err)
	assert.False(t, idempotent)

	require.Len(t, h.users.register, 1)
	assert.Equal(t, "13800000000@jgjl.cn", h.users.register[0].Email)
	assert.Equal(t, types.TenantProvisioningTenantless, h.users.register[0].TenantProvisioning,
		"admin provisioning never auto-creates a personal space")

	identity, err := h.identity.GetBySubject(context.Background(), types.IdentityProviderDingTalk, "corp-1", "union-1")
	require.NoError(t, err)
	assert.Equal(t, user.ID, identity.UserID)
	assert.Empty(t, h.members.added, "no tenant requested, no membership")
}

func TestAdminProvisionEnrollsTenantWithDefaultRole(t *testing.T) {
	h := newHarness(t)
	adminOf42(t, h)
	user, _, err := h.svc.AdminProvision(adminCtx(), &types.AdminProvisionDingTalkUserRequest{
		Mobile:   "13800000000",
		TenantID: 42,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{fmt.Sprintf("%s:42:%s", user.ID, types.TenantRoleContributor)}, h.members.added)
}

func TestAdminProvisionRejectsNonAdminTenant(t *testing.T) {
	// 各用例用子测试隔离：newTestIdentityRepo 按测试名共享内存库。
	cases := []struct {
		name string
		role types.TenantRole
		seed bool
	}{
		{"contributor", types.TenantRoleContributor, true},
		{"viewer", types.TenantRoleViewer, true},
		{"no membership", "", false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			if tt.seed {
				h.members.membership = map[string]*types.TenantMember{
					"admin-1:42": {UserID: "admin-1", TenantID: 42, Role: tt.role},
				}
			}
			_, _, err := h.svc.AdminProvision(adminCtx(), &types.AdminProvisionDingTalkUserRequest{
				Mobile: "13800000000", TenantID: 42,
			})
			assert.ErrorIs(t, err, ErrDingTalkTenantForbidden)
			assert.Empty(t, h.users.register, "no account may be created when the actor is not a tenant admin")
		})
	}
}

// adminCtx 模拟经过认证中间件后的请求上下文。
func adminCtx() context.Context {
	return context.WithValue(context.Background(), types.UserIDContextKey, "admin-1")
}

// adminOf42 让 admin-1 成为空间 42 的管理员。
func adminOf42(t *testing.T, h *harness) {
	t.Helper()
	h.members.membership = map[string]*types.TenantMember{
		"admin-1:42": {UserID: "admin-1", TenantID: 42, Role: types.TenantRoleAdmin},
	}
}

func TestAdminProvisionIsIdempotent(t *testing.T) {
	h := newHarness(t)
	adminOf42(t, h)
	first, _, err := h.svc.AdminProvision(adminCtx(), &types.AdminProvisionDingTalkUserRequest{
		Mobile: "13800000000", TenantID: 42,
	})
	require.NoError(t, err)

	second, idempotent, err := h.svc.AdminProvision(adminCtx(), &types.AdminProvisionDingTalkUserRequest{
		Mobile: "13800000000", TenantID: 42,
	})
	require.NoError(t, err)
	assert.True(t, idempotent)
	assert.Equal(t, first.ID, second.ID)
	assert.Len(t, h.users.register, 1, "no duplicate account on retry")
}

func TestAdminProvisionRejectsTakenEmail(t *testing.T) {
	h := newHarness(t)
	h.users.seed(&types.User{ID: "u-1", Username: "taken", Email: "13800000000@jgjl.cn", IsActive: true})
	_, _, err := h.svc.AdminProvision(context.Background(), &types.AdminProvisionDingTalkUserRequest{
		Mobile: "13800000000",
	})
	assert.ErrorIs(t, err, ErrDingTalkEmailTaken)
	assert.Empty(t, h.users.register)
}
