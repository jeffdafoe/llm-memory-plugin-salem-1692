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
-- KNOWN LIMITATION — exact-value matching bounds the blast radius, it does not
-- establish PROVENANCE (code_review). An entry that already existed with exactly
-- this migration's value is indistinguishable from one this migration appended,
-- so the down removes it too. Reproduced deliberately rather than assumed: seed a
-- policy with all five lines at 6/6/6/4/4, run the up (correctly a no-op, every
-- kind already present), run the down, and all five are gone.
--
-- Not fixed, as a judged trade rather than an oversight. The two ways to fix it
-- both cost more than the bug:
--
--   * a provenance marker field on each inserted object would sit in the stored
--     policy of a live actor forever, visible to every operator who ever reads it,
--     to make a revert path exact;
--   * a bookkeeping table means new schema for a seed migration.
--
-- Against that: the distributor holds ZERO garment lines today, so the collision
-- needs someone to author this migration's exact caps in the window before the
-- next deploy; and the loss only materializes on a deliberate revert, where the
-- entry is one umbilical edit away from being restored. A documented revert-path
-- caveat is the cheaper side of that trade. If clothing caps ever become
-- operator-tuned in practice, revisit — the calculus changes.
--
-- The same reasoning covers a DUPLICATE of one of these objects: both copies go.
-- That one is unreachable through the _up (which appends a kind only when no line
-- for it exists) and inert regardless, since the restock rebuild takes
-- first-listed on item ties.
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
