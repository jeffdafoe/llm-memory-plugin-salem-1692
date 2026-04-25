-- ZBBS-070: Per-asset occupancy threshold and night-only gating.
--
-- The existing occupancy state-flip (ZBBS-063) treats any non-empty
-- structure as "occupied" — fine for a market stall (one keeper opens
-- it) but wrong for hospitality buildings like a tavern, where a lone
-- keeper alone shouldn't make the windows glow with patrons.
--
-- Two new asset-level columns:
--   occupied_min_count — minimum NPC headcount required to flip to the
--     'occupied' state. Defaults to 1 to preserve existing market-stall
--     behavior. Set to 2 for the tavern (hearth glow needs at least
--     keeper + a patron).
--
--   occupied_night_only — when true, the 'occupied' state is suppressed
--     during day phase regardless of headcount. The tavern glow goes
--     dark at dawn and lights up at dusk (assuming the threshold is
--     also met). Engine handles re-evaluation on phase transitions.

BEGIN;

ALTER TABLE asset
    ADD COLUMN occupied_min_count INTEGER NOT NULL DEFAULT 1,
    ADD COLUMN occupied_night_only BOOLEAN NOT NULL DEFAULT FALSE;

-- Defensive: prevent zero or negative thresholds. Zero would mean
-- "always occupied" (any state with the tag flips on first eval),
-- which has no real use case and would be a footgun.
ALTER TABLE asset
    ADD CONSTRAINT chk_asset_occupied_min_count CHECK (occupied_min_count >= 1);

-- Configure Black House (Medium) — currently the only tavern asset —
-- with a threshold of 2 (keeper + patron) and night-only gating
-- (no warm hearth glow during daylight hours).
UPDATE asset
   SET occupied_min_count = 2,
       occupied_night_only = TRUE
 WHERE name = 'Black House (Medium)';

COMMIT;
