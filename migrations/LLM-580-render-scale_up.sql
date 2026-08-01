-- LLM-580: per-sprite render scale. The client had a hardcoded 2x for every
-- NPC sprite — sized for the 32x32 human sheets, which made a duck (most of
-- its cell) stand hip-height to a villager. render_scale is a render hint the
-- client applies to the character sprite, its anchor offset, and the ground
-- decal; the engine never reads it. Data-driven so the next animal (chickens
-- are drawn smaller still) is a number in a row, not a client special case.

BEGIN;

ALTER TABLE public.npc_sprite
    ADD COLUMN IF NOT EXISTS render_scale double precision NOT NULL DEFAULT 2.0;

UPDATE public.npc_sprite
   SET render_scale = 1.0
 WHERE pack_id = 'mana-seed-livestock';

-- Defer the display-name uniqueness check to COMMIT. The checkpoint upserts
-- the live actor set BEFORE deleting stale rows (SaveSnapshot: upsert loop,
-- then DELETE WHERE snapshot_gen < gen, one transaction) — so with an
-- immediate constraint, deleting an actor and recreating its name between two
-- checkpoints collides with the not-yet-deleted stale row and wedges
-- checkpointing permanently (the same durability failure as the two-"Villager"
-- incident, via a path the in-memory uniqueness gate cannot see). Deferred,
-- the constraint judges the transaction's END state, where the stale row is
-- gone. End-state uniqueness is unchanged; no FK/upsert arbiter references
-- display_name.
ALTER TABLE public.actor
    DROP CONSTRAINT IF EXISTS actor_display_name_key;
ALTER TABLE public.actor
    ADD CONSTRAINT actor_display_name_key UNIQUE (display_name)
    DEFERRABLE INITIALLY DEFERRED;

COMMIT;
