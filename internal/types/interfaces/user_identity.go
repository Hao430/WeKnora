package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// UserIdentityRepository persists external login identities.
type UserIdentityRepository interface {
	// GetBySubject returns the identity bound to the given external account,
	// or ErrIdentityNotFound.
	GetBySubject(ctx context.Context, provider, providerTenant, subject string) (*types.UserIdentity, error)
	// GetByExternalID returns the identity bound to a secondary provider-side
	// id (DingTalk userid). Returns ErrIdentityNotFound when unbound or when
	// externalID is empty.
	GetByExternalID(ctx context.Context, provider, providerTenant, externalID string) (*types.UserIdentity, error)
	// ListByUserID returns every binding of one local user.
	ListByUserID(ctx context.Context, userID string) ([]*types.UserIdentity, error)
	// Create inserts one binding. A duplicate binding is reported as
	// ErrIdentityAlreadyBound so callers can treat it as a race, not a 500.
	Create(ctx context.Context, identity *types.UserIdentity) error
	// Update persists mutable fields (external_id, display_name) of a binding.
	Update(ctx context.Context, identity *types.UserIdentity) error
}
