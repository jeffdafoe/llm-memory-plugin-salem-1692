-- LLM-586: let decorative actors share a display name — several ducks all
-- called "Duck".
--
-- A decorative actor is sprite-only and never ticked, so it can neither speak
-- nor be spoken to. Every name-to-actor resolution in the engine (speak's `to`
-- and its vocative fallback, pay and pay_with_item targets, PC quotes) scans
-- HUDDLE PEERS rather than the world, and since LLM-582 a decorative is never
-- a huddle member — so a shared decorative name is unambiguous everywhere it
-- is read. The one genuinely name-keyed table, npc_acquaintance (PK
-- (actor_id, other_name)), is written only from the ActorMet event that huddle
-- formation emits, so a duck never acquires a row there. THAT is the thing to
-- revisit if decoratives are ever made huddle-eligible again.
--
-- WHY AN EXCLUSION CONSTRAINT AND NOT A PARTIAL UNIQUE INDEX. The obvious
-- shapes are both rejected by PostgreSQL 17 (verified, not assumed):
--
--   CREATE UNIQUE INDEX ... WHERE (...) DEFERRABLE INITIALLY DEFERRED
--     -> ERROR: syntax error at or near "DEFERRABLE"
--        CREATE INDEX has no deferrability option at all.
--
--   ALTER TABLE ... ADD CONSTRAINT ... UNIQUE (display_name) WHERE (...)
--     -> ERROR: syntax error at or near "WHERE"
--        Unique CONSTRAINTS cannot be partial.
--
-- So "partial AND deferrable" is unreachable through a unique constraint, and
-- deferrability is not negotiable: the checkpoint upserts the live actor set
-- BEFORE deleting stale rows inside one transaction (pg/actors.go
-- SaveSnapshot), so an immediate constraint wedges checkpointing permanently
-- the first time an actor is deleted and its name recreated between two
-- checkpoints. That is precisely the durability failure LLM-580 fixed.
--
-- An exclusion constraint supports both, and needs no btree_gist — plain
-- USING btree with WITH = is an equality exclusion, i.e. a unique constraint
-- that happens to accept a WHERE clause.
--
-- The predicate is the column-level spelling of KindDecorative:
-- ClassifyActorKind derives the kind from these two columns (neither set =>
-- decorative), and `kind` itself is not stored. Both are plain columns, so the
-- predicate is IMMUTABLE as partial-index predicates require.
--
-- Uniqueness is UNCHANGED for every actor that can talk, be paid, or be
-- remembered. Only sprite-only rows are exempted.

BEGIN;

ALTER TABLE public.actor
    DROP CONSTRAINT IF EXISTS actor_display_name_key;

ALTER TABLE public.actor
    ADD CONSTRAINT actor_display_name_excl
    EXCLUDE USING btree (display_name WITH =)
    WHERE (llm_memory_agent IS NOT NULL OR login_username IS NOT NULL)
    DEFERRABLE INITIALLY DEFERRED;

COMMIT;
