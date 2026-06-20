-- LLM-49: correct Notice Board content-capacity tags to match the sprite art.
--
-- The Notice Board asset (df06e1c7-3912-4846-b828-6c0435af8c01) carries one
-- sprite frame per fill level. The content-capacity-N tags that tell the engine
-- how many notices each frame depicts were mismapped, so the crier drew an
-- unrelated number of slips for the notices she posted (a board the engine
-- "emptied" rendered a full 5-slip frame). Actual slip counts per state (Jeff,
-- 2026-06-20): variant-1=5, variant-2=3, variant-3=2, variant-4=0 (empty),
-- variant-5=4. variant-3 (capacity-2) and variant-5 (capacity-4) are already
-- correct and are left untouched.
--
-- Each touched state is reset to EXACTLY its intended capacity tag (or none)
-- via delete-then-insert, so the result is deterministic even on an environment
-- that drifted (stray or duplicate content-capacity tags). Keyed by asset_id +
-- state name so it applies regardless of per-environment asset_state ids.
BEGIN;

-- variant-1 (5 slips): -> exactly content-capacity-5 (was untagged, read as empty).
DELETE FROM asset_state_tag
WHERE tag LIKE 'content-capacity-%'
  AND state_id IN (
    SELECT id FROM asset_state
    WHERE asset_id = 'df06e1c7-3912-4846-b828-6c0435af8c01' AND state = 'variant-1'
  );
INSERT INTO asset_state_tag (state_id, tag)
SELECT id, 'content-capacity-5'
FROM asset_state
WHERE asset_id = 'df06e1c7-3912-4846-b828-6c0435af8c01' AND state = 'variant-1'
ON CONFLICT (state_id, tag) DO NOTHING;

-- variant-2 (3 slips): -> exactly content-capacity-3 (was content-capacity-1).
DELETE FROM asset_state_tag
WHERE tag LIKE 'content-capacity-%'
  AND state_id IN (
    SELECT id FROM asset_state
    WHERE asset_id = 'df06e1c7-3912-4846-b828-6c0435af8c01' AND state = 'variant-2'
  );
INSERT INTO asset_state_tag (state_id, tag)
SELECT id, 'content-capacity-3'
FROM asset_state
WHERE asset_id = 'df06e1c7-3912-4846-b828-6c0435af8c01' AND state = 'variant-2'
ON CONFLICT (state_id, tag) DO NOTHING;

-- variant-4 (empty): -> no capacity tag at all (was content-capacity-3).
DELETE FROM asset_state_tag
WHERE tag LIKE 'content-capacity-%'
  AND state_id IN (
    SELECT id FROM asset_state
    WHERE asset_id = 'df06e1c7-3912-4846-b828-6c0435af8c01' AND state = 'variant-4'
  );

COMMIT;
