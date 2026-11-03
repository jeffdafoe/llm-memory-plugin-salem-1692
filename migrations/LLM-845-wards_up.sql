-- LLM-845: wards — a horseshoe over the tent door, a witch bottle by the
-- hearth (omens slice 4).
--
-- WHY. Jeff (10-29, specced 11-03): folk practice against witchcraft gives a
-- player something to do with their fear, and is a small coin sink. A ward
-- is a pack good the player places at their pitched tent (the engine keeps it
-- as a tag on the tent object, sim.WardKinds). The game never says it works.
--
-- WHAT THIS DOES:
--   1. horseshoe (already in the catalog, never made or sold): gains the
--      `ward` capability, and a forge recipe for Ezekiel — a bar of iron and
--      a little water make two shoes (the skillet / nail shape). His
--      blacksmith restock gains {horseshoe, produce, max 2}, so his stall
--      carries a few.
--   2. witch_bottle (new): a stoneware jug from the factor's import pack
--      (sim factorImportKinds), dearer and rarer than a horseshoe. A price
--      anchor (inert item_recipe), a Josiah buy line and a small starter shelf,
--      the LLM-846 lantern shape.
--
-- Checkpoint-written tables (actor_attribute, actor_inventory); the deploy
-- runs migrations with the engine stopped.

BEGIN;

-- 1a. The horseshoe gains `ward`; its other capabilities are kept.
UPDATE item_kind
   SET capabilities = array_append(capabilities, 'ward')
 WHERE name = 'horseshoe'
   AND NOT ('ward' = ANY(capabilities));

-- A fresh schema-only database has no horseshoe row: add it whole.
INSERT INTO item_kind
    (name, display_label, display_label_singular, display_label_plural,
     category, sort_order, capabilities, description)
VALUES
    ('horseshoe', 'Horseshoe', 'horseshoe', 'horseshoes', 'tool', 330, '{portable,ward}',
     'A horseshoe of wrought iron with its nail holes punched; many a household nails one over the door for luck.')
ON CONFLICT (name) DO NOTHING;

-- 1b. The forge recipe: one bar of iron and a little water, two shoes an hour.
INSERT INTO item_recipe
    (output_item, output_qty, rate_qty, rate_per_hours, inputs,
     wholesale_price, retail_price)
VALUES
    ('horseshoe', 2, 2, 1, '[{"item": "iron", "qty": 1}, {"item": "water", "qty": 1}]'::jsonb, 2, 3)
ON CONFLICT (output_item) DO UPDATE SET
    output_qty      = EXCLUDED.output_qty,
    rate_qty        = EXCLUDED.rate_qty,
    rate_per_hours  = EXCLUDED.rate_per_hours,
    inputs          = EXCLUDED.inputs,
    wholesale_price = EXCLUDED.wholesale_price,
    retail_price    = EXCLUDED.retail_price,
    updated_at      = now();

-- 1c. Ezekiel Crane's blacksmith restock gains the horseshoe produce line.
--     Corrective: every existing horseshoe entry is stripped before the
--     canonical one is appended, so a rerun converges on exactly one.
UPDATE actor_attribute
   SET params = jsonb_set(
       params,
       '{restock}',
       COALESCE(
           (SELECT jsonb_agg(e)
              FROM jsonb_array_elements(
                   CASE WHEN jsonb_typeof(params->'restock') = 'array'
                        THEN params->'restock' ELSE '[]'::jsonb END) AS e
             WHERE e->>'item' IS DISTINCT FROM 'horseshoe'),
           '[]'::jsonb
       ) || '[{"item": "horseshoe", "source": "produce", "max": 2}]'::jsonb
   )
 WHERE actor_id = '019da6f9-1b4c-7dda-bb6b-3248cdafb2c4'  -- Ezekiel Crane
   AND slug = 'blacksmith';

-- 2a. The witch bottle.
INSERT INTO item_kind
    (name, display_label, display_label_singular, display_label_plural,
     category, sort_order, capabilities, description)
VALUES
    ('witch_bottle', 'Witch bottle', 'witch bottle', 'witch bottles', 'charm', 335, '{portable,ward}',
     'A brown stoneware jug with a bearded face on its neck, come over from the Rhineland by way of London. Country people bury one by the hearth to turn ill-wishing away.')
ON CONFLICT (name) DO UPDATE SET
    display_label          = EXCLUDED.display_label,
    display_label_singular = EXCLUDED.display_label_singular,
    display_label_plural   = EXCLUDED.display_label_plural,
    category               = EXCLUDED.category,
    sort_order             = EXCLUDED.sort_order,
    capabilities           = EXCLUDED.capabilities,
    description            = EXCLUDED.description;

INSERT INTO item_recipe
    (output_item, output_qty, rate_qty, rate_per_hours, inputs,
     wholesale_price, retail_price)
VALUES
    ('witch_bottle', 1, 1, 1, '[]'::jsonb, 4, 8)
ON CONFLICT (output_item) DO UPDATE SET
    output_qty      = EXCLUDED.output_qty,
    rate_qty        = EXCLUDED.rate_qty,
    rate_per_hours  = EXCLUDED.rate_per_hours,
    inputs          = EXCLUDED.inputs,
    wholesale_price = EXCLUDED.wholesale_price,
    retail_price    = EXCLUDED.retail_price,
    updated_at      = now();

-- 2b. The distributor's buy line and a one-time starter shelf, resolved from
--     the distributor tag as LLM-846.
DO $$
DECLARE
    distributor_id uuid;
    added          int;
BEGIN
    SELECT vo.owner_actor_id::uuid INTO distributor_id
      FROM village_object vo
     WHERE 'distributor' = ANY(vo.tags)
       AND vo.owner_actor_id IS NOT NULL
     LIMIT 1;
    IF distributor_id IS NULL THEN
        RETURN;  -- fresh schema-only database
    END IF;
    IF NOT EXISTS (SELECT 1 FROM actor_attribute
                    WHERE actor_id = distributor_id AND slug = 'merchant') THEN
        RAISE EXCEPTION 'LLM-845: the distributor has no merchant actor_attribute row to hold the witch bottle line';
    END IF;
    UPDATE actor_attribute aa
       SET params = jsonb_set(
               aa.params,
               '{restock}',
               COALESCE(aa.params->'restock', '[]'::jsonb)
                   || '[{"item": "witch_bottle", "source": "buy", "max": 2}]'::jsonb)
     WHERE aa.actor_id = distributor_id
       AND aa.slug = 'merchant'
       AND NOT (COALESCE(aa.params->'restock', '[]'::jsonb) @> '[{"item": "witch_bottle"}]'::jsonb);
    GET DIAGNOSTICS added = ROW_COUNT;
    -- The starter shelf goes in only with the line, so a rerun after the
    -- bottles sold out (their row gone) never restocks them. DO NOTHING never
    -- overwrites live, checkpoint-owned stock.
    IF added > 0 THEN
        INSERT INTO actor_inventory (actor_id, item_kind, quantity)
        VALUES (distributor_id, 'witch_bottle', 2)
        ON CONFLICT (actor_id, item_kind) DO NOTHING;
    END IF;
END $$;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM item_kind WHERE name = 'horseshoe' AND 'ward' = ANY(capabilities)) THEN
        RAISE EXCEPTION 'LLM-845: horseshoe has no ward capability';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM item_kind WHERE name = 'witch_bottle' AND 'ward' = ANY(capabilities)) THEN
        RAISE EXCEPTION 'LLM-845: witch_bottle missing or without ward';
    END IF;
    IF (SELECT count(*) FROM item_recipe WHERE output_item IN ('horseshoe', 'witch_bottle')) <> 2 THEN
        RAISE EXCEPTION 'LLM-845: horseshoe recipe or witch bottle price anchor missing';
    END IF;
    IF EXISTS (SELECT 1 FROM actor WHERE id = '019da6f9-1b4c-7dda-bb6b-3248cdafb2c4')
       AND NOT EXISTS (SELECT 1 FROM actor_attribute
                        WHERE actor_id = '019da6f9-1b4c-7dda-bb6b-3248cdafb2c4' AND slug = 'blacksmith'
                          AND params->'restock' @> '[{"item": "horseshoe", "source": "produce", "max": 2}]'::jsonb) THEN
        RAISE EXCEPTION 'LLM-845: Ezekiel''s horseshoe produce line did not land';
    END IF;
END $$;

COMMIT;
