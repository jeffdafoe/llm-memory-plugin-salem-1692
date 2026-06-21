-- Revert LLM-58b: remove the Blueberry Bush's berries/bare state tags.
-- (If the LLM-58 down also runs, deleting the 'bare' asset_state row cascades
-- its tag anyway via asset_state_tag.state_id FK ON DELETE CASCADE; this just
-- undoes LLM-58b on its own.)

BEGIN;

DELETE FROM asset_state_tag t
USING asset_state s
WHERE t.state_id = s.id
  AND s.asset_id = '630909ca-df4f-43ac-9fc4-5192ca44da73'
  AND s.state IN ('berries', 'bare')
  AND t.tag IN ('berries', 'bare');

COMMIT;
