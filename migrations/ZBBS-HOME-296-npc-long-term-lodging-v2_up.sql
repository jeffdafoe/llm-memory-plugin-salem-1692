-- ZBBS-HOME-296: NPC long-term lodging (v2) — PR1 config.
--
-- Tags the nights_stay item with the `lodging` capability so the v2
-- deliver_order lodging branch (engine/sim/order_commands.go) routes a
-- delivered nights_stay to AssignBedroomForLodger (granting/extending a
-- ledger RoomAccess) instead of transferring a physical good.
--
-- nights_stay already carries `service` in prod (the inventory-bypass
-- token, ZBBS-131); this adds the v2-specific `lodging` token that keys
-- the room grant off a capability rather than the literal item name.
--
-- Config-only: the item_kind.capabilities column already exists in the
-- baseline (migrations/schema.sql), and the lodging_check_in_hour /
-- lodging_check_out_hour settings already load with code defaults
-- (15 / 11), so there is no schema change and no setting rows here. The
-- lodging_default_weekly_rate setting + any lodger seed are deliberately
-- NOT here — lodging self-bootstraps from coins-on-restart + the
-- recovery_options homeless cue (ZBBS-HOME-297). Idempotent.

BEGIN;

UPDATE item_kind
   SET capabilities = array_append(capabilities, 'lodging')
 WHERE name = 'nights_stay'
   AND NOT ('lodging' = ANY(capabilities));

COMMIT;
