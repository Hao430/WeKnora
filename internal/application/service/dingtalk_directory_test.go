package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/infrastructure/dingtalk"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- fakes (embed the interface so unused methods stay out of the test) ---

type dirFakeUsers struct {
	interfaces.UserService
	byID    map[string]*types.User
	byEmail map[string]*types.User
	nextID  int
}

func newDirFakeUsers() *dirFakeUsers {
	return &dirFakeUsers{byID: map[string]*types.User{}, byEmail: map[string]*types.User{}}
}

func (f *dirFakeUsers) GetUserByID(_ context.Context, id string) (*types.User, error) {
	if u, ok := f.byID[id]; ok {
		return u, nil
	}
	return nil, apprepo.ErrUserNotFound
}

func (f *dirFakeUsers) GetUserByEmail(_ context.Context, email string) (*types.User, error) {
	if u, ok := f.byEmail[email]; ok {
		return u, nil
	}
	return nil, apprepo.ErrUserNotFound
}

func (f *dirFakeUsers) GetUserByUsername(_ context.Context, name string) (*types.User, error) {
	for _, u := range f.byID {
		if u.Username == name {
			return u, nil
		}
	}
	return nil, apprepo.ErrUserNotFound
}

func (f *dirFakeUsers) Register(_ context.Context, req *types.RegisterRequest) (*types.User, error) {
	f.nextID++
	u := &types.User{ID: fmt.Sprintf("user-%d", f.nextID), Username: req.Username, Email: req.Email, IsActive: true}
	f.byID[u.ID] = u
	f.byEmail[u.Email] = u
	return u, nil
}

func (f *dirFakeUsers) UpdateUser(_ context.Context, u *types.User) error { return nil }

func (f *dirFakeUsers) DeleteUser(_ context.Context, id string) error {
	delete(f.byID, id)
	return nil
}

func (f *dirFakeUsers) SetUserActive(_ context.Context, id, _ string, active bool) (*types.User, error) {
	u, ok := f.byID[id]
	if !ok {
		return nil, apprepo.ErrUserNotFound
	}
	u.IsActive = active
	return u, nil
}

type dirFakeIdentities struct {
	interfaces.UserIdentityRepository
	bySubject  map[string]*types.UserIdentity
	byExternal map[string]*types.UserIdentity
}

func newDirFakeIdentities() *dirFakeIdentities {
	return &dirFakeIdentities{bySubject: map[string]*types.UserIdentity{}, byExternal: map[string]*types.UserIdentity{}}
}

func (f *dirFakeIdentities) GetBySubject(_ context.Context, _, _, subject string) (*types.UserIdentity, error) {
	if i, ok := f.bySubject[subject]; ok {
		return i, nil
	}
	return nil, apprepo.ErrIdentityNotFound
}

func (f *dirFakeIdentities) GetByExternalID(_ context.Context, _, _, externalID string) (*types.UserIdentity, error) {
	if i, ok := f.byExternal[externalID]; ok {
		return i, nil
	}
	return nil, apprepo.ErrIdentityNotFound
}

func (f *dirFakeIdentities) Create(_ context.Context, identity *types.UserIdentity) error {
	if _, ok := f.bySubject[identity.Subject]; ok {
		return apprepo.ErrIdentityAlreadyBound
	}
	identity.ID = uint64(len(f.bySubject) + 1)
	f.bySubject[identity.Subject] = identity
	if identity.ExternalID != "" {
		f.byExternal[identity.ExternalID] = identity
	}
	return nil
}

func (f *dirFakeIdentities) Update(_ context.Context, identity *types.UserIdentity) error {
	if identity.ExternalID != "" {
		f.byExternal[identity.ExternalID] = identity
	}
	return nil
}

type dirFakeMembers struct {
	interfaces.TenantMemberService
	added map[string]types.TenantRole
}

func (f *dirFakeMembers) AddMember(_ context.Context, userID string, _ uint64, role types.TenantRole, _ *string) (*types.TenantMember, error) {
	if _, ok := f.added[userID]; ok {
		return nil, ErrMembershipAlreadyExists
	}
	f.added[userID] = role
	return &types.TenantMember{UserID: userID, Role: role}, nil
}

type dirFakeSettings struct {
	interfaces.SystemSettingService
	enabled  bool
	tenantID int64
	role     string
}

func (f *dirFakeSettings) GetBool(_ context.Context, _ string, _ string, _ bool) bool {
	return f.enabled
}
func (f *dirFakeSettings) GetInt(_ context.Context, _ string, _ string, _ int64) int64 {
	return f.tenantID
}
func (f *dirFakeSettings) GetString(_ context.Context, _ string, _ string, def string) string {
	if f.role == "" {
		return def
	}
	return f.role
}

type dirFakeClient struct {
	dingtalk.DirectoryClient
	users []*dingtalk.Profile
}

func (f *dirFakeClient) ListCorpUsers(context.Context) ([]*dingtalk.Profile, error) {
	return f.users, nil
}

func (f *dirFakeClient) GetUserByUserID(_ context.Context, id string) (*dingtalk.Profile, error) {
	for _, u := range f.users {
		if u.UserID == id {
			return u, nil
		}
	}
	return nil, dingtalk.ErrCodeRejected
}

func newDirFixture(enabled bool) (*dingTalkDirectoryService, *dirFakeUsers, *dirFakeIdentities, *dirFakeMembers, *dirFakeClient) {
	users := newDirFakeUsers()
	identities := newDirFakeIdentities()
	members := &dirFakeMembers{added: map[string]types.TenantRole{}}
	client := &dirFakeClient{}
	settings := &dirFakeSettings{enabled: enabled, tenantID: 7, role: "viewer"}
	svc := NewDingTalkDirectoryService(
		users, identities, members, client, settings,
		&config.Config{DingTalkAuth: &config.DingTalkAuthConfig{CorpID: "corp1"}},
	)
	return svc.(*dingTalkDirectoryService), users, identities, members, client
}

func TestDingTalkDirectorySyncCreatesAndBinds(t *testing.T) {
	svc, users, identities, members, client := newDirFixture(true)

	// One brand-new member and one whose placeholder email is already a local
	// account (the operator pre-created it).
	existing := &types.User{ID: "pre-existing", Username: "pre", Email: "13800000002@jgjl.cn", IsActive: true}
	users.byID[existing.ID] = existing
	users.byEmail[existing.Email] = existing

	client.users = []*dingtalk.Profile{
		{UserID: "dt1", UnionID: "union1", Nick: "张三", Mobile: "13800000001"},
		{UserID: "dt2", UnionID: "union2", Nick: "李四", Mobile: "13800000002"},
	}

	result, err := svc.SyncDirectory(context.Background())
	if err != nil {
		t.Fatalf("SyncDirectory: %v", err)
	}
	if result.Scanned != 2 || result.Created != 1 || result.Bound != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(identities.bySubject) != 2 {
		t.Fatalf("expected 2 identities, got %d", len(identities.bySubject))
	}
	if identities.byExternal["dt1"] == nil || identities.byExternal["dt2"] == nil {
		t.Fatal("identities must store the corp userid for leave events")
	}
	if len(members.added) != 2 {
		t.Fatalf("expected both members enrolled, got %v", members.added)
	}
	if members.added["pre-existing"] != types.TenantRoleViewer {
		t.Fatalf("expected viewer role, got %q", members.added["pre-existing"])
	}
}

func TestDingTalkDirectoryLeaveDisablesAndAddRestores(t *testing.T) {
	svc, users, identities, members, client := newDirFixture(true)

	user := &types.User{ID: "u1", Username: "zhangsan", Email: "13800000001@jgjl.cn", IsActive: true}
	users.byID[user.ID] = user
	users.byEmail[user.Email] = user
	identity := &types.UserIdentity{ID: 1, UserID: user.ID, Subject: "union1", ExternalID: "dt1"}
	identities.bySubject[identity.Subject] = identity
	identities.byExternal[identity.ExternalID] = identity
	client.users = []*dingtalk.Profile{{UserID: "dt1", UnionID: "union1", Nick: "张三", Mobile: "13800000001"}}

	if err := svc.HandleDirectoryEvent(context.Background(), dingtalk.DirectoryEventUserLeave, "corp1", []string{"dt1"}); err != nil {
		t.Fatalf("leave event: %v", err)
	}
	if users.byID["u1"].IsActive {
		t.Fatal("departed member must be disabled")
	}

	if err := svc.HandleDirectoryEvent(context.Background(), dingtalk.DirectoryEventUserAdd, "corp1", []string{"dt1"}); err != nil {
		t.Fatalf("add event: %v", err)
	}
	if !users.byID["u1"].IsActive {
		t.Fatal("returning member must be re-enabled")
	}
	if len(members.added) != 1 {
		t.Fatalf("expected one enrollment, got %v", members.added)
	}
}

func TestDingTalkDirectoryDisabledIsNoop(t *testing.T) {
	svc, users, _, _, client := newDirFixture(false)
	client.users = []*dingtalk.Profile{{UserID: "dt1", UnionID: "union1", Mobile: "13800000001"}}

	if _, err := svc.SyncDirectory(context.Background()); !errors.Is(err, ErrDingTalkDirectoryDisabled) {
		t.Fatalf("expected disabled error, got %v", err)
	}
	if err := svc.HandleDirectoryEvent(context.Background(), dingtalk.DirectoryEventUserAdd, "corp1", []string{"dt1"}); err != nil {
		t.Fatalf("add event must be a no-op: %v", err)
	}
	if len(users.byID) != 0 {
		t.Fatalf("disabled sync must not create users, got %d", len(users.byID))
	}
}

func TestDingTalkDirectorySkipsMaskedMobile(t *testing.T) {
	svc, _, _, _, client := newDirFixture(true)
	client.users = []*dingtalk.Profile{{UserID: "dt1", UnionID: "union1", Mobile: "138****0001"}}

	result, err := svc.SyncDirectory(context.Background())
	if err != nil {
		t.Fatalf("SyncDirectory: %v", err)
	}
	if result.Skipped != 1 || result.Created != 0 {
		t.Fatalf("masked mobile must be skipped, got %+v", result)
	}
}

func TestDingTalkDirectoryLeaveFallsBackToCorpLookup(t *testing.T) {
	svc, users, identities, _, client := newDirFixture(true)

	user := &types.User{ID: "u-legacy", Username: "legacy", Email: "13800000001@jgjl.cn", IsActive: true}
	users.byID[user.ID] = user
	// Bound before the userid was stored: only the unionId is known locally.
	identity := &types.UserIdentity{ID: 1, UserID: user.ID, Subject: "union-legacy"}
	identities.bySubject[identity.Subject] = identity
	client.users = []*dingtalk.Profile{{UserID: "dt-legacy", UnionID: "union-legacy"}}

	require.NoError(t, svc.HandleDirectoryEvent(
		context.Background(), dingtalk.DirectoryEventUserLeave, "corp1", []string{"dt-legacy"}))
	assert.False(t, users.byID["u-legacy"].IsActive)
}

func TestDingTalkDirectoryLeaveIsIdempotentAndIgnoresUnknown(t *testing.T) {
	svc, users, identities, _, _ := newDirFixture(true)

	user := &types.User{ID: "u1", Username: "zhangsan", Email: "13800000001@jgjl.cn", IsActive: false}
	users.byID[user.ID] = user
	identity := &types.UserIdentity{ID: 1, UserID: user.ID, Subject: "union1", ExternalID: "dt1"}
	identities.bySubject[identity.Subject] = identity
	identities.byExternal[identity.ExternalID] = identity

	// Already disabled: a redelivered event must not fail.
	require.NoError(t, svc.HandleDirectoryEvent(
		context.Background(), dingtalk.DirectoryEventUserLeave, "corp1", []string{"dt1"}))
	// No binding at all: a member who never logged in is a silent no-op.
	require.NoError(t, svc.HandleDirectoryEvent(
		context.Background(), dingtalk.DirectoryEventUserLeave, "corp1", []string{"unknown"}))
	assert.False(t, users.byID["u1"].IsActive)
}

func TestDingTalkDirectoryEventWithoutProfileIsRetried(t *testing.T) {
	svc, _, _, _, client := newDirFixture(true)
	client.users = nil // DingTalk can no longer resolve the userid.

	err := svc.HandleDirectoryEvent(
		context.Background(), dingtalk.DirectoryEventUserAdd, "corp1", []string{"gone"})
	require.Error(t, err, "an unresolvable add must surface so the event is redelivered")
}

func TestDingTalkDirectoryIgnoresForeignCorp(t *testing.T) {
	svc, users, _, _, client := newDirFixture(true)
	client.users = []*dingtalk.Profile{{UserID: "dt1", UnionID: "union1", Mobile: "13800000001"}}

	require.NoError(t, svc.HandleDirectoryEvent(
		context.Background(), dingtalk.DirectoryEventUserAdd, "another-corp", []string{"dt1"}))
	assert.Empty(t, users.byID, "events from another corp must not provision accounts")
}
