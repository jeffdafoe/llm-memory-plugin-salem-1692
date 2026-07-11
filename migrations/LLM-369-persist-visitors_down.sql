-- Revert LLM-369: drop the durable in-flight visitor mirror.
-- DROP TABLE removes the PK, the format / nonempty CHECKs, and the snapshot_gen
-- index with it; the standalone snapshot_gen sequence is not owned by the column,
-- so drop it explicitly. Manual-rollback only (the runner never applies _down.sql).
-- Losing the table reverts to the pre-LLM-369 restart-lossy behavior (in-flight
-- visitors drop on restart) — data-clean, since visitors are transient by design.
BEGIN;

DROP TABLE IF EXISTS public.visitor;
DROP SEQUENCE IF EXISTS public.visitor_snapshot_gen_seq;

COMMIT;
