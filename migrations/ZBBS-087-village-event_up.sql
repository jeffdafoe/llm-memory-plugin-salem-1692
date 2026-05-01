-- ZBBS-087: Village event log.
--
-- Player-facing feed of "things happening around town" — arrivals,
-- departures, phase transitions. Distinct from agent_action_log
-- (mechanical, includes private decisions) and from world_environment
-- (chronicler atmosphere prose, fed to the top-bar marquee ticker).
--
-- The Village tab on the talk_panel reads from this table; the ticker
-- reads from world_environment. They don't overlap by design — chronicler
-- prose is curated literary atmosphere; this table is mechanical events.
--
-- text is pre-rendered at write time so the row is self-contained for
-- display. Joining to actor/village_object isn't needed at read time.
--
-- x, y record the world position of the event. Nullable because phase
-- transitions are world-wide (no coordinate). Arrivals/departures get
-- the actor's current_x/current_y at write time. Future visibility
-- constraints can filter rows by distance from the player's PC.
--
-- ON DELETE SET NULL on actor and village_object FKs preserves history
-- when the referenced row is deleted — the pre-rendered text stays
-- readable, only the link to the now-gone entity is gone. No companion
-- booleans to keep in sync (unlike actor.inside_structure_id, which
-- caused the Grace Edwards orphan earlier on 2026-05-01).

BEGIN;

CREATE TABLE village_event (
    id           bigserial PRIMARY KEY,
    event_type   text NOT NULL CHECK (event_type IN (
        'arrival',
        'departure',
        'phase_dawn',
        'phase_midday',
        'phase_dusk'
    )),
    text         text NOT NULL,
    actor_id     uuid REFERENCES actor(id) ON DELETE SET NULL,
    structure_id uuid REFERENCES village_object(id) ON DELETE SET NULL,
    x            double precision,
    y            double precision,
    occurred_at  timestamptz NOT NULL DEFAULT now(),
    -- Coordinates are paired: either both set (located event) or both
    -- NULL (world-wide event like a phase transition). Half-located
    -- rows would break future visibility-radius filtering.
    CONSTRAINT village_event_xy_paired CHECK (
        (x IS NULL AND y IS NULL) OR (x IS NOT NULL AND y IS NOT NULL)
    )
);

-- Composite (occurred_at DESC, id DESC) matches the recent-N query's
-- exact ORDER BY tie-breaker so Postgres can satisfy the LIMIT from the
-- index without a follow-up sort.
CREATE INDEX village_event_recent_idx ON village_event (occurred_at DESC, id DESC);

COMMIT;
