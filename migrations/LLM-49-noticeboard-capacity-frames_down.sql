-- Revert LLM-49: restore the original (mismapped) Notice Board capacity tags.
-- Resets each touched state to EXACTLY its original tag (or none) via
-- delete-then-insert, mirroring the up migration's determinism.
BEGIN;

-- variant-1: -> untagged.
DELETE FROM asset_state_tag
WHERE tag LIKE 'content-capacity-%'
  AND state_id IN (
    SELECT id FROM asset_state
    WHERE asset_id = 'df06e1c7-3912-4846-b828-6c0435af8c01' AND state = 'variant-1'
  );

-- variant-2: -> content-capacity-1.
DELETE FROM asset_state_tag
WHERE tag LIKE 'content-capacity-%'
  AND state_id IN (
    SELECT id FROM asset_state
    WHERE asset_id = 'df06e1c7-3912-4846-b828-6c0435af8c01' AND state = 'variant-2'
  );
INSERT INTO asset_state_tag (state_id, tag)
SELECT id, 'content-capacity-1'
FROM asset_state
WHERE asset_id = 'df06e1c7-3912-4846-b828-6c0435af8c01' AND state = 'variant-2'
ON CONFLICT (state_id, tag) DO NOTHING;

-- variant-4: -> content-capacity-3.
DELETE FROM asset_state_tag
WHERE tag LIKE 'content-capacity-%'
  AND state_id IN (
    SELECT id FROM asset_state
    WHERE asset_id = 'df06e1c7-3912-4846-b828-6c0435af8c01' AND state = 'variant-4'
  );
INSERT INTO asset_state_tag (state_id, tag)
SELECT id, 'content-capacity-3'
FROM asset_state
WHERE asset_id = 'df06e1c7-3912-4846-b828-6c0435af8c01' AND state = 'variant-4'
ON CONFLICT (state_id, tag) DO NOTHING;

COMMIT;
