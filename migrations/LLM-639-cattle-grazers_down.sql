-- LLM-639 down: remove the cattle sprites and the fence-gate contract.
--
-- The sprite deletes fail on the actor.sprite_id FK if a live actor still
-- wears a cattle sprite — deliberate (matching LLM-579's down): remove or
-- reassign those actors first rather than silently orphaning their render.
--
-- The gate half restores the Ranch Fence Gate to its pre-LLM-639 shape
-- (obstacle, anchor-only footprint, no fence-gate tag). Guarded like the up:
-- on a fresh replay DB the asset does not exist and these match zero rows.

BEGIN;

DELETE FROM public.npc_sprite_animation
WHERE sprite_id IN (
    '639d0c01-0000-4000-8000-000000000001', '639d0c02-0000-4000-8000-000000000002',
    '639d0c03-0000-4000-8000-000000000003', '639d0c04-0000-4000-8000-000000000004',
    '639d0c05-0000-4000-8000-000000000005', '639d0c06-0000-4000-8000-000000000006',
    '639d0c07-0000-4000-8000-000000000007', '639d0c08-0000-4000-8000-000000000008');

DELETE FROM public.npc_sprite
WHERE id IN (
    '639d0c01-0000-4000-8000-000000000001', '639d0c02-0000-4000-8000-000000000002',
    '639d0c03-0000-4000-8000-000000000003', '639d0c04-0000-4000-8000-000000000004',
    '639d0c05-0000-4000-8000-000000000005', '639d0c06-0000-4000-8000-000000000006',
    '639d0c07-0000-4000-8000-000000000007', '639d0c08-0000-4000-8000-000000000008');

DELETE FROM public.asset_state_tag t
 USING public.asset_state s
 WHERE t.state_id = s.id
   AND s.asset_id = 'e2c34e96-14ef-487c-a6af-d2526d4c4526'
   AND t.tag = 'fence-gate';

UPDATE public.asset
   SET is_obstacle = true,
       footprint_left = 0, footprint_right = 0,
       footprint_top = 0, footprint_bottom = 0
 WHERE id = 'e2c34e96-14ef-487c-a6af-d2526d4c4526';

COMMIT;
