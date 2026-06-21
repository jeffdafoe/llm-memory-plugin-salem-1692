-- Revert LLM-24: restore the strict amount < 0 constraint on object_refresh.
--
-- Any yield-only (amount = 0) rows must be removed first or the ADD CONSTRAINT
-- will fail. LLM-24 ships the mechanism only — farm-row seeding is deferred to
-- LLM-50 — so on a clean rollback no such rows exist; if a later slice has
-- seeded them, delete or convert those rows before applying this down.
BEGIN;

ALTER TABLE object_refresh DROP CONSTRAINT object_refresh_amount_negative;
ALTER TABLE object_refresh ADD CONSTRAINT object_refresh_amount_negative
    CHECK ((amount < 0));

COMMIT;
