-- ZBBS-121: actor_need table — graduated need values as rows.
--
-- First commit in the refactor that moves hunger/thirst/tiredness off
-- the denormalized actor columns and onto rows in actor_need. Schema
-- lands here; backfill copies current column values; legacy columns
-- stay in place during the dual-write transition window. Subsequent
-- commits convert read sites, then drop the legacy columns.
--
-- Why rows instead of columns: adding a fourth or fifth graduated need
-- (mood, social fatigue, fatigue-of-a-different-kind) today requires a
-- column add + 17 callsite edits. With rows, it becomes one entry in
-- the in-code Need registry plus an INSERT into actor_need rows for
-- existing actors. The actor_attribute table is not the right home —
-- that's a tag/role system (lamplighter, worker), not a graduated-
-- value system. Different lifecycles, different read patterns; keep
-- them separate.
--
-- Key shape: VARCHAR(32) + CHECK matching the in-code registry.
-- Mirrors object_refresh.attribute (ZBBS-085) — same VARCHAR + CHECK
-- pattern; cheap to extend with a CHECK list change rather than an
-- enum migration.
--
-- value SMALLINT [0, 24]: matches the existing column constraint
-- (needMax constant in engine/needs.go). Default 0 so a freshly-
-- inserted actor that lacks a row reads as silent rather than
-- exploding. The 24 here is the same number as needMax in Go;
-- both have to change together if the cap ever shifts.
--
-- ON DELETE CASCADE: when an actor is removed, their need rows go too.
-- No standalone meaning to a need value without an owning actor.

BEGIN;

CREATE TABLE actor_need (
    actor_id  UUID     NOT NULL REFERENCES actor(id) ON DELETE CASCADE,
    key       VARCHAR(32) NOT NULL,
    value     SMALLINT NOT NULL DEFAULT 0,
    PRIMARY KEY (actor_id, key),
    CONSTRAINT actor_need_value_check CHECK (value >= 0 AND value <= 24),
    CONSTRAINT actor_need_key_check   CHECK (key IN ('hunger', 'thirst', 'tiredness'))
);

-- The PRIMARY KEY (actor_id, key) already supports both
-- `WHERE actor_id = $1` lookups and joins on actor_id; no separate
-- index needed.

-- Backfill: one row per (actor, need) for every existing actor. The
-- existing columns are NOT NULL DEFAULT 0, so every actor gets three
-- rows. UNION ALL beats three separate INSERTs because it's a single
-- planner pass.
INSERT INTO actor_need (actor_id, key, value)
SELECT id, 'hunger',    hunger    FROM actor
UNION ALL
SELECT id, 'thirst',    thirst    FROM actor
UNION ALL
SELECT id, 'tiredness', tiredness FROM actor;

COMMIT;
