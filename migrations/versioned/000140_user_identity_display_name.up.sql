-- The profile page shows "bound as <nickname>" for provider identities.
-- The nickname is provider data (DingTalk Nick), not a local user field, so
-- it lives on the binding row and is written when the binding is created.
ALTER TABLE user_identities ADD COLUMN display_name VARCHAR(191) NOT NULL DEFAULT '';
