-- ZBBS-040 down

DELETE FROM setting WHERE key = 'world_rotation_time';
ALTER TABLE world_phase DROP COLUMN IF EXISTS last_rotation_at;
ALTER TABLE asset DROP COLUMN IF EXISTS transition_spread_seconds;
ALTER TABLE asset DROP COLUMN IF EXISTS rotation_algo;
