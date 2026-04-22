-- ZBBS-067 down: drop the lateness window column.

BEGIN;

ALTER TABLE npc DROP COLUMN lateness_window_minutes;

COMMIT;
