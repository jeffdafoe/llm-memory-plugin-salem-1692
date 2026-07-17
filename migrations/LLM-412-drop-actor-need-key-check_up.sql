-- LLM-412 (follow-up): drop the actor_need.key enum CHECK; the engine is
-- authoritative on which needs exist.
--
-- LLM-412 ("cold + the hearth") added a fourth need, `cold`, a storm-driven
-- need relieved by shelter and fire. But actor_need carried a CHECK constraint,
-- actor_need_key_check, that hard-coded the valid key set as exactly
-- ('hunger','thirst','tiredness'). Adding `cold` to the engine without also
-- migrating that enum meant the DB rejected every `cold` need row.
--
-- The failure was latent, not immediate: `cold` only materializes on actors
-- while a storm is active, so checkpoints kept succeeding until weather turned.
-- When a storm rolled in (2026-07-17 01:51 UTC) the checkpointer's per-need
-- upsert inside Actors.SaveSnapshot began aborting with
--   ERROR: new row for relation "actor_need" violates check constraint
--   "actor_need_key_check" (SQLSTATE 23514)
-- and, because SaveWorld is one transaction, the ENTIRE world checkpoint failed.
-- Durability broke silently for ~10.5 hours (the running village looked fine;
-- nothing was being persisted) until the umbilical durability alarm surfaced it.
-- A restart in that window would have rolled the world back to the last good
-- checkpoint and lost every one of those hours.
--
-- An in-SQL enumeration of need keys is engine business logic living in the
-- schema: every new need would need a coupled migration or durability breaks
-- again. The engine already defines the need set and clamps values; the DB's job
-- is to store what the engine produces. So we retire the key enum entirely and
-- let the engine own key validity — the same posture actor_known_place.place_kind
-- already takes ("No DB CHECK: Go owns the discriminator", LLM-77). The value
-- range guard (actor_need_value_check, 0..24) stays: that is a genuine
-- corruption guard, not a business enum.
--
-- Emergency remediation already dropped this constraint on the live production
-- DB (2026-07-17) so the running world's checkpoints could resume without a
-- restart; this migration makes that permanent and keeps schema.sql / fresh
-- applies in sync. IF EXISTS makes it a safe no-op on the already-remediated DB.

BEGIN;

ALTER TABLE public.actor_need DROP CONSTRAINT IF EXISTS actor_need_key_check;

COMMIT;
