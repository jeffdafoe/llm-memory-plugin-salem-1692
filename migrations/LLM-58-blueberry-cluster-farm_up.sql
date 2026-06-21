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
-- Instance geography (point-in-time, verified at authoring): all 18 Cluster
-- instances and 14 of the 22 Blueberry instances sit in Prudence's plot (x < 0,
-- just east of her 16 raspberry bushes) -> her forage-to-sell rows. The other 8
-- Blueberry instances are a separate clump far to the southeast (x > 0) -> wild
-- edible food, like the loose raspberries. Counts: 32 west + 8 far = 40.
--
-- ENGINE-OWNED TABLES. village_object and object_refresh are checkpoint-written
-- by the running engine. Apply with the engine STOPPED (stop -> migrate ->
-- start) or the old binary's shutdown checkpoint clobbers it. snapshot_gen is
-- left at the column default; LoadAll has no gen filter, so migrated rows enter
-- memory at boot and the first checkpoint re-stamps them.

BEGIN;

-- 1. Blueberry asset (630909ca) -> 2-state bush.
--    Re-tag its lone 'default' state (the fruited blue-berry sprite) as
--    'berries', and add a 'bare' state pointing at the Cluster's sprite (the
--    same bush without berries). default_state -> 'berries' to match Raspberry.
UPDATE asset_state
SET state = 'berries'
WHERE asset_id = '630909ca-df4f-43ac-9fc4-5192ca44da73'
  AND state = 'default';

INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h)
SELECT '630909ca-df4f-43ac-9fc4-5192ca44da73', 'bare',
       '/tilesets/mana-seed/summer-forest/summer sheets/summer 16x32.png', 64, 0, 16, 32
WHERE NOT EXISTS (
    SELECT 1 FROM asset_state
    WHERE asset_id = '630909ca-df4f-43ac-9fc4-5192ca44da73' AND state = 'bare'
);

UPDATE asset
SET default_state = 'berries'
WHERE id = '630909ca-df4f-43ac-9fc4-5192ca44da73';

-- 2. Item catalog: 'raspberries' and 'blueberries' as distinct food items,
--    mirroring the generic 'berries' (food/portable, satisfies hunger +2,
--    recipe price 1/1). The generic 'berries' kind is left in place (still a
--    valid item; some inventory may hold it).
INSERT INTO item_kind (name, display_label, category, sort_order, capabilities)
VALUES
    ('raspberries', 'Raspberries', 'food', 141, '{portable}'),
    ('blueberries', 'Blueberries', 'food', 142, '{portable}')
ON CONFLICT DO NOTHING;

INSERT INTO item_satisfies (item_kind, attribute, amount)
VALUES
    ('raspberries', 'hunger', 2),
    ('blueberries', 'hunger', 2)
ON CONFLICT DO NOTHING;

INSERT INTO item_recipe (output_item, output_qty, rate_qty, rate_per_hours, wholesale_price, retail_price)
VALUES
    ('raspberries', 1, 2, 1, 1, 1),
    ('blueberries', 1, 2, 1, 1, 1)
ON CONFLICT DO NOTHING;

-- 3. Raspberry bushes now yield 'raspberries' instead of the generic 'berries'.
--    Covers both the 8 wild (amount -8) and 16 Prudence-farm (amount 0) rows.
UPDATE object_refresh r
SET gather_item = 'raspberries'
FROM village_object v
WHERE r.object_id = v.id
  AND v.asset_id = 'db4b428c-9ab6-4457-85fb-3f85fe86c946'
  AND r.gather_item = 'berries';

-- 4. The 32 west bushes (all Cluster instances + the x<0 Blueberry instances)
--    become Prudence Ward's forage-to-sell blueberry bushes: re-pointed to the
--    unified Blueberry asset, owned by her, started ripe. The d699b17f clause
--    catches every Cluster (all of which are in the plot); the 630909ca/x<0
--    clause catches her in-plot Blueberry instances.
UPDATE village_object
SET asset_id       = '630909ca-df4f-43ac-9fc4-5192ca44da73',
    owner_actor_id = '019dbcec-1149-7149-8a49-2cdb54680b86',  -- Prudence Ward
    current_state  = 'berries'
WHERE asset_id = 'd699b17f-c743-48d4-8bb9-debaba884a55'
   OR (asset_id = '630909ca-df4f-43ac-9fc4-5192ca44da73' AND x < 0);

-- One forage-to-sell refresh row per just-migrated farm bush (amount 0 = harvest
-- to SELL, not eat in place; finite supply 10, regrow over 168h = 7 real days).
-- Sourced from the rows the UPDATE above produced so the two steps can't diverge.
INSERT INTO object_refresh
    (object_id, attribute, amount, max_quantity, available_quantity,
     refresh_mode, refresh_period_hours, gather_item)
SELECT id, 'hunger', 0, 10, 10, 'periodic', 168, 'blueberries'
FROM village_object
WHERE asset_id = '630909ca-df4f-43ac-9fc4-5192ca44da73'
  AND owner_actor_id = '019dbcec-1149-7149-8a49-2cdb54680b86'
  AND x < 0
ON CONFLICT (object_id, attribute) DO NOTHING;

-- 5. The 8 far-SE Blueberry instances (x>0, unowned) become wild edible bushes,
--    mirroring the loose raspberries: eat in place (amount -8 hunger), finite
--    supply 3, regrow over 6h. Asset and owner are unchanged; only ripen them.
UPDATE village_object
SET current_state = 'berries'
WHERE asset_id = '630909ca-df4f-43ac-9fc4-5192ca44da73'
  AND x > 0
  AND owner_actor_id IS NULL;

INSERT INTO object_refresh
    (object_id, attribute, amount, max_quantity, available_quantity,
     refresh_mode, refresh_period_hours, gather_item)
SELECT id, 'hunger', -8, 3, 3, 'periodic', 6, 'blueberries'
FROM village_object
WHERE asset_id = '630909ca-df4f-43ac-9fc4-5192ca44da73'
  AND x > 0
  AND owner_actor_id IS NULL
ON CONFLICT (object_id, attribute) DO NOTHING;

COMMIT;
