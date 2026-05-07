-- ZBBS-159 down: drop npc_errand_offer table.

BEGIN;
DROP TABLE IF EXISTS npc_errand_offer;
COMMIT;
