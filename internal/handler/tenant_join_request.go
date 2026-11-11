package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/config"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	secutils "github.com/Tencent/WeKnora/internal/utils"
)

// TenantJoinRequestHandler exposes the reverse flow of the invitation
// handler: users submit join requests against PUBLIC tenants, and the
// tenants' Owner/admins approve or reject them from their inbox.
//
// Route-level RBAC:
//   - Public directory + applicant self-service (/tenant-join-requests*)
//     only require authentication; the service enforces "only the
//     requester can withdraw".
//   - Tenant-scoped list is Viewer+; accept/decline are Admin+ (owner
//     included via role level), matching the spec decision that both
//     Owner and admin can approve.
type TenantJoinRequestHandler struct {
	joinService   interfaces.TenantJoinRequestService
	tenantService interfaces.TenantService
	userService   interfaces.UserService
	memberService interfaces.TenantMemberService
	configInfo    *config.Config
}

// NewTenantJoinRequestHandler wires the dependencies. memberService
// hydrates member counts on the public directory; nil-guarded for
// tests (production wiring always injects it).
func NewTenantJoinRequestHandler(
	joinService interfaces.TenantJoinRequestService,
	tenantService interfaces.TenantService,
	userService interfaces.UserService,
	memberService interfaces.TenantMemberService,
	configInfo *config.Config,
) *TenantJoinRequestHandler {
	return &TenantJoinRequestHandler{
		joinService:   joinService,
		tenantService: tenantService,
		userService:   userService,
		memberService: memberService,
		configInfo:    configInfo,
	}
}

// createJoinRequestRequest is the JSON body for POST /tenant-join-requests.
// Message is the requester-supplied reason — surfaced to the approving
// Owner/admin in their inbox.
type createJoinRequestRequest struct {
	TenantID uint64 `json:"tenant_id" binding:"required"`
	Message  string `json:"message" binding:"required,max=500"`
}

// parseJoinRequestIDFromPath reads :req_id off the gin context.
func parseJoinRequestIDFromPath(c *gin.Context) (uint64, bool) {
	raw := strings.TrimSpace(c.Param("req_id"))
	if raw == "" {
		c.Error(apperrors.NewValidationError("join request id is required"))
		return 0, false
	}
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || v == 0 {
		c.Error(apperrors.NewValidationError("join request id must be a positive integer"))
		return 0, false
	}
	return v, true
}

// projectJoinRequest hydrates a join request into the API response
// shape: requester / handler names via usersByID, tenant name via
// tenantsByID. Missing rows degrade to bare ids.
func projectJoinRequest(
	req *types.TenantJoinRequest,
	usersByID map[string]*types.User,
	tenantsByID map[uint64]*types.Tenant,
) types.TenantJoinRequestResponse {
	resp := types.TenantJoinRequestResponse{
		ID:              req.ID,
		TenantID:        req.TenantID,
		RequesterUserID: req.RequesterUserID,
		Message:         req.Message,
		Status:          req.Status,
		HandledBy:       req.HandledBy,
		HandledAt:       req.HandledAt,
		ExpiresAt:       req.ExpiresAt,
		CreatedAt:       req.CreatedAt,
	}
	if u, ok := usersByID[req.RequesterUserID]; ok && u != nil {
		resp.RequesterEmail = u.Email
		resp.RequesterName = u.Username
	}
	if req.HandledBy != nil {
		if u, ok := usersByID[*req.HandledBy]; ok && u != nil {
			resp.HandlerName = u.Username
		}
	}
	if t, ok := tenantsByID[req.TenantID]; ok && t != nil {
		resp.TenantName = t.Name
	}
	return resp
}

// hydrateJoinUsers batches GetUsersByIDs over requester + handled_by
// ids. Best-effort: a transient failure logs and returns an empty map.
func (h *TenantJoinRequestHandler) hydrateJoinUsers(ctx context.Context, reqs []*types.TenantJoinRequest) map[string]*types.User {
	if len(reqs) == 0 {
		return map[string]*types.User{}
	}
	idSet := make(map[string]struct{}, len(reqs)*2)
	for _, req := range reqs {
		idSet[req.RequesterUserID] = struct{}{}
		if req.HandledBy != nil {
			idSet[*req.HandledBy] = struct{}{}
		}
	}
	ids := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	users, err := h.userService.GetUsersByIDs(ctx, ids)
	if err != nil {
		logger.Warnf(ctx, "join request list: batch user hydrate failed: %v", err)
		return map[string]*types.User{}
	}
	return users
}

// hydrateJoinTenants batches GetTenantsByIDs over the distinct tenant
// ids touched by reqs.
func (h *TenantJoinRequestHandler) hydrateJoinTenants(ctx context.Context, reqs []*types.TenantJoinRequest) map[uint64]*types.Tenant {
	if len(reqs) == 0 || h.tenantService == nil {
		return map[uint64]*types.Tenant{}
	}
	idSet := make(map[uint64]struct{}, len(reqs))
	for _, req := range reqs {
		idSet[req.TenantID] = struct{}{}
	}
	ids := make([]uint64, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	tenants, err := h.tenantService.GetTenantsByIDs(ctx, ids)
	if err != nil {
		logger.Warnf(ctx, "join request list: batch tenant hydrate failed: %v", err)
		return map[uint64]*types.Tenant{}
	}
	return tenants
}

// approverTenantIDs gathers the ids of tenants the caller actively
// Owner/admins — the scope of their approver inbox.
func (h *TenantJoinRequestHandler) approverTenantIDs(ctx context.Context, callerUserID string) []uint64 {
	memberships, err := h.memberService.ListByUser(ctx, callerUserID)
	if err != nil {
		logger.Warnf(ctx, "join request inbox: list memberships failed: %v", err)
		return nil
	}
	ids := make([]uint64, 0, len(memberships))
	for _, m := range memberships {
		if m.Status == types.TenantMemberStatusActive &&
			m.Role.HasPermission(types.TenantRoleAdmin) {
			ids = append(ids, m.TenantID)
		}
	}
	return ids
}

// ListPublicTenants godoc
// @Summary      公开空间列表
// @Description  分页列出 is_public 空间，keyword 可选按名称/描述过滤。仅返回可公开的元数据（id/名称/描述/成员数），用于"申请加入空间"目录页。
// @Tags         空间申请
// @Produce      json
// @Param        keyword    query  string  false  "按名称/描述过滤"
// @Param        page       query  int     false  "页码（从 1 起）"  default(1)
// @Param        page_size  query  int     false  "每页数量"        default(20)
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /tenants/public [get]
func (h *TenantJoinRequestHandler) ListPublicTenants(c *gin.Context) {
	ctx := c.Request.Context()
	keyword := strings.TrimSpace(c.Query("keyword"))
	page, pageSize, ok := parseListPagination(c)
	if !ok {
		return
	}

	tenants, total, err := h.tenantService.ListPublicTenants(ctx, keyword, page, pageSize)
	if err != nil {
		logger.Errorf(ctx, "ListPublicTenants failed: keyword=%s err=%v",
			secutils.SanitizeForLog(keyword), err)
		c.Error(apperrors.NewInternalServerError("failed to list public tenants").WithDetails(err.Error()))
		return
	}

	resp := make([]types.PublicTenantResponse, 0, len(tenants))
	for _, t := range tenants {
		item := types.PublicTenantResponse{
			ID:          t.ID,
			Name:        t.Name,
			Description: t.Description,
			CreatedAt:   t.CreatedAt,
		}
		if h.memberService != nil {
			members, mErr := h.memberService.ListByTenant(ctx, t.ID)
			if mErr == nil {
				item.MemberCount = int64(len(members))
			}
		}
		resp = append(resp, item)
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"tenants":   resp,
			"total":     total,
			"page":      page,
			"page_size": pageSize,
		},
	})
}

// CreateJoinRequest godoc
// @Summary      提交加入空间申请
// @Description  登录用户向公开空间提交加入申请（附申请理由）。已是成员、已有 pending 申请或已有 pending 邀请时返回冲突。
// @Tags         空间申请
// @Accept       json
// @Produce      json
// @Param        request  body  createJoinRequestRequest  true  "申请请求"
// @Success      201  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /tenant-join-requests [post]
func (h *TenantJoinRequestHandler) CreateJoinRequest(c *gin.Context) {
	ctx := c.Request.Context()
	var body createJoinRequestRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.Error(apperrors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}

	caller, _ := types.UserIDFromContext(ctx)
	req, err := h.joinService.Create(ctx, body.TenantID, caller, strings.TrimSpace(body.Message))
	if err != nil {
		switch {
		case errors.Is(err, service.ErrTenantNotPublic):
			c.Error(apperrors.NewNotFoundError(err.Error()))
		case errors.Is(err, service.ErrAlreadyMember):
			c.Error(apperrors.NewConflictError(err.Error()))
		case errors.Is(err, service.ErrPendingJoinRequestExists),
			errors.Is(err, service.ErrPendingInvitationExists):
			c.Error(apperrors.NewConflictError(err.Error()))
		default:
			logger.Errorf(ctx, "CreateJoinRequest failed: tenant=%d err=%v", body.TenantID, err)
			c.Error(apperrors.NewInternalServerError("failed to submit join request").WithDetails(err.Error()))
		}
		return
	}

	usersByID := h.hydrateJoinUsers(ctx, []*types.TenantJoinRequest{req})
	c.JSON(http.StatusCreated, gin.H{
		"success": true,
		"data":    projectJoinRequest(req, usersByID, nil),
	})
}

// ListMyJoinRequests godoc
// @Summary      我的加入申请列表
// @Description  列出我发出的加入申请（默认仅 pending，include_terminal=true 附带历史）。
// @Tags         空间申请
// @Produce      json
// @Param        include_terminal  query  bool  false  "是否包含终止态行"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /tenant-join-requests/mine [get]
func (h *TenantJoinRequestHandler) ListMyJoinRequests(c *gin.Context) {
	ctx := c.Request.Context()
	includeTerminal := strings.EqualFold(c.Query("include_terminal"), "true")
	caller, _ := types.UserIDFromContext(ctx)

	rows, err := h.joinService.ListByRequester(ctx, caller, includeTerminal)
	if err != nil {
		logger.Errorf(ctx, "ListMyJoinRequests failed: user=%s err=%v",
			secutils.SanitizeForLog(caller), err)
		c.Error(apperrors.NewInternalServerError("failed to list join requests").WithDetails(err.Error()))
		return
	}

	usersByID := h.hydrateJoinUsers(ctx, rows)
	tenantsByID := h.hydrateJoinTenants(ctx, rows)
	resp := make([]types.TenantJoinRequestResponse, 0, len(rows))
	for _, req := range rows {
		resp = append(resp, projectJoinRequest(req, usersByID, tenantsByID))
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"join_requests": resp,
		},
	})
}

// WithdrawMyJoinRequest godoc
// @Summary      撤回我的加入申请
// @Description  申请人撤回自己的 pending 申请（pending -> withdrawn），撤回后可再次提交。
// @Tags         空间申请
// @Produce      json
// @Param        id  path  int  true  "申请 ID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /tenant-join-requests/{id} [delete]
func (h *TenantJoinRequestHandler) WithdrawMyJoinRequest(c *gin.Context) {
	ctx := c.Request.Context()
	reqID, ok := parseJoinRequestIDFromPath(c)
	if !ok {
		return
	}
	caller, _ := types.UserIDFromContext(ctx)

	if err := h.joinService.Withdraw(ctx, reqID, caller); err != nil {
		switch {
		case errors.Is(err, service.ErrTenantJoinRequestNotFound):
			c.Error(apperrors.NewNotFoundError(err.Error()))
		case errors.Is(err, service.ErrJoinRequestForbidden):
			c.Error(apperrors.NewForbiddenError(err.Error()))
		case errors.Is(err, service.ErrJoinRequestNotPending):
			c.Error(apperrors.NewConflictError(err.Error()))
		default:
			logger.Errorf(ctx, "WithdrawMyJoinRequest failed: id=%d err=%v", reqID, err)
			c.Error(apperrors.NewInternalServerError("failed to withdraw join request").WithDetails(err.Error()))
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": "withdrawn"})
}

// ListApproverInbox godoc
// @Summary      审批人收件箱
// @Description  聚合我作为 Owner/admin 的所有空间的 pending 加入申请，收件箱数据源。
// @Tags         空间申请
// @Produce      json
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /tenant-join-requests/inbox [get]
func (h *TenantJoinRequestHandler) ListApproverInbox(c *gin.Context) {
	ctx := c.Request.Context()
	caller, _ := types.UserIDFromContext(ctx)

	rows, err := h.joinService.ListPendingForTenants(ctx, h.approverTenantIDs(ctx, caller))
	if err != nil {
		logger.Errorf(ctx, "ListApproverInbox failed: user=%s err=%v",
			secutils.SanitizeForLog(caller), err)
		c.Error(apperrors.NewInternalServerError("failed to list join request inbox").WithDetails(err.Error()))
		return
	}

	usersByID := h.hydrateJoinUsers(ctx, rows)
	tenantsByID := h.hydrateJoinTenants(ctx, rows)
	resp := make([]types.TenantJoinRequestResponse, 0, len(rows))
	for _, req := range rows {
		resp = append(resp, projectJoinRequest(req, usersByID, tenantsByID))
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"join_requests": resp,
		},
	})
}

// CountPendingJoinRequests godoc
// @Summary      待审申请计数
// @Description  返回我作为 Owner/admin 的所有空间的 pending 申请数，供铃铛角标轮询。
// @Tags         空间申请
// @Produce      json
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /tenant-join-requests/pending-count [get]
func (h *TenantJoinRequestHandler) CountPendingJoinRequests(c *gin.Context) {
	ctx := c.Request.Context()
	caller, _ := types.UserIDFromContext(ctx)

	count, err := h.joinService.CountPendingForTenants(ctx, h.approverTenantIDs(ctx, caller))
	if err != nil {
		logger.Errorf(ctx, "CountPendingJoinRequests failed: user=%s err=%v",
			secutils.SanitizeForLog(caller), err)
		c.Error(apperrors.NewInternalServerError("failed to count join requests").WithDetails(err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"pending_count": count,
		},
	})
}

// ListTenantJoinRequests godoc
// @Summary      列出空间的加入申请
// @Description  按空间列出待审/历史加入申请（管理页查历史用；审批动作在收件箱）。
// @Tags         空间申请
// @Produce      json
// @Param        id                path   string  true   "空间 ID"
// @Param        include_terminal  query  bool    false  "是否包含终止态行"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /tenants/{id}/join-requests [get]
func (h *TenantJoinRequestHandler) ListTenantJoinRequests(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := parseTenantIDFromPath(c)
	if !ok {
		return
	}
	includeTerminal := strings.EqualFold(c.Query("include_terminal"), "true")

	rows, err := h.joinService.ListByTenant(ctx, tenantID, includeTerminal)
	if err != nil {
		logger.Errorf(ctx, "ListTenantJoinRequests failed: tenant=%d err=%v", tenantID, err)
		c.Error(apperrors.NewInternalServerError("failed to list join requests").WithDetails(err.Error()))
		return
	}

	usersByID := h.hydrateJoinUsers(ctx, rows)
	resp := make([]types.TenantJoinRequestResponse, 0, len(rows))
	for _, req := range rows {
		// Tenant-scoped view: caller already knows the tenant.
		resp = append(resp, projectJoinRequest(req, usersByID, nil))
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"join_requests": resp,
		},
	})
}

// AcceptJoinRequest godoc
// @Summary      同意加入申请
// @Description  Owner/admin 同意申请：申请转为 accepted 并同流创建 viewer 成员行。
// @Tags         空间申请
// @Produce      json
// @Param        id      path  string  true  "空间 ID"
// @Param        req_id  path  int     true  "申请 ID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /tenants/{id}/join-requests/{req_id}/accept [post]
func (h *TenantJoinRequestHandler) AcceptJoinRequest(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := parseTenantIDFromPath(c)
	if !ok {
		return
	}
	reqID, ok := parseJoinRequestIDFromPath(c)
	if !ok {
		return
	}
	caller, _ := types.UserIDFromContext(ctx)

	member, err := h.joinService.Accept(ctx, reqID, caller)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrTenantJoinRequestNotFound):
			c.Error(apperrors.NewNotFoundError(err.Error()))
		case errors.Is(err, service.ErrJoinRequestForbidden):
			c.Error(apperrors.NewForbiddenError(err.Error()))
		case errors.Is(err, service.ErrJoinRequestNotPending):
			c.Error(apperrors.NewConflictError(err.Error()))
		case errors.Is(err, service.ErrJoinRequestExpired):
			c.Error(apperrors.NewConflictError(err.Error()))
		default:
			logger.Errorf(ctx, "AcceptJoinRequest failed: tenant=%d id=%d err=%v", tenantID, reqID, err)
			c.Error(apperrors.NewInternalServerError("failed to accept join request").WithDetails(err.Error()))
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"member": member,
		},
	})
}

// DeclineJoinRequest godoc
// @Summary      拒绝加入申请
// @Description  Owner/admin 拒绝申请（pending -> declined）。申请人可再次提交。
// @Tags         空间申请
// @Produce      json
// @Param        id      path  string  true  "空间 ID"
// @Param        req_id  path  int     true  "申请 ID"
// @Success      200  {object}  map[string]interface{}
// @Security     Bearer
// @Router       /tenants/{id}/join-requests/{req_id}/decline [post]
func (h *TenantJoinRequestHandler) DeclineJoinRequest(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := parseTenantIDFromPath(c)
	if !ok {
		return
	}
	reqID, ok := parseJoinRequestIDFromPath(c)
	if !ok {
		return
	}
	caller, _ := types.UserIDFromContext(ctx)

	if err := h.joinService.Decline(ctx, reqID, caller); err != nil {
		switch {
		case errors.Is(err, service.ErrTenantJoinRequestNotFound):
			c.Error(apperrors.NewNotFoundError(err.Error()))
		case errors.Is(err, service.ErrJoinRequestForbidden):
			c.Error(apperrors.NewForbiddenError(err.Error()))
		case errors.Is(err, service.ErrJoinRequestNotPending):
			c.Error(apperrors.NewConflictError(err.Error()))
		case errors.Is(err, service.ErrJoinRequestExpired):
			c.Error(apperrors.NewConflictError(err.Error()))
		default:
			logger.Errorf(ctx, "DeclineJoinRequest failed: tenant=%d id=%d err=%v", tenantID, reqID, err)
			c.Error(apperrors.NewInternalServerError("failed to decline join request").WithDetails(err.Error()))
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": "declined"})
}
