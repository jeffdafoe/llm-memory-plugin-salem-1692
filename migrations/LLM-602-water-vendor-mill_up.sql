-- LLM-602: seat the water-vendor role at the MILL.
--
-- Josiah Thorne has carried a `water` / `source: forage` / `max: 20` restock entry
-- since LLM-254 but sits at 0 of 20 with nothing in his prompt telling him to draw.
-- The cause is that his `forage_range` grant is GONE from prod. That capability
-- (engine/sim/gather_target.go:171) is what enables the LLM-253 ranged forage cue —
-- "## Free sources you can gather from" — which names the nearest ripe UNOWNED
-- source at any distance. It is the ONLY cue that can ever surface a well:
--
--   * "## Restocking" covers `buy` entries only ("your shop stock of these
--     bought-in goods"), so a forage entry never appears there.
--   * "## Your bushes to harvest" is owner-gated and both Wells are unowned.
--
-- Without the grant his water entry is unreachable by every cue that exists.
--
-- WHY THE MILL RATHER THAN RESTORING JOSIAH'S GRANT (Jeff, 2026-08-05). The Mill is
-- `wholesaler`-tagged and the General Store is `distributor`. Under the wholesale
-- tier a wholesaler sells only to the distributor, so seating the draw at the mill
-- makes a two-hop chain — Well -> Joseph draws -> wholesales to Josiah -> Josiah
-- retails -> the forge and the kitchen — and coin flows Ezekiel -> Josiah -> Joseph.
-- That feeds the two actors the economy has starved (Josiah 6 coins, Joseph 11).
-- Restoring Josiah's grant instead would have him draw for free and retail at pure
-- margin: good for his purse, nothing for the miller. The mill also simply sits on
-- the water, which a general store does not.
--
-- NOT DONE HERE, DELIBERATELY: Josiah's `forage_range` is not restored. Under this
-- design he does not need it, and how his row disappeared is still unexplained —
-- see LLM-602 for what was ruled out. Restoring it would also give the village two
-- competing drawers for one 20-per-6h well row.
--
-- ACCEPTED RISK: a RestockPolicy holds one entry per item, so Josiah cannot both
-- draw and buy. Flipping him to `buy` makes shop water depend on Joseph actually
-- drawing. Contained: drinking at the well is free and uncapped for everyone
-- (the thirst row is separate and infinite), so the failure mode is recipe friction
-- for the forge and the kitchen, never thirst.
--
-- actor_attribute is CHECKPOINT-WRITTEN; deploy.sh does stop -> migrate -> start,
-- so this applies engine-stopped with no checkpoint race. An ad-hoc apply against a
-- running engine would be overwritten from memory.

BEGIN;

-- 1. Grant Joseph the forage_range capability. Presence-only — the key's existence
--    is the grant, params are unused (see AttrForageRange). LLM-253 registered the
--    attribute_definition, so no catalog row is needed here. Guarded on the actor
--    existing so a schema-only integration DB touches zero rows.
INSERT INTO actor_attribute (actor_id, slug, params)
SELECT '019da6b7-a853-79fb-91eb-645e5d9915c1', 'forage_range', '{}'::jsonb
WHERE EXISTS (SELECT 1 FROM actor WHERE id = '019da6b7-a853-79fb-91eb-645e5d9915c1')
ON CONFLICT (actor_id, slug) DO NOTHING;

-- 2. Ensure Joseph carries the `forage water` restock entry. On prod this is a
--    no-op: it was added live through the umbilical on 2026-08-05. Asserting it
--    here rather than assuming it is what makes a fresh DB land in the same state
--    as prod. His RestockPolicy lives on the `merchant` attribute (same slug as
--    Josiah's), so APPEND rather than replace — he already holds flour/produce,
--    wheat/buy and firewood/forage. The jsonb_typeof guard stops a malformed
--    non-array restock corrupting via ||.
--
--    The idempotency guard matches on ITEM ALONE, deliberately. Matching the full
--    intended entry would append a SECOND water entry whenever an existing one had
--    a different source or max, and a policy with two entries for one item is
--    ambiguous — worse than the state being repaired (code_review). Item-only means
--    "a water entry exists, leave it alone"; a wrong-shape one is then caught by the
--    full-shape assertion below and fails the deploy loud rather than being silently
--    accepted or duplicated.
UPDATE actor_attribute
   SET params = jsonb_set(
       params,
       '{restock}',
       (CASE WHEN jsonb_typeof(params->'restock') = 'array'
             THEN params->'restock' ELSE '[]'::jsonb END)
           || '[{"item": "water", "source": "forage", "max": 20}]'::jsonb
   )
 WHERE actor_id = '019da6b7-a853-79fb-91eb-645e5d9915c1'
   AND slug = 'merchant'
   AND NOT (COALESCE(params->'restock', '[]'::jsonb) @> '[{"item": "water"}]'::jsonb);

-- 3. Flip Josiah's water entry from `forage` to `buy` so the shop restocks from the
--    mill instead of drawing its own. Rebuilds the array element-wise rather than
--    remove-then-append, which preserves his other 14 entries and their ordering.
--    Matched on the entry being `forage` today, which also makes it idempotent — a
--    second apply finds no forage water entry and updates nothing.
--
--    The CASE tests item AND source, not item alone. The outer WHERE only proves
--    that AT LEAST ONE water/forage entry exists; an item-only CASE would rewrite
--    every water entry in the array, including one already correctly on `buy`
--    (code_review). One-entry-per-item is the policy invariant, but this migration
--    enforces rather than assumes it — the assertions below reject duplicates.
UPDATE actor_attribute
   SET params = jsonb_set(
       params,
       '{restock}',
       (SELECT jsonb_agg(
                   CASE WHEN elem->>'item' = 'water' AND elem->>'source' = 'forage'
                        THEN jsonb_set(elem, '{source}', '"buy"'::jsonb)
                        ELSE elem END
                   ORDER BY ord)
          FROM jsonb_array_elements(params->'restock') WITH ORDINALITY AS t(elem, ord))
   )
 WHERE actor_id = '019dcac2-e78a-715e-91b7-101f339b0891'
   AND slug = 'merchant'
   AND jsonb_typeof(params->'restock') = 'array'
   AND params->'restock' @> '[{"item": "water", "source": "forage"}]'::jsonb;

-- Validate on a SEEDED DB — fail loud rather than shipping this half-applied. A
-- schema-only DB has an empty actor table and skips. Mirrors the LLM-254 guard.
-- Assertions check the COMPLETE entry and its UNIQUENESS, not `@>` containment on
-- item+source. Containment alone passes for a forage entry with the wrong `max`, for
-- extra unexpected fields, and for duplicate water entries as long as one matches
-- (code_review). Exact equality plus a count of water entries closes all three.
DO $$
DECLARE
    joseph   constant uuid  := '019da6b7-a853-79fb-91eb-645e5d9915c1';  -- Joseph Scott, the mill
    josiah   constant uuid  := '019dcac2-e78a-715e-91b7-101f339b0891';  -- Josiah Thorne, General Store
    want_jos constant jsonb := '{"item": "water", "source": "forage", "max": 20}';
    want_jth constant jsonb := '{"item": "water", "source": "buy", "max": 20}';
    n        int;
    got      jsonb;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM actor) THEN
        RETURN;  -- schema-only integration DB: nothing seeded, nothing to assert
    END IF;

    IF NOT EXISTS (SELECT 1 FROM actor WHERE id = joseph) THEN
        RAISE EXCEPTION 'LLM-602: seeded actors but Joseph Scott % is missing (stale id?)', joseph;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM actor WHERE id = josiah) THEN
        RAISE EXCEPTION 'LLM-602: seeded actors but Josiah Thorne % is missing (stale id?)', josiah;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM actor_attribute WHERE actor_id = joseph AND slug = 'forage_range') THEN
        RAISE EXCEPTION 'LLM-602: Joseph forage_range grant was not applied';
    END IF;

    -- Joseph: exactly one water entry, and it is the full intended shape. The grant
    -- is inert without this entry and the entry is inert without the grant, so both
    -- halves are asserted — this can never ship in the shape it was written to repair.
    -- Count and fetch are separate statements: there is no min(jsonb) aggregate, and
    -- asserting the count FIRST is what makes the bare SELECT INTO below single-row.
    SELECT count(*) INTO n
      FROM actor_attribute aa, jsonb_array_elements(aa.params->'restock') e
     WHERE aa.actor_id = joseph AND aa.slug = 'merchant' AND e->>'item' = 'water';
    IF n <> 1 THEN
        RAISE EXCEPTION 'LLM-602: Joseph must carry exactly 1 water restock entry, found %', n;
    END IF;
    SELECT e INTO got
      FROM actor_attribute aa, jsonb_array_elements(aa.params->'restock') e
     WHERE aa.actor_id = joseph AND aa.slug = 'merchant' AND e->>'item' = 'water';
    IF got <> want_jos THEN
        RAISE EXCEPTION 'LLM-602: Joseph water entry is %, expected %', got, want_jos;
    END IF;

    -- Josiah: exactly one water entry, now `buy`. The count is what rejects a flip
    -- that rebuilt the array wrongly and left a forage entry beside the buy one.
    SELECT count(*) INTO n
      FROM actor_attribute aa, jsonb_array_elements(aa.params->'restock') e
     WHERE aa.actor_id = josiah AND aa.slug = 'merchant' AND e->>'item' = 'water';
    IF n <> 1 THEN
        RAISE EXCEPTION 'LLM-602: Josiah must carry exactly 1 water restock entry, found %', n;
    END IF;
    SELECT e INTO got
      FROM actor_attribute aa, jsonb_array_elements(aa.params->'restock') e
     WHERE aa.actor_id = josiah AND aa.slug = 'merchant' AND e->>'item' = 'water';
    IF got <> want_jth THEN
        RAISE EXCEPTION 'LLM-602: Josiah water entry is %, expected %', got, want_jth;
    END IF;
END $$;

COMMIT;
