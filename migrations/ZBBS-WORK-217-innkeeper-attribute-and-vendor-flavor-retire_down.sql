-- ZBBS-WORK-217 down — restore actor.vendor_flavor, drop innkeeper attribute.
--
-- Best-effort reversal. Hannah's flavor string is restored to its
-- WORK-204 seed value; if it was edited via UPDATE between deploy and
-- rollback, that edit is lost.

BEGIN;

ALTER TABLE actor ADD COLUMN IF NOT EXISTS vendor_flavor TEXT;

UPDATE actor
   SET vendor_flavor = 'The village whispers about who comes and goes from her inn after dark — but Hannah herself never confirms or denies.'
 WHERE display_name = 'Hannah Boggs'
   AND vendor_flavor IS NULL;

DELETE FROM actor_attribute
 WHERE slug = 'innkeeper';

DELETE FROM attribute_definition
 WHERE slug = 'innkeeper';

COMMIT;
