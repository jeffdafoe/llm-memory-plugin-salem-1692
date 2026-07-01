-- LLM-218: remove the vestigial generic 'berries' item_kind.
--
-- Orphaned since LLM-58 split the generic berry into 'raspberries' +
-- 'blueberries' and repointed every bush: nothing sources 'berries' any more
-- (no bush gather_item, no restock entry, no recipe input) and its item_recipe
-- row is an inert terminator. The one live inventory holding (Jefferey's 1)
-- was clawed back through the engine on 2026-07-01, before this migration.
--
-- pay_ledger references item_kind with a RESTRICT FK, and two delivered
-- 2026-05-08/09 purchases (ids 54 and 71 — Jefferey buying from Prudence
-- Ward) predate the LLM-58 split. Repoint exactly those two rows to
-- 'raspberries' — the direct successor of the generic berry (LLM-58's down
-- migration maps raspberry bushes back to 'berries') and the item Prudence
-- actually sells — so the transaction history stays truthful instead of
-- being purged. Any OTHER 'berries' ledger row would be unexpected (nothing
-- can mint one), so the DO block aborts rather than silently rewriting
-- history the down migration doesn't know about.
--
-- The item_kind delete cascades the inert item_recipe / item_satisfies rows
-- (actor_produce_state also cascades but has no 'berries' rows — verified 0
-- live on 2026-07-01). The engine reads the item catalog at boot
-- (World.ItemKinds), so the standard deploy order (migrate -> restart) drops
-- it from memory. Rerun-safe: on a second run the UPDATE matches 0 rows
-- (allowed by the assertion) and the DELETE no-ops.

BEGIN;

DO $$
DECLARE
    updated_count integer;
BEGIN
    UPDATE pay_ledger
    SET item_kind = 'raspberries'
    WHERE id IN (54, 71)
      AND item_kind = 'berries';

    GET DIAGNOSTICS updated_count = ROW_COUNT;

    IF updated_count NOT IN (0, 2) THEN
        RAISE EXCEPTION 'LLM-218: expected to repoint 0 or 2 berries pay_ledger rows, repointed %', updated_count;
    END IF;

    IF EXISTS (SELECT 1 FROM pay_ledger WHERE item_kind = 'berries') THEN
        RAISE EXCEPTION 'LLM-218: unexpected pay_ledger rows still reference berries — investigate before removing the item_kind';
    END IF;
END $$;

DELETE FROM item_kind
WHERE name = 'berries';

COMMIT;
