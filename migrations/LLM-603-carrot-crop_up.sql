-- LLM-603: growable carrots — the second staged crop on the LLM-576 mechanism,
-- and carrots moving from produced to foraged.
--
-- Migration only, no engine code: berry_state.go's crop path is stage-count
-- agnostic and keys on the 'growth-N' state tags. Same three parts as wheat:
--   1. The art: the crop-carrot asset with its six growth states off the
--      Mana Seed single-crop carrot sheet (already on the VPS, same 160x32
--      layout as the wheat sheet).
--   2. The policy: an asset_refresh_default row so every plant the editor
--      drops lands with a full, working gatherable row.
--   3. The economy: carrots stop being manufactured and start being harvested,
--      via Moses's restock entry alone (item_recipe is left untouched — the
--      dormant row carries carrots' wholesale/retail prices, still read by
--      perception/trade_value.go and perception/restock.go).
--
-- The refresh template is authored at wheat's LIVE tuning (2 units / 168h),
-- not the LLM-576 migration seed (3 / 120h): production retuned the wheat
-- template after that migration shipped, and the crops codebase note flags
-- the divergence. Sizing at 2/168h: plants x 2 / 7 days; Moses moved ~8.4
-- carrots/day over the 21 days to 2026-08-05, so ~52 plants gives the same
-- ~2x headroom the wheat field has. Plant count is a look knob — Jeff places
-- the field himself.
--
-- WHY SIX STATES FOR A FIVE-STAGE SHEET: the Mana Seed row is [icon, seedbag,
-- seeds, stage1..stage5, sign icon, map sign]. The SEEDS cell is the
-- just-pulled visual ahead of the artist's five, so a harvested plant reads as
-- sown ground rather than as an already-sprouted seedling. The tag ordinal is
-- only an ORDER, not a claim about the artist's numbering.
--
-- asset / asset_state / asset_state_tag / asset_refresh_default are REFERENCE
-- data: load-only, no snapshot_gen, never checkpoint-clobbered by the engine.
-- actor_attribute IS checkpoint-written (raw params bytes, actors.go
-- SaveSnapshot step 30), so part 3's restock edit needs the engine stopped —
-- which the deploy already does (stop -> migrate -> start). An ad-hoc apply
-- must stop it first.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. Pack + asset + states
-- ---------------------------------------------------------------------------

-- The pack already exists from LLM-576; this is here so the migration is
-- self-contained against a fresh database.
INSERT INTO tileset_pack (id, name, url)
VALUES ('mana-seed-crops', 'Farming Crops', 'https://seliel-the-shaper.itch.io/farming-crops')
ON CONFLICT (id) DO NOTHING;

-- DO NOTHING is silent about WHY it did nothing, so assert the row we ended up
-- with is the row we meant. A PRESENCE check rather than a mismatch check so
-- it covers absence too — a mismatch-only test passes vacuously when there is
-- no row at all, deferring the failure to a murkier FK violation below.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM tileset_pack
         WHERE id = 'mana-seed-crops'
           AND name = 'Farming Crops'
           AND url = 'https://seliel-the-shaper.itch.io/farming-crops'
    ) THEN
        RAISE EXCEPTION
            'LLM-603: expected mana-seed-crops tileset_pack row is missing or has incompatible metadata';
    END IF;
END $$;

-- Fixed UUID rather than gen_random_uuid(): the asset_state and
-- asset_refresh_default inserts below reference it, and the _down needs to
-- find it again. Rendering fields match crop-wheat exactly.
INSERT INTO asset (
    id, name, category, default_state, anchor_x, anchor_y, layer, pack_id, z_index, is_obstacle)
VALUES (
    '019e5f00-c401-7a10-9e00-000000000603',
    'Carrots', 'crops',
    -- Placed plants come down ripe: asset_refresh_default seeds them full, so
    -- default_state matching the ripe stage means what you drop in the editor
    -- is what the engine derives a moment later.
    'growth-6',
    0.5, 0.85, 'objects', 'mana-seed-crops', 10,
    -- Not an obstacle: the farmer has to be able to stand in his own field.
    false);

-- Six 16x32 cells off the single-crop carrot sheet. src_x steps by 16; the
-- first is the seeds cell (32), then the artist's five stages (48..112).
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT '019e5f00-c401-7a10-9e00-000000000603', v.state,
       '/tilesets/mana-seed/farming-crops-1/sheets by crop/farming crops (carrot) 16x32.png',
       v.src_x, 0, 16, 32, 1, 0
  FROM (VALUES
        ('growth-1',  32),   -- scattered seed — just harvested and resown
        ('growth-2',  48),
        ('growth-3',  64),
        ('growth-4',  80),
        ('growth-5',  96),
        ('growth-6', 112)    -- ripe; renders whenever the plant has stock
       ) AS v(state, src_x);

-- The tag, not the state name, is what berry_state.go reads. They match here
-- for legibility, but the tag is the contract.
INSERT INTO asset_state_tag (state_id, tag)
SELECT s.id, s.state
  FROM asset_state s
 WHERE s.asset_id = '019e5f00-c401-7a10-9e00-000000000603';

-- ---------------------------------------------------------------------------
-- 2. Placement template
-- ---------------------------------------------------------------------------

-- Every new plant gets this row copied onto it by CreateVillageObject, seeded
-- to available_quantity = max_quantity (seedRefreshesFromDefaults ->
-- normalizeDefaultSupply), so a plant drops in genuinely ripe.
--
-- PERIODIC, not continuous, and that choice is load-bearing: periodic holds
-- the plant at 0 for the whole period and then jumps to full, so an unripe
-- plant has no stock and every existing gate already refuses it — the Gather
-- command, ResolveGatherSource, the at-source cue, and the forage move handle.
-- No separate ripeness gate exists or is needed. Continuous would trickle
-- units in and make half-grown carrots harvestable.
INSERT INTO asset_refresh_default (
    asset_id, attribute, amount, available_quantity, max_quantity,
    refresh_mode, refresh_period_hours, gather_item)
VALUES (
    '019e5f00-c401-7a10-9e00-000000000603', NULL, 0, 2, 2, 'periodic', 168, 'carrots');

-- ---------------------------------------------------------------------------
-- 3. Carrots: produced -> foraged
-- ---------------------------------------------------------------------------

-- THE RESTOCK ENTRY IS THE WHOLE SWITCH — item_recipe is deliberately
-- untouched. StartProductionCycle checks produceEntry(actor, kind) BEFORE
-- makeableRecipe, and every other produce surface iterates
-- RestockPolicy.ProduceEntries(). Moses is the only actor with a produce entry
-- for carrots (John Ellis and Josiah both buy), so flipping his entry to
-- forage removes carrots from the produce path entirely.
DO $$
DECLARE n int;
BEGIN
    -- Rebuilt element-wise so Moses's wheat entry is untouched. COALESCE
    -- because jsonb_agg over an EMPTY array returns SQL NULL, and jsonb_set
    -- with a NULL replacement yields NULL for the whole params document.
    UPDATE actor_attribute
       SET params = jsonb_set(params, '{restock}', COALESCE((
               SELECT jsonb_agg(
                          CASE WHEN e->>'item' = 'carrots'
                               THEN jsonb_set(e, '{source}', '"forage"')
                               ELSE e END
                          ORDER BY ord)
                 FROM jsonb_array_elements(params->'restock') WITH ORDINALITY AS t(e, ord)
           ), '[]'::jsonb))
     WHERE actor_id = '019da6ae-3376-73fc-8872-1cbb3ada1c78'  -- Moses James
       AND slug = 'farmer'
       AND jsonb_typeof(params->'restock') = 'array';
    GET DIAGNOSTICS n = ROW_COUNT;
    -- 0 rows is the unseeded case (fresh schema-only DB / integration
    -- harness) — fine. But if Moses exists without his farmer row, fail loud
    -- rather than leave the field unworked.
    IF n = 0 AND EXISTS (SELECT 1 FROM actor WHERE id = '019da6ae-3376-73fc-8872-1cbb3ada1c78') THEN
        RAISE EXCEPTION 'LLM-603: Moses James exists but his farmer actor_attribute row was not found';
    END IF;
    -- Assert the OUTCOME, not just that a row was touched: the UPDATE reports
    -- one affected row even when the array held no carrots entry to flip.
    IF EXISTS (
        SELECT 1 FROM actor_attribute
         WHERE actor_id = '019da6ae-3376-73fc-8872-1cbb3ada1c78' AND slug = 'farmer'
           AND NOT EXISTS (
               SELECT 1 FROM jsonb_array_elements(params->'restock') e
                WHERE e->>'item' = 'carrots' AND e->>'source' = 'forage')
    ) THEN
        RAISE EXCEPTION 'LLM-603: Moses James has no forage restock entry for carrots after the flip';
    END IF;
END $$;

COMMIT;
