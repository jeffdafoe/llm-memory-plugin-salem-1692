-- ZBBS-084 — Unify NPC/PC into a single `actor` table.
--
-- See shared/tasks/pending/zbbs-084-unified-actor-table for the full design
-- doc. Single transactional migration: build actor, backfill from npc +
-- village_agent + pc_position (preserving npc.id as actor.id for FK
-- continuity), repoint downstream FKs on agent_action_log and
-- npc_acquaintance, drop the old tables.
--
-- "Kind" is implicit in which login column is populated:
--   llm_memory_agent IS NOT NULL  → LLM-driven NPC (runs agent_tick)
--   login_username   IS NOT NULL  → human-driven PC
--   both NULL                     → decorative NPC (has presence, sprite,
--                                    schedule, but no LLM agent loop —
--                                    e.g., Grace Edwards, Constance Scott,
--                                    background villagers who walk around
--                                    but don't speak)
-- The constraint enforces "not both populated" (an actor can't be an agent
-- and a player simultaneously). No `driver` discriminator column.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. Create the unified actor table
-- ---------------------------------------------------------------------------
CREATE TABLE actor (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    display_name             VARCHAR(100) NOT NULL UNIQUE,

    -- Presence (always populated for any actor on the map). sprite_id is
    -- nullable so PCs can exist before the rendering story is settled —
    -- today PCs aren't rendered as map actors, they're parked at the tavern
    -- as a placeholder. NPCs always have a sprite.
    sprite_id                UUID REFERENCES npc_sprite(id),
    current_x                DOUBLE PRECISION NOT NULL,
    current_y                DOUBLE PRECISION NOT NULL,
    facing                   VARCHAR(5) NOT NULL DEFAULT 'south',
    inside                   BOOLEAN NOT NULL DEFAULT false,
    inside_structure_id      UUID REFERENCES village_object(id) ON DELETE SET NULL,
    current_huddle_id        UUID REFERENCES scene_huddle(id) ON DELETE SET NULL,
    home_structure_id        UUID REFERENCES village_object(id) ON DELETE SET NULL,

    -- Wallet & needs (always populated, defaulted). PCs gain coins/needs
    -- columns by being in the same table — pay-side-effects are uniform.
    coins                    INTEGER NOT NULL DEFAULT 20,
    hunger                   SMALLINT NOT NULL DEFAULT 0,
    thirst                   SMALLINT NOT NULL DEFAULT 0,
    tiredness                SMALLINT NOT NULL DEFAULT 0,

    -- LLM-driven only (NULL for players). The presence of llm_memory_agent
    -- is the implicit "is this an LLM-driven NPC?" check used throughout
    -- the engine.
    llm_memory_agent         VARCHAR(100) UNIQUE,
    llm_memory_api_key       VARCHAR(255),
    role                     VARCHAR(50),
    behavior                 VARCHAR(32) REFERENCES npc_behavior(slug)
                                 ON UPDATE CASCADE ON DELETE SET NULL,
    work_structure_id        UUID REFERENCES village_object(id) ON DELETE SET NULL,
    home_x                   DOUBLE PRECISION,  -- spawn point (NPCs only)
    home_y                   DOUBLE PRECISION,

    schedule_interval_hours  INTEGER,
    active_start_hour        INTEGER,
    active_end_hour          INTEGER,
    schedule_start_minute    SMALLINT,
    schedule_end_minute      SMALLINT,
    lateness_window_minutes  INTEGER NOT NULL DEFAULT 0,
    last_shift_tick_at       TIMESTAMPTZ,

    social_tag               VARCHAR(64),
    social_start_minute      SMALLINT,
    social_end_minute        SMALLINT,
    social_last_boundary_at  TIMESTAMPTZ,

    agent_override_until     TIMESTAMPTZ,
    last_agent_tick_at       TIMESTAMPTZ,

    -- Player-driven only (NULL for NPCs). The Salem login name (was
    -- pc_position.actor_name; renamed because "actor_name" was a
    -- misleading label that read like a foreign key into the actor
    -- entity but actually meant the player's account name).
    login_username           VARCHAR(100) UNIQUE,

    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at             TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- An actor can be at most one of {LLM-driven agent, human player}.
    -- Both NULL is fine (decorative NPC). Both NOT NULL is invalid.
    CONSTRAINT actor_driver_not_both CHECK (
        NOT (llm_memory_agent IS NOT NULL AND login_username IS NOT NULL)
    ),

    CONSTRAINT actor_facing_check CHECK (
        facing IN ('north', 'south', 'east', 'west')
    ),

    -- Schedule / social / activity-window CHECKs carried over from npc.
    CONSTRAINT actor_active_start_hour_check CHECK (
        active_start_hour IS NULL OR (active_start_hour >= 0 AND active_start_hour <= 23)
    ),
    CONSTRAINT actor_active_end_hour_check CHECK (
        active_end_hour IS NULL OR (active_end_hour >= 0 AND active_end_hour <= 23)
    ),
    CONSTRAINT actor_schedule_interval_hours_check CHECK (
        schedule_interval_hours IS NULL OR (schedule_interval_hours >= 1 AND schedule_interval_hours <= 24)
    ),
    CONSTRAINT actor_schedule_start_minute_check CHECK (
        schedule_start_minute IS NULL OR (schedule_start_minute >= 0 AND schedule_start_minute <= 1439)
    ),
    CONSTRAINT actor_schedule_end_minute_check CHECK (
        schedule_end_minute IS NULL OR (schedule_end_minute >= 0 AND schedule_end_minute <= 1439)
    ),
    CONSTRAINT actor_social_start_minute_check CHECK (
        social_start_minute IS NULL OR (social_start_minute >= 0 AND social_start_minute <= 1439)
    ),
    CONSTRAINT actor_social_end_minute_check CHECK (
        social_end_minute IS NULL OR (social_end_minute >= 0 AND social_end_minute <= 1439)
    ),
    CONSTRAINT actor_lateness_window_minutes_check CHECK (
        lateness_window_minutes >= 0 AND lateness_window_minutes <= 180
    ),
    CONSTRAINT actor_schedule_window_all_or_none CHECK (
        (schedule_start_minute IS NULL AND schedule_end_minute IS NULL)
        OR (schedule_start_minute IS NOT NULL AND schedule_end_minute IS NOT NULL)
    ),
    CONSTRAINT actor_social_all_or_none CHECK (
        (social_tag IS NULL AND social_start_minute IS NULL AND social_end_minute IS NULL)
        OR (social_tag IS NOT NULL AND social_start_minute IS NOT NULL AND social_end_minute IS NOT NULL)
    ),
    CONSTRAINT actor_schedule_all_or_none CHECK (
        (schedule_interval_hours IS NULL AND active_start_hour IS NULL AND active_end_hour IS NULL)
        OR (schedule_interval_hours IS NOT NULL AND active_start_hour IS NOT NULL AND active_end_hour IS NOT NULL)
    )
);

-- Partial indexes for the "who's here" lookups that drive perception and
-- huddle membership. UNIQUE constraints on llm_memory_agent / login_username
-- / display_name already create B-tree indexes for those.
CREATE INDEX idx_actor_huddle ON actor (current_huddle_id) WHERE current_huddle_id IS NOT NULL;
CREATE INDEX idx_actor_inside ON actor (inside_structure_id) WHERE inside_structure_id IS NOT NULL;

-- ---------------------------------------------------------------------------
-- 2. Backfill actor from npc + village_agent
-- ---------------------------------------------------------------------------
-- Preserves npc.id as actor.id so existing FK references (agent_action_log,
-- npc_acquaintance) stay valid through the rename. LEFT JOIN against
-- village_agent because not every npc row necessarily has a paired
-- village_agent row (defensive — current data has the join populated).
INSERT INTO actor (
    id, display_name, sprite_id, current_x, current_y, facing,
    inside, inside_structure_id, current_huddle_id, home_structure_id,
    coins, hunger, thirst, tiredness,
    llm_memory_agent, llm_memory_api_key, role, behavior, work_structure_id,
    home_x, home_y,
    schedule_interval_hours, active_start_hour, active_end_hour,
    schedule_start_minute, schedule_end_minute, lateness_window_minutes,
    last_shift_tick_at,
    social_tag, social_start_minute, social_end_minute, social_last_boundary_at,
    agent_override_until, last_agent_tick_at,
    created_at
)
SELECT
    n.id, n.display_name, n.sprite_id, n.current_x, n.current_y, n.facing,
    n.inside, n.inside_structure_id, n.current_huddle_id, n.home_structure_id,
    COALESCE(va.coins, 20),
    COALESCE(va.hunger, 0),
    COALESCE(va.thirst, 0),
    COALESCE(va.tiredness, 0),
    n.llm_memory_agent, va.llm_memory_api_key, va.role,
    n.behavior, n.work_structure_id,
    n.home_x, n.home_y,
    n.schedule_interval_hours, n.active_start_hour, n.active_end_hour,
    n.schedule_start_minute, n.schedule_end_minute, n.lateness_window_minutes,
    n.last_shift_tick_at,
    n.social_tag, n.social_start_minute, n.social_end_minute, n.social_last_boundary_at,
    n.agent_override_until, n.last_agent_tick_at,
    n.created_at
FROM npc n
LEFT JOIN village_agent va ON va.llm_memory_agent = n.llm_memory_agent;
-- All npc rows come through — including decorative NPCs without
-- llm_memory_agent. They become actor rows with both llm_memory_agent
-- and login_username NULL, which the constraint allows. Their coins
-- default to 20 (COALESCE catches the missing village_agent row) and
-- needs default to 0 — same starting wallet as a fresh agent NPC,
-- which means a future "decorative NPC takes a coin from a tip jar"
-- feature works without further migration.

-- ---------------------------------------------------------------------------
-- 3. Backfill actor from pc_position
-- ---------------------------------------------------------------------------
-- PCs get fresh actor.id UUIDs (no equivalent existed before). The PC's
-- in-world identity (character_name) becomes display_name. The Salem login
-- (was pc_position.actor_name) becomes login_username.
--
-- last_seen_at carries over so the "logged in within 5min" checks work.
-- coins / hunger / thirst / tiredness use the column defaults (20 / 0 / 0 / 0)
-- which means PCs come into the new schema with the same starting wallet
-- as a fresh NPC.
INSERT INTO actor (
    display_name, current_x, current_y,
    inside_structure_id, current_huddle_id, home_structure_id,
    login_username, last_seen_at
)
SELECT
    pp.character_name, pp.x, pp.y,
    pp.inside_structure_id, pp.current_huddle_id, pp.home_structure_id,
    pp.actor_name, pp.last_seen_at
FROM pc_position pp;

-- ---------------------------------------------------------------------------
-- 4. Repoint downstream FKs (rename npc_id → actor_id on the two tables
--    that referenced npc(id)).
-- ---------------------------------------------------------------------------

-- agent_action_log: every NPC action's npc_id is already a valid actor.id
-- (we preserved npc.id → actor.id). PC actions historically had NULL npc_id
-- and stay NULL after rename — the column remains nullable. Going forward,
-- pc_handlers.go writes the actor.id (post-refactor code change).
ALTER TABLE agent_action_log DROP CONSTRAINT agent_action_log_npc_id_fkey;
ALTER TABLE agent_action_log RENAME COLUMN npc_id TO actor_id;
ALTER TABLE agent_action_log
    ADD CONSTRAINT agent_action_log_actor_id_fkey
        FOREIGN KEY (actor_id) REFERENCES actor(id) ON DELETE CASCADE;

-- npc_acquaintance: same pattern. Acquaintances are NPC-tracked today
-- (other_name can be either an NPC display name or a PC character_name).
-- After rename, actor_id always points at the NPC who's tracking the
-- acquaintance.
ALTER TABLE npc_acquaintance DROP CONSTRAINT npc_acquaintance_npc_id_fkey;
ALTER TABLE npc_acquaintance RENAME COLUMN npc_id TO actor_id;
ALTER TABLE npc_acquaintance
    ADD CONSTRAINT npc_acquaintance_actor_id_fkey
        FOREIGN KEY (actor_id) REFERENCES actor(id) ON DELETE CASCADE;

-- ---------------------------------------------------------------------------
-- 5. Drop the old tables.
-- ---------------------------------------------------------------------------
-- CASCADE is needed because dropping village_agent would otherwise fail on
-- the npc.llm_memory_agent FK that still references it (we drop npc next
-- but PG doesn't process the dependency in the user-friendly order).
-- After this point, every code path must read/write `actor`.
DROP TABLE pc_position;
DROP TABLE npc CASCADE;
DROP TABLE village_agent CASCADE;

COMMIT;
