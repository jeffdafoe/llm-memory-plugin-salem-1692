-- ZBBS-086: Drop the npc_baseline_ticks_enabled setting row.
--
-- The setting was the kill switch / safety valve for autonomous hourly
-- NPC ticks (introduced in ZBBS-081 as part of the chronicler design).
-- Reactive-only became the permanent design — chronicler dispatch +
-- cascade origins (PC speech, NPC arrival, heard-speech) are sufficient
-- without an autonomous baseline pass — so the gate, the dispatcher
-- (`dispatchAgentTicks`), and this row are all gone.

BEGIN;

DELETE FROM setting WHERE key = 'npc_baseline_ticks_enabled';

COMMIT;
