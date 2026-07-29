-- LLM-556 down: restore the pay_ledger.huddle_id column.
--
-- Restores the schema shape: the column was a plain nullable TEXT column with no
-- default, constraint, or index.
--
-- Data-lossless under the invariant the up migration verified — huddle_id was
-- NULL on every row in production, so there are no values to recover. DROP
-- COLUMN discards values permanently, so that is a conditional guarantee, not an
-- absolute one: if some other environment had populated the column before the
-- drop, this cannot bring those values back.

BEGIN;

ALTER TABLE public.pay_ledger ADD COLUMN IF NOT EXISTS huddle_id text;

COMMIT;
