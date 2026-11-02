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

// dingTalkEmailDomain builds the placeholder email for accounts created by a
// DingTalk login. The mobile number is the only stable, human-meaningful value
// the provider guarantees once the phone permission is granted, and users.email
// is NOT NULL UNIQUE.
const dingTalkEmailDomain = "@jgjl.cn"

// Errors surfaced to the handler, which maps them to 403 / 409 / 401.
var (
	// ErrDingTalkRegistrationDisabled means the deployment closed registration,
	// so even a valid invitation link must not create an account. This mirrors
	// RegisterByInvite: invite_only stays closed to newcomers.
	ErrDingTalkRegistrationDisabled = errors.New("registration is disabled")
	// ErrDingTalkInviteRequired means a first-time DingTalk user arrived
	// without an invitation link. Account creation is invite-driven only.
	ErrDingTalkInviteRequired = errors.New("an invitation link is required to create an account")
	// ErrDingTalkAccountDisabled covers both a disabled DingTalk account
	// (active == false) and a locally disabled user row.
	ErrDingTalkAccountDisabled = errors.New("account is disabled")
	// ErrDingTalkMobileRequired means the provider returned no mobile number;
	// without it there is no placeholder email to provision an account with.
	ErrDingTalkMobileRequired = errors.New("dingtalk account has no mobile number")
	// ErrDingTalkEmailTaken means the placeholder email already belongs to an
	// account. It is almost always a password account registered with that
	// address, and silently binding it would hand an existing account to
	// whoever holds the matching mobile number.
	ErrDingTalkEmailTaken = errors.New("an account already exists for this phone number")
)

// userStore is the slice of UserService this flow needs. Keeping it narrow
// makes the准入 matrix unit-testable without booting token issuance, tenants
// and memberships.
type userStore interface {
	GetUserByID(ctx context.Context, id string) (*types.User, error)
	GetUserByEmail(ctx context.Context, email string) (*types.User, error)
	GetUserByUsername(ctx context.Context, username string) (*types.User, error)
	Register(ctx context.Context, req *types.RegisterRequest) (*types.User, error)
	UpdateUser(ctx context.Context, user *types.User) error
	DeleteUser(ctx context.Context, id string) error
	GenerateTokens(ctx context.Context, user *types.User) (accessToken, refreshToken string, err error)
}

// inviteStore is the slice of TenantInvitationService this flow needs.
type inviteStore interface {
	LookupByToken(ctx context.Context, plainToken string) (*types.TenantInvitation, error)
	AcceptByToken(ctx context.Context, plainToken string, newUserID string) (*types.TenantMember, error)
}

type dingTalkAuthService struct {
	identityRepo interfaces.UserIdentityRepository
	users        userStore
	invitations  inviteStore
	client       dingtalk.Client
	corpID       string
}

// NewDingTalkAuthService wires the DingTalk login flow. corpID scopes the
// identity subject: the same unionId in another corp is a different account.
func NewDingTalkAuthService(
	identityRepo interfaces.UserIdentityRepository,
	users userStore,
	invitations inviteStore,
	client dingtalk.Client,
	cfg *config.Config,
) interfaces.DingTalkAuthService {
	corpID := ""
	if cfg != nil && cfg.DingTalkAuth != nil {
		corpID = strings.TrimSpace(cfg.DingTalkAuth.CorpID)
	}
	return &dingTalkAuthService{
		identityRepo: identityRepo,
		users:        users,
		invitations:  invitations,
		client:       client,
		corpID:       corpID,
	}
}

// Resolve logs in, provisioning the account on first sight.
func (s *dingTalkAuthService) Resolve(
	ctx context.Context, in types.DingTalkLoginInput,
) (*types.DingTalkLoginResult, error) {
	profile, err := s.profile(ctx, in.Code, in.Source)
	if err != nil {
		return nil, err
	}
	if profile.Active != nil && !*profile.Active {
		return nil, ErrDingTalkAccountDisabled
	}

	inviteToken := strings.TrimSpace(in.InviteToken)
	user, isNew, err := s.resolveUser(ctx, profile, in, inviteToken)
	if err != nil {
		return nil, err
	}

	// A returning user who follows a link must still join the workspace.
	if inviteToken != "" {
		if err := s.joinByInvite(ctx, user, inviteToken); err != nil {
			return nil, err
		}
	}

	accessToken, refreshToken, err := s.users.GenerateTokens(ctx, user)
	if err != nil {
		return nil, fmt.Errorf("generate tokens: %w", err)
	}
	return &types.DingTalkLoginResult{
		User:         user,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		IsNewUser:    isNew,
	}, nil
}

// Bind attaches the identity to an existing account. It never provisions and
// never issues tokens; a duplicate binding surfaces as ErrIdentityAlreadyBound.
func (s *dingTalkAuthService) Bind(ctx context.Context, userID string, in types.DingTalkBindInput) error {
	if strings.TrimSpace(userID) == "" {
		return errors.New("user id is required")
	}
	profile, err := s.profile(ctx, in.Code, in.Source)
	if err != nil {
		return err
	}
	if profile.Active != nil && !*profile.Active {
		return ErrDingTalkAccountDisabled
	}
	if _, err := s.users.GetUserByID(ctx, userID); err != nil {
		return fmt.Errorf("load user: %w", err)
	}
	return s.identityRepo.Create(ctx, &types.UserIdentity{
		UserID:         userID,
		Provider:       types.IdentityProviderDingTalk,
		ProviderTenant: s.corpID,
		Subject:        profile.UnionID,
	})
}

func (s *dingTalkAuthService) profile(
	ctx context.Context, code string, source types.DingTalkLoginSource,
) (*dingtalk.Profile, error) {
	switch source {
	case types.DingTalkSourceInner:
		return s.client.GetUserByInnerCode(ctx, code)
	case types.DingTalkSourceOAuth:
		return s.client.GetUserByOAuthCode(ctx, code)
	default:
		return nil, fmt.Errorf("unsupported dingtalk login source %q", source)
	}
}

func (s *dingTalkAuthService) resolveUser(
	ctx context.Context, profile *dingtalk.Profile, in types.DingTalkLoginInput, inviteToken string,
) (*types.User, bool, error) {
	identity, err := s.identityRepo.GetBySubject(
		ctx, types.IdentityProviderDingTalk, s.corpID, profile.UnionID)
	switch {
	case err == nil:
		user, userErr := s.users.GetUserByID(ctx, identity.UserID)
		if userErr != nil {
			return nil, false, fmt.Errorf("load bound user: %w", userErr)
		}
		if !user.IsActive {
			return nil, false, ErrDingTalkAccountDisabled
		}
		return user, false, nil
	case errors.Is(err, apprepo.ErrIdentityNotFound):
		return s.provision(ctx, profile, in, inviteToken)
	default:
		return nil, false, fmt.Errorf("lookup dingtalk identity: %w", err)
	}
}

// provision creates the local account and binding. The准入 rules are the same
// ones RegisterByInvite applies, so switching registration off also switches
// DingTalk provisioning off — the SSO path must not become a back door.
func (s *dingTalkAuthService) provision(
	ctx context.Context, profile *dingtalk.Profile, in types.DingTalkLoginInput, inviteToken string,
) (*types.User, bool, error) {
	mode := in.RegistrationMode
	if mode == "" {
		mode = config.AuthRegistrationModeSelfServe
	}
	registrationOpen := mode == config.AuthRegistrationModeSelfServe ||
		mode == config.AuthRegistrationModeInviteRegister
	if inviteToken == "" {
		if mode != config.AuthRegistrationModeSelfServe {
			return nil, false, ErrDingTalkInviteRequired
		}
	} else if !registrationOpen {
		return nil, false, ErrDingTalkRegistrationDisabled
	}

	mobile := strings.TrimSpace(profile.Mobile)
	if mobile == "" {
		return nil, false, ErrDingTalkMobileRequired
	}
	email := mobile + dingTalkEmailDomain
	if existing, err := s.users.GetUserByEmail(ctx, email); err == nil && existing != nil {
		return nil, false, ErrDingTalkEmailTaken
	}

	username, err := s.availableUsername(ctx, mobile)
	if err != nil {
		return nil, false, err
	}
	randomPassword, err := generateRandomString(32)
	if err != nil {
		return nil, false, fmt.Errorf("generate password: %w", err)
	}
	provisioning := in.DefaultTenantMode
	if inviteToken != "" {
		// Invited accounts stay tenantless until AcceptByToken grants the
		// membership; RegisterByInvite uses the same order.
		provisioning = types.TenantProvisioningTenantless
	}

	user, err := s.users.Register(ctx, &types.RegisterRequest{
		Username:           username,
		Email:              email,
		Password:           randomPassword,
		TenantProvisioning: provisioning,
	})
	if err != nil {
		// A concurrent first login can win between our lookup and Register.
		if errors.Is(err, ErrUserEmailExists) {
			if winner, ok := s.boundUser(ctx, profile.UnionID); ok {
				return winner, false, nil
			}
			return nil, false, ErrDingTalkEmailTaken
		}
		return nil, false, fmt.Errorf("provision account: %w", err)
	}

	identity := &types.UserIdentity{
		UserID:         user.ID,
		Provider:       types.IdentityProviderDingTalk,
		ProviderTenant: s.corpID,
		Subject:        profile.UnionID,
	}
	if err := s.identityRepo.Create(ctx, identity); err != nil {
		if errors.Is(err, apprepo.ErrIdentityAlreadyBound) {
			// The race winner already bound this DingTalk account; drop the
			// duplicate local account we just created and reuse theirs.
			_ = s.users.DeleteUser(ctx, user.ID)
			if winner, ok := s.boundUser(ctx, profile.UnionID); ok {
				return winner, false, nil
			}
		}
		return nil, false, fmt.Errorf("bind dingtalk identity: %w", err)
	}

	// The account has a random password nobody received; hide password
	// self-service until someone sets a real one.
	ssoOnly := true
	user.Preferences.OidcOnlyLogin = &ssoOnly
	user.UpdatedAt = time.Now()
	if err := s.users.UpdateUser(ctx, user); err != nil {
		logger.Warnf(ctx, "dingtalk: failed to mark account %s as SSO-only: %v", user.ID, err)
	}
	return user, true, nil
}

// joinByInvite makes the invited workspace the home tenant (first join only)
// and creates the membership. Membership creation is idempotent, so a repeated
// link heals a login that previously stopped halfway.
func (s *dingTalkAuthService) joinByInvite(ctx context.Context, user *types.User, token string) error {
	inv, err := s.invitations.LookupByToken(ctx, token)
	if err != nil {
		return err
	}
	if user.TenantID == 0 {
		user.TenantID = inv.TenantID
		if err := s.users.UpdateUser(ctx, user); err != nil {
			user.TenantID = 0
			return fmt.Errorf("set home tenant: %w", err)
		}
	}
	if _, err := s.invitations.AcceptByToken(ctx, token, user.ID); err != nil {
		// Never leave a home tenant the user is not a member of; the next
		// login with a valid link retries the join.
		if user.TenantID == inv.TenantID {
			user.TenantID = 0
			if restoreErr := s.users.UpdateUser(ctx, user); restoreErr != nil {
				logger.Errorf(ctx, "dingtalk: failed to restore tenantless user %s: %v", user.ID, restoreErr)
			}
		}
		return err
	}
	return nil
}

// boundUser re-reads the binding after a lost race. ok is false when no
// identity exists, which means the failure was a genuine email conflict.
func (s *dingTalkAuthService) boundUser(ctx context.Context, unionID string) (*types.User, bool) {
	identity, err := s.identityRepo.GetBySubject(ctx, types.IdentityProviderDingTalk, s.corpID, unionID)
	if err != nil {
		return nil, false
	}
	user, err := s.users.GetUserByID(ctx, identity.UserID)
	if err != nil {
		return nil, false
	}
	return user, true
}

// availableUsername derives a free username from the mobile number so the
// account list stays readable, falling back to a numeric suffix on collision.
func (s *dingTalkAuthService) availableUsername(ctx context.Context, mobile string) (string, error) {
	base := sanitizeUsernameCandidate(mobile)
	for i := 0; i < 20; i++ {
		candidate := base
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d", base, i)
		}
		existing, err := s.users.GetUserByUsername(ctx, candidate)
		switch {
		case err != nil && isUserLookupNotFound(err), err == nil && existing == nil:
			return candidate, nil
		case err != nil:
			return "", fmt.Errorf("check username: %w", err)
		}
	}
	// Deliberately not time-based: a second collision after 20 attempts means
	// the mobile number is in heavy use, and a random suffix keeps us moving.
	suffix, err := generateRandomString(6)
	if err != nil {
		return "", fmt.Errorf("generate username suffix: %w", err)
	}
	return sanitizeUsernameCandidate(base + "-" + suffix), nil
}
