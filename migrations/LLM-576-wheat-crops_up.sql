-- LLM-576: growable wheat — the crops asset category, a staged crop-wheat asset,
-- and wheat moving from produced to foraged.
--
-- Three parts:
--   1. The art: a new tileset pack + the crop-wheat asset with its six growth
--      states, tagged 'growth-1'..'growth-6' so berry_state.go's crop path picks
--      which one renders.
--   2. The policy: an asset_refresh_default row (LLM-363), so every plant the
--      editor drops lands with a full, working gatherable row instead of inert.
--   3. The economy: wheat stops being manufactured and starts being harvested,
--      via Moses's restock entry alone (item_recipe is left untouched).
--
-- WHY SIX STATES FOR A FIVE-STAGE SHEET: the Mana Seed row is [icon, seedbag,
-- seeds, stage1..stage5, sign icon, map sign]. We use the SEEDS cell as the
-- just-cut visual ahead of the artist's five, so a reaped plant reads as sown
-- ground rather than as an already-sprouted seedling. That makes five immature
-- stages across a 120h period — one visible change per day — with the sixth,
-- the artist's ripe stage 5, rendering whenever the plant has stock.
--
-- The tag ordinal is only an ORDER, not a claim about the artist's numbering.
--
-- asset / asset_state / asset_state_tag / asset_refresh_default are REFERENCE
-- data: load-only, no snapshot_gen, never checkpoint-clobbered by the engine.
-- actor_attribute IS checkpoint-written (raw params bytes, actors.go SaveSnapshot
-- step 30), so part 3's restock edit needs the engine stopped — which the deploy
-- already does (stop -> migrate -> start). An ad-hoc apply must stop it first.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. Pack + asset + states
-- ---------------------------------------------------------------------------

INSERT INTO tileset_pack (id, name, url)
VALUES ('mana-seed-crops', 'Farming Crops', 'https://seliel-the-shaper.itch.io/farming-crops')
ON CONFLICT (id) DO NOTHING;

-- Fixed UUID rather than gen_random_uuid(): the asset_state and
-- asset_refresh_default inserts below reference it, and the _down needs to find
-- it again. category 'crops' is new — plural + lowercase, matching the existing
-- 'water-features' and 'stumps-and-logs' rather than the singular older ones.
INSERT INTO asset (
    id, name, category, default_state, anchor_x, anchor_y, layer, pack_id, z_index, is_obstacle)
VALUES (
    '019e5f00-c401-7a10-9e00-000000000576',
    'Wheat', 'crops',
    -- Placed plants come down ripe: asset_refresh_default seeds them full, so
    -- default_state matching the ripe stage means what you drop in the editor is
    -- what the engine derives a moment later. Same posture as Blueberry Bush,
    -- whose default_state is 'berries'.
    'growth-6',
    0.5, 0.85, 'objects', 'mana-seed-crops', 10,
    -- Not an obstacle: a farmer has to be able to stand in his own field.
    false);

-- Six 16x32 cells off the single-crop wheat sheet. src_x steps by 16; the first
-- is the seeds cell (32), then the artist's five stages (48..112).
INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
SELECT '019e5f00-c401-7a10-9e00-000000000576', v.state,
       '/tilesets/mana-seed/farming-crops-1/sheets by crop/farming crops (wheat) 16x32.png',
       v.src_x, 0, 16, 32, 1, 0
  FROM (VALUES
        ('growth-1',  32),   -- scattered seed — just reaped and sown
        ('growth-2',  48),
        ('growth-3',  64),
        ('growth-4',  80),
        ('growth-5',  96),
        ('growth-6', 112)    -- ripe; renders whenever the plant has stock
       ) AS v(state, src_x);

-- The tag, not the state name, is what berry_state.go reads. They match here for
-- legibility, but the tag is the contract.
INSERT INTO asset_state_tag (state_id, tag)
SELECT s.id, s.state
  FROM asset_state s
 WHERE s.asset_id = '019e5f00-c401-7a10-9e00-000000000576';

-- ---------------------------------------------------------------------------
-- 2. Placement template
-- ---------------------------------------------------------------------------

-- Every new plant gets this row copied onto it by CreateVillageObject, seeded to
-- available_quantity = max_quantity (seedRefreshesFromDefaults -> normalizeDefaultSupply).
--
-- PERIODIC, not continuous, and that choice is load-bearing: periodic holds the
-- plant at 0 for the whole 120h and then jumps to full, so an unripe plant has no
-- stock and every existing gate already refuses it — the Gather command,
-- ResolveGatherSource, the at-source cue, and the forage move handle. No separate
-- ripeness gate exists or is needed. Continuous would trickle units in and make
-- half-grown wheat harvestable.
--
-- 3 grains per plant over 120h. Capacity is plants x 3 / 5 days; at ~40 plants
-- that is ~24/day against the ~8/day Moses actually sells. Grains-per-plant is
-- the yield knob (one gather is one plant and one tick, so a restock trip should
-- be a handful of calls, not twenty); plant count is a look knob.
INSERT INTO asset_refresh_default (
    asset_id, attribute, amount, available_quantity, max_quantity,
    refresh_mode, refresh_period_hours, gather_item)
VALUES (
    '019e5f00-c401-7a10-9e00-000000000576', NULL, 0, 3, 3, 'periodic', 120, 'wheat');

-- ---------------------------------------------------------------------------
-- 3. Wheat: produced -> foraged
-- ---------------------------------------------------------------------------

-- THE RESTOCK ENTRY IS THE WHOLE SWITCH — item_recipe is deliberately untouched.
--
-- StartProductionCycle checks produceEntry(actor, kind) BEFORE makeableRecipe, and
-- every other produce surface (the perception cues, derived demand, the
-- distributor, merchant capital, forge choice) iterates RestockPolicy.ProduceEntries().
-- Moses is the only actor with a produce entry for wheat, so flipping his entry to
-- forage removes wheat from the produce path entirely. The recipe row then sits
-- dormant, which is what we want: wheat's wholesale_price/retail_price live on it
-- and are read by perception/trade_value.go and perception/restock.go for a good
-- that is still actively bought and sold.
--
-- An earlier draft zeroed rate_qty to make wheat "unmakeable". That would have
-- FAILED — item_recipe carries CHECK (rate_qty > 0). It also passed the
-- integration suite, because a schema-only database has no item_recipe rows for
-- the UPDATE to match, so the constraint was never evaluated. Left recorded here
-- so nobody re-derives the idea.
DO $$
DECLARE n int;
BEGIN
    -- Rebuilt element-wise so Moses's carrots entry is untouched. COALESCE because
    -- jsonb_agg over an EMPTY array returns SQL NULL, and jsonb_set with a NULL
    -- replacement yields NULL for the whole params document (code_review).
    UPDATE actor_attribute
       SET params = jsonb_set(params, '{restock}', COALESCE((
               SELECT jsonb_agg(
                          CASE WHEN e->>'item' = 'wheat'
                               THEN jsonb_set(e, '{source}', '"forage"')
                               ELSE e END
                          ORDER BY ord)
                 FROM jsonb_array_elements(params->'restock') WITH ORDINALITY AS t(e, ord)
           ), '[]'::jsonb))
     WHERE actor_id = '019da6ae-3376-73fc-8872-1cbb3ada1c78'  -- Moses James
       AND slug = 'farmer'
       AND jsonb_typeof(params->'restock') = 'array';
    GET DIAGNOSTICS n = ROW_COUNT;
    -- 0 rows is the unseeded case (fresh schema-only DB / integration harness) —
    -- fine. But if Moses exists without his farmer row, fail loud rather than
    -- leave the field unworked.
    IF n = 0 AND EXISTS (SELECT 1 FROM actor WHERE id = '019da6ae-3376-73fc-8872-1cbb3ada1c78') THEN
        RAISE EXCEPTION 'LLM-576: Moses James exists but his farmer actor_attribute row was not found';
    END IF;
    -- Assert the OUTCOME, not just that a row was touched: the UPDATE reports one
    -- affected row even when the array held no wheat entry to flip, which would
    -- silently leave wheat on produce (code_review).
    IF EXISTS (
        SELECT 1 FROM actor_attribute
         WHERE actor_id = '019da6ae-3376-73fc-8872-1cbb3ada1c78' AND slug = 'farmer'
           AND NOT EXISTS (
               SELECT 1 FROM jsonb_array_elements(params->'restock') e
                WHERE e->>'item' = 'wheat' AND e->>'source' = 'forage')
    ) THEN
        RAISE EXCEPTION 'LLM-576: Moses James has no forage restock entry for wheat after the flip';
    END IF;
END $$;

COMMIT;
