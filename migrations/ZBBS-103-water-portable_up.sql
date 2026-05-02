-- ZBBS-103: water is portable.
--
-- Water was the only drink without the `portable` capability — a vestige
-- of the original "drink at the well, that's it" framing. Now that the
-- gather tool (added alongside this migration) lets an NPC fill a pail
-- of water at the well and credit it to inventory, water needs to be
-- carryable so the take-home flows work end-to-end:
--
--   - serve(item=water, consume_now=false) — tavernkeeper hands a pail
--     to a customer to take home. Without portable: rejected as
--     non-carryable.
--   - pay(item=water, consume_now=false) — customer buys water from a
--     vendor to take home. Same rejection.
--
-- Stew remains non-portable (hot bowl, no sealed container). Ale and
-- milk were already portable; this aligns water with them.

BEGIN;

UPDATE item_kind
   SET capabilities = capabilities || ARRAY['portable']::varchar[]
 WHERE name = 'water'
   AND NOT ('portable' = ANY(capabilities));

COMMIT;
