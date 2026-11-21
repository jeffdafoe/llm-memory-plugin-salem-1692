-- LLM-967: painted stumps and logs: Big Stump, Fallen Log, Fallen Tree 2,
-- Tree Stump, Fallen Tree, Small Stump, Stump (Axe) and Camp Stump.
--
-- They drew Mana Seed forest, village-accessories and travelers-camp art,
-- whose licence forbids use alongside AI. tools/tree-art/stumps.py now paints
-- them: client/assets/trees/big-stump.png, fallen-log.png, birch-log.png
-- (Fallen Tree 2), tree-stump.png, broken-trunk.png (Fallen Tree),
-- small-stump.png, axe-stump.png, camp-stump.png. This migration sorts after
-- LLM-967i-rocks (salem migrations run in filename order).
--
-- Sheet layout (stumps.py): the five forest pieces have one row per look, top
-- to bottom: summer, autumn, winter, winter-snowy; Small Stump, Stump (Axe)
-- and Camp Stump have their one look. Every piece is one still frame. Frames
-- are the pack art's sizes (48x32, 32x32, Camp Stump 16x16) at the same
-- anchor, so every placed piece keeps its place, and Fallen Log still fills
-- its 3x2 obstacle footprint.
--
-- No state is added or removed, and no tag changes: each existing state points
-- at its look's row. The season states map: default -> summer, autumn ->
-- autumn, winter-clean and winter-leaves -> winter, winter-snowy-top and
-- winter-snowy -> winter-snowy; a one-look piece's default is its row 0.
-- Nothing is copied and no placement moves.
--
-- For each piece that exists (a fresh database has no catalog; then it does
-- nothing), its states point at its sheet in that layout, and then every
-- piece must have exactly those states with exactly those values, or the
-- migration fails.
--
-- ENGINE-OWNED data (the catalog), read at boot; deploy.sh runs stop ->
-- migrate -> start. Convergent: rerunning ends in the same place, and a rerun
-- repairs a hand edit of these rows.

BEGIN;

DO $$
DECLARE
    bad integer;
BEGIN
    -- Every piece: its sheet, frame, and the layout of its rows.
    CREATE TEMP TABLE llm967j_asset (asset_id uuid PRIMARY KEY, sheet text NOT NULL, frame_w integer NOT NULL,
                                     frame_h integer NOT NULL, layout text NOT NULL) ON COMMIT DROP;
    INSERT INTO llm967j_asset VALUES
        ('c1120ffa-8bef-46e3-b95b-626082a13dab', 'res://assets/trees/big-stump.png',    48, 32, 'seasons-six'),  -- Big Stump
        ('1d0accaa-dc6b-4ab1-8d3d-ad3076b5efd5', 'res://assets/trees/fallen-log.png',   48, 32, 'seasons-six'),  -- Fallen Log
        ('6e894192-421d-45a0-b67c-d34d8f0f5885', 'res://assets/trees/birch-log.png',    48, 32, 'seasons-six'),  -- Fallen Tree 2
        ('f363320f-a5f0-43d2-87f5-47f2a8a370fe', 'res://assets/trees/tree-stump.png',   32, 32, 'seasons-six'),  -- Tree Stump
        ('cd7f06e8-3536-4e72-bae9-24e79230c925', 'res://assets/trees/broken-trunk.png', 32, 32, 'seasons-six'),  -- Fallen Tree
        ('1e7905d7-3f3f-490b-8065-ed1924834415', 'res://assets/trees/small-stump.png',  32, 32, 'one'),          -- Small Stump
        ('03cbbdab-c50d-432b-a3f7-9e642f3de290', 'res://assets/trees/axe-stump.png',    32, 32, 'one'),          -- Stump (Axe)
        ('2c2a423d-afea-45e6-a8bb-f26e0ef80e1e', 'res://assets/trees/camp-stump.png',   16, 16, 'one');          -- Camp Stump
    -- A piece that is not in this database takes no part.
    DELETE FROM llm967j_asset v WHERE NOT EXISTS (SELECT 1 FROM asset WHERE id = v.asset_id);
    IF NOT EXISTS (SELECT 1 FROM llm967j_asset) THEN
        RETURN;
    END IF;

    -- Each layout's states and the sheet row each one shows.
    CREATE TEMP TABLE llm967j_look (layout text NOT NULL, state text NOT NULL, row_index integer NOT NULL,
                                    PRIMARY KEY (layout, state)) ON COMMIT DROP;
    INSERT INTO llm967j_look VALUES
        ('seasons-six', 'default',          0),
        ('seasons-six', 'autumn',           1),
        ('seasons-six', 'winter-clean',     2),
        ('seasons-six', 'winter-leaves',    2),
        ('seasons-six', 'winter-snowy-top', 3),
        ('seasons-six', 'winter-snowy',     3),
        ('one',         'default',          0);

    -- Every state of every piece, as authored: one still frame each.
    CREATE TEMP TABLE llm967j_state ON COMMIT DROP AS
    SELECT v.asset_id, l.state, v.sheet, 0 AS src_x, l.row_index * v.frame_h AS src_y, v.frame_w AS src_w,
           v.frame_h AS src_h, 1 AS frame_count, 0::double precision AS frame_rate
      FROM llm967j_asset v JOIN llm967j_look l ON l.layout = v.layout;

    INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
    SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate
      FROM llm967j_state
    ON CONFLICT (asset_id, state) DO UPDATE
       SET sheet = EXCLUDED.sheet, src_x = EXCLUDED.src_x, src_y = EXCLUDED.src_y, src_w = EXCLUDED.src_w,
           src_h = EXCLUDED.src_h, frame_count = EXCLUDED.frame_count, frame_rate = EXCLUDED.frame_rate;

    -- Every piece has exactly its states, with exactly these values.
    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm967j_state
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm967j_asset v ON v.asset_id = s.asset_id)
        UNION ALL
        (SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm967j_asset v ON v.asset_id = s.asset_id
         EXCEPT
         SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm967j_state)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967j: % stump and log state rows differ from the authored values', bad;
    END IF;
END $$;

COMMIT;
