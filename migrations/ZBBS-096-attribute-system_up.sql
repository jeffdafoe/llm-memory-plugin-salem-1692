-- ZBBS-095: attribute system — actor (and later object) role assignment.
--
-- Replaces the single-slug actor.behavior + npc_behavior lookup with a
-- many-to-many attribute system. An attribute_definition row carries the
-- role's tools (LLM-side, for VA-attached actors), instructions (prompt
-- copy injected when VA-attached), and behaviors (deterministic Go
-- routines that fire regardless of VA attachment). actor_attribute is
-- the join table from actor to attribute_definition.
--
-- Naming note: this collides with the pre-existing "attribute" vocabulary
-- used by engine/attributes.go for hunger/thirst/tiredness ticking. The
-- needs system will be renamed in a follow-up; here we use Jeff's
-- preferred term for the chip/role concept.
--
-- Existing schema (kept intact during transition):
--   * actor.behavior     — single varchar(32) FK, kept while engine still
--                          dispatches the legacy switch.
--   * npc_behavior(slug, display_name) — lookup table, kept while FK
--                          on actor.behavior is live.
-- A follow-up migration will retire both once the engine has fully
-- migrated to attribute-driven dispatch.
--
-- Seed data: ports the three live behavior slugs (lamplighter,
-- washerwoman, town_crier) into attribute_definition rows with their
-- behavior specs populated. The fourth slug (worker) is intentionally
-- NOT ported — it has no engine dispatch today and would carry no
-- tools, instructions, or behaviors. Existing actors with behavior=
-- 'worker' simply do not get an actor_attribute row; their behavior
-- column stays set until the column itself is retired.
--
-- Behavior spec shape: each entry in attribute_definition.behaviors is
--   {"type": "<handler-slug>", "params": { ... }}
-- where the type is registered in the engine. lamp_route and
-- rotation_route are the two handlers needed on day one. params
-- follow the per-handler shape — empty for lamp_route (phase is a
-- global), {domain_tag, label} for rotation_route.

BEGIN;

CREATE TABLE attribute_definition (
    slug         VARCHAR(64) PRIMARY KEY,
    display_name VARCHAR(100) NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    -- 'actor' for now; 'object' and 'both' reserved for the later object-
    -- side rollout. Constraint locks the values so a typo doesn't slip in.
    scope        VARCHAR(16) NOT NULL DEFAULT 'actor',
    -- LLM tool slugs the engine should expose when this attribute is held
    -- by a VA-attached actor. Engine validates each slug against its
    -- registered tool handlers at startup; missing handler = log + skip.
    tools        JSONB NOT NULL DEFAULT '[]'::jsonb,
    -- Prompt copy injected into the system prompt for VA-attached actors
    -- holding this attribute. Empty string = no role-specific copy.
    instructions TEXT NOT NULL DEFAULT '',
    -- Array of {type, params} specs that fire deterministically regardless
    -- of VA attachment. Type values are a closed set defined in the engine
    -- (lamp_route, rotation_route, ...).
    behaviors    JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT attribute_definition_scope_check
        CHECK (scope IN ('actor', 'object', 'both'))
);

CREATE TABLE actor_attribute (
    actor_id   UUID NOT NULL REFERENCES actor(id) ON DELETE CASCADE,
    slug       VARCHAR(64) NOT NULL
                 REFERENCES attribute_definition(slug)
                 ON UPDATE CASCADE ON DELETE RESTRICT,
    -- Per-assignment params override / supplement the registry's defaults.
    -- Empty object for the common case; populated by SQL where a specific
    -- actor needs different parameters from the registry default.
    params     JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (actor_id, slug)
);

-- Supports the engine's "find every actor with attribute X" lookup.
-- The PK already indexes (actor_id, slug); this is the inverse direction.
CREATE INDEX idx_actor_attribute_slug ON actor_attribute(slug);

-- Seed the three engine-backed behavior slugs as attributes. tools and
-- instructions are empty for now — no VA-attached lamplighter exists
-- yet, so there's no LLM copy to inject. Behaviors carry the handler
-- type + params the engine will dispatch on once it's been refactored.
INSERT INTO attribute_definition (slug, display_name, description, behaviors) VALUES
    (
        'lamplighter',
        'Lamplighter',
        'Walks the village at dusk lighting lamps, and at dawn extinguishing them. Targets objects whose asset_state carries the lamplighter-target tag and whose current state mismatches the phase target.',
        '[{"type": "lamp_route", "params": {}}]'::jsonb
    ),
    (
        'washerwoman',
        'Washerwoman',
        'Walks the village at the daily rotation boundary, advancing laundry-tagged states by one rotation step.',
        '[{"type": "rotation_route", "params": {"domain_tag": "laundry", "label": "washerwoman"}}]'::jsonb
    ),
    (
        'town_crier',
        'Town Crier',
        'Walks the village at the daily rotation boundary, advancing notice-board-tagged states by one rotation step.',
        '[{"type": "rotation_route", "params": {"domain_tag": "notice-board", "label": "town_crier"}}]'::jsonb
    );

-- Migrate existing actor.behavior assignments into actor_attribute rows.
-- Skips NULL (actors with no behavior) and 'worker' (no engine dispatch,
-- not ported as an attribute). The actor.behavior column stays set on
-- 'worker' actors and is left in place until the column itself retires.
INSERT INTO actor_attribute (actor_id, slug)
SELECT id, behavior FROM actor
WHERE behavior IS NOT NULL AND behavior <> 'worker';

COMMIT;
