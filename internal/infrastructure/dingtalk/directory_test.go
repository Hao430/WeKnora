package dingtalk

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func decodeBody(r *http.Request, out any) error {
	return json.NewDecoder(r.Body).Decode(out)
}

func TestListCorpUsersWalksDepartmentsPagesAndDeduplicates(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.0/oauth2/accessToken", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"accessToken": "app-token", "expireIn": 7200})
	})
	mux.HandleFunc("/topapi/v2/department/listsub", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			DeptID int64 `json:"dept_id"`
		}
		require.NoError(t, decodeBody(r, &body))
		if body.DeptID == 1 {
			writeJSON(t, w, map[string]any{"errcode": 0, "result": []map[string]int64{{"dept_id": 2}}})
			return
		}
		writeJSON(t, w, map[string]any{"errcode": 0, "result": []any{}})
	})
	mux.HandleFunc("/topapi/v2/user/list", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			DeptID int64 `json:"dept_id"`
			Cursor int64 `json:"cursor"`
		}
		require.NoError(t, decodeBody(r, &body))
		user := func(id, union string) map[string]any {
			return map[string]any{"userid": id, "unionid": union, "name": "n-" + id, "mobile": "13800000000"}
		}
		switch {
		case body.DeptID == 1 && body.Cursor == 0:
			writeJSON(t, w, map[string]any{"errcode": 0, "result": map[string]any{
				"has_more": true, "next_cursor": 10, "list": []any{user("u1", "union1")},
			}})
		case body.DeptID == 1:
			writeJSON(t, w, map[string]any{"errcode": 0, "result": map[string]any{
				"has_more": false, "list": []any{user("u2", "union2")},
			}})
		default:
			// u1 belongs to two departments and must not be returned twice.
			writeJSON(t, w, map[string]any{"errcode": 0, "result": map[string]any{
				"has_more": false, "list": []any{user("u1", "union1"), user("u3", "union3")},
			}})
		}
	})

	c := newTestClient(t, mux)
	users, err := c.ListCorpUsers(context.Background())
	require.NoError(t, err)
	require.Len(t, users, 3)
	assert.Equal(t, []string{"u1", "u2", "u3"}, []string{users[0].UserID, users[1].UserID, users[2].UserID})
	assert.Equal(t, "union1", users[0].UnionID)
}

func TestListCorpUsersRejectsProviderErrcode(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.0/oauth2/accessToken", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"accessToken": "app-token", "expireIn": 7200})
	})
	mux.HandleFunc("/topapi/v2/department/listsub", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"errcode": 60011, "errmsg": "no permission"})
	})

	c := newTestClient(t, mux)
	_, err := c.ListCorpUsers(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "60011")
}

func TestGetUserByUserIDReadsProfile(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.0/oauth2/accessToken", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"accessToken": "app-token", "expireIn": 7200})
	})
	mux.HandleFunc("/topapi/v2/user/get", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			UserID string `json:"userid"`
		}
		require.NoError(t, decodeBody(r, &body))
		require.Equal(t, "u-1", body.UserID)
		writeJSON(t, w, map[string]any{"errcode": 0, "result": map[string]any{
			"userid": "u-1", "unionid": "union-1", "name": "张三", "mobile": "13800000001", "active": true,
		}})
	})

	c := newTestClient(t, mux)
	profile, err := c.GetUserByUserID(context.Background(), "u-1")
	require.NoError(t, err)
	assert.Equal(t, "union-1", profile.UnionID)
	assert.Equal(t, "13800000001", profile.Mobile)

	_, err = c.GetUserByUserID(context.Background(), "  ")
	require.Error(t, err)
}
