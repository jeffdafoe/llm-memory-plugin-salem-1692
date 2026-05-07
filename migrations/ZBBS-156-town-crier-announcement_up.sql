-- ZBBS-156: town_crier_announcement table.
--
-- Content layer for the existing Town Crier rotation behavior. The
-- crier walks through notice-board-tagged objects on his route; on
-- each arrival he reads one piece of village news. The schema gives
-- announcements a finite "post count" (the crier voices the same
-- announcement up to max_posts times before retiring it) and an
-- optional hard expiry, so old news doesn't loop forever.
--
-- Authoring path for v1: direct INSERT (admin or seeded). A future
-- ZBBS-N can wire a chronicler tool `record_announcement(text)` so
-- the LLM authors news; the schema is intentionally agnostic so any
-- writer can post.
--
-- Selection rule: crier picks the oldest unexpired row with
-- posted_count < max_posts on each arrival. Ties broken by id ASC
-- (stable, deterministic). Once posted_count == max_posts OR
-- expires_at <= NOW(), the row falls out of the candidate pool.

BEGIN;

CREATE TABLE town_crier_announcement (
    id           BIGSERIAL PRIMARY KEY,
    text         TEXT NOT NULL CHECK (length(trim(text)) > 0),
    authored_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at   TIMESTAMPTZ NULL,
    posted_count INTEGER NOT NULL DEFAULT 0 CHECK (posted_count >= 0),
    max_posts    INTEGER NOT NULL DEFAULT 3 CHECK (max_posts > 0)
);

-- Selection index: ranks active announcements by authored_at ASC.
-- Partial-index predicate can only use IMMUTABLE expressions, so
-- the expires_at gate runs at query time (not in the index). The
-- posted_count predicate is fine because both columns are
-- IMMUTABLE-relative. Most announcements are quickly retired so
-- the partial index stays small either way.
CREATE INDEX ix_town_crier_announcement_active
    ON town_crier_announcement (authored_at ASC, id ASC)
    WHERE posted_count < max_posts;

-- Seed two starter announcements. The expires_at is left NULL so
-- they don't suddenly retire on a date boundary; max_posts=3 means
-- each will be heard three times across the crier's route before
-- falling out.
INSERT INTO town_crier_announcement (text, max_posts) VALUES
    ('Hear ye, hear ye! A traveler hath come to the Tavern and seeks lodging there.', 3),
    ('Hear ye! Goodwife Ward hath fresh berries in her larder for the asking.', 3);

COMMIT;
