-- LLM-1005 down: drop actor.farm_tools_worn_qty / farm_tools_worn_at. Same
-- engine-stopped caveat as the up.

BEGIN;

ALTER TABLE actor DROP COLUMN IF EXISTS farm_tools_worn_at;
ALTER TABLE actor DROP COLUMN IF EXISTS farm_tools_worn_qty;

COMMIT;
