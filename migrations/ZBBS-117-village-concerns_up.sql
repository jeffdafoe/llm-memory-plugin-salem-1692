-- ZBBS-117: village_concern — chronicler-authored named-entity facts.
--
-- The chronicler authors noticeboard prose and, in the same call, attaches
-- structured concerns to named actors and structures. Targeted NPCs retain
-- the fact in their perception without ever reading the board. The fact's
-- lifetime is bound to the source's "generation" — when the noticeboard
-- rotates, the prior posting's concerns age out of perception in the same
-- UPDATE that overwrites the prose (perception filters on
-- source_generation == current_generation; stale rows are swept lazily).
--
-- The table is generic from day one. v1 source is noticeboards
-- (source_kind='village_object_content'); future sources (village_event,
-- world_environment, engine-direct emergencies) plug in by adding new
-- enum values and emitting rows with a different source_kind.
--
-- Read path: agent_tick.go buildAgentPerception section 3.0b joins
-- village_concern by target match (actor.id, or home/work structure_id)
-- AND source_generation == current_generation, capped at 3 newest per
-- category (you / workplace / home).

BEGIN;

CREATE TYPE concern_source_kind AS ENUM ('village_object_content');
CREATE TYPE concern_target_kind AS ENUM ('actor', 'structure');

CREATE TABLE village_concern (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    source_kind          concern_source_kind NOT NULL,
    source_id            UUID                NOT NULL,
    source_generation    INT                 NOT NULL,
    target_kind          concern_target_kind NOT NULL,
    target_id            UUID                NOT NULL,
    text                 TEXT                NOT NULL,
    created_at           TIMESTAMPTZ         NOT NULL DEFAULT NOW()
);

-- Perception read-path index: every NPC tick joins concerns by target.
CREATE INDEX ix_village_concern_target ON village_concern (target_kind, target_id);

-- Source-side cleanup index: clearConcernsForSource and the lazy janitor
-- both filter by source.
CREATE INDEX ix_village_concern_source ON village_concern (source_kind, source_id);

-- Generation counter on the placement. Bumped on every saveObjectContent
-- and clearObjectContent so prior-posting concerns naturally fail the
-- perception join. Default 0 so existing placements have a starting
-- generation; the first content write bumps to 1.
ALTER TABLE village_object
    ADD COLUMN content_generation INT NOT NULL DEFAULT 0;

COMMIT;
