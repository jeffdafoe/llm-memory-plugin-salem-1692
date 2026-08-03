-- LLM-592 follow-up down: put Ezekiel's garments back to their seeded wear.
--
-- Guarded on the exact values the _up wrote, so a revert after the engine has
-- worn them further does not hand him back clothes he has since used up. Same
-- posture as the seed migration's down: undo what this migration DID, never
-- overwrite live state that has moved on.
--
-- actor_inventory is checkpoint-written; apply with the engine stopped.

BEGIN;

UPDATE actor_inventory
   SET worn_minutes_left = CASE item_kind
                               WHEN 'shift'    THEN 9720
                               WHEN 'breeches' THEN 12960
                           END
 WHERE actor_id = '019da6f9-1b4c-7dda-bb6b-3248cdafb2c4'  -- Ezekiel Crane
   AND item_kind IN ('shift', 'breeches')
   AND worn_minutes_left IN (1188, 1584);

COMMIT;
