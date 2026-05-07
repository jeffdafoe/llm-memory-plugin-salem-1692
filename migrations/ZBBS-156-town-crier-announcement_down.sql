-- ZBBS-156 down: drop the announcement table. Loss of content only;
-- the existing Town Crier rotation behavior continues without it.

BEGIN;
DROP TABLE IF EXISTS town_crier_announcement;
COMMIT;
