-- ZBBS-098 down: remove tavernkeeper attribute and any assignments.

BEGIN;

DELETE FROM actor_attribute WHERE slug = 'tavernkeeper';
DELETE FROM attribute_definition WHERE slug = 'tavernkeeper';

COMMIT;
