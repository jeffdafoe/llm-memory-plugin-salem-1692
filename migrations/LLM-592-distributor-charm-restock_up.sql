-- LLM-592 follow-up: the other two things the factor carries.
--
-- The clothing migration covered the five garment kinds. The factor's pack is
-- SEVEN ware kinds (engine/sim/visitor.go, factorWareKinds):
--
--     coat, cloak, gown, breeches, shift, silver_locket, whalebone_charm
--
-- so the two charms were left with exactly the bug the clothing lines were added
-- to fix. Josiah is holding a whalebone charm right now with no restock entry, so
-- "## What your wares fetch" never prices it and he has nothing to quote when a
-- customer asks — the same reason he sat on garments for a month.
--
-- Note the charms are NOT dead stock the way the clothing was: the silver locket
-- he imported on 2026-07-28 has already sold on. So this is not fixing a stall,
-- it is giving a line that already moves an anchor to move it AT — which is the
-- LLM-591 problem in miniature, since without a price he is quoting from nothing.
--
-- CAPS of 2 each, well under the garments'. A charm is a keepsake, not a
-- consumable: demand is one-off per villager rather than a replacement cycle, so
-- a deep shelf would be capital tied up in goods nobody needs twice.
--
-- iron_ward is DELIBERATELY EXCLUDED. LLM-410 seeded three charms but the factor
-- carries only two, and there is no other source in the village — an entry for it
-- would be permanently inert, naming a line of trade he can never stock. Add it
-- if it ever joins factorWareKinds.
--
-- Same per-kind merge as the clothing migration: each kind tested and added
-- independently so a partially populated policy converges, existing entries never
-- rewritten so an operator's tuned cap survives, and the whole thing idempotent.
--
-- actor_attribute is CHECKPOINT-WRITTEN. The deploy runs migrations with the
-- engine stopped, so this applies cleanly and the post-deploy boot derives the
-- new RestockPolicy.

BEGIN;

CREATE TEMP TABLE llm592_charm_lines (item text, max_qty int) ON COMMIT DROP;
INSERT INTO llm592_charm_lines (item, max_qty) VALUES
    ('silver_locket', 2), ('whalebone_charm', 2);

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
        -- Fresh schema-only database (the integration harness) — nothing to do.
        RETURN;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM actor_attribute
                    WHERE actor_id = distributor_id AND slug = 'merchant') THEN
        RAISE EXCEPTION 'LLM-592: the distributor has no merchant actor_attribute row to hold the charm lines';
    END IF;

    UPDATE actor_attribute aa
       SET params = jsonb_set(
               aa.params,
               '{restock}',
               COALESCE(aa.params->'restock', '[]'::jsonb) || (
                   SELECT COALESCE(jsonb_agg(
                              jsonb_build_object('item', l.item, 'source', 'buy', 'max', l.max_qty)
                              ORDER BY l.item), '[]'::jsonb)
                     FROM llm592_charm_lines l
                    WHERE NOT (COALESCE(aa.params->'restock', '[]'::jsonb)
                               @> jsonb_build_array(jsonb_build_object('item', l.item)))))
     WHERE aa.actor_id = distributor_id
       AND aa.slug = 'merchant';

    SELECT string_agg(l.item, ', ' ORDER BY l.item) INTO missing
      FROM llm592_charm_lines l
     WHERE NOT EXISTS (
           SELECT 1 FROM actor_attribute aa
            WHERE aa.actor_id = distributor_id
              AND aa.slug = 'merchant'
              AND aa.params->'restock' @> jsonb_build_array(jsonb_build_object('item', l.item)));
    IF missing IS NOT NULL THEN
        RAISE EXCEPTION 'LLM-592: distributor charm lines still missing after merge: %', missing;
    END IF;
END $$;

COMMIT;
