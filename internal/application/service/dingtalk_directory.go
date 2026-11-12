package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/infrastructure/dingtalk"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// System-setting keys for the DingTalk directory sync. Read on every event and
// on every reconciliation tick, so edits take effect without a restart.
const (
	SettingDingTalkDirectoryEnabled  = "dingtalk.directory_sync.enabled"
	SettingDingTalkDirectoryTenantID = "dingtalk.directory_sync.tenant_id"
	SettingDingTalkDirectoryRole     = "dingtalk.directory_sync.role"
)

// dingTalkDirectoryActor is the audit actor recorded when the sync disables an
// account. It is not a user id, which also avoids the self-disable guard.
const dingTalkDirectoryActor = "system:dingtalk-directory"

// ErrDingTalkDirectoryDisabled means the operator has not turned the sync on.
var ErrDingTalkDirectoryDisabled = errors.New("dingtalk directory sync is disabled")

// dingTalkDirectoryService keeps local accounts aligned with the DingTalk
// corp directory: new members get an account and a viewer seat in the
// configured space, departed members are disabled.
type dingTalkDirectoryService struct {
	users      interfaces.UserService
	identities interfaces.UserIdentityRepository
	members    interfaces.TenantMemberService
	client     dingtalk.DirectoryClient
	settings   interfaces.SystemSettingService
	corpID     string
}

// NewDingTalkDirectoryService wires the directory sync. A nil settings service
// means the sync can never be enabled, which is the safe default.
func NewDingTalkDirectoryService(
	users interfaces.UserService,
	identities interfaces.UserIdentityRepository,
	members interfaces.TenantMemberService,
	client dingtalk.DirectoryClient,
	settings interfaces.SystemSettingService,
	cfg *config.Config,
) interfaces.DingTalkDirectoryService {
	corpID := ""
	if cfg != nil && cfg.DingTalkAuth != nil {
		corpID = strings.TrimSpace(cfg.DingTalkAuth.CorpID)
	}
	return &dingTalkDirectoryService{
		users:      users,
		identities: identities,
		members:    members,
		client:     client,
		settings:   settings,
		corpID:     corpID,
	}
}

type dingTalkDirectoryConfig struct {
	enabled  bool
	tenantID uint64
	role     types.TenantRole
}

func (s *dingTalkDirectoryService) config(ctx context.Context) dingTalkDirectoryConfig {
	cfg := dingTalkDirectoryConfig{}
	if s.settings == nil {
		return cfg
	}
	cfg.enabled = s.settings.GetBool(ctx, SettingDingTalkDirectoryEnabled, "", false)
	tenantID := s.settings.GetInt(ctx, SettingDingTalkDirectoryTenantID, "", 0)
	if tenantID > 0 {
		cfg.tenantID = uint64(tenantID)
	}
	role := types.TenantRole(strings.TrimSpace(s.settings.GetString(ctx, SettingDingTalkDirectoryRole, "", string(types.TenantRoleViewer))))
	if role != types.TenantRoleViewer && role != types.TenantRoleContributor {
		role = types.TenantRoleViewer
	}
	cfg.role = role
	return cfg
}

// SyncDirectory reconciles every corp member once. It is the backstop for
// missed stream events, run at boot and on a timer.
func (s *dingTalkDirectoryService) SyncDirectory(ctx context.Context) (*types.DingTalkDirectorySyncResult, error) {
	cfg := s.config(ctx)
	if !cfg.enabled {
		return nil, ErrDingTalkDirectoryDisabled
	}
	if s.client == nil {
		return nil, errors.New("dingtalk directory client is not configured")
	}

	members, err := s.client.ListCorpUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list dingtalk corp users: %w", err)
	}

	result := &types.DingTalkDirectorySyncResult{Scanned: len(members)}
	for _, profile := range members {
		if profile.Active != nil && !*profile.Active {
			continue
		}
		outcome, err := s.upsertMember(ctx, profile, cfg)
		if err != nil {
			// A single member without a usable phone number or with an email
			// collision must not stop the rest of the org from syncing.
			logger.Warnf(ctx, "[DingTalk] directory sync skipped member %s: %v", profile.UserID, err)
			result.Skipped++
			continue
		}
		switch outcome {
		case directoryOutcomeCreated:
			result.Created++
		case directoryOutcomeBound:
			result.Bound++
		}
		if outcome == directoryOutcomeRestored {
			result.Restored++
		}
	}
	logger.Infof(ctx, "[DingTalk] directory sync done: scanned=%d created=%d bound=%d restored=%d skipped=%d",
		result.Scanned, result.Created, result.Bound, result.Restored, result.Skipped)
	return result, nil
}

type directoryOutcome int

const (
	directoryOutcomeNone directoryOutcome = iota
	directoryOutcomeCreated
	directoryOutcomeBound
	directoryOutcomeRestored
)

// HandleDirectoryEvent applies one stream event. Disabled sync is a no-op so
// the stream can stay connected before the operator turns it on.
func (s *dingTalkDirectoryService) HandleDirectoryEvent(
	ctx context.Context, eventType string, corpID string, userIDs []string,
) error {
	cfg := s.config(ctx)
	if !cfg.enabled {
		return nil
	}
	if corpID != "" && s.corpID != "" && corpID != s.corpID {
		return nil
	}

	var errs []error
	switch eventType {
	case dingtalk.DirectoryEventUserAdd, dingtalk.DirectoryEventUserModify:
		for _, userID := range userIDs {
			profile, err := s.client.GetUserByUserID(ctx, userID)
			if err != nil {
				errs = append(errs, fmt.Errorf("resolve %s: %w", userID, err))
				continue
			}
			if _, err := s.upsertMember(ctx, profile, cfg); err != nil {
				errs = append(errs, fmt.Errorf("provision %s: %w", userID, err))
			}
		}
	case dingtalk.DirectoryEventUserLeave:
		for _, userID := range userIDs {
			if err := s.deactivateMember(ctx, userID); err != nil {
				errs = append(errs, fmt.Errorf("deactivate %s: %w", userID, err))
			}
		}
	}
	// Returning an error makes DingTalk redeliver the event. Handlers are
	// idempotent, so a retry is always safe.
	return errors.Join(errs...)
}

// upsertMember makes sure the member has an active account bound to the
// identity and a membership in the configured space.
func (s *dingTalkDirectoryService) upsertMember(
	ctx context.Context, profile *dingtalk.Profile, cfg dingTalkDirectoryConfig,
) (directoryOutcome, error) {
	if strings.TrimSpace(profile.UnionID) == "" {
		return directoryOutcomeNone, errors.New("dingtalk profile has no unionId")
	}

	identity, err := s.identities.GetBySubject(
		ctx, types.IdentityProviderDingTalk, s.corpID, profile.UnionID)
	switch {
	case err == nil:
		return s.reactivateAndEnroll(ctx, identity, profile, cfg)
	case !errors.Is(err, apprepo.ErrIdentityNotFound):
		return directoryOutcomeNone, fmt.Errorf("lookup dingtalk identity: %w", err)
	}

	// A local account may already use the placeholder email (for example the
	// operator pre-created it, or the member registered by password first).
	// Binding it keeps one account per person instead of failing the sync.
	if mobile := usableDingTalkMobile(profile.Mobile); mobile != "" {
		if user, lookupErr := s.users.GetUserByEmail(ctx, mobile+dingTalkEmailDomain); lookupErr == nil && user != nil {
			identity = &types.UserIdentity{
				UserID:         user.ID,
				Provider:       types.IdentityProviderDingTalk,
				ProviderTenant: s.corpID,
				Subject:        profile.UnionID,
				ExternalID:     strings.TrimSpace(profile.UserID),
				DisplayName:    strings.TrimSpace(profile.Nick),
			}
			if createErr := s.identities.Create(ctx, identity); createErr != nil {
				if !errors.Is(createErr, apprepo.ErrIdentityAlreadyBound) {
					return directoryOutcomeNone, fmt.Errorf("bind existing account: %w", createErr)
				}
				// Lost a race; fall back to the binding that won.
				identity, createErr = s.identities.GetBySubject(
					ctx, types.IdentityProviderDingTalk, s.corpID, profile.UnionID)
				if createErr != nil {
					return directoryOutcomeNone, fmt.Errorf("reload dingtalk identity: %w", createErr)
				}
			}
			outcome, enrollErr := s.reactivateAndEnroll(ctx, identity, profile, cfg)
			if enrollErr != nil {
				return directoryOutcomeNone, enrollErr
			}
			if outcome == directoryOutcomeRestored {
				return outcome, nil
			}
			return directoryOutcomeBound, nil
		}
	}
	return s.createMember(ctx, profile, cfg)
}

// reactivateAndEnroll restores a disabled account (a returning member) and
// ensures the space membership. Departed-then-rehired members land here.
func (s *dingTalkDirectoryService) reactivateAndEnroll(
	ctx context.Context, identity *types.UserIdentity, profile *dingtalk.Profile, cfg dingTalkDirectoryConfig,
) (directoryOutcome, error) {
	user, err := s.users.GetUserByID(ctx, identity.UserID)
	if err != nil {
		return directoryOutcomeNone, fmt.Errorf("load bound user: %w", err)
	}

	if !user.IsActive {
		if _, err := s.users.SetUserActive(ctx, user.ID, dingTalkDirectoryActor, true); err != nil {
			return directoryOutcomeNone, fmt.Errorf("reactivate user: %w", err)
		}
		logger.Infof(ctx, "[DingTalk] reactivated returning member %s", user.ID)
		if err := s.enroll(ctx, user.ID, cfg); err != nil {
			return directoryOutcomeNone, err
		}
		return directoryOutcomeRestored, nil
	}

	// Backfill the userid and nickname on identities bound by the login flow,
	// so leave events resolve this member and the profile page has a name.
	changed := false
	if strings.TrimSpace(identity.ExternalID) == "" && strings.TrimSpace(profile.UserID) != "" {
		identity.ExternalID = strings.TrimSpace(profile.UserID)
		changed = true
	}
	if strings.TrimSpace(identity.DisplayName) == "" && strings.TrimSpace(profile.Nick) != "" {
		identity.DisplayName = strings.TrimSpace(profile.Nick)
		changed = true
	}
	if changed {
		if err := s.identities.Update(ctx, identity); err != nil {
			logger.Warnf(ctx, "[DingTalk] backfill identity %d failed: %v", identity.ID, err)
		}
	}
	return directoryOutcomeNone, s.enroll(ctx, user.ID, cfg)
}

// createMember provisions the SSO-only placeholder account, mirroring the
// first-login provisioning so both paths produce identical accounts.
func (s *dingTalkDirectoryService) createMember(
	ctx context.Context, profile *dingtalk.Profile, cfg dingTalkDirectoryConfig,
) (directoryOutcome, error) {
	mobile := usableDingTalkMobile(profile.Mobile)
	if mobile == "" {
		return directoryOutcomeNone, ErrDingTalkMobileRequired
	}
	email := mobile + dingTalkEmailDomain
	if existing, err := s.users.GetUserByEmail(ctx, email); err == nil && existing != nil {
		return directoryOutcomeNone, fmt.Errorf("placeholder email %s is already taken", email)
	}

	username, err := availableUsername(ctx, s.users, dingTalkUsernameSeed(profile), "")
	if err != nil {
		return directoryOutcomeNone, err
	}
	randomPassword, err := generateRandomString(32)
	if err != nil {
		return directoryOutcomeNone, fmt.Errorf("generate password: %w", err)
	}

	user, err := s.users.Register(ctx, &types.RegisterRequest{
		Username:           username,
		Email:              email,
		Password:           randomPassword,
		TenantProvisioning: types.TenantProvisioningTenantless,
	})
	if err != nil {
		return directoryOutcomeNone, fmt.Errorf("provision account: %w", err)
	}

	identity := &types.UserIdentity{
		UserID:         user.ID,
		Provider:       types.IdentityProviderDingTalk,
		ProviderTenant: s.corpID,
		Subject:        profile.UnionID,
		ExternalID:     strings.TrimSpace(profile.UserID),
		DisplayName:    strings.TrimSpace(profile.Nick),
	}
	if err := s.identities.Create(ctx, identity); err != nil {
		// Leave no orphan account behind if the binding lost a race.
		_ = s.users.DeleteUser(ctx, user.ID)
		return directoryOutcomeNone, fmt.Errorf("bind dingtalk identity: %w", err)
	}

	ssoOnly := true
	user.Preferences.OidcOnlyLogin = &ssoOnly
	user.UpdatedAt = time.Now()
	if err := s.users.UpdateUser(ctx, user); err != nil {
		logger.Warnf(ctx, "[DingTalk] mark account %s SSO-only failed: %v", user.ID, err)
	}

	if err := s.enroll(ctx, user.ID, cfg); err != nil {
		return directoryOutcomeNone, err
	}
	logger.Infof(ctx, "[DingTalk] provisioned member %s (%s)", user.ID, username)
	return directoryOutcomeCreated, nil
}

// enroll adds the account to the configured space. An existing membership is
// a no-op, so a rehire keeps whatever role the operator gave them.
func (s *dingTalkDirectoryService) enroll(ctx context.Context, userID string, cfg dingTalkDirectoryConfig) error {
	if cfg.tenantID == 0 || s.members == nil {
		return nil
	}
	_, err := s.members.AddMember(ctx, userID, cfg.tenantID, cfg.role, nil)
	if err != nil && !errors.Is(err, ErrMembershipAlreadyExists) {
		return fmt.Errorf("enroll user %s: %w", userID, err)
	}
	return nil
}

// deactivateMember disables the account behind a departed corp userid. Data is
// kept (soft state), so a rehire restores the same account on the next add.
func (s *dingTalkDirectoryService) deactivateMember(ctx context.Context, userID string) error {
	identity, err := s.identities.GetByExternalID(
		ctx, types.IdentityProviderDingTalk, s.corpID, userID)
	if err != nil {
		if errors.Is(err, apprepo.ErrIdentityNotFound) {
			// Legacy binding without a stored userid: fall back to the corp
			// lookup while the member is still readable.
			profile, lookupErr := s.client.GetUserByUserID(ctx, userID)
			if lookupErr != nil {
				return nil
			}
			identity, err = s.identities.GetBySubject(
				ctx, types.IdentityProviderDingTalk, s.corpID, profile.UnionID)
		}
		if err != nil {
			if errors.Is(err, apprepo.ErrIdentityNotFound) {
				return nil
			}
			return err
		}
	}

	user, err := s.users.GetUserByID(ctx, identity.UserID)
	if err != nil {
		return fmt.Errorf("load departed member: %w", err)
	}
	if !user.IsActive {
		return nil
	}
	if _, err := s.users.SetUserActive(ctx, user.ID, dingTalkDirectoryActor, false); err != nil {
		return fmt.Errorf("disable departed member: %w", err)
	}
	logger.Infof(ctx, "[DingTalk] disabled departed member %s", user.ID)
	return nil
}

// usableDingTalkMobile rejects empty and masked phone numbers: the placeholder
// email derives from it, and "138****0000@corp" is not an address.
func usableDingTalkMobile(mobile string) string {
	mobile = strings.TrimSpace(mobile)
	if mobile == "" || strings.Contains(mobile, "*") {
		return ""
	}
	return mobile
}
