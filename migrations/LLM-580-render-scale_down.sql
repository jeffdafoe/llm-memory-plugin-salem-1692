-- LLM-580 down: drop the render-scale hint. Clients fall back to their
-- default (2.0) for every sprite.

BEGIN;

ALTER TABLE public.npc_sprite
    DROP COLUMN IF EXISTS render_scale;

COMMIT;
