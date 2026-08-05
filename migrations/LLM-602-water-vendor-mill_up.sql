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
--    non-array restock corrupting via ||; the @> guard makes it idempotent.
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
UPDATE actor_attribute
   SET params = jsonb_set(
       params,
       '{restock}',
       (SELECT jsonb_agg(
                   CASE WHEN elem->>'item' = 'water'
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
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM actor) THEN
        RETURN;
    END IF;

    IF NOT EXISTS (SELECT 1 FROM actor WHERE id = '019da6b7-a853-79fb-91eb-645e5d9915c1') THEN
        RAISE EXCEPTION 'LLM-602: seeded actors but Joseph Scott 019da6b7... is missing (stale id?)';
    END IF;

    IF NOT EXISTS (SELECT 1 FROM actor_attribute
                    WHERE actor_id = '019da6b7-a853-79fb-91eb-645e5d9915c1'
                      AND slug = 'forage_range') THEN
        RAISE EXCEPTION 'LLM-602: Joseph forage_range grant was not applied';
    END IF;

    -- The grant is inert without the entry, and the entry is inert without the
    -- grant. Assert BOTH halves so this can never ship in the exact broken shape
    -- it was written to repair.
    IF NOT EXISTS (SELECT 1 FROM actor_attribute
                    WHERE actor_id = '019da6b7-a853-79fb-91eb-645e5d9915c1'
                      AND slug = 'merchant'
                      AND params->'restock' @> '[{"item": "water", "source": "forage"}]'::jsonb) THEN
        RAISE EXCEPTION 'LLM-602: Joseph forage-water restock entry missing';
    END IF;

    IF NOT EXISTS (SELECT 1 FROM actor_attribute
                    WHERE actor_id = '019dcac2-e78a-715e-91b7-101f339b0891'
                      AND slug = 'merchant'
                      AND params->'restock' @> '[{"item": "water", "source": "buy"}]'::jsonb) THEN
        RAISE EXCEPTION 'LLM-602: Josiah water entry was not flipped to buy';
    END IF;

    -- One entry per item: if a forage water entry survives alongside the buy one,
    -- the flip rebuilt the array wrongly and his policy is ambiguous.
    IF EXISTS (SELECT 1 FROM actor_attribute
                WHERE actor_id = '019dcac2-e78a-715e-91b7-101f339b0891'
                  AND slug = 'merchant'
                  AND params->'restock' @> '[{"item": "water", "source": "forage"}]'::jsonb) THEN
        RAISE EXCEPTION 'LLM-602: Josiah still carries a forage water entry after the flip';
    END IF;
END $$;

COMMIT;
