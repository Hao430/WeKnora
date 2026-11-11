-- The DingTalk directory sync keys local accounts by the corp-local userid,
-- which `user_leave_org` events carry. unionId stays the login subject; the
-- userid is only an extra lookup key so a departure can be resolved after the
-- member is already gone from the corp (user/get no longer answers then).
ALTER TABLE user_identities ADD COLUMN external_id VARCHAR(191) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_user_identity_external
    ON user_identities(provider, provider_tenant, external_id);
