-- LLM-600: a school of fish for the village water.
--
-- Two placeable assets off the Mana Seed "Fishing Gear 2.0" pack, each a
-- 4-frame animation that loops in place. The school shimmers where it is
-- dropped; it does not roam the pond. A roaming version would need a decorative
-- actor and a movement predicate that admits water tiles, which the engine does
-- not have -- waterfowlShore is explicitly the NON-water band, which is why
-- ducks stay on the bank.
--
-- WHY TWO ROWS AND NOT FOURTEEN, OR ONE. The pack ships 14 files and all 14 are
-- pixel-identical in silhouette -- hashing each file's alpha mask yields one
-- distinct hash across the whole set. The vN suffixes are colourways, nothing
-- more, so importing all 14 would be the same school fourteen times. Two is
-- what makes a pond with several schools in it read as different fish rather
-- than one sprite repeated.
--
-- WHY THESE TWO COLOURWAYS. Chosen on measured luminance contrast against the
-- water we actually render (wang.png is dominated by #001C49/#002A52/#003E5C),
-- not on preference; full ranking and method are on LLM-600. desert v1 (cyan,
-- 4.07) and muddy v1 (green, 3.23) both score high AND differ from each other,
-- so two schools stay distinguishable. The ranking matters mainly for what it
-- rules out: 'summer' is not merely subtle, its two colours ARE water colours
-- two and three byte-for-byte, so those fish are perfectly invisible; 'black'
-- (1.04) reads as a hole in the pond.
--
-- WHY frame_rate 3 AND NOT 8. Every other animated asset in the catalog runs at
-- 8, but those are a campfire, a torch, and wind-blown trees. Fire flickers;
-- fish drift. At 8 the school reads as agitated. Below 3 the four frames become
-- visible as steps. frame_rate is double precision, so this is free to tune
-- live once it is in the water.
--
-- WHY anchor 0.5/0.5 RATHER THAN THE USUAL 0.85. The 0.85 default puts the
-- world position near the sprite's bottom edge to simulate ground contact,
-- which is right for anything standing up. A school of fish lies flat in the
-- water and has no contact point, so the anchor belongs at the centre of the
-- tile. Water Rock uses 0.7 because a rock protrudes.
--
-- asset / asset_state are REFERENCE data: load-only, no snapshot_gen, never
-- checkpoint-clobbered by the engine, so this needs no engine stop. The catalog
-- IS boot-loaded, so the rows do not render until the next restart -- which the
-- deploy performs anyway (stop -> migrate -> start).
--
-- THIS MIGRATION ALONE DOES NOT MAKE THE FISH APPEAR. The live client fetches
-- sheets over HTTP from nginx and the Mana Seed PNGs are gitignored, so they
-- travel by scp, not by deploy. Both files must be at
-- /var/www/llm-memory-salem-1692/tilesets/mana-seed/fishing-gear-2/ (owner
-- www-data) or the asset loads with a broken texture.
--
-- THE FILES ARE RENAMED ON THE WAY IN, dropping the pack's original
-- "school of fish, desert v1 32x32.png" for "school-of-fish-desert-v1.png".
-- source_file below keeps the original name so provenance is not lost. The
-- reason is the COMMA: catalog.gd:145 builds the request as a raw string concat
-- (api_base + sheet_path) with no percent-encoding, so whatever is in this
-- column goes on the wire as-is. Spaces in a sheet path are proven safe -- 312
-- placed wheat plants and 80 blueberry bushes render off spaced paths today --
-- but of the 65 catalog rows whose sheet contains a comma, NOT ONE is placed,
-- so the comma has never actually been exercised at render time. Renaming costs
-- nothing; being the first to find out does not.

BEGIN;

INSERT INTO tileset_pack (id, name, url)
VALUES ('mana-seed-fishing', 'Fishing Gear', 'https://seliel-the-shaper.itch.io/mana-seed')
ON CONFLICT (id) DO NOTHING;

-- DO NOTHING is silent about WHY it did nothing, so assert the row we ended up
-- with is the row we meant. No such pack exists in the live catalog today, but
-- that is a snapshot, not an invariant -- a restored backup or a pre-seeded
-- environment could hold an incompatible row of the same id, and without this
-- the two assets would attach to it and mislabel themselves in the editor
-- sidebar while still rendering fine, which is the kind of wrong that goes
-- unnoticed.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM tileset_pack
         WHERE id = 'mana-seed-fishing'
           AND (name, url) IS DISTINCT FROM
               ('Fishing Gear', 'https://seliel-the-shaper.itch.io/mana-seed')
    ) THEN
        RAISE EXCEPTION
            'LLM-600: a tileset_pack row with id mana-seed-fishing already exists with different metadata; resolve it before applying';
    END IF;
END $$;

-- Fixed UUIDs rather than gen_random_uuid(): asset_state below references them
-- and the _down needs to find them again. Suffix is <ticket><ordinal>.
INSERT INTO asset (
    id, name, category, default_state, anchor_x, anchor_y, layer, pack_id,
    z_index, is_obstacle, source_file)
VALUES
    ('019e5f00-c401-7a10-9e00-000000600001',
     'School of Fish (cyan)', 'water-features', 'default',
     0.5, 0.5, 'objects', 'mana-seed-fishing',
     -- z_index 1 is the GROUND-OVERLAY band, not an arbitrary sort key.
     -- world.gd:79-84 defines exactly two: terrain draws at 0, ground overlays
     -- ("bridges, future road decals") at 1, and everything else -- objects and
     -- NPCs alike -- at OBJECT_Z = 10. The live catalog holds only those two
     -- values, 1 for Bridge and 10 for the other 191 assets. A school lying
     -- flat on the water surface is a ground decal in exactly the Bridge sense,
     -- so it belongs in that band. An intermediate value would be outside the
     -- model the client documents rather than a finer-grained ordering.
     1,
     -- Fish do not block. Water is unwalkable anyway, so this is moot today; it
     -- is set correctly so it stays right if a shallow-water tile ever becomes
     -- passable.
     false,
     'school of fish, desert v1 32x32.png'),
    ('019e5f00-c401-7a10-9e00-000000600002',
     'School of Fish (green)', 'water-features', 'default',
     0.5, 0.5, 'objects', 'mana-seed-fishing',
     1, false,
     'school of fish, muddy v1 32x32.png');

-- These two UUIDs are permanently reserved for these assets. The _down deletes
-- by id alone, deliberately: asset.name is editable from the village editor, so
-- guarding the delete on it would make a rollback silently no-op after someone
-- renames a school. Never re-point either id at a different asset.

-- 128x32 sheets: four 32x32 frames laid out consecutively along x, which is
-- exactly the layout Catalog.get_sprite_frames() walks (src_x + N*src_w). No
-- re-cutting needed, and the frames must NOT be cropped tighter -- the fish are
-- distributed across the full tile and relocate between frames, so no
-- sub-rectangle holds the same fish in all four.
INSERT INTO asset_state (
    asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
VALUES
    ('019e5f00-c401-7a10-9e00-000000600001', 'default',
     '/tilesets/mana-seed/fishing-gear-2/school-of-fish-desert-v1.png',
     0, 0, 32, 32, 4, 3),
    ('019e5f00-c401-7a10-9e00-000000600002', 'default',
     '/tilesets/mana-seed/fishing-gear-2/school-of-fish-muddy-v1.png',
     0, 0, 32, 32, 4, 3);

COMMIT;
