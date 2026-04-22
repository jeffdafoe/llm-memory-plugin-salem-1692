-- ZBBS-066: widen worker schedule offset to minutes.
--
-- ZBBS-064 shipped schedule_offset_hours as INTEGER. Jeff hit the
-- first case where that granularity is too coarse — he wants to be
-- able to pick half and quarter-hour shifts.
--
-- Switching the unit to minutes (rather than REAL hours) keeps every
-- layer of the stack doing integer arithmetic: no SQL/Go/Godot float
-- precision drift, no equality-near-boundaries gotchas. Godot UI still
-- shows hours with step=0.25, translates to/from minutes on the wire.
--
-- Range: [-1380, 1380] = [-23h, +23h], identical to the old range in
-- finer units. No existing configurations are invalidated.

BEGIN;

-- Rename the column in place, then widen and rescale. Preserving data
-- via  *  60  ensures any existing worker's arrive/leave times stay
-- byte-for-byte identical after the migration.
ALTER TABLE npc
    RENAME COLUMN schedule_offset_hours TO schedule_offset_minutes;

ALTER TABLE npc
    DROP CONSTRAINT IF EXISTS npc_schedule_offset_hours_check;

-- pgSQL names auto-generated CHECK constraints predictably but we
-- guard with IF EXISTS above since the historical name may differ.
-- Then rescale and install the new CHECK.
UPDATE npc SET schedule_offset_minutes = schedule_offset_minutes * 60;

ALTER TABLE npc
    ADD CONSTRAINT npc_schedule_offset_minutes_check
        CHECK (schedule_offset_minutes BETWEEN -1380 AND 1380);

COMMIT;
