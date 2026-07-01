-- LLM-218: remove the vestigial generic 'berries' item_kind.
--
-- Orphaned since LLM-58 split the generic berry into 'raspberries' +
-- 'blueberries' and repointed every bush: nothing sources 'berries' any more
-- (no bush gather_item, no restock entry, no recipe input) and its item_recipe
-- row is an inert terminator. The one live inventory holding (Jefferey's 1)
-- was clawed back through the engine on 2026-07-01, before this migration.
--
-- pay_ledger references item_kind with a RESTRICT FK, and two delivered
-- 2026-05-08/09 purchases (Jefferey buying from Prudence Ward) predate the
-- LLM-58 split. Repoint them to 'raspberries' — the direct successor of the
-- generic berry (LLM-58's down migration maps raspberry bushes back to
-- 'berries') and the item Prudence actually sells — so the transaction
-- history stays truthful instead of being purged.
--
-- The item_kind delete cascades the inert item_recipe / item_satisfies /
-- actor_produce_state rows. The engine reads the item catalog at boot
-- (World.ItemKinds), so the standard deploy order (migrate -> restart) drops
-- it from memory. Rerun-safe: both statements no-op once applied. If any
-- unexpected RESTRICT reference (actor_inventory / scene_quote — both
-- verified empty) appears before deploy, the DELETE aborts the transaction
-- and halts the deploy rather than half-applying.

BEGIN;

UPDATE pay_ledger
SET item_kind = 'raspberries'
WHERE item_kind = 'berries';

DELETE FROM item_kind
WHERE name = 'berries';

COMMIT;
