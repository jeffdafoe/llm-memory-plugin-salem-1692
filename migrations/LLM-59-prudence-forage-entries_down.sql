-- Revert LLM-59: clear Prudence Ward's herbalist forage restock entries, back to
-- the empty params the role carried before. Apply with the engine STOPPED, same
-- as the up-migration (checkpoint-written actor_attribute).

BEGIN;

-- Exact-match guard: only reset if params still equals what the up-migration
-- set, so a later change to her herbalist params isn't erased by this revert.
UPDATE actor_attribute
SET params = '{}'::jsonb
WHERE actor_id = '019dbcec-1149-7149-8a49-2cdb54680b86'  -- Prudence Ward
  AND slug = 'herbalist'
  AND params = '{"restock": [{"item": "raspberries", "source": "forage", "max": 10}, {"item": "blueberries", "source": "forage", "max": 10}]}'::jsonb;

COMMIT;
