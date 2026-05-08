-- ZBBS-180: tag the Tavern as lodging too
--
-- Cleanup migration: the Tavern is a combo establishment (bar / dining
-- AND paid bedrooms upstairs), but until now it carried only the
-- 'tavern' tag. Two queries had to special-case it via
-- `tag IN ('lodging', 'tavern')`:
--
--   - pc_handlers.go: spawn-home lookup
--   - recovery_options.go: loadInnRestSpots
--
-- Both will simplify to `tag = 'lodging'` once the Tavern carries the
-- lodging tag itself. The 'tavern' tag stays — it describes the
-- *kind* of establishment (bar/dining behaviors); 'lodging' describes
-- the *function* of providing paid sleep. Layered tags, single-purpose
-- queries.
--
-- Mirrors the pattern just established in ZBBS-179 with the 'business'
-- tag: instead of `IN ('tavern','smithy','shop')` everywhere, tag them
-- with 'business' and query that. Same idea here.
--
-- Idempotent: NOT EXISTS guard.

BEGIN;

INSERT INTO village_object_tag (object_id, tag)
SELECT vot.object_id, 'lodging'
  FROM village_object_tag vot
 WHERE vot.tag = 'tavern'
   AND NOT EXISTS (
       SELECT 1 FROM village_object_tag existing
        WHERE existing.object_id = vot.object_id
          AND existing.tag = 'lodging'
   );

COMMIT;
