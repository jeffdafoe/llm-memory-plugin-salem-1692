-- LLM-600 down: remove the two school-of-fish assets.
--
-- REFUSES rather than deleting placed schools. There is NO foreign key from
-- village_object.asset_id to asset.id -- the only FK on village_object is
-- attached_to -- so nothing at the database level stops this rollback from
-- leaving orphaned placements behind, and equally nothing forces us to remove
-- them. A placed school is editor work, not migration state. Deleting it as a
-- silent side effect of reverting a catalog row is the wrong default: the
-- operator gets no warning and the count is unrecoverable.
--
-- So the contract is: clear the placements first (from the editor, or by an
-- explicit DELETE you write knowingly), then run this. The exception names the
-- count so it is obvious what is in the way.
--
-- If you DO delete placements by hand, note that village_object carries
-- snapshot_gen and is checkpoint-written: the engine must be STOPPED or the
-- shutdown checkpoint resurrects the rows. A deploy already stops it
-- (stop -> migrate -> start); an ad-hoc run does not.
--
-- Deletes are by id alone, deliberately. asset.name is editable from the
-- village editor, so guarding on name would make this silently no-op after a
-- rename -- worse than the id-reuse risk it would protect against, since these
-- two UUIDs are reserved and no later migration may re-point them.

BEGIN;

DO $$
DECLARE placed int;
BEGIN
    SELECT count(*) INTO placed
      FROM village_object
     WHERE asset_id IN (
        '019e5f00-c401-7a10-9e00-000000600001',
        '019e5f00-c401-7a10-9e00-000000600002');

    IF placed > 0 THEN
        RAISE EXCEPTION
            'LLM-600 down: % school-of-fish placement(s) still in the village. Remove them first (engine stopped), then re-run.',
            placed;
    END IF;
END $$;

DELETE FROM asset_state
 WHERE asset_id IN (
    '019e5f00-c401-7a10-9e00-000000600001',
    '019e5f00-c401-7a10-9e00-000000600002');

DELETE FROM asset
 WHERE id IN (
    '019e5f00-c401-7a10-9e00-000000600001',
    '019e5f00-c401-7a10-9e00-000000600002');

-- Only if nothing else adopted the pack AND the row is still the one the up
-- wrote. Matching on metadata as well as id means a row someone else put there
-- is left alone rather than removed on our way out; an orphaned pack row is
-- harmless display metadata, deleting someone else's is not. The up asserts the
-- same metadata, so under normal operation this always matches.
DELETE FROM tileset_pack
 WHERE id = 'mana-seed-fishing'
   AND name = 'Fishing Gear'
   AND url = 'https://seliel-the-shaper.itch.io/mana-seed'
   AND NOT EXISTS (SELECT 1 FROM asset WHERE pack_id = 'mana-seed-fishing');

COMMIT;
