package dingtalk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestClient points both API bases at one fake server so the legacy and
// v1.0 endpoints can be exercised without touching the network.
func newTestClient(t *testing.T, handler http.Handler) *client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	c := NewClient(Config{CorpID: "corp-1", ClientID: "id", ClientSecret: "secret"}).(*client)
	c.apiBase = server.URL
	c.legacyBase = server.URL
	return c
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(v))
}

func TestClientInnerCodeResolvesProfileAndCachesToken(t *testing.T) {
	var tokenCalls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.0/oauth2/accessToken", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&tokenCalls, 1)
		writeJSON(t, w, map[string]any{"accessToken": "app-token", "expireIn": 7200})
	})
	mux.HandleFunc("/topapi/v2/user/getuserinfo", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "app-token", r.URL.Query().Get("access_token"))
		writeJSON(t, w, map[string]any{"errcode": 0, "result": map[string]string{"userid": "u-1"}})
	})
	mux.HandleFunc("/topapi/v2/user/get", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"errcode": 0,
			"result": map[string]any{
				"userid": "u-1", "unionid": "union-1", "name": "张三",
				"mobile": "13800000000", "dept_id_list": []int64{1, 2}, "active": true,
			},
		})
	})

	c := newTestClient(t, mux)
	ctx := context.Background()

	profile, err := c.GetUserByInnerCode(ctx, "code-1")
	require.NoError(t, err)
	assert.Equal(t, "union-1", profile.UnionID)
	assert.Equal(t, "13800000000", profile.Mobile)
	assert.Equal(t, []int64{1, 2}, profile.DeptIDs)
	require.NotNil(t, profile.Active)
	assert.True(t, *profile.Active)

	// Second login reuses the cached app token.
	_, err = c.GetUserByInnerCode(ctx, "code-2")
	require.NoError(t, err)
	assert.EqualValues(t, 1, atomic.LoadInt32(&tokenCalls), "app token must be cached")
}

func TestClientInnerCodeTreatsMissingActiveAsUnknown(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.0/oauth2/accessToken", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"accessToken": "app-token", "expireIn": 7200})
	})
	mux.HandleFunc("/topapi/v2/user/getuserinfo", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"errcode": 0, "result": map[string]string{"userid": "u-1"}})
	})
	mux.HandleFunc("/topapi/v2/user/get", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"errcode": 0,
			"result":  map[string]any{"userid": "u-1", "unionid": "union-1"},
		})
	})

	profile, err := newTestClient(t, mux).GetUserByInnerCode(context.Background(), "code-1")
	require.NoError(t, err)
	assert.Nil(t, profile.Active, "a response without active must not read as disabled")
}

func TestClientOAuthFlowRejectsForeignCorp(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.0/oauth2/userAccessToken", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"accessToken": "user-token", "corpId": "corp-other"})
	})

	_, err := newTestClient(t, mux).GetUserByOAuthCode(context.Background(), "code-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "another corp")
}

func TestClientOAuthFlowResolvesProfile(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.0/oauth2/userAccessToken", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"accessToken": "user-token", "corpId": "corp-1"})
	})
	mux.HandleFunc("/v1.0/contact/users/me", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "user-token", r.Header.Get("x-acs-dingtalk-access-token"))
		writeJSON(t, w, map[string]string{"unionId": "union-2", "nick": "李四", "mobile": "13900000000"})
	})

	profile, err := newTestClient(t, mux).GetUserByOAuthCode(context.Background(), "code-1")
	require.NoError(t, err)
	assert.Equal(t, "union-2", profile.UnionID)
	assert.Equal(t, "13900000000", profile.Mobile)
	assert.Nil(t, profile.Active)
}

func TestClientErrorsDoNotLeakAccessToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.0/oauth2/accessToken", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{"accessToken": "app-token", "expireIn": 7200})
	})
	mux.HandleFunc("/topapi/v2/user/getuserinfo", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errcode":40014,"errmsg":"invalid access_token"}`))
	})

	_, err := newTestClient(t, mux).GetUserByInnerCode(context.Background(), "code-1")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "app-token", "the credentialed query string must not reach the error")
}
