-- LLM-58: fold the Blueberry Bush + Berry Bush Cluster into Prudence Ward's
-- farm (and the loose clump into wild food), and split the generic 'berries'
-- item into distinct 'raspberries' / 'blueberries'.
--
-- Slice of epic LLM-25 (foraging economy). Same shape as LLM-50 (Prudence's
-- raspberry farm) and LLM-12 (the 2-state berries/bare bush + supply->state
-- reactor in engine/sim/berry_state.go).
--
-- The "Berry Bush Cluster" (asset d699b17f, sheet cell 64,0,16,32 -- a green
-- bush with no berries) and "Blueberry Bush" (asset 630909ca, cell 80,0,16,32 --
-- the same bush WITH blue berries) are the bare/fruited pair of one bush, just
-- entered as two single-state assets. We unify them on the Blueberry asset
-- (which keeps the blue identity) as a 2-state bush, exactly like the Raspberry
-- Bush (db4b428c) already is. The generic refreshObjectBerryState reactor needs
-- only a 'berries'- and 'bare'-tagged state pair plus a finite gatherable
-- refresh row -- no engine code.
--
-- The target bushes are pinned by explicit id (captured at authoring time) so
-- the migration is precisely scoped and rerun-safe -- it can never pull in a
-- later-placed Blueberry object by geography. For reference, the farm set is the
-- 18 Cluster + 14 Blueberry instances in Prudence's plot (x<0, beside her 16
-- raspberry bushes); the wild set is the 8 Blueberry instances in the far-SE
-- clump (x>0). Counts: 32 farm + 8 wild = 40.
--
-- ENGINE-OWNED TABLES. village_object and object_refresh are checkpoint-written
-- by the running engine. Apply with the engine STOPPED (stop -> migrate ->
-- start) or the old binary's shutdown checkpoint clobbers it. snapshot_gen is
-- left at the column default; LoadAll has no gen filter, so migrated rows enter
-- memory at boot and the first checkpoint re-stamps them.

BEGIN;

-- Target id sets. llm58_farm = Prudence's plot (18 Cluster + 14 in-plot
-- Blueberry); llm58_wild = the 8 far-SE wild Blueberry. ON COMMIT DROP cleans up.
CREATE TEMP TABLE llm58_farm (id uuid PRIMARY KEY) ON COMMIT DROP;
INSERT INTO llm58_farm (id) VALUES
    -- 18 Berry Bush Cluster instances (originally asset d699b17f)
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
    ('019da6a0-c7d5-7ec7-8220-3d3710d501db'),
    -- 14 in-plot Blueberry instances (already asset 630909ca, x<0)
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

-- Fail loud if a pinned id is stale/deleted -- otherwise a missing id would just
-- shrink the migrated set silently. Existence counts only (not pre-migration
-- asset split), so the assertion stays rerun-safe: the objects still exist after
-- a prior apply even though their asset/owner changed. A count of 0 is the
-- UNSEEDED case (a fresh schema-only DB -- the test harness, a new environment):
-- the migration no-ops there, so 0 is allowed; a partial count (some but not all)
-- is the real stale-id failure this guards.
DO $$
DECLARE n int;
BEGIN
    IF (SELECT count(*) FROM llm58_farm) <> 32 THEN
        RAISE EXCEPTION 'LLM-58: farm id set has %, expected 32', (SELECT count(*) FROM llm58_farm);
    END IF;
    IF (SELECT count(*) FROM llm58_wild) <> 8 THEN
        RAISE EXCEPTION 'LLM-58: wild id set has %, expected 8', (SELECT count(*) FROM llm58_wild);
    END IF;
    SELECT count(*) INTO n FROM village_object WHERE id IN (SELECT id FROM llm58_farm);
    IF n <> 0 AND n <> 32 THEN
        RAISE EXCEPTION 'LLM-58: expected 32 (or 0 on an unseeded DB) farm bushes, found %', n;
    END IF;
    SELECT count(*) INTO n FROM village_object WHERE id IN (SELECT id FROM llm58_wild);
    IF n <> 0 AND n <> 8 THEN
        RAISE EXCEPTION 'LLM-58: expected 8 (or 0 on an unseeded DB) wild bushes, found %', n;
    END IF;
END $$;

-- 1. Blueberry asset (630909ca) -> 2-state bush.
--    Re-tag its lone 'default' state (the fruited blue-berry sprite) as
--    'berries', and add a 'bare' state pointing at the Cluster's sprite (the
--    same bush without berries). The NOT EXISTS guard keeps the retag safe if a
--    prior partial run already produced 'berries' (asset_state is UNIQUE on
--    (asset_id, state), so an unguarded retag could collide). default_state ->
--    'berries' to match the Raspberry Bush.
UPDATE asset_state
SET state = 'berries'
WHERE asset_id = '630909ca-df4f-43ac-9fc4-5192ca44da73'
  AND state = 'default'
  AND NOT EXISTS (
      SELECT 1 FROM asset_state
      WHERE asset_id = '630909ca-df4f-43ac-9fc4-5192ca44da73' AND state = 'berries'
  );

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h)
SELECT '630909ca-df4f-43ac-9fc4-5192ca44da73', 'bare',
       '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x32.png', 64, 0, 16, 32
WHERE NOT EXISTS (
    SELECT 1 FROM asset_state
    WHERE asset_id = '630909ca-df4f-43ac-9fc4-5192ca44da73' AND state = 'bare'
);

-- Converge: if 'berries' now exists, drop any leftover 'default' from a prior
-- partial run so the asset ends as exactly {berries, bare}.
DELETE FROM asset_state
WHERE asset_id = '630909ca-df4f-43ac-9fc4-5192ca44da73'
  AND state = 'default'
  AND EXISTS (
      SELECT 1 FROM asset_state
      WHERE asset_id = '630909ca-df4f-43ac-9fc4-5192ca44da73' AND state = 'berries'
  );

UPDATE asset
SET default_state = 'berries'
WHERE id = '630909ca-df4f-43ac-9fc4-5192ca44da73';

-- 2. Item catalog: 'raspberries' and 'blueberries' as distinct food items,
--    mirroring the generic 'berries' (food/portable, satisfies hunger +2,
--    recipe price 1/1). DO UPDATE makes these rows authoritative for the
--    migration (and lets the down-migration legitimately own/remove them). The
--    generic 'berries' kind is left in place (still a valid item).
INSERT INTO item_kind (name, display_label, category, sort_order, capabilities)
VALUES
    ('raspberries', 'Raspberries', 'food', 141, '{portable}'),
    ('blueberries', 'Blueberries', 'food', 142, '{portable}')
ON CONFLICT (name) DO UPDATE
SET display_label = EXCLUDED.display_label,
    category      = EXCLUDED.category,
    sort_order    = EXCLUDED.sort_order,
    capabilities  = EXCLUDED.capabilities;

INSERT INTO item_satisfies (item_kind, attribute, amount)
VALUES
    ('raspberries', 'hunger', 2),
    ('blueberries', 'hunger', 2)
ON CONFLICT (item_kind, attribute) DO UPDATE
SET amount = EXCLUDED.amount;

INSERT INTO item_recipe (output_item, output_qty, rate_qty, rate_per_hours, wholesale_price, retail_price)
VALUES
    ('raspberries', 1, 2, 1, 1, 1),
    ('blueberries', 1, 2, 1, 1, 1)
ON CONFLICT (output_item) DO UPDATE
SET output_qty      = EXCLUDED.output_qty,
    rate_qty        = EXCLUDED.rate_qty,
    rate_per_hours  = EXCLUDED.rate_per_hours,
    wholesale_price = EXCLUDED.wholesale_price,
    retail_price    = EXCLUDED.retail_price;

-- 3. Raspberry bushes now yield 'raspberries' instead of the generic 'berries'.
--    Covers both the 8 wild (amount -8) and 16 Prudence-farm (amount 0) rows.
UPDATE object_refresh r
SET gather_item = 'raspberries'
FROM village_object v
WHERE r.object_id = v.id
  AND v.asset_id = 'db4b428c-9ab6-4457-85fb-3f85fe86c946'
  AND r.gather_item = 'berries';

-- 4. The 32 farm bushes become Prudence Ward's forage-to-sell blueberry bushes:
--    re-pointed to the unified Blueberry asset, owned by her, started ripe.
UPDATE village_object
SET asset_id       = '630909ca-df4f-43ac-9fc4-5192ca44da73',
    owner_actor_id = '019dbcec-1149-7149-8a49-2cdb54680b86',  -- Prudence Ward
    current_state  = 'berries'
WHERE id IN (SELECT id FROM llm58_farm);

-- One forage-to-sell refresh row per farm bush (amount 0 = harvest to SELL, not
-- eat in place; finite supply 10, regrow over 168h = 7 real days). DO UPDATE
-- enforces the migrated shape on rerun but deliberately leaves available_quantity
-- alone so a rerun never resets Prudence's harvested stock.
INSERT INTO object_refresh
    (object_id, attribute, amount, max_quantity, available_quantity,
     refresh_mode, refresh_period_hours, gather_item)
SELECT v.id, 'hunger', 0, 10, 10, 'periodic', 168, 'blueberries'
FROM village_object v
WHERE v.id IN (SELECT id FROM llm58_farm)
  AND v.asset_id = '630909ca-df4f-43ac-9fc4-5192ca44da73'
  AND v.owner_actor_id = '019dbcec-1149-7149-8a49-2cdb54680b86'
ON CONFLICT (object_id, attribute) DO UPDATE
SET amount               = EXCLUDED.amount,
    max_quantity         = EXCLUDED.max_quantity,
    refresh_mode         = EXCLUDED.refresh_mode,
    refresh_period_hours = EXCLUDED.refresh_period_hours,
    gather_item          = EXCLUDED.gather_item;

-- 5. The 8 far-SE Blueberry bushes become wild edible bushes, mirroring the
--    loose raspberries: eat in place (amount -8 hunger), finite supply 3, regrow
--    over 6h. Asset and owner (NULL) are unchanged; only ripen + add the row.
UPDATE village_object
SET current_state = 'berries'
WHERE id IN (SELECT id FROM llm58_wild);

INSERT INTO object_refresh
    (object_id, attribute, amount, max_quantity, available_quantity,
     refresh_mode, refresh_period_hours, gather_item)
SELECT v.id, 'hunger', -8, 3, 3, 'periodic', 6, 'blueberries'
FROM village_object v
WHERE v.id IN (SELECT id FROM llm58_wild)
  AND v.asset_id = '630909ca-df4f-43ac-9fc4-5192ca44da73'
  AND v.owner_actor_id IS NULL
ON CONFLICT (object_id, attribute) DO UPDATE
SET amount               = EXCLUDED.amount,
    max_quantity         = EXCLUDED.max_quantity,
    refresh_mode         = EXCLUDED.refresh_mode,
    refresh_period_hours = EXCLUDED.refresh_period_hours,
    gather_item          = EXCLUDED.gather_item;

COMMIT;
