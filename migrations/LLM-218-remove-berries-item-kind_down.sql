-- LLM-218 down: restore the generic 'berries' item_kind, its inert reference
-- rows, and the original item_kind on the two pre-LLM-58 pay_ledger purchases.
--
-- Row values captured from the live DB on 2026-07-01, immediately before the
-- up migration ran. These are the canonical values: ON CONFLICT DO UPDATE
-- overwrites any drifted re-creation rather than leaving it in place.
-- actor_produce_state (also on the item_kind cascade) had no 'berries' rows
-- live, so there is nothing to restore there. The ledger repoint targets the
-- two specific rows by id (54, 71) rather than all raspberry rows, because
-- raspberries has its own legitimate post-LLM-58 ledger history that must not
-- be rewritten; the assertion mirrors the up so a partial state aborts
-- instead of silently half-restoring.

BEGIN;

INSERT INTO item_kind (name, display_label, category, sort_order, capabilities, hours_per_unit, consume_dwell_narration, display_label_singular, display_label_plural)
VALUES ('berries', 'Berries', 'food', 140, '{portable}', NULL, NULL, 'berry', 'berries')
ON CONFLICT (name) DO UPDATE SET
    display_label           = EXCLUDED.display_label,
    category                = EXCLUDED.category,
    sort_order              = EXCLUDED.sort_order,
    capabilities            = EXCLUDED.capabilities,
    hours_per_unit          = EXCLUDED.hours_per_unit,
    consume_dwell_narration = EXCLUDED.consume_dwell_narration,
    display_label_singular  = EXCLUDED.display_label_singular,
    display_label_plural    = EXCLUDED.display_label_plural;

INSERT INTO item_recipe (output_item, output_qty, rate_qty, rate_per_hours, inputs, wholesale_price, retail_price)
VALUES ('berries', 1, 2, 1, '[]'::jsonb, 1, 1)
ON CONFLICT (output_item) DO UPDATE SET
    output_qty      = EXCLUDED.output_qty,
    rate_qty        = EXCLUDED.rate_qty,
    rate_per_hours  = EXCLUDED.rate_per_hours,
    inputs          = EXCLUDED.inputs,
    wholesale_price = EXCLUDED.wholesale_price,
    retail_price    = EXCLUDED.retail_price,
    updated_at      = now();

INSERT INTO item_satisfies (item_kind, attribute, amount, dwell_amount, dwell_period_minutes, dwell_total_ticks)
VALUES ('berries', 'hunger', 1, NULL, NULL, NULL)
ON CONFLICT (item_kind, attribute) DO UPDATE SET
    amount               = EXCLUDED.amount,
    dwell_amount         = EXCLUDED.dwell_amount,
    dwell_period_minutes = EXCLUDED.dwell_period_minutes,
    dwell_total_ticks    = EXCLUDED.dwell_total_ticks;

DO $$
DECLARE
    updated_count integer;
BEGIN
    UPDATE pay_ledger
    SET item_kind = 'berries'
    WHERE id IN (54, 71)
      AND item_kind = 'raspberries';

    GET DIAGNOSTICS updated_count = ROW_COUNT;

    IF updated_count NOT IN (0, 2) THEN
        RAISE EXCEPTION 'LLM-218 down: expected to restore 0 or 2 pay_ledger rows, restored %', updated_count;
    END IF;
END $$;

COMMIT;
