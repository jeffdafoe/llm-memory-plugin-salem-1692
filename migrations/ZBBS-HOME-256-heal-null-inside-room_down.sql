-- Down for ZBBS-HOME-256 — intentional no-op.
--
-- The up migration healed inside_room_id values that were broken to
-- begin with. There's no way to identify which inside_room_id values
-- were healed by this migration vs. set normally by setNPCInside, so
-- any reverse would scrub valid runtime state.

SELECT 1;
