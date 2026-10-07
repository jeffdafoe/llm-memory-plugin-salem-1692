-- LLM-710: the clothing goods become the character creator's wardrobe pieces,
-- and the pieces and colours a player does not get for free become goods sold
-- at the Store.
--
-- WHY. The LLM-691 creator offered every garment and every colour to every
-- player, while the village traded five clothing goods (coat, cloak, homespun,
-- woolens, linens) that had nothing to do with what anyone looks like. Jeff
-- (10-07): give a player a plain starter set, and sell the rest from Josiah.
--
-- WHAT THIS DOES.
--   1. Renames four goods onto wardrobe pieces and re-labels the cloak:
--          linens   -> linen_shirt    (working garment)
--          woolens  -> vest           (working garment)
--          homespun -> stockings      (working garment)
--          coat     -> mantled_cloak  (warms)
--      Only pieces both sexes wore are used: the engine models no sex, and the
--      working-clothes cue must never tell a villager to buy breeches or a dress
--      (the LLM-596 rule). Breeches, the long skirt, the linen shirt's free
--      starter copy and shoes are free in the creator and are not goods.
--   2. Adds one good per remaining wardrobe piece (hats, headscarf, suspenders,
--      spectacles, mantle, scarf, the dresses and frilly skirt, knee socks,
--      boots, cuffed boots, gloves) and five dyes (indigo, greenweed, weld,
--      madder, logwood). Which colours each dye unlocks is engine data
--      (sim.farmerDyes); a dye is held, not spent. These goods have no wear
--      budget, so none of them is a working garment.
--   3. Gives the distributor a buy line for each new good, and a one-time
--      starter shelf (one of each garment, two of each dye) so players can buy
--      before the first factor brings a bale.
--
-- The rename follows LLM-596: every FK onto item_kind(name) is ON UPDATE
-- CASCADE (asserted below, not assumed), and the jsonb surfaces that hold item
-- names are rewritten field by field. History is rewritten with it: a ledger
-- row for a coat reads as a mantled_cloak.
--
-- Checkpoint-written tables throughout; the deploy runs migrations with the
-- engine stopped.

BEGIN;

CREATE TEMP TABLE llm710_rename (old_name text, new_name text, label text, singular text, plural text, descr text) ON COMMIT DROP;
INSERT INTO llm710_rename (old_name, new_name, label, singular, plural, descr) VALUES
    ('linens', 'linen_shirt', 'Linen shirt', 'linen shirt', 'linen shirts',
     'A long shirt of plain linen, worn next to the skin and washed oftener than anything else.'),
    ('woolens', 'vest', 'Vest', 'vest', 'vests',
     'A sleeveless wool vest, the stout layer of the working day.'),
    ('homespun', 'stockings', 'Stockings', 'pair of stockings', 'pairs of stockings',
     'Knitted wool stockings, darned at the heel more than once.'),
    ('coat', 'mantled_cloak', 'Mantled cloak', 'mantled cloak', 'mantled cloaks',
     'A heavy wool cloak with a shoulder mantle, long against the wind and the rain.');

-- Rewrites the item name under key in every element of a jsonb array of
-- objects; other elements and fields are kept as they are.
CREATE OR REPLACE FUNCTION pg_temp.llm710_rename_array(arr jsonb, key text) RETURNS jsonb AS $$
    SELECT CASE WHEN arr IS NULL OR jsonb_typeof(arr) <> 'array' THEN arr
           ELSE COALESCE((SELECT jsonb_agg(
                            CASE WHEN r.new_name IS NULL THEN entry
                                 ELSE jsonb_set(entry, ARRAY[key], to_jsonb(r.new_name))
                            END ORDER BY ord)
                       FROM jsonb_array_elements(arr) WITH ORDINALITY AS t(entry, ord)
                       LEFT JOIN llm710_rename r ON r.old_name = entry->>key), '[]'::jsonb)
           END
$$ LANGUAGE sql;

-- True when the array names a retired kind under key.
CREATE OR REPLACE FUNCTION pg_temp.llm710_names_old(arr jsonb, key text) RETURNS boolean AS $$
    SELECT jsonb_typeof(arr) = 'array'
       AND EXISTS (SELECT 1 FROM jsonb_array_elements(arr) e
                     JOIN llm710_rename r ON r.old_name = e->>key)
$$ LANGUAGE sql;

DO $$
DECLARE
    old_present int;
    new_present int;
    renamed int;
    rec record;
    plan_new jsonb;
    inv jsonb;
    mapped text;
    leftover text := NULL;
    found bool;
BEGIN
    SELECT count(*) INTO old_present FROM item_kind k JOIN llm710_rename r ON r.old_name = k.name;
    SELECT count(*) INTO new_present FROM item_kind k JOIN llm710_rename r ON r.new_name = k.name;

    IF old_present = 0 AND new_present = 4 THEN
        RETURN;  -- already applied
    ELSIF old_present = 0 AND new_present = 0 THEN
        RETURN;  -- fresh schema-only database (the integration harness)
    ELSIF old_present <> 4 OR new_present <> 0 THEN
        RAISE EXCEPTION 'LLM-710: refusing to run against a mixed catalog — % of 4 old names and % of 4 target names present', old_present, new_present;
    END IF;

    UPDATE item_kind k
       SET name                   = r.new_name,
           display_label          = r.label,
           display_label_singular = r.singular,
           display_label_plural   = r.plural,
           description            = r.descr
      FROM llm710_rename r
     WHERE k.name = r.old_name;

    GET DIAGNOSTICS renamed = ROW_COUNT;
    IF renamed <> 4 THEN
        RAISE EXCEPTION 'LLM-710: expected to rename 4 garment kinds, renamed %', renamed;
    END IF;

    -- jsonb: restock policies.
    UPDATE actor_attribute aa
       SET params = jsonb_set(aa.params, '{restock}', pg_temp.llm710_rename_array(aa.params->'restock', 'item'))
     WHERE pg_temp.llm710_names_old(aa.params->'restock', 'item');

    -- jsonb: recipe input lists. No recipe consumes a garment today; rewritten
    -- anyway so the guarantee does not rest on that staying true.
    UPDATE item_recipe
       SET inputs       = pg_temp.llm710_rename_array(inputs, 'item'),
           boost_inputs = pg_temp.llm710_rename_array(boost_inputs, 'item'),
           speed_inputs = pg_temp.llm710_rename_array(speed_inputs, 'item')
     WHERE pg_temp.llm710_names_old(inputs, 'item')
        OR pg_temp.llm710_names_old(boost_inputs, 'item')
        OR pg_temp.llm710_names_old(speed_inputs, 'item');

    -- jsonb: in-kind wages of open labor contracts.
    UPDATE labor_contract
       SET reward_items = pg_temp.llm710_rename_array(reward_items, 'kind')
     WHERE pg_temp.llm710_names_old(reward_items, 'kind');

    -- jsonb: standing input shortages.
    UPDATE world_state
       SET input_shortages = pg_temp.llm710_rename_array(input_shortages, 'item')
     WHERE pg_temp.llm710_names_old(input_shortages, 'item');

    -- jsonb: in-flight visitor plans, FIELD-AWARE (LLM-596): item names sit in
    -- trade.good and as keys of inventory; the rest is names and narrative.
    FOR rec IN SELECT actor_id, plan FROM visitor WHERE plan IS NOT NULL LOOP
        plan_new := rec.plan;

        IF plan_new #>> '{trade,good}' IS NOT NULL THEN
            SELECT r.new_name INTO mapped FROM llm710_rename r WHERE r.old_name = plan_new #>> '{trade,good}';
            IF mapped IS NOT NULL THEN
                plan_new := jsonb_set(plan_new, '{trade,good}', to_jsonb(mapped));
            END IF;
        END IF;

        IF jsonb_typeof(plan_new->'inventory') = 'object' THEN
            SELECT jsonb_object_agg(COALESCE(r.new_name, kv.key), kv.value) INTO inv
              FROM jsonb_each(plan_new->'inventory') kv
              LEFT JOIN llm710_rename r ON r.old_name = kv.key;
            IF inv IS NOT NULL THEN
                plan_new := jsonb_set(plan_new, '{inventory}', inv);
            END IF;
        END IF;

        IF plan_new IS DISTINCT FROM rec.plan THEN
            UPDATE visitor SET plan = plan_new WHERE actor_id = rec.actor_id;
        END IF;
    END LOOP;

    -- Nothing may still refer to the retired names. The FK surfaces are
    -- discovered from the catalog, which also asserts every one cascades.
    FOR rec IN
        SELECT c.conrelid::regclass::text AS tbl, a.attname AS col
          FROM pg_constraint c
          JOIN LATERAL unnest(c.conkey) WITH ORDINALITY AS k(attnum, ord) ON true
          JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.attnum
         WHERE c.confrelid = 'item_kind'::regclass AND c.contype = 'f'
    LOOP
        EXECUTE format(
            'SELECT EXISTS (SELECT 1 FROM %s WHERE %I IN (SELECT old_name FROM llm710_rename))',
            rec.tbl, rec.col) INTO found;
        IF found THEN
            leftover := concat_ws(', ', leftover, rec.tbl || '.' || rec.col);
        END IF;
    END LOOP;

    IF EXISTS (SELECT 1 FROM actor_attribute WHERE pg_temp.llm710_names_old(params->'restock', 'item')) THEN
        leftover := concat_ws(', ', leftover, 'restock policy');
    END IF;
    IF EXISTS (SELECT 1 FROM item_recipe
                WHERE pg_temp.llm710_names_old(inputs, 'item')
                   OR pg_temp.llm710_names_old(boost_inputs, 'item')
                   OR pg_temp.llm710_names_old(speed_inputs, 'item')) THEN
        leftover := concat_ws(', ', leftover, 'item_recipe inputs');
    END IF;
    IF EXISTS (SELECT 1 FROM labor_contract WHERE pg_temp.llm710_names_old(reward_items, 'kind')) THEN
        leftover := concat_ws(', ', leftover, 'labor_contract.reward_items');
    END IF;
    IF EXISTS (SELECT 1 FROM world_state WHERE pg_temp.llm710_names_old(input_shortages, 'item')) THEN
        leftover := concat_ws(', ', leftover, 'world_state.input_shortages');
    END IF;
    IF EXISTS (SELECT 1 FROM visitor v
                WHERE v.plan IS NOT NULL
                  AND (v.plan #>> '{trade,good}' IN (SELECT old_name FROM llm710_rename)
                       OR EXISTS (SELECT 1 FROM jsonb_each(COALESCE(v.plan->'inventory', '{}'::jsonb)) kv
                                   WHERE kv.key IN (SELECT old_name FROM llm710_rename)))) THEN
        leftover := concat_ws(', ', leftover, 'visitor.plan');
    END IF;

    IF leftover IS NOT NULL THEN
        RAISE EXCEPTION 'LLM-710: retired garment names still referenced in: %', leftover;
    END IF;
END $$;
DROP FUNCTION pg_temp.llm710_rename_array(jsonb, text);
DROP FUNCTION pg_temp.llm710_names_old(jsonb, text);

-- The working garments' prices follow their new pieces: stockings are far
-- cheaper than the suit of homespun they replace.
UPDATE item_recipe SET wholesale_price = 3, retail_price = 5, updated_at = now() WHERE output_item = 'stockings';

-- The new goods. Clothing in the 200s after the renamed garments; dyes their
-- own category. ON CONFLICT DO UPDATE is corrective, as in LLM-410: it
-- promotes an engine-minted discovery row of the same name to the real good.
CREATE TEMP TABLE llm710_goods (name text, label text, singular text, plural text, category text, sort_order int, wholesale int, retail int, shelf int, line_max int, descr text) ON COMMIT DROP;
INSERT INTO llm710_goods VALUES
    ('headscarf',    'Headscarf',    'headscarf',          'headscarves',         'clothing', 230, 1, 3,  1, 2, 'A square of cloth tied over the hair.'),
    ('straw_hat',    'Straw hat',    'straw hat',          'straw hats',          'clothing', 231, 2, 4,  1, 2, 'A broad hat of plaited straw, for the field and the sun.'),
    ('felt_hat',     'Felt hat',     'felt hat',           'felt hats',           'clothing', 232, 4, 7,  1, 2, 'A soft felt hat with a wide brim.'),
    ('brimmed_hat',  'Brimmed hat',  'brimmed hat',        'brimmed hats',        'clothing', 233, 4, 8,  1, 2, 'A stiff hat with a flat brim and a band.'),
    ('suspenders',   'Suspenders',   'pair of suspenders', 'pairs of suspenders', 'clothing', 234, 2, 4,  1, 2, 'Cloth straps over the shoulders to hold up breeches or a skirt.'),
    ('spectacles',   'Spectacles',   'pair of spectacles', 'pairs of spectacles', 'clothing', 235, 6, 12, 1, 2, 'Round glass lenses in a wire frame, brought from the city.'),
    ('mantle',       'Mantle',       'mantle',             'mantles',             'clothing', 236, 4, 7,  1, 2, 'A short cape worn over the shoulders.'),
    ('scarf',        'Scarf',        'scarf',              'scarves',             'clothing', 237, 2, 4,  1, 2, 'A length of wool wound about the neck.'),
    ('long_dress',   'Long dress',   'long dress',         'long dresses',        'clothing', 238, 6, 11, 1, 2, 'A plain dress to the ankle.'),
    ('frilly_skirt', 'Frilly skirt', 'frilly skirt',       'frilly skirts',       'clothing', 239, 6, 10, 1, 2, 'A full skirt with a frill at the hem.'),
    ('frilly_dress', 'Frilly dress', 'frilly dress',       'frilly dresses',      'clothing', 240, 9, 16, 1, 2, 'A dress with frills at the hem and the sleeves, for the meeting-house.'),
    ('knee_socks',   'Knee socks',   'pair of knee socks', 'pairs of knee socks', 'clothing', 241, 2, 4,  1, 2, 'Short wool socks that reach the knee.'),
    ('boots',        'Boots',        'pair of boots',      'pairs of boots',      'clothing', 242, 6, 10, 1, 2, 'Stout leather boots for mud and snow.'),
    ('cuffed_boots', 'Cuffed boots', 'pair of cuffed boots', 'pairs of cuffed boots', 'clothing', 243, 8, 13, 1, 2, 'Tall leather boots with a turned-down cuff.'),
    ('gloves',       'Gloves',       'pair of gloves',     'pairs of gloves',     'clothing', 244, 3, 5,  1, 2, 'Leather gloves for rough work.'),
    ('indigo',       'Indigo',       'cake of indigo',     'cakes of indigo',     'dye',      260, 4, 7,  2, 3, 'A cake of indigo from the islands. It dyes cloth blue.'),
    ('greenweed',    'Greenweed',    'bundle of greenweed', 'bundles of greenweed', 'dye',    261, 2, 4,  2, 3, 'Dried dyer''s greenweed. It dyes cloth green.'),
    ('weld',         'Weld',         'bundle of weld',     'bundles of weld',     'dye',      262, 2, 4,  2, 3, 'Dried weld. It dyes cloth yellow.'),
    ('madder',       'Madder',       'bag of madder',      'bags of madder',      'dye',      263, 3, 6,  2, 3, 'Ground madder root. It dyes cloth red.'),
    ('logwood',      'Logwood',      'bag of logwood',     'bags of logwood',     'dye',      264, 7, 12, 2, 3, 'Logwood chips from the Spanish Main. They dye cloth purple.');

INSERT INTO item_kind
    (name, display_label, display_label_singular, display_label_plural,
     category, sort_order, capabilities, description)
SELECT name, label, singular, plural, category, sort_order, '{}'::text[], descr
  FROM llm710_goods
ON CONFLICT (name) DO UPDATE SET
    display_label          = EXCLUDED.display_label,
    display_label_singular = EXCLUDED.display_label_singular,
    display_label_plural   = EXCLUDED.display_label_plural,
    category               = EXCLUDED.category,
    sort_order             = EXCLUDED.sort_order,
    capabilities           = EXCLUDED.capabilities,
    description            = EXCLUDED.description;

-- Price anchors: inert recipes (no producer, no inputs), as LLM-410.
INSERT INTO item_recipe
    (output_item, output_qty, rate_qty, rate_per_hours, inputs,
     wholesale_price, retail_price)
SELECT name, 1, 1, 1, '[]'::jsonb, wholesale, retail
  FROM llm710_goods
ON CONFLICT (output_item) DO UPDATE SET
    output_qty      = EXCLUDED.output_qty,
    rate_qty        = EXCLUDED.rate_qty,
    rate_per_hours  = EXCLUDED.rate_per_hours,
    inputs          = EXCLUDED.inputs,
    wholesale_price = EXCLUDED.wholesale_price,
    retail_price    = EXCLUDED.retail_price,
    updated_at      = now();

-- The distributor's buy lines and starter shelf. Resolved from the distributor
-- tag, as LLM-592.
DO $$
DECLARE
    distributor_id uuid;
    missing text;
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
        RAISE EXCEPTION 'LLM-710: the distributor has no merchant actor_attribute row to hold the wardrobe lines';
    END IF;

    UPDATE actor_attribute aa
       SET params = jsonb_set(
               aa.params,
               '{restock}',
               COALESCE(aa.params->'restock', '[]'::jsonb) || (
                   SELECT COALESCE(jsonb_agg(
                              jsonb_build_object('item', g.name, 'source', 'buy', 'max', g.line_max)
                              ORDER BY g.sort_order), '[]'::jsonb)
                     FROM llm710_goods g
                    WHERE NOT (COALESCE(aa.params->'restock', '[]'::jsonb)
                               @> jsonb_build_array(jsonb_build_object('item', g.name)))))
     WHERE aa.actor_id = distributor_id
       AND aa.slug = 'merchant';

    SELECT string_agg(g.name, ', ' ORDER BY g.name) INTO missing
      FROM llm710_goods g
     WHERE NOT EXISTS (
           SELECT 1 FROM actor_attribute aa
            WHERE aa.actor_id = distributor_id
              AND aa.slug = 'merchant'
              AND aa.params->'restock' @> jsonb_build_array(jsonb_build_object('item', g.name)));
    IF missing IS NOT NULL THEN
        RAISE EXCEPTION 'LLM-710: distributor wardrobe lines still missing after merge: %', missing;
    END IF;

    -- One-time shelf, not a convergent invariant: DO NOTHING never overwrites
    -- live, checkpoint-owned stock.
    INSERT INTO actor_inventory (actor_id, item_kind, quantity)
    SELECT distributor_id, g.name, g.shelf
      FROM llm710_goods g
    ON CONFLICT (actor_id, item_kind) DO NOTHING;
END $$;

COMMIT;
