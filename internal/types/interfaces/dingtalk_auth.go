package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// DingTalkAuthService turns a DingTalk authorization code into a local session
// or a bound identity. Account provisioning reuses the password-registration
// paths so both entry points share one准入 policy.
type DingTalkAuthService interface {
	// Resolve logs in (provisioning on first sight) and returns fresh tokens.
	// It returns ErrDingTalkInviteRequired / ErrDingTalkRegistrationDisabled
	// when the registration policy forbids creating an account.
	Resolve(ctx context.Context, in types.DingTalkLoginInput) (*types.DingTalkLoginResult, error)
	// Bind attaches a DingTalk identity to an already authenticated user.
	// It never provisions or issues tokens.
	Bind(ctx context.Context, userID string, in types.DingTalkBindInput) error
}
