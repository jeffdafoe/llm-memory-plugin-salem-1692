-- ZBBS-168: bump pc_idle_sleep_minutes default from 5 to 15.
--
-- Pairs with the engine change in autoBedIdleLodgers that broadens
-- the gate from "PC in their private bedroom" to "PC anywhere in a
-- structure where they hold private room_access". With the wider
-- gate, the auto-bed sweep now also fires when a lodger is sitting
-- at the bar — so the threshold needs to be long enough to feel
-- like "I forgot I was AFK" rather than "I stepped away for 5 min."
--
-- Idempotent: only updates rows still at the original ZBBS-132
-- seed value of '5'. If an admin has retuned the setting to any
-- other value (3, 30, etc.) we leave their override alone.

BEGIN;

UPDATE setting
   SET value = '15'
 WHERE key = 'pc_idle_sleep_minutes'
   AND value = '5';

COMMIT;
