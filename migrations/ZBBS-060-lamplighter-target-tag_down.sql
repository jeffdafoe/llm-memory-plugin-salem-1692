-- Rollback ZBBS-060.
BEGIN;

DELETE FROM asset_state_tag WHERE tag = 'lamplighter-target';

COMMIT;
