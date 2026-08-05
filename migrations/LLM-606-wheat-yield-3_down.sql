-- LLM-606 down: wheat yield back to 2 grains per plant.
--
-- Restores the pre-migration LIVE tuning (2 per plant), which is the point of
-- a rollback against production. On a rebuilt-from-migrations database the
-- pre-LLM-606 template was the LLM-576 seed (3/120), so this down lands such
-- a DB on 2 rather than 3 — accepted: the down exists to unwind prod, and the
-- template value is a tuning, not a shape invariant.
--
-- available_quantity is clamped to the new max on placed rows so no plant is
-- left holding more stock than its maximum.
--
-- Same engine-stopped requirement as the up (object_refresh is
-- checkpoint-written).

BEGIN;

DO $$
DECLARE template_rows int; placed_rows int;
BEGIN
    -- Mirror of the up's discipline: exactly one template row at the state
    -- the up produced (periodic, max 3), else refuse — a down that silently
    -- does nothing reads as a successful rollback (code_review).
    UPDATE asset_refresh_default
       SET available_quantity = 2, max_quantity = 2
     WHERE asset_id = '019e5f00-c401-7a10-9e00-000000000576'
       AND gather_item = 'wheat'
       AND refresh_mode = 'periodic'
       AND max_quantity = 3;
    GET DIAGNOSTICS template_rows = ROW_COUNT;
    IF template_rows <> 1 THEN
        RAISE EXCEPTION 'LLM-606 down: expected exactly one crop-wheat template at periodic/max 3, updated % — refusing a rollback of an unexpected state', template_rows;
    END IF;

    UPDATE object_refresh r
       SET max_quantity = 2,
           available_quantity = LEAST(r.available_quantity, 2)
      FROM village_object vo
     WHERE vo.id = r.object_id
       AND vo.asset_id = '019e5f00-c401-7a10-9e00-000000000576'
       AND r.gather_item = 'wheat'
       AND r.max_quantity = 3;
    GET DIAGNOSTICS placed_rows = ROW_COUNT;
    -- All placed wheat rows must land on max 2 (IS DISTINCT FROM catches
    -- NULLs a plain <> would pass). 0 placed rows is fine — a fresh DB has
    -- no placements.
    IF EXISTS (
        SELECT 1 FROM object_refresh r
          JOIN village_object vo ON vo.id = r.object_id
         WHERE vo.asset_id = '019e5f00-c401-7a10-9e00-000000000576'
           AND r.gather_item = 'wheat'
           AND r.max_quantity IS DISTINCT FROM 2
    ) THEN
        RAISE EXCEPTION 'LLM-606 down: some placed wheat rows are not at max 2 after the rollback (% updated)', placed_rows;
    END IF;
    RAISE NOTICE 'LLM-606 down: template restored, % placed plant(s) back to max 2', placed_rows;
END $$;

COMMIT;
