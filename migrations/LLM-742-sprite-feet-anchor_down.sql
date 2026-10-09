-- LLM-742 down: drop the per-sprite feet line. Clients fall back to their
-- default (0.9) for every sprite.

BEGIN;

ALTER TABLE public.npc_sprite
    DROP COLUMN IF EXISTS anchor_y;

COMMIT;
