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

-- Dedupe any existing duplicate display names BEFORE re-adding the
-- constraint. The live prod DB has two "Villager" rows (the 2026-08-01
-- incident's ducks, persisted after the constraint was operator-dropped to
-- restore checkpointing), and ADD CONSTRAINT fails on pre-existing
-- duplicates. Later rows (by id) get a " N" suffix, matching the spelling
-- CreateNPC's new self-dedupe mints; the CASE guards the pathological
-- "Villager 2 already exists" case with an id fragment, and the base is
-- clipped so the suffixed name stays inside varchar(100).
WITH ranked AS (
    SELECT id, display_name,
           row_number() OVER (PARTITION BY display_name ORDER BY id) AS rn
      FROM public.actor
)
UPDATE public.actor a
   SET display_name = left(a.display_name, 90) || ' ' ||
       CASE WHEN EXISTS (
                SELECT 1 FROM public.actor x
                 WHERE x.display_name = left(a.display_name, 90) || ' ' || r.rn::text
            )
            THEN r.rn::text || '-' || left(a.id::text, 4)
            ELSE r.rn::text
       END
  FROM ranked r
 WHERE a.id = r.id
   AND r.rn > 1;

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
