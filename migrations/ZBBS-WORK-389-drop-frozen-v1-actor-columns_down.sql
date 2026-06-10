-- ZBBS-WORK-389 reverse: re-add the dropped v1 actor bookkeeping columns.
--
-- Shape-only restore. The frozen v1-era values (and any deleted visitor
-- rows) are unrecoverable — columns come back with their original types,
-- defaults, and constraints, holding NULL (or the default on the two
-- NOT NULL columns) everywhere. That is acceptable because no engine
-- code in any deployable revision reads them: a code rollback to any
-- v2 revision pairs fine with the empty shape, and there is no v1 left
-- to roll back to (the monolith was deleted wholesale post-go-live).
--
-- The partial index and lateness CHECK are restored to keep schema.sql
-- diff-clean against a database that never ran the up migration.

BEGIN;

ALTER TABLE public.actor
    ADD COLUMN inside boolean DEFAULT false NOT NULL,
    ADD COLUMN llm_memory_api_key character varying(255),
    ADD COLUMN home_x double precision,
    ADD COLUMN home_y double precision,
    ADD COLUMN lateness_window_minutes integer DEFAULT 0 NOT NULL,
    ADD COLUMN last_shift_tick_at timestamp with time zone,
    ADD COLUMN agent_override_until timestamp with time zone,
    ADD COLUMN last_pc_input_at timestamp with time zone,
    ADD COLUMN last_pc_seen_at timestamp with time zone,
    ADD COLUMN last_tiredness_recovery_at timestamp with time zone,
    ADD COLUMN visitor_expires_at timestamp with time zone,
    ADD COLUMN visitor_archetype character varying(50),
    ADD COLUMN visitor_origin character varying(100),
    ADD COLUMN visitor_disposition character varying(50);

ALTER TABLE public.actor
    ADD CONSTRAINT actor_lateness_window_minutes_check
    CHECK (((lateness_window_minutes >= 0) AND (lateness_window_minutes <= 180)));

CREATE INDEX idx_actor_visitor_expires ON public.actor USING btree (visitor_expires_at)
    WHERE (visitor_expires_at IS NOT NULL);

COMMIT;
