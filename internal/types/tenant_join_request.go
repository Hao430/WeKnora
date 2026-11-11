package types

import (
	"time"

	"gorm.io/gorm"
)

// TenantJoinRequestStatus enumerates the lifecycle states of a single
// join request row — the reverse flow of TenantInvitationStatus (a
// user asks to join a public tenant instead of being invited). The
// legal transitions are:
//
//	pending -> accepted | declined | withdrawn | expired
//
// All non-pending states are terminal; once a row leaves pending it is
// kept for the audit trail and a new pending row can be issued.
type TenantJoinRequestStatus string

const (
	// TenantJoinRequestStatusPending is the initial state: the
	// Owner/admin has not yet acted and the row has not been withdrawn
	// or aged out.
	TenantJoinRequestStatusPending TenantJoinRequestStatus = "pending"
	// TenantJoinRequestStatusAccepted means an Owner/admin approved and
	// a corresponding active tenant_members row (role=viewer) has been
	// created.
	TenantJoinRequestStatusAccepted TenantJoinRequestStatus = "accepted"
	// TenantJoinRequestStatusDeclined means an Owner/admin rejected the
	// request. The requester may submit a new one afterwards.
	TenantJoinRequestStatusDeclined TenantJoinRequestStatus = "declined"
	// TenantJoinRequestStatusWithdrawn means the requester cancelled
	// their own pending request before it was handled.
	TenantJoinRequestStatusWithdrawn TenantJoinRequestStatus = "withdrawn"
	// TenantJoinRequestStatusExpired means the row outlived its
	// expires_at without being accepted/declined/withdrawn. Set by the
	// lazy-sweep run before every List/Act.
	TenantJoinRequestStatusExpired TenantJoinRequestStatus = "expired"
)

// IsTerminal reports whether s is a non-pending state. Used by the
// service layer to short-circuit accept/decline/withdraw on rows that
// have already been finalised.
func (s TenantJoinRequestStatus) IsTerminal() bool {
	switch s {
	case TenantJoinRequestStatusAccepted,
		TenantJoinRequestStatusDeclined,
		TenantJoinRequestStatusWithdrawn,
		TenantJoinRequestStatusExpired:
		return true
	}
	return false
}

// TenantJoinRequest is one pending or finalised join request raised by
// RequesterUserID against TenantID. The row is created in pending
// state when the user submits through the public tenant list; it flips
// to a terminal state when an Owner/admin accepts/declines, the
// requester withdraws, or the row expires.
//
// A tenant_members row is created only on accept (role=viewer); the
// request table is therefore the single source of truth for "pending
// intent" without polluting the authoritative member roster — same
// contract as tenant_invitations.
type TenantJoinRequest struct {
	// Surrogate primary key.
	ID uint64 `json:"id" gorm:"primaryKey;autoIncrement"`
	// TenantID references tenants.id.
	TenantID uint64 `json:"tenant_id" gorm:"not null;index"`
	// RequesterUserID references users.id — the user asking to join.
	// The (tenant_id, requester_user_id) partial unique index in
	// migration 000138 guards at most one pending row per pair;
	// terminal rows accumulate for audit.
	RequesterUserID string `json:"requester_user_id" gorm:"type:varchar(36);not null;index"`
	// Message is the requester-supplied reason, surfaced to the
	// approving Owner/admin in the inbox.
	Message string `json:"message" gorm:"type:varchar(500);not null;default:''"`
	// Status is the lifecycle state; see TenantJoinRequestStatus.
	Status TenantJoinRequestStatus `json:"status" gorm:"type:varchar(20);not null;default:'pending'"`
	// HandledBy records the Owner/admin user id that accepted or
	// declined. NULL while pending / withdrawn.
	HandledBy *string `json:"handled_by,omitempty" gorm:"type:varchar(36)"`
	// HandledAt is the accept/decline transition time. NULL while
	// pending / withdrawn.
	HandledAt *time.Time `json:"handled_at,omitempty"`
	// ExpiresAt bounds the pending lifetime (default 7d, same as
	// invitations); the lazy sweep flips overdue rows to expired.
	ExpiresAt time.Time `json:"expires_at" gorm:"not null"`
	// CreatedAt / UpdatedAt are GORM-managed timestamps.
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// DeletedAt is the GORM soft-delete timestamp.
	DeletedAt gorm.DeletedAt `json:"deleted_at,omitempty" gorm:"index"`
}

// IsExpired reports whether the pending row has aged past its
// expires_at at the supplied instant. Mirrors TenantInvitation.IsExpired.
func (r *TenantJoinRequest) IsExpired(now time.Time) bool {
	return r.Status == TenantJoinRequestStatusPending && now.After(r.ExpiresAt)
}

// TenantJoinRequestResponse is the API projection of a join request
// with requester / handler / tenant names hydrated best-effort.
type TenantJoinRequestResponse struct {
	ID              uint64                  `json:"id"`
	TenantID        uint64                  `json:"tenant_id"`
	TenantName      string                  `json:"tenant_name,omitempty"`
	RequesterUserID string                  `json:"requester_user_id"`
	RequesterName   string                  `json:"requester_name,omitempty"`
	RequesterEmail  string                  `json:"requester_email,omitempty"`
	Message         string                  `json:"message"`
	Status          TenantJoinRequestStatus `json:"status"`
	HandledBy       *string                 `json:"handled_by,omitempty"`
	HandlerName     string                  `json:"handler_name,omitempty"`
	HandledAt       *time.Time              `json:"handled_at,omitempty"`
	ExpiresAt       time.Time               `json:"expires_at"`
	CreatedAt       time.Time               `json:"created_at"`
}

// PublicTenantResponse is the slim projection served by the public
// tenant list/search endpoint. Only discoverability metadata — never
// settings, quotas, or member rosters.
type PublicTenantResponse struct {
	ID          uint64    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	MemberCount int64     `json:"member_count"`
	CreatedAt   time.Time `json:"created_at"`
}
