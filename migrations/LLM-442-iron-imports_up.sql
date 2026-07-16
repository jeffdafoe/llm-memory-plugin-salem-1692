-- LLM-442: imported iron gates nail production (structural coin sink), with the
-- inputless base recipe retained as the rough-nails emergency fallback.
--
-- WHAT this adds:
--   1. item_kind `iron` — an import-only smith's input (category `material`).
--      NO local producer ever: like the LLM-410 clothing family, its price
--      anchor below has no actor assigned to produce it, so the only sources
--      are the factor's pack (visitor.go factorWareKinds, LLM-442) and the
--      one-time distributor seed in step 4.
--   2. A price ANCHOR for iron via item_recipe (wholesale 2 / retail 3): the
--      factor sells bars to Josiah at wholesale, Josiah retails them to the
--      smith. Rate columns are the CHECK-required (>0) placeholders; empty
--      inputs — nothing is consumed to "make" iron, it is imported.
--   3. The nail recipe reshaped into the two-leg LLM-442 design:
--        - BASE (the rough-nails fallback): rate 4/hr -> 1/hr, still inputless.
--          One nail an hour from scrap — slow, thankless, but nails all the
--          same. Because the base recipe consumes nothing, nail production can
--          NEVER be starved out by the import chain: this is the liveness
--          guarantee (the absorbing-state rule), expressed as data.
--        - BOOST (the normal path): boost_inputs [{iron,1,+4}] — each executed
--          batch holding a bar consumes it and mints 4 extra nails (5/hr,
--          slightly above the old 4/hr, minus the new input cost). Boosters
--          are elective (LLM-248): no iron, no stall, just the rough rate.
--      The gate and the fallback are two legs of ONE design (LLM-442 settled
--      decisions) — do not remove either.
--   4. Ezekiel Crane's blacksmith restock gains a hand-authored `buy iron`
--      entry (cap 6). Boost inputs are deliberately NOT derived into buy
--      demand (derived_demand.go, LLM-260), so without this entry the smith
--      would never be cued to restock bars. Cap 6 ≈ 30 boosted nails ≈ most of
--      a week's live demand, reordering at the shared threshold.
--   5. A one-time iron seed (6 bars) at the distributor (Josiah Thorne) so the
--      loop is live from deploy, before the first factor visit.
--
-- ENGINE-OWNED reference tables (item_kind / item_recipe) read at boot + rebuilt
-- on SIGHUP; actor_attribute and actor_inventory are CHECKPOINT-WRITTEN. deploy.sh
-- does stop -> migrate -> start, so all of this applies engine-STOPPED and the
-- post-boot checkpoints preserve it.
--
-- Rerun-safe: catalog upserts are ON CONFLICT DO UPDATE (corrective — re-asserts
-- the canonical def at deploy time, same posture as LLM-410); the restock append
-- is @>-guarded; the inventory seed is ON CONFLICT DO NOTHING (engine-owned live
-- stock after go-live, never overwrite). Loud validation at the end.

BEGIN;

-- 1. The iron item kind. `portable` — it must ride home in the smith's pack
--    (take-home purchase, not eat-here). New soft category `material`: a
--    production input, not a tool the smith works WITH nor a consumable.
INSERT INTO item_kind
    (name, display_label, display_label_singular, display_label_plural,
     category, sort_order, capabilities, description)
VALUES
    ('iron', 'bar iron', 'bar of iron', 'bars of iron',
     'material', 400, '{portable}'::text[],
     'A bar of good Boston iron, off the last brig from the coast — the smith''s craft starts here.')
ON CONFLICT (name) DO UPDATE SET
    display_label          = EXCLUDED.display_label,
    display_label_singular = EXCLUDED.display_label_singular,
    display_label_plural   = EXCLUDED.display_label_plural,
    category               = EXCLUDED.category,
    sort_order             = EXCLUDED.sort_order,
    capabilities           = EXCLUDED.capabilities,
    description            = EXCLUDED.description;

-- 2. Iron's price anchor: inert recipe (no producer, empty inputs), a price
--    carrier only. Wholesale 2 (factor -> Josiah), retail 3 (Josiah -> smith):
--    at 1 bar : +4 nails that is 0.6 coin of input per boosted nail against a
--    1..2 coin sale — the smith is economically pulled toward bar iron, and
--    roughly 16 coins/week leave the village with the factor at live volume.
INSERT INTO item_recipe
    (output_item, output_qty, rate_qty, rate_per_hours, inputs,
     wholesale_price, retail_price)
VALUES
    ('iron', 1, 1, 1, '[]'::jsonb, 2, 3)
ON CONFLICT (output_item) DO UPDATE SET
    output_qty      = EXCLUDED.output_qty,
    rate_qty        = EXCLUDED.rate_qty,
    rate_per_hours  = EXCLUDED.rate_per_hours,
    inputs          = EXCLUDED.inputs,
    wholesale_price = EXCLUDED.wholesale_price,
    retail_price    = EXCLUDED.retail_price,
    updated_at      = now();

-- 3. Reshape the nail recipe: base rate 4/hr -> 1/hr (the rough-nails leg),
--    iron boost +4/batch (the normal leg). Corrective UPDATE (not upsert): the
--    nail recipe predates this migration on any seeded DB; on the schema-only
--    replay harness there is no row and this is a clean 0-row no-op (the
--    validation below only asserts the reshape where the row exists).
UPDATE item_recipe
   SET rate_qty     = 1,
       boost_inputs = '[{"item": "iron", "qty": 1, "bonus_qty": 4}]'::jsonb,
       updated_at   = now()
 WHERE output_item = 'nail';

-- 4. Ezekiel Crane's hand-authored `buy iron` entry on the blacksmith
--    attribute's restock (the union policy home of his produce entries).
--    CORRECTIVE like the catalog upserts: any pre-existing iron/buy entry
--    (a partial or hand-edited deploy) is stripped and the canonical entry
--    appended, so a rerun converges on exactly one {iron, buy, max 6} row
--    rather than merely tolerating whatever is there. His other entries pass
--    through the filter untouched. The jsonb_typeof CASE keeps a malformed
--    non-array restock from corrupting the rebuild. 0 rows on schema-only
--    (no actor).
UPDATE actor_attribute
   SET params = jsonb_set(
       params,
       '{restock}',
       COALESCE(
           (SELECT jsonb_agg(e)
              FROM jsonb_array_elements(
                   CASE WHEN jsonb_typeof(params->'restock') = 'array'
                        THEN params->'restock' ELSE '[]'::jsonb END) AS e
             WHERE NOT (e->>'item' = 'iron' AND e->>'source' = 'buy')),
           '[]'::jsonb
       ) || '[{"item": "iron", "source": "buy", "max": 6}]'::jsonb
   )
 WHERE actor_id = '019da6f9-1b4c-7dda-bb6b-3248cdafb2c4'  -- Ezekiel Crane
   AND slug = 'blacksmith';

-- 5. Seed the distributor's iron shelf (Josiah Thorne) — a ONE-TIME bootstrap so
--    the smith can buy bars from day one, NOT a convergent invariant. DO NOTHING,
--    never DO UPDATE: after go-live this row is engine-owned live stock.
INSERT INTO actor_inventory (actor_id, item_kind, quantity)
SELECT '019dcac2-e78a-715e-91b7-101f339b0891', 'iron', 6
 WHERE EXISTS (SELECT 1 FROM actor WHERE id = '019dcac2-e78a-715e-91b7-101f339b0891')
ON CONFLICT (actor_id, item_kind) DO NOTHING;

-- Validate loud. Catalog rows always land; the nail reshape, the smith's buy
-- entry, and the distributor seed are asserted only where the underlying rows
-- exist (a schema-only harness has no recipe/actor rows and skips them).
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM item_kind WHERE name = 'iron' AND category = 'material'
                      AND 'portable' = ANY(capabilities)) THEN
        RAISE EXCEPTION 'LLM-442: iron item_kind missing or wrong shape after insert';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM item_recipe WHERE output_item = 'iron'
                      AND wholesale_price = 2 AND retail_price = 3) THEN
        RAISE EXCEPTION 'LLM-442: iron price anchor missing after insert';
    END IF;

    -- The nail reshape is asserted on any DB that HAS a nail recipe, and a
    -- SEEDED DB (actor rows present — i.e. every real deployment, as distinct
    -- from the schema-only replay harness) must additionally HAVE one: a real
    -- world where iron landed but nails were never reshaped would ship the
    -- coin sink without the thing that drains into it.
    IF EXISTS (SELECT 1 FROM item_recipe WHERE output_item = 'nail') THEN
        IF NOT EXISTS (SELECT 1 FROM item_recipe
                        WHERE output_item = 'nail' AND rate_qty = 1
                          AND boost_inputs @> '[{"item": "iron", "qty": 1, "bonus_qty": 4}]'::jsonb) THEN
            RAISE EXCEPTION 'LLM-442: nail recipe reshape (rough base + iron boost) did not apply';
        END IF;
    ELSIF EXISTS (SELECT 1 FROM actor) THEN
        RAISE EXCEPTION 'LLM-442: seeded DB has no nail recipe to reshape — catalog state unexpected';
    END IF;

    IF EXISTS (SELECT 1 FROM actor) THEN
        IF NOT EXISTS (SELECT 1 FROM actor WHERE id = '019da6f9-1b4c-7dda-bb6b-3248cdafb2c4') THEN
            RAISE EXCEPTION 'LLM-442: seeded actors but blacksmith Ezekiel 019da6f9... is missing (stale id?)';
        END IF;
        -- Assert the exact canonical entry (max included), not mere presence —
        -- the corrective rewrite above must have converged on it.
        IF NOT EXISTS (SELECT 1 FROM actor_attribute
                        WHERE actor_id = '019da6f9-1b4c-7dda-bb6b-3248cdafb2c4' AND slug = 'blacksmith'
                          AND params->'restock' @> '[{"item": "iron", "source": "buy", "max": 6}]'::jsonb) THEN
            RAISE EXCEPTION 'LLM-442: Ezekiel''s blacksmith buy-iron restock entry did not land as the canonical {iron, buy, max 6}';
        END IF;
        IF NOT EXISTS (SELECT 1 FROM actor WHERE id = '019dcac2-e78a-715e-91b7-101f339b0891') THEN
            RAISE EXCEPTION 'LLM-442: seeded actors but distributor Josiah 019dcac2... is missing (stale id?)';
        END IF;
        IF NOT EXISTS (SELECT 1 FROM actor_inventory
                        WHERE actor_id = '019dcac2-e78a-715e-91b7-101f339b0891' AND item_kind = 'iron') THEN
            RAISE EXCEPTION 'LLM-442: distributor iron seed missing (expected an iron holding after seed)';
        END IF;
    END IF;
END $$;

COMMIT;
