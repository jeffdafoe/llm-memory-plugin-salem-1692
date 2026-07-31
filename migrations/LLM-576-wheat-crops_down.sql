-- LLM-576 down: remove the crop-wheat asset and put wheat back on production.
--
-- Deliberately does NOT delete placed village_object rows for crop-wheat. If the
-- field has been planted, dropping the asset would orphan those placements — so
-- the asset delete will FAIL on them by design (see the guard below) rather than
-- silently taking the field with it. Remove the plants in the editor first if a
-- real rollback is wanted.

BEGIN;

DO $$
DECLARE planted int;
BEGIN
    SELECT count(*) INTO planted
      FROM village_object
     WHERE asset_id = '019e5f00-c401-7a10-9e00-000000000576';
    IF planted > 0 THEN
        RAISE EXCEPTION 'LLM-576 down: % wheat plant(s) are still placed — remove them in the editor before rolling back', planted;
    END IF;
END $$;

-- asset_state_tag and asset_refresh_default both cascade from their parents.
DELETE FROM asset_state WHERE asset_id = '019e5f00-c401-7a10-9e00-000000000576';
DELETE FROM asset_refresh_default WHERE asset_id = '019e5f00-c401-7a10-9e00-000000000576';
DELETE FROM asset WHERE id = '019e5f00-c401-7a10-9e00-000000000576';

-- Only if nothing else adopted the pack meanwhile.
DELETE FROM tileset_pack
 WHERE id = 'mana-seed-crops'
   AND NOT EXISTS (SELECT 1 FROM asset WHERE pack_id = 'mana-seed-crops');

-- Restore the origin-producer rate the _up zeroed (3/hr, from the 2026-07-02 seed).
UPDATE item_recipe SET rate_qty = 3, updated_at = now() WHERE output_item = 'wheat';

-- And put Moses back on produce.
UPDATE actor_attribute
   SET params = jsonb_set(params, '{restock}', (
           SELECT jsonb_agg(
                      CASE WHEN e->>'item' = 'wheat'
                           THEN jsonb_set(e, '{source}', '"produce"')
                           ELSE e END
                      ORDER BY ord)
             FROM jsonb_array_elements(params->'restock') WITH ORDINALITY AS t(e, ord)))
 WHERE actor_id = '019da6ae-3376-73fc-8872-1cbb3ada1c78'  -- Moses James
   AND slug = 'farmer'
   AND jsonb_typeof(params->'restock') = 'array';

COMMIT;
