-- LLM-50: activate Prudence Ward's berry farm.
--
-- Slice 3 of epic LLM-25 (foraging economy); depends on LLM-12 (the 2-state
-- berries/bare "Bush" asset + supply->state reactor) and LLM-24 (the yield-only
-- / forage-to-sell object_refresh row, amount = 0).
--
-- The 16 "Berry Bush" instances forming the NW plot (editor tiles x -44..-41,
-- y -40..-24) become Prudence Ward's private forage-to-sell rows. Each one is:
--
--   * re-pointed to the unified 2-state "Bush" asset
--     (db4b428c-9ab6-4457-85fb-3f85fe86c946) so it renders the berries/bare
--     fruited form. The "Berry Bush" asset (3d3a2147-...) carries only a single
--     static 'default' frame -- no fruited variant -- so it cannot show or be
--     foraged as a 2-state bush.
--   * owned by Prudence Ward (019dbcec-1149-7149-8a49-2cdb54680b86). The strict
--     owner-only gather/eat gate (LLM-50 D2, VillageObject.OwnedByOther) then
--     makes these forageable by her alone; everyone else neither sees the cue
--     nor may gather them.
--   * given one yield-only (forage-to-sell) object_refresh row: amount = 0 (no
--     consume-in-place need -- she harvests to SELL, not to eat in place),
--     gather_item = 'berries', finite supply 10 of 10, periodic regrowth over
--     168 hours (7 real days -- the engine runs 1:1 with wall-clock time).
--
-- Deliberately OUT OF SCOPE this slice (left untouched / decorative): the
-- "Berry Bush Cluster" and "Blueberry Bush" instances.
--
-- ENGINE-OWNED TABLES. village_object and object_refresh are checkpoint-written
-- by the running engine (it UPSERTs both and prunes object_refresh by
-- snapshot_gen). This migration MUST be applied with the engine STOPPED
-- (stop -> migrate -> start), or the old binary's shutdown checkpoint clobbers
-- it. snapshot_gen is left at the column default (0); LoadAll has no gen filter,
-- so the migrated refresh rows enter memory at boot and the first checkpoint
-- re-stamps them.
--
-- Requires LLM-24's relaxed object_refresh_amount_negative CHECK (amount = 0
-- with a gather_item is legal). LLM-24 sorts before LLM-50, so it applies first.

BEGIN;

-- Re-point + own + ripen the 16 Berry Bush instances. The asset_id guard keeps
-- this safe to re-run: a row already migrated (or not actually a Berry Bush) is
-- left alone.
UPDATE village_object
SET asset_id       = 'db4b428c-9ab6-4457-85fb-3f85fe86c946',  -- unified 2-state Bush
    owner_actor_id = '019dbcec-1149-7149-8a49-2cdb54680b86',  -- Prudence Ward
    current_state  = 'berries'                                -- start ripe
WHERE asset_id = '3d3a2147-08cb-4409-8c81-06ef4a59a420'       -- the "Berry Bush" asset
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

-- One yield-only forage-to-sell refresh row per just-migrated farm bush. Sourced
-- from the rows the UPDATE above produced (Bush asset + Prudence-owned), so the
-- two steps can never diverge. last_refresh_at / dwell_* stay NULL; the regen
-- tick stamps last_refresh_at on its first pass.
INSERT INTO object_refresh
    (object_id, attribute, amount, max_quantity, available_quantity,
     refresh_mode, refresh_period_hours, gather_item)
SELECT id, 'hunger', 0, 10, 10, 'periodic', 168, 'berries'
FROM village_object
WHERE asset_id = 'db4b428c-9ab6-4457-85fb-3f85fe86c946'
  AND owner_actor_id = '019dbcec-1149-7149-8a49-2cdb54680b86'
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
