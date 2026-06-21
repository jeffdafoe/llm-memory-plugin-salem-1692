-- Revert LLM-58: return the Blueberry/Cluster bushes to plain decorative
-- single-state instances, undo the raspberry/blueberry item split, and revert
-- the Blueberry asset to its single 'default' state.
--
-- Apply with the engine STOPPED, same as the up-migration (checkpoint-written
-- engine-owned tables).
--
-- The 18 Cluster instances are restored to their original asset (d699b17f) by
-- explicit id -- after the up-migration they are indistinguishable by asset or
-- position from the 14 in-plot Blueberry instances (all became Prudence-owned
-- 630909ca at x<0). The Blueberry instances keep asset 630909ca (their original).

BEGIN;

-- 5. Undo the far-SE wild blueberry bushes: drop the wild refresh rows (matched
--    by exact migrated shape so a later-added row is never collaterally removed)
--    and return them to the unfruited 'default' state.
DELETE FROM object_refresh r
USING village_object v
WHERE r.object_id = v.id
  AND v.asset_id = '630909ca-df4f-43ac-9fc4-5192ca44da73'
  AND v.x > 0
  AND v.owner_actor_id IS NULL
  AND r.attribute = 'hunger'
  AND r.amount = -8
  AND r.max_quantity = 3
  AND r.refresh_mode = 'periodic'
  AND r.refresh_period_hours = 6
  AND r.gather_item = 'blueberries';

UPDATE village_object
SET current_state = 'default'
WHERE asset_id = '630909ca-df4f-43ac-9fc4-5192ca44da73'
  AND x > 0
  AND owner_actor_id IS NULL;

-- 4. Undo Prudence's blueberry plot. Drop the 32 forage-to-sell rows (exact
--    shape), then restore placements: Clusters -> d699b17f (by id), in-plot
--    Blueberries -> unowned 630909ca. Both go back to the 'default' state.
DELETE FROM object_refresh r
USING village_object v
WHERE r.object_id = v.id
  AND v.asset_id = '630909ca-df4f-43ac-9fc4-5192ca44da73'
  AND v.owner_actor_id = '019dbcec-1149-7149-8a49-2cdb54680b86'
  AND v.x < 0
  AND r.attribute = 'hunger'
  AND r.amount = 0
  AND r.max_quantity = 10
  AND r.refresh_mode = 'periodic'
  AND r.refresh_period_hours = 168
  AND r.gather_item = 'blueberries';

UPDATE village_object
SET asset_id       = 'd699b17f-c743-48d4-8bb9-debaba884a55',
    owner_actor_id = NULL,
    current_state  = 'default'
WHERE id IN (
    '019da69d-c72d-7455-836f-08758f7968bc',
    '019da69e-8faa-78e3-b692-773a8ab3cd86',
    '019da69e-ad72-79ed-ac7b-5f8ddd5f2e40',
    '019da69e-c96f-71a7-9ca9-013b5e0f59f7',
    '019da69f-122e-7709-b252-e9e1740e8faf',
    '019da69f-366a-799f-8e1a-8485a7f97ef9',
    '019da69f-4d2a-7f13-8c98-013e1681519c',
    '019da69f-6513-77df-bb7f-8c5dc7ebb584',
    '019da69f-9308-71cb-95d4-c5612fee36ff',
    '019da69f-b003-7d07-9d1d-fd2d9dfc301d',
    '019da69f-d1f9-79c1-a5e0-231b95330709',
    '019da69f-f0ad-736a-9c39-2a953448ec74',
    '019da6a0-0cf1-7ddb-927b-b323e1f552ea',
    '019da6a0-2acf-7986-a4ea-0a448232a484',
    '019da6a0-613b-7041-bf08-8c9b9c9fe707',
    '019da6a0-8279-70b6-9578-981bea479f9f',
    '019da6a0-ab60-7206-a82f-5080c259f94d',
    '019da6a0-c7d5-7ec7-8220-3d3710d501db'
);

UPDATE village_object
SET owner_actor_id = NULL,
    current_state  = 'default'
WHERE asset_id = '630909ca-df4f-43ac-9fc4-5192ca44da73'
  AND owner_actor_id = '019dbcec-1149-7149-8a49-2cdb54680b86'
  AND x < 0;

-- 3. Raspberry bushes yield the generic 'berries' again.
UPDATE object_refresh r
SET gather_item = 'berries'
FROM village_object v
WHERE r.object_id = v.id
  AND v.asset_id = 'db4b428c-9ab6-4457-85fb-3f85fe86c946'
  AND r.gather_item = 'raspberries';

-- 2. Remove the split item kinds (recipe / satisfies first, then the kind).
DELETE FROM item_recipe    WHERE output_item IN ('raspberries', 'blueberries');
DELETE FROM item_satisfies WHERE item_kind   IN ('raspberries', 'blueberries');
DELETE FROM item_kind      WHERE name        IN ('raspberries', 'blueberries');

-- 1. Blueberry asset back to a single 'default' state.
UPDATE asset
SET default_state = 'default'
WHERE id = '630909ca-df4f-43ac-9fc4-5192ca44da73';

DELETE FROM asset_state
WHERE asset_id = '630909ca-df4f-43ac-9fc4-5192ca44da73'
  AND state = 'bare';

UPDATE asset_state
SET state = 'default'
WHERE asset_id = '630909ca-df4f-43ac-9fc4-5192ca44da73'
  AND state = 'berries';

COMMIT;
