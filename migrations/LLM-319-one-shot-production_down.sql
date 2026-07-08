-- LLM-319 down: restore the continuous-production schema — re-add
-- actor.production_focus (LLM-128), recreate actor_produce_state and its
-- sequence (shapes verbatim from the schema.sql baseline), and drop the
-- one-shot cycle columns.
--
-- Data is NOT restorable: the dropped focus/anchors are gone (restored empty,
-- the pre-LLM-128 restart posture — crafters re-choose on their next tick),
-- and any in-flight one-shot batches are abandoned with their consumed inputs.
-- Same engine-stopped caveat as the up.
--
-- Rerun-safe via IF EXISTS / IF NOT EXISTS.

BEGIN;

ALTER TABLE actor ADD COLUMN IF NOT EXISTS production_focus text NOT NULL DEFAULT '';

ALTER TABLE actor DROP COLUMN IF EXISTS production_item;
ALTER TABLE actor DROP COLUMN IF EXISTS production_batch_qty;
ALTER TABLE actor DROP COLUMN IF EXISTS production_remaining_seconds;

CREATE TABLE IF NOT EXISTS actor_produce_state (
    actor_id uuid NOT NULL,
    item_kind character varying(32) NOT NULL,
    last_produced_at timestamp with time zone,
    snapshot_gen bigint DEFAULT 0 NOT NULL,
    CONSTRAINT actor_produce_state_pkey PRIMARY KEY (actor_id, item_kind),
    CONSTRAINT actor_produce_state_actor_id_fkey FOREIGN KEY (actor_id)
        REFERENCES actor(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_actor_produce_state_snapshot_gen
    ON actor_produce_state (snapshot_gen);

CREATE SEQUENCE IF NOT EXISTS actor_produce_state_snapshot_gen_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;

COMMIT;
