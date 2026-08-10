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
-- converted tree someone re-owned or re-stated. Both are state this migration
-- did not create, so refuse rather than generalize.
--
-- PROVENANCE IS BY ID, matching the _up. A count of apple trees in a bounding
-- box proves only that twenty exist there, not that they are the twenty this
-- migration converted — swap one for a hand-placed apple and the count is still
-- twenty, and reverting it would destroy legitimate post-migration state
-- (code_review). The pinned list, plus the owner and refresh-shape checks
-- below, means the _down reverts precisely what the _up did or nothing at all.
--
-- Reversibility limit, stated rather than hidden: if the editor has since
-- MOVED one of these trees, its id still matches and the revert still lands —
-- position is not restored, because the _up never changed it. If a tree has
-- been deleted outright, the _down refuses and the orchard has to be finished
-- off by hand.
--
-- The engine must be stopped, same as for the _up.

BEGIN;

DO $$
DECLARE
    maple_id  CONSTANT uuid := '2d91c8a9-6501-4e16-873a-4d18bdc6f63e';
    apple_id  CONSTANT uuid := '019e5f00-c401-7a10-9e00-000000000623';
    prudence  CONSTANT text := '019dbcec-1149-7149-8a49-2cdb54680b86';
    grove_ids CONSTANT uuid[] := ARRAY[
        '019da68e-1ac4-75f4-9164-ad6bfb303a5d'::uuid,
        '019da68e-39fe-7cf2-bc72-39d0d02a323b'::uuid,
        '019da68d-f208-77af-b331-b64de9723109'::uuid,
        '019da68d-a3ce-723a-91c6-3381b169eee2'::uuid,
        '019da68b-e156-776e-a0e4-46c8c3b2f2f5'::uuid,
        '019da68e-6c2d-7c41-8ab5-8f39f4426f55'::uuid,
        '019da68e-dd93-7209-9614-ba403ade9640'::uuid,
        '019da68c-335c-770e-88d5-166179ad0f0f'::uuid,
        '019da68f-7458-7c2f-a67e-822964956bac'::uuid,
        '019da690-07a2-7a66-8d97-db76d77d5648'::uuid,
        '019da68f-0487-7574-a120-4161cc983632'::uuid,
        '019da68f-a92e-7120-8527-c5bc88ed56fa'::uuid,
        '019da68e-86dc-78b0-9125-ea4aa5ce392a'::uuid,
        '019da68c-94ff-71d8-be5d-efcbed2fc4ae'::uuid,
        '019da690-2863-7213-bb2e-76db22f5ddb1'::uuid,
        '019da68e-a71b-7878-b122-f938c7f1db4a'::uuid,
        '019da68f-2271-76e1-ac8c-88af8e53c194'::uuid,
        '019da68c-cb14-7f28-a7c3-ff971e40feb7'::uuid,
        '019da68f-dee7-74a3-bb0b-2da1226b906a'::uuid,
        '019da690-4df2-75ce-bc2a-30180d5a5132'::uuid];
    total int;
    pinned int;
    seeded_rows int;
    restored int;
BEGIN
    SELECT count(*) INTO total FROM village_object WHERE asset_id = apple_id;

    -- Fresh schema-only DB, or a village where the _up's part 5 short-circuited.
    IF total = 0 THEN
        RETURN;
    END IF;

    -- Every apple tree in the world must be one this migration made. A
    -- hand-placed one would otherwise be orphaned by the asset delete at the
    -- bottom of this file.
    IF total <> 20 THEN
        RAISE EXCEPTION
            'LLM-623 down: found % apple tree(s), expected the 20 converted grove trees — remove any hand-placed apple trees first',
            total;
    END IF;

    SELECT count(*) INTO pinned
      FROM village_object
     WHERE id = ANY(grove_ids) AND asset_id = apple_id AND owner_actor_id = prudence;
    IF pinned <> 20 THEN
        RAISE EXCEPTION
            'LLM-623 down: only % of the 20 pinned grove objects are still apple trees owned by Prudence Ward — refusing to revert objects this migration did not create',
            pinned;
    END IF;

    -- The refresh rows must still be the ones the _up seeded. A retuned yield or
    -- period means someone has been tuning the orchard live, which is state this
    -- migration cannot claim to own.
    SELECT count(*) INTO seeded_rows
      FROM object_refresh
     WHERE object_id = ANY(grove_ids)
       AND gather_item = 'apples'
       AND attribute IS NULL
       AND amount = 0
       AND max_quantity = 3
       AND refresh_mode = 'periodic'
       AND refresh_period_hours = 168;
    IF seeded_rows <> 20 THEN
        RAISE EXCEPTION
            'LLM-623 down: only % of 20 apple refresh rows still have the shape this migration seeded (3 units / 168h periodic) — the orchard has been retuned since',
            seeded_rows;
    END IF;

    -- object_refresh would cascade on an object delete, but nothing is being
    -- deleted here, so the seeded rows come out explicitly. Scoped to the yield
    -- row this migration wrote, so a shade or need row added to one of these
    -- objects since would survive.
    DELETE FROM object_refresh
     WHERE object_id = ANY(grove_ids) AND gather_item = 'apples';

    UPDATE village_object
       SET asset_id = maple_id,
           -- The Maple Tree asset's default_state, which is what all twenty
           -- carried before the _up.
           current_state = 'default',
           -- They were unowned before the _up, which the _up asserted rather
           -- than assumed, so nulling this restores the recorded prior state.
           owner_actor_id = NULL
     WHERE id = ANY(grove_ids);

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

    -- Matched on the COMPLETE entry the _up wrote, not on item/source alone.
    -- An entry whose max has since been retuned — {"max":999,...} — is no
    -- longer this migration's row, and removing it would silently discard
    -- someone's tuning (code_review). jsonb equality ignores key order.
    SELECT count(*) INTO apple_entries
      FROM actor_attribute, jsonb_array_elements(params->'restock') e
     WHERE actor_id::text = prudence AND slug = 'herbalist'
       AND e = '{"max": 10, "item": "apples", "source": "forage"}'::jsonb;
    IF apple_entries <> 1 THEN
        RAISE EXCEPTION
            'LLM-623 down: expected exactly one apples/forage/max-10 entry on Prudence''s herbalist row, found % — it has been retuned since', apple_entries;
    END IF;

    -- COALESCE because jsonb_agg over an EMPTY array returns SQL NULL, and
    -- jsonb_set with a NULL replacement yields NULL for the whole params
    -- document. Her array is not empty today, but the guard costs nothing and
    -- the failure mode is silent data loss.
    UPDATE actor_attribute
       SET params = jsonb_set(params, '{restock}', COALESCE((
               SELECT jsonb_agg(e ORDER BY ord)
                 FROM jsonb_array_elements(params->'restock') WITH ORDINALITY AS t(e, ord)
                WHERE e <> '{"max": 10, "item": "apples", "source": "forage"}'::jsonb
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

    -- Complete-entry match, same reasoning as Prudence's above.
    SELECT count(*) INTO apple_entries
      FROM actor_attribute, jsonb_array_elements(params->'restock') e
     WHERE actor_id::text = josiah AND slug = 'merchant'
       AND e = '{"max": 8, "item": "apples", "source": "buy"}'::jsonb;
    IF apple_entries <> 1 THEN
        RAISE EXCEPTION
            'LLM-623 down: expected exactly one apples/buy/max-8 entry on Josiah''s merchant row, found % — it has been retuned since', apple_entries;
    END IF;

    UPDATE actor_attribute
       SET params = jsonb_set(params, '{restock}', COALESCE((
               SELECT jsonb_agg(e ORDER BY ord)
                 FROM jsonb_array_elements(params->'restock') WITH ORDINALITY AS t(e, ord)
                WHERE e <> '{"max": 8, "item": "apples", "source": "buy"}'::jsonb
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
