-- LLM-625: clothing repair — Hannah Boggs takes in mending at the Inn.
--
-- Garment wear (LLM-422) destroys a worn-out in-use unit on the wearer's back,
-- and clothing is import-only (LLM-410), so with the factor rare the village
-- wardrobe only burns down. The engine side of LLM-625 adds a "mending"
-- service capability: buying the mending item restores every worn garment unit
-- the buyer carries (work and warms kinds alike) and consumes the mender's
-- thread — an imported factor ware, so the factor stays load-bearing for
-- clothing but sells cheap material instead of 150-coin garment bales.
--
-- WHAT this adds (the LLM-442 iron-imports shape):
--   1. item_kind `thread` — an import-only mending input (category `material`).
--      No local producer ever: sourced only from the factor's pack
--      (visitor.go factorThreadKind) and the one-time seed in step 5.
--   2. item_kind `mending` — the service itself (category `service`,
--      capabilities {service, mending}). Sellable only by a keeper of a
--      mending-tagged structure (engine gate); no inventory backing.
--   3. Price anchors via item_recipe: thread wholesale 1 / retail 2 (factor ->
--      Josiah -> Hannah), mending wholesale 2 / retail 4. At 1 spool per mend
--      Hannah nets ~2 coins over the thread she bought — a real margin, unlike
--      the water line's zero (the retail-1 floor trap this pricing avoids). A
--      mend at 4 against a replacement at 6-15 keeps mending the worker's
--      rational first stop.
--   4. Hannah Boggs's innkeeper restock gains a hand-authored `buy thread`
--      entry (cap 6): thread is a service input, not a produce-recipe input,
--      so no derived demand ever cues her to restock it (the LLM-442 iron
--      reasoning) — and per the LLM-324/LLM-608 rule, a consumable behind a
--      cue needs its restock entry or the capability silently starves.
--   5. The Inn (019d98af..., Hannah's work structure) gains the `mending`
--      object tag — the engine's seller-eligibility anchor (sim.TagMending).
--   6. One-time thread seeds: 6 spools at the distributor (Josiah Thorne, the
--      factor's counterparty shelf) and 2 at Hannah, so mending is live from
--      deploy rather than waiting weeks for the first factor.
--
-- ENGINE-OWNED reference tables (item_kind / item_recipe / village_object) read
-- at boot; actor_attribute and actor_inventory are CHECKPOINT-WRITTEN. deploy.sh
-- does stop -> migrate -> start, so all of this applies engine-STOPPED.
--
-- Rerun-safe: catalog upserts are ON CONFLICT DO UPDATE (corrective); the
-- restock append is a corrective rewrite converging on one canonical entry; the
-- tag append is ANY-guarded; the inventory seeds are ON CONFLICT DO NOTHING
-- (engine-owned live stock after go-live). Loud validation at the end.

BEGIN;

-- 1. The thread item kind. `portable` — Hannah carries it home from the store.
INSERT INTO item_kind
    (name, display_label, display_label_singular, display_label_plural,
     category, sort_order, capabilities, description)
VALUES
    ('thread', 'thread', 'spool of thread', 'spools of thread',
     'material', 410, '{portable}'::text[],
     'Stout linen thread on a wooden spool, off the same brigs that bring the cloth — a mender''s whole trade rides on it.')
ON CONFLICT (name) DO UPDATE SET
    display_label          = EXCLUDED.display_label,
    display_label_singular = EXCLUDED.display_label_singular,
    display_label_plural   = EXCLUDED.display_label_plural,
    category               = EXCLUDED.category,
    sort_order             = EXCLUDED.sort_order,
    capabilities           = EXCLUDED.capabilities,
    description            = EXCLUDED.description;

-- 2. The mending service kind. {service, mending}: `service` skips the stock
--    gates (no inventory backing, the nights_stay posture), `mending` routes
--    delivery to the garment-repair arm (transferOrderGoods, engine LLM-625).
INSERT INTO item_kind
    (name, display_label, display_label_singular, display_label_plural,
     category, sort_order, capabilities, description)
VALUES
    ('mending', 'Mending', 'mending', 'mending',
     'service', 420, '{service,mending}'::text[],
     'Needle and thread put to worn clothes — seams resewn, elbows patched, a garment brought back good as new.')
ON CONFLICT (name) DO UPDATE SET
    display_label          = EXCLUDED.display_label,
    display_label_singular = EXCLUDED.display_label_singular,
    display_label_plural   = EXCLUDED.display_label_plural,
    category               = EXCLUDED.category,
    sort_order             = EXCLUDED.sort_order,
    capabilities           = EXCLUDED.capabilities,
    description            = EXCLUDED.description;

-- 3. Price anchors: inert recipes (no producer, empty inputs), price carriers
--    only — the LLM-442 iron shape.
INSERT INTO item_recipe
    (output_item, output_qty, rate_qty, rate_per_hours, inputs,
     wholesale_price, retail_price)
VALUES
    ('thread', 1, 1, 1, '[]'::jsonb, 1, 2),
    ('mending', 1, 1, 1, '[]'::jsonb, 2, 4)
ON CONFLICT (output_item) DO UPDATE SET
    output_qty      = EXCLUDED.output_qty,
    rate_qty        = EXCLUDED.rate_qty,
    rate_per_hours  = EXCLUDED.rate_per_hours,
    inputs          = EXCLUDED.inputs,
    wholesale_price = EXCLUDED.wholesale_price,
    retail_price    = EXCLUDED.retail_price,
    updated_at      = now();

-- 4. Hannah Boggs's hand-authored `buy thread` entry on the innkeeper
--    attribute's restock (the union policy home of her produce/buy entries).
--    Corrective rewrite: any pre-existing thread/buy entry is stripped and the
--    canonical one appended, so a rerun converges on exactly one
--    {thread, buy, max 6} row. 0 rows on schema-only (no actor).
UPDATE actor_attribute
   SET params = jsonb_set(
       params,
       '{restock}',
       COALESCE(
           (SELECT jsonb_agg(e)
              FROM jsonb_array_elements(
                   CASE WHEN jsonb_typeof(params->'restock') = 'array'
                        THEN params->'restock' ELSE '[]'::jsonb END) AS e
             WHERE NOT (e->>'item' = 'thread' AND e->>'source' = 'buy')),
           '[]'::jsonb
       ) || '[{"item": "thread", "source": "buy", "max": 6}]'::jsonb
   )
 WHERE actor_id = '70419d0c-3668-428c-8bd8-633993c3aa60'  -- Hannah Boggs
   AND slug = 'innkeeper';

-- 5. The Inn gains the `mending` object tag — sim.TagMending, the engine's
--    seller-eligibility anchor (resolved from where the seller WORKS, the
--    TagDistributor posture). Pinned by id per the conversion rule; guarded so
--    a rerun appends nothing twice. 0 rows on schema-only (no object).
UPDATE village_object
   SET tags = tags || '{mending}'::text[]
 WHERE id = '019d98af-ac9b-7833-8e03-5a7015bb5b0c'  -- Inn (Hannah's work structure)
   AND NOT ('mending' = ANY(tags));

-- 6. One-time thread seeds — bootstrap liveness, NOT a convergent invariant
--    (DO NOTHING, never DO UPDATE: engine-owned live stock after go-live).
--    Josiah's shelf so the supply leg exists before the first factor lands;
--    Hannah's spools so a threadbare villager can mend from day one.
INSERT INTO actor_inventory (actor_id, item_kind, quantity)
SELECT '019dcac2-e78a-715e-91b7-101f339b0891', 'thread', 6
 WHERE EXISTS (SELECT 1 FROM actor WHERE id = '019dcac2-e78a-715e-91b7-101f339b0891')
ON CONFLICT (actor_id, item_kind) DO NOTHING;

INSERT INTO actor_inventory (actor_id, item_kind, quantity)
SELECT '70419d0c-3668-428c-8bd8-633993c3aa60', 'thread', 2
 WHERE EXISTS (SELECT 1 FROM actor WHERE id = '70419d0c-3668-428c-8bd8-633993c3aa60')
ON CONFLICT (actor_id, item_kind) DO NOTHING;

-- Validate loud. Catalog rows always land; the actor-facing steps are asserted
-- only where the underlying rows exist (a schema-only harness skips them).
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM item_kind WHERE name = 'thread' AND category = 'material'
                      AND 'portable' = ANY(capabilities)) THEN
        RAISE EXCEPTION 'LLM-625: thread item_kind missing or wrong shape after insert';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM item_kind WHERE name = 'mending' AND category = 'service'
                      AND 'service' = ANY(capabilities) AND 'mending' = ANY(capabilities)) THEN
        RAISE EXCEPTION 'LLM-625: mending item_kind missing or wrong capability shape after insert';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM item_recipe WHERE output_item = 'thread'
                      AND wholesale_price = 1 AND retail_price = 2) THEN
        RAISE EXCEPTION 'LLM-625: thread price anchor missing after insert';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM item_recipe WHERE output_item = 'mending'
                      AND wholesale_price = 2 AND retail_price = 4) THEN
        RAISE EXCEPTION 'LLM-625: mending price anchor missing after insert';
    END IF;

    IF EXISTS (SELECT 1 FROM actor) THEN
        IF NOT EXISTS (SELECT 1 FROM actor WHERE id = '70419d0c-3668-428c-8bd8-633993c3aa60') THEN
            RAISE EXCEPTION 'LLM-625: seeded actors but innkeeper Hannah 70419d0c... is missing (stale id?)';
        END IF;
        IF NOT EXISTS (SELECT 1 FROM actor_attribute
                        WHERE actor_id = '70419d0c-3668-428c-8bd8-633993c3aa60' AND slug = 'innkeeper'
                          AND params->'restock' @> '[{"item": "thread", "source": "buy", "max": 6}]'::jsonb) THEN
            RAISE EXCEPTION 'LLM-625: Hannah''s innkeeper buy-thread restock entry did not land as the canonical {thread, buy, max 6}';
        END IF;
        IF NOT EXISTS (SELECT 1 FROM village_object
                        WHERE id = '019d98af-ac9b-7833-8e03-5a7015bb5b0c'
                          AND 'mending' = ANY(tags)) THEN
            RAISE EXCEPTION 'LLM-625: the Inn 019d98af... is missing the mending tag (stale id?)';
        END IF;
        IF NOT EXISTS (SELECT 1 FROM actor_inventory
                        WHERE actor_id = '019dcac2-e78a-715e-91b7-101f339b0891' AND item_kind = 'thread') THEN
            RAISE EXCEPTION 'LLM-625: distributor thread seed missing (expected a thread holding after seed)';
        END IF;
        IF NOT EXISTS (SELECT 1 FROM actor_inventory
                        WHERE actor_id = '70419d0c-3668-428c-8bd8-633993c3aa60' AND item_kind = 'thread') THEN
            RAISE EXCEPTION 'LLM-625: Hannah''s thread seed missing (expected a thread holding after seed)';
        END IF;
    END IF;
END $$;

COMMIT;
