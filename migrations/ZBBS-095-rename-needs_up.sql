-- ZBBS-095: rename needs system from "attribute" to "needs".
--
-- The hunger / thirst / tiredness ticking system has been called the
-- "attribute" system since ZBBS-082, but the values it manages are
-- semantically NPC needs — that's the term the rest of the engine
-- already uses for the same concept (see needLabel, needLabelTier,
-- loadNeedThreshold in the same file). The mismatched naming was
-- never a problem in isolation; it becomes one now that ZBBS-096
-- introduces an attribute_definition / actor_attribute system whose
-- "attribute" means a chip-style role/trait assignment.
--
-- This migration renames the two persisted setting keys that the
-- ticking code reads/writes:
--
--   attribute_tick_amount    -> needs_tick_amount
--   last_attribute_tick_at   -> last_needs_tick_at
--
-- The Go-side rename of constants, functions, and log strings lands
-- in the same commit; this migration must run before the renamed code
-- starts, so the deploy ordering (Ansible runs migrations before
-- service restart) handles it correctly.
--
-- The setting table uses key as the PK, so the rename is an UPDATE on
-- the key column (Postgres allows updating a primary key value when
-- no rows reference it; nothing has a FK to setting.key). The
-- description column also gets refreshed to drop the "attribute" word.

BEGIN;

UPDATE setting
   SET key = 'needs_tick_amount',
       description = 'Amount added to each NPC need (hunger/thirst/tiredness) per simulated hour. Capped at 24 in code.'
 WHERE key = 'attribute_tick_amount';

UPDATE setting
   SET key = 'last_needs_tick_at',
       description = 'RFC3339 timestamp of the most recent needs tick boundary. The engine compares NOW() against this to decide how many catch-up hours to apply on the next tick.'
 WHERE key = 'last_attribute_tick_at';

COMMIT;
