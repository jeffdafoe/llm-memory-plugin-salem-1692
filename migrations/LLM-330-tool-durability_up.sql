-- LLM-330: per-use tool durability — replace the batch-based skillet-wear
-- approximation (LLM-83 deferred decision #2).
--
-- 1. item_kind.durability_uses: kind-level durability attribute. > 0 marks the
--    kind a durable TOOL: a recipe input of that kind is required on hand at
--    produce start but not consumed; instead it wears 1 use per produce
--    execution and is spent (inventory -1) when its uses run out. 0 (the
--    default) keeps the plain consumed-input semantics.
--
-- 2. actor_inventory.uses_left: uses remaining on the actor's in-use unit of
--    a tool kind (engine Actor.ToolWear). NULL for ordinary stock and for a
--    tool no execution has worn yet. Rides the existing inventory checkpoint
--    row so wear dies with the stock that carries it.
--
-- 3. Seed: skillet lasts 20 produce executions (tunable per kind via the
--    umbilical item/set route). fried_meat (output_qty 1, shipped in LLM-325
--    fallout WITHOUT a skillet input to dodge the 1-skillet-per-meal batch
--    wear) now gains its skillet input under the sane per-use rate. Stew
--    already lists skillet — its wear drops from 1 skillet per batch to 1 use
--    per batch with no recipe change.
--
-- 4. Stew batch retune: output_qty was inflated to 10 solely to amortize the
--    old per-batch skillet CONSUMPTION (1 skillet / 10 stew). Per-use wear
--    retires that kludge, so the batch shrinks to 6 (shorter ~72-min cycles,
--    more frequent restock decisions). The consumed inputs scale DOWN with the
--    batch to hold stew's per-bowl ingredient cost stable — dropping output_qty
--    alone would nearly double the ingredient cost per bowl, since inputs are
--    charged per batch. Water/carrots scale exactly (5→3 at 0.6×); meat/milk
--    round 1.8→2; sage stays 1 (already a floor). Skillet stays as a wear
--    input. Reorder floors and derived-demand caps rescale automatically off
--    the new quantities.

BEGIN;

ALTER TABLE item_kind ADD COLUMN durability_uses INTEGER NOT NULL DEFAULT 0;

-- uses_left NULL = ordinary stock / an unworn tool; a set value must be a
-- live counter (the engine deletes wear entries at zero), so guard the
-- invariant against out-of-band writes.
ALTER TABLE actor_inventory ADD COLUMN uses_left INTEGER
    CONSTRAINT actor_inventory_uses_left_positive CHECK (uses_left IS NULL OR uses_left > 0);

UPDATE item_kind SET durability_uses = 20 WHERE name = 'skillet';

UPDATE item_recipe
   SET inputs = inputs || '[{"item": "skillet", "qty": 1}]'::jsonb
 WHERE output_item = 'fried_meat'
   AND NOT inputs @> '[{"item": "skillet"}]';

UPDATE item_recipe
   SET output_qty = 6,
       inputs = '[{"item": "meat", "qty": 2}, {"item": "water", "qty": 3}, {"item": "milk", "qty": 2}, {"item": "carrots", "qty": 3}, {"item": "skillet", "qty": 1}, {"item": "sage", "qty": 1}]'::jsonb
 WHERE output_item = 'stew';

COMMIT;
