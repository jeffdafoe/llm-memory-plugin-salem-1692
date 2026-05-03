-- ZBBS-109: store the target's structure_id at dispatch on summon_errand.
--
-- The earlier tolerance check compared target's current pos to
-- target_dispatch_x/y, but that field stores the WALK target — when
-- the target is inside a structure, the loiter slot, which is 96-160px
-- away from the stall center. Tolerance failed even when the target
-- was right where we left them.
--
-- Cleaner gate: when we walk to a structure's loiter slot, remember
-- which structure. At delivery time, compare target's current
-- inside_structure_id to the dispatch-time value. NULL means target
-- was in the open village; fall back to a tight distance tolerance
-- against target_dispatch_x/y (which equals their tile in that case).

BEGIN;

ALTER TABLE summon_errand
    ADD COLUMN target_dispatch_structure_id UUID
    REFERENCES village_object(id) ON DELETE SET NULL;

COMMIT;
