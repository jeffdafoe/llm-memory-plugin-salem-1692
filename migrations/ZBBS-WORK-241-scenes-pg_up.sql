-- ZBBS-WORK-241: Slice 13 — Scenes pg-impl.
--
-- Creates the v2 `scene` parent table + `scene_huddle_ref` child table.
-- Option (A) "minimal + cascade-lifetime" — persists load-bearing small
-- fields (ID, OriginAt, OriginKind, Bound, OriginPosition) and the
-- canonical scene→huddle mapping. Drops ParticipantStateAtOrigin (heavy,
-- loop-detection re-anchors on restart) and QuoteIDs (already rebuilt
-- at LoadWorld via PR S3's rebuildSceneQuoteIndex).
--
-- Closes the outdoor-huddle round-trip gap Slice 11 left open: Slice 11
-- stored Huddle.StructureID = NULL for outdoor; this slice stores the
-- matching Scene's Bound{Anchor, Radius} so the spatial anchor survives
-- restart.
--
-- Pure additive migration — no v1 reshape. v1 `scenes` table (plural,
-- ZBBS-118) is left untouched; it's the admin UI's chat-attribution
-- log, shape-incompatible with v2's active-scene state. v2 uses the
-- new singular `scene` table.
--
-- Shared-Identity Bridge: bound_structure_id is a TEXT soft-ref to
-- structure.id per Slice 12 precedent. No FK (cross-aggregate; Structures
-- aggregate owns structure). LoadWorld orchestrator may drop scenes
-- whose bound_structure_id references missing structures — out of
-- scope here.
--
-- Cross-aggregate soft-ref: scene_huddle_ref.huddle_id references
-- scene_huddle(id) (the v1-named huddle table reshaped by Slice 11).
-- No FK — different aggregates. Loud orphan check at LoadWorld.
--
-- Unbounded scenes (chronicler atmosphere refresh, admin trigger pokes)
-- are explicitly NOT persisted — they "never officially end" in v2 and
-- would accumulate unboundedly. The bound_shape CHECK forbids
-- bound_kind='unbounded'; SaveSnapshot filters them; both layers
-- enforce.

BEGIN;

-- Parent table.
CREATE TABLE scene (
    id                  TEXT PRIMARY KEY,
    origin_at           TIMESTAMPTZ NOT NULL,
    origin_kind         TEXT NOT NULL,

    bound_kind          TEXT NOT NULL,
    bound_structure_id  TEXT NULL,
    bound_anchor_x      INT NULL,
    bound_anchor_y      INT NULL,
    bound_radius        INT NULL,

    origin_position_x   INT NOT NULL,
    origin_position_y   INT NOT NULL,

    snapshot_gen        BIGINT NOT NULL DEFAULT 0,

    CONSTRAINT scene_id_nonempty                 CHECK (btrim(id) <> ''),
    CONSTRAINT scene_origin_kind_nonempty        CHECK (btrim(origin_kind) <> ''),
    CONSTRAINT scene_bound_structure_id_nonempty CHECK (bound_structure_id IS NULL OR btrim(bound_structure_id) <> ''),
    -- Combined variant-shape CHECK (design_review preferred form):
    -- one of the two valid shapes (structure or area), fully populated
    -- for its kind and fully NULL for the other kind's fields.
    -- Implicitly forbids bound_kind='unbounded' since neither branch
    -- matches — defense-in-depth alongside SaveSnapshot's Unbounded
    -- skip. bound_radius >= 0 included here (radius=0 is legal —
    -- clamps to anchor tile only per NewAreaBound).
    CONSTRAINT scene_bound_shape_valid CHECK (
        (bound_kind = 'structure'
         AND bound_structure_id IS NOT NULL
         AND bound_anchor_x IS NULL AND bound_anchor_y IS NULL AND bound_radius IS NULL)
        OR
        (bound_kind = 'area'
         AND bound_structure_id IS NULL
         AND bound_anchor_x IS NOT NULL AND bound_anchor_y IS NOT NULL
         AND bound_radius IS NOT NULL AND bound_radius >= 0)
    )
);

CREATE INDEX idx_scene_snapshot_gen ON scene(snapshot_gen);
CREATE SEQUENCE scene_snapshot_gen_seq START 1;

-- Child table for the canonical Scene.Huddles mapping.
-- Naming: `scene_huddle_ref` (NOT `scene_huddle` — that's the v1
-- huddle parent table reshaped by Slice 11). FK CASCADE to scene means
-- SaveSnapshot's stale-DELETE on scene propagates here.
-- huddle_id is a TEXT soft-ref to scene_huddle(id) — no FK
-- (cross-aggregate; Huddles owns scene_huddle).
CREATE TABLE scene_huddle_ref (
    scene_id     TEXT NOT NULL REFERENCES scene(id) ON DELETE CASCADE,
    huddle_id    TEXT NOT NULL,
    snapshot_gen BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (scene_id, huddle_id),
    CONSTRAINT scene_huddle_ref_scene_id_nonempty  CHECK (btrim(scene_id)  <> ''),
    CONSTRAINT scene_huddle_ref_huddle_id_nonempty CHECK (btrim(huddle_id) <> '')
);

CREATE INDEX idx_scene_huddle_ref_snapshot_gen ON scene_huddle_ref(snapshot_gen);
CREATE SEQUENCE scene_huddle_ref_snapshot_gen_seq START 1;

COMMIT;
