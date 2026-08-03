-- LLM-592: give the distributor a price for the clothing he already sells.
--
-- WHY. Josiah Thorne is carrying 3 shifts, 2 gowns, a cloak and a coat that the
-- visiting factor pushed on him, and he has sold NONE of them. He paid 5 coins for
-- a shift against a 3-coin wholesale anchor. The reason is that clothing is
-- invisible to him: both "## What your wares fetch" (perception/trade_value.go)
-- and "## Restocking" (perception/restock.go) are driven entirely off
-- RestockPolicy — ProduceEntries() plus BuyEntries() — and he has no garment
-- entry at all. So no price line, no restock line, and nothing to quote when a
-- customer asks.
--
-- The working-clothes cue lands in the same release and steers buyers to him. A
-- keeper with no price for the thing he is being asked for is where that would
-- have stalled.
--
-- WHAT THIS DOES AND DOES NOT BUY. The price line is the real prize: buildTradeValue
-- reads the policy and the (inert, price-carrier) item_recipe rows LLM-410 seeded,
-- so each garment kind gets its wholesale-to-retail band and his own cost basis
-- rendered whenever he is in a huddle — which is exactly when he needs to name a
-- price.
--
-- The RESTOCKING line will mostly stay silent, and that is correct rather than a
-- gap: clothing is import-only, the factor is the sole source, and a visiting
-- factor has no WorkStructureID, so eachVendorOffer never yields him as a vendor.
-- An item nobody sells anywhere is omitted from the section outright rather than
-- rendered as a dead end. The entry still earns its place — it is what makes the
-- good HIS line of trade rather than an accident of the factor's pack.
--
-- CAPS are deliberately modest. Village demand at the LLM-589 budgets is roughly
-- 18-24 working garments a month across 15 workers, and a factor already arrives
-- with 10-15, so a 6/6/6 working-garment ceiling is about a quarter's cover
-- without turning the shelf into a warehouse. Outerwear at 4 each: coat and cloak
-- wear slowest (18000 minutes) and serve the cold line, not daily labour.
--
-- ALL FIVE GARMENT KINDS, not just the three the new cue owns. Leaving coat and
-- cloak out would reproduce exactly this bug for the cold self-line's vendor
-- nudge, which has been pointing buyers at a keeper with no price for a coat all
-- along.
--
-- PER-KIND MERGE, not all-or-nothing. The first cut guarded the whole UPDATE on
-- the absence of a `shift` entry, so a policy that already carried shift but was
-- missing breeches would have been skipped entirely — the migration reporting
-- success over a state it was written to fix (code_review). Each kind is now
-- tested and added independently, so a partially populated policy converges to
-- all five however it got there, and a re-apply adds nothing.
--
-- Existing entries are never rewritten, only appended past. An operator who has
-- tuned a cap through the umbilical keeps their value: the containment test asks
-- only whether a line for that KIND exists, not whether it matches ours.
--
-- The restock policy is the UNION of every attribute's params.restock; `merchant`
-- is the home for his bought-in lines.
--
-- actor_attribute is CHECKPOINT-WRITTEN by the engine (raw params bytes written
-- back verbatim). The deploy runs migrations with the engine stopped, so this
-- applies cleanly and the post-deploy boot derives the new RestockPolicy. An
-- ad-hoc apply outside a deploy must stop the engine first.

BEGIN;

-- The lines this migration owns, named once and read by the merge and the
-- assertion below so the two can never disagree about what "all five" means.
-- ON COMMIT DROP rolls back cleanly under the deploy's dry run, which swaps the
-- terminating COMMIT for ROLLBACK.
CREATE TEMP TABLE llm592_clothing_lines (item text, max_qty int) ON COMMIT DROP;
INSERT INTO llm592_clothing_lines (item, max_qty) VALUES
    ('shift', 6), ('breeches', 6), ('gown', 6), ('coat', 4), ('cloak', 4);

DO $$
DECLARE
    distributor_id uuid;
    missing text;
BEGIN
    -- Resolved from the distributor TAG rather than a hardcoded actor id: the
    -- village has exactly one distributor by design, and keying on the role means
    -- this still lands if the shop ever changes hands.
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
        -- Clothing would stay silently unpriced, which is the bug this migration
        -- exists to fix. Fail loud rather than let a stale role slug pass as success.
        RAISE EXCEPTION 'LLM-592: the distributor has no merchant actor_attribute row to hold the clothing lines';
    END IF;

    UPDATE actor_attribute aa
       SET params = jsonb_set(
               aa.params,
               '{restock}',
               COALESCE(aa.params->'restock', '[]'::jsonb) || (
                   SELECT COALESCE(jsonb_agg(
                              jsonb_build_object('item', l.item, 'source', 'buy', 'max', l.max_qty)
                              ORDER BY l.item), '[]'::jsonb)
                     FROM llm592_clothing_lines l
                    WHERE NOT (COALESCE(aa.params->'restock', '[]'::jsonb)
                               @> jsonb_build_array(jsonb_build_object('item', l.item)))))
     WHERE aa.actor_id = distributor_id
       AND aa.slug = 'merchant';

    -- Every owned kind must now have a line, whatever the policy looked like
    -- going in. A silent partial merge is the failure this replaced.
    SELECT string_agg(l.item, ', ' ORDER BY l.item) INTO missing
      FROM llm592_clothing_lines l
     WHERE NOT EXISTS (
           SELECT 1 FROM actor_attribute aa
            WHERE aa.actor_id = distributor_id
              AND aa.slug = 'merchant'
              AND aa.params->'restock' @> jsonb_build_array(jsonb_build_object('item', l.item)));
    IF missing IS NOT NULL THEN
        RAISE EXCEPTION 'LLM-592: distributor clothing lines still missing after merge: %', missing;
    END IF;
END $$;

COMMIT;
