-- LLM-601 down: remove the three added school-of-fish colourways.
--
-- REFUSES rather than deleting placed schools, same contract as LLM-600. There
-- is NO foreign key from village_object.asset_id to asset.id -- the only FK on
-- village_object is attached_to -- so nothing at the database level forces the
-- removal and nothing warns about it either. A placed school is editor work.
-- Clear the placements first, then run this; the exception names the count.
--
-- If you delete placements by hand, note that village_object carries
-- snapshot_gen and is checkpoint-written: the engine must be STOPPED or the
-- shutdown checkpoint resurrects the rows. A deploy already stops it.
--
-- THE PACK ROW IS DELIBERATELY NOT TOUCHED HERE. LLM-600's two assets still
-- reference mana-seed-fishing, so removing it would orphan them. LLM-600's own
-- down owns that cleanup and already guards it with NOT EXISTS -- a guard that
-- was theoretical when written and is load-bearing now that a second migration
-- shares the pack.
--
-- Deletes are by id alone: asset.name is editable from the village editor, so
-- guarding on name would make this silently no-op after a rename.

BEGIN;

DO $$
DECLARE placed int;
BEGIN
    SELECT count(*) INTO placed
      FROM village_object
     WHERE asset_id IN (
        '019e5f00-c401-7a10-9e00-000000601001',
        '019e5f00-c401-7a10-9e00-000000601002',
        '019e5f00-c401-7a10-9e00-000000601003');

    IF placed > 0 THEN
        RAISE EXCEPTION
            'LLM-601 down: % school-of-fish placement(s) still in the village for the added colourways. Remove them first (engine stopped), then re-run.',
            placed;
    END IF;
END $$;

DELETE FROM asset_state
 WHERE asset_id IN (
    '019e5f00-c401-7a10-9e00-000000601001',
    '019e5f00-c401-7a10-9e00-000000601002',
    '019e5f00-c401-7a10-9e00-000000601003');

DELETE FROM asset
 WHERE id IN (
    '019e5f00-c401-7a10-9e00-000000601001',
    '019e5f00-c401-7a10-9e00-000000601002',
    '019e5f00-c401-7a10-9e00-000000601003');

COMMIT;
