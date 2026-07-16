-- LLM-422: clothing wear — garments worn by working actors degrade and
-- eventually need replacing, turning the LLM-410 clothing goods from a
-- durable-forever one-time equip into a RECURRING market (the factor keeps a
-- reason to come). Built parallel to the LLM-330 per-use tool-durability
-- substrate, but as its own worked-MINUTE axis (a garment's life is measured in
-- time worn under labor, not produce executions).
--
-- WHAT this adds:
--   1. item_kind.wear_minutes: kind-level wear budget. > 0 marks a garment that
--      wears out; the value is how many WORKED MINUTES one unit lasts (the
--      engine's garment-wear sweep decrements the in-use unit while its bearer
--      is in a working posture). 0 (the default) = a good that never wears —
--      the whole pre-422 catalog, and the charms (their mechanic is LLM-423).
--      Parallel to durability_uses; tunable per kind via the umbilical item/set
--      route.
--   2. actor_inventory.worn_minutes_left: worked-minutes remaining on the
--      actor's in-use unit of a garment kind (engine Actor.GarmentWear). NULL
--      for a garment no work has yet worn (fresh) and for every non-garment.
--      Rides the existing inventory checkpoint row, exactly like uses_left, so
--      the wear dies with the stock that carries it. Durable ON PURPOSE: the
--      village redeploys many times a day, and boot-resetting every garment to
--      fresh would mean clothing never wears out — no recurring demand, the
--      whole point of the ticket.
--   3. Budgets on the five garment kinds (category `clothing`, LLM-410). Warm
--      wool goods (coat/cloak) are sturdier and last longest; the linen shift
--      wears fastest. These are a first calibration to watch in play (the
--      stall-wear-threshold posture) — an operator retunes any of them live via
--      item/set without a deploy. Charms are left at 0 (they don't wear).
--
-- Rerun-safe: ADD COLUMN IF NOT EXISTS on both columns; the budget UPDATEs are
-- idempotent and tolerate the schema-only harness (0 rows matched is fine — the
-- garment kinds are seeded by LLM-410, absent on a bare schema). A loud
-- validation block asserts the budgets landed ONLY when the garment rows exist,
-- so a schema-only replay passes and a real catalog that failed to update fails
-- the deploy.
--
-- item_kind is an ENGINE-OWNED reference table (read at boot, rebuilt on SIGHUP);
-- actor_inventory is CHECKPOINT-WRITTEN. deploy.sh does stop -> migrate -> start,
-- so both columns exist before the engine's next LoadAll/checkpoint.

BEGIN;

-- 1. The garment wear budget on the catalog. Parallel to durability_uses.
ALTER TABLE item_kind ADD COLUMN IF NOT EXISTS wear_minutes INTEGER NOT NULL DEFAULT 0;

-- 2. The in-use unit's remaining worked-minutes on the inventory row. NULL =
--    fresh / non-garment; a set value must be a live counter (the engine deletes
--    wear entries at zero), so guard the invariant against out-of-band writes —
--    the exact shape of actor_inventory_uses_left_positive (LLM-330).
ALTER TABLE actor_inventory ADD COLUMN IF NOT EXISTS worn_minutes_left INTEGER
    CONSTRAINT actor_inventory_worn_minutes_left_positive CHECK (worn_minutes_left IS NULL OR worn_minutes_left > 0);

-- 3. Budgets (worked-minutes). Sturdier warm goods last longer; linen wears fast.
UPDATE item_kind SET wear_minutes = 600 WHERE name = 'coat';
UPDATE item_kind SET wear_minutes = 600 WHERE name = 'cloak';
UPDATE item_kind SET wear_minutes = 480 WHERE name = 'gown';
UPDATE item_kind SET wear_minutes = 480 WHERE name = 'breeches';
UPDATE item_kind SET wear_minutes = 360 WHERE name = 'shift';

-- Validate loud — but only when the garment catalog is present (LLM-410 applied).
-- A schema-only harness has no item_kind rows and skips the assertion; a real DB
-- whose garment rows exist must have taken every budget.
DO $$
DECLARE
    unset int;
BEGIN
    IF EXISTS (SELECT 1 FROM item_kind WHERE name IN
              ('coat','cloak','gown','breeches','shift')) THEN
        SELECT count(*) INTO unset
          FROM item_kind
         WHERE name IN ('coat','cloak','gown','breeches','shift')
           AND wear_minutes = 0;
        IF unset > 0 THEN
            RAISE EXCEPTION 'LLM-422: % garment kind(s) still have wear_minutes = 0 after budget update', unset;
        END IF;
    END IF;
END $$;

COMMIT;
