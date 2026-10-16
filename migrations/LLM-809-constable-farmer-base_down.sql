-- LLM-809 down: Constable Gideon Marsh back on "Guard (v00)" and the farmer
-- sprite removed. Any other actor put on the sprite since is cleared to no
-- sprite first (actor.sprite_id has an FK).

BEGIN;

UPDATE public.actor
   SET sprite_id = '4a3286a7-d34d-4a4d-8041-8adc04193148'
 WHERE id = '4561da54-eb08-46c8-8f05-ddc0aadaebff'
   AND sprite_id = '809f0c01-0000-4000-8000-000000000001';

UPDATE public.actor
   SET sprite_id = NULL
 WHERE sprite_id = '809f0c01-0000-4000-8000-000000000001';

DELETE FROM public.npc_sprite WHERE id = '809f0c01-0000-4000-8000-000000000001';

COMMIT;
