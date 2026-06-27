-- LLM-150: drop the dead social-scheduler columns on actor.
--
-- The decorative social scheduler (the once-a-minute deterministic mover that
-- walked no-VA NPCs to a tagged gathering spot and back home) is removed in
-- favour of LLM-decided evening behaviour (epic LLM-147). It was never
-- configured on any live actor -- all four columns are NULL everywhere -- so
-- this drops dead storage. Nothing in engine/sim reads these columns after the
-- companion code removal; the all-or-none + range CHECK constraints go with them.
--
-- The actor table is checkpointed by the running engine, so this migration must
-- be applied with the engine STOPPED, or the old binary's checkpoint INSERT
-- (which still names these columns) would error against the dropped columns
-- mid-flight. The deploy playbook already does this: it stops salem-engine before
-- running migrations and restarts the freshly-built binary after (the LLM-73
-- stop -> migrate -> start order in infrastructure/playbooks/deploy.yml), so the
-- standard deploy (sudo bash deploy.sh) applies this safely.

BEGIN;

ALTER TABLE actor
    DROP CONSTRAINT IF EXISTS actor_social_all_or_none,
    DROP CONSTRAINT IF EXISTS actor_social_end_minute_check,
    DROP CONSTRAINT IF EXISTS actor_social_start_minute_check,
    DROP COLUMN IF EXISTS social_tag,
    DROP COLUMN IF EXISTS social_start_minute,
    DROP COLUMN IF EXISTS social_end_minute,
    DROP COLUMN IF EXISTS social_last_boundary_at;

COMMIT;
