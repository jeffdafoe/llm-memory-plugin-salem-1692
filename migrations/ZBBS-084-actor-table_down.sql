-- ZBBS-084 down — restore the three-table NPC/PC/village_agent layout.
--
-- Recreates `npc`, `village_agent`, `pc_position` with their original
-- shapes, reverse-backfills from `actor`, repoints downstream FKs back
-- to npc(id), then drops actor.
--
-- Rolling back a migration this large is unusual. The down is here for
-- emergency rollback during initial deploy validation; after a few days
-- of post-deploy stability it stops being a realistic recovery path
-- (engine code on main has been rewritten against `actor` and a rollback
-- would also need to revert the engine code).

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. Recreate village_agent
-- ---------------------------------------------------------------------------
CREATE TABLE village_agent (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name               VARCHAR(100) NOT NULL UNIQUE,
    llm_memory_agent   VARCHAR(100) NOT NULL UNIQUE,
    llm_memory_api_key VARCHAR(255) NOT NULL,
    role               VARCHAR(50)  NOT NULL,
    coins              INTEGER      NOT NULL DEFAULT 20,
    is_virtual         BOOLEAN      NOT NULL DEFAULT true,
    created_at         TIMESTAMP(0) NOT NULL DEFAULT now(),
    location_type      VARCHAR(10)  NOT NULL DEFAULT 'off-map',
    location_object_id UUID REFERENCES village_object(id) ON DELETE SET NULL,
    location_x         DOUBLE PRECISION,
    location_y         DOUBLE PRECISION,
    hunger             SMALLINT     NOT NULL DEFAULT 0,
    thirst             SMALLINT     NOT NULL DEFAULT 0,
    tiredness          SMALLINT     NOT NULL DEFAULT 0
);
CREATE INDEX idx_village_agent_location_object ON village_agent (location_object_id);

-- Reverse-backfill village_agent from actor (NPCs only).
-- The "name" column was the slug (e.g., "ezekiel-crane") — derive it from
-- llm_memory_agent by stripping the "zbbs-" prefix that all current NPCs
-- have. If a future NPC came in without that prefix the slug fallback uses
-- llm_memory_agent verbatim.
INSERT INTO village_agent (
    name, llm_memory_agent, llm_memory_api_key, role,
    coins, hunger, thirst, tiredness,
    location_type, location_object_id, location_x, location_y,
    created_at
)
SELECT
    COALESCE(NULLIF(REPLACE(a.llm_memory_agent, 'zbbs-', ''), ''), a.llm_memory_agent),
    a.llm_memory_agent,
    a.llm_memory_api_key,
    a.role,
    a.coins, a.hunger, a.thirst, a.tiredness,
    CASE WHEN a.inside_structure_id IS NOT NULL THEN 'inside' ELSE 'on-map' END,
    a.inside_structure_id,
    a.current_x, a.current_y,
    a.created_at
FROM actor a
WHERE a.llm_memory_agent IS NOT NULL;

-- ---------------------------------------------------------------------------
-- 2. Recreate npc
-- ---------------------------------------------------------------------------
CREATE TABLE npc (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    display_name             VARCHAR(100) NOT NULL,
    sprite_id                UUID NOT NULL REFERENCES npc_sprite(id),
    home_x                   DOUBLE PRECISION NOT NULL,
    home_y                   DOUBLE PRECISION NOT NULL,
    current_x                DOUBLE PRECISION NOT NULL,
    current_y                DOUBLE PRECISION NOT NULL,
    facing                   VARCHAR(5) NOT NULL DEFAULT 'south'
                                 CHECK (facing IN ('north', 'south', 'east', 'west')),
    llm_memory_agent         VARCHAR(100) REFERENCES village_agent(llm_memory_agent),
    created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
    behavior                 VARCHAR(32) REFERENCES npc_behavior(slug)
                                 ON UPDATE CASCADE ON DELETE SET NULL,
    home_structure_id        UUID REFERENCES village_object(id) ON DELETE SET NULL,
    work_structure_id        UUID REFERENCES village_object(id) ON DELETE SET NULL,
    inside                   BOOLEAN NOT NULL DEFAULT false,
    inside_structure_id      UUID REFERENCES village_object(id) ON DELETE SET NULL,
    schedule_interval_hours  INTEGER CHECK (schedule_interval_hours IS NULL OR (schedule_interval_hours BETWEEN 1 AND 24)),
    active_start_hour        INTEGER CHECK (active_start_hour IS NULL OR (active_start_hour BETWEEN 0 AND 23)),
    active_end_hour          INTEGER CHECK (active_end_hour IS NULL OR (active_end_hour BETWEEN 0 AND 23)),
    last_shift_tick_at       TIMESTAMPTZ,
    lateness_window_minutes  INTEGER NOT NULL DEFAULT 0
                                 CHECK (lateness_window_minutes BETWEEN 0 AND 180),
    social_tag               VARCHAR(64),
    social_last_boundary_at  TIMESTAMPTZ,
    schedule_start_minute    SMALLINT CHECK (schedule_start_minute IS NULL OR (schedule_start_minute BETWEEN 0 AND 1439)),
    schedule_end_minute      SMALLINT CHECK (schedule_end_minute IS NULL OR (schedule_end_minute BETWEEN 0 AND 1439)),
    social_start_minute      SMALLINT CHECK (social_start_minute IS NULL OR (social_start_minute BETWEEN 0 AND 1439)),
    social_end_minute        SMALLINT CHECK (social_end_minute IS NULL OR (social_end_minute BETWEEN 0 AND 1439)),
    agent_override_until     TIMESTAMPTZ,
    last_agent_tick_at       TIMESTAMPTZ,
    current_huddle_id        UUID REFERENCES scene_huddle(id) ON DELETE SET NULL,
    CONSTRAINT npc_schedule_window_all_or_none CHECK (
        (schedule_start_minute IS NULL AND schedule_end_minute IS NULL)
        OR (schedule_start_minute IS NOT NULL AND schedule_end_minute IS NOT NULL)
    ),
    CONSTRAINT social_all_or_none CHECK (
        (social_tag IS NULL AND social_start_minute IS NULL AND social_end_minute IS NULL)
        OR (social_tag IS NOT NULL AND social_start_minute IS NOT NULL AND social_end_minute IS NOT NULL)
    ),
    CONSTRAINT schedule_all_or_none CHECK (
        (schedule_interval_hours IS NULL AND active_start_hour IS NULL AND active_end_hour IS NULL)
        OR (schedule_interval_hours IS NOT NULL AND active_start_hour IS NOT NULL AND active_end_hour IS NOT NULL)
    )
);
CREATE INDEX idx_npc_current_huddle ON npc (current_huddle_id) WHERE current_huddle_id IS NOT NULL;

-- Reverse-backfill npc from actor (NPCs only). Preserves actor.id → npc.id
-- so agent_action_log/npc_acquaintance FKs survive the round trip.
INSERT INTO npc (
    id, display_name, sprite_id, home_x, home_y, current_x, current_y,
    facing, llm_memory_agent, created_at, behavior,
    home_structure_id, work_structure_id, inside, inside_structure_id,
    schedule_interval_hours, active_start_hour, active_end_hour,
    last_shift_tick_at, lateness_window_minutes,
    social_tag, social_last_boundary_at,
    schedule_start_minute, schedule_end_minute,
    social_start_minute, social_end_minute,
    agent_override_until, last_agent_tick_at, current_huddle_id
)
SELECT
    a.id, a.display_name, a.sprite_id, a.home_x, a.home_y, a.current_x, a.current_y,
    a.facing, a.llm_memory_agent, a.created_at, a.behavior,
    a.home_structure_id, a.work_structure_id, a.inside, a.inside_structure_id,
    a.schedule_interval_hours, a.active_start_hour, a.active_end_hour,
    a.last_shift_tick_at, a.lateness_window_minutes,
    a.social_tag, a.social_last_boundary_at,
    a.schedule_start_minute, a.schedule_end_minute,
    a.social_start_minute, a.social_end_minute,
    a.agent_override_until, a.last_agent_tick_at, a.current_huddle_id
FROM actor a
WHERE a.login_username IS NULL;
-- All non-PC actors come back as npc rows. Decorative NPCs (both NULL)
-- ride along with llm_memory_agent NULL, matching their original state.

-- ---------------------------------------------------------------------------
-- 3. Recreate pc_position
-- ---------------------------------------------------------------------------
CREATE TABLE pc_position (
    actor_name          VARCHAR(100) PRIMARY KEY,
    x                   DOUBLE PRECISION NOT NULL,
    y                   DOUBLE PRECISION NOT NULL,
    inside_structure_id UUID REFERENCES village_object(id) ON DELETE SET NULL,
    current_huddle_id   UUID REFERENCES scene_huddle(id) ON DELETE SET NULL,
    last_seen_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    character_name      VARCHAR(100) NOT NULL,
    home_structure_id   UUID REFERENCES village_object(id) ON DELETE SET NULL
);
CREATE INDEX idx_pc_position_character ON pc_position (character_name);
CREATE INDEX idx_pc_position_huddle    ON pc_position (current_huddle_id) WHERE current_huddle_id IS NOT NULL;
CREATE INDEX idx_pc_position_inside    ON pc_position (inside_structure_id) WHERE inside_structure_id IS NOT NULL;

INSERT INTO pc_position (
    actor_name, x, y, inside_structure_id, current_huddle_id, last_seen_at,
    character_name, home_structure_id
)
SELECT
    a.login_username, a.current_x, a.current_y,
    a.inside_structure_id, a.current_huddle_id, a.last_seen_at,
    a.display_name, a.home_structure_id
FROM actor a
WHERE a.login_username IS NOT NULL;

-- ---------------------------------------------------------------------------
-- 4. Repoint downstream FKs back to npc(id)
-- ---------------------------------------------------------------------------
ALTER TABLE agent_action_log DROP CONSTRAINT agent_action_log_actor_id_fkey;
ALTER TABLE agent_action_log RENAME COLUMN actor_id TO npc_id;
ALTER TABLE agent_action_log
    ADD CONSTRAINT agent_action_log_npc_id_fkey
        FOREIGN KEY (npc_id) REFERENCES npc(id) ON DELETE CASCADE;

ALTER TABLE npc_acquaintance DROP CONSTRAINT npc_acquaintance_actor_id_fkey;
ALTER TABLE npc_acquaintance RENAME COLUMN actor_id TO npc_id;
ALTER TABLE npc_acquaintance
    ADD CONSTRAINT npc_acquaintance_npc_id_fkey
        FOREIGN KEY (npc_id) REFERENCES npc(id) ON DELETE CASCADE;

-- ---------------------------------------------------------------------------
-- 5. Drop the actor table
-- ---------------------------------------------------------------------------
DROP TABLE actor;

COMMIT;
