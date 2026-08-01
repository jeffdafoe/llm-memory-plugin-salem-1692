-- Revert the duck render scale to the LLM-580 value.
--
-- This is a rollback to a KNOWN migration value, not a true per-row inverse:
-- it restores 1.0 unconditionally rather than whatever each row happened to
-- hold beforehand. That is exact here because LLM-580 set every duck row to
-- 1.0 and nothing between the two migrations writes render_scale (the engine
-- only ever reads it), so 1.0 is the only value these rows can have been at.
-- A database where that is not true would need its values captured first.

BEGIN;

UPDATE public.npc_sprite
   SET render_scale = 1.0
 WHERE pack_id = 'mana-seed-livestock'
   AND name LIKE 'Duck (%';

COMMIT;
