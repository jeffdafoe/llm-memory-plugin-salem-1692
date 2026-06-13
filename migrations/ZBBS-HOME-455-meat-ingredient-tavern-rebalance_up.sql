-- ZBBS-HOME-455: meat is a raw ingredient only; stew becomes the meal that
-- draws villagers to the tavern.
--
-- Background: a live commerce dig (2026-06-13) showed raw meat dominating the
-- food economy -- bought in bulk and eaten straight from inventory. Meat
-- satisfied 10 hunger/unit (the most filling food), was {portable} (stockable,
-- eat-anywhere), and beat stew on cost-per-hunger. The result: NPCs hauled raw
-- meat home and grazed from their packs instead of eating at the tavern,
-- quietly hollowing out the tavern as a social hub (cuts against the
-- "conversation is primary" principle).
--
-- Three data changes -- no engine-logic change. Edibility is derived purely
-- from item_satisfies: ItemKindDef.Consumable() == (len(Satisfies) > 0).
--
--   1. meat: drop its hunger row entirely. With zero Satisfies entries meat is
--      no longer Consumable() -- the consume command rejects it at precondition
--      and the perception/recovery cues (which iterate Satisfies) stop offering
--      it as a hunger remedy. Meat stays a {portable}, tradeable INGREDIENT:
--      the stew recipe (30 meat per batch) and the farm meat supply chain are
--      untouched.
--
--   2. stew: 4 -> 12 immediate (its dwell credit is unchanged). One bowl now
--      clears most of a hungry NPC's bar (need scale 0..24) in a single tavern
--      visit -- the clear "real meal" vs. grazing. stew is the only
--      non-portable (eat-here) food, so this is what draws NPCs to the tavern.
--
--   3. cheese: 8 -> 4. cheese is also {portable}; left at 8 it would simply
--      inherit meat's role as the dominant portable hunger food. Dropping it to
--      the bread tier puts every portable food in one weak "snack" band, so the
--      tradeoff becomes location/convenience (snack anywhere, weak) vs. meal
--      quality (walk to the tavern when open, hearty stew).
--
-- item_satisfies is load-only reference data (the engine reads it at boot and
-- never writes it back), so this is NOT resurrected by the shutdown checkpoint
-- and takes effect on the next engine restart.

BEGIN;

-- Fail loud if a baseline row we rebalance is absent, rather than silently
-- applying a no-op UPDATE that still gets stamped as a successful migration.
-- These are hand-seeded reference rows; a missing one means the target DB's
-- item catalog is not what this migration assumes.
--
-- ...but only guard when the catalog is actually populated. On a freshly built
-- database -- the embedded-postgres integration-test template, or a brand-new
-- prod deploy (deploy.yml loads schema.sql then runs migrations; neither seeds
-- item_satisfies) -- the table is legitimately empty before this migration
-- runs, and the UPDATE/DELETE below are harmless no-ops. An empty table is a
-- fresh DB, not a drifted catalog, so the guard would be a false alarm there.
-- We still fail loud on the real anomaly: a non-empty catalog that is somehow
-- missing the specific stew/cheese rows we rebalance.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM item_satisfies) THEN
        IF NOT EXISTS (SELECT 1 FROM item_satisfies WHERE item_kind = 'stew' AND attribute = 'hunger') THEN
            RAISE EXCEPTION 'ZBBS-HOME-455: expected item_satisfies row stew/hunger is missing';
        END IF;
        IF NOT EXISTS (SELECT 1 FROM item_satisfies WHERE item_kind = 'cheese' AND attribute = 'hunger') THEN
            RAISE EXCEPTION 'ZBBS-HOME-455: expected item_satisfies row cheese/hunger is missing';
        END IF;
    END IF;
END $$;

DELETE FROM item_satisfies WHERE item_kind = 'meat'   AND attribute = 'hunger';
UPDATE item_satisfies SET amount = 12 WHERE item_kind = 'stew'   AND attribute = 'hunger';
UPDATE item_satisfies SET amount = 4  WHERE item_kind = 'cheese' AND attribute = 'hunger';

COMMIT;
