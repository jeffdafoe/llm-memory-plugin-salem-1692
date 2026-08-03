-- LLM-589: a working shirt lasted three quarters of one working day.
--
-- LLM-422 gave garments a wear budget in worked MINUTES: a per-minute sweep
-- draws GarmentWearPerMinute off the in-use unit of every garment a working
-- actor holds, and at 0 the unit is spent. The budgets it authored on
-- item_kind.wear_minutes were 360-600, and setting.garment_wear_per_minute has
-- never been written, so the rate falls back to DefaultGarmentWearPerMinute = 1
-- — one minute of budget per worked minute. Against a 600-720 minute working
-- day a shift (360) is spent before the day is out.
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
-- WHAT THIS DOES NOT DO: it does not extend a garment already in use. The wear
-- counter is remaining minutes, not elapsed, and applyGarmentWear only adopts
-- the catalog budget when the entry is absent/zero or ABOVE it
-- (engine/sim/garment_wear.go:167-170) — so an actor holding a unit with 100
-- minutes left still has 100 minutes left after this runs, and only the NEXT
-- unit taken up gets the longer life. The reverse direction is the one the code
-- guards: a budget retuned below a live counter clamps at next use. In practice
-- neither case arises here — no actor in the village owns a garment.
--
-- item_kind is a boot-loaded catalog the engine never writes back, so the new
-- values take effect at the deploy restart and cannot be clobbered by a
-- checkpoint.

BEGIN;

-- The kind list is carried once, as a VALUES relation joined on name. A CASE
-- over an IN list would state it twice and let the two drift; wear_minutes is
-- NOT NULL, so a name reached by the WHERE but missed by the CASE would abort
-- the migration rather than write a NULL — noisy, but still a trap worth not
-- setting.
UPDATE public.item_kind AS k
   SET wear_minutes = v.wear_minutes
  FROM (VALUES
            ('shift',    10800),
            ('breeches', 14400),
            ('gown',     14400),
            ('cloak',    18000),
            ('coat',     18000)
       ) AS v(name, wear_minutes)
 WHERE k.name = v.name;

-- Assert the catalog actually landed where intended. An UPDATE that matches no
-- rows is a success in Postgres, so without this a partially-populated (or
-- entirely unpopulated) item_kind would pass silently and the engine would boot
-- on the old budgets. The five rows are seeded by LLM-410-clothing-goods_up.sql,
-- so any environment that has replayed the migrations in order has them.
-- Mirrors the same guard LLM-422-clothing-wear_up.sql puts after its own budget
-- update.
DO $$
DECLARE
    wrong INTEGER;
BEGIN
    SELECT count(*) INTO wrong
      FROM (VALUES
                ('shift',    10800),
                ('breeches', 14400),
                ('gown',     14400),
                ('cloak',    18000),
                ('coat',     18000)
           ) AS v(name, wear_minutes)
      LEFT JOIN public.item_kind k ON k.name = v.name
     WHERE k.name IS NULL
        OR k.wear_minutes <> v.wear_minutes;

    IF wrong > 0 THEN
        RAISE EXCEPTION 'LLM-589: % garment kind(s) missing from item_kind or not at the intended wear budget after update', wrong;
    END IF;
END $$;

COMMIT;
