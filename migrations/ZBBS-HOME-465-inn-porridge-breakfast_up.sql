-- ZBBS-HOME-465: porridge — the Inn's breakfast, served by Hannah Boggs.
--
-- Background: the village had no morning meal. The only "real meal" (eat-here)
-- is stew at the Tavern, but the tavernkeeper works nights and sleeps every
-- morning, so the Tavern is shut at breakfast and there is nowhere to eat in
-- the AM. ZBBS-HOME-465 makes the Inn a breakfast venue: Hannah Boggs was
-- promoted to a stateful innkeeper on a 06:00-20:00 day shift (slice 2), so the
-- Inn is open and attended through the morning. This slice gives her something
-- to serve.
--
-- Vendorship in v2 is STRUCTURAL (ZBBS-HOME-299): a keeper surfaces as a food
-- source in perception simply by holding, qty>0, an item the catalog says eases
-- a need, stationed at a resolvable workplace. So the whole change is data:
-- define porridge, and stock Hannah with it.
--
--   1. porridge item_kind — eat-here (empty capabilities, like stew; NOT
--      {portable}), category food. So it is eaten AT the Inn (a sit-down beat in
--      the common room), not carried off and grazed.
--   2. porridge item_recipe — an origin good (empty inputs[], like bread/ale/
--      water) Hannah produces at her post at 8/hour, so the morning larder stays
--      stocked. retail 2 = bread tier (a cheap, humble breakfast).
--   3. porridge item_satisfies — hunger 10 immediate + a small dwell (1 per
--      2-minute tick x 6). A proper meal that clears most of a hungry NPC's bar
--      (need scale 0..24), but lighter than stew's 12 + larger dwell — breakfast,
--      not a hearty dinner. Edibility is derived from item_satisfies
--      (ItemKindDef.Consumable() == len(Satisfies) > 0), so this row is what
--      makes porridge eat-able and surfaces it as a hunger remedy.
--   4. Hannah's innkeeper attribute carries the restock list (was empty {}),
--      mirroring John Ellis's tavernkeeper stew entry: porridge, produced,
--      capped at 60.
--   5. A one-time opening stock of 60 porridge so breakfast works from the first
--      morning; the produce tick maintains it thereafter (only while she is at
--      post — closed and not producing overnight, which is correct).
--
-- The three catalog rows use ON CONFLICT DO UPDATE so the migration is the
-- authoritative source of porridge's definition: a re-run forces the intended
-- values rather than leaving a drifted/partial row in place.
--
-- Resurrection note: item_kind / item_recipe / item_satisfies are load-only
-- reference data (read at boot, never checkpointed back), so they take effect on
-- the next engine restart and are not clobbered by the shutdown checkpoint. The
-- actor_attribute (restock policy) and actor_inventory rows ARE engine-owned and
-- ARE resurrected by a running engine's checkpoint, so this migration must be
-- applied with the engine STOPPED (stop -> migrate -> start), or it rides a
-- normal deploy whose restart loads it on a fresh boot.

BEGIN;

-- Guard: on a populated village exactly one Hannah Boggs with an innkeeper
-- attribute must exist, or the inn-stocking steps below would silently stock
-- nobody (or, on a duplicate name, error / stock the wrong actor). On a fresh DB
-- (no actors seeded yet — e.g. the integration-test template) the steps are
-- harmless no-ops and the reference rows above still apply, so the guard is
-- skipped there.
DO $$
DECLARE
    hannah_count integer;
BEGIN
    IF (SELECT count(*) FROM actor) > 0 THEN
        SELECT count(*) INTO hannah_count FROM actor WHERE display_name = 'Hannah Boggs';
        IF hannah_count <> 1 THEN
            RAISE EXCEPTION 'ZBBS-HOME-465: expected exactly one actor named Hannah Boggs, found %', hannah_count;
        END IF;
        IF NOT EXISTS (
            SELECT 1 FROM actor_attribute aa
            JOIN actor a ON a.id = aa.actor_id
            WHERE a.display_name = 'Hannah Boggs' AND aa.slug = 'innkeeper'
        ) THEN
            RAISE EXCEPTION 'ZBBS-HOME-465: Hannah Boggs has no innkeeper attribute to attach the restock policy to';
        END IF;
    END IF;
END $$;

-- 1. porridge item_kind — eat-here food (empty capabilities, like stew).
INSERT INTO item_kind (name, display_label, category, sort_order, capabilities, hours_per_unit, consume_dwell_narration)
VALUES ('porridge', 'Porridge', 'food', 115, '{}', NULL,
        'A bowl of warm porridge to break your fast; you settle in to eat.')
ON CONFLICT (name) DO UPDATE SET
    display_label           = EXCLUDED.display_label,
    category                = EXCLUDED.category,
    sort_order              = EXCLUDED.sort_order,
    capabilities            = EXCLUDED.capabilities,
    hours_per_unit          = EXCLUDED.hours_per_unit,
    consume_dwell_narration = EXCLUDED.consume_dwell_narration;

-- 2. porridge item_recipe — origin good (no inputs), 8 produced per hour, cheap.
INSERT INTO item_recipe (output_item, output_qty, rate_qty, rate_per_hours, inputs, wholesale_price, retail_price, created_at, updated_at)
VALUES ('porridge', 1, 8, 1, '[]'::jsonb, 1, 2, NOW(), NOW())
ON CONFLICT (output_item) DO UPDATE SET
    output_qty      = EXCLUDED.output_qty,
    rate_qty        = EXCLUDED.rate_qty,
    rate_per_hours  = EXCLUDED.rate_per_hours,
    inputs          = EXCLUDED.inputs,
    wholesale_price = EXCLUDED.wholesale_price,
    retail_price    = EXCLUDED.retail_price,
    updated_at      = NOW();

-- 3. porridge item_satisfies — hunger 10 immediate + small dwell (sit-and-eat).
INSERT INTO item_satisfies (item_kind, attribute, amount, dwell_amount, dwell_period_minutes, dwell_total_ticks)
VALUES ('porridge', 'hunger', 10, 1, 2, 6)
ON CONFLICT (item_kind, attribute) DO UPDATE SET
    amount               = EXCLUDED.amount,
    dwell_amount         = EXCLUDED.dwell_amount,
    dwell_period_minutes = EXCLUDED.dwell_period_minutes,
    dwell_total_ticks    = EXCLUDED.dwell_total_ticks;

-- 4. Hannah's innkeeper restock policy — produce porridge, cap 60. jsonb_set so
--    only the restock key is written (any other innkeeper params are preserved).
UPDATE actor_attribute
SET params = jsonb_set(
        COALESCE(params, '{}'::jsonb),
        '{restock}',
        '[{"max": 60, "item": "porridge", "source": "produce"}]'::jsonb,
        true)
WHERE slug = 'innkeeper'
  AND actor_id = (SELECT id FROM actor WHERE display_name = 'Hannah Boggs');

-- 5. One-time opening stock so the Inn can serve from the first morning.
--    DO NOTHING (NOT a top-up upsert) on purpose: on a re-run after the engine
--    has been live, Hannah's porridge quantity reflects real production and
--    consumption — a GREATEST(...,60) upsert would refill her larder to 60 every
--    deploy, wiping legitimate runtime depletion. The produce tick maintains
--    stock; this seed only kickstarts the very first morning.
INSERT INTO actor_inventory (actor_id, item_kind, quantity)
SELECT id, 'porridge', 60 FROM actor WHERE display_name = 'Hannah Boggs'
ON CONFLICT (actor_id, item_kind) DO NOTHING;

COMMIT;
