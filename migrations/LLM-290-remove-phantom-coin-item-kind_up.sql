-- LLM-290: remove the phantom 'coin' item_kind minted from an NPC tool call.
--
-- Coins are currency, not a good. An NPC named coins as an item on a discovery
-- mint site (consume / scene_quote / pay_items — resolveOrMintItemKind,
-- ZBBS-WORK-412), which minted an economically inert 'coin' kind: category
-- 'unknown', no satisfies rows, no recipe, no price. Left in the catalog it is
-- worse than the unknown-kind error — resolveItemKind's label fallbacks let a
-- model's "coin"/"coins" resolve onto it and stake a nonsense goods-offer.
-- LLM-290 adds coin-token handling at every trade entrance (pay_with_item
-- translates to the pay flow; pay_items folds into amount; scene_quote /
-- offer_trade steer) and guards mintDiscoveredKind so a coin token can never
-- re-mint; this migration removes the row the earlier live occurrence left.
--
-- Same defensive posture as LLM-218 (the 'berries' removal): abort if any
-- RESTRICT-FK table (actor_inventory, pay_ledger, scene_quote) references the
-- kind — a reference means a real transaction/holding exists and needs a
-- human decision, not a silent purge. Cascade tables (item_recipe,
-- item_satisfies, actor_produce_state) clean up with the delete, though a
-- phantom mint has no such rows. The category='unknown' guard scopes the
-- delete to the minted shape: a future REAL coin item (authored with a proper
-- category) is never touched. 'coins' (plural) is included in case a plural
-- mint ever landed; live 2026-07-06 the catalog carries only 'coin'.
--
-- The engine reads the item catalog at boot (World.ItemKinds) and the
-- checkpointer writes it back, so this must run engine-stopped — deploy.sh's
-- stop -> migrate -> start order. Rerun-safe: a second run matches 0 rows.

BEGIN;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM actor_inventory ai
         JOIN item_kind ik ON ik.name = ai.item_kind
        WHERE ik.name IN ('coin', 'coins') AND ik.category = 'unknown'
    ) THEN
        RAISE EXCEPTION 'LLM-290: actor_inventory rows hold the phantom coin kind — claw them back through the engine before removing it';
    END IF;

    IF EXISTS (
        SELECT 1 FROM pay_ledger pl
         JOIN item_kind ik ON ik.name = pl.item_kind
        WHERE ik.name IN ('coin', 'coins') AND ik.category = 'unknown'
    ) THEN
        RAISE EXCEPTION 'LLM-290: pay_ledger rows reference the phantom coin kind — decide how to repoint history before removing it (see LLM-218 for the pattern)';
    END IF;

    IF EXISTS (
        SELECT 1 FROM scene_quote sq
         JOIN item_kind ik ON ik.name = sq.item_kind
        WHERE ik.name IN ('coin', 'coins') AND ik.category = 'unknown'
    ) THEN
        RAISE EXCEPTION 'LLM-290: scene_quote rows reference the phantom coin kind — expire/remove those quotes before removing it';
    END IF;
END $$;

DELETE FROM item_kind
WHERE name IN ('coin', 'coins')
  AND category = 'unknown';

COMMIT;
