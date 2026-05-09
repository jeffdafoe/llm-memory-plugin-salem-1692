-- ZBBS-WORK-201 — Add visitor fields to the actor table.
--
-- Visitors are transient VAs that arrive in the village, hang around,
-- carry a payload (news/rumor/letter/goods/quest_hook), and depart.
-- Replaces the chronicler's deleted role of injecting "outside news"
-- into the village. See shared/tasks/pending/zbbs-work-201-visitor-archetype/design.
--
-- All columns are nullable. NULL on `visitor_expires_at` is the implicit
-- "this is a persistent NPC, not a visitor" check used throughout the
-- engine. A persistent NPC's row never has any visitor_* fields set.
--
-- This migration is purely additive — it doesn't touch any existing
-- column, constraint, or index. Safe to land alongside the chronicler
-- deletion (ZBBS-HOME-202) without conflict.

BEGIN;

ALTER TABLE actor ADD COLUMN visitor_expires_at TIMESTAMPTZ;
ALTER TABLE actor ADD COLUMN visitor_archetype  VARCHAR(50);
ALTER TABLE actor ADD COLUMN visitor_origin     VARCHAR(100);
ALTER TABLE actor ADD COLUMN visitor_disposition VARCHAR(50);

-- Partial index: cleanup / despawn / count-concurrent queries all filter
-- on `visitor_expires_at IS NOT NULL`. Persistent NPCs (the vast
-- majority of rows) are skipped by the index.
CREATE INDEX idx_actor_visitor_expires
    ON actor (visitor_expires_at)
    WHERE visitor_expires_at IS NOT NULL;

COMMIT;
