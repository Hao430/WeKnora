package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// TenantJoinRequestRepository persists tenant_join_requests rows that
// capture the "user asks to join" intent before it becomes a real
// tenant_members row — the reverse flow of TenantInvitationRepository.
// All read methods skip GORM soft-deleted rows.
type TenantJoinRequestRepository interface {
	// Create inserts a new pending request. Returns the conflict error
	// sentinel (apprepo ErrPendingJoinRequestExists) if the (tenant_id,
	// requester) partial unique index rejects the insert.
	Create(ctx context.Context, req *types.TenantJoinRequest) error

	// GetByID fetches a request by surrogate id, or (nil, nil) if
	// missing. Used by Accept/Decline/Withdraw handlers.
	GetByID(ctx context.Context, id uint64) (*types.TenantJoinRequest, error)

	// GetPendingByPair returns the in-flight pending request for
	// (tenant, requester) or (nil, nil) when none exists.
	GetPendingByPair(ctx context.Context, tenantID uint64, requesterUserID string) (*types.TenantJoinRequest, error)

	// ListByTenant returns requests for the tenant ordered by id DESC
	// (newest first). includeTerminal controls whether non-pending rows
	// are returned — the management view shows pending by default.
	ListByTenant(ctx context.Context, tenantID uint64, includeTerminal bool) ([]*types.TenantJoinRequest, error)

	// ListByRequester returns requests raised by the user across all
	// tenants, newest first. Powers the applicant's own status view.
	ListByRequester(ctx context.Context, requesterUserID string, includeTerminal bool) ([]*types.TenantJoinRequest, error)

	// ListPendingForTenants returns pending requests across the
	// supplied tenant ids, newest first. Powers the approver inbox:
	// the handler gathers the tenants the caller Owner/admins and
	// passes their ids here.
	ListPendingForTenants(ctx context.Context, tenantIDs []uint64) ([]*types.TenantJoinRequest, error)

	// CountPendingForTenants returns the pending request count across
	// the supplied tenant ids, for the approver inbox badge.
	CountPendingForTenants(ctx context.Context, tenantIDs []uint64) (int64, error)

	// MarkStatusIfPending atomically transitions a row from pending to
	// the supplied status, recording handledAt / handledBy. Returns
	// gorm.ErrRecordNotFound when the row does not exist or is already
	// non-pending (the "stale state machine" signal).
	MarkStatusIfPending(
		ctx context.Context,
		id uint64,
		status types.TenantJoinRequestStatus,
		handledAt time.Time,
		handledBy *string,
	) error

	// SweepExpired transitions all pending rows whose expires_at is
	// before now into the expired state. Returns the affected row
	// count. Mirrors TenantInvitationRepository.SweepExpired.
	SweepExpired(ctx context.Context, now time.Time) (int64, error)
}
