-- See versioned/000139_user_identity_external_id.up.sql.
ALTER TABLE user_identities ADD COLUMN external_id VARCHAR(191) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_user_identity_external
    ON user_identities(provider, provider_tenant, external_id);
