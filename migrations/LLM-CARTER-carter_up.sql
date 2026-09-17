-- The carter — the inside-supply visitor (engine sim/carter.go).
--
-- Goods are the village's currency: the distributor pays in stock, so coin never
-- reaches producers and the goods he hands over land where nobody uses them
-- (residue). The carter is an out-of-towner with a bounded purse who buys residue
-- from its holders for coin and sells it to keepers who hold a line for it, and
-- carries the rest away. He comes on a cooldown counted in days.
--
-- The cooldown anchor is the only new durable state. It must survive restart:
-- the village restarts several times a day for deploys, so an in-memory stamp
-- would send a carter every restart. It rides world_state beside the shortage
-- record; NULL means no carter has come yet.
--
-- The four knobs (carter_days 3, carter_residue_floor_coins 4,
-- carter_residue_spawn_coins 40, carter_purse_max 100) are registry settings
-- with compiled defaults; they need no setting rows here and are live-tunable
-- through the umbilical (/settings/set), persisting on the next checkpoint.
--
-- ENGINE-OWNED table; deploy.sh runs stop -> migrate -> start, so this applies
-- engine-STOPPED. Rerun-safe: ADD COLUMN IF NOT EXISTS. Loud validation at the
-- end.

BEGIN;

ALTER TABLE world_state ADD COLUMN IF NOT EXISTS last_carter_at timestamp with time zone;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns
                    WHERE table_name = 'world_state' AND column_name = 'last_carter_at') THEN
        RAISE EXCEPTION 'carter: world_state.last_carter_at missing after ALTER';
    END IF;
END $$;

COMMIT;
