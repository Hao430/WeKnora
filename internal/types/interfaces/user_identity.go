package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// UserIdentityRepository persists external login identities. Phase one needs
// exactly three operations: resolve an identity on login, list a user's
// bindings, and bind one. Account provisioning goes through UserService.Register
// so the registration policy stays in one place. Unbinding is deliberately
// absent (phase two).
type UserIdentityRepository interface {
	// GetBySubject returns the identity bound to the given external account,
	// or ErrIdentityNotFound.
	GetBySubject(ctx context.Context, provider, providerTenant, subject string) (*types.UserIdentity, error)
	// ListByUserID returns every binding of one local user.
	ListByUserID(ctx context.Context, userID string) ([]*types.UserIdentity, error)
	// Create inserts one binding. A duplicate binding is reported as
	// ErrIdentityAlreadyBound so callers can treat it as a race, not a 500.
	Create(ctx context.Context, identity *types.UserIdentity) error
}
