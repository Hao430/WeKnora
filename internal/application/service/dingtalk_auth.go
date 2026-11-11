package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

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
	// ErrDingTalkTenantForbidden means the operator tried to enroll the new
	// member into a workspace they do not administer themselves.
	ErrDingTalkTenantForbidden = errors.New("you need admin permission in the target tenant")
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

// memberStore is the slice of TenantMemberService the admin provisioning
// path needs.
type memberStore interface {
	AddMember(ctx context.Context, userID string, tenantID uint64, role types.TenantRole, invitedBy *string) (*types.TenantMember, error)
	GetMembership(ctx context.Context, userID string, tenantID uint64) (*types.TenantMember, error)
}

type dingTalkAuthService struct {
	identityRepo interfaces.UserIdentityRepository
	users        userStore
	invitations  inviteStore
	members      memberStore
	client       dingtalk.Client
	corpID       string
}

// NewDingTalkAuthService wires the DingTalk login flow. corpID scopes the
// identity subject: the same unionId in another corp is a different account.
func NewDingTalkAuthService(
	identityRepo interfaces.UserIdentityRepository,
	users interfaces.UserService,
	invitations interfaces.TenantInvitationService,
	members interfaces.TenantMemberService,
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
		members:      members,
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

	// Binding the account that is already bound to this user is a no-op: the
	// settings page offers the button without a status query, so clicking it
	// twice (or after a second device) must not read as a conflict.
	existing, err := s.identityRepo.GetBySubject(
		ctx, types.IdentityProviderDingTalk, s.corpID, profile.UnionID)
	switch {
	case err == nil && existing.UserID == userID:
		return nil
	case err == nil:
		return apprepo.ErrIdentityAlreadyBound
	case !errors.Is(err, apprepo.ErrIdentityNotFound):
		return fmt.Errorf("lookup dingtalk identity: %w", err)
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
		return s.syncUsername(ctx, user, profile), false, nil
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

	username, err := availableUsername(ctx, s.users, dingTalkUsernameSeed(profile), "")
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

// AdminProvision pre-creates a DingTalk-bound account for an operator.
// The registration policy deliberately does not gate it: closing signup
// shuts the self-serve doors, while this endpoint is the SystemAdmin
// holding them open for a specific member. Idempotent - provisioning an
// already-bound mobile returns the existing account.
func (s *dingTalkAuthService) AdminProvision(
	ctx context.Context, req *types.AdminProvisionDingTalkUserRequest,
) (*types.User, bool, error) {
	profile, err := s.client.GetUserByMobile(ctx, req.Mobile)
	if err != nil {
		return nil, false, err
	}
	mobile := strings.TrimSpace(profile.Mobile)
	if mobile == "" || strings.Contains(mobile, "*") {
		// Masked or missing in the contact read - fall back to what the
		// operator typed; it came from the same corp directory.
		mobile = strings.TrimSpace(req.Mobile)
	}
	if mobile == "" {
		return nil, false, ErrDingTalkMobileRequired
	}

	// The permission gate runs before anything is written: a refused
	// request must not leave a provisioned account behind.
	if req.TenantID != 0 && s.members != nil {
		if err := s.requireTenantAdmin(ctx, req.TenantID); err != nil {
			return nil, false, err
		}
	}

	identity, err := s.identityRepo.GetBySubject(ctx, types.IdentityProviderDingTalk, s.corpID, profile.UnionID)
	switch {
	case err == nil:
		user, userErr := s.users.GetUserByID(ctx, identity.UserID)
		if userErr != nil {
			return nil, false, fmt.Errorf("load bound user: %w", userErr)
		}
		if err := s.enrollTenant(ctx, user.ID, req); err != nil {
			return nil, false, err
		}
		return user, true, nil
	case errors.Is(err, apprepo.ErrIdentityNotFound):
		// fall through to first-time provisioning
	default:
		return nil, false, fmt.Errorf("lookup dingtalk identity: %w", err)
	}

	// self_serve reuses the exact first-login provisioning (random nobody-
	// knows password, SSO-only, placeholder email); the admin action just
	// replaces the policy gate. Tenantless: only the enrollment below gives
	// this account a workspace.
	user, _, err := s.provision(ctx, profile, types.DingTalkLoginInput{
		RegistrationMode:  config.AuthRegistrationModeSelfServe,
		DefaultTenantMode: types.TenantProvisioningTenantless,
	}, "")
	if err != nil {
		return nil, false, err
	}
	if err := s.enrollTenant(ctx, user.ID, req); err != nil {
		return nil, false, err
	}
	return user, false, nil
}

// enrollTenant adds the account to the requested workspace. The operator
// may only hand out workspaces they administer themselves; a missing role
// defaults to contributor, and joining twice is a no-op so retries stay
// clean.
func (s *dingTalkAuthService) enrollTenant(
	ctx context.Context, userID string, req *types.AdminProvisionDingTalkUserRequest,
) error {
	if req.TenantID == 0 || s.members == nil {
		return nil
	}
	actorID, _ := types.UserIDFromContext(ctx)
	role := types.TenantRole(strings.TrimSpace(req.Role))
	if role == "" {
		role = types.TenantRoleContributor
	}
	if _, err := s.members.AddMember(ctx, userID, req.TenantID, role, &actorID); err != nil && !errors.Is(err, ErrMembershipAlreadyExists) {
		return err
	}
	return nil
}

// requireTenantAdmin refuses enrollment into workspaces the operator does
// not administer. Membership rows are the source of truth - no superuser
// bypass, per the admin provisioning admission policy.
func (s *dingTalkAuthService) requireTenantAdmin(ctx context.Context, tenantID uint64) error {
	actorID, _ := types.UserIDFromContext(ctx)
	membership, err := s.members.GetMembership(ctx, actorID, tenantID)
	if err != nil && !errors.Is(err, ErrMembershipNotFound) {
		return err
	}
	if membership == nil || !membership.Role.HasPermission(types.TenantRoleAdmin) {
		return ErrDingTalkTenantForbidden
	}
	return nil
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

// availableUsername turns a readable seed into a free username, falling back
// to a numeric suffix on collision. exceptUserID is excluded from the check so
// a returning account does not collide with itself while syncing its name.
func availableUsername(ctx context.Context, users userStore, base, exceptUserID string) (string, error) {
	for i := 0; i < 20; i++ {
		candidate := base
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d", base, i)
		}
		existing, err := users.GetUserByUsername(ctx, candidate)
		if err == nil && existing != nil && existing.ID == exceptUserID {
			return candidate, nil
		}
		switch {
		case err != nil && isUserLookupNotFound(err), err == nil && existing == nil:
			return candidate, nil
		case err != nil:
			return "", fmt.Errorf("check username: %w", err)
		}
	}
	// Deliberately not time-based: a second collision after 20 attempts means
	// the display name is in heavy use, and a random suffix keeps us moving.
	suffix, err := generateRandomString(6)
	if err != nil {
		return "", fmt.Errorf("generate username suffix: %w", err)
	}
	return base + "-" + suffix, nil
}

// dingTalkUsernameSeed is the readable base of a provisioned username: the
// DingTalk display name, or the mobile number when the provider returns none.
// The placeholder email keeps deriving from the mobile number either way.
func dingTalkUsernameSeed(profile *dingtalk.Profile) string {
	if name := sanitizeDingTalkUsername(profile.Nick); name != "" {
		return name
	}
	return sanitizeUsernameCandidate(profile.Mobile)
}

// syncUsername keeps a returning account aligned with the DingTalk display
// name, so accounts provisioned before the name became the username (and
// accounts renamed in DingTalk) pick the new name up on their next login.
// Failing to rename never blocks the login itself.
func (s *dingTalkAuthService) syncUsername(
	ctx context.Context, user *types.User, profile *dingtalk.Profile,
) *types.User {
	// Only accounts provisioned by DingTalk carry the mobile-number placeholder
	// email. A local account that merely bound DingTalk — the admin account, for
	// one — keeps the username someone chose for it.
	if !isDingTalkPlaceholderEmail(user.Email) {
		return user
	}
	name := sanitizeDingTalkUsername(profile.Nick)
	if name == "" {
		return user
	}
	candidate, err := availableUsername(ctx, s.users, name, user.ID)
	if err != nil {
		logger.Warnf(ctx, "dingtalk: resolve username for user %s: %v", user.ID, err)
		return user
	}
	if candidate == user.Username {
		return user
	}
	previous := user.Username
	user.Username = candidate
	if err := s.users.UpdateUser(ctx, user); err != nil {
		user.Username = previous
		logger.Warnf(ctx, "dingtalk: rename user %s to %q: %v", user.ID, candidate, err)
	}
	return user
}

// isDingTalkPlaceholderEmail reports whether email is the placeholder built
// from a mobile number (digits@jgjl.cn) rather than a real mailbox.
func isDingTalkPlaceholderEmail(email string) bool {
	local, domain, ok := strings.Cut(strings.ToLower(strings.TrimSpace(email)), "@")
	if !ok || "@"+domain != dingTalkEmailDomain || local == "" {
		return false
	}
	for _, r := range local {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// sanitizeDingTalkUsername keeps letters, digits, '_', '.' and '-' — Chinese
// names included, since that is what the DingTalk display name usually is.
// Every other run of characters collapses into a single dash.
func sanitizeDingTalkUsername(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '.' {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	result := strings.Trim(b.String(), "-._")
	if runes := []rune(result); len(runes) > 50 {
		result = strings.Trim(string(runes[:50]), "-._")
	}
	return result
}
