package dingtalk

import (
	"context"
	"fmt"
	"strings"
)

// DirectoryClient is the contact-read surface used by the directory sync.
// It extends Client so the login-only fakes stay small: only the sync wiring
// needs the extra calls.
type DirectoryClient interface {
	Client
	// ListCorpUsers walks the department tree and returns every corp member
	// exactly once.
	ListCorpUsers(ctx context.Context) ([]*Profile, error)
	// GetUserByUserID resolves one member by corp userid. Directory events
	// only carry userids.
	GetUserByUserID(ctx context.Context, userID string) (*Profile, error)
}

// rootDeptID is the corp root department. listsub does not return it, so the
// walk seeds the queue with it explicitly.
const rootDeptID int64 = 1

// deptPageSize is the user/list page size. DingTalk caps it at 100.
const deptPageSize = 100

// ListCorpUsers walks every department and merges the member lists. Members
// belong to several departments; the first occurrence wins.
func (c *client) ListCorpUsers(ctx context.Context) ([]*Profile, error) {
	token, err := c.accessToken(ctx)
	if err != nil {
		return nil, err
	}

	deptIDs, err := c.allDeptIDs(ctx, token)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{}, 256)
	users := make([]*Profile, 0, 256)
	for _, deptID := range deptIDs {
		page, err := c.listDeptUsers(ctx, token, deptID)
		if err != nil {
			return nil, err
		}
		for _, u := range page {
			if u.UnionID == "" {
				continue
			}
			if _, ok := seen[u.UnionID]; ok {
				continue
			}
			seen[u.UnionID] = struct{}{}
			users = append(users, u)
		}
	}
	return users, nil
}

// allDeptIDs returns the root plus every descendant department id.
func (c *client) allDeptIDs(ctx context.Context, token string) ([]int64, error) {
	queue := []int64{rootDeptID}
	visited := map[int64]struct{}{rootDeptID: {}}
	ids := make([]int64, 0, 16)

	for len(queue) > 0 {
		deptID := queue[0]
		queue = queue[1:]
		ids = append(ids, deptID)

		var resp struct {
			ErrCode int    `json:"errcode"`
			ErrMsg  string `json:"errmsg"`
			Result  []struct {
				DeptID int64 `json:"dept_id"`
			} `json:"result"`
		}
		body := map[string]any{"dept_id": deptID, "language": "zh_CN"}
		endpoint := c.legacyBase + "/topapi/v2/department/listsub"
		if err := c.postQueryToken(ctx, endpoint, token, body, &resp); err != nil {
			return nil, err
		}
		if resp.ErrCode != 0 {
			return nil, fmt.Errorf("dingtalk department/listsub dept=%d errcode=%d errmsg=%s", deptID, resp.ErrCode, resp.ErrMsg)
		}
		for _, sub := range resp.Result {
			if sub.DeptID == 0 {
				continue
			}
			if _, ok := visited[sub.DeptID]; ok {
				continue
			}
			visited[sub.DeptID] = struct{}{}
			queue = append(queue, sub.DeptID)
		}
	}
	return ids, nil
}

// listDeptUsers pages through one department's member list.
func (c *client) listDeptUsers(ctx context.Context, token string, deptID int64) ([]*Profile, error) {
	users := make([]*Profile, 0, deptPageSize)
	var cursor int64
	for {
		var resp struct {
			ErrCode int    `json:"errcode"`
			ErrMsg  string `json:"errmsg"`
			Result  struct {
				HasMore    bool  `json:"has_more"`
				NextCursor int64 `json:"next_cursor"`
				List       []struct {
					UserID  string `json:"userid"`
					UnionID string `json:"unionid"`
					Name    string `json:"name"`
					Mobile  string `json:"mobile"`
					Active  *bool  `json:"active"`
				} `json:"list"`
			} `json:"result"`
		}
		body := map[string]any{
			"dept_id":  deptID,
			"cursor":   cursor,
			"size":     deptPageSize,
			"language": "zh_CN",
		}
		endpoint := c.legacyBase + "/topapi/v2/user/list"
		if err := c.postQueryToken(ctx, endpoint, token, body, &resp); err != nil {
			return nil, err
		}
		if resp.ErrCode != 0 {
			return nil, fmt.Errorf("dingtalk user/list dept=%d errcode=%d errmsg=%s", deptID, resp.ErrCode, resp.ErrMsg)
		}
		for _, u := range resp.Result.List {
			users = append(users, &Profile{
				UserID:  strings.TrimSpace(u.UserID),
				UnionID: strings.TrimSpace(u.UnionID),
				Nick:    strings.TrimSpace(u.Name),
				Mobile:  strings.TrimSpace(u.Mobile),
				Active:  u.Active,
			})
		}
		if !resp.Result.HasMore || resp.Result.NextCursor == 0 {
			return users, nil
		}
		cursor = resp.Result.NextCursor
	}
}

// GetUserByUserID resolves one corp member. It shares the user/get projection
// with the mobile lookup so the sync sees exactly the same fields.
func (c *client) GetUserByUserID(ctx context.Context, userID string) (*Profile, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, fmt.Errorf("dingtalk userid is required")
	}
	token, err := c.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	return c.userDetailByUserID(ctx, token, userID)
}
