-- ZBBS-124 rollback: strip the price-quoting bullet from vendor roles.
--
-- regexp_replace nukes the appended bullet (and its leading newline)
-- from each vendor's instructions. Pattern uses [^\n]* to match the
-- whole bullet line regardless of phrasing nudges that might have
-- landed since the up-migration.
--
-- Idempotent — running on already-stripped rows is a no-op.

BEGIN;

UPDATE attribute_definition
   SET instructions = regexp_replace(instructions, E'\n- When you state a per-unit price out loud[^\n]*', '', 'g')
 WHERE slug IN ('tavernkeeper', 'merchant', 'herbalist');

COMMIT;
