-- LLM-968: drop the Mana Seed props, signs and gates the village does not
-- use, and the slots that only served attachments, before the rest is
-- repainted.
--
-- Jeff (2026-11-25): drop the unused ones; and an item is ONE complete sprite
-- (its board, banner or head painted in), not a post with pieces attached
-- through slots.
--
-- WHAT THIS DOES:
--   1. Deletes 45 assets: the 18 Shop Signs, the flower and small pots, the
--      potted cypress, barrel and crate stacks, open crate, the two buckets,
--      the small wood pile, log rack, Lamp Post (Banner), Lamp Post 3, the
--      hanging lantern, the four laundry lines, Outhouse 2, Well (Empty),
--      Wishing Well, the iron and low stone gates and the wrought iron door.
--      None is placed (village_object has no foreign key to asset, so a
--      placement would be left pointing at nothing), none hosts a slot or has
--      a refresh default, and the engine and client name none of them. The
--      editor palette is built from the catalog, so they leave it too.
--   2. Deletes the 'sign' slot of Wood Sign Post, Hanging Sign and Hanging
--      Banner (only the Shop Signs fitted it) and the Campfire's 'top' slot
--      (nothing fits it). The 'debris' slots stay: the engine places the storm
--      debris heap through them.
--   3. Clears the Torch's fits_slot 'torch': no asset has a torch slot.
--
-- The rows are checked against a snapshot of production (2026-11-25) before
-- they go, both ways, with their state tags and lights; any difference fails
-- the migration rather than lose something the down cannot bring back. The
-- state rows' tags and lights go with them (ON DELETE CASCADE).
--
-- A fresh database (schema.sql plus the migrations) has none of these rows;
-- then this does nothing.
--
-- ENGINE-OWNED data (the catalog), read at boot; deploy.sh runs stop ->
-- migrate -> start. Rerunnable: a second run finds nothing to delete.

BEGIN;

CREATE TEMP TABLE llm968_asset (id uuid, name text, category text, default_state text, anchor_x double precision, anchor_y double precision, layer text, created_at timestamp, pack_id text, interior boolean, fits_slot text, source_file text, rotation_algo text, transition_spread_seconds integer, is_obstacle boolean, is_passage boolean, z_index integer, footprint_left integer, footprint_right integer, footprint_top integer, footprint_bottom integer, door_offset_x integer, door_offset_y integer, visible_when_inside boolean, stand_offset_x integer, stand_offset_y integer, occupied_min_count integer, occupied_night_only boolean, render_scale double precision) ON COMMIT DROP;
INSERT INTO llm968_asset VALUES
    ('54912584-7e34-42d2-9c76-0c416d62528d', 'Barrel Stack', 'prop', 'default', 0.5, 0.9, 'objects', '2026-04-11 18:50:27', 'mana-seed', false, NULL, NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('6dbb582f-1bd2-4fd5-82a1-c70719443dfa', 'Bucket (Empty)', 'prop', 'default', 0.5, 0.85, 'objects', '2026-04-11 18:50:27', 'mana-seed', false, NULL, NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('23f2625e-4f8f-44bc-93d1-618d6e2816ba', 'Crate Stack', 'prop', 'default', 0.5, 0.9, 'objects', '2026-04-11 18:50:27', 'mana-seed', false, NULL, NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('55ee1ab5-50e3-475d-a85f-c06b5e82d072', 'Flower Pot (Empty)', 'prop', 'default', 0.5, 0.85, 'objects', '2026-04-11 18:50:27', 'mana-seed', false, NULL, NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('934ca72e-6435-4cca-bd13-6f575df50b1c', 'Flower Pot (Mixed)', 'prop', 'default', 0.5, 0.85, 'objects', '2026-04-11 18:50:27', 'mana-seed', false, NULL, NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('da1df5c4-7ac3-4ed7-9110-cddb8c35f619', 'Flower Pot (Red)', 'prop', 'default', 0.5, 0.85, 'objects', '2026-04-11 18:50:27', 'mana-seed', false, NULL, NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('eccf6f53-e588-4ff4-bf30-a99e7723110b', 'Flower Pot (Violet)', 'prop', 'default', 0.5, 0.85, 'objects', '2026-04-11 18:50:27', 'mana-seed', false, NULL, NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('b016c28b-323a-4851-b57d-c5c600aad85a', 'Hanging Lantern', 'prop', 'unlit', 0.5, 0.85, 'objects', '2026-04-17 17:52:17', 'mana-seed', false, NULL, 'village accessories 32x32.png', 'random_per_object', 120, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('145ab1e4-0179-41c2-95f6-21a1cb6080ab', 'Iron Stone Gate', 'fence', 'default', 0.5, 0.7, 'objects', '2026-04-11 18:50:27', 'mana-seed-fences', false, NULL, NULL, 'random_per_object', 0, true, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('7e8c4224-b816-4394-bb13-a3518f6c8b6a', 'Lamp Post (Banner)', 'prop', 'default', 0.5, 0.9, 'objects', '2026-04-11 14:12:42', 'mana-seed', false, NULL, NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('464a861d-2376-4203-b18f-7dce2b648d3c', 'Lamp Post 3', 'prop', 'unlit', 0.5, 0.85, 'objects', '2026-04-17 19:03:28', 'mana-seed', false, NULL, 'village accessories 48x80.png', 'random_per_object', 120, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('63fa2344-5423-416c-8555-5a2e20bfee9a', 'Laundry (Alley, Angle)', 'prop', 'empty', 0.5, 0.5, 'objects', '2026-04-17 19:13:10', 'mana-seed', false, NULL, 'village laundry, alley angle 48x64.png', 'random_per_object', 1800, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('69802b26-04cc-4b76-a379-79cb07f10aed', 'Laundry (Alley, Straight)', 'prop', 'empty', 0.5, 0.5, 'objects', '2026-04-17 19:13:10', 'mana-seed', false, NULL, 'village laundry, alley straight 64x48.png', 'random_per_object', 1800, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('ac595245-50a7-4911-a5ae-0dc44d5a5be9', 'Laundry (Post, Angle)', 'prop', 'empty', 0.5, 0.5, 'objects', '2026-04-17 19:13:10', 'mana-seed', false, NULL, 'village laundry, post angle 48x64.png', 'random_per_object', 1800, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('20734988-6ef3-40fe-a3df-bee0e353476d', 'Laundry (Post, Straight)', 'prop', 'empty', 0.5, 0.5, 'objects', '2026-04-17 19:13:10', 'mana-seed', false, NULL, 'village laundry, post straight 64x48.png', 'random_per_object', 1800, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('bcddc4e6-8e3f-442a-8059-c6c13596d0ae', 'Log Rack', 'prop', 'empty', 0.5, 0.85, 'objects', '2026-04-17 18:10:14', 'mana-seed', false, NULL, 'village accessories 32x32.png', 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('26d28f0e-6e36-46c3-96f0-1ebef67abb06', 'Low Stone Gate', 'fence', 'default', 0.5, 0.7, 'objects', '2026-04-11 18:50:27', 'mana-seed-fences', false, NULL, NULL, 'random_per_object', 0, true, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('7cc29114-d3ac-4d5f-b4c1-b7b8555e76fc', 'Open Crate', 'prop', 'default', 0.5, 0.85, 'objects', '2026-04-11 18:50:27', 'mana-seed', false, NULL, NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('650426a0-4fe5-46f6-95d2-90c7450afcf5', 'Outhouse 2', 'structure', 'default', 0.5, 0.85, 'objects', '2026-04-17 18:54:03', 'mana-seed', false, NULL, 'village accessories 32x64.png', 'random_per_object', 0, true, false, 10, 1, 0, 3, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('9e793549-e6de-4942-a5d7-3348f0d2e2a2', 'Potted Cypress', 'prop', 'default', 0.5, 0.9, 'objects', '2026-04-11 18:50:27', 'mana-seed', false, NULL, NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('d53b6706-73ed-4172-bfb9-2974c233141f', 'Shop Sign (Anvil)', 'sign', 'default', 0.5, 0.5, 'above', '2026-04-11 18:50:27', 'mana-seed', false, 'sign', NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('fa419ee9-0700-4018-9979-7e735746dfd3', 'Shop Sign (Barrel)', 'sign', 'default', 0.5, 0.5, 'above', '2026-04-11 18:50:27', 'mana-seed', false, 'sign', NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('90946fba-be74-44d9-97fd-4b19f4ea9121', 'Shop Sign (Blank)', 'sign', 'default', 0.5, 0.5, 'above', '2026-04-11 18:50:27', 'mana-seed', false, 'sign', NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('a9853a10-ac33-4733-b817-849ed0922f1d', 'Shop Sign (Bottle)', 'sign', 'default', 0.5, 0.5, 'above', '2026-04-11 18:50:27', 'mana-seed', false, 'sign', NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('9f63c3c7-a135-4588-8c1b-dd042a487404', 'Shop Sign (Bread)', 'sign', 'default', 0.5, 0.5, 'above', '2026-04-11 18:50:27', 'mana-seed', false, 'sign', NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('f47ae961-389f-4d58-b4ed-db0e3ede2720', 'Shop Sign (Crossed Tools)', 'sign', 'default', 0.5, 0.5, 'above', '2026-04-11 18:50:27', 'mana-seed', false, 'sign', NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('91f0aeff-8e01-4638-a6af-4313d0e44250', 'Shop Sign (Flower)', 'sign', 'default', 0.5, 0.5, 'above', '2026-04-11 18:50:27', 'mana-seed', false, 'sign', NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('6dedf58f-4a17-4f33-94eb-d43132e895ca', 'Shop Sign (Gem)', 'sign', 'default', 0.5, 0.5, 'above', '2026-04-11 18:50:27', 'mana-seed', false, 'sign', NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('d35eed79-a0f4-42f8-b840-2b75ce365a38', 'Shop Sign (Inn)', 'sign', 'default', 0.5, 0.5, 'above', '2026-04-11 18:50:27', 'mana-seed', false, 'sign', NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('de7f7c9d-7f5b-42cc-8cff-3859b93619d9', 'Shop Sign (Key)', 'sign', 'default', 0.5, 0.5, 'above', '2026-04-11 18:50:27', 'mana-seed', false, 'sign', NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('a1bbdfe5-d6e7-4812-a369-02ee57d36e03', 'Shop Sign (Percent)', 'sign', 'default', 0.5, 0.5, 'above', '2026-04-11 18:50:27', 'mana-seed', false, 'sign', NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('dabe7e31-16c0-47de-920d-cc8f50c94d2a', 'Shop Sign (Plank)', 'sign', 'default', 0.5, 0.5, 'above', '2026-04-11 18:50:27', 'mana-seed', false, 'sign', NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('2139ea76-4321-4b70-877f-ad3ed7708ff3', 'Shop Sign (Potion)', 'sign', 'default', 0.5, 0.5, 'above', '2026-04-11 18:50:27', 'mana-seed', false, 'sign', NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('9931fcfb-850a-4c75-8e81-be8c874d8237', 'Shop Sign (Scales)', 'sign', 'default', 0.5, 0.5, 'above', '2026-04-11 18:50:27', 'mana-seed', false, 'sign', NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('f34db5fd-50b6-4083-9c07-f0c08cdaa5b8', 'Shop Sign (Scroll)', 'sign', 'default', 0.5, 0.5, 'above', '2026-04-11 18:50:27', 'mana-seed', false, 'sign', NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('b554e662-4655-47b5-a42d-183a09cd8bd4', 'Shop Sign (Shield)', 'sign', 'default', 0.5, 0.5, 'above', '2026-04-11 18:50:27', 'mana-seed', false, 'sign', NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('70a2bd14-8b31-4d56-9f6b-a9e9fb464f22', 'Shop Sign (Sword)', 'sign', 'default', 0.5, 0.5, 'above', '2026-04-11 18:50:27', 'mana-seed', false, 'sign', NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('ea32135d-2e20-4fbd-90d6-0807492c5ee5', 'Shop Sign (Tools)', 'sign', 'default', 0.5, 0.5, 'above', '2026-04-11 18:50:27', 'mana-seed', false, 'sign', NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('2b720831-711f-4ab3-8c91-ecf5755f5c3a', 'Small Pot (Empty)', 'prop', 'default', 0.5, 0.85, 'objects', '2026-04-11 18:50:27', 'mana-seed', false, NULL, NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('1952470d-fa16-4bb4-8fe9-6ddeee1a66e3', 'Small Pot (Full)', 'prop', 'default', 0.5, 0.85, 'objects', '2026-04-11 18:50:27', 'mana-seed', false, NULL, NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('08c626c1-fbbe-4ef7-84ff-cc8b2de0bff6', 'Water Bucket', 'prop', 'default', 0.5, 0.85, 'objects', '2026-04-11 18:50:27', 'mana-seed', false, NULL, NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('fabb9229-2cd0-42e1-a451-9aa0f165417a', 'Well (Empty)', 'structure', 'default', 0.5, 0.85, 'objects', '2026-04-11 14:12:42', 'mana-seed', false, NULL, NULL, 'random_per_object', 0, true, false, 10, 1, 1, 4, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('8e5ebe25-6f2d-4599-a3ae-4bd0825e1fd4', 'Wishing Well', 'structure', 'default', 0.5, 0.85, 'objects', '2026-04-11 14:12:42', 'mana-seed', false, NULL, NULL, 'random_per_object', 0, true, false, 10, 1, 1, 4, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('4f260352-0d26-489d-a2f5-ba1b00c7480f', 'Wood Pile (Small)', 'prop', 'default', 0.5, 0.85, 'objects', '2026-04-11 18:50:27', 'mana-seed', false, NULL, NULL, 'random_per_object', 0, false, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2),
    ('bd35ecbd-47e5-4c54-ac92-b3463b3bc469', 'Wrought Iron Door', 'fence', 'default', 0.5, 0.85, 'objects', '2026-04-11 18:50:27', 'mana-seed-fences', false, NULL, NULL, 'random_per_object', 0, true, false, 10, 0, 0, 0, 0, NULL, NULL, false, NULL, NULL, 1, false, 2);

-- tags: the state's tags, sorted, comma-joined. lights: each light as
-- color|radius|energy|offset_x|offset_y|flicker_amplitude|flicker_period_ms.
CREATE TEMP TABLE llm968_state (asset_id uuid, state text, sheet text, src_x integer, src_y integer, src_w integer, src_h integer, frame_count integer, frame_rate double precision, created_at timestamp, tags text, lights text) ON COMMIT DROP;
INSERT INTO llm968_state VALUES
    ('54912584-7e34-42d2-9c76-0c416d62528d', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x48.png', 16, 0, 16, 48, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Barrel Stack
    ('6dbb582f-1bd2-4fd5-82a1-c70719443dfa', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 128, 32, 16, 16, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Bucket (Empty)
    ('23f2625e-4f8f-44bc-93d1-618d6e2816ba', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x48.png', 32, 0, 16, 48, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Crate Stack
    ('55ee1ab5-50e3-475d-a85f-c06b5e82d072', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 64, 32, 16, 16, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Flower Pot (Empty)
    ('934ca72e-6435-4cca-bd13-6f575df50b1c', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 112, 32, 16, 16, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Flower Pot (Mixed)
    ('da1df5c4-7ac3-4ed7-9110-cddb8c35f619', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 96, 32, 16, 16, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Flower Pot (Red)
    ('eccf6f53-e588-4ff4-bf30-a99e7723110b', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 80, 32, 16, 16, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Flower Pot (Violet)
    ('b016c28b-323a-4851-b57d-c5c600aad85a', 'lit', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 32, 64, 32, 32, 1, 0, '2026-04-17 17:52:17', 'lamplighter-target,night-active', '#FFAA55|96|1|0|0|0|0'),  -- Hanging Lantern
    ('b016c28b-323a-4851-b57d-c5c600aad85a', 'unlit', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 0, 64, 32, 32, 1, 0, '2026-04-17 17:52:17', 'day-active,lamplighter-target', ''),  -- Hanging Lantern
    ('145ab1e4-0179-41c2-95f6-21a1cb6080ab', 'closed', '/tilesets/mana-seed/fences-walls/iron stone wall gate 64x48.png', 0, 0, 64, 48, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Iron Stone Gate
    ('145ab1e4-0179-41c2-95f6-21a1cb6080ab', 'open', '/tilesets/mana-seed/fences-walls/iron stone wall gate 64x48.png', 64, 0, 64, 48, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Iron Stone Gate
    ('7e8c4224-b816-4394-bb13-a3518f6c8b6a', 'default', '/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 432, 80, 48, 80, 1, 0, '2026-04-11 14:12:42', '', ''),  -- Lamp Post (Banner)
    ('464a861d-2376-4203-b18f-7dce2b648d3c', 'lit', '/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 144, 80, 48, 80, 1, 0, '2026-04-17 19:03:28', 'lamplighter-target,night-active', '#FFAA55|96|1|0|0|0|0'),  -- Lamp Post 3
    ('464a861d-2376-4203-b18f-7dce2b648d3c', 'unlit', '/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 96, 80, 48, 80, 1, 0, '2026-04-17 19:03:28', 'day-active,lamplighter-target', ''),  -- Lamp Post 3
    ('63fa2344-5423-416c-8555-5a2e20bfee9a', 'empty', '/tilesets/mana-seed/village-accessories/village laundry, alley angle 48x64.png', 0, 0, 48, 64, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Angle)
    ('63fa2344-5423-416c-8555-5a2e20bfee9a', 'variant-1', '/tilesets/mana-seed/village-accessories/village laundry, alley angle 48x64.png', 48, 0, 48, 64, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Angle)
    ('63fa2344-5423-416c-8555-5a2e20bfee9a', 'variant-10', '/tilesets/mana-seed/village-accessories/village laundry, alley angle 48x64.png', 96, 64, 48, 64, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Angle)
    ('63fa2344-5423-416c-8555-5a2e20bfee9a', 'variant-11', '/tilesets/mana-seed/village-accessories/village laundry, alley angle 48x64.png', 144, 64, 48, 64, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Angle)
    ('63fa2344-5423-416c-8555-5a2e20bfee9a', 'variant-12', '/tilesets/mana-seed/village-accessories/village laundry, alley angle 48x64.png', 192, 64, 48, 64, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Angle)
    ('63fa2344-5423-416c-8555-5a2e20bfee9a', 'variant-13', '/tilesets/mana-seed/village-accessories/village laundry, alley angle 48x64.png', 240, 64, 48, 64, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Angle)
    ('63fa2344-5423-416c-8555-5a2e20bfee9a', 'variant-14', '/tilesets/mana-seed/village-accessories/village laundry, alley angle 48x64.png', 288, 64, 48, 64, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Angle)
    ('63fa2344-5423-416c-8555-5a2e20bfee9a', 'variant-15', '/tilesets/mana-seed/village-accessories/village laundry, alley angle 48x64.png', 336, 64, 48, 64, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Angle)
    ('63fa2344-5423-416c-8555-5a2e20bfee9a', 'variant-2', '/tilesets/mana-seed/village-accessories/village laundry, alley angle 48x64.png', 96, 0, 48, 64, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Angle)
    ('63fa2344-5423-416c-8555-5a2e20bfee9a', 'variant-3', '/tilesets/mana-seed/village-accessories/village laundry, alley angle 48x64.png', 144, 0, 48, 64, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Angle)
    ('63fa2344-5423-416c-8555-5a2e20bfee9a', 'variant-4', '/tilesets/mana-seed/village-accessories/village laundry, alley angle 48x64.png', 192, 0, 48, 64, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Angle)
    ('63fa2344-5423-416c-8555-5a2e20bfee9a', 'variant-5', '/tilesets/mana-seed/village-accessories/village laundry, alley angle 48x64.png', 240, 0, 48, 64, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Angle)
    ('63fa2344-5423-416c-8555-5a2e20bfee9a', 'variant-6', '/tilesets/mana-seed/village-accessories/village laundry, alley angle 48x64.png', 288, 0, 48, 64, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Angle)
    ('63fa2344-5423-416c-8555-5a2e20bfee9a', 'variant-7', '/tilesets/mana-seed/village-accessories/village laundry, alley angle 48x64.png', 336, 0, 48, 64, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Angle)
    ('63fa2344-5423-416c-8555-5a2e20bfee9a', 'variant-8', '/tilesets/mana-seed/village-accessories/village laundry, alley angle 48x64.png', 0, 64, 48, 64, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Angle)
    ('63fa2344-5423-416c-8555-5a2e20bfee9a', 'variant-9', '/tilesets/mana-seed/village-accessories/village laundry, alley angle 48x64.png', 48, 64, 48, 64, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Angle)
    ('69802b26-04cc-4b76-a379-79cb07f10aed', 'empty', '/tilesets/mana-seed/village-accessories/village laundry, alley straight 64x48.png', 0, 0, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Straight)
    ('69802b26-04cc-4b76-a379-79cb07f10aed', 'variant-1', '/tilesets/mana-seed/village-accessories/village laundry, alley straight 64x48.png', 64, 0, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Straight)
    ('69802b26-04cc-4b76-a379-79cb07f10aed', 'variant-10', '/tilesets/mana-seed/village-accessories/village laundry, alley straight 64x48.png', 128, 48, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Straight)
    ('69802b26-04cc-4b76-a379-79cb07f10aed', 'variant-11', '/tilesets/mana-seed/village-accessories/village laundry, alley straight 64x48.png', 192, 48, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Straight)
    ('69802b26-04cc-4b76-a379-79cb07f10aed', 'variant-12', '/tilesets/mana-seed/village-accessories/village laundry, alley straight 64x48.png', 256, 48, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Straight)
    ('69802b26-04cc-4b76-a379-79cb07f10aed', 'variant-13', '/tilesets/mana-seed/village-accessories/village laundry, alley straight 64x48.png', 320, 48, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Straight)
    ('69802b26-04cc-4b76-a379-79cb07f10aed', 'variant-14', '/tilesets/mana-seed/village-accessories/village laundry, alley straight 64x48.png', 384, 48, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Straight)
    ('69802b26-04cc-4b76-a379-79cb07f10aed', 'variant-15', '/tilesets/mana-seed/village-accessories/village laundry, alley straight 64x48.png', 448, 48, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Straight)
    ('69802b26-04cc-4b76-a379-79cb07f10aed', 'variant-16', '/tilesets/mana-seed/village-accessories/village laundry, alley straight 64x48.png', 0, 96, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Straight)
    ('69802b26-04cc-4b76-a379-79cb07f10aed', 'variant-17', '/tilesets/mana-seed/village-accessories/village laundry, alley straight 64x48.png', 64, 96, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Straight)
    ('69802b26-04cc-4b76-a379-79cb07f10aed', 'variant-18', '/tilesets/mana-seed/village-accessories/village laundry, alley straight 64x48.png', 128, 96, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Straight)
    ('69802b26-04cc-4b76-a379-79cb07f10aed', 'variant-19', '/tilesets/mana-seed/village-accessories/village laundry, alley straight 64x48.png', 192, 96, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Straight)
    ('69802b26-04cc-4b76-a379-79cb07f10aed', 'variant-2', '/tilesets/mana-seed/village-accessories/village laundry, alley straight 64x48.png', 128, 0, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Straight)
    ('69802b26-04cc-4b76-a379-79cb07f10aed', 'variant-20', '/tilesets/mana-seed/village-accessories/village laundry, alley straight 64x48.png', 256, 96, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Straight)
    ('69802b26-04cc-4b76-a379-79cb07f10aed', 'variant-21', '/tilesets/mana-seed/village-accessories/village laundry, alley straight 64x48.png', 320, 96, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Straight)
    ('69802b26-04cc-4b76-a379-79cb07f10aed', 'variant-22', '/tilesets/mana-seed/village-accessories/village laundry, alley straight 64x48.png', 384, 96, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Straight)
    ('69802b26-04cc-4b76-a379-79cb07f10aed', 'variant-23', '/tilesets/mana-seed/village-accessories/village laundry, alley straight 64x48.png', 448, 96, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Straight)
    ('69802b26-04cc-4b76-a379-79cb07f10aed', 'variant-3', '/tilesets/mana-seed/village-accessories/village laundry, alley straight 64x48.png', 192, 0, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Straight)
    ('69802b26-04cc-4b76-a379-79cb07f10aed', 'variant-4', '/tilesets/mana-seed/village-accessories/village laundry, alley straight 64x48.png', 256, 0, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Straight)
    ('69802b26-04cc-4b76-a379-79cb07f10aed', 'variant-5', '/tilesets/mana-seed/village-accessories/village laundry, alley straight 64x48.png', 320, 0, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Straight)
    ('69802b26-04cc-4b76-a379-79cb07f10aed', 'variant-6', '/tilesets/mana-seed/village-accessories/village laundry, alley straight 64x48.png', 384, 0, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Straight)
    ('69802b26-04cc-4b76-a379-79cb07f10aed', 'variant-7', '/tilesets/mana-seed/village-accessories/village laundry, alley straight 64x48.png', 448, 0, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Straight)
    ('69802b26-04cc-4b76-a379-79cb07f10aed', 'variant-8', '/tilesets/mana-seed/village-accessories/village laundry, alley straight 64x48.png', 0, 48, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Straight)
    ('69802b26-04cc-4b76-a379-79cb07f10aed', 'variant-9', '/tilesets/mana-seed/village-accessories/village laundry, alley straight 64x48.png', 64, 48, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Alley, Straight)
    ('ac595245-50a7-4911-a5ae-0dc44d5a5be9', 'empty', '/tilesets/mana-seed/village-accessories/village laundry, post angle 48x64.png', 0, 0, 48, 64, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Post, Angle)
    ('ac595245-50a7-4911-a5ae-0dc44d5a5be9', 'variant-1', '/tilesets/mana-seed/village-accessories/village laundry, post angle 48x64.png', 48, 0, 48, 64, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Post, Angle)
    ('ac595245-50a7-4911-a5ae-0dc44d5a5be9', 'variant-2', '/tilesets/mana-seed/village-accessories/village laundry, post angle 48x64.png', 96, 0, 48, 64, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Post, Angle)
    ('ac595245-50a7-4911-a5ae-0dc44d5a5be9', 'variant-3', '/tilesets/mana-seed/village-accessories/village laundry, post angle 48x64.png', 144, 0, 48, 64, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Post, Angle)
    ('ac595245-50a7-4911-a5ae-0dc44d5a5be9', 'variant-4', '/tilesets/mana-seed/village-accessories/village laundry, post angle 48x64.png', 192, 0, 48, 64, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Post, Angle)
    ('ac595245-50a7-4911-a5ae-0dc44d5a5be9', 'variant-5', '/tilesets/mana-seed/village-accessories/village laundry, post angle 48x64.png', 240, 0, 48, 64, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Post, Angle)
    ('ac595245-50a7-4911-a5ae-0dc44d5a5be9', 'variant-6', '/tilesets/mana-seed/village-accessories/village laundry, post angle 48x64.png', 288, 0, 48, 64, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Post, Angle)
    ('20734988-6ef3-40fe-a3df-bee0e353476d', 'empty', '/tilesets/mana-seed/village-accessories/village laundry, post straight 64x48.png', 0, 0, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Post, Straight)
    ('20734988-6ef3-40fe-a3df-bee0e353476d', 'variant-1', '/tilesets/mana-seed/village-accessories/village laundry, post straight 64x48.png', 64, 0, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Post, Straight)
    ('20734988-6ef3-40fe-a3df-bee0e353476d', 'variant-10', '/tilesets/mana-seed/village-accessories/village laundry, post straight 64x48.png', 128, 48, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Post, Straight)
    ('20734988-6ef3-40fe-a3df-bee0e353476d', 'variant-11', '/tilesets/mana-seed/village-accessories/village laundry, post straight 64x48.png', 192, 48, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Post, Straight)
    ('20734988-6ef3-40fe-a3df-bee0e353476d', 'variant-12', '/tilesets/mana-seed/village-accessories/village laundry, post straight 64x48.png', 256, 48, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Post, Straight)
    ('20734988-6ef3-40fe-a3df-bee0e353476d', 'variant-13', '/tilesets/mana-seed/village-accessories/village laundry, post straight 64x48.png', 320, 48, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Post, Straight)
    ('20734988-6ef3-40fe-a3df-bee0e353476d', 'variant-14', '/tilesets/mana-seed/village-accessories/village laundry, post straight 64x48.png', 384, 48, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Post, Straight)
    ('20734988-6ef3-40fe-a3df-bee0e353476d', 'variant-15', '/tilesets/mana-seed/village-accessories/village laundry, post straight 64x48.png', 448, 48, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Post, Straight)
    ('20734988-6ef3-40fe-a3df-bee0e353476d', 'variant-2', '/tilesets/mana-seed/village-accessories/village laundry, post straight 64x48.png', 128, 0, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Post, Straight)
    ('20734988-6ef3-40fe-a3df-bee0e353476d', 'variant-3', '/tilesets/mana-seed/village-accessories/village laundry, post straight 64x48.png', 192, 0, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Post, Straight)
    ('20734988-6ef3-40fe-a3df-bee0e353476d', 'variant-4', '/tilesets/mana-seed/village-accessories/village laundry, post straight 64x48.png', 256, 0, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Post, Straight)
    ('20734988-6ef3-40fe-a3df-bee0e353476d', 'variant-5', '/tilesets/mana-seed/village-accessories/village laundry, post straight 64x48.png', 320, 0, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Post, Straight)
    ('20734988-6ef3-40fe-a3df-bee0e353476d', 'variant-6', '/tilesets/mana-seed/village-accessories/village laundry, post straight 64x48.png', 384, 0, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Post, Straight)
    ('20734988-6ef3-40fe-a3df-bee0e353476d', 'variant-7', '/tilesets/mana-seed/village-accessories/village laundry, post straight 64x48.png', 448, 0, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Post, Straight)
    ('20734988-6ef3-40fe-a3df-bee0e353476d', 'variant-8', '/tilesets/mana-seed/village-accessories/village laundry, post straight 64x48.png', 0, 48, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Post, Straight)
    ('20734988-6ef3-40fe-a3df-bee0e353476d', 'variant-9', '/tilesets/mana-seed/village-accessories/village laundry, post straight 64x48.png', 64, 48, 64, 48, 1, 0, '2026-04-17 19:13:10', 'laundry,rotatable', ''),  -- Laundry (Post, Straight)
    ('bcddc4e6-8e3f-442a-8059-c6c13596d0ae', 'empty', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 0, 128, 32, 32, 1, 0, '2026-04-17 18:10:14', '', ''),  -- Log Rack
    ('bcddc4e6-8e3f-442a-8059-c6c13596d0ae', 'full', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 96, 128, 32, 32, 1, 0, '2026-04-17 18:10:14', '', ''),  -- Log Rack
    ('bcddc4e6-8e3f-442a-8059-c6c13596d0ae', 'low', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 32, 128, 32, 32, 1, 0, '2026-04-17 18:10:14', '', ''),  -- Log Rack
    ('bcddc4e6-8e3f-442a-8059-c6c13596d0ae', 'mid', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 64, 128, 32, 32, 1, 0, '2026-04-17 18:10:14', '', ''),  -- Log Rack
    ('26d28f0e-6e36-46c3-96f0-1ebef67abb06', 'closed', '/tilesets/mana-seed/fences-walls/low stone wall gate 64x48.png', 0, 0, 64, 48, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Low Stone Gate
    ('26d28f0e-6e36-46c3-96f0-1ebef67abb06', 'open', '/tilesets/mana-seed/fences-walls/low stone wall gate 64x48.png', 64, 0, 64, 48, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Low Stone Gate
    ('7cc29114-d3ac-4d5f-b4c1-b7b8555e76fc', 'default', '/tilesets/mana-seed/village-accessories/village accessories 32x32.png', 32, 32, 32, 32, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Open Crate
    ('650426a0-4fe5-46f6-95d2-90c7450afcf5', 'default', '/tilesets/mana-seed/village-accessories/village accessories 32x64.png', 32, 0, 32, 64, 1, 0, '2026-04-17 18:54:03', '', ''),  -- Outhouse 2
    ('9e793549-e6de-4942-a5d7-3348f0d2e2a2', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x48.png', 0, 0, 16, 48, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Potted Cypress
    ('d53b6706-73ed-4172-bfb9-2974c233141f', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 16, 0, 16, 16, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Shop Sign (Anvil)
    ('fa419ee9-0700-4018-9979-7e735746dfd3', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 208, 0, 16, 16, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Shop Sign (Barrel)
    ('90946fba-be74-44d9-97fd-4b19f4ea9121', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 224, 0, 16, 16, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Shop Sign (Blank)
    ('a9853a10-ac33-4733-b817-849ed0922f1d', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 96, 0, 16, 16, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Shop Sign (Bottle)
    ('9f63c3c7-a135-4588-8c1b-dd042a487404', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 128, 0, 16, 16, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Shop Sign (Bread)
    ('f47ae961-389f-4d58-b4ed-db0e3ede2720', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 112, 16, 16, 16, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Shop Sign (Crossed Tools)
    ('91f0aeff-8e01-4638-a6af-4313d0e44250', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 48, 0, 16, 16, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Shop Sign (Flower)
    ('6dedf58f-4a17-4f33-94eb-d43132e895ca', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 144, 0, 16, 16, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Shop Sign (Gem)
    ('d35eed79-a0f4-42f8-b840-2b75ce365a38', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 80, 16, 16, 16, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Shop Sign (Inn)
    ('de7f7c9d-7f5b-42cc-8cff-3859b93619d9', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 192, 0, 16, 16, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Shop Sign (Key)
    ('a1bbdfe5-d6e7-4812-a369-02ee57d36e03', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 96, 16, 16, 16, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Shop Sign (Percent)
    ('dabe7e31-16c0-47de-920d-cc8f50c94d2a', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 240, 0, 16, 16, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Shop Sign (Plank)
    ('2139ea76-4321-4b70-877f-ad3ed7708ff3', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 32, 0, 16, 16, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Shop Sign (Potion)
    ('9931fcfb-850a-4c75-8e81-be8c874d8237', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 112, 0, 16, 16, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Shop Sign (Scales)
    ('f34db5fd-50b6-4083-9c07-f0c08cdaa5b8', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 160, 0, 16, 16, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Shop Sign (Scroll)
    ('b554e662-4655-47b5-a42d-183a09cd8bd4', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 176, 0, 16, 16, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Shop Sign (Shield)
    ('70a2bd14-8b31-4d56-9f6b-a9e9fb464f22', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 0, 0, 16, 16, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Shop Sign (Sword)
    ('ea32135d-2e20-4fbd-90d6-0807492c5ee5', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 80, 0, 16, 16, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Shop Sign (Tools)
    ('2b720831-711f-4ab3-8c91-ecf5755f5c3a', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 32, 32, 16, 16, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Small Pot (Empty)
    ('1952470d-fa16-4bb4-8fe9-6ddeee1a66e3', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 48, 32, 16, 16, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Small Pot (Full)
    ('08c626c1-fbbe-4ef7-84ff-cc8b2de0bff6', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 144, 32, 16, 16, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Water Bucket
    ('fabb9229-2cd0-42e1-a451-9aa0f165417a', 'default', '/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 0, 0, 48, 80, 1, 0, '2026-04-11 14:12:42', '', ''),  -- Well (Empty)
    ('8e5ebe25-6f2d-4599-a3ae-4bd0825e1fd4', 'default', '/tilesets/mana-seed/village-accessories/village accessories 48x80.png', 48, 0, 48, 80, 1, 0, '2026-04-11 14:12:42', '', ''),  -- Wishing Well
    ('4f260352-0d26-489d-a2f5-ba1b00c7480f', 'default', '/tilesets/mana-seed/village-accessories/village accessories 16x16.png', 0, 32, 16, 16, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Wood Pile (Small)
    ('bd35ecbd-47e5-4c54-ac92-b3463b3bc469', 'closed', '/tilesets/mana-seed/fences-walls/door, wrought iron 32x32.png', 0, 0, 32, 31, 1, 0, '2026-04-11 18:50:27', '', ''),  -- Wrought Iron Door
    ('bd35ecbd-47e5-4c54-ac92-b3463b3bc469', 'open', '/tilesets/mana-seed/fences-walls/door, wrought iron 32x32.png', 32, 0, 32, 31, 1, 0, '2026-04-11 18:50:27', '', '');  -- Wrought Iron Door

-- The slots only the dropped Shop Signs fitted ('sign'), and the Campfire's
-- 'top', which nothing fits.
CREATE TEMP TABLE llm968_slot (asset_id uuid, slot_name text, offset_x integer, offset_y integer) ON COMMIT DROP;
INSERT INTO llm968_slot VALUES
    ('3b42aadd-dc7e-411c-90cc-f3d8395a7c32', 'top', 0, 0),  -- Campfire
    ('91cbb3a7-da0d-40f1-97d6-5fc04c0326b8', 'sign', 22, -78),  -- Hanging Banner
    ('01404aa0-8f3c-4e85-9d55-eb7099d6d89e', 'sign', 22, -78),  -- Hanging Sign
    ('796d76b4-9a3b-4541-9852-98d389c1906a', 'sign', 22, -78);  -- Wood Sign Post

-- The catalog as it is now, in the snapshot's shape, for the both-ways
-- compare below.
CREATE TEMP VIEW llm968_asset_actual AS
SELECT id, name::text, category::text, default_state::text, anchor_x, anchor_y, layer::text, created_at, pack_id::text, interior, fits_slot::text, source_file::text, rotation_algo::text, transition_spread_seconds, is_obstacle, is_passage, z_index, footprint_left, footprint_right, footprint_top, footprint_bottom, door_offset_x, door_offset_y, visible_when_inside, stand_offset_x, stand_offset_y, occupied_min_count, occupied_night_only, render_scale FROM asset WHERE id IN (SELECT id FROM llm968_asset);
CREATE TEMP VIEW llm968_state_actual AS
SELECT s.asset_id, s.state::text, s.sheet::text, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate,
       s.created_at,
       coalesce((SELECT string_agg(t.tag, ',' ORDER BY t.tag) FROM asset_state_tag t WHERE t.state_id = s.id), '') AS tags,
       coalesce((SELECT string_agg(format('%s|%s|%s|%s|%s|%s|%s', l.color, l.radius, l.energy, l.offset_x, l.offset_y,
                                          l.flicker_amplitude, l.flicker_period_ms), ';' ORDER BY 1)
                   FROM asset_state_light l WHERE l.state_id = s.id), '') AS lights
  FROM asset_state s WHERE s.asset_id IN (SELECT id FROM llm968_asset);

DO $$
DECLARE
    torch CONSTANT uuid := '4ffca19a-2b3e-4ed3-844f-41d25f15a3c6';
    bad integer;
BEGIN
    -- Nothing may point at an asset that goes.
    SELECT count(*) INTO bad FROM village_object WHERE asset_id IN (SELECT id FROM llm968_asset);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968: % placement(s) of an asset this drops; remove them (engine stopped) first', bad;
    END IF;
    SELECT count(*) INTO bad FROM asset_slot WHERE asset_id IN (SELECT id FROM llm968_asset);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968: % slot(s) on an asset this drops', bad;
    END IF;
    SELECT count(*) INTO bad FROM asset_refresh_default WHERE asset_id IN (SELECT id FROM llm968_asset);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968: % refresh default(s) on an asset this drops', bad;
    END IF;
    -- Nothing may hang from a slot that goes, or from a torch.
    SELECT count(*) INTO bad
      FROM village_object c JOIN village_object p ON p.id = c.attached_to
      JOIN asset ca ON ca.id = c.asset_id
     WHERE ca.fits_slot = 'sign' OR ca.id = torch
        OR (p.asset_id IN (SELECT asset_id FROM llm968_slot WHERE slot_name = 'top'));
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968: % attached placement(s) use a slot this drops', bad;
    END IF;

    SELECT count(*) INTO bad FROM (
        (SELECT * FROM llm968_asset WHERE id IN (SELECT id FROM asset) EXCEPT SELECT * FROM llm968_asset_actual)
        UNION ALL
        (SELECT * FROM llm968_asset_actual EXCEPT SELECT * FROM llm968_asset)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968: % asset row(s) differ from the snapshot the down recreates', bad;
    END IF;
    SELECT count(*) INTO bad FROM (
        (SELECT * FROM llm968_state WHERE asset_id IN (SELECT id FROM asset) EXCEPT SELECT * FROM llm968_state_actual)
        UNION ALL
        (SELECT * FROM llm968_state_actual EXCEPT SELECT * FROM llm968_state)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968: % state row(s) (with tags and lights) differ from the snapshot the down recreates', bad;
    END IF;

    -- A slot here with other offsets would not come back as it was.
    SELECT count(*) INTO bad
      FROM asset_slot x JOIN llm968_slot e ON e.asset_id = x.asset_id AND e.slot_name = x.slot_name
     WHERE x.offset_x <> e.offset_x OR x.offset_y <> e.offset_y;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968: % slot row(s) differ from the snapshot the down recreates', bad;
    END IF;
    IF EXISTS (SELECT 1 FROM asset WHERE id = torch AND fits_slot IS DISTINCT FROM 'torch' AND fits_slot IS NOT NULL) THEN
        RAISE EXCEPTION 'LLM-968: the Torch fits a slot other than torch';
    END IF;

    DELETE FROM asset_state WHERE asset_id IN (SELECT id FROM llm968_asset);
    DELETE FROM asset WHERE id IN (SELECT id FROM llm968_asset);
    DELETE FROM asset_slot x USING llm968_slot e WHERE x.asset_id = e.asset_id AND x.slot_name = e.slot_name;
    UPDATE asset SET fits_slot = NULL WHERE id = torch AND fits_slot = 'torch';
END $$;

COMMIT;
