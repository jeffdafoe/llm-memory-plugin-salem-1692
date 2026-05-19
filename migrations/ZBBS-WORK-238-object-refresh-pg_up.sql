-- ZBBS-WORK-238: object_refresh pg-impl + v2 schema extension.
--
-- Slice 10 of the engine rewrite. Closes the deferred carry-forward
-- from Slice 9 (ZBBS-WORK-237). Extends object_refresh to hold v2's
-- runtime fields and the generation-marker pattern's snapshot_gen
-- column. Refreshes is a child of village_object — co-managed by
-- VillageObjectsRepo under the same SaveSnapshot Tx, sharing the
-- parent's advisory lock.
--
-- Companion design / pattern references:
--   shared/notes/codebase/salem-engine-v2/pg-snapshot-pattern
--   shared/notes/codebase/salem-engine-v2/village-objects-pg (Refreshes section)
--
-- Changes:
--
--   1. snapshot_gen column + sequence + index (gen-marker pattern).
--      Owns its own sequence (object_refresh_snapshot_gen_seq) —
--      independent tier counter from the parent's
--      village_object_snapshot_gen_seq. Shares the parent's advisory
--      lock (acquired by VillageObjectsRepo.SaveSnapshot at the start
--      of the same Tx), so no separate lock for this table.
--
--   2. v2-only fields. All NULL-default so v1 rows naturally read as
--      "infinite supply, no regen, no dwell" — matches v1's behavior
--      (v1 didn't have stock, regen, or dwell; arrivals always
--      decremented the actor's need).
--        - max_quantity         INTEGER NULL  — paired with available_quantity
--        - available_quantity   INTEGER NULL  — runtime stock (NULL = infinite)
--        - refresh_mode         VARCHAR(16) NULL — 'continuous' | 'periodic' | NULL
--        - refresh_period_hours INTEGER NULL  — regen period (NULL when infinite)
--        - last_refresh_at      TIMESTAMPTZ NULL — regen anchor
--        - dwell_delta          INTEGER NULL  — per-tick dwell credit amount
--        - dwell_period_minutes INTEGER NULL  — dwell credit period
--
--   3. CHECK constraints defending invariants v2 enforces in Go
--      (sim.ObjectRefresh.IsFinite / HasDwell paired fields). Substrate
--      Commands are public-callable, so the contract is guarded at the
--      schema layer too — a buggy writer can't silently install a
--      half-configured refresh row.
--        - finite_pair:                available_quantity ↔ max_quantity
--        - finite_regen:               finite rows must have mode + period_hours > 0
--        - regen_only_when_finite:     infinite rows must have NULL mode + period_hours + last_refresh_at
--        - supply_bounds:              finite rows: max > 0 AND 0 <= available <= max
--        - dwell_pair:                 dwell_delta ↔ dwell_period_minutes
--        - dwell_delta_negative:       dwell_delta < 0 when set (decrement)
--        - dwell_period_positive:      dwell_period_minutes > 0 when set
--
-- Restart-loss flagged by Slice 9 closes with this slice. Per-instance
-- refresh state (available_quantity decrements, last_refresh_at anchor
-- advances) now survives engine restart.
--
-- Rollback caveat: rolling back drops v2 runtime state. Finite-supply
-- rows lose their AvailableQuantity / MaxQuantity / regen config and
-- on next engine load behave as infinite (v1 default). Dwell config
-- is dropped (v1 didn't have it). Operator drains supply by accepting
-- this on rollback or pre-rolls catalog state to match.

BEGIN;

-- Gen-marker pattern column + sequence + index.
CREATE SEQUENCE object_refresh_snapshot_gen_seq START 1;
ALTER TABLE object_refresh
    ADD COLUMN snapshot_gen BIGINT NOT NULL DEFAULT 0;
CREATE INDEX idx_object_refresh_snapshot_gen ON object_refresh(snapshot_gen);

-- v2-only fields. NULL default for the optional pointer-shaped columns
-- so v1 rows naturally read as "infinite, no regen, no dwell".
ALTER TABLE object_refresh
    ADD COLUMN max_quantity         INTEGER NULL,
    ADD COLUMN available_quantity   INTEGER NULL,
    ADD COLUMN refresh_mode         VARCHAR(16) NULL,
    ADD COLUMN refresh_period_hours INTEGER NULL,
    ADD COLUMN last_refresh_at      TIMESTAMPTZ NULL,
    ADD COLUMN dwell_delta          INTEGER NULL,
    ADD COLUMN dwell_period_minutes INTEGER NULL;

-- Defense-in-depth CHECK constraints. v2 enforces these as Go
-- invariants (sim.ObjectRefresh.IsFinite / HasDwell); substrate
-- Commands are public-callable, so guard the contract at the schema
-- level too.

-- AvailableQuantity ↔ MaxQuantity. v2's IsFinite() check pairs them.
ALTER TABLE object_refresh
    ADD CONSTRAINT object_refresh_finite_pair
    CHECK ((available_quantity IS NULL) = (max_quantity IS NULL));

-- Finite rows must carry a regen mode + positive period. The Go-side
-- regen ticker (regenObjectRefresh) skips rows with nil/0 period
-- already, but rejecting at the schema means a misconfigured row
-- can't even land.
ALTER TABLE object_refresh
    ADD CONSTRAINT object_refresh_finite_regen
    CHECK (
        available_quantity IS NULL
        OR (refresh_mode IN ('continuous', 'periodic')
            AND refresh_period_hours IS NOT NULL
            AND refresh_period_hours > 0)
    );

-- Infinite rows carry NO regen config at all — mode, period_hours,
-- and last_refresh_at must all be NULL. A narrower mode-only gate
-- would allow an infinite row with period_hours/last_refresh_at set
-- to bypass the rest of the CHECK surface (code_review R1 catch).
ALTER TABLE object_refresh
    ADD CONSTRAINT object_refresh_regen_only_when_finite
    CHECK (
        available_quantity IS NOT NULL
        OR (refresh_mode IS NULL
            AND refresh_period_hours IS NULL
            AND last_refresh_at IS NULL)
    );

-- Supply bounds on finite rows. max_quantity must be positive (the
-- continuous regen step divides by it — engine/sim/object_refresh.go
-- regenObjectRefresh); available_quantity must lie in [0, max].
-- Infinite rows are unconstrained (both NULL via finite_pair).
ALTER TABLE object_refresh
    ADD CONSTRAINT object_refresh_supply_bounds
    CHECK (
        available_quantity IS NULL
        OR (available_quantity >= 0
            AND max_quantity > 0
            AND available_quantity <= max_quantity)
    );

-- Dwell config is paired (both NULL or both set). v2's HasDwell()
-- check pairs them.
ALTER TABLE object_refresh
    ADD CONSTRAINT object_refresh_dwell_pair
    CHECK ((dwell_delta IS NULL) = (dwell_period_minutes IS NULL));

-- Dwell delta is a need decrement (matches the existing `amount < 0`
-- convention on the parent column). v2 doc-comment on ObjectRefresh
-- explicitly types it as negative.
ALTER TABLE object_refresh
    ADD CONSTRAINT object_refresh_dwell_delta_negative
    CHECK (dwell_delta IS NULL OR dwell_delta < 0);

-- Dwell period must be positive — the dwell tick divides time
-- elapsed by it.
ALTER TABLE object_refresh
    ADD CONSTRAINT object_refresh_dwell_period_positive
    CHECK (dwell_period_minutes IS NULL OR dwell_period_minutes > 0);

COMMIT;
