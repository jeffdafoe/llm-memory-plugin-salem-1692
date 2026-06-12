-- ZBBS-HOME-440 reverse: re-add the v1 self-tick scheduler columns.
--
-- Shape-only restore, same rationale as the WORK-389 down: the frozen
-- v1-era values are unrecoverable, and no deployable engine revision
-- after the paired code change reads these columns, so columns coming
-- back NULL everywhere pairs fine with any code rollback.
--
-- next_self_tick_at is restored as TIMESTAMPTZ — its live type after
-- ZBBS-WORK-243's conversion — not the TIMESTAMP a pre-243 schema had.
-- The partial index is restored to keep schema.sql diff-clean against a
-- database that never ran the up migration.

BEGIN;

ALTER TABLE public.actor
    ADD COLUMN next_self_tick_at timestamp with time zone,
    ADD COLUMN next_self_tick_reason text;

CREATE INDEX idx_actor_next_self_tick_at ON public.actor USING btree (next_self_tick_at)
    WHERE (next_self_tick_at IS NOT NULL);

COMMIT;
