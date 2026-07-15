-- LLM-410 (slice 2): the clothing + charm goods family, an item_kind.description
-- flavor column, and a small seed clothing stock at the distributor.
--
-- WHAT this adds:
--   1. item_kind.description — a nullable free-text flavor column (a general win:
--      what a good IS in-world, distinct from its counting labels). Surfaced when
--      a good is laid out/examined (the factor's wares, slice 3); the whole
--      pre-410 catalog leaves it NULL.
--   2. Eight import-only goods: garments (breeches, shift, coat, gown, cloak) and
--      charms (whalebone_charm, silver_locket, iron_ward). coat + cloak carry the
--      `warms` capability — the cold sweep's outdoor relief (coldRatePerMinuteX100,
--      LLM-410). Charms ship as flavored trade goods only; their mechanic is LLM-423.
--   3. A price ANCHOR for each via item_recipe.wholesale_price / retail_price — the
--      catalog price book every commerce surface reads (scene_quote / trade_value).
--      These goods have NO local producer: production is per-actor (RestockPolicy
--      ProduceEntries), and no actor is assigned to make clothing, so the recipe is
--      inert — a price carrier only, never produced. output_qty / rate_qty /
--      rate_per_hours are the CHECK-required (>0) placeholders they must carry; the
--      empty inputs make the point that nothing is consumed to "make" a coat (it is
--      imported). This preserves the import-only design: the factor (slice 3) is the
--      only source once the seed below is sold through.
--   4. A small seed clothing stock in the distributor's (Josiah Thorne's) inventory
--      so the demand loop is demonstrable from the FIRST storm — a cold outdoor
--      worker can find a coat to buy before the factor channel is even built.
--      Vendorship is structural (a keeper holding qty>0 at a workplace IS a vendor),
--      so seeding the stock is all it takes to light up the "buy a coat" nudge.
--
-- ENGINE-OWNED reference tables (item_kind / item_recipe) read at boot + rebuilt on
-- SIGHUP; actor_inventory is CHECKPOINT-WRITTEN. deploy.sh does stop -> migrate ->
-- start, so all of this applies engine-STOPPED: the description column exists before
-- LoadAll's SELECT runs, and the inventory seed can't race a checkpoint. On start the
-- engine loads the seeded rows into live state; later checkpoints then preserve them.
--
-- Rerun-safe: ADD COLUMN IF NOT EXISTS; every INSERT is ON CONFLICT DO NOTHING (a
-- re-run never clobbers an operator's later item/set edit, nor resurrects seed stock
-- Josiah has since sold). The inventory seed is guarded on Josiah existing so it is a
-- clean no-op on the schema-only migration-replay harness (no seed rows there). A
-- loud validation block at the end fails the deploy if the catalog rows didn't land,
-- and — on a seeded actor DB — if the distributor seed didn't apply (stale UUID).

BEGIN;

-- 1. The flavor column. Nullable free text; the loader maps NULL <-> "" like the
--    label columns, so the whole existing catalog is unaffected.
ALTER TABLE item_kind ADD COLUMN IF NOT EXISTS description text;

-- 2. The goods. display_label is the title-case catalog label; the article-less
--    singular/plural counting phrases feed prose + cues (LLM-113); category is a
--    soft-typed bucket (`clothing` / `charm`, both new — the type is open). coat +
--    cloak carry the `warms` capability; everything else takes the '{}' default.
--    sort_order clusters clothing in the 200s and charms in the 300s, after the
--    food/craft catalog. ON CONFLICT (name) DO UPDATE — CORRECTIVE for these owned
--    canonical goods: if a name already exists as an engine-minted discovery row
--    (ZBBS-WORK-412 — e.g. an NPC once referenced "coat", minting an inert
--    category='unknown' row with no capabilities/labels), this promotes it to the
--    proper clothing def (warms, price, description all land). A rerun INTENTIONALLY
--    re-asserts this canonical definition — technically it would overwrite a later
--    operator item/set edit, but migrations are applied once at deploy, so that
--    reassertion only ever runs at deploy time.
INSERT INTO item_kind
    (name, display_label, display_label_singular, display_label_plural,
     category, sort_order, capabilities, description)
VALUES
    ('breeches', 'Breeches', 'pair of breeches', 'pairs of breeches',
     'clothing', 215, '{}'::text[],
     'Woolen breeches, cut to the knee for a working man.'),
    ('shift', 'Shift', 'shift', 'shifts',
     'clothing', 220, '{}'::text[],
     'A plain linen shift, worn next to the skin.'),
    ('coat', 'Coat', 'coat', 'coats',
     'clothing', 200, '{warms}'::text[],
     'A heavy wool coat, long against the wind and the rain.'),
    ('gown', 'Gown', 'gown', 'gowns',
     'clothing', 210, '{}'::text[],
     'A woman''s gown of good cloth, for the meeting-house and the cold.'),
    ('cloak', 'Cloak', 'cloak', 'cloaks',
     'clothing', 205, '{warms}'::text[],
     'A thick woolen cloak that throws off the rain and holds the warmth in.'),
    ('whalebone_charm', 'Whalebone charm', 'whalebone charm', 'whalebone charms',
     'charm', 310, '{}'::text[],
     'A charm cut from whalebone, said to keep a body from drowning.'),
    ('silver_locket', 'Silver locket', 'silver locket', 'silver lockets',
     'charm', 320, '{}'::text[],
     'A small silver locket on a chain, for a keepsake or a lock of hair.'),
    ('iron_ward', 'Iron ward', 'iron ward', 'iron wards',
     'charm', 330, '{}'::text[],
     'A twist of cold iron worn against the skin, to turn away ill wishes.')
ON CONFLICT (name) DO UPDATE SET
    display_label          = EXCLUDED.display_label,
    display_label_singular = EXCLUDED.display_label_singular,
    display_label_plural   = EXCLUDED.display_label_plural,
    category               = EXCLUDED.category,
    sort_order             = EXCLUDED.sort_order,
    capabilities           = EXCLUDED.capabilities,
    description            = EXCLUDED.description;

-- 3. Price anchors. One recipe per good carrying wholesale/retail; inert (no
--    producer, empty inputs) — a price carrier, not a production path. The rate
--    columns are the CHECK-required placeholders. FK output_item -> item_kind(name)
--    is satisfied by the rows just inserted. ON CONFLICT (output_item) DO UPDATE —
--    CORRECTIVE like the item_kind upsert above: re-asserts the canonical price for
--    an owned good rather than leaving a stale/absent recipe in place. A rerun
--    intentionally re-asserts the canonical price — it would overwrite a later
--    operator recipe/set edit, but migrations apply once at deploy.
INSERT INTO item_recipe
    (output_item, output_qty, rate_qty, rate_per_hours, inputs,
     wholesale_price, retail_price)
VALUES
    ('breeches', 1, 1, 1, '[]'::jsonb, 4, 7),
    ('shift', 1, 1, 1, '[]'::jsonb, 3, 6),
    ('coat', 1, 1, 1, '[]'::jsonb, 9, 15),
    ('gown', 1, 1, 1, '[]'::jsonb, 8, 14),
    ('cloak', 1, 1, 1, '[]'::jsonb, 7, 12),
    ('whalebone_charm', 1, 1, 1, '[]'::jsonb, 2, 5),
    ('silver_locket', 1, 1, 1, '[]'::jsonb, 4, 9),
    ('iron_ward', 1, 1, 1, '[]'::jsonb, 2, 5)
ON CONFLICT (output_item) DO UPDATE SET
    output_qty      = EXCLUDED.output_qty,
    rate_qty        = EXCLUDED.rate_qty,
    rate_per_hours  = EXCLUDED.rate_per_hours,
    inputs          = EXCLUDED.inputs,
    wholesale_price = EXCLUDED.wholesale_price,
    retail_price    = EXCLUDED.retail_price,
    updated_at      = now();

-- 4. Seed the distributor's clothing stock (Josiah Thorne, 019dcac2...) — a ONE-TIME
--    bootstrap so the loop is live from the first storm, NOT a convergent invariant.
--    Guarded on Josiah existing so it is a no-op on the schema-only harness. The whole
--    migration is transactional (BEGIN/COMMIT), so it never lands partially. ON CONFLICT
--    (actor_id, item_kind) DO NOTHING so it doesn't DOUBLE a still-present holding —
--    deliberately NOT DO UPDATE, since after go-live this row is engine-owned live
--    stock (checkpoint-written) the seed must never overwrite. snapshot_gen takes its
--    column default (0). coat + cloak (the warms goods) lead so the cold-relief loop is
--    live at once; a gown, a pair of breeches, and a locket round out the shelf.
INSERT INTO actor_inventory (actor_id, item_kind, quantity)
SELECT '019dcac2-e78a-715e-91b7-101f339b0891', v.item_kind, v.quantity
  FROM (VALUES
        ('coat', 2),
        ('cloak', 2),
        ('gown', 1),
        ('breeches', 1),
        ('silver_locket', 1)
  ) AS v(item_kind, quantity)
 WHERE EXISTS (SELECT 1 FROM actor WHERE id = '019dcac2-e78a-715e-91b7-101f339b0891')
ON CONFLICT (actor_id, item_kind) DO NOTHING;

-- Validate loud. The catalog rows must always land (item_kind is populated by this
-- migration regardless of seed state). The distributor seed is asserted only on a
-- seeded actor DB — a schema-only harness has no actor rows and skips it.
DO $$
DECLARE
    missing_kinds   int;
    missing_recipes int;
BEGIN
    SELECT count(*) INTO missing_kinds
      FROM (VALUES ('breeches'),('shift'),('coat'),('gown'),('cloak'),
                   ('whalebone_charm'),('silver_locket'),('iron_ward')) AS v(name)
     WHERE NOT EXISTS (SELECT 1 FROM item_kind k WHERE k.name = v.name);
    IF missing_kinds > 0 THEN
        RAISE EXCEPTION 'LLM-410: % clothing/charm item_kind row(s) missing after insert', missing_kinds;
    END IF;

    SELECT count(*) INTO missing_recipes
      FROM (VALUES ('breeches'),('shift'),('coat'),('gown'),('cloak'),
                   ('whalebone_charm'),('silver_locket'),('iron_ward')) AS v(name)
     WHERE NOT EXISTS (SELECT 1 FROM item_recipe r WHERE r.output_item = v.name AND r.retail_price IS NOT NULL);
    IF missing_recipes > 0 THEN
        RAISE EXCEPTION 'LLM-410: % clothing/charm price anchor(s) missing after insert', missing_recipes;
    END IF;

    -- warms capability landed on the two garments that carry it.
    IF NOT (SELECT 'warms' = ANY(capabilities) FROM item_kind WHERE name = 'coat') THEN
        RAISE EXCEPTION 'LLM-410: coat is missing the warms capability';
    END IF;
    IF NOT (SELECT 'warms' = ANY(capabilities) FROM item_kind WHERE name = 'cloak') THEN
        RAISE EXCEPTION 'LLM-410: cloak is missing the warms capability';
    END IF;

    IF EXISTS (SELECT 1 FROM actor) THEN
        IF NOT EXISTS (SELECT 1 FROM actor WHERE id = '019dcac2-e78a-715e-91b7-101f339b0891') THEN
            RAISE EXCEPTION 'LLM-410: seeded actors but distributor Josiah 019dcac2... is missing (stale id?)';
        END IF;
        -- Assert the FULL seed shape, not just the coat. The seed's ON CONFLICT
        -- (actor_id, item_kind) DO NOTHING conflicts per-row, so any MISSING seeded
        -- kind is still inserted (only a still-present holding is skipped — that is
        -- how a live-traded quantity is preserved rather than reset). So after this
        -- migration all five seeded kinds must be present; fail loud otherwise,
        -- which also catches a broken/partial pre-existing seed.
        IF (SELECT count(*) FROM actor_inventory
             WHERE actor_id = '019dcac2-e78a-715e-91b7-101f339b0891'
               AND item_kind IN ('coat', 'cloak', 'gown', 'breeches', 'silver_locket')) <> 5 THEN
            RAISE EXCEPTION 'LLM-410: distributor clothing seed incomplete (expected all 5 seeded kinds present)';
        END IF;
    END IF;
END $$;

COMMIT;
