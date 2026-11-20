-- LLM-967: painted rocks: Pebble, Tiny Rock, Medium Rocks, Large Rocks,
-- Boulder Pair, Flat Rock, Small Rock and River Rock.
--
-- They drew Mana Seed forest art, whose licence forbids use alongside AI.
-- tools/tree-art/rocks.py now paints them: client/assets/trees/pebble.png,
-- tiny-rock.png, medium-rocks.png, large-rocks.png, boulder-pair.png,
-- flat-rock.png, small-rock.png, river-rock.png. This migration sorts after
-- LLM-967h-water-edge (salem migrations run in filename order).
--
-- Sheet layout (rocks.py): one row per look, top to bottom: summer, autumn,
-- winter, winter-snowy. The River Rock's rows are 12 frames played at 6 fps
-- (its ripples spread round it); every other rock is one still frame. Frames
-- are 16x16, Small Rock's and River Rock's 32x32, as the pack art's were, at
-- the same anchor, so every placed rock keeps its place.
--
-- No state is added or removed, and no tag changes: each existing state points
-- at its look's row. The season states map: default -> summer, autumn ->
-- autumn, winter-clean -> winter, winter-snowy -> winter-snowy; the 32x32
-- rocks' winter-leaves shows the winter row and their winter-snowy-top the
-- winter-snowy row. No rock is copied and no placement moves.
--
-- For each rock that exists (a fresh database has no catalog; then it does
-- nothing), its states point at its sheet in that layout, and then every rock
-- must have exactly those states with exactly those values, or the migration
-- fails.
--
-- ENGINE-OWNED data (the catalog), read at boot; deploy.sh runs stop ->
-- migrate -> start. Convergent: rerunning ends in the same place, and a rerun
-- repairs a hand edit of these rows.

BEGIN;

DO $$
DECLARE
    bad integer;
BEGIN
    -- Every rock: its sheet, frame and play rate, and the layout of its rows.
    CREATE TEMP TABLE llm967i_asset (asset_id uuid PRIMARY KEY, sheet text NOT NULL, frame_size integer NOT NULL,
                                     frame_count integer NOT NULL, frame_rate double precision NOT NULL,
                                     layout text NOT NULL) ON COMMIT DROP;
    INSERT INTO llm967i_asset VALUES
        ('055d4cb0-99d1-44b0-bbe2-e968dc868cc2', 'res://assets/trees/pebble.png',       16,  1, 0, 'seasons'),      -- Pebble
        ('24ca2da0-4dd4-474a-a471-7ce818742818', 'res://assets/trees/tiny-rock.png',    16,  1, 0, 'seasons'),      -- Tiny Rock
        ('adb0dbe7-5406-479c-a5c7-54a30fd958de', 'res://assets/trees/medium-rocks.png', 16,  1, 0, 'seasons'),      -- Medium Rocks
        ('f12a835e-74bd-44eb-9788-c9627e896e4f', 'res://assets/trees/large-rocks.png',  16,  1, 0, 'seasons'),      -- Large Rocks
        ('3552f47f-1ebb-420c-8871-3578f1ee501a', 'res://assets/trees/boulder-pair.png', 16,  1, 0, 'seasons'),      -- Boulder Pair
        ('004a194c-b409-45ec-9d89-595d10372008', 'res://assets/trees/flat-rock.png',    16,  1, 0, 'seasons'),      -- Flat Rock
        ('dcec1fc9-e629-4649-974c-3c298cd7e39b', 'res://assets/trees/small-rock.png',   32,  1, 0, 'seasons-six'),  -- Small Rock
        ('87e8703b-2be9-4212-a06a-3372d7f9dfcb', 'res://assets/trees/river-rock.png',   32, 12, 6, 'seasons-six');  -- River Rock
    -- A rock that is not in this database takes no part.
    DELETE FROM llm967i_asset v WHERE NOT EXISTS (SELECT 1 FROM asset WHERE id = v.asset_id);
    IF NOT EXISTS (SELECT 1 FROM llm967i_asset) THEN
        RETURN;
    END IF;

    -- Each layout's states and the sheet row each one shows.
    CREATE TEMP TABLE llm967i_look (layout text NOT NULL, state text NOT NULL, row_index integer NOT NULL,
                                    PRIMARY KEY (layout, state)) ON COMMIT DROP;
    INSERT INTO llm967i_look VALUES
        ('seasons',     'default',          0),
        ('seasons',     'autumn',           1),
        ('seasons',     'winter-clean',     2),
        ('seasons',     'winter-snowy',     3),
        ('seasons-six', 'default',          0),
        ('seasons-six', 'autumn',           1),
        ('seasons-six', 'winter-clean',     2),
        ('seasons-six', 'winter-leaves',    2),
        ('seasons-six', 'winter-snowy-top', 3),
        ('seasons-six', 'winter-snowy',     3);

    -- Every state of every rock, as authored.
    CREATE TEMP TABLE llm967i_state ON COMMIT DROP AS
    SELECT v.asset_id, l.state, v.sheet, 0 AS src_x, l.row_index * v.frame_size AS src_y, v.frame_size AS src_w,
           v.frame_size AS src_h, v.frame_count, v.frame_rate
      FROM llm967i_asset v JOIN llm967i_look l ON l.layout = v.layout;

    INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
    SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate
      FROM llm967i_state
    ON CONFLICT (asset_id, state) DO UPDATE
       SET sheet = EXCLUDED.sheet, src_x = EXCLUDED.src_x, src_y = EXCLUDED.src_y, src_w = EXCLUDED.src_w,
           src_h = EXCLUDED.src_h, frame_count = EXCLUDED.frame_count, frame_rate = EXCLUDED.frame_rate;

    -- Every rock has exactly its states, with exactly these values.
    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm967i_state
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm967i_asset v ON v.asset_id = s.asset_id)
        UNION ALL
        (SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm967i_asset v ON v.asset_id = s.asset_id
         EXCEPT
         SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm967i_state)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967i: % rock state rows differ from the authored values', bad;
    END IF;
END $$;

COMMIT;
