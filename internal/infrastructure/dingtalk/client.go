// Package dingtalk talks to the DingTalk Open Platform for login only.
// It deliberately does not reuse internal/im/dingtalk: that adapter serves the
// IM bot (appKey/appSecret robot credentials, different token cache and error
// semantics) and sharing it would couple two unrelated failure domains.
package dingtalk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	apiBaseURL    = "https://api.dingtalk.com"
	legacyBaseURL = "https://oapi.dingtalk.com"

	// requestTimeout bounds every call: the browser is waiting on a redirect,
	// so a hanging IdP must fail fast instead of holding the request open.
	requestTimeout = 5 * time.Second
	// tokenRefreshMargin renews the app token before it expires so an
	// in-flight login never races the expiry.
	tokenRefreshMargin = 5 * time.Minute
	// maxErrorBody keeps provider HTML/JSON out of our error strings.
	maxErrorBody = 512
)

// ErrCodeRejected means DingTalk refused the code or the profile lookup
// (expired code, wrong app, revoked permission). Callers translate it to a
// user-facing "please scan again" instead of an internal error.
var ErrCodeRejected = errors.New("dingtalk rejected the login code")

// Config is the subset of the DingTalk application credentials the login flow
// needs. ClientID/ClientSecret belong to the 网页应用, not the IM robot.
type Config struct {
	CorpID       string
	ClientID     string
	ClientSecret string
}

// Profile is the account projection the login flow consumes. The raw provider
// payload never leaves this package.
type Profile struct {
	// UnionID identifies the account across the corp's applications. It is the
	// subject stored in user_identities and is never logged.
	UnionID string
	// UserID is the corp-local userid. Empty on the browser-scan path, which
	// cannot read it.
	UserID string
	// Nick is the display name.
	Nick string
	// Mobile is the bound phone number. Empty when the app lacks the phone
	// permission — the caller decides whether that is fatal.
	Mobile string
	// DeptIDs is the account's department list (container path only).
	DeptIDs []int64
	// Active is nil when the code path cannot tell (browser scan): the caller
	// must treat nil as "allow", not as "disabled".
	Active *bool
}

// Client exchanges a DingTalk authorization code for an account profile.
type Client interface {
	// GetUserByInnerCode resolves the免登 code handed to a page inside the
	// DingTalk container.
	GetUserByInnerCode(ctx context.Context, code string) (*Profile, error)
	// GetUserByOAuthCode resolves a browser scan (OAuth2) code.
	GetUserByOAuthCode(ctx context.Context, code string) (*Profile, error)
	// GetUserByMobile resolves a corp member by phone number. Used by the
	// SystemAdmin pre-provisioning flow; the mobile lookup requires the
	// app contact read scope.
	GetUserByMobile(ctx context.Context, mobile string) (*Profile, error)
}

type client struct {
	cfg          Config
	httpClient   *http.Client
	apiBase      string
	legacyBase   string
	tokenMu      sync.Mutex
	appToken     string
	tokenExpires time.Time
}

// NewClient builds a login client. The HTTP client uses the default transport
// on purpose: a custom transport would bypass HTTPS_PROXY, which deployments
// behind an egress proxy depend on.
func NewClient(cfg Config) Client {
	return &client{
		cfg:        cfg,
		httpClient: &http.Client{Timeout: requestTimeout},
		apiBase:    apiBaseURL,
		legacyBase: legacyBaseURL,
	}
}

// GetUserByInnerCode runs the container免登 dance: code → userid → detail.
// The detail call is what carries the mobile number, and the mobile number is
// what the placeholder email is built from.
func (c *client) GetUserByInnerCode(ctx context.Context, code string) (*Profile, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, errors.New("dingtalk code is required")
	}
	token, err := c.accessToken(ctx)
	if err != nil {
		return nil, err
	}

	var info struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
		Result  struct {
			UserID string `json:"userid"`
		} `json:"result"`
	}
	endpoint := c.legacyBase + "/topapi/v2/user/getuserinfo"
	if err := c.postQueryToken(ctx, endpoint, token, map[string]string{"code": code}, &info); err != nil {
		return nil, err
	}
	if info.ErrCode != 0 {
		return nil, fmt.Errorf("%w: getuserinfo errcode=%d errmsg=%s", ErrCodeRejected, info.ErrCode, info.ErrMsg)
	}
	if info.Result.UserID == "" {
		return nil, fmt.Errorf("%w: no userid in the getuserinfo response", ErrCodeRejected)
	}

	return c.userDetailByUserID(ctx, token, info.Result.UserID)
}

// GetUserByMobile resolves a corp member by phone number, then reads the full
// profile. Same dance as GetUserByInnerCode minus the code exchange.
func (c *client) GetUserByMobile(ctx context.Context, mobile string) (*Profile, error) {
	mobile = strings.TrimSpace(mobile)
	if mobile == "" {
		return nil, errors.New("dingtalk mobile is required")
	}
	token, err := c.accessToken(ctx)
	if err != nil {
		return nil, err
	}

	var found struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
		Result  struct {
			UserID string `json:"userid"`
		} `json:"result"`
	}
	endpoint := c.legacyBase + "/topapi/v2/user/getbymobile"
	if err := c.postQueryToken(ctx, endpoint, token, map[string]string{"mobile": mobile}, &found); err != nil {
		return nil, err
	}
	if found.ErrCode != 0 {
		return nil, fmt.Errorf("%w: getbymobile errcode=%d errmsg=%s", ErrCodeRejected, found.ErrCode, found.ErrMsg)
	}
	if found.Result.UserID == "" {
		return nil, fmt.Errorf("%w: no userid in the getbymobile response", ErrCodeRejected)
	}
	return c.userDetailByUserID(ctx, token, found.Result.UserID)
}

// userDetailByUserID reads /topapi/v2/user/get for a corp userid and projects
// the fields the login and provisioning flows consume.
func (c *client) userDetailByUserID(ctx context.Context, token, userid string) (*Profile, error) {
	var detail struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
		Result  struct {
			UserID  string  `json:"userid"`
			UnionID string  `json:"unionid"`
			Name    string  `json:"name"`
			Mobile  string  `json:"mobile"`
			DeptIDs []int64 `json:"dept_id_list"`
			Active  *bool   `json:"active"`
		} `json:"result"`
	}
	endpoint := c.legacyBase + "/topapi/v2/user/get"
	body := map[string]any{"userid": userid, "language": "zh_CN"}
	if err := c.postQueryToken(ctx, endpoint, token, body, &detail); err != nil {
		return nil, err
	}
	if detail.ErrCode != 0 {
		return nil, fmt.Errorf("%w: user/get errcode=%d errmsg=%s", ErrCodeRejected, detail.ErrCode, detail.ErrMsg)
	}
	if detail.Result.UnionID == "" {
		return nil, fmt.Errorf("%w: no unionId in the user/get response", ErrCodeRejected)
	}
	return &Profile{
		UnionID: detail.Result.UnionID,
		UserID:  detail.Result.UserID,
		Nick:    detail.Result.Name,
		Mobile:  detail.Result.Mobile,
		DeptIDs: detail.Result.DeptIDs,
		Active:  detail.Result.Active,
	}, nil
}

// GetUserByOAuthCode exchanges a scan code for a user access token, then reads
// /contact/users/me. That endpoint exposes unionId, nick and mobile only — no
// userid, no departments, no activation flag — so Active stays nil here.
func (c *client) GetUserByOAuthCode(ctx context.Context, code string) (*Profile, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, errors.New("dingtalk code is required")
	}

	var token struct {
		AccessToken string `json:"accessToken"`
		CorpID      string `json:"corpId"`
	}
	body := map[string]string{
		"clientId":     c.cfg.ClientID,
		"clientSecret": c.cfg.ClientSecret,
		"code":         code,
		"grantType":    "authorization_code",
	}
	if err := c.postJSON(ctx, c.apiBase+"/v1.0/oauth2/userAccessToken", body, &token); err != nil {
		return nil, err
	}
	if token.AccessToken == "" {
		return nil, errors.New("dingtalk returned an empty user access token")
	}
	// unionId is corp-scoped, but an unexpected corp still means this token did
	// not come from the application we are configured for.
	if c.cfg.CorpID != "" && token.CorpID != "" && token.CorpID != c.cfg.CorpID {
		return nil, errors.New("dingtalk login code belongs to another corp")
	}

	var me struct {
		UnionID string `json:"unionId"`
		Nick    string `json:"nick"`
		Mobile  string `json:"mobile"`
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBase+"/v1.0/contact/users/me", nil)
	if err != nil {
		return nil, fmt.Errorf("build dingtalk request: %w", err)
	}
	req.Header.Set("x-acs-dingtalk-access-token", token.AccessToken)
	if err := c.do(req, &me); err != nil {
		return nil, err
	}
	if me.UnionID == "" {
		return nil, fmt.Errorf("%w: no unionId in the contact/users/me response", ErrCodeRejected)
	}
	return &Profile{UnionID: me.UnionID, Nick: me.Nick, Mobile: me.Mobile}, nil
}

// accessToken returns a cached app access token, refreshing it 5 minutes ahead
// of expiry. The lock is held across the refresh so a cold start issues one
// token request, not one per concurrent login.
func (c *client) accessToken(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if c.appToken != "" && time.Now().Before(c.tokenExpires) {
		return c.appToken, nil
	}

	var resp struct {
		AccessToken string `json:"accessToken"`
		ExpireIn    int64  `json:"expireIn"`
	}
	body := map[string]string{"appKey": c.cfg.ClientID, "appSecret": c.cfg.ClientSecret}
	if err := c.postJSON(ctx, c.apiBase+"/v1.0/oauth2/accessToken", body, &resp); err != nil {
		return "", err
	}
	if resp.AccessToken == "" {
		return "", errors.New("dingtalk returned an empty app access token")
	}

	ttl := time.Duration(resp.ExpireIn)*time.Second - tokenRefreshMargin
	if ttl <= 0 {
		ttl = time.Duration(resp.ExpireIn) * time.Second / 2
	}
	c.appToken = resp.AccessToken
	c.tokenExpires = time.Now().Add(ttl)
	return c.appToken, nil
}

// postQueryToken posts JSON to a legacy (oapi) endpoint, which authenticates
// with access_token in the query string. Errors report the path only: the URL
// carries the app token.
func (c *client) postQueryToken(ctx context.Context, endpoint, token string, body, out any) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("build dingtalk request: %w", err)
	}
	q := u.Query()
	q.Set("access_token", token)
	u.RawQuery = q.Encode()
	return c.postJSON(ctx, u.String(), body, out)
}

func (c *client) postJSON(ctx context.Context, endpoint string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode dingtalk request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build dingtalk request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, out)
}

func (c *client) do(req *http.Request, out any) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		// url.Error embeds the request URL, which may carry access_token.
		// Unwrap to keep the transport cause without the credentialed URL.
		return fmt.Errorf("dingtalk %s request failed: %w", req.URL.Path, errors.Unwrap(err))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return fmt.Errorf("dingtalk %s returned %d: %s", req.URL.Path, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode dingtalk %s response: %w", req.URL.Path, err)
	}
	return nil
}
