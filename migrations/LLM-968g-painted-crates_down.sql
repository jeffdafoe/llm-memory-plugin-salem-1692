-- LLM-968 down: Crate, Crate (Dark), Crate (Light) and the Notice Board back
-- on their Mana Seed sheets at render_scale 2: the crates on the LLM-699
-- angled-crates.png frames (dark 0/40, light 80/120, 40x40), the board on
-- village notice boards 48x64.png (x 192 .. 384).
--
-- This is a reset to those values, not an undo of whatever the up found: the
-- up keeps no record of the values it overwrote.
--
-- Each asset that exists is set, then checked both ways; any difference fails
-- the migration. Convergent.

BEGIN;

-- Hold off any other catalog writer (the editor) between the presence check
-- and the checks below.
LOCK TABLE asset, asset_state, asset_state_tag IN SHARE ROW EXCLUSIVE MODE;

DO $$
DECLARE
    bad integer;
BEGIN
    CREATE TEMP TABLE llm968g_down_asset (asset_id uuid PRIMARY KEY) ON COMMIT DROP;
    INSERT INTO llm968g_down_asset VALUES
        ('7bf7022c-c354-4333-a607-bb42650d2666'),   -- Crate
        ('019e5f00-c401-7a10-9e00-000000690001'),   -- Crate (Dark)
        ('019e5f00-c401-7a10-9e00-000000690002'),   -- Crate (Light)
        ('df06e1c7-3912-4846-b828-6c0435af8c01');   -- Notice Board
    DELETE FROM llm968g_down_asset v WHERE NOT EXISTS (SELECT 1 FROM asset WHERE id = v.asset_id);
    IF NOT EXISTS (SELECT 1 FROM llm968g_down_asset) THEN
        RETURN;
    END IF;

    -- Every state, as LLM-699 and the pack catalog had it.
    CREATE TEMP TABLE llm968g_down_state (asset_id uuid, state text, sheet text, src_x integer, src_y integer,
                                     src_w integer, src_h integer, frame_count integer,
                                     frame_rate double precision) ON COMMIT DROP;
    INSERT INTO llm968g_down_state VALUES
        ('7bf7022c-c354-4333-a607-bb42650d2666', 'default', '/tilesets/mana-seed/village-accessories/angled-crates.png', 0, 0, 40, 40, 1, 0),
        ('7bf7022c-c354-4333-a607-bb42650d2666', 'lid-loose', '/tilesets/mana-seed/village-accessories/angled-crates.png', 40, 0, 40, 40, 1, 0),
        ('019e5f00-c401-7a10-9e00-000000690001', 'default', '/tilesets/mana-seed/village-accessories/angled-crates.png', 0, 0, 40, 40, 1, 0),
        ('019e5f00-c401-7a10-9e00-000000690001', 'lid-loose', '/tilesets/mana-seed/village-accessories/angled-crates.png', 40, 0, 40, 40, 1, 0),
        ('019e5f00-c401-7a10-9e00-000000690002', 'default', '/tilesets/mana-seed/village-accessories/angled-crates.png', 80, 0, 40, 40, 1, 0),
        ('019e5f00-c401-7a10-9e00-000000690002', 'lid-loose', '/tilesets/mana-seed/village-accessories/angled-crates.png', 120, 0, 40, 40, 1, 0),
        ('df06e1c7-3912-4846-b828-6c0435af8c01', 'variant-1', '/tilesets/mana-seed/village-accessories/village notice boards 48x64.png', 192, 0, 48, 64, 1, 0),
        ('df06e1c7-3912-4846-b828-6c0435af8c01', 'variant-2', '/tilesets/mana-seed/village-accessories/village notice boards 48x64.png', 240, 0, 48, 64, 1, 0),
        ('df06e1c7-3912-4846-b828-6c0435af8c01', 'variant-3', '/tilesets/mana-seed/village-accessories/village notice boards 48x64.png', 288, 0, 48, 64, 1, 0),
        ('df06e1c7-3912-4846-b828-6c0435af8c01', 'variant-4', '/tilesets/mana-seed/village-accessories/village notice boards 48x64.png', 336, 0, 48, 64, 1, 0),
        ('df06e1c7-3912-4846-b828-6c0435af8c01', 'variant-5', '/tilesets/mana-seed/village-accessories/village notice boards 48x64.png', 384, 0, 48, 64, 1, 0);
    DELETE FROM llm968g_down_state v WHERE NOT EXISTS (SELECT 1 FROM llm968g_down_asset w WHERE w.asset_id = v.asset_id);

    -- Their tags, unchanged by this migration: the minor works find a
    -- crate's loose-lid state by its minor-form-crate / minor-of-default
    -- tags, and the crier's posting finds a board variant by its
    -- content-capacity-N tag.
    CREATE TEMP TABLE llm968g_down_tag (asset_id uuid, state text, tag text) ON COMMIT DROP;
    INSERT INTO llm968g_down_tag
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
     WHERE EXISTS (SELECT 1 FROM llm968g_down_asset w WHERE w.asset_id = v.a);

    UPDATE asset a SET render_scale = 2 FROM llm968g_down_asset v WHERE a.id = v.asset_id;
    -- Existing states only: the check below fails on a missing one.
    UPDATE asset_state s
       SET sheet = v.sheet, src_x = v.src_x, src_y = v.src_y, src_w = v.src_w, src_h = v.src_h,
           frame_count = v.frame_count, frame_rate = v.frame_rate
      FROM llm968g_down_state v
     WHERE s.asset_id = v.asset_id AND s.state = v.state;

    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968g_down_state
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968g_down_asset v ON v.asset_id = s.asset_id)
        UNION ALL
        (SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s JOIN llm968g_down_asset v ON v.asset_id = s.asset_id
         EXCEPT
         SELECT asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate FROM llm968g_down_state)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968g down: % state rows differ from the pack values', bad;
    END IF;
    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, state, tag FROM llm968g_down_tag
         EXCEPT
         SELECT s.asset_id, s.state::text, t.tag::text
           FROM asset_state s JOIN llm968g_down_asset v ON v.asset_id = s.asset_id JOIN asset_state_tag t ON t.state_id = s.id)
        UNION ALL
        (SELECT s.asset_id, s.state::text, t.tag::text
           FROM asset_state s JOIN llm968g_down_asset v ON v.asset_id = s.asset_id JOIN asset_state_tag t ON t.state_id = s.id
         EXCEPT
         SELECT asset_id, state, tag FROM llm968g_down_tag)
    ) diff;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968g down: % state tags differ from the expected ones', bad;
    END IF;
    SELECT count(*) INTO bad FROM asset a JOIN llm968g_down_asset v ON v.asset_id = a.id WHERE a.render_scale <> 2;
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-968g down: % asset(s) not back at render_scale 2', bad;
    END IF;
END $$;

COMMIT;
