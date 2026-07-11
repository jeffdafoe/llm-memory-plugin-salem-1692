-- LLM-365: drop the dead v1 `summon_errand` table.
--
-- The v2 summon feature (engine/sim/summon.go, ZBBS-HOME-311, restored in
-- LLM-323) is an in-memory state machine: the errand lives in
-- World.SummonErrands (a Go map), with NO DB table and NO migration —
-- restart-loss is accepted by design. The `summon_errand` table, its four
-- indexes, and its five foreign keys are v1 residue that no Go/SQL/JS code reads
-- or writes (the only remaining mention is a prose comment in summon.go).
--
-- A plain DROP TABLE removes the table with its own primary key, indexes, check
-- constraints, and outbound foreign keys. No CASCADE on purpose: nothing depends
-- on summon_errand, so CASCADE would buy nothing — and omitting it makes the
-- migration fail loudly if some unexpected inbound dependency ever appears,
-- rather than silently dropping it (code_review, LLM-365). IF EXISTS keeps this
-- idempotent — a fresh DB built from the post-365 schema.sql (which no longer
-- defines the table) applies this as a no-op.

BEGIN;

DROP TABLE IF EXISTS public.summon_errand;

COMMIT;
