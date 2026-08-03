-- LLM-592 follow-up down: strip the distributor's charm restock lines.
--
-- Same shape and the same documented limit as the clothing down: each entry is
-- matched by its EXACT value — item, source and cap together — so an operator who
-- has tuned a charm cap keeps their entry through the revert. Exact-value matching
-- bounds the blast radius; it does not establish PROVENANCE, so an entry that
-- already existed with exactly this migration's value is removed too. That trade
-- is argued in full in LLM-592-distributor-clothing-restock_down.sql and is not
-- restated here.
--
-- actor_attribute is checkpoint-written; apply with the engine stopped.

BEGIN;

CREATE TEMP TABLE llm592_charm_lines (item text, max_qty int) ON COMMIT DROP;
INSERT INTO llm592_charm_lines (item, max_qty) VALUES
    ('silver_locket', 2), ('whalebone_charm', 2);

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
                           SELECT 1 FROM llm592_charm_lines l
                            WHERE entry = jsonb_build_object(
                                      'item', l.item, 'source', 'buy', 'max', l.max_qty))),
                   '[]'::jsonb))
     WHERE aa.actor_id = distributor_id
       AND aa.slug = 'merchant'
       AND aa.params->'restock' IS NOT NULL;
END $$;

COMMIT;
