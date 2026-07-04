-- LLM-264: make object_refresh.attribute nullable.
--
-- A yield-only (forage-to-sell) row (amount = 0, gather_item set) eases NO need on
-- arrival — applyObjectRefreshEffect skips amount = 0 before applying anything —
-- yet it was forced to carry a non-null attribute purely to satisfy three schema
-- constraints: the NOT NULL column, the (object_id, attribute) primary key, and the
-- attribute -> refresh_attribute FK. That placeholder (e.g. `hunger` on a berry
-- bush that only yields berries) is semantically wrong and is inert only because
-- every consumption surface happens to gate on magnitude > 0. This makes the column
-- genuinely optional so a yield-only row carries no fake need.
--
-- A NULL cannot sit in a primary key, so the composite PK is replaced by a
-- surrogate `id`. Need-row identity stays the full UNIQUE (object_id, attribute) —
-- also the arbiter the checkpoint upsert and the already-applied LLM-50 / LLM-58
-- seeds use via a bare ON CONFLICT (a partial index would not satisfy that). Yield
-- rows carry a NULL attribute, so that constraint leaves them unconstrained (NULLs
-- are distinct); a PARTIAL unique index on (object_id, gather_item) WHERE attribute
-- IS NULL gives them their own natural-identity uniqueness AND a real ON CONFLICT
-- target, so the checkpoint upsert updates a yield row in place instead of churning
-- the surrogate id and leaning on delete-stale to prune the old copy.
--
-- Engine-owned, checkpoint-written table, but deploy.sh does stop -> migrate ->
-- start, so no running binary races these DDL changes. The data UPDATE (nulling
-- existing yield rows) is checkpoint-safe: the next boot loads them as
-- attribute = "" and SaveSnapshot writes them back as NULL — a fixed point.

BEGIN;

-- Surrogate PK: a NULL attribute can't live in the (object_id, attribute) PK.
-- GENERATED ALWAYS AS IDENTITY backfills existing rows and is transparent to the
-- engine's inserts (they list columns explicitly and never supply id).
ALTER TABLE object_refresh DROP CONSTRAINT object_refresh_pkey;
ALTER TABLE object_refresh ADD COLUMN id bigint GENERATED ALWAYS AS IDENTITY;
ALTER TABLE object_refresh ADD CONSTRAINT object_refresh_pkey PRIMARY KEY (id);

-- The attribute a yield-only row was forced to fake is now optional.
ALTER TABLE object_refresh ALTER COLUMN attribute DROP NOT NULL;

-- A need-bearing row (amount < 0) must still name its need; a yield-only row
-- (amount = 0) need not. Permissive on yield rows on purpose — a later-replaying
-- seed migration that inserts an amount = 0 row with a legacy placeholder attribute
-- stays valid.
ALTER TABLE object_refresh
    ADD CONSTRAINT object_refresh_attribute_required_for_need
    CHECK (attribute IS NOT NULL OR amount = 0);

-- Drop the vestigial attribute from the yield-only rows already in the world (the
-- berry / blueberry / sage bushes) BEFORE the partial indexes below, so their
-- attribute-IS-NULL / attribute-IS-NOT-NULL predicates see the final state.
UPDATE object_refresh
   SET attribute = NULL
 WHERE amount = 0
   AND NULLIF(btrim(gather_item), '') IS NOT NULL;

-- Preflight the yield index's key: fail loud with a clear reason rather than an
-- opaque CREATE INDEX violation if any object already carries two yield-only rows
-- for the same gather_item.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM object_refresh
        WHERE attribute IS NULL
        GROUP BY object_id, gather_item
        HAVING COUNT(*) > 1
    ) THEN
        RAISE EXCEPTION 'LLM-264: an object has multiple yield-only rows for the same gather_item; resolve the duplicates before the unique index can be created';
    END IF;
END $$;

-- Need-row identity: restore the full (object_id, attribute) uniqueness. This also
-- keeps the bare `ON CONFLICT (object_id, attribute)` arbiter that the checkpoint
-- upsert and the already-applied LLM-50 / LLM-58 seed migrations use (a partial
-- index would NOT satisfy an unqualified ON CONFLICT). NULLs are distinct in a
-- UNIQUE constraint, so it does not constrain yield rows — the partial index below
-- does that.
ALTER TABLE object_refresh
    ADD CONSTRAINT object_refresh_object_attribute_key UNIQUE (object_id, attribute);

-- Yield-row identity: one yield-only row per (object, gather_item), and a real
-- ON CONFLICT target so the checkpoint upsert updates a yield row IN PLACE on its
-- gather_item key instead of re-inserting (churning the surrogate id) and leaning
-- on delete-stale to prune the old copy.
--
-- This uniqueness is COMPLETE — there are no NULL-gather_item yield rows for
-- Postgres to treat as distinct — because a null-attribute row always has a
-- non-blank gather_item, enforced by two CHECKs in tandem: the
-- object_refresh_attribute_required_for_need CHECK above forces attribute-NULL ->
-- amount = 0, and the LLM-24 object_refresh_amount_negative CHECK forces
-- amount = 0 -> NULLIF(btrim(gather_item), '') IS NOT NULL. So a
-- (attribute NULL, gather_item NULL/blank) row fails a CHECK at write time
-- regardless of whether it goes through ValidateObjectRefreshes.
CREATE UNIQUE INDEX object_refresh_yield_key
    ON object_refresh (object_id, gather_item) WHERE attribute IS NULL;

COMMIT;
