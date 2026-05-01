-- Down for ZBBS-086 — restore the npc_baseline_ticks_enabled setting row
-- with its original default. Won't restore the dispatcher code; that
-- requires a code revert. Default 'false' matches ZBBS-081's seed.

BEGIN;

INSERT INTO setting (key, value)
VALUES ('npc_baseline_ticks_enabled', 'false')
ON CONFLICT (key) DO NOTHING;

COMMIT;
