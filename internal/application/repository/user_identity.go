package repository

import (
	"context"
	"errors"
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

// GetByExternalID returns the binding for one secondary provider-side id. An
// empty id is never a valid lookup key: every row without an external id would
// match, and the first one would win.
func (r *userIdentityRepository) GetByExternalID(
	ctx context.Context, provider, providerTenant, externalID string,
) (*types.UserIdentity, error) {
	if strings.TrimSpace(externalID) == "" {
		return nil, ErrIdentityNotFound
	}
	var identity types.UserIdentity
	err := r.db.WithContext(ctx).
		Where("provider = ? AND provider_tenant = ? AND external_id = ?", provider, providerTenant, externalID).
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

// Update persists the mutable fields of a binding.
func (r *userIdentityRepository) Update(ctx context.Context, identity *types.UserIdentity) error {
	return r.db.WithContext(ctx).Model(&types.UserIdentity{}).
		Where("id = ?", identity.ID).
		Updates(map[string]any{
			"external_id":  identity.ExternalID,
			"display_name": identity.DisplayName,
		}).Error
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
