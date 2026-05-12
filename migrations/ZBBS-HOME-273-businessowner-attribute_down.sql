-- ZBBS-HOME-273 — rollback.
--
-- Order matters: delete cooldown rows (FK to actor), drop the table,
-- drop the actor_attribute seeds (FK to attribute_definition), drop
-- the attribute_definition row. Settings drop last — they're inert
-- and removing them is just hygiene.

BEGIN;

DROP TABLE IF EXISTS actor_interaction_cooldown;

DELETE FROM actor_attribute WHERE slug = 'businessowner';
DELETE FROM attribute_definition WHERE slug = 'businessowner';

DELETE FROM setting WHERE key IN (
    'businessowner_greet_cooldown_minutes',
    'businessowner_farewell_cooldown_minutes'
);

COMMIT;
