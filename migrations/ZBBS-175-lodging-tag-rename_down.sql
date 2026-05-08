-- ZBBS-175 down — restore `lodging` tag to `inn`
--
-- Inverse of the up migration. Note this only flips back rows that
-- carry `lodging` as a result of the up migration; if anyone has
-- since added new lodging-tagged rows for non-inn structures (e.g.,
-- a barn or dwelling tagged for the upcoming NPC sleep mechanism),
-- those would also revert to `inn` here. Down migrations are best-
-- effort restoration of the data shape pre-up; manual cleanup of
-- post-up additions is on the operator.

BEGIN;

UPDATE village_object_tag SET tag = 'inn' WHERE tag = 'lodging';

DELETE FROM setting WHERE key IN (
    'npc_sleep_max_duration_hours',
    'npc_auto_sleep_min_tiredness'
);

COMMIT;
