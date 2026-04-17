-- ZBBS-031: Drop every asset whose states live on village accessories 16x32.png.
-- Jeff flagged the whole sheet as "nothing useful." 17 assets affected (8 banners,
-- Hanging Lantern (Mini), Cabbage, Broom, Tall Barrel, Tall Crate, Dark Crate,
-- Light Crate, Straw Crate, Hay Stack). All verified 0 placements.
-- All assets had states only on this sheet, so no orphaned states result.

CREATE TEMP TABLE _nuke_16x32 AS
SELECT DISTINCT asset_id FROM asset_state
WHERE sheet = '/tilesets/mana-seed/village-accessories/village accessories 16x32.png';

-- Tags auto-delete via ON DELETE CASCADE on asset_state_tag.state_id FK.
DELETE FROM asset_state
WHERE sheet = '/tilesets/mana-seed/village-accessories/village accessories 16x32.png';

DELETE FROM asset WHERE id IN (SELECT asset_id FROM _nuke_16x32);

DROP TABLE _nuke_16x32;
