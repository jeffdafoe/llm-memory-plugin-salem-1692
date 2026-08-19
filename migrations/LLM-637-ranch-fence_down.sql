-- LLM-637 down: remove the Ranch Fence asset and its piece states.
--
-- REFUSES while segments are placed. There is no FK from village_object.asset_id
-- to asset.id, so nothing stops this from orphaning placed fence segments and
-- nothing forces their removal; a placed pen is editor work, not migration
-- state (LLM-600 convention). Remove the runs first — from the editor ("Delete
-- fence run"), or by an explicit DELETE with the engine STOPPED, since
-- village_object is checkpoint-written and a running engine resurrects the
-- rows — then re-run.
--
-- Deletes are by id alone: asset.name is editable from the editor, so guarding
-- on it would make the rollback silently no-op after a rename. The pack row
-- goes only if nothing else references it AND it still carries the metadata the
-- up wrote (on the live box the gates reference it, so it stays).

BEGIN;

DO $$
DECLARE placed int;
BEGIN
    SELECT count(*) INTO placed
      FROM village_object
     WHERE asset_id = '019e5f00-c401-7a10-9e00-000000637001';

    IF placed > 0 THEN
        RAISE EXCEPTION
            'LLM-637 down: % ranch fence segment(s) still in the village. Remove them first (engine stopped), then re-run.',
            placed;
    END IF;
END $$;

DELETE FROM asset_state_tag
 WHERE state_id IN (
    SELECT id FROM asset_state
     WHERE asset_id = '019e5f00-c401-7a10-9e00-000000637001');

DELETE FROM asset_state
 WHERE asset_id = '019e5f00-c401-7a10-9e00-000000637001';

DELETE FROM asset
 WHERE id = '019e5f00-c401-7a10-9e00-000000637001';

DELETE FROM tileset_pack
 WHERE id = 'mana-seed-fences'
   AND name = 'Fences & Walls'
   AND url = 'https://seliel-the-shaper.itch.io/mana-seed'
   AND NOT EXISTS (SELECT 1 FROM asset WHERE pack_id = 'mana-seed-fences');

COMMIT;
