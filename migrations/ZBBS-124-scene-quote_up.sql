-- ZBBS-124: scene_quote table — structural price tracking.
--
-- Records the most recent unit price an NPC has quoted for an item in
-- a given scene huddle, so the pay handler can enforce that buyer
-- offers honor seller quotes. Without this guard the engine accepts
-- any non-negative amount, which means an LLM merchant can quote "3
-- coins" and the buyer can pay 1 with no pushback.
--
-- Keyed by (huddle_id, from_actor_id, item_kind) — one quote per
-- vendor per item per scene. A new speak with price for the same item
-- in the same scene UPSERTs the row, so the most recent quote wins.
-- Quotes scope to the huddle: when the huddle ends and a new one
-- starts at the same structure, fresh quotes are required.
--
-- ON DELETE CASCADE on huddle_id means the row vanishes when the
-- scene_huddle is deleted (which happens when the structure is
-- deleted, not when the huddle just concludes — concluded huddles
-- linger via concluded_at). Pay-handler reads filter on the buyer's
-- current_huddle_id, so concluded-but-undeleted quotes don't leak
-- across scenes.
--
-- ON DELETE CASCADE on from_actor_id covers the actor-removal path so
-- the row doesn't dangle past the seller's lifetime.

BEGIN;

CREATE TABLE scene_quote (
    huddle_id      uuid                     NOT NULL REFERENCES scene_huddle(id) ON DELETE CASCADE,
    from_actor_id  uuid                     NOT NULL REFERENCES actor(id) ON DELETE CASCADE,
    item_kind      varchar(32)              NOT NULL REFERENCES item_kind(name) ON UPDATE CASCADE,
    unit_price     integer                  NOT NULL CHECK (unit_price >= 0),
    quoted_at      timestamp with time zone NOT NULL DEFAULT NOW(),
    PRIMARY KEY (huddle_id, from_actor_id, item_kind)
);

CREATE INDEX ix_scene_quote_huddle ON scene_quote (huddle_id);

COMMIT;
