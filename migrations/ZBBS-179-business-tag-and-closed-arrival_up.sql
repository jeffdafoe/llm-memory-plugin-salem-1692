-- ZBBS-179: business tag + closed-business arrival narration foundation
--
-- Adds a 'business' village_object_tag and seeds it on existing
-- transaction-shaped placements (tavern / smithy / shop). The tag is
-- the canonical "yes, this is a place where customers go to transact"
-- classifier — used by the new isBusinessClosed predicate and the
-- closed-arrival narration.
--
-- Why a separate tag rather than IN ('tavern','smithy','shop') in the
-- predicate: the category set will keep growing (apothecary, bakery,
-- forge variants), and an explicit 'business' tag means the predicate
-- doesn't need to track that list. Seed once, query simply.
--
-- 'lodging' is intentionally not auto-tagged 'business'. A pure inn
-- (lodging-only, no keeper actively selling stuff) shouldn't trigger
-- closed-for-business narration just because the keeper's on break;
-- it's not "open" or "closed" in the shop sense. The tavern in the
-- village is both lodging and business — the seed below only tags
-- placements that already have the 'tavern' category, so the tavern
-- gets the business tag via that path.
--
-- Idempotent: NOT EXISTS guard on each insert.

BEGIN;

INSERT INTO village_object_tag (object_id, tag)
SELECT vot.object_id, 'business'
  FROM village_object_tag vot
 WHERE vot.tag IN ('tavern', 'smithy', 'shop')
   AND NOT EXISTS (
       SELECT 1 FROM village_object_tag existing
        WHERE existing.object_id = vot.object_id
          AND existing.tag = 'business'
   );

COMMIT;
