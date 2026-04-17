-- ZBBS-046: Rename the seeded NPC Martha → Prudence Ward.
--
-- "Martha" was a placeholder during milestone 1 wiring. Prudence Ward fits
-- the Salem 1692 period aesthetic. Only the display_name changes; the UUID
-- and position stay.

UPDATE npc
SET display_name = 'Prudence Ward'
WHERE display_name = 'Martha';
