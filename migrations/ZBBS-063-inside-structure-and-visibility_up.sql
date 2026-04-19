-- ZBBS-063: track which structure an NPC is inside + per-asset
-- visible-while-inside flag.
--
-- npc.inside_structure_id: set on arrival at home/work door, cleared on
-- walk start. Enables occupancy-driven state flipping (market stall's
-- open/closed), and will back any future "who's in this building" UI.
--
-- asset.visible_when_inside: market stalls and other see-through buildings
-- want the villager rendered at their door tile, not hidden. Default false
-- matches existing house behavior.

BEGIN;

ALTER TABLE npc
    ADD COLUMN inside_structure_id UUID
        REFERENCES village_object(id) ON DELETE SET NULL;

ALTER TABLE asset
    ADD COLUMN visible_when_inside BOOLEAN NOT NULL DEFAULT false;

COMMIT;
