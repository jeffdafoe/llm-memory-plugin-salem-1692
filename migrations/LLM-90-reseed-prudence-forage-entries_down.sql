-- Revert LLM-90 re-seed: clear Prudence Ward's herbalist forage restock entries,
-- back to the empty params the role carried after the LLM-59 hotfix. Manual-
-- rollback-only (the runner never applies a _down); apply with the engine stopped.

BEGIN;

-- Exact-match guard: only reset if params still equals what this re-seed set, so a
-- later change to her herbalist params isn't erased by this revert.
UPDATE actor_attribute
SET params = '{}'::jsonb
WHERE actor_id = '019dbcec-1149-7149-8a49-2cdb54680b86'  -- Prudence Ward
  AND slug = 'herbalist'
  AND params = '{"restock": [{"item": "raspberries", "source": "forage", "max": 10}, {"item": "blueberries", "source": "forage", "max": 10}]}'::jsonb;

COMMIT;
