-- LLM-49: correct Notice Board content-capacity tags to match the sprite art.
--
-- The Notice Board asset (df06e1c7-3912-4846-b828-6c0435af8c01) carries one
-- sprite frame per fill level. The content-capacity-N tags that tell the engine
-- how many notices each frame depicts were mismapped, so the crier drew an
-- unrelated number of slips for the notices she posted (a board the engine
-- "emptied" rendered a full 5-slip frame). Actual slip counts per state (Jeff,
-- 2026-06-20): variant-1=5, variant-2=3, variant-3=2, variant-4=0 (empty),
-- variant-5=4. variant-3 (capacity-2) and variant-5 (capacity-4) were already
-- correct and are left untouched.
--
-- Keyed by asset_id + state name so it applies regardless of per-environment
-- asset_state ids.
BEGIN;

-- variant-1 (5 slips): previously untagged, which the engine reads as the empty
-- board. Tag it as the full, 5-notice frame.
INSERT INTO asset_state_tag (state_id, tag)
SELECT id, 'content-capacity-5'
FROM asset_state
WHERE asset_id = 'df06e1c7-3912-4846-b828-6c0435af8c01' AND state = 'variant-1'
ON CONFLICT (state_id, tag) DO NOTHING;

-- variant-2 (3 slips): content-capacity-1 -> content-capacity-3.
UPDATE asset_state_tag
SET tag = 'content-capacity-3'
WHERE tag = 'content-capacity-1'
  AND state_id IN (
    SELECT id FROM asset_state
    WHERE asset_id = 'df06e1c7-3912-4846-b828-6c0435af8c01' AND state = 'variant-2'
  );

-- variant-4 (empty): drop content-capacity-3 so it reads as the empty board (0).
DELETE FROM asset_state_tag
WHERE tag = 'content-capacity-3'
  AND state_id IN (
    SELECT id FROM asset_state
    WHERE asset_id = 'df06e1c7-3912-4846-b828-6c0435af8c01' AND state = 'variant-4'
  );

COMMIT;
