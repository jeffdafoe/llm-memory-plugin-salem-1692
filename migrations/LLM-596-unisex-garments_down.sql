-- LLM-596 down: put the gendered garment names back.
--
-- Exact mirror of the _up, including the mixed-state guard and the FIELD-AWARE
-- jsonb rewrites the FK cascade cannot reach. Reverting the item_kind names alone
-- would leave every restock policy naming a kind that no longer exists, which
-- does not error — it silently drops the good from both the price line and the
-- restock line.
--
-- The visitor-plan rewrite is field-aware for the same reason as the _up: a
-- blanket text replace would rewrite any JSON string equal to the name anywhere
-- in the document, and an up/down cycle must return unrelated plan content
-- byte-identical.
--
-- Checkpoint-written tables; apply with the engine stopped.

BEGIN;

CREATE TEMP TABLE llm596_rename (old_name text, new_name text, label text, singular text, plural text, descr text) ON COMMIT DROP;
INSERT INTO llm596_rename (old_name, new_name, label, singular, plural, descr) VALUES
    ('linens', 'shift', 'Shift', 'shift', 'shifts',
     'A plain linen shift, worn next to the skin.'),
    ('woolens', 'breeches', 'Breeches', 'pair of breeches', 'pairs of breeches',
     'Woolen breeches, cut to the knee for a working man.'),
    ('homespun', 'gown', 'Gown', 'gown', 'gowns',
     'A woman''s gown of good cloth, for the meeting-house and the cold.');

DO $$
DECLARE
    old_present int;
    new_present int;
    restored int;
    rec record;
    plan_new jsonb;
    inv jsonb;
    mapped text;
BEGIN
    SELECT count(*) INTO old_present FROM item_kind k JOIN llm596_rename r ON r.old_name = k.name;
    SELECT count(*) INTO new_present FROM item_kind k JOIN llm596_rename r ON r.new_name = k.name;

    IF old_present = 0 AND new_present = 3 THEN
        RETURN;  -- already reverted
    ELSIF old_present = 0 AND new_present = 0 THEN
        RETURN;  -- fresh schema-only database
    ELSIF old_present <> 3 OR new_present <> 0 THEN
        RAISE EXCEPTION 'LLM-596 down: refusing to run against a mixed catalog — % of 3 renamed names and % of 3 original names present', old_present, new_present;
    END IF;

    UPDATE item_kind k
       SET name                   = r.new_name,
           display_label          = r.label,
           display_label_singular = r.singular,
           display_label_plural   = r.plural,
           description            = r.descr
      FROM llm596_rename r
     WHERE k.name = r.old_name;

    GET DIAGNOSTICS restored = ROW_COUNT;
    IF restored <> 3 THEN
        RAISE EXCEPTION 'LLM-596 down: expected to restore 3 garment kinds, restored %', restored;
    END IF;

    UPDATE actor_attribute aa
       SET params = jsonb_set(
               aa.params,
               '{restock}',
               (SELECT jsonb_agg(
                           CASE WHEN r.new_name IS NULL THEN entry
                                ELSE jsonb_set(entry, '{item}', to_jsonb(r.new_name))
                           END ORDER BY ord)
                  FROM jsonb_array_elements(aa.params->'restock')
                       WITH ORDINALITY AS t(entry, ord)
                  LEFT JOIN llm596_rename r ON r.old_name = entry->>'item'))
     WHERE aa.params ? 'restock'
       AND EXISTS (SELECT 1
                     FROM jsonb_array_elements(aa.params->'restock') e
                     JOIN llm596_rename r ON r.old_name = e->>'item');

    UPDATE item_recipe ir
       SET inputs = (SELECT jsonb_agg(
                         CASE WHEN r.new_name IS NULL THEN entry
                              ELSE jsonb_set(entry, '{item}', to_jsonb(r.new_name))
                         END ORDER BY ord)
                       FROM jsonb_array_elements(ir.inputs)
                            WITH ORDINALITY AS t(entry, ord)
                       LEFT JOIN llm596_rename r ON r.old_name = entry->>'item')
     WHERE jsonb_typeof(ir.inputs) = 'array'
       AND EXISTS (SELECT 1 FROM jsonb_array_elements(ir.inputs) e
                     JOIN llm596_rename r ON r.old_name = e->>'item');

    FOR rec IN SELECT actor_id, plan FROM visitor WHERE plan IS NOT NULL LOOP
        plan_new := rec.plan;

        IF plan_new #>> '{trade,good}' IS NOT NULL THEN
            SELECT r.new_name INTO mapped FROM llm596_rename r WHERE r.old_name = plan_new #>> '{trade,good}';
            IF mapped IS NOT NULL THEN
                plan_new := jsonb_set(plan_new, '{trade,good}', to_jsonb(mapped));
            END IF;
        END IF;

        IF jsonb_typeof(plan_new->'inventory') = 'object' THEN
            SELECT jsonb_object_agg(COALESCE(r.new_name, kv.key), kv.value) INTO inv
              FROM jsonb_each(plan_new->'inventory') kv
              LEFT JOIN llm596_rename r ON r.old_name = kv.key;
            IF inv IS NOT NULL THEN
                plan_new := jsonb_set(plan_new, '{inventory}', inv);
            END IF;
        END IF;

        IF plan_new IS DISTINCT FROM rec.plan THEN
            UPDATE visitor SET plan = plan_new WHERE actor_id = rec.actor_id;
        END IF;
    END LOOP;
END $$;

COMMIT;
