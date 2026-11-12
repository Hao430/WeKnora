-- Reverse of 000140_user_identity_display_name.
ALTER TABLE user_identities DROP COLUMN IF EXISTS display_name;
