-- LLM-641 down: remove the chicken sprites.
--
-- The sprite deletes fail on the actor.sprite_id FK if a live actor still
-- wears a chicken sprite — deliberate (matching LLM-639's down): remove or
-- reassign those actors first rather than silently orphaning their render.

BEGIN;

DELETE FROM public.npc_sprite_animation
WHERE sprite_id IN (
    '641c0001-0000-4000-8000-000000000001', '641c0002-0000-4000-8000-000000000002',
    '641c0003-0000-4000-8000-000000000003', '641c0004-0000-4000-8000-000000000004',
    '641c0005-0000-4000-8000-000000000005', '641c0006-0000-4000-8000-000000000006');

DELETE FROM public.npc_sprite
WHERE id IN (
    '641c0001-0000-4000-8000-000000000001', '641c0002-0000-4000-8000-000000000002',
    '641c0003-0000-4000-8000-000000000003', '641c0004-0000-4000-8000-000000000004',
    '641c0005-0000-4000-8000-000000000005', '641c0006-0000-4000-8000-000000000006');

COMMIT;
