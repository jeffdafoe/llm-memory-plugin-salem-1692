-- Revert LLM-77: drop the durable per-actor known-places substrate.
-- DROP TABLE removes the PK, the snapshot_gen index, and the actor FK with it;
-- the standalone snapshot_gen sequence is not owned by the column, so drop it
-- explicitly. Manual-rollback only (the runner never applies _down.sql).
BEGIN;

DROP TABLE IF EXISTS public.actor_known_place;
DROP SEQUENCE IF EXISTS public.actor_known_place_snapshot_gen_seq;

COMMIT;
