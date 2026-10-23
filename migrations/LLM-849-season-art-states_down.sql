-- LLM-849 down: remove the season art states.
--
-- Objects standing in a season state go back to their asset's default state
-- first; the engine's one-minute sweep then restores a bush's berries / bare
-- from its stock. The tags go with their states (ON DELETE CASCADE).

BEGIN;

UPDATE village_object o
   SET current_state = a.default_state
  FROM asset a
 WHERE a.id = o.asset_id
   AND o.current_state IN (SELECT s.state
                             FROM asset_state s
                             JOIN asset_state_tag t ON t.state_id = s.id
                            WHERE s.asset_id = o.asset_id AND t.tag LIKE 'season-%');

DELETE FROM asset_state s
 USING asset_state_tag t
 WHERE t.state_id = s.id
   AND t.tag LIKE 'season-%';

COMMIT;
