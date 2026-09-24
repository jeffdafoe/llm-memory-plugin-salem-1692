-- LLM-654: damage events + public works, slice 1 (wells).
--
-- A well can break (engine damage.go: a hazard roll at the daily boundary and
-- at each storm start, scaled by the draws since its last repair). A broken
-- well is out of use — no drink, no water — until a workless hand mends it for
-- a bounty the town chest pays.
--
-- WHAT this adds:
--   1. village_object.damaged_at — when the object broke; NULL = sound.
--      Checkpointed, so a restart never quietly mends a broken well.
--   2. village_object.use_since_repair — draws since the last repair, the
--      use factor of the hazard. Checkpointed so the hazard survives deploys.
--   3. A `damaged` asset state (tagged `damaged`) on the two live well assets,
--      Well (Bucket) and Well (Roofed): the open stone ring with the windlass
--      gone — frame (0,0) of the same village-accessories sheet, the art the
--      catalog already carries as "Well (Empty)". The engine resolves the state
--      by TAG (Asset.StateForTag), so the name is free.
--
-- Settings need no rows here: every new key falls back to its engine default
-- (damage.go) until set through the umbilical.
--
-- ENGINE-OWNED tables; deploy.sh runs stop -> migrate -> start, so this
-- applies engine-STOPPED. Asset states are reference data read at boot.
--
-- Rerun-safe: ADD COLUMN IF NOT EXISTS; state rows NOT EXISTS-guarded; tag
-- rows ON CONFLICT DO NOTHING. Loud validation at the end.

BEGIN;

-- 1-2. The damage columns.
ALTER TABLE village_object ADD COLUMN IF NOT EXISTS damaged_at timestamptz NULL;
ALTER TABLE village_object ADD COLUMN IF NOT EXISTS use_since_repair integer NOT NULL DEFAULT 0;

-- 3. The damaged state on each live well asset, copying the sheet from the
-- asset's own default state so the frame lands on the same image.
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT s.asset_id, 'damaged', s.sheet, 0, 0, 48, 80, 1, 0
  FROM asset_state s
 WHERE s.asset_id IN ('afc2110d-e882-461b-91c5-abb363a76476',  -- Well (Bucket)
                      '64b37a3f-8b77-4083-bee9-37fe197b8270')  -- Well (Roofed)
   AND s.state = 'default'
   AND NOT EXISTS (SELECT 1 FROM asset_state d
                    WHERE d.asset_id = s.asset_id AND d.state = 'damaged');

INSERT INTO asset_state_tag (state_id, tag)
SELECT s.id, 'damaged'
  FROM asset_state s
 WHERE s.asset_id IN ('afc2110d-e882-461b-91c5-abb363a76476',
                      '64b37a3f-8b77-4083-bee9-37fe197b8270')
   AND s.state = 'damaged'
ON CONFLICT DO NOTHING;

-- Validate loud. The columns always land; the asset states are asserted only
-- where the well assets exist (a schema-only harness has no catalog rows).
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns
                    WHERE table_name = 'village_object' AND column_name = 'damaged_at') THEN
        RAISE EXCEPTION 'LLM-654: village_object.damaged_at missing after ALTER';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM information_schema.columns
                    WHERE table_name = 'village_object' AND column_name = 'use_since_repair') THEN
        RAISE EXCEPTION 'LLM-654: village_object.use_since_repair missing after ALTER';
    END IF;
    IF EXISTS (SELECT 1 FROM asset_state
                WHERE asset_id IN ('afc2110d-e882-461b-91c5-abb363a76476',
                                   '64b37a3f-8b77-4083-bee9-37fe197b8270')
                  AND state = 'default'
                  AND asset_id NOT IN (SELECT d.asset_id FROM asset_state d
                                        JOIN asset_state_tag t ON t.state_id = d.id
                                       WHERE d.state = 'damaged' AND t.tag = 'damaged')) THEN
        RAISE EXCEPTION 'LLM-654: a well asset has no tagged damaged state after insert';
    END IF;
END $$;

COMMIT;
