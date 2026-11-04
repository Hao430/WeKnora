-- Third-party login identities (mirror of versioned 000137).
CREATE TABLE IF NOT EXISTS user_identities (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id VARCHAR(36) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider VARCHAR(32) NOT NULL,
    provider_tenant VARCHAR(64) NOT NULL DEFAULT '',
    subject VARCHAR(191) NOT NULL,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS uniq_user_identity
    ON user_identities(provider, provider_tenant, subject);
CREATE INDEX IF NOT EXISTS idx_user_identity_user ON user_identities(user_id);
