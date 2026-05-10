-- ZBBS-HOME-244 — Buy walker trip tracking.
--
-- Tracks an NPC's in-progress restock trip across ticks. Inserted
-- when the buy dispatcher picks a candidate and starts the outbound
-- walk; updated to phase='inbound' after the on-arrival transaction;
-- deleted when the return arrival fires.
--
-- One row per actor (PK on actor_id alone). An actor can be on at
-- most one restock trip at a time — if multiple buy entries are
-- below target on the same tick, the first-listed one wins (matches
-- the design's first-listed priority rule).
--
-- Engine restart safety: rows persist across restarts. The
-- dispatcher's tick handler clears stale rows (phase outbound but
-- no walk in progress, or any row older than 30 minutes) so a crash
-- mid-trip doesn't leave the actor permanently mid-restock.

BEGIN;

CREATE TABLE actor_restock_in_progress (
    actor_id        UUID PRIMARY KEY REFERENCES actor(id) ON DELETE CASCADE,
    seller_id       UUID NOT NULL REFERENCES actor(id) ON DELETE CASCADE,
    item_kind       VARCHAR(32) NOT NULL REFERENCES item_kind(name) ON UPDATE CASCADE,
    seller_structure_id UUID NOT NULL REFERENCES village_object(id) ON DELETE CASCADE,
    home_x          DOUBLE PRECISION NOT NULL,
    home_y          DOUBLE PRECISION NOT NULL,
    phase           VARCHAR(16) NOT NULL CHECK (phase IN ('outbound','inbound')),
    started_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_actor_restock_in_progress_seller_structure
    ON actor_restock_in_progress (seller_structure_id);

COMMIT;
