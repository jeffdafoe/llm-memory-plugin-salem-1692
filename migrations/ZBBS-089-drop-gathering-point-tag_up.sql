-- ZBBS-089: Drop the gathering-point tag, plus repair NPCs incorrectly
-- flagged as inside non-enterable structures.
--
-- Loiter pins now distribute visitors across an 8-tile king's-move slot
-- ring (engine/village_objects.go pickVisitorSlot). The gathering-point
-- tag was a hack to recolor the loiter marker gold for "village rally"
-- placements like the well — irrelevant once visitor distribution is
-- the default behavior. Editor/engine references to the tag are removed
-- in the same change.
--
-- Pre-fix, agent visitor moves (executeAgentMoveTo to a non-owned target,
-- executeAgentChore) ran through startReturnWalk with no
-- enterOnArrival flag, so applyArrival flipped inside=true regardless
-- of whether the target was enterable. NPCs at the well, market, etc.
-- got marked inside their loiter target. The post-fix engine has a
-- defensive guard in setNPCInside, but rows already in this state need
-- a one-time repair.

DELETE FROM village_object_tag WHERE tag = 'gathering-point';

UPDATE actor
SET inside = false, inside_structure_id = NULL
WHERE inside = true
  AND inside_structure_id IN (
      SELECT o.id FROM village_object o
      JOIN asset a ON a.id = o.asset_id
      WHERE a.enterable = false
  );
