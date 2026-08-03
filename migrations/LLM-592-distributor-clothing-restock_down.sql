-- LLM-592 down: strip the distributor's clothing restock lines.
--
-- Matches each entry by its EXACT value — item, source and cap together — not by
-- item name. The first cut filtered on `item NOT IN (the five kinds)`, which would
-- also have deleted a pre-existing or operator-authored garment line and made the
-- up/down pair lossy rather than reversible (code_review). An operator who has
-- tuned `coat` to a cap of 9 through the umbilical has a different object, so it
-- no longer matches and survives the revert.
--
-- jsonb equality is key-order insensitive, so the comparison holds however the
-- blob was serialized.
--
-- Known limitation, stated rather than hidden: if a policy somehow carries a
-- DUPLICATE of one of these exact objects, both copies go. That is unreachable
-- through the _up (which appends a kind only when no line for it exists) and
-- harmless if it ever happened — the restock rebuild takes first-listed on item
-- ties, so a duplicate was inert anyway.
--
-- Rebuilding the array by filtering is the only safe shape here: a positional
-- splice would break the moment an operator edits the policy through the
-- umbilical.
--
-- actor_attribute is checkpoint-written; apply with the engine stopped.

BEGIN;

CREATE TEMP TABLE llm592_clothing_lines (item text, max_qty int) ON COMMIT DROP;
INSERT INTO llm592_clothing_lines (item, max_qty) VALUES
    ('shift', 6), ('breeches', 6), ('gown', 6), ('coat', 4), ('cloak', 4);

DO $$
DECLARE distributor_id uuid;
BEGIN
    SELECT vo.owner_actor_id::uuid INTO distributor_id
      FROM village_object vo
     WHERE 'distributor' = ANY(vo.tags)
       AND vo.owner_actor_id IS NOT NULL
     LIMIT 1;

    IF distributor_id IS NULL THEN
        RETURN;
    END IF;

    UPDATE actor_attribute aa
       SET params = jsonb_set(
               aa.params,
               '{restock}',
               COALESCE(
                   (SELECT jsonb_agg(entry ORDER BY ord)
                      FROM jsonb_array_elements(aa.params->'restock')
                           WITH ORDINALITY AS t(entry, ord)
                     WHERE NOT EXISTS (
                           SELECT 1 FROM llm592_clothing_lines l
                            WHERE entry = jsonb_build_object(
                                      'item', l.item, 'source', 'buy', 'max', l.max_qty))),
                   '[]'::jsonb))
     WHERE aa.actor_id = distributor_id
       AND aa.slug = 'merchant'
       AND aa.params->'restock' IS NOT NULL;
END $$;

COMMIT;
