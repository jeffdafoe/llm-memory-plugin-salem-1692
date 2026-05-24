-- Rollback ZBBS-HOME-296 PR2 lodging rate — remove the
-- lodging_default_weekly_rate setting row. The loader falls back to its
-- code default (28) when the key is absent, so removing the row reverts to
-- the built-in default rather than disabling lodging. Idempotent.

BEGIN;

DELETE FROM setting WHERE key = 'lodging_default_weekly_rate';

COMMIT;
