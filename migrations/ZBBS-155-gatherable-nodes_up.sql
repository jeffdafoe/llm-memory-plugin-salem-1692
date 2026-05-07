-- ZBBS-155: gatherable nodes — small finds scattered on the map.
--
-- Random "things on the ground" the PC can stumble across as they walk
-- the village. Pure pickup loop: walk near, get an item, respawn after
-- a cooldown. Foraging atmosphere; small reward; gives the player
-- something passive to do beyond the structured economy.
--
-- v1 scope:
--   - Fixed positions only (no random spawns). Hand-seeded near
--     plausible spots — picnic / wood edges / outskirts.
--   - PC-only pickup. NPCs walk past; the village isn't picked clean
--     before the player arrives.
--   - One item per node, qty per pickup.
--   - last_picked_at + respawn_seconds gates re-pickup.
--   - Auto-pickup on PC walk arrival within proximity (engine-side hook
--     in applyArrivalSideEffects). No new tool / endpoint.
--
-- Out of scope: rarity, generated random spawns, NPC gathering,
-- non-item rewards.

BEGIN;

CREATE TABLE gatherable_node (
    id              BIGSERIAL PRIMARY KEY,
    x               INTEGER NOT NULL,
    y               INTEGER NOT NULL,
    item_kind       VARCHAR(32) NOT NULL REFERENCES item_kind(name) ON UPDATE CASCADE,
    qty             INTEGER NOT NULL DEFAULT 1 CHECK (qty > 0),
    respawn_seconds INTEGER NOT NULL DEFAULT 1800 CHECK (respawn_seconds > 0),
    last_picked_at  TIMESTAMPTZ NULL,
    display_label   TEXT NULL
);

-- Spatial index for the proximity query at pickup time. Two-column
-- btree is fine — pickup runs O(N rows in a small bounding box) per
-- arrival, and the table is hand-seeded at ~10 rows. Replace with PostGIS
-- if the table grows past hundreds of rows.
CREATE INDEX ix_gatherable_node_xy ON gatherable_node (x, y);

-- Seed: 6 berries + 2 wheat scattered around outdoor / unmarked spots.
-- Coordinates picked relative to existing structure positions:
--   Picnic Area:   (923, -1269)
--   Meeting House: (527, -1466)
--   Tavern:        (1503, 636)
--   PW Apothecary: (1487, 321)
-- Berries cluster near the picnic area + meeting house grounds (places
-- a player would plausibly stop and look around). Wheat ears are loose
-- near the apothecary garden — hint at edge-of-cultivation foraging.
INSERT INTO gatherable_node (x, y, item_kind, qty, respawn_seconds, display_label) VALUES
    (1023, -1169, 'berries', 1, 1800, 'a thicket of wild berries'),
    ( 823, -1369, 'berries', 1, 1800, 'a small berry patch'),
    (1023, -1369, 'berries', 1, 1800, 'a tangle of brambles'),
    ( 627, -1366, 'berries', 1, 1800, 'a few berries fallen on the path'),
    ( 427, -1566, 'berries', 1, 1800, 'a wild bush heavy with fruit'),
    ( 627, -1566, 'berries', 1, 1800, 'a low bramble patch'),
    (1387,  221, 'wheat',   1, 2400, 'a stray bundle of wheat'),
    (1587,  221, 'wheat',   1, 2400, 'wheat ears scattered at the verge');

COMMIT;
