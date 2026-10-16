-- LLM-798 down: take every camp off the map, every tent out of every pack, and
-- the tent item and the four camp assets out of the catalog. A player whose
-- tent was their home has no home again. Run with the engine stopped.

BEGIN;

UPDATE actor SET home_structure_id = NULL
 WHERE home_structure_id IN (SELECT id::text FROM village_object
                              WHERE asset_id = '019e5f00-c401-7a10-9e00-000000798001');

UPDATE actor SET inside_structure_id = NULL, inside_room_id = NULL
 WHERE inside_structure_id IN (SELECT id::text FROM village_object
                                WHERE asset_id = '019e5f00-c401-7a10-9e00-000000798001');

-- door_note has no FK to structure; a note left at a tent goes with the tent.
DELETE FROM door_note
 WHERE structure_id IN (SELECT id::text FROM village_object
                         WHERE asset_id = '019e5f00-c401-7a10-9e00-000000798001');

DELETE FROM structure
 WHERE id IN (SELECT id::text FROM village_object
               WHERE asset_id = '019e5f00-c401-7a10-9e00-000000798001');

DELETE FROM village_object
 WHERE asset_id IN ('019e5f00-c401-7a10-9e00-000000798001', '019e5f00-c401-7a10-9e00-000000798002',
                    '019e5f00-c401-7a10-9e00-000000798003', '019e5f00-c401-7a10-9e00-000000798004');

DELETE FROM actor_inventory WHERE item_kind = 'tent';
DELETE FROM item_kind WHERE name = 'tent';

DELETE FROM asset_state_tag
 WHERE state_id IN (SELECT id FROM asset_state
                     WHERE asset_id IN ('019e5f00-c401-7a10-9e00-000000798001', '019e5f00-c401-7a10-9e00-000000798002',
                                        '019e5f00-c401-7a10-9e00-000000798003', '019e5f00-c401-7a10-9e00-000000798004'));
DELETE FROM asset_state
 WHERE asset_id IN ('019e5f00-c401-7a10-9e00-000000798001', '019e5f00-c401-7a10-9e00-000000798002',
                    '019e5f00-c401-7a10-9e00-000000798003', '019e5f00-c401-7a10-9e00-000000798004');
DELETE FROM asset
 WHERE id IN ('019e5f00-c401-7a10-9e00-000000798001', '019e5f00-c401-7a10-9e00-000000798002',
              '019e5f00-c401-7a10-9e00-000000798003', '019e5f00-c401-7a10-9e00-000000798004');

COMMIT;
