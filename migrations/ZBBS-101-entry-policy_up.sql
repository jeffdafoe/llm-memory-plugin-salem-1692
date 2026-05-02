-- ZBBS-101: per-structure entry_policy (replaces per-asset enterable).
--
-- Three states encoded as text + CHECK:
--   'none'   — entry is not a concept (decorative, fences, statues, wells).
--   'owner'  — only actors associated with this structure (home_structure_id
--              or work_structure_id pointing at it) may enter. Non-associated
--              actors get a "knock" interaction instead. Market stalls,
--              private homes.
--   'anyone' — public access. Taverns, inns, public buildings.
--
-- Rationale: enterable was per-asset, which over-coupled the policy to the
-- asset type. The same house glyph can be a private home (owner-only) at
-- one location and a public tavern (anyone) at another — this needs to be
-- per-instance. visible_when_inside stays on asset because rendering IS
-- a property of the glyph (market-stall icons visible inside, house icons
-- not), but who is allowed to enter is a per-placement decision.
--
-- Backfill preserves current behavior (enterable=true → 'anyone',
-- enterable=false → 'none'), then upgrades market-stall instances to
-- 'owner' (the immediate bug Jeff hit: PC clicked his own market stall and
-- walked inside instead of transacting at the boundary). Other owner-only
-- candidates (homes, outhouses) are left to per-instance editor adjustment
-- because they aren't reliably identifiable from asset metadata alone.

BEGIN;

ALTER TABLE village_object
    ADD COLUMN entry_policy TEXT NOT NULL DEFAULT 'none'
    CHECK (entry_policy IN ('none', 'owner', 'anyone'));

-- Preserve current "you go inside on click/arrival" behavior for every
-- structure whose asset was enterable.
UPDATE village_object vo SET entry_policy = 'anyone'
  FROM asset a
 WHERE vo.asset_id = a.id
   AND a.enterable = true;

-- Market stalls: vendor stands inside operating, customers transact at the
-- boundary. visible_when_inside=true means the vendor icon remains visible
-- to passers-by, which is exactly the owner-only pattern.
UPDATE village_object vo SET entry_policy = 'owner'
  FROM asset a
 WHERE vo.asset_id = a.id
   AND a.name LIKE 'Market Stall%';

ALTER TABLE asset DROP COLUMN enterable;

INSERT INTO migrations_applied (migration_name) VALUES ('ZBBS-101-entry-policy');

COMMIT;
