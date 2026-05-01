-- ZBBS-097 down: remove worker from attribute_definition and unwind
-- the assignments it produced. Reverses ZBBS-097 only — does NOT
-- restore the actor.behavior column, which was left intact by both
-- this migration and ZBBS-096.

BEGIN;

DELETE FROM actor_attribute WHERE slug = 'worker';
DELETE FROM attribute_definition WHERE slug = 'worker';

COMMIT;
