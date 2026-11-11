-- tenant_join_requests — sqlite mirror of versioned 000138.
-- Adds tenants.is_public plus the join-request table (reverse flow of
-- tenant invitations: user applies to a public tenant, Owner/admin
-- approves and a viewer tenant_members row is created on accept).
ALTER TABLE tenants ADD COLUMN is_public BOOLEAN NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS tenant_join_requests (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id         INTEGER NOT NULL,
    requester_user_id VARCHAR(36) NOT NULL,
    message           VARCHAR(500) NOT NULL DEFAULT '',
    status            VARCHAR(20) NOT NULL DEFAULT 'pending',
    handled_by        VARCHAR(36),
    handled_at        DATETIME,
    expires_at        DATETIME NOT NULL,
    created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at        DATETIME
);
-- One pending request per (tenant, requester); terminal rows accumulate.
CREATE UNIQUE INDEX IF NOT EXISTS idx_tenant_join_requests_unique_pending
    ON tenant_join_requests(tenant_id, requester_user_id)
    WHERE status = 'pending' AND deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_tenant_join_requests_tenant
    ON tenant_join_requests(tenant_id);
CREATE INDEX IF NOT EXISTS idx_tenant_join_requests_requester
    ON tenant_join_requests(requester_user_id);
