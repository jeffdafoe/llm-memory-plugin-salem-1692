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
-- Mirror of the _up's discipline: assert the shape before mutating, scope the
-- flip to item AND source, assert the outcome. This down restores only the
-- exact entry the up transformed (carrots/forage -> carrots/produce); any
-- other shape — duplicates, an already-produce entry, a missing entry — means
-- state this migration did not create, so refuse rather than generalize.
DO $$
DECLARE carrot_forage int; carrot_produce int; carrot_total int;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM actor WHERE id = '019da6ae-3376-73fc-8872-1cbb3ada1c78') THEN
        RETURN;
    END IF;
    SELECT count(*),
           count(*) FILTER (WHERE e->>'source' = 'forage')
      INTO carrot_total, carrot_forage
      FROM actor_attribute, jsonb_array_elements(params->'restock') e
     WHERE actor_id = '019da6ae-3376-73fc-8872-1cbb3ada1c78' AND slug = 'farmer'
       AND e->>'item' = 'carrots';
    IF carrot_total <> 1 OR carrot_forage <> 1 THEN
        RAISE EXCEPTION 'LLM-603 down: expected exactly one carrots/forage restock entry on Moses''s farmer row, found % carrots entr(y/ies) of which % forage', carrot_total, carrot_forage;
    END IF;

    -- COALESCE for the same empty-array reason as the _up.
    UPDATE actor_attribute
       SET params = jsonb_set(params, '{restock}', COALESCE((
               SELECT jsonb_agg(
                          CASE WHEN e->>'item' = 'carrots' AND e->>'source' = 'forage'
                               THEN jsonb_set(e, '{source}', '"produce"')
                               ELSE e END
                          ORDER BY ord)
                 FROM jsonb_array_elements(params->'restock') WITH ORDINALITY AS t(e, ord)
           ), '[]'::jsonb))
     WHERE actor_id = '019da6ae-3376-73fc-8872-1cbb3ada1c78'  -- Moses James
       AND slug = 'farmer'
       AND jsonb_typeof(params->'restock') = 'array';

    SELECT count(*),
           count(*) FILTER (WHERE e->>'source' = 'produce')
      INTO carrot_total, carrot_produce
      FROM actor_attribute, jsonb_array_elements(params->'restock') e
     WHERE actor_id = '019da6ae-3376-73fc-8872-1cbb3ada1c78' AND slug = 'farmer'
       AND e->>'item' = 'carrots';
    IF carrot_total <> 1 OR carrot_produce <> 1 THEN
        RAISE EXCEPTION 'LLM-603 down: carrots restock restore did not land — % carrots entr(y/ies), % produce', carrot_total, carrot_produce;
    END IF;
END $$;

COMMIT;
