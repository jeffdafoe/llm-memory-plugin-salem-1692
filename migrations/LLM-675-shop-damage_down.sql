-- LLM-675 down: remove the Debris asset and put the well use reference back.
--
-- REFUSES while a Debris overlay is placed. A placed overlay means a business
-- is damaged right now; deleting the asset would orphan the placement (no FK
-- from village_object.asset_id) and leave the shop damaged with nothing drawn.
-- Mend it first — POST /api/village/umbilical/object/damage {id, action:
-- "repair"} removes the overlay — then re-run. The engine must be STOPPED for
-- any hand DELETE on village_object (checkpoint-written).
--
-- The rolled-back engine does not know business damage: a business still
-- carrying damaged_at would read as a broken commons object to the old
-- completion code. The same repair clears it.
--
-- Deletes by id alone (asset.name is editable from the editor).

BEGIN;

DO $$
DECLARE placed int;
DECLARE damaged int;
BEGIN
    SELECT count(*) INTO placed
      FROM village_object
     WHERE asset_id = '019e5f00-c401-7a10-9e00-000000675001';
    IF placed > 0 THEN
        RAISE EXCEPTION
            'LLM-675 down: % Debris overlay(s) still in the village. Repair the damaged business(es) first (engine running, umbilical), then re-run.',
            placed;
    END IF;
    SELECT count(*) INTO damaged
      FROM village_object
     WHERE damaged_at IS NOT NULL
       AND owner_actor_id IS NOT NULL
       AND 'business' = ANY(tags);
    IF damaged > 0 THEN
        RAISE EXCEPTION
            'LLM-675 down: % business(es) still damaged. Repair them first, then re-run.',
            damaged;
    END IF;
END $$;

DELETE FROM asset_state WHERE asset_id = '019e5f00-c401-7a10-9e00-000000675001';
DELETE FROM asset WHERE id = '019e5f00-c401-7a10-9e00-000000675001';

UPDATE setting SET value = '60'
 WHERE key = 'well_damage_use_reference' AND value = '120';

COMMIT;
