-- See versioned/000140_user_identity_display_name.up.sql.
ALTER TABLE user_identities ADD COLUMN display_name VARCHAR(191) NOT NULL DEFAULT '';
