-- LLM-60 down: revert the blueberry bushes to nameless (their pre-LLM-60 state).
-- Only the rows this migration named ("Blueberry Bush" on the blueberry asset)
-- are reverted; a differently-named blueberry instance is left alone. Engine-owned
-- table -- apply with the engine STOPPED.

BEGIN;

UPDATE village_object
SET display_name = NULL
WHERE asset_id = '630909ca-df4f-43ac-9fc4-5192ca44da73'
  AND display_name = 'Blueberry Bush';

COMMIT;
