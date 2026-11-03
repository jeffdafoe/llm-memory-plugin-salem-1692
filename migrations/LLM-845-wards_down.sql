-- LLM-845 down: the horseshoe loses `ward`, its recipe and Ezekiel's produce
-- line; the witch bottle goes (buy lines, holdings, price anchor, kind). The
-- ward tags on tents are dropped too. A pay_ledger row naming witch_bottle
-- makes the kind delete fail loud: that history must be dealt with by hand.

BEGIN;

UPDATE village_object
   SET tags = array_remove(array_remove(tags, 'ward_horseshoe'), 'ward_witch_bottle')
 WHERE tags && ARRAY['ward_horseshoe', 'ward_witch_bottle'];

UPDATE actor_attribute aa
   SET params = jsonb_set(aa.params, '{restock}',
           COALESCE((SELECT jsonb_agg(entry ORDER BY ord)
                       FROM jsonb_array_elements(aa.params->'restock') WITH ORDINALITY AS t(entry, ord)
                      WHERE NOT (entry->>'item' = 'witch_bottle'
                                 OR (entry->>'item' = 'horseshoe' AND entry->>'source' = 'produce'))), '[]'::jsonb))
 WHERE jsonb_typeof(aa.params->'restock') = 'array'
   AND EXISTS (SELECT 1 FROM jsonb_array_elements(aa.params->'restock') e
                WHERE e->>'item' = 'witch_bottle'
                   OR (e->>'item' = 'horseshoe' AND e->>'source' = 'produce'));

DELETE FROM actor_inventory WHERE item_kind = 'witch_bottle';
DELETE FROM item_recipe WHERE output_item IN ('horseshoe', 'witch_bottle');
DELETE FROM item_kind WHERE name = 'witch_bottle';
UPDATE item_kind SET capabilities = array_remove(capabilities, 'ward') WHERE name = 'horseshoe';

COMMIT;
