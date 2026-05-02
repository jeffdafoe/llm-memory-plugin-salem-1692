-- ZBBS-104 down: remove herbalist / blacksmith / merchant attributes.

BEGIN;

DELETE FROM actor_attribute
 WHERE slug IN ('herbalist', 'blacksmith', 'merchant');

DELETE FROM attribute_definition
 WHERE slug IN ('herbalist', 'blacksmith', 'merchant');

COMMIT;
