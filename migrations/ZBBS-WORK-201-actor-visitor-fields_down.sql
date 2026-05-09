-- ZBBS-WORK-201 down — drop the visitor fields and their index.

BEGIN;

DROP INDEX IF EXISTS idx_actor_visitor_expires;
ALTER TABLE actor DROP COLUMN IF EXISTS visitor_disposition;
ALTER TABLE actor DROP COLUMN IF EXISTS visitor_origin;
ALTER TABLE actor DROP COLUMN IF EXISTS visitor_archetype;
ALTER TABLE actor DROP COLUMN IF EXISTS visitor_expires_at;

COMMIT;
