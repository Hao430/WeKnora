-- Reverse of 000054_tenant_join_requests.
DROP INDEX IF EXISTS idx_tenant_join_requests_requester;
DROP INDEX IF EXISTS idx_tenant_join_requests_tenant;
DROP INDEX IF EXISTS idx_tenant_join_requests_unique_pending;
DROP TABLE IF EXISTS tenant_join_requests;
ALTER TABLE tenants DROP COLUMN is_public;
