-- ZBBS-085: Per-instance need-refresh attributes on village_object.
--
-- Origin (active-work 2026-04-30): John Ellis went to the well at 07:00
-- because his LLM reasoned a well was the right morning quench. The chore
-- walked him there but `executeAgentChore` is a no-op for thirst — only
-- `pay(for:"ale"|...)` decrements needs today. This table generalizes:
-- any object can carry one or more refresh rows, and an actor arriving at
-- the object has the corresponding need attribute decremented.
--
-- One row per (object, attribute). Multi-attribute objects (a "shaded
-- oak" that refreshes both tiredness and hunger from acorns) get
-- multiple rows. Inanimate objects only — taverns work through
-- `pay(for:"ale")` with a counterparty NPC, not arrival side-effect.
--
-- Zero rows for an object = no refresh effect (dry well, plain bench,
-- decorative tree). Drying up a well is a row delete, no schema change.
--
-- Future columns can land on this row scoped to the (object, attribute)
-- pair without bloating village_object: current_stock, max_stock,
-- regrow_per_hour, last_consumed_at when stock/depletion is added.
--
-- The trigger fires in applyArrival (engine/npc_movement.go) via spatial
-- lookup against the arrival point; no walk-state plumbing. PC and NPC
-- share the code path.

BEGIN;

-- Extend agent_action_log.source to allow 'engine'. ZBBS-073 originally
-- allowed only 'agent', 'magistrate', 'player' — covering tool-call
-- commits and player-issued actions. Object-refresh is neither: the
-- engine fires it as an arrival side-effect with no actor choice
-- involved, so 'engine' is the right discriminator. Reusing 'agent'
-- would conflate engine side-effects with NPC-chosen actions.
ALTER TABLE agent_action_log DROP CONSTRAINT agent_action_log_source_check;
ALTER TABLE agent_action_log ADD CONSTRAINT agent_action_log_source_check
    CHECK (source IN ('agent', 'magistrate', 'player', 'engine'));

CREATE TABLE object_refresh (
    object_id  UUID NOT NULL REFERENCES village_object(id) ON DELETE CASCADE,
    attribute  VARCHAR(32) NOT NULL,
    amount     SMALLINT NOT NULL,
    PRIMARY KEY (object_id, attribute),
    CONSTRAINT object_refresh_attribute_check
        CHECK (attribute IN ('hunger','thirst','tiredness')),
    -- Refresh = decrement only. A zero amount is a misconfigured row
    -- (audit/broadcast noise without effect); a positive amount would
    -- model need-inducing objects, which isn't in scope. Tighten now;
    -- relax with a future migration if need-inducing ever lands.
    CONSTRAINT object_refresh_amount_negative
        CHECK (amount < 0)
);

-- Spatial index for the arrival-side bounding-box filter in
-- applyObjectRefreshAtArrival. Without it the bbox predicate degrades
-- to a per-row scan of village_object once row counts grow. Also helps
-- the existing nearest-tagged-object lookup in executeAgentChore that
-- uses the same x/y squared-distance pattern.
CREATE INDEX IF NOT EXISTS idx_village_object_xy ON village_object (x, y);

COMMIT;
