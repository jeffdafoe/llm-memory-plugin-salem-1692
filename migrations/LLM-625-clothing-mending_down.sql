-- LLM-625 rollback: remove the mending economy — the thread/mending catalog
-- rows, Hannah's buy-thread restock entry, the Inn's mending tag, and any
-- thread holdings (the FK from actor_inventory blocks the kind delete
-- otherwise). Manual-rollback only (the migration runner never applies _down).

BEGIN;

UPDATE village_object
   SET tags = array_remove(tags, 'mending')
 WHERE id = '019d98af-ac9b-7833-8e03-5a7015bb5b0c';

UPDATE actor_attribute
   SET params = jsonb_set(
       params,
       '{restock}',
       COALESCE(
           (SELECT jsonb_agg(e)
              FROM jsonb_array_elements(
                   CASE WHEN jsonb_typeof(params->'restock') = 'array'
                        THEN params->'restock' ELSE '[]'::jsonb END) AS e
             WHERE NOT (e->>'item' = 'thread' AND e->>'source' = 'buy')),
           '[]'::jsonb
       )
   )
 WHERE actor_id = '70419d0c-3668-428c-8bd8-633993c3aa60'
   AND slug = 'innkeeper';

DELETE FROM actor_inventory WHERE item_kind IN ('thread', 'mending');
DELETE FROM item_recipe WHERE output_item IN ('thread', 'mending');
DELETE FROM item_kind WHERE name IN ('thread', 'mending');

COMMIT;
