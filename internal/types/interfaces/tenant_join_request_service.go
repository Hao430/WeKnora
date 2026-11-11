package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// TenantJoinRequestService is the business-logic layer over
// TenantJoinRequestRepository — the reverse flow of
// TenantInvitationService. It enforces the lifecycle invariants
// described in TenantJoinRequestStatus, runs the lazy expiry sweep,
// and brokers the accept-time hand-off into TenantMemberService so
// that "accept" produces both the request transition AND the
// tenant_members (viewer) row.
type TenantJoinRequestService interface {
	// Create submits a new pending join request for (tenantID,
	// requesterUserID). The tenant must exist and be public; conflicts
	// (already member, pending request, pending invitation) return
	// their sentinel errors.
	Create(ctx context.Context, tenantID uint64, requesterUserID string, message string) (*types.TenantJoinRequest, error)

	// Withdraw transitions the caller's own pending request into
	// withdrawn. Only the requester themselves.
	Withdraw(ctx context.Context, reqID uint64, callerUserID string) error

	// Accept transitions the pending request into accepted AND creates
	// the active tenant_members (viewer) row. callerUserID must hold
	// Owner/admin on the request's tenant (route gate applies the
	// first check; the service re-checks via the membership roles).
	Accept(ctx context.Context, reqID uint64, handlerUserID string) (*types.TenantMember, error)

	// Decline transitions the pending request into declined. Same
	// handler-authorisation rule as Accept.
	Decline(ctx context.Context, reqID uint64, handlerUserID string) error

	// GetByID returns a request or (nil, nil).
	GetByID(ctx context.Context, reqID uint64) (*types.TenantJoinRequest, error)

	// ListByTenant returns requests for a tenant after the lazy sweep.
	ListByTenant(ctx context.Context, tenantID uint64, includeTerminal bool) ([]*types.TenantJoinRequest, error)

	// ListByRequester returns the user's own requests after the sweep.
	ListByRequester(ctx context.Context, requesterUserID string, includeTerminal bool) ([]*types.TenantJoinRequest, error)

	// ListPendingForTenants returns pending requests across tenant ids
	// after the sweep — the approver inbox data source.
	ListPendingForTenants(ctx context.Context, tenantIDs []uint64) ([]*types.TenantJoinRequest, error)

	// CountPendingForTenants returns the approver-inbox badge count
	// after the sweep.
	CountPendingForTenants(ctx context.Context, tenantIDs []uint64) (int64, error)
}
