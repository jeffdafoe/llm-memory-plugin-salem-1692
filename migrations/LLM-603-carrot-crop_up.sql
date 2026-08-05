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
DECLARE carrot_produce int; carrot_forage int; carrot_total int;
BEGIN
    -- Fresh schema-only DB (integration harness): no Moses, nothing to flip.
    IF NOT EXISTS (SELECT 1 FROM actor WHERE id = '019da6ae-3376-73fc-8872-1cbb3ada1c78') THEN
        RETURN;
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM actor_attribute
         WHERE actor_id = '019da6ae-3376-73fc-8872-1cbb3ada1c78' AND slug = 'farmer'
           AND jsonb_typeof(params->'restock') = 'array'
    ) THEN
        RAISE EXCEPTION 'LLM-603: Moses James exists but his farmer restock array was not found';
    END IF;

    -- Assert the shape BEFORE mutating it. actor_attribute is checkpoint-
    -- written and live-tunable (umbilical /restock/*), so it can drift between
    -- authoring and deploy — fail loud on drift rather than generalize over
    -- it (code_review). Exactly one carrots entry, and it must still be
    -- source='produce'. max is deliberately NOT asserted: it is a tuning
    -- knob, not part of the shape this migration owns.
    SELECT count(*),
           count(*) FILTER (WHERE e->>'source' = 'produce')
      INTO carrot_total, carrot_produce
      FROM actor_attribute, jsonb_array_elements(params->'restock') e
     WHERE actor_id = '019da6ae-3376-73fc-8872-1cbb3ada1c78' AND slug = 'farmer'
       AND e->>'item' = 'carrots';
    IF carrot_total <> 1 OR carrot_produce <> 1 THEN
        RAISE EXCEPTION 'LLM-603: expected exactly one carrots/produce restock entry on Moses''s farmer row, found % carrots entr(y/ies) of which % produce', carrot_total, carrot_produce;
    END IF;

    -- Rebuilt element-wise so Moses's wheat entry is untouched, and scoped to
    -- item AND source so the flip touches only the entry just asserted.
    -- COALESCE because jsonb_agg over an EMPTY array returns SQL NULL, and
    -- jsonb_set with a NULL replacement yields NULL for the whole params
    -- document.
    UPDATE actor_attribute
       SET params = jsonb_set(params, '{restock}', COALESCE((
               SELECT jsonb_agg(
                          CASE WHEN e->>'item' = 'carrots' AND e->>'source' = 'produce'
                               THEN jsonb_set(e, '{source}', '"forage"')
                               ELSE e END
                          ORDER BY ord)
                 FROM jsonb_array_elements(params->'restock') WITH ORDINALITY AS t(e, ord)
           ), '[]'::jsonb))
     WHERE actor_id = '019da6ae-3376-73fc-8872-1cbb3ada1c78'  -- Moses James
       AND slug = 'farmer'
       AND jsonb_typeof(params->'restock') = 'array';

    -- Outcome assert, cardinality included: exactly one carrots entry remains
    -- and it is now forage.
    SELECT count(*),
           count(*) FILTER (WHERE e->>'source' = 'forage')
      INTO carrot_total, carrot_forage
      FROM actor_attribute, jsonb_array_elements(params->'restock') e
     WHERE actor_id = '019da6ae-3376-73fc-8872-1cbb3ada1c78' AND slug = 'farmer'
       AND e->>'item' = 'carrots';
    IF carrot_total <> 1 OR carrot_forage <> 1 THEN
        RAISE EXCEPTION 'LLM-603: carrots restock flip did not land — % carrots entr(y/ies), % forage', carrot_total, carrot_forage;
    END IF;
END $$;

COMMIT;
