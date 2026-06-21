-- Revert LLM-58: return the Blueberry/Cluster bushes to plain decorative
-- single-state instances, undo the raspberry/blueberry item split, and revert
-- the Blueberry asset to its single 'default' state.
--
-- Apply with the engine STOPPED, same as the up-migration (checkpoint-written
-- engine-owned tables).
--
-- Targets are pinned by explicit id (same sets as the up-migration). The 18
-- Cluster instances are restored to their original asset (d699b17f); the 14
-- in-plot Blueberry instances keep asset 630909ca (their original) and are just
-- un-owned; the 8 wild Blueberry instances keep 630909ca and lose their wild row.

BEGIN;

CREATE TEMP TABLE llm58_clusters (id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO llm58_clusters (id) VALUES
    ('019da69d-c72d-7455-836f-08758f7968bc'),
    ('019da69e-8faa-78e3-b692-773a8ab3cd86'),
    ('019da69e-ad72-79ed-ac7b-5f8ddd5f2e40'),
    ('019da69e-c96f-71a7-9ca9-013b5e0f59f7'),
    ('019da69f-122e-7709-b252-e9e1740e8faf'),
    ('019da69f-366a-799f-8e1a-8485a7f97ef9'),
    ('019da69f-4d2a-7f13-8c98-013e1681519c'),
    ('019da69f-6513-77df-bb7f-8c5dc7ebb584'),
    ('019da69f-9308-71cb-95d4-c5612fee36ff'),
    ('019da69f-b003-7d07-9d1d-fd2d9dfc301d'),
    ('019da69f-d1f9-79c1-a5e0-231b95330709'),
    ('019da69f-f0ad-736a-9c39-2a953448ec74'),
    ('019da6a0-0cf1-7ddb-927b-b323e1f552ea'),
    ('019da6a0-2acf-7986-a4ea-0a448232a484'),
    ('019da6a0-613b-7041-bf08-8c9b9c9fe707'),
    ('019da6a0-8279-70b6-9578-981bea479f9f'),
    ('019da6a0-ab60-7206-a82f-5080c259f94d'),
    ('019da6a0-c7d5-7ec7-8220-3d3710d501db');

CREATE TEMP TABLE llm58_westblue (id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO llm58_westblue (id) VALUES
    ('019da6a2-eb05-7a11-975d-8f3fc329ad79'),
    ('019da6a3-063f-74e0-98a5-34c1925da2eb'),
    ('019da6a3-1e95-7f0f-802e-03a7cc44d36d'),
    ('019da6a3-33ae-7132-917d-068487774665'),
    ('019da6a3-4b08-7e38-bc8b-e4312f6a4c02'),
    ('019da6a3-6a04-734a-a231-cac5a5c8911d'),
    ('019da6a3-8a95-7f55-bd18-e359aeeca693'),
    ('019da6a3-b32e-713c-aff6-0a5ca4c04f2c'),
    ('019da6a3-e874-7fa8-a211-168c03ed4f9a'),
    ('019da6a4-02fd-7d75-8a9e-3382a9b14031'),
    ('019da6a4-214a-7123-b093-56d605d708df'),
    ('019da6a4-3bf4-742e-90a0-411d00bc00ff'),
    ('019da6a4-520c-72bf-a45d-6328d3faf4d8'),
    ('019da6a4-706a-779d-adc1-a072df50833a');

CREATE TEMP TABLE llm58_wild (id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO llm58_wild (id) VALUES
    ('019d98b8-7a4b-757e-a70a-96076633d0db'),
    ('019d98b8-9c42-787e-8cbf-c91f99f8cbef'),
    ('019d98b8-bb9d-7185-b8a3-3c32435b6569'),
    ('019d98b8-d5f7-7c16-8c95-8648f0c4c54d'),
    ('019d98b8-e68f-7919-93e0-3a592797c73f'),
    ('019d98b8-f75a-7441-af9c-e8b8addae500'),
    ('019d98b9-0ad3-725a-9aaa-cb11ef87a152'),
    ('019d98b9-1bb9-77e9-ae7b-6b061aa64e1f');

-- 5. Undo the far-SE wild blueberry bushes: drop the wild refresh rows (matched
--    by id + exact migrated shape, so a later-added row is never collaterally
--    removed) and return them to the unfruited 'default' state.
DELETE FROM object_refresh
WHERE object_id IN (SELECT id FROM llm58_wild)
  AND attribute = 'hunger'
  AND amount = -8
  AND max_quantity = 3
  AND refresh_mode = 'periodic'
  AND refresh_period_hours = 6
  AND gather_item = 'blueberries';

UPDATE village_object
SET current_state = 'default'
WHERE id IN (SELECT id FROM llm58_wild);

-- 4. Undo Prudence's blueberry plot. Drop the 32 forage-to-sell rows (id + exact
--    shape), then restore placements: Clusters -> d699b17f unowned, in-plot
--    Blueberries -> unowned 630909ca. Both go back to the 'default' state.
DELETE FROM object_refresh
WHERE object_id IN (SELECT id FROM llm58_clusters UNION SELECT id FROM llm58_westblue)
  AND attribute = 'hunger'
  AND amount = 0
  AND max_quantity = 10
  AND refresh_mode = 'periodic'
  AND refresh_period_hours = 168
  AND gather_item = 'blueberries';

UPDATE village_object
SET asset_id       = 'd699b17f-c743-48d4-8bb9-debaba884a55',
    owner_actor_id = NULL,
    current_state  = 'default'
WHERE id IN (SELECT id FROM llm58_clusters);

UPDATE village_object
SET owner_actor_id = NULL,
    current_state  = 'default'
WHERE id IN (SELECT id FROM llm58_westblue);

-- 3. Raspberry bushes yield the generic 'berries' again. Broad (any 'raspberries'
--    refresh row) so no 'raspberries' references survive the item-kind delete
--    below, even if a raspberry bush was repointed after LLM-58.
UPDATE object_refresh
SET gather_item = 'berries'
WHERE gather_item = 'raspberries';

-- 2. Remove the split item kinds (recipe / satisfies first, then the kind).
DELETE FROM item_recipe    WHERE output_item IN ('raspberries', 'blueberries');
DELETE FROM item_satisfies WHERE item_kind   IN ('raspberries', 'blueberries');
DELETE FROM item_kind      WHERE name        IN ('raspberries', 'blueberries');

-- 1. Blueberry asset back to a single 'default' state. The NOT EXISTS guard keeps
--    the retag safe if a 'default' row somehow still exists (asset_state is
--    UNIQUE on (asset_id, state)).
UPDATE asset
SET default_state = 'default'
WHERE id = '630909ca-df4f-43ac-9fc4-5192ca44da73';

DELETE FROM asset_state
WHERE asset_id = '630909ca-df4f-43ac-9fc4-5192ca44da73'
  AND state = 'bare';

UPDATE asset_state
SET state = 'default'
WHERE asset_id = '630909ca-df4f-43ac-9fc4-5192ca44da73'
  AND state = 'berries'
  AND NOT EXISTS (
      SELECT 1 FROM asset_state
      WHERE asset_id = '630909ca-df4f-43ac-9fc4-5192ca44da73' AND state = 'default'
  );

COMMIT;
