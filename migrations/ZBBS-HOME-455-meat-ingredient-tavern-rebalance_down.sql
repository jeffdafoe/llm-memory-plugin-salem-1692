-- ZBBS-HOME-455 reverse: restore meat as a directly-edible food and revert the
-- stew/cheese hunger rebalance to their pre-455 values.
--
-- meat's row is re-inserted with its original immediate amount (10) and no
-- dwell (all three dwell columns left NULL, satisfying the dwell_triple
-- constraint), matching the pre-455 live value. stew and cheese immediate
-- amounts revert to 4 and 8; their dwell credits were never touched by the up
-- migration, so nothing else is restored.

BEGIN;

-- Upsert (not a plain INSERT) so rollback is robust even if meat's hunger row
-- was already reintroduced by a partial/manual repair -- the PK is
-- (item_kind, attribute). Restores the pre-455 shape: amount 10, no dwell.
INSERT INTO item_satisfies (item_kind, attribute, amount, dwell_amount, dwell_period_minutes, dwell_total_ticks)
    VALUES ('meat', 'hunger', 10, NULL, NULL, NULL)
ON CONFLICT (item_kind, attribute) DO UPDATE
    SET amount               = EXCLUDED.amount,
        dwell_amount         = EXCLUDED.dwell_amount,
        dwell_period_minutes = EXCLUDED.dwell_period_minutes,
        dwell_total_ticks    = EXCLUDED.dwell_total_ticks;
UPDATE item_satisfies SET amount = 4 WHERE item_kind = 'stew'   AND attribute = 'hunger';
UPDATE item_satisfies SET amount = 8 WHERE item_kind = 'cheese' AND attribute = 'hunger';

COMMIT;
