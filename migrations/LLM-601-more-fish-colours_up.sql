-- LLM-601: three more school-of-fish colourways.
--
-- Follow-up to LLM-600, which shipped desert v1 (cyan) and muddy v1 (green).
-- Both are placed in the pond and read correctly, so this widens the palette.
-- Same shape as LLM-600 in every respect -- the pack's 14 files are
-- pixel-identical in silhouette (one alpha-mask hash across the set), so only
-- the sheet path differs from the two already live.
--
-- WHICH THREE. Scores are luminance contrast against the dominant water colour
-- #001C49:
--
--     muddy v2       3.73   steel blue    <- added
--     desert v2      3.26   sage green    <- added
--     spring         3.04   teal          -- too close to the live desert v1
--     rainforest v3  2.78   brown         <- added
--     autumn         2.33   olive         -- dropped, see below
--
-- The first draft took autumn over desert v2 on hue separation, reasoning that
-- a second green reads as the same school twice. Rendered over real water tiles
-- that was the wrong trade: autumn is the lowest scorer of the candidates and
-- reads as a shadow rather than as fish. Jeff's call was to take the legibility
-- over the hue variety, so desert v2 is in and autumn is out. It is a genuinely
-- different green from muddy v1 -- sage (416B58/4E7A69) against deep green-teal
-- (035141/337F75) -- just a nearer neighbour than the others.
--
-- spring stays out: a second cyan beside the live desert v1 buys nothing.
--
-- DELIBERATELY EXCLUDED, so nobody re-derives the question:
--   summer (1.68) -- its two colours are #002A52 and #003E5C, byte-identical
--                    to water colours two and three. Perfectly invisible.
--   winter (1.68) -- near-invisible for the same reason.
--   black  (1.04) -- one flat tone; reads as a hole in the pond.
--   autumn (2.33) -- visible on paper, but over actual water it reads as a
--                    shadow. Dropped after looking at it, not after scoring it.
--   muddy v4 (3.45) -- the only saturated variant and perfectly visible, but a
--                    bright orange koi is an ornamental Asian carp in a 1692
--                    Massachusetts pond. Excluded on theme, not on legibility.
--
-- Every rendering field matches the LLM-600 pair on purpose: z_index 1 is the
-- GROUND-OVERLAY band (world.gd:79-84 -- terrain 0, overlays 1, everything else
-- OBJECT_Z 10; the catalog holds only those two values), anchor 0.5/0.5 because
-- a school lies flat and has no ground-contact point, and frame_rate 3 because
-- fish drift where the campfire's 8 flickers.
--
-- asset / asset_state are REFERENCE data -- load-only, no snapshot_gen, never
-- checkpoint-clobbered -- so this needs no engine stop. The catalog IS
-- boot-loaded, so the rows do not appear until the deploy restart. There is no
-- hot-reload: the SIGHUP path described in several source comments is not wired
-- (server.go:503), and SIGHUP is absent from signal.Notify, so sending it would
-- terminate the engine rather than reload anything.
--
-- THIS MIGRATION ALONE DOES NOT MAKE THE FISH APPEAR. The live client fetches
-- sheets over HTTP from nginx and the Mana Seed PNGs are gitignored, so they
-- travel by scp, not by deploy. All three files must be at
-- /var/www/llm-memory-salem-1692/tilesets/mana-seed/fishing-gear-2/ (owner
-- www-data). They are renamed on the way in to drop the pack's commas --
-- catalog.gd:145 concatenates the sheet path onto the base URL with no
-- percent-encoding, and while spaces are proven safe in production, of the
-- catalog rows whose sheet holds a comma not one has ever been placed, so that
-- path has never been exercised at render time. source_file keeps the original.

BEGIN;

-- The pack already exists from LLM-600; this is here so the migration is
-- self-contained against a fresh database.
INSERT INTO tileset_pack (id, name, url)
VALUES ('mana-seed-fishing', 'Fishing Gear', 'https://seliel-the-shaper.itch.io/mana-seed')
ON CONFLICT (id) DO NOTHING;

-- DO NOTHING is silent about WHY it did nothing, so assert the row we ended up
-- with is the row we meant. Written as a PRESENCE check rather than a
-- mismatch check so it covers absence too: an ON CONFLICT DO NOTHING can be
-- swallowed by a conflict on some other unique constraint, and a
-- mismatch-only test passes vacuously when there is no row at all, deferring
-- the failure to a less legible FK violation on the asset inserts below.
-- tileset_pack currently has only its primary key, so that path is not
-- reachable today -- this does not depend on it staying that way.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM tileset_pack
         WHERE id = 'mana-seed-fishing'
           AND name = 'Fishing Gear'
           AND url = 'https://seliel-the-shaper.itch.io/mana-seed'
    ) THEN
        RAISE EXCEPTION
            'LLM-601: expected mana-seed-fishing tileset_pack row is missing or has incompatible metadata';
    END IF;
END $$;

-- Fixed UUIDs, suffix <ticket><ordinal>, continuing LLM-600's 600001/600002.
-- Permanently reserved: the _down deletes by id alone, because asset.name is
-- editable from the village editor and guarding on it would make a rollback
-- silently no-op after a rename.
INSERT INTO asset (
    id, name, category, default_state, anchor_x, anchor_y, layer, pack_id,
    z_index, is_obstacle, source_file)
VALUES
    ('019e5f00-c401-7a10-9e00-000000601001',
     'School of Fish (blue)', 'water-features', 'default',
     0.5, 0.5, 'objects', 'mana-seed-fishing', 1, false,
     'school of fish, muddy v2 32x32.png'),
    ('019e5f00-c401-7a10-9e00-000000601002',
     'School of Fish (brown)', 'water-features', 'default',
     0.5, 0.5, 'objects', 'mana-seed-fishing', 1, false,
     'school of fish, rainforest v3 32x32.png'),
    ('019e5f00-c401-7a10-9e00-000000601003',
     'School of Fish (sage)', 'water-features', 'default',
     0.5, 0.5, 'objects', 'mana-seed-fishing', 1, false,
     'school of fish, desert v2 32x32.png');

-- 128x32 sheets: four 32x32 frames consecutive along x, the layout
-- Catalog.get_sprite_frames() walks (src_x + N*src_w). Not croppable tighter --
-- the fish are distributed across the whole tile and relocate between frames,
-- so no sub-rectangle holds the same fish in all four.
INSERT INTO asset_state (
    asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
VALUES
    ('019e5f00-c401-7a10-9e00-000000601001', 'default',
     '/tilesets/mana-seed/fishing-gear-2/school-of-fish-muddy-v2.png',
     0, 0, 32, 32, 4, 3),
    ('019e5f00-c401-7a10-9e00-000000601002', 'default',
     '/tilesets/mana-seed/fishing-gear-2/school-of-fish-rainforest-v3.png',
     0, 0, 32, 32, 4, 3),
    ('019e5f00-c401-7a10-9e00-000000601003', 'default',
     '/tilesets/mana-seed/fishing-gear-2/school-of-fish-desert-v2.png',
     0, 0, 32, 32, 4, 3);

COMMIT;
