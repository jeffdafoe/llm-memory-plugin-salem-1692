-- LLM-557: the constable's town rate — durable arrears on each owned business.
--
-- The constable is the one villager with no trade of his own (no production, no
-- restock policy, a workplace that is not a shop). Seeded with a purse, he spent it
-- down and fell out of the market entirely. Every owned business now owes him a coin
-- a day, which he collects when he calls there on his rounds — a levy, not a
-- stipend, so it mints no coin and simply recirculates.
--
-- rate_owed is that accrued balance. Unlike the farm-upkeep levy (whose obligation is
-- a pure function of coins held and so needs no stored state), "have you paid today"
-- is a record, so this one carries a per-object accumulator in the shape of
-- village_object.wear (LLM-118).
--
-- Durable on the village_object row for the same reason hearth_lit_until is
-- (LLM-412): the village restarts many times a day for deploys, and an in-memory
-- balance would reset before a day's levy could ever be collected — the constable
-- would never be paid.
--
-- ENGINE-OWNED TABLE — village_object is checkpoint-written by the running engine,
-- but the deploy stops the engine before migrating (down -> migrate -> up), and this
-- is a PURELY ADDITIVE column with a default: existing rows read 0 (nothing owed),
-- and the new binary's checkpoint UPSERT is what starts writing it. Apply before
-- deploying the LLM-557 binary (its UPSERT references the column); the standard
-- deploy order does this.
--
-- NOT NULL DEFAULT 0 rather than nullable, matching wear: the balance is a count with
-- a meaningful zero, and a NULL would have to be coerced on every read.
--
-- Rerun-safe via IF NOT EXISTS.

BEGIN;

ALTER TABLE village_object ADD COLUMN IF NOT EXISTS rate_owed integer NOT NULL DEFAULT 0;

COMMIT;
