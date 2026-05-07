-- ZBBS-157 down: drop the village_gossip table. Loses all historical
-- gossip rows. NPCs continue ticking unaffected; the perception block
-- silently disappears.

BEGIN;
DROP TABLE IF EXISTS village_gossip;
COMMIT;
