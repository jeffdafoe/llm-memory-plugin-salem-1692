-- ZBBS-112: object content for noticeboards (and future state-driven
-- content surfaces).
--
-- Generic primitive: an asset state can declare a content capacity via
-- an asset_state_tag of the form 'content-capacity-N'. When a placement's
-- current_state advances to a state with capacity > 0, the engine asks
-- an LLM agent (the chronicler, today) to generate content and stores
-- it on the instance. Cycling back to a state with no capacity clears
-- prior content. Generic by name (content_text / content_posted_at) so
-- future content surfaces (wanted posters, market signage, etc.) can
-- reuse the same column without a schema rename.
--
-- First concretion: noticeboards. The five Notice Board variants
-- seeded by ZBBS-025 get per-state capacity tags:
--
--     variant-1 — no tag                  (empty board)
--     variant-2 — content-capacity-1      (1 line)
--     variant-3 — content-capacity-2      (2 lines)
--     variant-4 — content-capacity-3      (3 lines)
--     variant-5 — content-capacity-4      (4 lines)
--
-- The crier's rotation route (startTownCrierRoute) drives the
-- transition; the post-flip hook decides whether to (re)generate
-- content for the new state. Per-instance opt-in via the existing
-- village_object_tag table — admins mark a placed Notice Board with
-- the 'noticeboard_content' tag to activate generation for that
-- specific instance. The 'noticeboard_content' tag itself is registered
-- in the allowedObjectTags allowlist in code (assets.go).
--
-- Idempotent: each insert guards on NOT EXISTS so a re-run (or replay
-- over partially preseeded data) is safe.

BEGIN;

ALTER TABLE village_object
    ADD COLUMN IF NOT EXISTS content_text TEXT NULL,
    ADD COLUMN IF NOT EXISTS content_posted_at TIMESTAMP NULL;

INSERT INTO asset_state_tag (state_id, tag)
SELECT s.id, 'content-capacity-1'
FROM asset_state s
JOIN asset a ON a.id = s.asset_id
WHERE a.name = 'Notice Board' AND a.pack_id = 'mana-seed' AND s.state = 'variant-2'
  AND NOT EXISTS (
      SELECT 1 FROM asset_state_tag t
       WHERE t.state_id = s.id AND t.tag = 'content-capacity-1'
  );

INSERT INTO asset_state_tag (state_id, tag)
SELECT s.id, 'content-capacity-2'
FROM asset_state s
JOIN asset a ON a.id = s.asset_id
WHERE a.name = 'Notice Board' AND a.pack_id = 'mana-seed' AND s.state = 'variant-3'
  AND NOT EXISTS (
      SELECT 1 FROM asset_state_tag t
       WHERE t.state_id = s.id AND t.tag = 'content-capacity-2'
  );

INSERT INTO asset_state_tag (state_id, tag)
SELECT s.id, 'content-capacity-3'
FROM asset_state s
JOIN asset a ON a.id = s.asset_id
WHERE a.name = 'Notice Board' AND a.pack_id = 'mana-seed' AND s.state = 'variant-4'
  AND NOT EXISTS (
      SELECT 1 FROM asset_state_tag t
       WHERE t.state_id = s.id AND t.tag = 'content-capacity-3'
  );

INSERT INTO asset_state_tag (state_id, tag)
SELECT s.id, 'content-capacity-4'
FROM asset_state s
JOIN asset a ON a.id = s.asset_id
WHERE a.name = 'Notice Board' AND a.pack_id = 'mana-seed' AND s.state = 'variant-5'
  AND NOT EXISTS (
      SELECT 1 FROM asset_state_tag t
       WHERE t.state_id = s.id AND t.tag = 'content-capacity-4'
  );

-- Seed the per-instance opt-in on any existing Notice Board placement so
-- the feature works out of the box on a village that was set up before
-- ZBBS-112 landed. New placements start untagged; an admin opts them in
-- via the editor's tag UI (or this seed pattern in a follow-on migration).
INSERT INTO village_object_tag (object_id, tag)
SELECT o.id, 'noticeboard_content'
FROM village_object o
JOIN asset a ON a.id = o.asset_id
WHERE a.name = 'Notice Board' AND a.pack_id = 'mana-seed'
  AND NOT EXISTS (
      SELECT 1 FROM village_object_tag t
       WHERE t.object_id = o.id AND t.tag = 'noticeboard_content'
  );

COMMIT;
