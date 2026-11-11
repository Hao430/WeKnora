-- Reverse of 000139_user_identity_external_id.
DROP INDEX IF EXISTS idx_user_identity_external;
ALTER TABLE user_identities DROP COLUMN IF EXISTS external_id;
