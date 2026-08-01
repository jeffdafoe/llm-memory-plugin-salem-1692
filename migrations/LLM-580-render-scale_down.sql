-- LLM-580 down: drop the render-scale hint. Clients fall back to their
-- default (2.0) for every sprite.

BEGIN;

ALTER TABLE public.npc_sprite
    DROP COLUMN IF EXISTS render_scale;

ALTER TABLE public.actor
    DROP CONSTRAINT IF EXISTS actor_display_name_key;
ALTER TABLE public.actor
    ADD CONSTRAINT actor_display_name_key UNIQUE (display_name);

COMMIT;
