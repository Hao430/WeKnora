-- Reverse of 000138_tenant_join_requests.
DO $$ BEGIN RAISE NOTICE '[Migration 000138] Dropping table: tenant_join_requests'; END $$;

DROP INDEX IF EXISTS idx_tenant_join_requests_requester;
DROP INDEX IF EXISTS idx_tenant_join_requests_tenant;
DROP INDEX IF EXISTS idx_tenant_join_requests_unique_pending;
DROP TABLE IF EXISTS tenant_join_requests;

ALTER TABLE tenants DROP COLUMN IF EXISTS is_public;

DO $$ BEGIN RAISE NOTICE '[Migration 000138] tenant_join_requests table dropped'; END $$;
