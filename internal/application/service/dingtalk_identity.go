package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

// ListIdentities returns the caller's bound external accounts. The profile
// page uses it to show "bound as <nickname>" instead of the bind button.
func (s *dingTalkAuthService) ListIdentities(
	ctx context.Context, userID string,
) ([]types.UserIdentitySummary, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, nil
	}
	rows, err := s.identityRepo.ListByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list identities: %w", err)
	}
	out := make([]types.UserIdentitySummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, types.UserIdentitySummary{
			Provider:    row.Provider,
			DisplayName: row.DisplayName,
			BoundAt:     row.CreatedAt,
		})
	}
	return out, nil
}
