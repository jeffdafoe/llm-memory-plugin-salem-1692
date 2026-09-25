-- LLM-678 down: remove the snapped-tree road assets and village_object.expires_at.
--
-- REFUSES while any of the five assets is placed. A placed top means a road is
-- blocked right now; a placed stump is waiting out its week. The rolled-back
-- engine knows neither the assets nor expires_at, so the log would stay in the
-- road and the stump would never go. Clear the road first —
-- POST /api/village/umbilical/object/damage {id, action: "repair"} — and delete
-- any stump through the umbilical (object/delete) with the engine running,
-- then re-run.

BEGIN;

DO $$
DECLARE placed int;
BEGIN
    SELECT count(*) INTO placed
      FROM village_object
     WHERE asset_id IN ('019e5f00-c401-7a10-9e00-000000678001', '019e5f00-c401-7a10-9e00-000000678002',
                        '019e5f00-c401-7a10-9e00-000000678003', '019e5f00-c401-7a10-9e00-000000678004',
                        '019e5f00-c401-7a10-9e00-000000678005');
    IF placed > 0 THEN
        RAISE EXCEPTION
            'LLM-678 down: % fallen tree / stump placement(s) still in the village. Clear and delete them first (engine running, umbilical), then re-run.',
            placed;
    END IF;
END $$;

DELETE FROM asset_state
 WHERE asset_id IN ('019e5f00-c401-7a10-9e00-000000678001', '019e5f00-c401-7a10-9e00-000000678002',
                    '019e5f00-c401-7a10-9e00-000000678003', '019e5f00-c401-7a10-9e00-000000678004',
                    '019e5f00-c401-7a10-9e00-000000678005');
DELETE FROM asset
 WHERE id IN ('019e5f00-c401-7a10-9e00-000000678001', '019e5f00-c401-7a10-9e00-000000678002',
              '019e5f00-c401-7a10-9e00-000000678003', '019e5f00-c401-7a10-9e00-000000678004',
              '019e5f00-c401-7a10-9e00-000000678005');

ALTER TABLE village_object DROP COLUMN IF EXISTS expires_at;

COMMIT;
