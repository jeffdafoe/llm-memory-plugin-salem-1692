-- LLM-444: imported salt as an OPTIONAL cooking booster — a structural coin sink
-- on the food economy that never gates a dish. Follow-up to LLM-442 (iron), and
-- deliberately the softer half of that pair: iron is a REQUIRED input with a local
-- emergency fallback; salt is a pure booster with NO fallback because nothing is
-- ever blocked. Food is the survival good — every dish must always cook with zero
-- salt anywhere at its normal, unboosted yield. Hard-gating any food recipe on an
-- import is off the table permanently (settled by Jeff 2026-07-16, LLM-442).
--
-- WHAT this adds:
--   1. item_kind `salt` — an import-only cooking ingredient (category `material`,
--      `portable`, exactly like `sage`, the village's existing seasoning). NO
--      local producer ever: like iron (LLM-442) and the clothing family (LLM-410
--      slice 2), its price anchor below has no actor assigned to produce it, so
--      the only sources are the factor's pack (visitor.go factorWareKinds/
--      factorSaltKind, LLM-444) and the one-time distributor seed in step 5.
--   2. A price ANCHOR for salt via item_recipe (wholesale 2 / retail 3): the
--      factor sells sacks to Josiah at wholesale, Josiah retails them to the
--      cooks. Rate columns are the CHECK-required (>0) placeholders; empty inputs
--      — nothing is consumed to "make" salt, it is imported.
--   3. Salt added as a boost_input on the three cooked dishes an NPC actually
--      produces (confirmed live via GET /umbilical/recipes 2026-07-17):
--        - stew      (John Ellis, tavernkeeper): +2 bowls per salted batch
--        - porridge  (Hannah Boggs, innkeeper):  +3 bowls per salted batch
--        - fried_meat (Hannah Boggs, innkeeper): +2 per salted batch
--      1 salt per execution, consumed at batch landing (the LLM-248 booster
--      machinery). Boosters are ELECTIVE: no salt, no stall, just the base yield.
--      Required inputs and rates are UNTOUCHED — unsalted cooking keeps its exact
--      pre-444 yields. bonus_qty is sized so salt (retail 3) clearly pays for
--      itself when the dish sells (stew +2×5=+10, porridge +3×2=+6, fried_meat
--      +2×3=+6 against the 3-coin sack) — the cook is economically PULLED toward
--      salt, never pushed. All prices/yields tunable via the operator catalog.
--   4. Each cook gains a hand-authored `buy salt` restock entry (cap 6). Boost
--      inputs are deliberately NOT derived into buy demand (derived_demand.go,
--      LLM-260), so without this entry the cook would never be cued to restock
--      salt AND the vendor-gated "## Restocking" / "## Keeping up production"
--      salt lines would never render. Cap 6 ≈ a full production cycle's worth.
--   5. A one-time salt seed (8 sacks) at the distributor (Josiah Thorne) so the
--      loop is demonstrable across both kitchens before the first factor visit.
--
-- ENGINE-OWNED reference tables (item_kind / item_recipe) read at boot + rebuilt
-- on SIGHUP; actor_attribute and actor_inventory are CHECKPOINT-WRITTEN. deploy.sh
-- does stop -> migrate -> start, so all of this applies engine-STOPPED and the
-- post-boot checkpoints preserve it.
--
-- Rerun-safe: catalog upserts are ON CONFLICT DO UPDATE (corrective — re-asserts
-- the canonical def at deploy time, same posture as LLM-410/LLM-442); each dish's
-- boost is a corrective SET (these dishes carry no other booster, so replacing the
-- array converges on exactly the salt boost); the restock appends are @>-guarded
-- and strip-then-append; the inventory seed is ON CONFLICT DO NOTHING (engine-owned
-- live stock after go-live, never overwrite). Loud validation at the end.

BEGIN;

-- 1. The salt item kind. `portable` — the cook buys a sack and it rides in the
--    pack as a take-home purchase (not eat-here). Category `material`: a cooking
--    input, the same shelf as sage/flour, not a consumable dish nor a tool.
INSERT INTO item_kind
    (name, display_label, display_label_singular, display_label_plural,
     category, sort_order, capabilities, description)
VALUES
    ('salt', 'salt', 'sack of salt', 'sacks of salt',
     'material', 410, '{portable}'::text[],
     'Coarse bay salt in a canvas sack, up from the Boston docks — a pinch turns a plain pot into a good one.')
ON CONFLICT (name) DO UPDATE SET
    display_label          = EXCLUDED.display_label,
    display_label_singular = EXCLUDED.display_label_singular,
    display_label_plural   = EXCLUDED.display_label_plural,
    category               = EXCLUDED.category,
    sort_order             = EXCLUDED.sort_order,
    capabilities           = EXCLUDED.capabilities,
    description            = EXCLUDED.description;

-- 2. Salt's price anchor: inert recipe (no producer, empty inputs), a price
--    carrier only. Wholesale 2 (factor -> Josiah), retail 3 (Josiah -> cooks):
--    at 1 sack : +2..+3 servings that is a clear margin against a 2..5 coin
--    serving, and the coins Josiah pays the factor to restock leave the map.
INSERT INTO item_recipe
    (output_item, output_qty, rate_qty, rate_per_hours, inputs,
     wholesale_price, retail_price)
VALUES
    ('salt', 1, 1, 1, '[]'::jsonb, 2, 3)
ON CONFLICT (output_item) DO UPDATE SET
    output_qty      = EXCLUDED.output_qty,
    rate_qty        = EXCLUDED.rate_qty,
    rate_per_hours  = EXCLUDED.rate_per_hours,
    inputs          = EXCLUDED.inputs,
    wholesale_price = EXCLUDED.wholesale_price,
    retail_price    = EXCLUDED.retail_price,
    updated_at      = now();

-- 3. Salt boost on the three cooked dishes. Corrective SET (not upsert): the
--    dish recipes predate this migration on any seeded DB; on the schema-only
--    replay harness there is no row and each of these is a clean 0-row no-op
--    (the validation below asserts the boost only where the row exists). ONLY
--    boost_inputs is written — inputs, rate_qty, rate_per_hours, output_qty are
--    left exactly as they are, so unsalted yields never change.
UPDATE item_recipe
   SET boost_inputs = '[{"item": "salt", "qty": 1, "bonus_qty": 2}]'::jsonb,
       updated_at   = now()
 WHERE output_item = 'stew';

UPDATE item_recipe
   SET boost_inputs = '[{"item": "salt", "qty": 1, "bonus_qty": 3}]'::jsonb,
       updated_at   = now()
 WHERE output_item = 'porridge';

UPDATE item_recipe
   SET boost_inputs = '[{"item": "salt", "qty": 1, "bonus_qty": 2}]'::jsonb,
       updated_at   = now()
 WHERE output_item = 'fried_meat';

-- 4. The cooks' hand-authored `buy salt` restock entries, on the role attribute
--    that carries their produce entries (tavernkeeper / innkeeper). CORRECTIVE
--    like LLM-442: any pre-existing salt/buy entry (a partial or hand-edited
--    deploy) is stripped and the canonical {salt, buy, max 6} appended, so a
--    rerun converges on exactly one such row. Other entries pass through the
--    filter untouched; the jsonb_typeof CASE keeps a malformed non-array restock
--    from corrupting the rebuild. 0 rows on schema-only (no actor).
UPDATE actor_attribute
   SET params = jsonb_set(
       params,
       '{restock}',
       COALESCE(
           (SELECT jsonb_agg(e)
              FROM jsonb_array_elements(
                   CASE WHEN jsonb_typeof(params->'restock') = 'array'
                        THEN params->'restock' ELSE '[]'::jsonb END) AS e
             WHERE NOT (e->>'item' = 'salt' AND e->>'source' = 'buy')),
           '[]'::jsonb
       ) || '[{"item": "salt", "source": "buy", "max": 6}]'::jsonb
   )
 WHERE actor_id = '019da6b2-7074-7b19-ab19-89b6fc3a29a1'  -- John Ellis
   AND slug = 'tavernkeeper';

UPDATE actor_attribute
   SET params = jsonb_set(
       params,
       '{restock}',
       COALESCE(
           (SELECT jsonb_agg(e)
              FROM jsonb_array_elements(
                   CASE WHEN jsonb_typeof(params->'restock') = 'array'
                        THEN params->'restock' ELSE '[]'::jsonb END) AS e
             WHERE NOT (e->>'item' = 'salt' AND e->>'source' = 'buy')),
           '[]'::jsonb
       ) || '[{"item": "salt", "source": "buy", "max": 6}]'::jsonb
   )
 WHERE actor_id = '70419d0c-3668-428c-8bd8-633993c3aa60'  -- Hannah Boggs
   AND slug = 'innkeeper';

-- 5. Seed the distributor's salt shelf (Josiah Thorne) — a ONE-TIME bootstrap so
--    both kitchens can buy salt from day one, NOT a convergent invariant. DO
--    NOTHING, never DO UPDATE: after go-live this row is engine-owned live stock.
INSERT INTO actor_inventory (actor_id, item_kind, quantity)
SELECT '019dcac2-e78a-715e-91b7-101f339b0891', 'salt', 8
 WHERE EXISTS (SELECT 1 FROM actor WHERE id = '019dcac2-e78a-715e-91b7-101f339b0891')
ON CONFLICT (actor_id, item_kind) DO NOTHING;

-- Validate loud. Catalog rows always land. The dish boosts, the cook buy entries,
-- and the distributor seed are asserted only where the underlying rows exist (a
-- schema-only harness has no recipe/actor rows and skips them).
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM item_kind WHERE name = 'salt' AND category = 'material'
                      AND 'portable' = ANY(capabilities)) THEN
        RAISE EXCEPTION 'LLM-444: salt item_kind missing or wrong shape after insert';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM item_recipe WHERE output_item = 'salt'
                      AND wholesale_price = 2 AND retail_price = 3) THEN
        RAISE EXCEPTION 'LLM-444: salt price anchor missing after insert';
    END IF;

    -- Salt must NEVER be a required input on ANY recipe (the permanent
    -- constraint: food is the survival good, never gated on an import). Pinned
    -- at the data layer so a hand-edit or later migration that promotes salt to
    -- a required input trips the deploy loudly instead of silently gating a dish.
    IF EXISTS (SELECT 1 FROM item_recipe WHERE inputs @> '[{"item": "salt"}]'::jsonb) THEN
        RAISE EXCEPTION 'LLM-444: salt appears as a REQUIRED input on a recipe — salt must only ever be a boost_input (food is never gated on an import)';
    END IF;

    -- On a SEEDED DB (actor rows present — every real deployment, as distinct
    -- from the schema-only replay harness) the three salted dishes must exist and
    -- each must carry its salt boost: a world where salt landed but no dish was
    -- boosted would ship the coin sink with nothing draining into it.
    IF EXISTS (SELECT 1 FROM actor) THEN
        IF NOT EXISTS (SELECT 1 FROM item_recipe WHERE output_item = 'stew'
                          AND boost_inputs @> '[{"item": "salt", "qty": 1, "bonus_qty": 2}]'::jsonb) THEN
            RAISE EXCEPTION 'LLM-444: stew salt boost (+2) did not apply';
        END IF;
        IF NOT EXISTS (SELECT 1 FROM item_recipe WHERE output_item = 'porridge'
                          AND boost_inputs @> '[{"item": "salt", "qty": 1, "bonus_qty": 3}]'::jsonb) THEN
            RAISE EXCEPTION 'LLM-444: porridge salt boost (+3) did not apply';
        END IF;
        IF NOT EXISTS (SELECT 1 FROM item_recipe WHERE output_item = 'fried_meat'
                          AND boost_inputs @> '[{"item": "salt", "qty": 1, "bonus_qty": 2}]'::jsonb) THEN
            RAISE EXCEPTION 'LLM-444: fried_meat salt boost (+2) did not apply';
        END IF;

        IF NOT EXISTS (SELECT 1 FROM actor WHERE id = '019da6b2-7074-7b19-ab19-89b6fc3a29a1') THEN
            RAISE EXCEPTION 'LLM-444: seeded actors but tavernkeeper John Ellis 019da6b2... is missing (stale id?)';
        END IF;
        IF NOT EXISTS (SELECT 1 FROM actor_attribute
                        WHERE actor_id = '019da6b2-7074-7b19-ab19-89b6fc3a29a1' AND slug = 'tavernkeeper'
                          AND params->'restock' @> '[{"item": "salt", "source": "buy", "max": 6}]'::jsonb) THEN
            RAISE EXCEPTION 'LLM-444: John Ellis'' tavernkeeper buy-salt restock entry did not land as the canonical {salt, buy, max 6}';
        END IF;
        IF NOT EXISTS (SELECT 1 FROM actor WHERE id = '70419d0c-3668-428c-8bd8-633993c3aa60') THEN
            RAISE EXCEPTION 'LLM-444: seeded actors but innkeeper Hannah Boggs 70419d0c... is missing (stale id?)';
        END IF;
        IF NOT EXISTS (SELECT 1 FROM actor_attribute
                        WHERE actor_id = '70419d0c-3668-428c-8bd8-633993c3aa60' AND slug = 'innkeeper'
                          AND params->'restock' @> '[{"item": "salt", "source": "buy", "max": 6}]'::jsonb) THEN
            RAISE EXCEPTION 'LLM-444: Hannah Boggs'' innkeeper buy-salt restock entry did not land as the canonical {salt, buy, max 6}';
        END IF;
        IF NOT EXISTS (SELECT 1 FROM actor WHERE id = '019dcac2-e78a-715e-91b7-101f339b0891') THEN
            RAISE EXCEPTION 'LLM-444: seeded actors but distributor Josiah 019dcac2... is missing (stale id?)';
        END IF;
        IF NOT EXISTS (SELECT 1 FROM actor_inventory
                        WHERE actor_id = '019dcac2-e78a-715e-91b7-101f339b0891' AND item_kind = 'salt') THEN
            RAISE EXCEPTION 'LLM-444: distributor salt seed missing (expected a salt holding after seed)';
        END IF;
    END IF;
END $$;

COMMIT;
