-- LLM-254: water economy — make the town Well yield collectable, tradeable water.
--
-- Water was consume-in-place only. The Well (019d79ef..., UNOWNED commons) carried
-- ONE eat+pick row — thirst -8 AND gather_item=water, INFINITE supply (the standard
-- `well`-tag backfill, ZBBS-WORK-328). Nobody collected water only because no actor
-- was cued to. This makes water a finite, gathered, sellable resource:
--
--   1. Split the Well's single row into two non-overlapping rows. If a second
--      gather_item=water row were simply ADDED, the Well would carry TWO gatherable
--      water rows and at-bush Gather resolves to the FIRST IsGatherable() row (the
--      existing INFINITE one) -- so the finite cap below would never bind. And the
--      drink row can't itself be made finite: a finite well breaks NPC auto-drink
--      (LLM-87) and drink+gather share one counter, so drinking would drain the
--      pail stock. So: drop gather_item from the -8 thirst row (pure drink, still
--      infinite -- public drinking unchanged) and add a separate yield-only water
--      row as the SOLE gatherable source.
--   2. Josiah Thorne (019dcac2..., merchant, General Store) gets the forage_range
--      capability (LLM-253) + a `forage water` restock entry, so he is cued to draw
--      water at the Well and -- as a merchant -- resell it.
--
-- Hannah Boggs's porridge demand (milk:3 + water:5) is NOT hand-authored here; it
-- derives from her produce recipe once water has a vendor (LLM-260).
--
-- ORDERING: this is LLM-254 but depends on LLM-264's nullable object_refresh.attribute
-- (a yield-only row carries no attribute). The migration-replay harness applies
-- *_up.sql in lexical order, so LLM-254 replays BEFORE LLM-264 -- the nullable column
-- and the yield partial index don't exist yet there. That is fine ONLY because the
-- harness is SCHEMA-ONLY (no Well, no Josiah): every statement below is guarded to
-- touch ZERO rows there (WHERE EXISTS / NOT EXISTS), and the yield INSERT deliberately
-- AVOIDS ON CONFLICT on LLM-264's partial index (absent at that point) -- a NOT EXISTS
-- guard gives idempotency with no plan-time index dependency. NOTE: this migration is
-- NOT safe for a hypothetical "partially seeded BEFORE LLM-264" DB (the NULL-attribute
-- insert would hit the still-NOT-NULL column); no such environment exists (prod has
-- LLM-264 applied; the harness has no seed rows).
--
-- object_refresh and actor_attribute are CHECKPOINT-WRITTEN; deploy.sh does
-- stop -> migrate -> start, so these apply engine-stopped (no checkpoint race).

BEGIN;

-- 1a. The Well's thirst DRINK+GATHER row becomes PURE DRINK: drop its gather_item so
--     it is no longer a gatherable source. Matched on the exact expected shape
--     (thirst / -8 / gather_item=water) so a hypothetical second thirst row is never
--     touched. Amount -8 still names its need and stays infinite -> public drinking
--     unchanged. 0 rows on a schema-only DB.
UPDATE object_refresh
   SET gather_item = NULL
 WHERE object_id   = '019d79ef-d9df-73d7-967a-dc202ceaf624'
   AND attribute   = 'thirst'
   AND amount      = -8
   AND gather_item = 'water';

-- 1b. Add the yield-only water row: forage-to-sell (amount 0), NULL attribute
--     (LLM-264 -- eases no need), finite 20 with 6-hour periodic regen. This is now
--     the Well's SOLE gatherable water source. Guarded on the Well existing (FK) and
--     on no yield-only water row already existing (the LLM-264 partial-index key:
--     attribute IS NULL + gather_item). NOT ON CONFLICT, so it needs no LLM-264
--     partial index at plan time (see ORDERING). A wrong-shape existing row is caught
--     by the full-shape validation below, not silently accepted. 0 rows on schema-only.
INSERT INTO object_refresh
    (object_id, attribute, amount, available_quantity, max_quantity,
     refresh_mode, refresh_period_hours, gather_item)
SELECT '019d79ef-d9df-73d7-967a-dc202ceaf624', NULL, 0, 20, 20, 'periodic', 6, 'water'
WHERE EXISTS (SELECT 1 FROM village_object WHERE id = '019d79ef-d9df-73d7-967a-dc202ceaf624')
  AND NOT EXISTS (
      SELECT 1 FROM object_refresh
       WHERE object_id = '019d79ef-d9df-73d7-967a-dc202ceaf624'
         AND attribute IS NULL
         AND gather_item = 'water'
  );

-- 2a. Grant Josiah the forage_range capability (presence-only; LLM-253 registered
--     the attribute_definition). Guarded on Josiah existing (actor FK). (actor_id,
--     slug) is a baseline unique key, so ON CONFLICT is safe pre-LLM-264. 0 rows on
--     schema-only.
INSERT INTO actor_attribute (actor_id, slug, params)
SELECT '019dcac2-e78a-715e-91b7-101f339b0891', 'forage_range', '{}'::jsonb
WHERE EXISTS (SELECT 1 FROM actor WHERE id = '019dcac2-e78a-715e-91b7-101f339b0891')
ON CONFLICT (actor_id, slug) DO NOTHING;

-- 2b. Add Josiah's `forage water` restock entry. The RestockPolicy is the union of
--     every attribute's params.restock; the `merchant` role is the home of his
--     restock, so APPEND to it (don't clobber his existing buy entries). The
--     jsonb_typeof guard keeps a malformed non-array restock from corrupting via ||.
--     Idempotent via the @> guard. 0 rows on schema-only (no merchant row).
UPDATE actor_attribute
   SET params = jsonb_set(
       params,
       '{restock}',
       (CASE WHEN jsonb_typeof(params->'restock') = 'array'
             THEN params->'restock' ELSE '[]'::jsonb END)
           || '[{"item": "water", "source": "forage", "max": 20}]'::jsonb
   )
 WHERE actor_id = '019dcac2-e78a-715e-91b7-101f339b0891'
   AND slug = 'merchant'
   AND NOT (COALESCE(params->'restock', '[]'::jsonb) @> '[{"item": "water", "source": "forage"}]'::jsonb);

-- Validate on a SEEDED DB (fail loud rather than silently shipping this disabled or
-- half-applied). A schema-only DB has empty village_object / actor -> skip. Mirrors
-- the LLM-253 / LLM-90 deploy-time guards. The yield-row check asserts the FULL
-- intended shape so a pre-existing wrong-shape water yield row (which 1b's NOT EXISTS
-- would have skipped) fails loud instead of shipping silently.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM village_object) THEN
        IF NOT EXISTS (SELECT 1 FROM village_object WHERE id = '019d79ef-d9df-73d7-967a-dc202ceaf624') THEN
            RAISE EXCEPTION 'LLM-254: seeded world but Well 019d79ef... is missing (stale id?)';
        END IF;
        IF EXISTS (SELECT 1 FROM object_refresh
                    WHERE object_id = '019d79ef-d9df-73d7-967a-dc202ceaf624'
                      AND attribute = 'thirst' AND gather_item IS NOT NULL) THEN
            RAISE EXCEPTION 'LLM-254: Well thirst row still carries gather_item (drink/gather split not applied)';
        END IF;
        IF NOT EXISTS (SELECT 1 FROM object_refresh
                        WHERE object_id = '019d79ef-d9df-73d7-967a-dc202ceaf624'
                          AND attribute IS NULL AND gather_item = 'water'
                          AND amount = 0 AND available_quantity = 20 AND max_quantity = 20
                          AND refresh_mode = 'periodic' AND refresh_period_hours = 6) THEN
            RAISE EXCEPTION 'LLM-254: Well water yield row missing or wrong shape';
        END IF;
    END IF;
    IF EXISTS (SELECT 1 FROM actor) THEN
        IF NOT EXISTS (SELECT 1 FROM actor WHERE id = '019dcac2-e78a-715e-91b7-101f339b0891') THEN
            RAISE EXCEPTION 'LLM-254: seeded actors but Josiah 019dcac2... is missing (stale id?)';
        END IF;
        IF NOT EXISTS (SELECT 1 FROM actor_attribute
                        WHERE actor_id = '019dcac2-e78a-715e-91b7-101f339b0891' AND slug = 'forage_range') THEN
            RAISE EXCEPTION 'LLM-254: Josiah forage_range grant was not applied';
        END IF;
        IF NOT EXISTS (SELECT 1 FROM actor_attribute
                        WHERE actor_id = '019dcac2-e78a-715e-91b7-101f339b0891' AND slug = 'merchant'
                          AND params->'restock' @> '[{"item": "water", "source": "forage"}]'::jsonb) THEN
            RAISE EXCEPTION 'LLM-254: Josiah forage-water restock entry was not applied';
        END IF;
    END IF;
END $$;

COMMIT;
