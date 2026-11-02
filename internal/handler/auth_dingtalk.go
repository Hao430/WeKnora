package handler

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/config"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/handler/dto"
	"github.com/Tencent/WeKnora/internal/infrastructure/dingtalk"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	secutils "github.com/Tencent/WeKnora/internal/utils"
)

const (
	// dingTalkNonceCookieName binds the signed state to this browser. Kept
	// separate from the OIDC nonce so the two flows cannot clobber each other
	// when a user starts both in the same profile.
	dingTalkNonceCookieName   = "weknora_dingtalk_nonce"
	dingTalkNonceCookieMaxAge = 600
	dingTalkAuthorizeEndpoint = "https://login.dingtalk.com/oauth2/auth"
	// dingTalkCodeFragment hands the authorization code back to the SPA. The
	// code is exchanged by the POST callback, which is also the only path the
	// container免登 uses — one exchange implementation for both flows.
	dingTalkCodeFragment = "#dingtalk_code="
	dingTalkErrFragment  = "#dingtalk_error="
)

// dingTalkCallbackRequest is the body of POST /auth/dingtalk/callback.
type dingTalkCallbackRequest struct {
	// Code is the DingTalk authorization code (JSAPI or OAuth2).
	Code string `json:"code" binding:"required"`
	// Source selects the exchange API: "inner" (container) or "oauth" (scan).
	Source string `json:"source" binding:"required"`
	// InviteToken joins the workspace the link points at. Sent by the SPA from
	// sessionStorage, never by DingTalk, and only trusted as far as a normal
	// share link is.
	InviteToken string `json:"invite_token,omitempty"`
	// Mode is "" / "login" for a normal login, "bind" to attach the identity to
	// the bearer-authenticated user.
	Mode string `json:"mode,omitempty"`
}

func (h *AuthHandler) dingTalkConfig() *config.DingTalkAuthConfig {
	if h.configInfo == nil {
		return nil
	}
	return h.configInfo.DingTalkAuth
}

// dingTalkReady reports whether the login entry may be used at all.
func (h *AuthHandler) dingTalkReady() bool {
	cfg := h.dingTalkConfig()
	return cfg != nil && cfg.Enable && h.dingTalkAuth != nil
}

// GetDingTalkConfig godoc
// @Summary      获取钉钉登录配置
// @Description  返回钉钉扫码登录是否启用，供登录页决定是否展示入口
// @Tags         认证
// @Produce      json
// @Success      200  {object}  types.DingTalkConfigResponse
// @Failure      404  {object}  apperrors.AppError  "钉钉登录未启用"
// @Router       /auth/dingtalk/config [get]
func (h *AuthHandler) GetDingTalkConfig(c *gin.Context) {
	if !h.dingTalkReady() {
		c.Error(apperrors.NewNotFoundError("DingTalk login is not enabled"))
		return
	}
	cfg := h.dingTalkConfig()
	c.JSON(http.StatusOK, &types.DingTalkConfigResponse{
		Success:             true,
		Enabled:             true,
		ProviderDisplayName: strings.TrimSpace(cfg.ProviderDisplayName),
		CorpID:              strings.TrimSpace(cfg.CorpID),
	})
}

// DingTalkStart godoc
// @Summary      发起钉钉登录
// @Description  默认 302 跳转到钉钉授权页；mode=bind 时返回授权地址 JSON，供已登录用户绑定账号。
// @Tags         认证
// @Produce      json
// @Param        mode  query     string  false  "bind 时返回 JSON 而非 302"
// @Success      302
// @Success      200   {object}  map[string]string  "authorization_url"
// @Failure      404   {object}  apperrors.AppError  "钉钉登录未启用"
// @Router       /auth/dingtalk/start [get]
func (h *AuthHandler) DingTalkStart(c *gin.Context) {
	if !h.dingTalkReady() {
		c.Error(apperrors.NewNotFoundError("DingTalk login is not enabled"))
		return
	}

	nonce, err := randomNonce()
	if err != nil {
		c.Error(apperrors.NewInternalServerError("Failed to start DingTalk login"))
		return
	}
	state, err := secutils.SignOIDCState(&secutils.OIDCStatePayload{
		Nonce:       nonce,
		RedirectURI: "/",
	})
	if err != nil {
		c.Error(apperrors.NewInternalServerError("Failed to start DingTalk login"))
		return
	}
	setDingTalkNonceCookie(c, nonce)

	authURL := buildDingTalkAuthorizationURL(
		strings.TrimSpace(h.dingTalkConfig().ClientID),
		dingTalkCallbackURL(c),
		state,
	)
	if strings.EqualFold(strings.TrimSpace(c.Query("mode")), "bind") {
		c.JSON(http.StatusOK, gin.H{"success": true, "authorization_url": authURL})
		return
	}
	c.Redirect(http.StatusFound, authURL)
}

// DingTalkCallback godoc
// @Summary      钉钉回调
// @Description  GET 为浏览器回调：校验 state/nonce 后把 code 交还给前端；POST 完成 code 交换，登录或绑定。
// @Tags         认证
// @Accept       json
// @Produce      json
// @Success      302
// @Success      200  {object}  dto.AuthLoginResponse
// @Failure      400  {object}  apperrors.AppError  "参数错误"
// @Failure      401  {object}  apperrors.AppError  "绑定未登录"
// @Failure      403  {object}  apperrors.AppError  "注册关闭或账号停用"
// @Failure      409  {object}  apperrors.AppError  "邮箱或钉钉身份已被占用"
// @Router       /auth/dingtalk/callback [get]
// @Router       /auth/dingtalk/callback [post]
func (h *AuthHandler) DingTalkCallback(c *gin.Context) {
	if c.Request.Method == http.MethodGet {
		h.dingTalkRedirectCallback(c)
		return
	}
	h.dingTalkExchange(c)
}

// dingTalkRedirectCallback only validates the browser round trip and hands the
// code back to the SPA. Exchanging here would need the invite token and the
// bind target in the signed state, both of which are already in the SPA.
func (h *AuthHandler) dingTalkRedirectCallback(c *gin.Context) {
	if providerErr := strings.TrimSpace(c.Query("error")); providerErr != "" {
		c.Redirect(http.StatusFound, "/"+dingTalkErrFragment+urlQueryEscape(providerErr))
		return
	}

	payload, err := secutils.VerifyOIDCState(strings.TrimSpace(c.Query("state")))
	if err != nil {
		logger.Errorf(c.Request.Context(), "Failed to verify DingTalk state: %v", err)
		c.Redirect(http.StatusFound, "/"+dingTalkErrFragment+urlQueryEscape("invalid_state"))
		return
	}
	cookie, cookieErr := c.Request.Cookie(dingTalkNonceCookieName)
	// One-time use: clear the binding cookie as soon as it is checked.
	clearDingTalkNonceCookie(c)
	if cookieErr != nil || cookie.Value == "" || cookie.Value != payload.Nonce {
		c.Redirect(http.StatusFound, "/"+dingTalkErrFragment+urlQueryEscape("invalid_state"))
		return
	}

	code := strings.TrimSpace(c.Query("code"))
	if code == "" {
		c.Redirect(http.StatusFound, "/"+dingTalkErrFragment+urlQueryEscape("missing_code"))
		return
	}
	c.Redirect(http.StatusFound, "/"+dingTalkCodeFragment+urlQueryEscape(code))
}

func (h *AuthHandler) dingTalkExchange(c *gin.Context) {
	ctx := c.Request.Context()
	if !h.dingTalkReady() {
		c.Error(apperrors.NewNotFoundError("DingTalk login is not enabled"))
		return
	}

	var req dingTalkCallbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewValidationError("Invalid DingTalk callback parameters").WithDetails(err.Error()))
		return
	}
	source := types.DingTalkLoginSource(strings.TrimSpace(req.Source))
	if source != types.DingTalkSourceInner && source != types.DingTalkSourceOAuth {
		c.Error(apperrors.NewValidationError("source must be inner or oauth"))
		return
	}
	code := strings.TrimSpace(req.Code)
	if code == "" {
		c.Error(apperrors.NewValidationError("code is required"))
		return
	}

	if strings.EqualFold(strings.TrimSpace(req.Mode), "bind") {
		user, authErr := h.dingTalkBearerUser(c)
		if authErr != nil {
			c.Error(apperrors.NewUnauthorizedError("Please sign in before binding a DingTalk account"))
			return
		}
		if err := h.dingTalkAuth.Bind(ctx, user.ID, types.DingTalkBindInput{Code: code, Source: source}); err != nil {
			h.failDingTalk(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"success": true, "message": "DingTalk account bound"})
		return
	}

	result, err := h.dingTalkAuth.Resolve(ctx, types.DingTalkLoginInput{
		Code:              code,
		Source:            source,
		InviteToken:       strings.TrimSpace(req.InviteToken),
		RegistrationMode:  h.resolveRegistrationMode(ctx),
		DefaultTenantMode: h.resolveDefaultTenantMode(ctx),
	})
	if err != nil {
		h.failDingTalk(c, err)
		return
	}
	c.JSON(http.StatusOK, h.dingTalkLoginResponse(ctx, result))
}

// dingTalkLoginResponse mirrors the password-login payload so the SPA can feed
// both through the same login handler. The active tenant is the account's home
// workspace; the follow-up /auth/me call reconciles it with the token claim.
func (h *AuthHandler) dingTalkLoginResponse(
	ctx context.Context, result *types.DingTalkLoginResult,
) *dto.AuthLoginResponse {
	user := result.User
	var tenant *types.Tenant
	if user.TenantID > 0 && h.tenantService != nil {
		if t, err := h.tenantService.GetTenantByID(ctx, user.TenantID); err == nil {
			tenant = t
		}
	}
	memberships := h.userService.BuildLoginMemberships(ctx, user, tenant)
	return dto.NewAuthLoginResponse(&types.LoginResponse{
		Success:      true,
		Message:      "Login successful",
		User:         user,
		ActiveTenant: tenant,
		Memberships:  memberships,
		Token:        result.AccessToken,
		RefreshToken: result.RefreshToken,
	})
}

// dingTalkBearerUser resolves the caller from the Authorization header. These
// routes live outside the auth middleware (the login ones must), so the bind
// mode authenticates itself.
func (h *AuthHandler) dingTalkBearerUser(c *gin.Context) (*types.User, error) {
	parts := strings.Fields(strings.TrimSpace(c.GetHeader("Authorization")))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return nil, errors.New("missing bearer token")
	}
	user, _, err := h.userService.ValidateToken(c.Request.Context(), parts[1])
	if err != nil {
		return nil, err
	}
	if user == nil {
		return nil, errors.New("invalid bearer token")
	}
	return user, nil
}

func (h *AuthHandler) failDingTalk(c *gin.Context, err error) {
	ctx := c.Request.Context()
	switch {
	case errors.Is(err, service.ErrDingTalkRegistrationDisabled):
		c.Error(apperrors.NewForbiddenError("Registration is disabled"))
	case errors.Is(err, service.ErrDingTalkInviteRequired):
		c.Error(apperrors.NewForbiddenError("An invitation link is required to join"))
	case errors.Is(err, service.ErrDingTalkAccountDisabled):
		c.Error(apperrors.NewForbiddenError("Account is disabled"))
	case errors.Is(err, service.ErrDingTalkMobileRequired):
		c.Error(apperrors.NewBadRequestError("Your DingTalk account has no mobile number; ask an administrator to add it"))
	case errors.Is(err, service.ErrDingTalkEmailTaken):
		c.Error(apperrors.NewConflictError("An account already exists for this phone number"))
	case errors.Is(err, apprepo.ErrIdentityAlreadyBound):
		c.Error(apperrors.NewConflictError("This DingTalk account is already bound to another user"))
	case errors.Is(err, service.ErrInvitationTokenInvalid):
		c.Error(&apperrors.AppError{
			Code:     apperrors.ErrNotFound,
			Message:  "invitation link is invalid or has been revoked",
			HTTPCode: http.StatusGone,
		})
	case errors.Is(err, dingtalk.ErrCodeRejected):
		logger.Warnf(ctx, "DingTalk rejected a login code: %v", err)
		c.Error(apperrors.NewBadRequestError("DingTalk login code is invalid or expired, please scan again"))
	default:
		logger.Errorf(ctx, "DingTalk login failed: %v", err)
		c.Error(apperrors.NewInternalServerError("DingTalk login failed"))
	}
}

// buildDingTalkAuthorizationURL builds the scan-login URL. scope=openid is the
// minimum; the mobile number only comes back once the app holds the phone
// permission, which is configured in the DingTalk console, not here.
func buildDingTalkAuthorizationURL(clientID, redirectURI, state string) string {
	query := url.Values{}
	query.Set("redirect_uri", redirectURI)
	query.Set("response_type", "code")
	query.Set("client_id", clientID)
	query.Set("scope", "openid")
	query.Set("state", state)
	query.Set("prompt", "consent")
	return dingTalkAuthorizeEndpoint + "?" + query.Encode()
}

// dingTalkCallbackURL derives the absolute callback URL from the request's own
// origin so the redirect_uri always matches the console configuration for this
// deployment.
func dingTalkCallbackURL(c *gin.Context) string {
	scheme := "http"
	if c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host + "/api/v1/auth/dingtalk/callback"
}

func setDingTalkNonceCookie(c *gin.Context, nonce string) {
	if nonce == "" {
		return
	}
	secure := c.Request.TLS != nil || strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https")
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(dingTalkNonceCookieName, nonce, dingTalkNonceCookieMaxAge, "/", "", secure, true)
}

func clearDingTalkNonceCookie(c *gin.Context) {
	c.SetCookie(dingTalkNonceCookieName, "", -1, "/", "", false, true)
}

func randomNonce() (string, error) {
	buffer := make([]byte, 24)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}
