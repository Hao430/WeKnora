package types

import "time"

// DingTalkLoginSource identifies which DingTalk code flow produced a login.
// The two flows return different profile fields (see dingtalk.Profile), so the
// service and the frontend both need to know which one ran.
type DingTalkLoginSource string

const (
	// DingTalkSourceInner is the免登 flow inside the DingTalk container: the
	// page receives a JSAPI code that resolves to userid + department + status.
	DingTalkSourceInner DingTalkLoginSource = "inner"
	// DingTalkSourceOAuth is the browser scan flow: an OAuth2 code resolves to
	// unionId + nick + mobile only.
	DingTalkSourceOAuth DingTalkLoginSource = "oauth"
)

// DingTalkLoginInput is one DingTalk login or first-time provisioning request.
type DingTalkLoginInput struct {
	// Code is the DingTalk authorization code (JSAPI or OAuth2).
	Code string
	// Source tells the service which exchange to run.
	Source DingTalkLoginSource
	// InviteToken joins (or provisions into) the inviting workspace.
	InviteToken string
	// RegistrationMode is the resolved auth.registration_mode value. The
	// handler passes it in so DingTalk shares one policy with password
	// registration instead of reading system settings a second time.
	RegistrationMode string
	// DefaultTenantMode is the resolved auth.default_tenant_mode, used only
	// when a new account is created without an invitation.
	DefaultTenantMode TenantProvisioningMode
}

// DingTalkBindInput is one "bind my DingTalk account" request.
type DingTalkBindInput struct {
	// Code is the DingTalk authorization code.
	Code string
	// Source tells the service which exchange to run.
	Source DingTalkLoginSource
}

// DingTalkConfigResponse tells the login page whether to render the DingTalk
// entry. It never carries the client secret.
type DingTalkConfigResponse struct {
	Success             bool   `json:"success"`
	Enabled             bool   `json:"enabled"`
	ProviderDisplayName string `json:"provider_display_name,omitempty"`
	CorpID              string `json:"corp_id,omitempty"`
}

// DingTalkLoginResult carries a completed login.
type DingTalkLoginResult struct {
	// User is the local account owning the DingTalk identity.
	User *User
	// AccessToken and RefreshToken are freshly issued for this login.
	AccessToken  string
	RefreshToken string
	// IsNewUser reports that this login provisioned the account.
	IsNewUser bool
}

// DingTalkDirectorySyncResult summarizes one directory reconciliation pass.
type DingTalkDirectorySyncResult struct {
	// Scanned is the number of corp members returned by DingTalk.
	Scanned int
	// Created counts newly provisioned accounts.
	Created int
	// Bound counts existing local accounts that just got the identity.
	Bound int
	// Restored counts returning members whose account was re-enabled.
	Restored int
	// Skipped counts members without a usable phone number or with an
	// email collision.
	Skipped int
}

// UserIdentitySummary is the API projection of one bound external account,
// returned by GET /auth/identities and rendered by the profile page.
type UserIdentitySummary struct {
	Provider string `json:"provider"`
	// DisplayName is the provider-side nickname. Empty for bindings made
	// before the nickname was stored.
	DisplayName string    `json:"display_name,omitempty"`
	BoundAt     time.Time `json:"bound_at"`
}
