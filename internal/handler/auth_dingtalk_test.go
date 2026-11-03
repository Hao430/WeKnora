package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	secutils "github.com/Tencent/WeKnora/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubDingTalkAuth struct {
	resolveInput  *types.DingTalkLoginInput
	resolveResult *types.DingTalkLoginResult
	resolveErr    error
	bindUserID    string
	bindErr       error
}

func (s *stubDingTalkAuth) Resolve(
	_ context.Context, in types.DingTalkLoginInput,
) (*types.DingTalkLoginResult, error) {
	s.resolveInput = &in
	return s.resolveResult, s.resolveErr
}

func (s *stubDingTalkAuth) Bind(_ context.Context, userID string, _ types.DingTalkBindInput) error {
	s.bindUserID = userID
	return s.bindErr
}

type stubDingTalkUserService struct {
	interfaces.UserService
	validateUser *types.User
	validateErr  error
}

func (s *stubDingTalkUserService) ValidateToken(context.Context, string) (*types.User, uint64, error) {
	if s.validateErr != nil {
		return nil, 0, s.validateErr
	}
	return s.validateUser, 0, nil
}

func (s *stubDingTalkUserService) BuildLoginMemberships(
	context.Context, *types.User, *types.Tenant,
) []types.Membership {
	return []types.Membership{}
}

func newDingTalkTestRouter(h *AuthHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(errorCapture())
	r.GET("/auth/dingtalk/config", h.GetDingTalkConfig)
	r.GET("/auth/dingtalk/start", h.DingTalkStart)
	r.GET("/auth/dingtalk/callback", h.DingTalkCallback)
	r.POST("/auth/dingtalk/callback", h.DingTalkCallback)
	return r
}

func newDingTalkHandler(dt interfaces.DingTalkAuthService, us interfaces.UserService) *AuthHandler {
	cfg := &config.Config{
		Auth: &config.AuthConfig{RegistrationMode: config.AuthRegistrationModeInviteRegister},
		DingTalkAuth: &config.DingTalkAuthConfig{
			Enable: true, CorpID: "corp-1", ClientID: "client-1", ProviderDisplayName: "钉钉",
		},
	}
	return NewAuthHandler(cfg, us, nil, nil, nil).SetDingTalkAuth(dt)
}

func TestGetDingTalkConfigHidesWhenDisabled(t *testing.T) {
	h := NewAuthHandler(&config.Config{}, nil, nil, nil, nil)
	r := newDingTalkTestRouter(h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/auth/dingtalk/config", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestGetDingTalkConfigReportsProvider(t *testing.T) {
	r := newDingTalkTestRouter(newDingTalkHandler(&stubDingTalkAuth{}, &stubDingTalkUserService{}))

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/auth/dingtalk/config", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body types.DingTalkConfigResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !body.Enabled || body.ProviderDisplayName != "钉钉" || body.CorpID != "corp-1" {
		t.Fatalf("body = %+v", body)
	}
}

func TestDingTalkStartRedirectsAndBindsNonce(t *testing.T) {
	r := newDingTalkTestRouter(newDingTalkHandler(&stubDingTalkAuth{}, &stubDingTalkUserService{}))

	req := httptest.NewRequest(http.MethodGet, "/auth/dingtalk/start", nil)
	req.Host = "kg.example.com"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	location, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatalf("location: %v", err)
	}
	if location.Query().Get("client_id") != "client-1" {
		t.Fatalf("client_id = %q", location.Query().Get("client_id"))
	}
	// The redirect_uri must match the console configuration for this host.
	if got := location.Query().Get("redirect_uri"); got != "http://kg.example.com/api/v1/auth/dingtalk/callback" {
		t.Fatalf("redirect_uri = %q", got)
	}
	if location.Query().Get("state") == "" {
		t.Fatal("state is empty")
	}
	if len(w.Result().Cookies()) == 0 {
		t.Fatal("nonce cookie not set")
	}
}

func TestDingTalkStartBindModeReturnsURL(t *testing.T) {
	r := newDingTalkTestRouter(newDingTalkHandler(&stubDingTalkAuth{}, &stubDingTalkUserService{}))

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/auth/dingtalk/start?mode=bind", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	authURL, _ := body["authorization_url"].(string)
	if !strings.HasPrefix(authURL, "https://login.dingtalk.com/oauth2/auth?") {
		t.Fatalf("authorization_url = %q", authURL)
	}
}

// signedDingTalkState mirrors what /start emits: an HMAC state plus the nonce
// cookie that pins it to this browser.
func signedDingTalkState(t *testing.T) (string, *http.Cookie) {
	t.Helper()
	state, err := secutils.SignOIDCState(&secutils.OIDCStatePayload{Nonce: "nonce-1", RedirectURI: "/"})
	if err != nil {
		t.Fatalf("sign state: %v", err)
	}
	return state, &http.Cookie{Name: dingTalkNonceCookieName, Value: "nonce-1"}
}

func TestDingTalkCallbackHandsCodeBackToSPA(t *testing.T) {
	r := newDingTalkTestRouter(newDingTalkHandler(&stubDingTalkAuth{}, &stubDingTalkUserService{}))
	state, cookie := signedDingTalkState(t)

	req := httptest.NewRequest(http.MethodGet, "/auth/dingtalk/callback?code=code-1&state="+url.QueryEscape(state), nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	if got := w.Header().Get("Location"); got != "/#dingtalk_code=code-1" {
		t.Fatalf("location = %q", got)
	}
}

// A sub-path deploy (FRONTEND_BASE_URL set) must keep both the callback URL
// and the post-callback redirect under that prefix — otherwise the browser
// lands on the site root, which serves no SPA and drops the code.
func TestDingTalkHonoursFrontendBasePath(t *testing.T) {
	cfg := &config.Config{
		FrontendBaseURL: "https://kg.example.com/app",
		Auth:            &config.AuthConfig{RegistrationMode: config.AuthRegistrationModeInviteRegister},
		DingTalkAuth:    &config.DingTalkAuthConfig{Enable: true, CorpID: "corp-1", ClientID: "client-1"},
	}
	h := NewAuthHandler(cfg, &stubDingTalkUserService{}, nil, nil, nil).SetDingTalkAuth(&stubDingTalkAuth{})
	r := newDingTalkTestRouter(h)

	startRec := httptest.NewRecorder()
	r.ServeHTTP(startRec, httptest.NewRequest(http.MethodGet, "/auth/dingtalk/start", nil))
	location, err := url.Parse(startRec.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "https://kg.example.com/app/api/v1/auth/dingtalk/callback",
		location.Query().Get("redirect_uri"))

	state, cookie := signedDingTalkState(t)
	req := httptest.NewRequest(http.MethodGet, "/auth/dingtalk/callback?code=code-1&state="+url.QueryEscape(state), nil)
	req.AddCookie(cookie)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, "https://kg.example.com/app/#dingtalk_code=code-1", w.Header().Get("Location"))
}

func TestDingTalkCallbackRejectsTamperedState(t *testing.T) {
	r := newDingTalkTestRouter(newDingTalkHandler(&stubDingTalkAuth{}, &stubDingTalkUserService{}))

	req := httptest.NewRequest(http.MethodGet, "/auth/dingtalk/callback?code=code-1&state=forged", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	if got := w.Header().Get("Location"); got != "/#dingtalk_error=invalid_state" {
		t.Fatalf("location = %q", got)
	}
}

func TestDingTalkCallbackRejectsStateWithoutNonceCookie(t *testing.T) {
	r := newDingTalkTestRouter(newDingTalkHandler(&stubDingTalkAuth{}, &stubDingTalkUserService{}))
	state, _ := signedDingTalkState(t)

	req := httptest.NewRequest(http.MethodGet, "/auth/dingtalk/callback?code=code-1&state="+url.QueryEscape(state), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if got := w.Header().Get("Location"); got != "/#dingtalk_error=invalid_state" {
		t.Fatalf("location = %q", got)
	}
}

func postDingTalkCallback(r *gin.Engine, body map[string]any, bearer string) *httptest.ResponseRecorder {
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/auth/dingtalk/callback", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestDingTalkCallbackLoginReturnsSession(t *testing.T) {
	dt := &stubDingTalkAuth{resolveResult: &types.DingTalkLoginResult{
		User:        &types.User{ID: "u-1", TenantID: 7, IsActive: true},
		AccessToken: "access", RefreshToken: "refresh",
	}}
	r := newDingTalkTestRouter(newDingTalkHandler(dt, &stubDingTalkUserService{}))

	w := postDingTalkCallback(r, map[string]any{
		"code": "code-1", "source": "oauth", "invite_token": "invite-1",
	}, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["token"] != "access" || body["refresh_token"] != "refresh" {
		t.Fatalf("body = %v", body)
	}
	if dt.resolveInput == nil || dt.resolveInput.InviteToken != "invite-1" {
		t.Fatalf("invite token not forwarded: %+v", dt.resolveInput)
	}
	// The handler resolves the policy; the client cannot choose its own.
	if dt.resolveInput.RegistrationMode != config.AuthRegistrationModeInviteRegister {
		t.Fatalf("registration mode = %q", dt.resolveInput.RegistrationMode)
	}
}

func TestDingTalkCallbackRequiresBearerForBind(t *testing.T) {
	dt := &stubDingTalkAuth{}
	r := newDingTalkTestRouter(newDingTalkHandler(dt, &stubDingTalkUserService{}))

	w := postDingTalkCallback(r, map[string]any{"code": "code-1", "source": "oauth", "mode": "bind"}, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if dt.bindUserID != "" {
		t.Fatal("bind must not run without a bearer")
	}
}

func TestDingTalkCallbackBindUsesBearerIdentity(t *testing.T) {
	dt := &stubDingTalkAuth{}
	us := &stubDingTalkUserService{validateUser: &types.User{ID: "u-1", IsActive: true}}
	r := newDingTalkTestRouter(newDingTalkHandler(dt, us))

	w := postDingTalkCallback(r, map[string]any{"code": "code-1", "source": "oauth", "mode": "bind"}, "token-1")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body.String())
	}
	if dt.bindUserID != "u-1" {
		t.Fatalf("bind user = %q", dt.bindUserID)
	}
}

func TestDingTalkCallbackMapsRegistrationErrors(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{"registration closed", service.ErrDingTalkRegistrationDisabled, http.StatusForbidden},
		{"invite required", service.ErrDingTalkInviteRequired, http.StatusForbidden},
		{"account disabled", service.ErrDingTalkAccountDisabled, http.StatusForbidden},
		{"email taken", service.ErrDingTalkEmailTaken, http.StatusConflict},
		{"invalid invite", service.ErrInvitationTokenInvalid, http.StatusGone},
		{"unexpected", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newDingTalkTestRouter(newDingTalkHandler(&stubDingTalkAuth{resolveErr: tt.err}, &stubDingTalkUserService{}))
			w := postDingTalkCallback(r, map[string]any{"code": "code-1", "source": "inner"}, "")
			if w.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tt.wantCode, w.Body.String())
			}
		})
	}
}

func TestDingTalkCallbackRejectsUnknownSource(t *testing.T) {
	r := newDingTalkTestRouter(newDingTalkHandler(&stubDingTalkAuth{}, &stubDingTalkUserService{}))
	w := postDingTalkCallback(r, map[string]any{"code": "code-1", "source": "sms"}, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}
