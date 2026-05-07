-- ZBBS-131: lodging foundation — nights_stay item, lodging settings,
-- retire salem_day_rate.
--
-- Stage A of the lodging design (`shared/tasks/lodging/design`). Adds
-- the schema/data primitives needed for "buy a night at the tavern":
-- the `nights_stay` item_kind, a new `service` capability tag, and the
-- check-in / check-out hour settings the engine uses to compute
-- `lodger_until` from the buyer's `pay_ledger.ready_by`.
--
-- Lodger status is materialized from existing ledger rows (no new
-- columns on pay_ledger). The engine's `isLodger(actor, structure)`
-- query in engine/lodging.go reads `state='accepted'` rows of
-- item_kind='nights_stay' and computes lodger_until = (ready_by + qty)
-- at lodging_check_out_hour.
--
-- The `service` capability is a general-purpose tag — future services
-- (carpenter labor for X hours, courier delivery, etc.) can share the
-- same shape: not portable, not consumable, no inventory transfer at
-- deliver time, just a ledger row that materializes status.
--
-- salem_day_rate retirement: that setting was vestigial under the
-- wall-clock = game-clock 1:1 model agreed with Jeff during the
-- lodging design (Q2). No code reads it anymore (verified via grep at
-- ZBBS-131 ship time).

BEGIN;

-- nights_stay item_kind. capabilities=['service'] gates the pay/deliver
-- handling that bypasses portability and consumability checks. No
-- satisfies_attribute / satisfies_amount — sleeping doesn't drop any
-- need at the moment-of-purchase. Tiredness reset happens in the sleep
-- mechanic (stage B of the lodging design, separate commit).
INSERT INTO item_kind (name, display_label, category, satisfies_attribute, satisfies_amount, sort_order, capabilities, hours_per_unit) VALUES
    ('nights_stay', 'Night''s Stay', 'service', NULL, NULL, 500, ARRAY['service'], NULL);

-- Lodging hours. Engine resolves these at lodger-status-query time:
--   lodger_until = (ready_by + qty) at lodging_check_out_hour
-- Stored as integer hours-of-day [0, 23]. Real-hotel default: 3pm
-- check-in, 11am check-out.
INSERT INTO setting (key, value, description, is_public) VALUES
    ('lodging_check_in_hour',  '15', 'Hour of day (0-23) lodgers can be checked in for ready_by=today (soft gate; keeper can check in earlier at their discretion)', false),
    ('lodging_check_out_hour', '11', 'Hour of day (0-23) lodger_until expires on the (ready_by + qty)th day', false);

-- Retire salem_day_rate. Vestigial under wall-clock = game-clock 1:1.
DELETE FROM setting WHERE key = 'salem_day_rate';

COMMIT;
