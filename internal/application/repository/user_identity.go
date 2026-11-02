package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
)

var (
	// ErrIdentityNotFound indicates that no binding exists for the external account.
	ErrIdentityNotFound = errors.New("user identity not found")
	// ErrIdentityAlreadyBound indicates that the external account is already
	// bound to a local user. Concurrent first logins race here; callers re-read
	// the row instead of treating it as a failure.
	ErrIdentityAlreadyBound = errors.New("user identity already bound")
)

// userIdentityRepository implements interfaces.UserIdentityRepository.
type userIdentityRepository struct {
	db *gorm.DB
}

// NewUserIdentityRepository creates a new user identity repository.
func NewUserIdentityRepository(db *gorm.DB) interfaces.UserIdentityRepository {
	return &userIdentityRepository{db: db}
}

// GetBySubject returns the binding for one external account.
func (r *userIdentityRepository) GetBySubject(
	ctx context.Context, provider, providerTenant, subject string,
) (*types.UserIdentity, error) {
	var identity types.UserIdentity
	err := r.db.WithContext(ctx).
		Where("provider = ? AND provider_tenant = ? AND subject = ?", provider, providerTenant, subject).
		First(&identity).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrIdentityNotFound
		}
		return nil, err
	}
	return &identity, nil
}

// ListByUserID returns every binding of one local user.
func (r *userIdentityRepository) ListByUserID(ctx context.Context, userID string) ([]*types.UserIdentity, error) {
	var identities []*types.UserIdentity
	if err := r.db.WithContext(ctx).Where("user_id = ?", userID).Find(&identities).Error; err != nil {
		return nil, err
	}
	return identities, nil
}

// Create inserts one binding.
func (r *userIdentityRepository) Create(ctx context.Context, identity *types.UserIdentity) error {
	if err := r.db.WithContext(ctx).Create(identity).Error; err != nil {
		if isDuplicateIdentity(err) {
			return ErrIdentityAlreadyBound
		}
		return err
	}
	return nil
}

// CreateUserWithIdentity inserts the account and its binding in one
// transaction. A failed binding insert rolls the account back, so a retried
// login can never hit a user whose identity row is missing.
func (r *userIdentityRepository) CreateUserWithIdentity(
	ctx context.Context, user *types.User, identity *types.UserIdentity,
) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Same rule as userRepository.CreateUser: serialising the zero
		// tenant as 0 would violate the PostgreSQL FK for a tenantless
		// (invite-driven) account. See internal/application/repository/user.go.
		insert := tx
		if user.TenantID == 0 {
			insert = insert.Omit("tenant_id")
		}
		if err := insert.Create(user).Error; err != nil {
			return fmt.Errorf("create user: %w", err)
		}
		identity.UserID = user.ID
		if err := tx.Create(identity).Error; err != nil {
			if isDuplicateIdentity(err) {
				return ErrIdentityAlreadyBound
			}
			return fmt.Errorf("create identity: %w", err)
		}
		return nil
	})
}

// isDuplicateIdentity recognises the uniq_user_identity violation.
// gorm.ErrDuplicatedKey covers drivers opened with TranslateError; the string
// fallback matches PostgreSQL "duplicate key value", SQLite "UNIQUE constraint
// failed" and MySQL "Duplicate entry". Kept local on purpose: the copies in
// the service package guard different indexes.
func isDuplicateIdentity(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate") || strings.Contains(msg, "unique constraint")
}
