package service

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// Sentinel errors returned by tenantJoinRequestService. Callers
// compare with errors.Is to render the appropriate HTTP responses.
var (
	// ErrTenantJoinRequestNotFound is returned when no request row matches
	// the supplied id.
	ErrTenantJoinRequestNotFound = errors.New("join request not found")

	// ErrPendingJoinRequestExists is returned by Create when the user
	// already has a pending request for the tenant. Maps to 409.
	ErrPendingJoinRequestExists = errors.New("you already have a pending join request for this tenant")

	// ErrJoinRequestNotPending is returned by Accept / Decline /
	// Withdraw when the row exists but has already been finalised.
	// Maps to 409.
	ErrJoinRequestNotPending = errors.New("join request is no longer pending")

	// ErrJoinRequestExpired is returned by Accept / Decline when the
	// pending row has aged past its expires_at. Maps to 410.
	ErrJoinRequestExpired = errors.New("join request has expired")

	// ErrJoinRequestForbidden is returned by Withdraw when the caller
	// is not the requester. Approver actions are gated at the route
	// layer (Admin gate) so this only surfaces on the /me/ paths.
	ErrJoinRequestForbidden = errors.New("only the requester can withdraw this join request")

	// ErrTenantNotPublic is returned by Create when the target tenant
	// does not exist or is not public. Deliberately one sentinel for
	// both so private tenants are not distinguishable from missing
	// ones (no existence leak). Maps to 404.
	ErrTenantNotPublic = errors.New("tenant is not accepting join requests")
)

// defaultJoinRequestTTL is the lifetime of a pending join request
// before the lazy sweep transitions it to expired. Same 7-day default
// as invitations; no env knob until someone actually asks for one.
const defaultJoinRequestTTL = 7 * 24 * time.Hour

// tenantJoinRequestService implements
// interfaces.TenantJoinRequestService.
type tenantJoinRequestService struct {
	repo      interfaces.TenantJoinRequestRepository
	tenantSvc interfaces.TenantService
	memberSvc interfaces.TenantMemberService
	// invRepo backs the "pending invitation wins" conflict check in
	// Create: if the Owner already invited this user, submitting a
	// join request is redundant and the invitation is the right move.
	invRepo interfaces.TenantInvitationRepository
	audit   interfaces.AuditLogService // optional; nil ⇒ no audit
	now     func() time.Time           // injection seam for tests
}

// NewTenantJoinRequestService wires the dependencies. memberSvc is
// required (Accept must create the tenant_members row); audit is
// optional and matches the same nil-safe pattern the invitation
// service uses.
func NewTenantJoinRequestService(
	repo interfaces.TenantJoinRequestRepository,
	tenantSvc interfaces.TenantService,
	memberSvc interfaces.TenantMemberService,
	invRepo interfaces.TenantInvitationRepository,
	audit interfaces.AuditLogService,
) interfaces.TenantJoinRequestService {
	return &tenantJoinRequestService{
		repo:      repo,
		tenantSvc: tenantSvc,
		memberSvc: memberSvc,
		invRepo:   invRepo,
		audit:     audit,
		now:       time.Now,
	}
}

// emitAudit is the best-effort audit hook; mirrors tenantInvitationService.
func (s *tenantJoinRequestService) emitAudit(ctx context.Context, entry *types.AuditLog) {
	if s.audit == nil {
		return
	}
	_ = s.audit.Log(ctx, entry)
}

// joinRequestDetails packs the request id into the audit Details.
func joinRequestDetails(reqID uint64) types.JSON {
	b, _ := json.Marshal(map[string]any{"join_request_id": reqID})
	return types.JSON(b)
}

// sweep transitions overdue pending rows to expired before any read.
// Failures are logged and swallowed — a transient sweep error must
// not block a user from reading their inbox; the next call retries.
func (s *tenantJoinRequestService) sweep(ctx context.Context) {
	if _, err := s.repo.SweepExpired(ctx, s.now()); err != nil {
		logger.Warnf(ctx, "tenant_join_request lazy sweep failed: %v", err)
	}
}

// Create submits a new pending join request. Validation order mirrors
// the spec: public-tenant gate (404 for missing OR private — no
// existence leak), already-member, pending request, pending
// invitation conflicts.
func (s *tenantJoinRequestService) Create(
	ctx context.Context,
	tenantID uint64,
	requesterUserID string,
	message string,
) (*types.TenantJoinRequest, error) {
	tenant, err := s.tenantSvc.GetTenantByID(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if tenant == nil || !tenant.IsPublic {
		return nil, ErrTenantNotPublic
	}

	membership, err := s.memberSvc.GetMembership(ctx, requesterUserID, tenantID)
	if err != nil {
		return nil, err
	}
	if membership != nil && membership.Status == types.TenantMemberStatusActive {
		return nil, ErrAlreadyMember
	}

	// An in-flight invitation means the Owner already wants this user
	// in — accepting the invitation is the right move, so reject the
	// request with the invitation sentinel (reused for the 409 body).
	pendingInv, err := s.invRepo.GetPendingByPair(ctx, tenantID, requesterUserID)
	if err != nil {
		return nil, err
	}
	if pendingInv != nil {
		return nil, ErrPendingInvitationExists
	}

	now := s.now()
	req := &types.TenantJoinRequest{
		TenantID:        tenantID,
		RequesterUserID: requesterUserID,
		Message:         message,
		Status:          types.TenantJoinRequestStatusPending,
		ExpiresAt:       now.Add(defaultJoinRequestTTL),
	}
	if err := s.repo.Create(ctx, req); err != nil {
		if errors.Is(err, apprepo.ErrPendingJoinRequestExists) {
			return nil, ErrPendingJoinRequestExists
		}
		return nil, err
	}

	s.emitAudit(ctx, &types.AuditLog{
		TenantID:     tenantID,
		ActorUserID:  auditActor(ctx),
		ActorRole:    auditActorRole(ctx),
		Action:       types.AuditActionJoinRequestSubmitted,
		TargetType:   "tenant_join_request",
		TargetID:     strconv.FormatUint(req.ID, 10),
		TargetUserID: requesterUserID,
		Outcome:      types.AuditOutcomeSuccess,
		Details:      joinRequestDetails(req.ID),
	})
	return req, nil
}

// Withdraw transitions the caller's own pending request into
// withdrawn.
func (s *tenantJoinRequestService) Withdraw(
	ctx context.Context,
	reqID uint64,
	callerUserID string,
) error {
	s.sweep(ctx)

	req, err := s.repo.GetByID(ctx, reqID)
	if err != nil {
		return err
	}
	if req == nil {
		return ErrTenantJoinRequestNotFound
	}
	if req.RequesterUserID != callerUserID {
		return ErrJoinRequestForbidden
	}
	if req.Status != types.TenantJoinRequestStatusPending {
		return ErrJoinRequestNotPending
	}

	if err := s.repo.MarkStatusIfPending(ctx, reqID, types.TenantJoinRequestStatusWithdrawn, s.now(), nil); err != nil {
		return ErrJoinRequestNotPending
	}

	s.emitAudit(ctx, &types.AuditLog{
		TenantID:     req.TenantID,
		ActorUserID:  auditActor(ctx),
		ActorRole:    auditActorRole(ctx),
		Action:       types.AuditActionJoinRequestWithdrawn,
		TargetType:   "tenant_join_request",
		TargetID:     strconv.FormatUint(req.ID, 10),
		TargetUserID: req.RequesterUserID,
		Outcome:      types.AuditOutcomeSuccess,
		Details:      joinRequestDetails(req.ID),
	})
	return nil
}

// Accept transitions the pending request into accepted AND creates
// the viewer tenant_members row. Same ordering rationale as the
// invitation Accept: the request transition commits first, then
// AddMember runs; an "already a member" failure there is the
// idempotent outcome, anything else is logged with the request left
// accepted so the state machine stays consistent.
func (s *tenantJoinRequestService) Accept(
	ctx context.Context,
	reqID uint64,
	handlerUserID string,
) (*types.TenantMember, error) {
	s.sweep(ctx)

	req, err := s.getPendingForHandler(ctx, reqID, handlerUserID)
	if err != nil {
		return nil, err
	}

	now := s.now()
	if err := s.repo.MarkStatusIfPending(ctx, reqID, types.TenantJoinRequestStatusAccepted, now, &handlerUserID); err != nil {
		return nil, ErrJoinRequestNotPending
	}

	member, err := s.memberSvc.AddMember(ctx, req.RequesterUserID, req.TenantID, types.TenantRoleViewer, &handlerUserID)
	if err != nil {
		if errors.Is(err, ErrMembershipAlreadyExists) {
			existing, getErr := s.memberSvc.GetMembership(ctx, req.RequesterUserID, req.TenantID)
			if getErr == nil && existing != nil {
				s.emitAcceptAudit(ctx, req)
				return existing, nil
			}
		}
		logger.Errorf(ctx,
			"join request %d accepted but tenant_members insert failed: %v",
			reqID, err)
		return nil, err
	}

	s.emitAcceptAudit(ctx, req)
	return member, nil
}

// Decline transitions the pending request into declined.
func (s *tenantJoinRequestService) Decline(
	ctx context.Context,
	reqID uint64,
	handlerUserID string,
) error {
	s.sweep(ctx)

	req, err := s.getPendingForHandler(ctx, reqID, handlerUserID)
	if err != nil {
		return err
	}

	if err := s.repo.MarkStatusIfPending(ctx, reqID, types.TenantJoinRequestStatusDeclined, s.now(), &handlerUserID); err != nil {
		return ErrJoinRequestNotPending
	}

	s.emitAudit(ctx, &types.AuditLog{
		TenantID:     req.TenantID,
		ActorUserID:  auditActor(ctx),
		ActorRole:    auditActorRole(ctx),
		Action:       types.AuditActionJoinRequestDeclined,
		TargetType:   "tenant_join_request",
		TargetID:     strconv.FormatUint(req.ID, 10),
		TargetUserID: req.RequesterUserID,
		Outcome:      types.AuditOutcomeSuccess,
		Details:      joinRequestDetails(req.ID),
	})
	return nil
}

// getPendingForHandler loads the request and re-checks that the
// handler holds Owner/admin on its tenant. The route gate applies the
// same check; this re-check covers service-level callers and keeps
// the authorisation next to the state transition.
func (s *tenantJoinRequestService) getPendingForHandler(
	ctx context.Context,
	reqID uint64,
	handlerUserID string,
) (*types.TenantJoinRequest, error) {
	req, err := s.repo.GetByID(ctx, reqID)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, ErrTenantJoinRequestNotFound
	}
	if req.Status != types.TenantJoinRequestStatusPending {
		return nil, ErrJoinRequestNotPending
	}
	if req.IsExpired(s.now()) {
		return nil, ErrJoinRequestExpired
	}
	membership, err := s.memberSvc.GetMembership(ctx, handlerUserID, req.TenantID)
	if err != nil {
		return nil, err
	}
	if membership == nil ||
		membership.Status != types.TenantMemberStatusActive ||
		!membership.Role.HasPermission(types.TenantRoleAdmin) {
		return nil, ErrJoinRequestForbidden
	}
	return req, nil
}

// emitAcceptAudit writes the rbac.join_request_accepted audit row.
func (s *tenantJoinRequestService) emitAcceptAudit(ctx context.Context, req *types.TenantJoinRequest) {
	s.emitAudit(ctx, &types.AuditLog{
		TenantID:     req.TenantID,
		ActorUserID:  auditActor(ctx),
		ActorRole:    auditActorRole(ctx),
		Action:       types.AuditActionJoinRequestAccepted,
		TargetType:   "tenant_join_request",
		TargetID:     strconv.FormatUint(req.ID, 10),
		TargetUserID: req.RequesterUserID,
		Outcome:      types.AuditOutcomeSuccess,
		Details:      joinRequestDetails(req.ID),
	})
}

// GetByID returns a request or (nil, nil).
func (s *tenantJoinRequestService) GetByID(ctx context.Context, reqID uint64) (*types.TenantJoinRequest, error) {
	return s.repo.GetByID(ctx, reqID)
}

// ListByTenant returns requests for a tenant after the lazy sweep.
func (s *tenantJoinRequestService) ListByTenant(
	ctx context.Context,
	tenantID uint64,
	includeTerminal bool,
) ([]*types.TenantJoinRequest, error) {
	s.sweep(ctx)
	return s.repo.ListByTenant(ctx, tenantID, includeTerminal)
}

// ListByRequester returns the user's own requests after the sweep.
func (s *tenantJoinRequestService) ListByRequester(
	ctx context.Context,
	requesterUserID string,
	includeTerminal bool,
) ([]*types.TenantJoinRequest, error) {
	s.sweep(ctx)
	return s.repo.ListByRequester(ctx, requesterUserID, includeTerminal)
}

// ListPendingForTenants returns pending requests across tenant ids
// after the sweep — the approver inbox data source.
func (s *tenantJoinRequestService) ListPendingForTenants(
	ctx context.Context,
	tenantIDs []uint64,
) ([]*types.TenantJoinRequest, error) {
	s.sweep(ctx)
	return s.repo.ListPendingForTenants(ctx, tenantIDs)
}

// CountPendingForTenants returns the approver-inbox badge count after
// the sweep.
func (s *tenantJoinRequestService) CountPendingForTenants(
	ctx context.Context,
	tenantIDs []uint64,
) (int64, error) {
	s.sweep(ctx)
	return s.repo.CountPendingForTenants(ctx, tenantIDs)
}
