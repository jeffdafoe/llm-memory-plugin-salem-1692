-- LLM-624: the apple orchard renders binary like a berry bush, not as a staged
-- crop. Migration only, no engine code.
--
-- WHY THE STAGED MODEL IS WRONG HERE, beyond taste. LLM-623 authored `growth-1`
-- as src (240, 64). That frame is NOT a growth stage — it is the CANOPY-ONLY
-- layer of a two-part sprite, whose matching TRUNK layer is src (288, 64).
-- Measured opaque-pixel extents in the green row:
--
--     frame            opaque rows   bottom 8 rows (the trunk)
--     144 full tree    1..63         13,13,14,15,11,11,4,3
--     192 fruited      1..63         13,13,14,15,11,11,4,3
--     240              1..55         all zero — no trunk at all
--     288 stump        47..63        13,13,14,15,11,11,4,3
--
-- 288's profile is identical to the bottom of the full tree, and 240 is the full
-- tree with exactly those rows removed; composited they reconstruct 144. The
-- artist splits them for depth sorting — trunk under the character, canopy over
-- it — the same idiom as `summer forest, tree wall (canopy only) 128x128.png`.
--
-- Nothing would have errored. Every tree sits ripe for the first 168h, so it
-- looks correct; at the first regrow rollover the whole orchard drops to
-- growth-1 and renders as canopies floating with no trunks.
--
-- HOW THE MODEL IS SELECTED. refreshObjectBerryState dispatches on
-- `len(growthStates(asset)) > 0` and otherwise falls through to the
-- berries/bare branch. So it is the ABSENCE of every growth tag that flips the
-- model — one surviving `growth-N` tag anywhere on this asset keeps the crop
-- path live and the bush states unreachable. Deleting the asset_state rows
-- cascades asset_state_tag, which is what makes the switch total.
--
-- Behaviour is unchanged: same yield-only 3 units / 168h row, same owner gate.
-- Only the visual model changes — the orchard now snaps fruited<->picked on
-- stock exactly as the raspberry and blueberry bushes do.
--
-- asset / asset_state / asset_state_tag are REFERENCE data (load-only).
-- village_object IS checkpoint-written, so the state and display_name updates
-- need the engine stopped — deploy.sh already does stop -> migrate -> start.

BEGIN;

-- ---------------------------------------------------------------------------
-- 1. Four growth stages -> two bush states
-- ---------------------------------------------------------------------------

DO $$
DECLARE
    apple_id CONSTANT uuid := '019e5f00-c401-7a10-9e00-000000000623';
    sheet    CONSTANT text := '/tilesets/mana-seed/growable-fruit-trees/fruit trees (apple, red) 48x64.png';
    growth_states int;
    leftover_growth_tags int;
    bush_states int;
BEGIN
    -- Fresh schema-only DB has the asset (LLM-623 is reference data and always
    -- lands), so unlike the village parts below there is no empty-install case
    -- here — the asset must exist.
    IF NOT EXISTS (SELECT 1 FROM asset WHERE id = apple_id) THEN
        RAISE EXCEPTION 'LLM-624: the Apple Tree asset (%) was not found — LLM-623 must be applied first', apple_id;
    END IF;

    -- Assert the shape before mutating: exactly the four growth states LLM-623
    -- created. Anything else means the asset has been re-authored since, which
    -- is state this migration must not silently overwrite.
    SELECT count(*) INTO growth_states
      FROM asset_state s
     WHERE s.asset_id = apple_id AND s.state LIKE 'growth-%';
    IF growth_states <> 4 THEN
        RAISE EXCEPTION
            'LLM-624: expected the 4 growth states LLM-623 authored, found % — the Apple Tree asset has been re-authored since', growth_states;
    END IF;

    -- asset_state_tag rows cascade with their states.
    DELETE FROM asset_state WHERE asset_id = apple_id;

    -- Both frames are COMPLETE trees, trunk included — src_y 64 is the green
    -- row, columns 4 and 5 one-referenced. `bare` is a leafy tree with no fruit
    -- rather than bare branches: the sheet's genuinely bare-branch rows read as
    -- winter or dead, not as harvested.
    INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
    SELECT apple_id, v.state, sheet, v.src_x, 64, 48, 64, 1, 0
      FROM (VALUES
            ('bare',    144),   -- picked out
            ('berries', 192)    -- carrying fruit
           ) AS v(state, src_x);

    -- The TAG is the contract read by StateForTag, not the state name (LLM-58b
    -- shipped two-state rows without their tags and the bush never flipped).
    INSERT INTO asset_state_tag (state_id, tag)
    SELECT s.id, s.state
      FROM asset_state s
     WHERE s.asset_id = apple_id;

    -- The dispatch is `any growth tag at all -> crop path`, so prove none
    -- survived rather than trusting the cascade.
    SELECT count(*) INTO leftover_growth_tags
      FROM asset_state_tag t
      JOIN asset_state s ON s.id = t.state_id
     WHERE s.asset_id = apple_id AND t.tag LIKE 'growth-%';
    IF leftover_growth_tags <> 0 THEN
        RAISE EXCEPTION
            'LLM-624: % growth tag(s) survived — refreshObjectBerryState would still take the crop path and never reach the bush states', leftover_growth_tags;
    END IF;

    -- Both tags must exist or the bush branch returns "not state-tracked" and
    -- the orchard stops flipping altogether.
    SELECT count(*) INTO bush_states
      FROM asset_state_tag t
      JOIN asset_state s ON s.id = t.state_id
     WHERE s.asset_id = apple_id AND t.tag IN ('berries', 'bare');
    IF bush_states <> 2 THEN
        RAISE EXCEPTION 'LLM-624: expected both berries and bare tags, found %', bush_states;
    END IF;

    -- A placed tree drops in ripe (asset_refresh_default seeds it full), so the
    -- default state is the fruited one.
    UPDATE asset SET default_state = 'berries' WHERE id = apple_id;
END $$;

-- ---------------------------------------------------------------------------
-- 2. The placed trees follow
-- ---------------------------------------------------------------------------

DO $$
DECLARE
    apple_id CONSTANT uuid := '019e5f00-c401-7a10-9e00-000000000623';
    trees int;
    restated int;
BEGIN
    SELECT count(*) INTO trees FROM village_object WHERE asset_id = apple_id;
    IF trees = 0 THEN
        RETURN;  -- schema-only DB: reference data only, no village
    END IF;

    -- current_state is recomputed by refreshObjectBerryState on its next sweep
    -- regardless; setting it here means the first frame drawn after boot is
    -- already right, and that a tree whose stock is currently zero is not left
    -- holding a state name that no longer exists on the asset.
    UPDATE village_object
       SET current_state = CASE
               WHEN EXISTS (
                   SELECT 1 FROM object_refresh r
                    WHERE r.object_id = village_object.id
                      AND r.gather_item = 'apples'
                      AND coalesce(r.available_quantity, 0) > 0
               ) THEN 'berries'
               ELSE 'bare'
           END
     WHERE asset_id = apple_id;

    GET DIAGNOSTICS restated = ROW_COUNT;
    IF restated <> trees THEN
        RAISE EXCEPTION 'LLM-624: restated % of % apple trees', restated, trees;
    END IF;

    IF EXISTS (SELECT 1 FROM village_object
                WHERE asset_id = apple_id AND current_state NOT IN ('berries', 'bare')) THEN
        RAISE EXCEPTION 'LLM-624: an apple tree is left in a state the asset no longer defines';
    END IF;
END $$;

-- ---------------------------------------------------------------------------
-- 3. Every gatherable object must be NAMED
-- ---------------------------------------------------------------------------

-- LLM-623 converted the maples in place and inherited their empty display_name.
-- All three at-source resolution paths skip an unnamed object —
-- ResolveLoiteringObject (structure_anchors.go:377), the walked-to bypass
-- (gather_target.go:215) and the widening loop (gather_target.go:146) — while
-- the forage steer in perception/forage.go has no such check. The two halves
-- disagreed: Prudence was steered to the orchard, arrived, found no gather verb
-- because nothing resolved at the source, and re-walked for an hour.
--
-- Already fixed live through the umbilical and persisted by checkpoint, so this
-- is a no-op on production. It exists so a replayed migration chain reproduces
-- the working state rather than the wedge.
UPDATE village_object
   SET display_name = 'Apple Tree'
 WHERE asset_id = '019e5f00-c401-7a10-9e00-000000000623'
   AND coalesce(btrim(display_name), '') = '';

COMMIT;
