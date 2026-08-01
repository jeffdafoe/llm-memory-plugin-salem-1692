-- LLM-589: a working shirt lasted three quarters of one working day.
--
-- LLM-422 gave garments a wear budget in worked MINUTES: a per-minute sweep
-- draws GarmentWearPerMinute off the in-use unit of every garment a working
-- actor holds, and at 0 the unit is spent. The budgets on item_kind.wear_minutes
-- were authored as 360-600, and setting.garment_wear_per_minute has never been
-- written, so the rate falls back to DefaultGarmentWearPerMinute = 1 — one
-- minute of budget per worked minute. Against a 600-720 minute working day a
-- shift (360) is spent before the day is out.
--
-- That is the defect LLM-330 was written to fix for tools, quoted from its own
-- header: "absurd for a single-serving recipe: fried_meat at output_qty 1 would
-- have worn a whole skillet per meal." Clothing has the skillet problem.
--
-- It matters beyond the absurdity because it makes the clothing economy
-- unsupplyable. Ten working NPCs at the old budgets consume roughly ten
-- garments a day; the village holds eleven, all of them Josiah's sale stock,
-- and the only source is a factor who visits every two to three weeks. Turning
-- on clothing DEMAND (the rest of LLM-589) against these budgets would empty
-- the shelf in a day and produce a shortage rather than a market.
--
-- The rate cannot be tuned instead: GarmentWearPerMinute is a plain int, so
-- there is no value below 1:1. Slowing wear means raising the budgets, which is
-- this migration.
--
-- x30 across the board, preserving the authored ordering (shift wears fastest,
-- outerwear slowest). At a ~600-minute working day that lands garment lifetimes
-- at roughly:
--
--     shift     10800 -> ~18 working days
--     breeches  14400 -> ~24
--     gown      14400 -> ~24
--     cloak     18000 -> ~30
--     coat      18000 -> ~30
--
-- Turnover every two and a half to four weeks of work: often enough to be the
-- recurring market LLM-422 exists to create, rare enough to read as clothing
-- rather than as consumables. These are a starting calibration, not a derived
-- constant — re-tuning later is this same UPDATE.
--
-- Safe against in-flight wear. Actor.GarmentWear counts DOWN from the budget,
-- so every existing entry is already at or below the new value and simply has
-- further to fall; the LLM-422 clamp handles the opposite case (a budget tuned
-- down under a live entry) at next use. In practice there is nothing to clamp
-- either way: no actor in the village owns a garment.
--
-- item_kind is a boot-loaded catalog the engine never writes back, so the new
-- values take effect at the deploy restart and cannot be clobbered by a
-- checkpoint.

BEGIN;

UPDATE public.item_kind
   SET wear_minutes = CASE name
                          WHEN 'shift'    THEN 10800
                          WHEN 'breeches' THEN 14400
                          WHEN 'gown'     THEN 14400
                          WHEN 'cloak'    THEN 18000
                          WHEN 'coat'     THEN 18000
                      END
 WHERE name IN ('shift', 'breeches', 'gown', 'cloak', 'coat');

COMMIT;
