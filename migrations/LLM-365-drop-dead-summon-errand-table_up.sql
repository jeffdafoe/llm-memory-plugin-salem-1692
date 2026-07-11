-- LLM-365: drop the dead v1 `summon_errand` table.
--
-- The v2 summon feature (engine/sim/summon.go, ZBBS-HOME-311, restored in
-- LLM-323) is an in-memory state machine: the errand lives in
-- World.SummonErrands (a Go map), with NO DB table and NO migration —
-- restart-loss is accepted by design. The `summon_errand` table, its four
-- indexes, and its five foreign keys are v1 residue that no Go/SQL/JS code reads
-- or writes (the only remaining mention is a prose comment in summon.go).
--
-- DROP ... CASCADE removes the table together with its primary key, its indexes,
-- and its own outbound foreign keys. Nothing in the schema references
-- summon_errand, so CASCADE has nothing else to drop. IF EXISTS keeps this
-- idempotent — a fresh DB built from the post-365 schema.sql (which no longer
-- defines the table) applies this as a no-op.

BEGIN;

DROP TABLE IF EXISTS public.summon_errand CASCADE;

COMMIT;
