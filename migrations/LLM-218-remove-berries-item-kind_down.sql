-- LLM-218 down: restore the generic 'berries' item_kind, its inert reference
-- rows, and the original item_kind on the two pre-LLM-58 pay_ledger purchases.
--
-- Row values captured from the live DB on 2026-07-01, immediately before the
-- up migration ran. The ledger repoint targets the two specific rows by id
-- (54, 71) rather than all raspberry rows, because raspberries has its own
-- legitimate post-LLM-58 ledger history that must not be rewritten.
-- Rerun-safe via ON CONFLICT DO NOTHING + idempotent UPDATE.

BEGIN;

INSERT INTO item_kind (name, display_label, category, sort_order, capabilities, display_label_singular, display_label_plural)
VALUES ('berries', 'Berries', 'food', 140, '{portable}', 'berry', 'berries')
ON CONFLICT (name) DO NOTHING;

INSERT INTO item_recipe (output_item, output_qty, rate_qty, rate_per_hours, inputs, wholesale_price, retail_price)
VALUES ('berries', 1, 2, 1, '[]'::jsonb, 1, 1)
ON CONFLICT (output_item) DO NOTHING;

INSERT INTO item_satisfies (item_kind, attribute, amount)
VALUES ('berries', 'hunger', 1)
ON CONFLICT (item_kind, attribute) DO NOTHING;

UPDATE pay_ledger
SET item_kind = 'berries'
WHERE id IN (54, 71)
  AND item_kind = 'raspberries';

COMMIT;
