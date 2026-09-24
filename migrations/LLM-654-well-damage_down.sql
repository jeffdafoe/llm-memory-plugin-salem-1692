-- LLM-654 rollback: drop the damage columns and the damaged well states.
--
-- Engine-STOPPED like the up (deploy.sh stop -> migrate -> start). A well that
-- was damaged at rollback is set back to its default state first, so no
-- placement is left pointing at a state that no longer exists.

BEGIN;

UPDATE village_object
   SET current_state = 'default'
 WHERE current_state = 'damaged'
   AND asset_id IN ('afc2110d-e882-461b-91c5-abb363a76476',
                    '64b37a3f-8b77-4083-bee9-37fe197b8270');

DELETE FROM asset_state_tag
 WHERE state_id IN (SELECT id FROM asset_state
                     WHERE state = 'damaged'
                       AND asset_id IN ('afc2110d-e882-461b-91c5-abb363a76476',
                                        '64b37a3f-8b77-4083-bee9-37fe197b8270'));

DELETE FROM asset_state
 WHERE state = 'damaged'
   AND asset_id IN ('afc2110d-e882-461b-91c5-abb363a76476',
                    '64b37a3f-8b77-4083-bee9-37fe197b8270');

ALTER TABLE village_object DROP COLUMN IF EXISTS use_since_repair;
ALTER TABLE village_object DROP COLUMN IF EXISTS damaged_at;

COMMIT;
