-- LLM-968: painted crates and notice board: Crate, Crate (Dark), Crate
-- (Light) and the Notice Board.
--
-- They drew Mana Seed art (the crates the LLM-699 angled-crates.png, made by
-- recolouring the pack's angled crate; the board village notice boards
-- 48x64.png), whose licence forbids use alongside AI. tools/tree-art/crates.py
-- now paints them: client/assets/trees/crate-dark.png and crate-light.png
-- (shut, lid loose; 80x80 frames) and notice-board.png (variant-1 .. 5;
-- 96x128 frames), at twice the pack art's size and shown at render_scale 1
-- (the same size in the world, twice the detail). Anchors do not change (the
-- crates 0.5/0.88 on the LLM-699 ground point, the board 0.5/0.85), so every
-- placement keeps its place.
--
-- Crate and Crate (Dark) share the dark sheet, as they shared the LLM-699
-- dark frames; Crate (Light) takes the light one. Each board variant shows as
-- many notices as its content-capacity-N tag holds (variant-1 five,
-- variant-2 three, variant-3 two, variant-4 none, variant-5 four), so the
-- notices drawn are the ones the crier posted.
--
-- No state, tag or placement changes; the tags are checked: the minor works
-- find a crate's loose lid by minor-form-crate / minor-of-default, the crier
-- a board variant by content-capacity-N, notice-board and rotatable.
--
-- Each asset that exists is set, then its states and tags are checked both
-- ways against these values; any difference fails the migration. A missing
-- state is not recreated (it would come back without its tags); the check
-- fails on it. An asset not in this database takes no part (Crate (Dark) and
-- Crate (Light) come from LLM-690; Crate and the Notice Board are live-only).
--
-- ENGINE-OWNED data (the catalog), read at boot; deploy.sh runs stop ->
-- migrate -> start. Convergent: a rerun ends in the same place and repairs a
-- hand edit of a rect (the up writes the authored values over whatever is
-- there; it does not keep them to restore).

BEGIN;

-- Hold off any other catalog writer (the editor) between the presence check
-- and the checks below.
LOCK TABLE asset, asset_state, asset_state_tag IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    bad integer;
BEGIN
    CREATE TEMP TABLE llm968g_asset (asset_id uuid PRIMARY KEY) ON COMMIT DROP;
    INSERT INTO llm968g_asset VALUES
        ('7bf7022c-c354-4333-a607-bb42650d2666'),   -- Crate
        ('019e5f00-c401-7a10-9e00-000000690001'),   -- Crate (Dark)
        ('019e5f00-c401-7a10-9e00-000000690002'),   -- Crate (Light)
        ('df06e1c7-3912-4846-b828-6c0435af8c01');   -- Notice Board
    DELETE FROM llm968g_asset v WHERE NOT EXISTS (SELECT 1 FROM asset WHERE id = v.asset_id);
    IF NOT EXISTS (SELECT 1 FROM llm968g_asset) THEN
        RETURN;
    END IF;

    -- Every state, as authored.
    CREATE TEMP TABLE llm968g_state (asset_id uuid, state text, sheet text, src_x integer, src_y integer,
                                     src_w integer, src_h integer, frame_count integer,
                                     frame_rate double precision) ON COMMIT DROP;
    INSERT INTO llm968g_state VALUES
        ('7bf7022c-c354-4333-a607-bb42650d2666', 'default', 'res://assets/trees/crate-dark.png', 0, 0, 80, 80, 1, 0),
        ('7bf7022c-c354-4333-a607-bb42650d2666', 'lid-loose', 'res://assets/trees/crate-dark.png', 80, 0, 80, 80, 1, 0),
        ('019e5f00-c401-7a10-9e00-000000690001', 'default', 'res://assets/trees/crate-dark.png', 0, 0, 80, 80, 1, 0),
        ('019e5f00-c401-7a10-9e00-000000690001', 'lid-loose', 'res://assets/trees/crate-dark.png', 80, 0, 80, 80, 1, 0),
        ('019e5f00-c401-7a10-9e00-000000690002', 'default', 'res://assets/trees/crate-light.png', 0, 0, 80, 80, 1, 0),
        ('019e5f00-c401-7a10-9e00-000000690002', 'lid-loose', 'res://assets/trees/crate-light.png', 80, 0, 80, 80, 1, 0),
        ('df06e1c7-3912-4846-b828-6c0435af8c01', 'variant-1', 'res://assets/trees/notice-board.png', 0, 0, 96, 128, 1, 0),
        ('df06e1c7-3912-4846-b828-6c0435af8c01', 'variant-2', 'res://assets/trees/notice-board.png', 96, 0, 96, 128, 1, 0),
        ('df06e1c7-3912-4846-b828-6c0435af8c01', 'variant-3', 'res://assets/trees/notice-board.png', 192, 0, 96, 128, 1, 0),
        ('df06e1c7-3912-4846-b828-6c0435af8c01', 'variant-4', 'res://assets/trees/notice-board.png', 288, 0, 96, 128, 1, 0),
        ('df06e1c7-3912-4846-b828-6c0435af8c01', 'variant-5', 'res://assets/trees/notice-board.png', 384, 0, 96, 128, 1, 0);
    DELETE FROM llm968g_state v WHERE NOT EXISTS (SELECT 1 FROM llm968g_asset w WHERE w.asset_id = v.asset_id);

    -- Their tags, unchanged by this migration: the minor works find a
    -- crate's loose-lid state by its minor-form-crate / minor-of-default
    -- tags, and the crier's posting finds a board variant by its
    -- content-capacity-N tag.
    CREATE TEMP TABLE llm968g_tag (asset_id uuid, state text, tag text) ON COMMIT DROP;
    INSERT INTO llm968g_tag
    SELECT a, s, t FROM (VALUES
        ('7bf7022c-c354-4333-a607-bb42650d2666'::uuid, 'lid-loose', 'minor-form-crate'),
        ('7bf7022c-c354-4333-a607-bb42650d2666'::uuid, 'lid-loose', 'minor-of-default'),
        ('019e5f00-c401-7a10-9e00-000000690001'::uuid, 'lid-loose', 'minor-form-crate'),
        ('019e5f00-c401-7a10-9e00-000000690001'::uuid, 'lid-loose', 'minor-of-default'),
        ('019e5f00-c401-7a10-9e00-000000690002'::uuid, 'lid-loose', 'minor-form-crate'),
        ('019e5f00-c401-7a10-9e00-000000690002'::uuid, 'lid-loose', 'minor-of-default'),
        ('df06e1c7-3912-4846-b828-6c0435af8c01'::uuid, 'variant-1', 'content-capacity-5'),
        ('df06e1c7-3912-4846-b828-6c0435af8c01'::uuid, 'variant-1', 'notice-board'),
        ('df06e1c7-3912-4846-b828-6c0435af8c01'::uuid, 'variant-1', 'rotatable'),
        ('df06e1c7-3912-4846-b828-6c0435af8c01'::uuid, 'variant-2', 'content-capacity-3'),
        ('df06e1c7-3912-4846-b828-6c0435af8c01'::uuid, 'variant-2', 'notice-board'),
        ('df06e1c7-3912-4846-b828-6c0435af8c01'::uuid, 'variant-2', 'rotatable'),
        ('df06e1c7-3912-4846-b828-6c0435af8c01'::uuid, 'variant-3', 'content-capacity-2'),
        ('df06e1c7-3912-4846-b828-6c0435af8c01'::uuid, 'variant-3', 'notice-board'),
        ('df06e1c7-3912-4846-b828-6c0435af8c01'::uuid, 'variant-3', 'rotatable'),
        ('df06e1c7-3912-4846-b828-6c0435af8c01'::uuid, 'variant-4', 'notice-board'),
        ('df06e1c7-3912-4846-b828-6c0435af8c01'::uuid, 'variant-4', 'rotatable'),
        ('df06e1c7-3912-4846-b828-6c0435af8c01'::uuid, 'variant-5', 'content-capacity-4'),
        ('df06e1c7-3912-4846-b828-6c0435af8c01'::uuid, 'variant-5', 'notice-board'),
        ('df06e1c7-3912-4846-b828-6c0435af8c01'::uuid, 'variant-5', 'rotatable')) v(a, s, t)
     WHERE EXISTS (SELECT 1 FROM llm968g_asset w WHERE w.asset_id = v.a);

    UPDATE asset a SET render_scale = 1 FROM llm968g_asset v WHERE a.id = v.asset_id;
    -- Existing states only: the check below fails on a missing one.
    UPDATE asset_state s
       SET sheet = v.sheet, src_x = v.src_x, src_y = v.src_y, src_w = v.src_w, src_h = v.src_h,
           frame_count = v.frame_count, frame_rate = v.frame_rate
      FROM llm968g_state v
     WHERE s.asset_id = v.asset_id AND s.state = v.state;

    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968g_state
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968g_asset v ON v.asset_id = s.asset_id)
        UNION ALL
        (SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968g_asset v ON v.asset_id = s.asset_id
         EXCEPT
         SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968g_state)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968g: % state rows differ from the authored values', bad;
    END IF;
    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, tag FROM llm968g_tag
         EXCEPT
         SELECT s.asset_id, s.state::text, t.tag::text
           FROM asset_state s JOIN llm968g_asset v ON v.asset_id = s.asset_id JOIN asset_state_tag t ON t.state_id = s.id)
        UNION ALL
        (SELECT s.asset_id, s.state::text, t.tag::text
           FROM asset_state s JOIN llm968g_asset v ON v.asset_id = s.asset_id JOIN asset_state_tag t ON t.state_id = s.id
         EXCEPT
         SELECT asset_id, state, tag FROM llm968g_tag)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968g: % state tags differ from the expected ones', bad;
    END IF;
    SELECT count(*) INTO bad FROM asset a JOIN llm968g_asset v ON v.asset_id = a.id WHERE a.render_scale <> 1;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968g: % asset(s) not at render_scale 1', bad;
    END IF;
END $$;

COMMIT;
