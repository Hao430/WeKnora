package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// DingTalkDirectoryService keeps local accounts aligned with the DingTalk
// corp directory. Both methods are idempotent: stream events are redelivered
// on failure and the reconciliation tick overlaps with them.
type DingTalkDirectoryService interface {
	// SyncDirectory walks the whole corp directory and provisions/restores
	// members. Returns ErrDingTalkDirectoryDisabled when the operator has
	// not enabled the sync.
	SyncDirectory(ctx context.Context) (*types.DingTalkDirectorySyncResult, error)
	// HandleDirectoryEvent applies one user_add_org / user_modify_org /
	// user_leave_org event.
	HandleDirectoryEvent(ctx context.Context, eventType string, corpID string, userIDs []string) error
}
