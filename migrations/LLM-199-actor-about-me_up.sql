-- LLM-199: per-actor "about me" soul column for shared-VA NPCs.
--
-- Shared-VA NPCs (one agent backs many bodies) can't carry identity in a
-- per-agent context/soul note the way a stateful NPC does, so their
-- "## Who you are" perception block rendered empty. The per-actor narrative
-- sweep now synthesizes an accreting first-person soul each day via the
-- dream-sim-soul agent and stores it here; perception renders it. seed_text
-- (dream-pipeline input, never set for shared VAs) and evolving_summary (the
-- older flat consolidation output, no longer written) stay as distinct columns.
--
-- ENGINE-OWNED TABLE — actor_narrative_state is checkpoint-written by the
-- running engine. Purely additive column with a DEFAULT, so no engine stop is
-- required: existing rows backfill to '' (no soul yet), and the currently-
-- running binary's checkpoint UPSERT lists explicit columns that do NOT include
-- about_me, so it leaves the new column at its default until the companion
-- LLM-199 binary deploys. Apply this migration BEFORE deploying that binary (the
-- new UPSERT references about_me); the standard deploy order (migrate -> restart)
-- does this.
--
-- Rerun-safe via IF NOT EXISTS.

BEGIN;

ALTER TABLE actor_narrative_state ADD COLUMN IF NOT EXISTS about_me text NOT NULL DEFAULT '';

COMMIT;
