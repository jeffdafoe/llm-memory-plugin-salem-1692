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
-- so shift/breeches/gown/coat/cloak get their wholesale-to-retail band and his own
-- cost basis rendered whenever he is in a huddle — which is exactly when he needs
-- to name a price.
--
-- The RESTOCKING line will mostly stay silent, and that is correct rather than a
-- gap: clothing is import-only, the factor is the sole source, and a visiting
-- factor has no WorkStructureID, so eachVendorOffer never yields him as a vendor.
-- An item nobody sells anywhere is omitted from the section outright rather than
-- rendered as a dead end. The entry still earns its place — it is what makes the
-- good HIS line of trade rather than an accident of the factor's pack.
--
-- CAPS are deliberately modest. Village demand at the LLM-589 budgets is roughly
-- 24 working garments a month across 15 workers, and a factor already arrives with
-- 10-15 garments, so a 6/6/6 working-garment ceiling is about a quarter's cover
-- without turning the shelf into a warehouse. Outerwear at 4 each: coat and cloak
-- wear slowest (18000 minutes) and serve the cold line, not daily labour.
--
-- ALL FIVE GARMENT KINDS, not just the three the new cue owns. Leaving coat and
-- cloak out would reproduce exactly this bug for the cold self-line's vendor
-- nudge, which has been pointing buyers at a keeper with no price for a coat all
-- along.
--
-- The restock policy is the UNION of every attribute's params.restock; `merchant`
-- is the home for his bought-in lines. Appended to the existing array rather than
-- replaced, so the food and fuel entries are untouched.
--
-- actor_attribute is CHECKPOINT-WRITTEN by the engine (raw params bytes written
-- back verbatim). The deploy runs migrations with the engine stopped, so this
-- applies cleanly and the post-deploy boot derives the new RestockPolicy. An
-- ad-hoc apply outside a deploy must stop the engine first.

BEGIN;

DO $$
DECLARE
    distributor_id uuid;
    n int;
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

    UPDATE actor_attribute aa
       SET params = jsonb_set(
               aa.params,
               '{restock}',
               COALESCE(aa.params->'restock', '[]'::jsonb) || '[
                   {"item": "shift",    "source": "buy", "max": 6},
                   {"item": "breeches", "source": "buy", "max": 6},
                   {"item": "gown",     "source": "buy", "max": 6},
                   {"item": "coat",     "source": "buy", "max": 4},
                   {"item": "cloak",    "source": "buy", "max": 4}
               ]'::jsonb)
     WHERE aa.actor_id = distributor_id
       AND aa.slug = 'merchant'
       -- Idempotent: a re-apply must not append a second copy. First-listed wins
       -- on item ties when the policy is rebuilt, so a duplicate would be inert
       -- rather than harmful — but it would still be wrong in the stored blob.
       AND NOT (COALESCE(aa.params->'restock', '[]'::jsonb) @> '[{"item": "shift"}]'::jsonb);

    GET DIAGNOSTICS n = ROW_COUNT;

    -- The distributor exists but has no merchant row, or already carries a shift
    -- entry. The second is fine on a re-apply; the first would leave clothing
    -- silently unpriced, which is the bug this migration exists to fix — so fail
    -- loud rather than let a stale role slug pass as success.
    IF n = 0 AND NOT EXISTS (
        SELECT 1 FROM actor_attribute aa
         WHERE aa.actor_id = distributor_id
           AND aa.slug = 'merchant'
           AND aa.params->'restock' @> '[{"item": "shift"}]'::jsonb) THEN
        RAISE EXCEPTION 'LLM-592: the distributor has no merchant actor_attribute row to hold the clothing lines';
    END IF;
END $$;

COMMIT;
