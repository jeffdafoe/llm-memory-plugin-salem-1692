-- ZBBS-124: teach vendor roles to emit speak.price.
--
-- The new optional `price` parameter on the speak tool records a
-- structured per-unit quote (scene_quote table) that the pay handler
-- enforces. Without role-prompt guidance, NPCs will keep quoting
-- prices in prose alone and the structural guard never triggers.
--
-- Adds a single bullet to the TOOL DISCIPLINE block of each vendor
-- role (tavernkeeper, merchant, herbalist) instructing them to set
-- speak.price whenever they state a per-unit price out loud. The
-- bullet is appended after the existing "mentions" rule so the two
-- structured fields appear together.
--
-- Each UPDATE is paired with an idempotency guard (WHERE NOT LIKE)
-- so a re-run after a follow-up prompt edit doesn't double-append.
-- The exact phrase "set speak.price" is the sentinel — match against
-- the bullet, not just the field name, because future prompts may
-- mention the field in non-bullet contexts.

BEGIN;

UPDATE attribute_definition
   SET instructions = instructions || E'\n- When you state a per-unit price out loud (e.g. "Stew for 3 coins" or "I''ll let you have a loaf for 2"), set speak.price to that whole-coin number alongside speak.mentions. The engine records the quote and rejects pay() offers that fall short of price * qty — protects you from customers underpaying after you''ve named a number. Only set price when you''re quoting one number for everything you mention; omit it for greetings, listings without prices, or follow-up speech that doesn''t fix a number.'
 WHERE slug = 'tavernkeeper'
   AND instructions NOT LIKE '%set speak.price%';

UPDATE attribute_definition
   SET instructions = instructions || E'\n- When you state a per-unit price out loud (e.g. "Bread is 1 coin a loaf" or "Cheese for 3"), set speak.price to that whole-coin number alongside speak.mentions. The engine records the quote and rejects pay() offers that fall short of price * qty — protects you from customers underpaying after you''ve named a number. Only set price when you''re quoting one number for everything you mention; omit it for greetings, listings without prices, or follow-up speech that doesn''t fix a number.'
 WHERE slug = 'merchant'
   AND instructions NOT LIKE '%set speak.price%';

UPDATE attribute_definition
   SET instructions = instructions || E'\n- When you state a per-unit price out loud (e.g. "Berries for 2 coins" or "A bundle of mint is 4"), set speak.price to that whole-coin number alongside speak.mentions. The engine records the quote and rejects pay() offers that fall short of price * qty — protects you from customers underpaying after you''ve named a number. Only set price when you''re quoting one number for everything you mention; omit it for greetings, listings without prices, or follow-up speech that doesn''t fix a number.'
 WHERE slug = 'herbalist'
   AND instructions NOT LIKE '%set speak.price%';

COMMIT;
