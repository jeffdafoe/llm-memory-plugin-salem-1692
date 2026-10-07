-- LLM-710 down: remove the wardrobe goods and dyes, then rename the four
-- working/warm garments back to linens, woolens, homespun and coat. The rename
-- block is the up migration's with the mapping reversed.
--
-- Deleting an item_kind fails loud if a pay_ledger row (or another FK) still
-- names one of the new goods; that history must be dealt with by hand.

BEGIN;

DO $$
DECLARE
    goods text[] := ARRAY['headscarf', 'straw_hat', 'felt_hat', 'brimmed_hat', 'suspenders',
                          'spectacles', 'mantle', 'scarf', 'long_dress', 'frilly_skirt',
                          'frilly_dress', 'knee_socks', 'boots', 'cuffed_boots', 'gloves',
                          'indigo', 'greenweed', 'weld', 'madder', 'logwood'];
BEGIN
    UPDATE actor_attribute aa
       SET params = jsonb_set(aa.params, '{restock}',
               COALESCE((SELECT jsonb_agg(entry ORDER BY ord)
                           FROM jsonb_array_elements(aa.params->'restock') WITH ORDINALITY AS t(entry, ord)
                          WHERE NOT (entry->>'item' = ANY(goods))), '[]'::jsonb))
     WHERE jsonb_typeof(aa.params->'restock') = 'array'
       AND EXISTS (SELECT 1 FROM jsonb_array_elements(aa.params->'restock') e WHERE e->>'item' = ANY(goods));
    DELETE FROM actor_inventory WHERE item_kind = ANY(goods);
    DELETE FROM item_recipe WHERE output_item = ANY(goods);
    DELETE FROM item_kind WHERE name = ANY(goods);
END $$;

UPDATE item_recipe SET wholesale_price = 8, retail_price = 14, updated_at = now() WHERE output_item = 'stockings';

CREATE TEMP TABLE llm710_rename (old_name text, new_name text, label text, singular text, plural text, descr text) ON COMMIT DROP;
INSERT INTO llm710_rename (old_name, new_name, label, singular, plural, descr) VALUES
    ('linen_shirt', 'linens', 'Linens', 'set of linens', 'sets of linens',
     'Plain linen worn next to the skin, and washed oftener than anything else.'),
    ('vest', 'woolens', 'Woolens', 'set of woolens', 'sets of woolens',
     'Stout wool cut for the working day, and mended more than once.'),
    ('stockings', 'homespun', 'Homespun', 'suit of homespun', 'suits of homespun',
     'Rough cloth woven at home, worn by most folk on most days.'),
    ('mantled_cloak', 'coat', 'Coat', 'coat', 'coats',
     'A heavy wool coat, long against the wind and the rain.');

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
        RETURN;  -- already reverted
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

COMMIT;
