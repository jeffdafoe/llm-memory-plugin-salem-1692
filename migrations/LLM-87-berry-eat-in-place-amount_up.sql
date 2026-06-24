-- LLM-87: a unit eaten IN PLACE at a pick-and-eat source should ease hunger by
-- the same amount as that unit picked and eaten from the pack.
--
-- An eat-and-pick source carries an object_refresh row that is BOTH a gather
-- source (gather_item set) and a consume-in-place source (amount < 0). The wild
-- berry bushes were authored with amount = -8, but the item they yield satisfies
-- only +2 hunger (item_satisfies raspberries/blueberries = 2). So the SAME unit
-- of stock was worth -8 eaten at the bush but -2 picked and consumed — a 4x
-- mismatch that let one in-place bite read as four berries' worth of food.
--
-- Fix: derive each eat-and-pick row's in-place amount from the item it yields,
-- amount = -(item_satisfies for its gather_item). For the berry bushes that is
-- -8 -> -2 (one bite = one berry however it is eaten). Deriving rather than
-- hardcoding -2 makes this correct for ANY pick-and-eat source regardless of how
-- its gather_item is tagged (generic 'berries' vs the split raspberries/
-- blueberries), and lands a non-berry source on its own item's value — so it
-- can't silently miss a bush. (The companion engine change makes NPCs eat
-- berry-by-berry agentically instead of auto-grazing the whole source.)
--
-- Scope: ONLY eat-and-pick rows — gather_item set (it yields a pickable unit) AND
-- amount < 0 (it feeds on arrival). Yield-only forage-to-SELL bushes (amount = 0)
-- and pure eat-only sources (no gather_item) are excluded. item_satisfies.amount
-- is CHECK > 0, so -(amount) is always < 0 and satisfies object_refresh's
-- amount-negative CHECK. A gather_item with no matching item_satisfies row is left
-- untouched (the INNER join skips it) — a contradiction the engine's ConfigWarnings
-- surfaces separately.
--
-- ENGINE-OWNED TABLE. object_refresh is checkpoint-written by the running engine.
-- Apply with the engine STOPPED (stop -> migrate -> start, the standard deploy
-- order) or the old binary's shutdown checkpoint clobbers it. snapshot_gen is
-- left untouched; LoadAll has no gen filter, so the edited rows enter memory at
-- boot and the first checkpoint re-stamps them.
--
-- Rerun-safe: the `amount <> -s.amount` predicate updates only still-misaligned
-- rows, so a rerun is a no-op; the guard then fails loud if any eat-and-pick
-- hunger row remains out of step with its item.

BEGIN;

UPDATE object_refresh r
SET amount = -s.amount
FROM item_satisfies s
WHERE r.gather_item IS NOT NULL
  AND r.amount < 0
  AND r.attribute = 'hunger'
  AND s.item_kind = r.gather_item
  AND s.attribute = 'hunger'
  AND r.amount <> -s.amount;

DO $$
DECLARE
    mismatched int;
BEGIN
    SELECT count(*) INTO mismatched
      FROM object_refresh r
      JOIN item_satisfies s
        ON s.item_kind = r.gather_item AND s.attribute = 'hunger'
     WHERE r.gather_item IS NOT NULL
       AND r.amount < 0
       AND r.attribute = 'hunger'
       AND r.amount <> -s.amount;
    IF mismatched > 0 THEN
        RAISE EXCEPTION 'LLM-87: % eat-in-place hunger row(s) still not aligned to item_satisfies', mismatched;
    END IF;
END $$;

COMMIT;
