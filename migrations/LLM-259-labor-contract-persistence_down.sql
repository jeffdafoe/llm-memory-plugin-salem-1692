-- Revert LLM-259: drop the durable accepted-labor-contract mirror.
-- DROP TABLE removes the PK and the snapshot_gen index with it; the standalone
-- snapshot_gen sequence is not owned by the column, so drop it explicitly.
-- Manual-rollback only (the runner never applies _down.sql). Losing the table
-- reverts to the pre-LLM-259 restart-lossy behavior (accepted contracts drop on
-- restart) — data-clean, since the reward only ever moves at completion.
BEGIN;

DROP TABLE IF EXISTS public.labor_contract;
DROP SEQUENCE IF EXISTS public.labor_contract_snapshot_gen_seq;

COMMIT;
