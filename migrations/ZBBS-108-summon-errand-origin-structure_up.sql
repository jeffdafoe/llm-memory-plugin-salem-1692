-- ZBBS-108: capture the messenger's origin structure on a summon errand.
--
-- Without this, the return-walk leg lands the messenger at their
-- origin world coords with inside=false — visibly outside their home
-- even when their sprite renders at the door tile. Two symptoms:
--
--   1. Go Home stays enabled in the editor on a non-VA messenger
--      who's clearly "at home."
--   2. Visiting NPCs (the messenger walking to a stall target) cut
--      across the structure footprint instead of approaching via the
--      loiter ring. The walk-to-target leg now resolves to a loiter
--      slot when the target is inside, so the messenger uses the
--      same approach a chore visitor would.
--
-- Captured at the summoner_at_point transition (when the messenger is
-- dispatched), BEFORE we clear inside/inside_structure_id to prep them
-- for the walk. NULL means the messenger was in the open village at
-- dispatch — no structure to re-enter on return.

BEGIN;

ALTER TABLE summon_errand
    ADD COLUMN messenger_origin_structure_id UUID
    REFERENCES village_object(id) ON DELETE SET NULL;

COMMIT;
