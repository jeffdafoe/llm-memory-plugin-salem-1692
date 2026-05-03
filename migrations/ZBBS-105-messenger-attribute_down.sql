-- ZBBS-105 down: remove the messenger attribute and any assignments.

BEGIN;

DELETE FROM actor_attribute WHERE slug = 'messenger';
DELETE FROM attribute_definition WHERE slug = 'messenger';

COMMIT;
