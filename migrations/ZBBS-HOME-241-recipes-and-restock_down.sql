-- ZBBS-HOME-241 down. Reverses the foundation migration. Safe to run
-- as long as no actor_attribute.params has restock entries that
-- reference recipes — those would orphan when the table drops, but
-- the params JSONB has no FK so it would just become dead config.

BEGIN;

-- Discipline copy revert is best-effort: if the appended sentence
-- has been further edited, this is a no-op for that row. We use a
-- substring-based strip so a clean apply/revert cycle is idempotent
-- but a hand-edited row keeps its edits.
UPDATE attribute_definition
   SET instructions = REGEXP_REPLACE(
           instructions,
           E'\\n\\nYour stock is replenished by your work and \\(later\\) by trips to your suppliers\\..*?Do not invent suppliers, prices, or transactions\\.',
           '',
           'gs'
       ),
       updated_at = now()
 WHERE slug IN ('tavernkeeper', 'innkeeper');

DELETE FROM setting WHERE key IN (
    'restock.cycle_lookback_hours',
    'restock.buy_failure_backoff_minutes'
);

DELETE FROM item_recipe WHERE output_item IN (
    'water','ale','bread','cheese','milk','meat','carrots','berries','coca_tea','stew'
);

-- Restore the original pay_ledger.state CHECK (without no_stock).
ALTER TABLE pay_ledger DROP CONSTRAINT IF EXISTS pay_ledger_state_check;
ALTER TABLE pay_ledger ADD CONSTRAINT pay_ledger_state_check
    CHECK (state IN ('pending','accepted','declined','countered','withdrawn','failed'));

-- carrots: only delete if no inventory rows reference it (safer than
-- cascading user inventory loss on a rollback).
DELETE FROM item_satisfies WHERE item_kind = 'carrots';
DELETE FROM item_kind WHERE name = 'carrots'
   AND NOT EXISTS (SELECT 1 FROM actor_inventory WHERE item_kind = 'carrots');

DROP TABLE IF EXISTS actor_buy_state;
DROP TABLE IF EXISTS actor_produce_state;
DROP TABLE IF EXISTS item_recipe;

COMMIT;
