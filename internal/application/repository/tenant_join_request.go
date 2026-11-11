package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

// ErrPendingJoinRequestExists is returned by Create when a pending
// join request for (tenant_id, requester_user_id) already exists. The
// service layer maps it to its own sentinel for the handler.
var ErrPendingJoinRequestExists = errors.New("repository: pending join request already exists")

// tenantJoinRequestRepository implements
// interfaces.TenantJoinRequestRepository. Mirrors
// tenantInvitationRepository with the roles reversed.
type tenantJoinRequestRepository struct {
	db *gorm.DB
}

// NewTenantJoinRequestRepository constructs the repo against db.
// Wired up via the dig container alongside other repositories.
func NewTenantJoinRequestRepository(db *gorm.DB) interfaces.TenantJoinRequestRepository {
	return &tenantJoinRequestRepository{db: db}
}

// Create inserts a new pending request row. The partial unique index
// on (tenant_id, requester_user_id) WHERE status='pending' is the
// authoritative duplicate guard, but we also pre-check so the usual
// "double click" case surfaces as a clean sentinel instead of a raw
// constraint-violation string. See tenantInvitationRepository.Create
// for the same pattern and its concurrency caveat.
func (r *tenantJoinRequestRepository) Create(
	ctx context.Context,
	req *types.TenantJoinRequest,
) error {
	if req.Status == "" {
		req.Status = types.TenantJoinRequestStatusPending
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var probe types.TenantJoinRequest
		err := tx.
			Where("tenant_id = ? AND requester_user_id = ? AND status = ?",
				req.TenantID, req.RequesterUserID, types.TenantJoinRequestStatusPending).
			First(&probe).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			// clean to insert
		case err != nil:
			return err
		default:
			return ErrPendingJoinRequestExists
		}
		return tx.Create(req).Error
	})
}

// GetByID returns the request row (regardless of status) or
// (nil, nil) when missing.
func (r *tenantJoinRequestRepository) GetByID(
	ctx context.Context,
	id uint64,
) (*types.TenantJoinRequest, error) {
	var req types.TenantJoinRequest
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&req).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &req, nil
}

// GetPendingByPair returns the in-flight pending request, if any.
func (r *tenantJoinRequestRepository) GetPendingByPair(
	ctx context.Context,
	tenantID uint64,
	requesterUserID string,
) (*types.TenantJoinRequest, error) {
	var req types.TenantJoinRequest
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND requester_user_id = ? AND status = ?",
			tenantID, requesterUserID, types.TenantJoinRequestStatusPending).
		First(&req).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &req, nil
}

// tenantJoinRequestScope applies the pending-only filter and id DESC
// ordering shared by every list path.
func (r *tenantJoinRequestRepository) tenantJoinRequestScope(
	ctx context.Context,
	includeTerminal bool,
) *gorm.DB {
	q := r.db.WithContext(ctx).
		Model(&types.TenantJoinRequest{}).
		Order("id DESC")
	if !includeTerminal {
		q = q.Where("status = ?", types.TenantJoinRequestStatusPending)
	}
	return q
}

// ListByTenant returns requests for the tenant ordered by id DESC.
func (r *tenantJoinRequestRepository) ListByTenant(
	ctx context.Context,
	tenantID uint64,
	includeTerminal bool,
) ([]*types.TenantJoinRequest, error) {
	var rows []*types.TenantJoinRequest
	err := r.tenantJoinRequestScope(ctx, includeTerminal).
		Where("tenant_id = ?", tenantID).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// ListByRequester returns requests raised by the user across tenants.
func (r *tenantJoinRequestRepository) ListByRequester(
	ctx context.Context,
	requesterUserID string,
	includeTerminal bool,
) ([]*types.TenantJoinRequest, error) {
	var rows []*types.TenantJoinRequest
	err := r.tenantJoinRequestScope(ctx, includeTerminal).
		Where("requester_user_id = ?", requesterUserID).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// ListPendingForTenants returns pending requests across the supplied
// tenant ids. Empty id list short-circuits to an empty slice so the
// approver inbox renders cleanly for callers who Owner/admin nothing.
func (r *tenantJoinRequestRepository) ListPendingForTenants(
	ctx context.Context,
	tenantIDs []uint64,
) ([]*types.TenantJoinRequest, error) {
	if len(tenantIDs) == 0 {
		return []*types.TenantJoinRequest{}, nil
	}
	var rows []*types.TenantJoinRequest
	err := r.tenantJoinRequestScope(ctx, false).
		Where("tenant_id IN ?", tenantIDs).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// CountPendingForTenants counts pending requests across tenant ids.
func (r *tenantJoinRequestRepository) CountPendingForTenants(
	ctx context.Context,
	tenantIDs []uint64,
) (int64, error) {
	if len(tenantIDs) == 0 {
		return 0, nil
	}
	var count int64
	err := r.db.WithContext(ctx).
		Model(&types.TenantJoinRequest{}).
		Where("tenant_id IN ? AND status = ?",
			tenantIDs, types.TenantJoinRequestStatusPending).
		Count(&count).Error
	return count, err
}

// MarkStatusIfPending atomically transitions a row from pending to
// the supplied terminal status. The WHERE status='pending' filter
// makes concurrent accept/decline/withdraw clicks safe — only the
// first writer succeeds; the rest see RowsAffected=0 and receive
// gorm.ErrRecordNotFound (mapped to the not-pending sentinel).
func (r *tenantJoinRequestRepository) MarkStatusIfPending(
	ctx context.Context,
	id uint64,
	status types.TenantJoinRequestStatus,
	handledAt time.Time,
	handledBy *string,
) error {
	updates := map[string]any{
		"status":     status,
		"updated_at": time.Now(),
	}
	if status == types.TenantJoinRequestStatusAccepted ||
		status == types.TenantJoinRequestStatusDeclined {
		updates["handled_at"] = handledAt
		updates["handled_by"] = handledBy
	}
	res := r.db.WithContext(ctx).
		Model(&types.TenantJoinRequest{}).
		Where("id = ? AND status = ?", id, types.TenantJoinRequestStatusPending).
		Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// SweepExpired transitions all overdue pending rows to expired in a
// single batch. Mirrors tenantInvitationRepository.SweepExpired.
func (r *tenantJoinRequestRepository) SweepExpired(
	ctx context.Context,
	now time.Time,
) (int64, error) {
	res := r.db.WithContext(ctx).
		Model(&types.TenantJoinRequest{}).
		Where("status = ? AND expires_at < ?", types.TenantJoinRequestStatusPending, now).
		Updates(map[string]any{
			"status":     types.TenantJoinRequestStatusExpired,
			"updated_at": time.Now(),
		})
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}
