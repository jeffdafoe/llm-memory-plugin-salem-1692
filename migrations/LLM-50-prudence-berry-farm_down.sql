-- Revert LLM-50: return Prudence Ward's berry farm to plain decorative
-- "Berry Bush" instances and remove their forage-to-sell refresh rows.
--
-- The 16 instances are identified by id (they were the only "Berry Bush" rows;
-- after the up-migration they are the Bush-asset, Prudence-owned plot). Their
-- forage-to-sell refresh rows are deleted first, then the placement reverts to
-- the original asset / unowned / 'default' state.
--
-- Apply with the engine STOPPED, same as the up-migration (these are
-- checkpoint-written engine-owned tables).

BEGIN;

-- Delete only the LLM-50 forage-to-sell rows: match the exact migrated shape
-- (NOT available_quantity, which legitimately changes as Prudence harvests) and
-- the migrated object state, so a later-added or repurposed refresh row on these
-- ids is never collaterally removed.
DELETE FROM object_refresh r
USING village_object v
WHERE r.object_id = v.id
  AND v.asset_id = 'db4b428c-9ab6-4457-85fb-3f85fe86c946'
  AND v.owner_actor_id = '019dbcec-1149-7149-8a49-2cdb54680b86'
  AND r.attribute = 'hunger'
  AND r.amount = 0
  AND r.max_quantity = 10
  AND r.refresh_mode = 'periodic'
  AND r.refresh_period_hours = 168
  AND r.gather_item = 'berries'
  AND v.id IN (
    '019da6a1-3007-7822-a266-1257bc65f3a6',
    '019da6a1-19f9-7ae8-90cf-6856a6c6cdb6',
    '019da6a1-675b-783d-ae03-29ece5fe5ced',
    '019da6a1-487e-7211-abc3-5b07d267841f',
    '019da6a1-9763-7758-afa1-2192e74b60a4',
    '019da6a1-7de4-7427-a3f9-823f514389bb',
    '019da6a1-cce9-7233-8dff-bd93ee0b782a',
    '019da6a1-b357-7ed0-a28f-6be638687950',
    '019da6a1-e84f-7c1c-ac28-c47b1fd5bb04',
    '019da6a2-0150-76af-b4fd-fabde9fb1efb',
    '019da6a2-478f-796f-b1be-1b8d2a29eb72',
    '019da6a2-20fb-7641-9c38-e4066f599bbe',
    '019da6a2-6f0f-7ac5-8b7a-490b1289122b',
    '019da6a2-8bfc-7951-adf7-a4c05c3813cb',
    '019da6a2-c17f-7c0b-9485-32fe984a21db',
    '019da6a2-a94a-7721-9c67-3cc3e23711c0'
  );

UPDATE village_object
SET asset_id       = '3d3a2147-08cb-4409-8c81-06ef4a59a420',  -- back to "Berry Bush"
    owner_actor_id = NULL,
    current_state  = 'default'
WHERE asset_id = 'db4b428c-9ab6-4457-85fb-3f85fe86c946'       -- only if still the Bush asset
  AND id IN (
    '019da6a1-3007-7822-a266-1257bc65f3a6',
    '019da6a1-19f9-7ae8-90cf-6856a6c6cdb6',
    '019da6a1-675b-783d-ae03-29ece5fe5ced',
    '019da6a1-487e-7211-abc3-5b07d267841f',
    '019da6a1-9763-7758-afa1-2192e74b60a4',
    '019da6a1-7de4-7427-a3f9-823f514389bb',
    '019da6a1-cce9-7233-8dff-bd93ee0b782a',
    '019da6a1-b357-7ed0-a28f-6be638687950',
    '019da6a1-e84f-7c1c-ac28-c47b1fd5bb04',
    '019da6a2-0150-76af-b4fd-fabde9fb1efb',
    '019da6a2-478f-796f-b1be-1b8d2a29eb72',
    '019da6a2-20fb-7641-9c38-e4066f599bbe',
    '019da6a2-6f0f-7ac5-8b7a-490b1289122b',
    '019da6a2-8bfc-7951-adf7-a4c05c3813cb',
    '019da6a2-c17f-7c0b-9485-32fe984a21db',
    '019da6a2-a94a-7721-9c67-3cc3e23711c0'
  );

COMMIT;
