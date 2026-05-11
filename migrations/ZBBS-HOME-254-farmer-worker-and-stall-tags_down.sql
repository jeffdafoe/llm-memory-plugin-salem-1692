BEGIN;

UPDATE attribute_definition
   SET behaviors = '[]'::jsonb
 WHERE slug IN ('farmer', 'dairykeeper')
   AND behaviors = '[{"type": "worker"}]'::jsonb;

DELETE FROM asset_state_tag t
 USING asset_state s, asset a
 WHERE t.state_id = s.id
   AND s.asset_id = a.id
   AND a.name = 'Market Stall (Fancy)'
   AND ((s.state = 'open' AND t.tag = 'occupied')
        OR (s.state = 'closed' AND t.tag = 'unoccupied'));

COMMIT;
