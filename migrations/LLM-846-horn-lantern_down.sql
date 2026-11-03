-- LLM-846 down: remove horn_lantern and tallow_candle — the distributor's buy
-- lines, every holding, the price anchors and the kinds — and the two lantern
-- columns on actor.
--
-- Deleting an item_kind fails loud if a pay_ledger row (or another FK) still
-- names one of these goods; that history must be dealt with by hand.

BEGIN;

DO $$
DECLARE
    goods text[] := ARRAY['horn_lantern', 'tallow_candle'];
BEGIN
    UPDATE actor_attribute aa
       SET params = jsonb_set(aa.params, '{restock}',
               COALESCE((SELECT jsonb_agg(entry ORDER BY ord)
                           FROM jsonb_array_elements(aa.params->'restock') WITH ORDINALITY AS t(entry, ord)
                          WHERE NOT (entry->>'item' = ANY(goods))), '[]'::jsonb))
     WHERE jsonb_typeof(aa.params->'restock') = 'array'
       AND EXISTS (SELECT 1 FROM jsonb_array_elements(aa.params->'restock') e WHERE e->>'item' = ANY(goods));
    DELETE FROM actor_inventory WHERE item_kind = ANY(goods);
    DELETE FROM item_recipe WHERE output_item = ANY(goods);
    DELETE FROM item_kind WHERE name = ANY(goods);
END $$;

ALTER TABLE actor DROP COLUMN IF EXISTS lantern_candle_left_seconds;
ALTER TABLE actor DROP COLUMN IF EXISTS lantern_lit_until;

COMMIT;
