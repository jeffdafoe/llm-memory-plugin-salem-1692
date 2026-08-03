-- LLM-596 down: put the gendered garment names back.
--
-- Exact mirror of the _up, including the jsonb rewrites the FK cascade cannot
-- reach. Reverting the item_kind names alone would leave every restock policy
-- naming a kind that no longer exists, which silently drops the good from both
-- the price line and the restock line.
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
DECLARE renamed int;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM item_kind WHERE name IN ('linens', 'woolens', 'homespun')) THEN
        RETURN;
    END IF;

    UPDATE item_kind k
       SET name                   = r.new_name,
           display_label          = r.label,
           display_label_singular = r.singular,
           display_label_plural   = r.plural,
           description            = r.descr
      FROM llm596_rename r
     WHERE k.name = r.old_name;

    GET DIAGNOSTICS renamed = ROW_COUNT;
    IF renamed <> 3 THEN
        RAISE EXCEPTION 'LLM-596 down: expected to restore 3 garment kinds, restored %', renamed;
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

    UPDATE visitor v
       SET plan = replace(replace(replace(v.plan::text,
               '"linens"', '"shift"'), '"woolens"', '"breeches"'), '"homespun"', '"gown"')::jsonb
     WHERE v.plan IS NOT NULL
       AND v.plan::text ~ '"(linens|woolens|homespun)"';
END $$;

COMMIT;
