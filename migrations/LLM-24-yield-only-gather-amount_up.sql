-- LLM-24: allow a yield-only (forage-to-sell) object_refresh row.
--
-- A gatherable source (gather_item set) was forced to ALSO be a need-bearing
-- consume-in-place source: object_refresh_amount_negative required amount < 0,
-- so every berry bush an NPC foraged also dropped her hunger on arrival and
-- stamped an eat-to-recover dwell. forage-to-SELL needs a "pure-material
-- gatherable" — a row that yields an item into inventory with no need drop and
-- no dwell, so a vendor harvesting to stock isn't sated at the bush.
--
-- The engine already treats an amount = 0 row as yield-only (arrival skips it;
-- Gather and the regen tick are amount-agnostic). This relaxes the CHECK so
-- such a row is legal, but ONLY when it is a gather source — a zero-amount row
-- that is not gatherable is still a misconfiguration and stays rejected. The
-- row's amount is the mode readout: amount < 0 = eat+pick, amount = 0 (with
-- gather_item) = forage-to-sell.
--
-- Engine-owned table, but a CHECK swap is checkpoint-safe (the running binary
-- writes only amount < 0 rows, which the new constraint still admits), so this
-- applies cleanly via the normal deploy path.
--
-- The zero-amount branch mirrors Go's trim-aware ObjectRefresh.IsGatherable()
-- (gather_item with surrounding whitespace stripped must be non-empty), so the
-- DB never admits a yield-only row the engine would treat as non-gatherable —
-- NULLIF(btrim(...), '') rejects '' and whitespace-only gather_item alongside
-- NULL. btrim/NULLIF are immutable, so this is valid in a CHECK.
BEGIN;

ALTER TABLE object_refresh DROP CONSTRAINT object_refresh_amount_negative;
ALTER TABLE object_refresh ADD CONSTRAINT object_refresh_amount_negative
    CHECK ((amount < 0) OR (amount = 0 AND NULLIF(btrim(gather_item), '') IS NOT NULL));

COMMIT;
