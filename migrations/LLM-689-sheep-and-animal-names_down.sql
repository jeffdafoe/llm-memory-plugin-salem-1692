-- LLM-689 down: remove the sheep sprite.
--
-- REFUSES while a sheep is placed — its actor row references the sprite
-- (actor_sprite_id_fkey). Remove the sheep first (editor, or engine stopped:
-- DELETE FROM actor WHERE sprite_id = '689d0c01-…'), then re-run.
--
-- The animal renames are NOT undone: "Villager N" was a default, not a choice,
-- and the rolled-back engine runs fine on species names. The tiny-swords pack
-- row predates this migration on prod, so it stays.

BEGIN;

DO $$
DECLARE placed int;
BEGIN
    SELECT count(*) INTO placed FROM public.actor
     WHERE sprite_id = '689d0c01-0000-4000-8000-000000000001';
    IF placed > 0 THEN
        RAISE EXCEPTION 'LLM-689 down: % sheep still placed. Remove them first, then re-run.', placed;
    END IF;
END $$;

DELETE FROM public.npc_sprite WHERE id = '689d0c01-0000-4000-8000-000000000001';

COMMIT;
