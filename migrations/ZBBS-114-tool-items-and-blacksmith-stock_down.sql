-- ZBBS-114 down: remove tool item kinds and any held tool stock.
--
-- actor_inventory has no ON DELETE on the item_kind FK, so any inventory
-- row referencing these items must be cleared before the item_kind rows
-- can drop. Cascading by hand here keeps the down predictable.

BEGIN;

-- Strip the grounding rule out of vendor role instructions.
UPDATE attribute_definition
   SET instructions = REPLACE(
           instructions,
           E'- You can only offer or sell items in your inventory list (the "Items you can sell" line in your perception). Never claim to have or invent goods that aren''t in that list. If a customer asks for something not in stock, say plainly that you don''t carry it.\n',
           ''
       ),
       updated_at = NOW()
 WHERE slug IN ('blacksmith', 'herbalist', 'merchant', 'tavernkeeper');

DELETE FROM actor_inventory
 WHERE item_kind IN ('hammer', 'axe', 'horseshoe', 'nail');

DELETE FROM item_kind
 WHERE name IN ('hammer', 'axe', 'horseshoe', 'nail');

COMMIT;
