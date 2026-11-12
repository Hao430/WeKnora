package types

import "time"

// IdentityProviderDingTalk is the provider key stored in user_identities for
// DingTalk logins. Phase one writes only this value; the column stays generic
// so a second IdP needs no schema change.
const IdentityProviderDingTalk = "dingtalk"

// UserIdentity links a local user account to a third-party login identity.
// The local user stays authoritative: sessions, memberships and permissions
// are still resolved from types.User, this table only answers "which local
// account owns this external account".
type UserIdentity struct {
	// ID is a surrogate key; callers match on the (provider, tenant, subject) tuple.
	ID uint64 `json:"id" gorm:"primaryKey;autoIncrement"`
	// UserID references users.id.
	UserID string `json:"user_id" gorm:"type:varchar(36);not null;index"`
	// Provider is the identity provider key, e.g. IdentityProviderDingTalk.
	Provider string `json:"provider" gorm:"type:varchar(32);not null;uniqueIndex:uniq_user_identity"`
	// ProviderTenant scopes the subject to one tenant of the provider (DingTalk corpId).
	// Empty for providers without a tenant concept.
	ProviderTenant string `json:"provider_tenant" gorm:"type:varchar(64);uniqueIndex:uniq_user_identity"`
	// Subject is the provider-side account id (DingTalk unionId). It is treated
	// as an opaque identifier and never logged.
	Subject string `json:"subject" gorm:"type:varchar(191);not null;uniqueIndex:uniq_user_identity"`
	// ExternalID is a secondary provider-side account id. For DingTalk it is
	// the corp-local userid, which directory events carry and the unionId
	// does not. Empty for identities bound before directory sync existed.
	ExternalID string `json:"external_id" gorm:"type:varchar(191);not null;default:''"`
	// DisplayName is the provider-side nickname shown in the profile page
	// ("bound as <name>"). Empty for bindings created before it was stored.
	DisplayName string `json:"display_name" gorm:"type:varchar(191);not null;default:''"`
	// CreatedAt is the first binding time.
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt is the last binding time.
	UpdatedAt time.Time `json:"updated_at"`
}
