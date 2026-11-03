-- LLM-846: a horn lantern and tallow candles at the Store; a lit lantern on the
-- actor row.
--
-- WHY. Jeff (10-22, specced 10-29): a player lights a lantern and a light
-- follows them at night. The candle burns down; putting the lantern out keeps
-- the rest of the candle for next time.
--
-- WHAT THIS DOES (the LLM-799 writing-goods shape):
--   1. item_kind `horn_lantern` (capability `light`, sim.CapabilityLight) and
--      `tallow_candle` (the fuel, sim.TallowCandleKind), category `gear` with
--      the tent.
--   2. Price anchors via inert item_recipe rows: lantern 3 / 6, candle 1 / 2.
--   3. The distributor (Josiah Thorne) gets a buy line for each, and a
--      one-time starter shelf (3 lanterns, 12 candles) so players can buy
--      before the first factor brings more.
--   4. actor.lantern_lit_until (NULL = not lit) and
--      actor.lantern_candle_left_seconds (the stub kept by Put out), written by
--      the checkpoint.
--
-- Checkpoint-written tables throughout; the deploy runs migrations with the
-- engine stopped.

BEGIN;

CREATE TEMP TABLE llm846_goods (name text, label text, singular text, plural text, sort_order int, caps text[], wholesale int, retail int, shelf int, line_max int, descr text) ON COMMIT DROP;
INSERT INTO llm846_goods VALUES
    ('horn_lantern', 'Horn lantern', 'horn lantern', 'horn lanterns', 401, '{portable,light}', 3, 6, 3, 3,
     'A tin lantern with panes of scraped horn, a ring to carry it by, and a socket for a candle.'),
    ('tallow_candle', 'Tallow candle', 'tallow candle', 'tallow candles', 402, '{portable}', 1, 2, 12, 20,
     'A plain candle of rendered tallow, good for some three hours in a lantern.');

-- ON CONFLICT DO UPDATE is corrective, as in LLM-710 / LLM-799: it promotes an
-- engine-minted discovery row of the same name to the real good.
INSERT INTO item_kind
    (name, display_label, display_label_singular, display_label_plural,
     category, sort_order, capabilities, description)
SELECT name, label, singular, plural, 'gear', sort_order, caps, descr
  FROM llm846_goods
ON CONFLICT (name) DO UPDATE SET
    display_label          = EXCLUDED.display_label,
    display_label_singular = EXCLUDED.display_label_singular,
    display_label_plural   = EXCLUDED.display_label_plural,
    category               = EXCLUDED.category,
    sort_order             = EXCLUDED.sort_order,
    capabilities           = EXCLUDED.capabilities,
    description            = EXCLUDED.description;

INSERT INTO item_recipe
    (output_item, output_qty, rate_qty, rate_per_hours, inputs,
     wholesale_price, retail_price)
SELECT name, 1, 1, 1, '[]'::jsonb, wholesale, retail
  FROM llm846_goods
ON CONFLICT (output_item) DO UPDATE SET
    output_qty      = EXCLUDED.output_qty,
    rate_qty        = EXCLUDED.rate_qty,
    rate_per_hours  = EXCLUDED.rate_per_hours,
    inputs          = EXCLUDED.inputs,
    wholesale_price = EXCLUDED.wholesale_price,
    retail_price    = EXCLUDED.retail_price,
    updated_at      = now();

-- The distributor's buy lines and starter shelf, resolved from the distributor
-- tag as LLM-710 / LLM-799.
DO $$
DECLARE
    distributor_id uuid;
    missing text;
BEGIN
    SELECT vo.owner_actor_id::uuid INTO distributor_id
      FROM village_object vo
     WHERE 'distributor' = ANY(vo.tags)
       AND vo.owner_actor_id IS NOT NULL
     LIMIT 1;
    IF distributor_id IS NULL THEN
        RETURN;  -- fresh schema-only database
    END IF;
    IF NOT EXISTS (SELECT 1 FROM actor_attribute
                    WHERE actor_id = distributor_id AND slug = 'merchant') THEN
        RAISE EXCEPTION 'LLM-846: the distributor has no merchant actor_attribute row to hold the lantern lines';
    END IF;
    UPDATE actor_attribute aa
       SET params = jsonb_set(
               aa.params,
               '{restock}',
               COALESCE(aa.params->'restock', '[]'::jsonb) || (
                   SELECT COALESCE(jsonb_agg(
                              jsonb_build_object('item', g.name, 'source', 'buy', 'max', g.line_max)
                              ORDER BY g.sort_order), '[]'::jsonb)
                     FROM llm846_goods g
                    WHERE NOT (COALESCE(aa.params->'restock', '[]'::jsonb)
                               @> jsonb_build_array(jsonb_build_object('item', g.name)))))
     WHERE aa.actor_id = distributor_id
       AND aa.slug = 'merchant';
    SELECT string_agg(g.name, ', ' ORDER BY g.name) INTO missing
      FROM llm846_goods g
     WHERE NOT EXISTS (
           SELECT 1 FROM actor_attribute aa
            WHERE aa.actor_id = distributor_id
              AND aa.slug = 'merchant'
              AND aa.params->'restock' @> jsonb_build_array(jsonb_build_object('item', g.name)));
    IF missing IS NOT NULL THEN
        RAISE EXCEPTION 'LLM-846: distributor lantern lines still missing after merge: %', missing;
    END IF;
    -- One-time shelf, not a convergent invariant: DO NOTHING never overwrites
    -- live, checkpoint-owned stock.
    INSERT INTO actor_inventory (actor_id, item_kind, quantity)
    SELECT distributor_id, g.name, g.shelf
      FROM llm846_goods g
    ON CONFLICT (actor_id, item_kind) DO NOTHING;
END $$;

ALTER TABLE actor ADD COLUMN IF NOT EXISTS lantern_lit_until timestamptz NULL;
ALTER TABLE actor ADD COLUMN IF NOT EXISTS lantern_candle_left_seconds integer NOT NULL DEFAULT 0;

DO $$
BEGIN
    IF (SELECT count(*) FROM item_kind
         WHERE (name = 'horn_lantern' AND 'light' = ANY(capabilities))
            OR name = 'tallow_candle') <> 2 THEN
        RAISE EXCEPTION 'LLM-846: horn_lantern (with light) and tallow_candle are not both in the catalog';
    END IF;
    IF (SELECT count(*) FROM item_recipe
         WHERE (output_item = 'horn_lantern' AND wholesale_price = 3 AND retail_price = 6)
            OR (output_item = 'tallow_candle' AND wholesale_price = 1 AND retail_price = 2)) <> 2 THEN
        RAISE EXCEPTION 'LLM-846: lantern and candle price anchors are missing or wrong';
    END IF;
END $$;

COMMIT;
