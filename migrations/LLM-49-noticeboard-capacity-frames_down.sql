-- Revert LLM-49: restore the original (mismapped) Notice Board capacity tags.
BEGIN;

-- variant-1: remove content-capacity-5 (back to untagged).
DELETE FROM asset_state_tag
WHERE tag = 'content-capacity-5'
  AND state_id IN (
    SELECT id FROM asset_state
    WHERE asset_id = 'df06e1c7-3912-4846-b828-6c0435af8c01' AND state = 'variant-1'
  );

-- variant-2: content-capacity-3 -> content-capacity-1.
UPDATE asset_state_tag
SET tag = 'content-capacity-1'
WHERE tag = 'content-capacity-3'
  AND state_id IN (
    SELECT id FROM asset_state
    WHERE asset_id = 'df06e1c7-3912-4846-b828-6c0435af8c01' AND state = 'variant-2'
  );

-- variant-4: restore content-capacity-3.
INSERT INTO asset_state_tag (state_id, tag)
SELECT id, 'content-capacity-3'
FROM asset_state
WHERE asset_id = 'df06e1c7-3912-4846-b828-6c0435af8c01' AND state = 'variant-4'
ON CONFLICT (state_id, tag) DO NOTHING;

COMMIT;
