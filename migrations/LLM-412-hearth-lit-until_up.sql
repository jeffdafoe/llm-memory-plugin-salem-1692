-- LLM-412: durable hearth fire state on structure-backed village objects.
--
-- A structure whose village_object is tagged `hearth` has a fireplace; its
-- fire state is one timestamp — hearth_lit_until — "when the fire burns out."
-- Lit means the instant is in the future; there is no burn-down sweep. Stoking
-- (the stoke source-activity, consuming firewood) pushes it out, capped.
-- A lit hearth warms its structure: occupants take no cold (LLM-412 cold need)
-- and recover what chill they carry.
--
-- Durable on the village_object row because the village restarts many times a
-- day for deploys: every fire going out on each deploy would turn firewood
-- into a restart tax and un-warm every structure mid-storm.
--
-- ENGINE-OWNED TABLE — village_object is checkpoint-written by the running
-- engine, but the deploy stops the engine before migrating (down -> migrate ->
-- up), and this is a PURELY ADDITIVE nullable column: existing rows read NULL
-- (fire never lit), and the new binary's checkpoint UPSERT is what starts
-- writing it. Apply before deploying the LLM-412 binary (its UPSERT references
-- the column); the standard deploy order does this.
--
-- Rerun-safe via IF NOT EXISTS.

BEGIN;

ALTER TABLE village_object ADD COLUMN IF NOT EXISTS hearth_lit_until timestamp with time zone;

COMMIT;
