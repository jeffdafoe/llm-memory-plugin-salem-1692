-- LLM-798: every player carries a tent — pitched on grass it is their camp and
-- their home until they strike it again.
--
-- WHAT THIS DOES:
--   1. Four camp assets with fixed ids (sim.TentAssetID … CampWoodpileAssetID),
--      drawn from the Mana Seed "Traveler's Camp 2.0" sheets already served
--      under /tilesets/mana-seed/travelers-camp/:
--        Tent           structure, obstacle, footprint L1 R2 T3 B0, door at the
--                       anchor (0,0). States: packed (the bundle — the pack's
--                       sack, 16x16), closed (64,0) and open (0,0) on the 64x64
--                       sheet. open is tagged occupied and closed unoccupied, so
--                       the occupancy reactor opens the flap while someone is
--                       inside and awake.
--        Camp Fire      obstacle. unlit (0,0), burning 4 frames from (32,0),
--                       ashes (0,32), dying 4 frames from (32,32) on the 32x32
--                       sheet. The engine turns it by the clock (UpdateCampFires).
--        Camp Stool     the pack's squat log, 16x16 (0,0).
--        Camp Woodpile  the pack's firewood, 16x16 (16,0).
--      The older hand-made camp assets (Tent (Open), Campfire, …) stay as the
--      scenery camps use them.
--   2. item_kind `tent`, capability `shelter`: never paid, bartered or given
--      (sim.CapabilityShelter). No item_recipe row, so it has no price and no
--      keeper's buy cue names it.
--   3. One tent in the pack of every existing player (actor.login_username set).
--      New players get theirs from CreatePC.
--
-- The engine runs migrations stopped (deploy.sh); actor_inventory and the
-- catalog are both loaded at boot. Rerun-safe: fixed ids with ON CONFLICT, the
-- tent grant DO NOTHING (never overwrites a live, checkpoint-owned count).

BEGIN;

CREATE TEMP TABLE llm798_asset (
    id uuid PRIMARY KEY, name text, category text, default_state text, is_obstacle boolean,
    fp_left int, fp_right int, fp_top int, fp_bottom int, door_x int, door_y int
) ON COMMIT DROP;
INSERT INTO llm798_asset VALUES
    ('019e5f00-c401-7a10-9e00-000000798001', 'Tent',          'structure', 'closed',  true,  1, 2, 3, 0, 0,    0),
    ('019e5f00-c401-7a10-9e00-000000798002', 'Camp Fire',     'camp',      'unlit',   true,  0, 0, 0, 0, NULL, NULL),
    ('019e5f00-c401-7a10-9e00-000000798003', 'Camp Stool',    'camp',      'default', false, 0, 0, 0, 0, NULL, NULL),
    ('019e5f00-c401-7a10-9e00-000000798004', 'Camp Woodpile', 'camp',      'default', false, 0, 0, 0, 0, NULL, NULL);

INSERT INTO asset (
    id, name, category, default_state, anchor_x, anchor_y, layer, pack_id,
    z_index, is_obstacle, footprint_left, footprint_right, footprint_top, footprint_bottom,
    door_offset_x, door_offset_y, visible_when_inside, occupied_min_count, occupied_night_only,
    render_scale, source_file)
SELECT id, name, category, default_state, 0.5, 0.85, 'objects', 'mana-seed-camp',
       10, is_obstacle, fp_left, fp_right, fp_top, fp_bottom,
       door_x, door_y, false, 1, false,
       2.0, 'Mana Seed Traveler''s Camp 2.0'
  FROM llm798_asset
ON CONFLICT (id) DO UPDATE SET
    name = EXCLUDED.name, category = EXCLUDED.category, default_state = EXCLUDED.default_state,
    is_obstacle = EXCLUDED.is_obstacle,
    footprint_left = EXCLUDED.footprint_left, footprint_right = EXCLUDED.footprint_right,
    footprint_top = EXCLUDED.footprint_top, footprint_bottom = EXCLUDED.footprint_bottom,
    door_offset_x = EXCLUDED.door_offset_x, door_offset_y = EXCLUDED.door_offset_y;

CREATE TEMP TABLE llm798_state (
    asset_id uuid, state text, sheet text, src_x int, src_y int, src_w int, src_h int,
    frame_count int, frame_rate double precision, tag text
) ON COMMIT DROP;
INSERT INTO llm798_state VALUES
    ('019e5f00-c401-7a10-9e00-000000798001', 'packed',  '/tilesets/mana-seed/travelers-camp/travelers camp tent 16x16.png', 32,  0, 16, 16, 1, 0, NULL),
    ('019e5f00-c401-7a10-9e00-000000798001', 'closed',  '/tilesets/mana-seed/travelers-camp/travelers camp tent 64x64.png', 64,  0, 64, 64, 1, 0, 'unoccupied'),
    ('019e5f00-c401-7a10-9e00-000000798001', 'open',    '/tilesets/mana-seed/travelers-camp/travelers camp tent 64x64.png',  0,  0, 64, 64, 1, 0, 'occupied'),
    ('019e5f00-c401-7a10-9e00-000000798002', 'unlit',   '/tilesets/mana-seed/travelers-camp/travelers camp 32x32.png',        0,  0, 32, 32, 1, 0, NULL),
    ('019e5f00-c401-7a10-9e00-000000798002', 'burning', '/tilesets/mana-seed/travelers-camp/travelers camp 32x32.png',       32,  0, 32, 32, 4, 8, NULL),
    ('019e5f00-c401-7a10-9e00-000000798002', 'dying',   '/tilesets/mana-seed/travelers-camp/travelers camp 32x32.png',       32, 32, 32, 32, 4, 6, NULL),
    ('019e5f00-c401-7a10-9e00-000000798002', 'ashes',   '/tilesets/mana-seed/travelers-camp/travelers camp 32x32.png',        0, 32, 32, 32, 1, 0, NULL),
    ('019e5f00-c401-7a10-9e00-000000798003', 'default', '/tilesets/mana-seed/travelers-camp/travelers camp tent 16x16.png',  0,  0, 16, 16, 1, 0, NULL),
    ('019e5f00-c401-7a10-9e00-000000798004', 'default', '/tilesets/mana-seed/travelers-camp/travelers camp tent 16x16.png', 16,  0, 16, 16, 1, 0, NULL);

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate
  FROM llm798_state
ON CONFLICT (asset_id, state) DO UPDATE SET
    sheet = EXCLUDED.sheet, src_x = EXCLUDED.src_x, src_y = EXCLUDED.src_y,
    src_w = EXCLUDED.src_w, src_h = EXCLUDED.src_h,
    frame_count = EXCLUDED.frame_count, frame_rate = EXCLUDED.frame_rate;

INSERT INTO asset_state_tag (state_id, tag)
SELECT s.id, t.tag
  FROM llm798_state t
  JOIN asset_state s ON s.asset_id = t.asset_id AND s.state = t.state
 WHERE t.tag IS NOT NULL
   AND NOT EXISTS (SELECT 1 FROM asset_state_tag x WHERE x.state_id = s.id AND x.tag = t.tag);

-- ON CONFLICT DO UPDATE promotes an engine-minted discovery row of the same
-- name to the real good, as LLM-710 / LLM-799.
INSERT INTO item_kind
    (name, display_label, display_label_singular, display_label_plural,
     category, sort_order, capabilities, description)
VALUES
    ('tent', 'Tent', 'tent', 'tents', 'gear', 400, '{shelter}'::text[],
     'A canvas tent with its pegs, and a bedroll rolled up inside — a camp wherever there is open grass.')
ON CONFLICT (name) DO UPDATE SET
    display_label          = EXCLUDED.display_label,
    display_label_singular = EXCLUDED.display_label_singular,
    display_label_plural   = EXCLUDED.display_label_plural,
    category               = EXCLUDED.category,
    sort_order             = EXCLUDED.sort_order,
    capabilities           = EXCLUDED.capabilities,
    description            = EXCLUDED.description;

INSERT INTO actor_inventory (actor_id, item_kind, quantity)
SELECT id, 'tent', 1
  FROM actor
 WHERE login_username IS NOT NULL AND login_username <> ''
ON CONFLICT (actor_id, item_kind) DO NOTHING;

DO $$
BEGIN
    IF (SELECT count(*) FROM asset a JOIN llm798_asset w ON w.id = a.id
         WHERE a.category = w.category AND a.default_state = w.default_state
           AND a.is_obstacle = w.is_obstacle
           AND a.footprint_left = w.fp_left AND a.footprint_right = w.fp_right
           AND a.footprint_top = w.fp_top AND a.footprint_bottom = w.fp_bottom
           AND a.door_offset_x IS NOT DISTINCT FROM w.door_x
           AND a.door_offset_y IS NOT DISTINCT FROM w.door_y) <> 4 THEN
        RAISE EXCEPTION 'LLM-798: the four camp assets are not exactly as written';
    END IF;
    IF (SELECT count(*) FROM asset_state s JOIN llm798_state t
          ON t.asset_id = s.asset_id AND t.state = s.state
         WHERE s.sheet = t.sheet AND s.src_x = t.src_x AND s.src_y = t.src_y
           AND s.src_w = t.src_w AND s.src_h = t.src_h AND s.frame_count = t.frame_count) <> 9 THEN
        RAISE EXCEPTION 'LLM-798: the camp asset states are not exactly as written';
    END IF;
    IF (SELECT count(*) FROM asset_state_tag x JOIN asset_state s ON s.id = x.state_id
         WHERE s.asset_id = '019e5f00-c401-7a10-9e00-000000798001'
           AND ((s.state = 'open' AND x.tag = 'occupied') OR (s.state = 'closed' AND x.tag = 'unoccupied'))) <> 2 THEN
        RAISE EXCEPTION 'LLM-798: the tent''s open/closed states lack their occupancy tags';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM item_kind WHERE name = 'tent' AND capabilities = '{shelter}'::text[]) THEN
        RAISE EXCEPTION 'LLM-798: item_kind tent is missing or not a shelter';
    END IF;
    IF EXISTS (SELECT 1 FROM actor a
                WHERE a.login_username IS NOT NULL AND a.login_username <> ''
                  AND NOT EXISTS (SELECT 1 FROM actor_inventory i
                                   WHERE i.actor_id = a.id AND i.item_kind = 'tent')) THEN
        RAISE EXCEPTION 'LLM-798: a player was left without a tent';
    END IF;
END $$;

COMMIT;
