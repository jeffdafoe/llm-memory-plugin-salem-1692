-- LLM-624 down: the apple orchard goes back to the four-stage crop model.
--
-- This restores LLM-623's authoring EXACTLY, including `growth-1` at src
-- (240, 64) — the canopy-only layer whose missing trunk is the whole reason the
-- _up exists. That is deliberate: a down migration's job is to return the
-- database to the state its _up found, not to ship a better version of the bug.
-- Rolling back reinstates the floating-canopy render at the next regrow
-- rollover.
--
-- display_name is deliberately NOT reverted to empty. The _up set it so a
-- replayed chain reproduces working state; putting the wedge back would break
-- gathering for no benefit, and it was never this migration's to own — LLM-623
-- created those rows and the live fix predates this file.
--
-- The engine must be stopped, same as for the _up.

BEGIN;

DO $$
DECLARE
    apple_id CONSTANT uuid := '019e5f00-c401-7a10-9e00-000000000623';
    sheet    CONSTANT text := '/tilesets/mana-seed/growable-fruit-trees/fruit trees (apple, red) 48x64.png';
    bush_states int;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM asset WHERE id = apple_id) THEN
        RETURN;  -- nothing of ours to roll back
    END IF;

    -- Only revert states this migration created. Anything else means the asset
    -- has been re-authored since, which is not ours to overwrite.
    SELECT count(*) INTO bush_states
      FROM asset_state s
     WHERE s.asset_id = apple_id AND s.state IN ('berries', 'bare');
    IF bush_states <> 2 THEN
        RAISE EXCEPTION
            'LLM-624 down: expected the 2 bush states this migration created, found % — the Apple Tree asset has been re-authored since', bush_states;
    END IF;

    DELETE FROM asset_state WHERE asset_id = apple_id;

    INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
    SELECT apple_id, v.state, sheet, v.src_x, v.src_y, 48, 64, 1, 0
      FROM (VALUES
            ('growth-1', 240,  0),
            ('growth-2', 144,  0),
            ('growth-3', 144, 64),
            ('growth-4', 192, 64)
           ) AS v(state, src_x, src_y);

    INSERT INTO asset_state_tag (state_id, tag)
    SELECT s.id, s.state
      FROM asset_state s
     WHERE s.asset_id = apple_id;

    UPDATE asset SET default_state = 'growth-4' WHERE id = apple_id;
END $$;

DO $$
DECLARE
    apple_id CONSTANT uuid := '019e5f00-c401-7a10-9e00-000000000623';
    trees int;
BEGIN
    SELECT count(*) INTO trees FROM village_object WHERE asset_id = apple_id;
    IF trees = 0 THEN
        RETURN;
    END IF;

    -- Back to the ripe stage, mirroring what LLM-623 set. A tree currently
    -- empty would be re-dated by refreshObjectBerryState on the next sweep.
    UPDATE village_object
       SET current_state = 'growth-4'
     WHERE asset_id = apple_id;
END $$;

COMMIT;
