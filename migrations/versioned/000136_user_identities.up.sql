-- Third-party login identities (DingTalk in phase one). users.id stays the
-- only local identity, so JWT claims, auth_tokens and memberships need no
-- change; this table only maps an external account to a local user.
CREATE TABLE IF NOT EXISTS user_identities (
    id BIGSERIAL PRIMARY KEY,
    user_id VARCHAR(36) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider VARCHAR(32) NOT NULL,
    provider_tenant VARCHAR(64) NOT NULL DEFAULT '',
    subject VARCHAR(191) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The login lookup key: one external account binds to exactly one local user.
CREATE UNIQUE INDEX IF NOT EXISTS uniq_user_identity
    ON user_identities(provider, provider_tenant, subject);
CREATE INDEX IF NOT EXISTS idx_user_identity_user ON user_identities(user_id);
