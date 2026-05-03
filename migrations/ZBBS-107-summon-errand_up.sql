-- ZBBS-107: summon_errand — multi-leg messenger errand state.
--
-- The v1 summon tool (2026-05-02) was effectively a teleport: it wrote
-- an audit row, fired a room_event, and triggered the target's tick
-- with a "Summons for you" perception. No walk, no messenger.
--
-- ZBBS-107 + the engine state machine that lands alongside it replace
-- that teleport with a real errand chain:
--
--   1. summoner walks to nearest summon_point village_object_tag
--   2. ring narration fires at the summon_point
--   3. nearest messenger-attribute NPC walks to the summon_point
--   4. brief chat pause (chat_at_summon_until)
--   5. messenger walks to target's location-at-dispatch
--   6. delivery pause (chat_at_target_until)
--   7a. target is a virtual agent: tick fires with summons perception
--   7b. target unavailable: messenger walks back to summoner with a
--       canned refusal speech, then continues
--   8. messenger walks back to its origin
--   9. terminal state (done)
--
-- State is fully held in this row so the periodic errand ticker (added
-- alongside this migration) can advance timer transitions, and the
-- existing applyArrival walk callback can advance walk transitions,
-- without any per-errand goroutine. Engine restart loses in-flight
-- walks (matching today's lamplighter / worker scheduler behavior) but
-- preserves the durable record for inspection.

BEGIN;

CREATE TABLE summon_errand (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Summoner is always an actor row (NPC or PC). Cascade so a deleted
    -- actor doesn't leave dangling errands.
    summoner_id              UUID NOT NULL REFERENCES actor(id) ON DELETE CASCADE,
    -- The chosen messenger at dispatch time. Locked for the duration of
    -- the errand (idx_summon_errand_messenger_active enforces uniqueness
    -- of non-terminal rows per messenger).
    messenger_id             UUID NOT NULL REFERENCES actor(id) ON DELETE CASCADE,
    -- Target by display name. Stored as a string so the errand can
    -- proceed to delivery even if the target actor doesn't exist yet
    -- (state will branch to delivered_unavail / messenger_to_summoner).
    target_name              TEXT NOT NULL,
    -- The summon_point village_object the summoner rang from.
    summon_point_id          UUID NOT NULL REFERENCES village_object(id) ON DELETE CASCADE,
    -- Optional free-text "for what" supplied by the summoner. Carried
    -- through to the VA target's perception ("Summons from John for X")
    -- or the canned unavailable speech.
    reason                   TEXT NOT NULL DEFAULT '',
    -- Errand state. CHECK enforces the closed enum so a typo in code
    -- surfaces as a constraint violation rather than an unrecognized
    -- string the ticker silently ignores.
    state                    TEXT NOT NULL,
    -- Target classification, set once at dispatch from actor lookup.
    --   va      — actor with llm_memory_agent set; receives a summons tick
    --   pc      — actor with login_username set; messenger speaks a
    --             canned line at the target tile (no tick)
    --   nonva   — non-VA NPC; same canned speech as pc
    --   unknown — no actor matches target_name; messenger walks back to
    --             summoner via state messenger_to_summoner with refusal
    target_kind              TEXT NOT NULL,
    -- Where the messenger started so they can walk back at the end.
    -- Frozen at dispatch (state = 'summoner_at_point' transition).
    messenger_origin_x       DOUBLE PRECISION NOT NULL,
    messenger_origin_y       DOUBLE PRECISION NOT NULL,
    -- Target's position at dispatch time. Frozen — no pursuit if target
    -- moves during the messenger's walk. If target isn't there on
    -- arrival (within tolerance) the errand branches to unavailable.
    target_dispatch_x        DOUBLE PRECISION NOT NULL,
    target_dispatch_y        DOUBLE PRECISION NOT NULL,
    -- Timers. NULL until the relevant state is entered; the errand
    -- ticker advances when now() >= timer.
    chat_at_summon_until     TIMESTAMPTZ,
    chat_at_target_until     TIMESTAMPTZ,
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT summon_errand_state_check CHECK (state IN (
        'dispatched',
        'summoner_at_point',
        'messenger_at_point',
        'messenger_to_target',
        'messenger_at_target',
        'messenger_to_summoner',
        'messenger_returning',
        'done',
        'failed'
    )),
    CONSTRAINT summon_errand_target_kind_check CHECK (target_kind IN (
        'va', 'pc', 'nonva', 'unknown'
    ))
);

-- Index for the periodic ticker — scans non-terminal errands. Partial
-- so the index stays small (most rows are terminal once the table grows).
CREATE INDEX idx_summon_errand_active
    ON summon_errand (state)
    WHERE state NOT IN ('done', 'failed');

-- One active errand per messenger. The non-terminal partial unique
-- index defends against a second summon trying to dispatch the same
-- messenger before the first finishes; the engine should also reject
-- earlier with "no messenger available" but the DB is the backstop.
CREATE UNIQUE INDEX idx_summon_errand_messenger_active
    ON summon_errand (messenger_id)
    WHERE state NOT IN ('done', 'failed');

-- Lookup by messenger for arrival callbacks: when a walk completes,
-- the engine asks "is this NPC mid-errand?" via this index.
CREATE INDEX idx_summon_errand_messenger_id
    ON summon_errand (messenger_id);

-- Lookup by summoner — the summoner's walk to the summon_point fires
-- arrival, and we need to find their pending errand row.
CREATE INDEX idx_summon_errand_summoner_active
    ON summon_errand (summoner_id)
    WHERE state NOT IN ('done', 'failed');

-- Allow the new 'summon_ring' village_event type so the ring narration
-- can land on the Around Town ticker. The original CHECK constraint
-- (added by the village_event migration) lists only arrival, departure,
-- and phase transitions; recreate it with the additional value.
ALTER TABLE village_event DROP CONSTRAINT village_event_event_type_check;
ALTER TABLE village_event ADD CONSTRAINT village_event_event_type_check
    CHECK (event_type = ANY (ARRAY[
        'arrival'::text,
        'departure'::text,
        'phase_dawn'::text,
        'phase_midday'::text,
        'phase_dusk'::text,
        'summon_ring'::text
    ]));

COMMIT;
