-- ZBBS-038 down

DELETE FROM setting WHERE key IN ('world_dawn_time', 'world_dusk_time', 'world_timezone');

DROP TABLE IF EXISTS world_phase;
