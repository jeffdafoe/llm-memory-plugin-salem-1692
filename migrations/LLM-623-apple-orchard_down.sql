-- LLM-623 down: the orchard reverts to a maple grove and apples cease to exist.
--
-- Unlike the wheat and carrot downs, this one DOES touch village_object — it
-- has to, because the _up did not place new plants, it converted twenty
-- existing ones. Refusing on "trees are still placed" the way LLM-603 does
-- would make this migration unrollable by construction, since the twenty are
-- exactly what the _up created. So the conversion is reversed instead, and the
-- guard is on the SHAPE: it restores only trees it can prove this migration
-- made, and refuses anything else.
--
-- Anything else means: an apple tree the editor placed after the fact, or a
-- converted tree someone moved outside the grove's bounding box. Both are state
-- this migration did not create, so refuse rather than generalize.
--
-- The engine must be stopped, same as for the _up.

BEGIN;

DO $$
DECLARE
    maple_id CONSTANT uuid := '2d91c8a9-6501-4e16-873a-4d18bdc6f63e';
    apple_id CONSTANT uuid := '019e5f00-c401-7a10-9e00-000000000623';
    total int;
    in_grove int;
    restored int;
BEGIN
    SELECT count(*) INTO total FROM village_object WHERE asset_id = apple_id;

    -- Fresh schema-only DB, or a village where the _up's part 5 short-circuited.
    IF total = 0 THEN
        RETURN;
    END IF;

    SELECT count(*) INTO in_grove
      FROM village_object
     WHERE asset_id = apple_id
       AND x BETWEEN 3000 AND 4000
       AND y BETWEEN -600 AND -50;

    IF total <> 20 OR in_grove <> 20 THEN
        RAISE EXCEPTION
            'LLM-623 down: expected exactly the 20 converted grove trees, found % apple tree(s) of which % in the grove — remove any hand-placed apple trees first',
            total, in_grove;
    END IF;

    -- object_refresh would cascade on an object delete, but nothing is being
    -- deleted here, so the seeded rows come out explicitly. Scoped to the yield
    -- row this migration wrote, so a shade or need row added to one of these
    -- objects since would survive.
    DELETE FROM object_refresh
     WHERE gather_item = 'apples'
       AND object_id IN (SELECT id FROM village_object WHERE asset_id = apple_id);

    UPDATE village_object
       SET asset_id = maple_id,
           -- The Maple Tree asset's default_state, which is what all twenty
           -- carried before the _up.
           current_state = 'default',
           -- They were unowned before the _up; owner_actor_id was NULL.
           owner_actor_id = NULL
     WHERE asset_id = apple_id;

    GET DIAGNOSTICS restored = ROW_COUNT;
    IF restored <> 20 THEN
        RAISE EXCEPTION 'LLM-623 down: restored % rows to Maple Tree, expected 20', restored;
    END IF;
END $$;

-- ---------------------------------------------------------------------------
-- Restock entries
-- ---------------------------------------------------------------------------

-- Mirror of the _up's discipline: assert the shape before mutating, scope the
-- removal to item AND source, assert the outcome. Removing only the exact entry
-- the _up added — any other shape means state this migration did not create.
DO $$
DECLARE
    prudence CONSTANT text := '019dbcec-1149-7149-8a49-2cdb54680b86';
    apple_entries int;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM actor WHERE id::text = prudence) THEN
        RETURN;
    END IF;

    SELECT count(*) INTO apple_entries
      FROM actor_attribute, jsonb_array_elements(params->'restock') e
     WHERE actor_id::text = prudence AND slug = 'herbalist'
       AND e->>'item' = 'apples' AND e->>'source' = 'forage';
    IF apple_entries <> 1 THEN
        RAISE EXCEPTION
            'LLM-623 down: expected exactly one apples/forage entry on Prudence''s herbalist row, found %', apple_entries;
    END IF;

    -- COALESCE because jsonb_agg over an EMPTY array returns SQL NULL, and
    -- jsonb_set with a NULL replacement yields NULL for the whole params
    -- document. Her array is not empty today, but the guard costs nothing and
    -- the failure mode is silent data loss.
    UPDATE actor_attribute
       SET params = jsonb_set(params, '{restock}', COALESCE((
               SELECT jsonb_agg(e ORDER BY ord)
                 FROM jsonb_array_elements(params->'restock') WITH ORDINALITY AS t(e, ord)
                WHERE NOT (e->>'item' = 'apples' AND e->>'source' = 'forage')
           ), '[]'::jsonb))
     WHERE actor_id::text = prudence
       AND slug = 'herbalist'
       AND jsonb_typeof(params->'restock') = 'array';

    SELECT count(*) INTO apple_entries
      FROM actor_attribute, jsonb_array_elements(params->'restock') e
     WHERE actor_id::text = prudence AND slug = 'herbalist'
       AND e->>'item' = 'apples';
    IF apple_entries <> 0 THEN
        RAISE EXCEPTION 'LLM-623 down: Prudence apples entry removal did not land — % remain', apple_entries;
    END IF;
END $$;

DO $$
DECLARE
    josiah CONSTANT text := '019dcac2-e78a-715e-91b7-101f339b0891';
    apple_entries int;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM actor WHERE id::text = josiah) THEN
        RETURN;
    END IF;

    SELECT count(*) INTO apple_entries
      FROM actor_attribute, jsonb_array_elements(params->'restock') e
     WHERE actor_id::text = josiah AND slug = 'merchant'
       AND e->>'item' = 'apples' AND e->>'source' = 'buy';
    IF apple_entries <> 1 THEN
        RAISE EXCEPTION
            'LLM-623 down: expected exactly one apples/buy entry on Josiah''s merchant row, found %', apple_entries;
    END IF;

    UPDATE actor_attribute
       SET params = jsonb_set(params, '{restock}', COALESCE((
               SELECT jsonb_agg(e ORDER BY ord)
                 FROM jsonb_array_elements(params->'restock') WITH ORDINALITY AS t(e, ord)
                WHERE NOT (e->>'item' = 'apples' AND e->>'source' = 'buy')
           ), '[]'::jsonb))
     WHERE actor_id::text = josiah
       AND slug = 'merchant'
       AND jsonb_typeof(params->'restock') = 'array';

    SELECT count(*) INTO apple_entries
      FROM actor_attribute, jsonb_array_elements(params->'restock') e
     WHERE actor_id::text = josiah AND slug = 'merchant'
       AND e->>'item' = 'apples';
    IF apple_entries <> 0 THEN
        RAISE EXCEPTION 'LLM-623 down: Josiah apples entry removal did not land — % remain', apple_entries;
    END IF;
END $$;

-- ---------------------------------------------------------------------------
-- Item + asset
-- ---------------------------------------------------------------------------

-- An apple anyone is still holding, or that appears in a settled trade, would
-- block the item_kind delete on an FK — actor_inventory and pay_ledger both
-- reference it. Refuse with a legible message rather than let the constraint
-- speak for itself.
DO $$
DECLARE held int; traded int;
BEGIN
    SELECT coalesce(sum(quantity), 0) INTO held FROM actor_inventory WHERE item_kind = 'apples';
    SELECT count(*) INTO traded FROM pay_ledger WHERE item_kind = 'apples';
    IF held > 0 OR traded > 0 THEN
        RAISE EXCEPTION
            'LLM-623 down: apples are in circulation (% held, % ledger row(s)) — the item cannot be removed while it is referenced',
            held, traded;
    END IF;
END $$;

-- item_satisfies and item_recipe both cascade from item_kind.
DELETE FROM item_kind WHERE name = 'apples';

-- asset_state_tag cascades from asset_state.
DELETE FROM asset_state WHERE asset_id = '019e5f00-c401-7a10-9e00-000000000623';
DELETE FROM asset_refresh_default WHERE asset_id = '019e5f00-c401-7a10-9e00-000000000623';
DELETE FROM asset WHERE id = '019e5f00-c401-7a10-9e00-000000000623';

-- This migration introduced the pack, so its down owns the cleanup — guarded by
-- NOT EXISTS over remaining assets, the same split LLM-600/LLM-601 use for
-- mana-seed-fishing. A second fruit tree added later would keep it.
DELETE FROM tileset_pack
 WHERE id = 'mana-seed-fruit-trees'
   AND NOT EXISTS (SELECT 1 FROM asset WHERE pack_id = 'mana-seed-fruit-trees');

COMMIT;
