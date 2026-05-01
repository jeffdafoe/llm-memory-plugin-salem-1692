-- ZBBS-089 down: Restore the gathering-point tag on the well placement(s).
--
-- This down migration is best-effort: the original set of gathering-point
-- placements isn't preserved, so we re-tag any placement currently tagged
-- 'well' (the only one configured pre-ZBBS-089). If you had additional
-- gathering-point placements at deploy time, they'll need to be re-tagged
-- manually.

INSERT INTO village_object_tag (object_id, tag)
SELECT object_id, 'gathering-point'
FROM village_object_tag
WHERE tag = 'well'
ON CONFLICT DO NOTHING;
