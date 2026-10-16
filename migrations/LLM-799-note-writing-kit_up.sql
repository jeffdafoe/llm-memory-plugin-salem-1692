-- LLM-799: a door note (LLM-19) needs a quill and ink and a sheet of parchment.
--
-- WHY. Jeff (10-16): a player may leave a note only with both, each note uses
-- a sheet, and both are sold at the Store.
--
-- WHAT THIS DOES (the LLM-710 wardrobe-goods shape):
--   1. item_kind `quill_and_ink` — held, not spent: the engine checks for one
--      and never takes it (sim.NoteQuillKind).
--   2. item_kind `parchment` — one sheet goes with each note pinned
--      (sim.NoteParchmentKind). The factor's pack carries more each visit.
--   3. Price anchors via inert item_recipe rows: quill and ink 3 / 6,
--      parchment 1 / 2.
--   4. The distributor (Josiah Thorne) gets a buy line for each, and a
--      one-time starter shelf (3 quill and ink, 12 parchment) so players can
--      write before the first factor brings more.
--
-- No capabilities: neither eases a need, so a buy is always take-home.
-- Checkpoint-written tables throughout; the deploy runs migrations with the
-- engine stopped.

BEGIN;

CREATE TEMP TABLE llm799_goods (name text, label text, singular text, plural text, sort_order int, wholesale int, retail int, shelf int, line_max int, descr text) ON COMMIT DROP;
INSERT INTO llm799_goods VALUES
    ('quill_and_ink', 'Quill and ink', 'quill and ink', 'quills and ink', 300, 3, 6, 3, 3,
     'A goose quill cut to a nib and a stoppered pot of ink, enough for many letters.'),
    ('parchment', 'Parchment', 'sheet of parchment', 'sheets of parchment', 301, 1, 2, 12, 20,
     'A sheet of pale parchment, smooth on one side, for a letter or a note.');

-- ON CONFLICT DO UPDATE is corrective, as in LLM-710: it promotes an
-- engine-minted discovery row of the same name to the real good.
INSERT INTO item_kind
    (name, display_label, display_label_singular, display_label_plural,
     category, sort_order, capabilities, description)
SELECT name, label, singular, plural, 'writing', sort_order, '{}'::text[], descr
  FROM llm799_goods
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
  FROM llm799_goods
ON CONFLICT (output_item) DO UPDATE SET
    output_qty      = EXCLUDED.output_qty,
    rate_qty        = EXCLUDED.rate_qty,
    rate_per_hours  = EXCLUDED.rate_per_hours,
    inputs          = EXCLUDED.inputs,
    wholesale_price = EXCLUDED.wholesale_price,
    retail_price    = EXCLUDED.retail_price,
    updated_at      = now();

-- The distributor's buy lines and starter shelf, resolved from the distributor
-- tag as LLM-710.
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
        RAISE EXCEPTION 'LLM-799: the distributor has no merchant actor_attribute row to hold the writing lines';
    END IF;

    UPDATE actor_attribute aa
       SET params = jsonb_set(
               aa.params,
               '{restock}',
               COALESCE(aa.params->'restock', '[]'::jsonb) || (
                   SELECT COALESCE(jsonb_agg(
                              jsonb_build_object('item', g.name, 'source', 'buy', 'max', g.line_max)
                              ORDER BY g.sort_order), '[]'::jsonb)
                     FROM llm799_goods g
                    WHERE NOT (COALESCE(aa.params->'restock', '[]'::jsonb)
                               @> jsonb_build_array(jsonb_build_object('item', g.name)))))
     WHERE aa.actor_id = distributor_id
       AND aa.slug = 'merchant';

    SELECT string_agg(g.name, ', ' ORDER BY g.name) INTO missing
      FROM llm799_goods g
     WHERE NOT EXISTS (
           SELECT 1 FROM actor_attribute aa
            WHERE aa.actor_id = distributor_id
              AND aa.slug = 'merchant'
              AND aa.params->'restock' @> jsonb_build_array(jsonb_build_object('item', g.name)));
    IF missing IS NOT NULL THEN
        RAISE EXCEPTION 'LLM-799: distributor writing lines still missing after merge: %', missing;
    END IF;

    -- One-time shelf, not a convergent invariant: DO NOTHING never overwrites
    -- live, checkpoint-owned stock.
    INSERT INTO actor_inventory (actor_id, item_kind, quantity)
    SELECT distributor_id, g.name, g.shelf
      FROM llm799_goods g
    ON CONFLICT (actor_id, item_kind) DO NOTHING;
END $$;

COMMIT;
