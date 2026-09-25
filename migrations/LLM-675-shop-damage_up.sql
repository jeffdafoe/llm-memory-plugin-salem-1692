-- LLM-675: public works slice 2 — business damage.
--
-- An owned business can now be damaged (engine damage.go: a hazard roll at the
-- daily boundary and at each storm start, scaled by the shop's stall wear).
-- While damaged it takes the degrade effect — no shelf stock in, production
-- slowed — until a workless hand mends it for a bounty the town chest pays.
-- The damage state itself rides the LLM-654 columns (village_object.damaged_at);
-- nothing new on village_object.
--
-- WHAT this adds:
--   1. The "Debris" asset the engine hangs on a damaged business as an overlay
--      (attached_to = the business) and removes on repair. Two states name the
--      cause, so the wording survives a restart with the placement:
--        worn  — a board fallen flat before one still leaning (daily damage)
--        storm — two fallen branches beside a leaning board (storm damage)
--      One 64x32 sheet, two 32x32 frames, composited from Mana Seed art the
--      catalog already licenses: the leaning boards of "village accessories
--      16x32.png" (cell 3,1) and the fallen branches of "summer 16x16.png"
--      (cells 3,1 and 4,1). Category "debris" — new, so a hand-placed copy in
--      the editor is at least filed apart from ordinary props.
--   2. well_damage_use_reference 60 -> 120. The well hazard now counts units of
--      water drawn (a drink 1, a pail its size) instead of trips; 60 was sized
--      for trips. Only a row still at the old default moves — a value the
--      operator set by hand is left alone.
--
-- THIS MIGRATION ALONE DOES NOT DRAW THE DEBRIS. The Mana Seed sheets are
-- gitignored and travel by scp: public-works-debris.png must be at
-- /var/www/llm-memory-salem-1692/tilesets/mana-seed/village-accessories/
-- (owner www-data) or a damaged shop's overlay loads with a broken texture.
-- The engine still damages and repairs without it.
--
-- asset / asset_state are reference data (load-only, boot-loaded). setting is
-- read at boot. deploy.sh runs stop -> migrate -> start, so both land on the
-- restart.
--
-- The asset UUID is fixed — the engine names it (sim.DebrisAssetID) and the
-- _down finds it again. Suffix is <ticket><ordinal>; reserved.
--
-- Rerun-safe: asset and states NOT EXISTS / ON CONFLICT guarded; the setting
-- update is conditional. Loud validation at the end.

BEGIN;

-- The mana-seed pack row predates this migration in every live catalog and is
-- not touched here: asset.pack_id has no FK, and the pg integration harness
-- (schema.sql replay, no rows) seeds its own pack rows per test.
INSERT INTO asset (
    id, name, category, default_state, anchor_x, anchor_y, layer, pack_id,
    z_index, is_obstacle, source_file)
VALUES (
    '019e5f00-c401-7a10-9e00-000000675001', 'Debris', 'debris', 'worn', 0.5, 0.85, 'objects', 'mana-seed',
    10, false, 'composite: village accessories 16x32.png (3,1) + summer 16x16.png (3,1),(4,1)')
ON CONFLICT (id) DO NOTHING;

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT '019e5f00-c401-7a10-9e00-000000675001', v.state,
       '/tilesets/mana-seed/village-accessories/public-works-debris.png', v.src_x, 0, 32, 32, 1, 0
  FROM (VALUES ('worn', 0), ('storm', 32)) AS v(state, src_x)
 WHERE NOT EXISTS (SELECT 1 FROM asset_state s
                    WHERE s.asset_id = '019e5f00-c401-7a10-9e00-000000675001'
                      AND s.state = v.state);

UPDATE setting SET value = '120'
 WHERE key = 'well_damage_use_reference' AND value = '60';

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM asset
                    WHERE id = '019e5f00-c401-7a10-9e00-000000675001'
                      AND pack_id = 'mana-seed' AND default_state = 'worn') THEN
        RAISE EXCEPTION 'LLM-675: the Debris asset is missing or not the row this migration writes';
    END IF;
    IF (SELECT count(*) FROM asset_state
         WHERE asset_id = '019e5f00-c401-7a10-9e00-000000675001'
           AND state IN ('worn', 'storm')) <> 2 THEN
        RAISE EXCEPTION 'LLM-675: the Debris asset does not carry both worn and storm states';
    END IF;
END $$;

COMMIT;
