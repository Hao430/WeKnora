-- Migration: 000138_tenant_join_requests
-- Adds the reverse flow of tenant invitations: a registered user can
-- discover PUBLIC tenants via a list/search endpoint and submit a join
-- request; the tenant Owner/admin approves and a tenant_members row
-- (role=viewer) is created in the same transaction.
--
-- tenants.is_public gates discoverability only ("can be searched and
-- can receive join requests"); it never widens content access —
-- non-members still go through the regular RBAC checks.
--
-- tenant_join_requests mirrors tenant_invitations (000048): a pending
-- intent table kept separate from tenant_members so the authoritative
-- roster only ever contains active rows, terminal rows accumulate for
-- audit, and a partial unique index guards the in-flight request.
--
-- Status machine:
--   pending  -> accepted  (terminal; Owner/admin approves, member row created)
--   pending  -> declined  (terminal; Owner/admin rejects)
--   pending  -> withdrawn (terminal; requester cancels)
--   pending  -> expired   (terminal; lazy sweep after expires_at)
DO $$ BEGIN RAISE NOTICE '[Migration 000138] Adding column tenants.is_public'; END $$;

ALTER TABLE tenants ADD COLUMN IF NOT EXISTS is_public BOOLEAN NOT NULL DEFAULT FALSE;

DO $$ BEGIN RAISE NOTICE '[Migration 000138] Creating table: tenant_join_requests'; END $$;

CREATE TABLE IF NOT EXISTS tenant_join_requests (
    id                BIGSERIAL    PRIMARY KEY,
    tenant_id         INTEGER      NOT NULL,
    requester_user_id VARCHAR(36)  NOT NULL,
    message           VARCHAR(500) NOT NULL DEFAULT '',
    status            VARCHAR(20)  NOT NULL DEFAULT 'pending',
    handled_by        VARCHAR(36),
    handled_at        TIMESTAMP WITH TIME ZONE,
    expires_at        TIMESTAMP WITH TIME ZONE NOT NULL,
    created_at        TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at        TIMESTAMP WITH TIME ZONE
);

-- At most one PENDING request per (tenant, requester); terminal rows
-- accumulate freely for auditability (same scheme as 000048).
CREATE UNIQUE INDEX IF NOT EXISTS idx_tenant_join_requests_unique_pending
    ON tenant_join_requests(tenant_id, requester_user_id)
    WHERE status = 'pending' AND deleted_at IS NULL;

-- Read paths:
--   - Approver inbox: pending requests across tenants the caller
--     Owner/admins.
--   - Tenant management history list per tenant.
CREATE INDEX IF NOT EXISTS idx_tenant_join_requests_tenant
    ON tenant_join_requests(tenant_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_tenant_join_requests_requester
    ON tenant_join_requests(requester_user_id) WHERE deleted_at IS NULL;

DO $$ BEGIN RAISE NOTICE '[Migration 000138] tenant_join_requests table ready'; END $$;
