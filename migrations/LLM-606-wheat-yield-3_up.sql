-- LLM-606: wheat yield 2 -> 3 grains per plant.
--
-- The LLM-576 field was sized against a 21-CALENDAR-day sales average
-- (~8/day), but the same 165 units moved in ~12 trading days — the zero-sale
-- days diluted trading-day demand (15-32/day) by half. The produce->forage
-- flip then removed the zero days (forage refills the cap in minutes; produce
-- was rate-limited to 3/hr while at work), so realized continuous demand
-- (~17/day, measured 2026-08-01..05) sits ABOVE the field's 15/day capacity
-- and the field lives stripped. Yield is the sizing lever per the crops note
-- (grains-per-plant is the yield knob, plant count is the look knob):
-- 52 x 3 / 7d ~= 22/day ~= 1.3x realized demand.
--
-- Two writes: the template (future placements) and the placed plants' own
-- object_refresh rows. available_quantity on placed rows is DELIBERATELY
-- untouched — regrowing plants stay at 0 and jump to the new max on their
-- next periodic refresh; currently-ripe plants read 2-of-3, which still
-- renders ripe and gathers fine (stage render keys on "has stock"), and top
-- up on their next cycle. No stock is minted by the migration.
--
-- asset_refresh_default is load-only reference data. object_refresh IS
-- engine-checkpoint-written (snapshot_gen column), so the placed-rows UPDATE
-- needs the engine stopped — the deploy does stop -> migrate -> start; an
-- ad-hoc apply must stop it first.

BEGIN;

DO $$
DECLARE template_rows int; placed_rows int;
BEGIN
    -- Template first. Assert the shape we expect to be changing: the
    -- crop-wheat template at the live 2/168 tuning. On a fresh schema-only DB
    -- the LLM-576 migration has just seeded 3/120 — accept either seed, since
    -- the point of the assert is refusing an UNKNOWN shape, not pinning one
    -- history. (The 2/168 came from a post-LLM-576 live retune; a
    -- rebuilt-from-migrations DB never had it.)
    UPDATE asset_refresh_default
       SET available_quantity = 3, max_quantity = 3
     WHERE asset_id = '019e5f00-c401-7a10-9e00-000000000576'
       AND gather_item = 'wheat'
       AND max_quantity IN (2, 3);
    GET DIAGNOSTICS template_rows = ROW_COUNT;
    IF template_rows <> 1 THEN
        -- Zero updates with the asset present means an unexpected tuning is
        -- live — refuse rather than stomp it.
        IF EXISTS (SELECT 1 FROM asset WHERE id = '019e5f00-c401-7a10-9e00-000000000576') THEN
            RAISE EXCEPTION 'LLM-606: crop-wheat refresh template not found at an expected tuning (max 2 or 3) — % row(s) updated', template_rows;
        END IF;
        -- Asset absent entirely = schema-only DB where LLM-576 didn't run?
        -- Not reachable (migrations run in order), but be explicit.
        RAISE EXCEPTION 'LLM-606: crop-wheat asset is missing';
    END IF;

    -- Placed plants. 0 rows is fine (fresh DB, nothing placed); any placed
    -- wheat rows must all move together.
    UPDATE object_refresh r
       SET max_quantity = 3
      FROM village_object vo
     WHERE vo.id = r.object_id
       AND vo.asset_id = '019e5f00-c401-7a10-9e00-000000000576'
       AND r.gather_item = 'wheat'
       AND r.max_quantity = 2;
    GET DIAGNOSTICS placed_rows = ROW_COUNT;
    IF EXISTS (
        SELECT 1 FROM object_refresh r
          JOIN village_object vo ON vo.id = r.object_id
         WHERE vo.asset_id = '019e5f00-c401-7a10-9e00-000000000576'
           AND r.gather_item = 'wheat'
           AND r.max_quantity <> 3
    ) THEN
        RAISE EXCEPTION 'LLM-606: some placed wheat rows are not at max 3 after the update (% updated)', placed_rows;
    END IF;
    RAISE NOTICE 'LLM-606: template updated, % placed plant(s) moved to max 3', placed_rows;
END $$;

COMMIT;
