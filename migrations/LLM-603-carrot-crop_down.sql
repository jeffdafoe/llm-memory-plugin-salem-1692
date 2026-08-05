-- LLM-603 down: remove the crop-carrot asset and put carrots back on production.
--
-- Deliberately does NOT delete placed village_object rows for crop-carrot.
-- village_object.asset_id has NO foreign key to asset (LLM-600 finding), so
-- dropping the asset would silently orphan any placed plants — the guard below
-- REFUSES instead. Remove the plants in the editor first if a real rollback is
-- wanted.
--
-- The mana-seed-crops tileset_pack row is deliberately untouched: LLM-576
-- introduced it and its down owns the cleanup, guarded by NOT EXISTS over
-- remaining assets — the same split LLM-600/LLM-601 use for mana-seed-fishing.

BEGIN;

DO $$
DECLARE planted int;
BEGIN
    SELECT count(*) INTO planted
      FROM village_object
     WHERE asset_id = '019e5f00-c401-7a10-9e00-000000000603';
    IF planted > 0 THEN
        RAISE EXCEPTION 'LLM-603 down: % carrot plant(s) are still placed — remove them in the editor before rolling back', planted;
    END IF;
END $$;

-- asset_state_tag cascades from asset_state.
DELETE FROM asset_state WHERE asset_id = '019e5f00-c401-7a10-9e00-000000000603';
DELETE FROM asset_refresh_default WHERE asset_id = '019e5f00-c401-7a10-9e00-000000000603';
DELETE FROM asset WHERE id = '019e5f00-c401-7a10-9e00-000000000603';

-- item_recipe needs no restoring: the _up never touched it. The produce path
-- is gated on Moses's restock entry, so putting that entry back is the entire
-- rollback.
--
-- COALESCE for the same empty-array reason as the _up.
UPDATE actor_attribute
   SET params = jsonb_set(params, '{restock}', COALESCE((
           SELECT jsonb_agg(
                      CASE WHEN e->>'item' = 'carrots'
                           THEN jsonb_set(e, '{source}', '"produce"')
                           ELSE e END
                      ORDER BY ord)
             FROM jsonb_array_elements(params->'restock') WITH ORDINALITY AS t(e, ord)
       ), '[]'::jsonb))
 WHERE actor_id = '019da6ae-3376-73fc-8872-1cbb3ada1c78'  -- Moses James
   AND slug = 'farmer'
   AND jsonb_typeof(params->'restock') = 'array';

COMMIT;
