-- LLM-87 down: restore the eat-and-pick sources' in-place bite to the uniform
-- pre-LLM-87 value (-8).
--
-- The up migration aligned each eat-and-pick row to its item's hunger value;
-- before it, every such source was authored at -8 (the bug being fixed). So
-- reverting all eat-and-pick hunger rows to -8 restores the prior state. Same
-- scope as the up (gather_item set, amount < 0, hunger) and the same
-- engine-stopped caveat. Rerun-safe (after the first apply nothing is off -8).

BEGIN;

UPDATE object_refresh
SET amount = -8
WHERE gather_item IS NOT NULL
  AND amount < 0
  AND attribute = 'hunger'
  AND amount <> -8;

COMMIT;
