-- Revert LLM-363: drop the asset-level refresh-default template table.
--
-- Destructive: any authored/backfilled defaults are lost. Placed village_objects
-- are unaffected — their object_refresh rows are independent copies made at
-- placement time, not references into this table.

BEGIN;

DROP TABLE IF EXISTS asset_refresh_default;

COMMIT;
