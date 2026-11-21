-- LLM-967k down: Mana Seed's School of Fish again, in all five colourways.
-- Shiners, Perch and Brook Trout get their old names, source files and pack
-- rects back; School of Fish (brown) and (sage), retired by the up, come back
-- as LLM-601 made them (their state rows get new ids; nothing pointed at the
-- old ones, which had no tags or lights).

BEGIN;

DO $$
DECLARE
    sheets CONSTANT text := '/tilesets/mana-seed/fishing-gear-2/';
    bad integer;
BEGIN
    -- Every colourway as it was before LLM-967k.
    CREATE TEMP TABLE llm967k_down (asset_id uuid PRIMARY KEY, name text NOT NULL, source_file text NOT NULL,
                                    sheet text NOT NULL) ON COMMIT DROP;
    INSERT INTO llm967k_down VALUES
        ('019e5f00-c401-7a10-9e00-000000600001', 'School of Fish (cyan)',  'school of fish, desert v1 32x32.png',
         sheets || 'school-of-fish-desert-v1.png'),
        ('019e5f00-c401-7a10-9e00-000000600002', 'School of Fish (green)', 'school of fish, muddy v1 32x32.png',
         sheets || 'school-of-fish-muddy-v1.png'),
        ('019e5f00-c401-7a10-9e00-000000601001', 'School of Fish (blue)',  'school of fish, muddy v2 32x32.png',
         sheets || 'school-of-fish-muddy-v2.png'),
        ('019e5f00-c401-7a10-9e00-000000601002', 'School of Fish (brown)', 'school of fish, rainforest v3 32x32.png',
         sheets || 'school-of-fish-rainforest-v3.png'),
        ('019e5f00-c401-7a10-9e00-000000601003', 'School of Fish (sage)',  'school of fish, desert v2 32x32.png',
         sheets || 'school-of-fish-desert-v2.png');

    -- The retired two come back only where LLM-601's set is here (its blue,
    -- kept by the up, stands for it), made when the blue was: LLM-601 made
    -- all three together. A database without the fish takes no part.
    IF EXISTS (SELECT 1 FROM asset WHERE id = '019e5f00-c401-7a10-9e00-000000601001') THEN
        INSERT INTO asset (id, name, category, default_state, anchor_x, anchor_y, layer, created_at, pack_id,
                           source_file, z_index, is_obstacle, render_scale)
        SELECT d.asset_id, d.name, 'water-features', 'default', 0.5, 0.5, 'objects',
               (SELECT created_at FROM asset WHERE id = '019e5f00-c401-7a10-9e00-000000601001'),
               'mana-seed-fishing', d.source_file, 1, false, 2.0
          FROM llm967k_down d
         WHERE d.asset_id IN ('019e5f00-c401-7a10-9e00-000000601002', '019e5f00-c401-7a10-9e00-000000601003')
        ON CONFLICT (id) DO NOTHING;
    END IF;
    DELETE FROM llm967k_down d WHERE NOT EXISTS (SELECT 1 FROM asset WHERE id = d.asset_id);

    UPDATE asset a
       SET name = d.name, source_file = d.source_file
      FROM llm967k_down d
     WHERE a.id = d.asset_id;

    INSERT INTO asset_state (asset_id, state, sheet, src_x, src_y, src_w, src_h, frame_count, frame_rate)
    SELECT asset_id, 'default', sheet, 0, 0, 32, 32, 4, 3
      FROM llm967k_down
    ON CONFLICT (asset_id, state) DO UPDATE
       SET sheet = EXCLUDED.sheet, src_x = EXCLUDED.src_x, src_y = EXCLUDED.src_y, src_w = EXCLUDED.src_w,
           src_h = EXCLUDED.src_h, frame_count = EXCLUDED.frame_count, frame_rate = EXCLUDED.frame_rate;

    -- Every colourway that is here has exactly its one pack state, its name
    -- and source file, and the asset row LLM-600/601 made.
    SELECT count(*) INTO bad FROM (
        (SELECT asset_id, 'default'::text AS state, sheet, 0 AS src_x, 0 AS src_y, 32 AS src_w, 32 AS src_h,
                4 AS frame_count, 3::double precision AS frame_rate
           FROM llm967k_down
         EXCEPT
         SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s WHERE s.asset_id IN (SELECT asset_id FROM llm967k_down))
        UNION ALL
        (SELECT s.asset_id, s.state, s.sheet, s.src_x, s.src_y, s.src_w, s.src_h, s.frame_count, s.frame_rate
           FROM asset_state s WHERE s.asset_id IN (SELECT asset_id FROM llm967k_down)
         EXCEPT
         SELECT asset_id, 'default', sheet, 0, 0, 32, 32, 4, 3::double precision
           FROM llm967k_down)
    ) diff;
    SELECT bad + count(*) INTO bad
      FROM asset a JOIN llm967k_down d ON d.asset_id = a.id
     WHERE (a.name, a.source_file, a.category, a.default_state, a.anchor_x, a.anchor_y, a.layer,
            a.pack_id, a.z_index, a.is_obstacle, a.render_scale)
           IS DISTINCT FROM
           (d.name::varchar, d.source_file::varchar, 'water-features'::varchar, 'default'::varchar,
            0.5::double precision, 0.5::double precision, 'objects'::varchar, 'mana-seed-fishing'::varchar, 1,
            false, 2.0::double precision);
    IF bad > 0 THEN
        RAISE EXCEPTION 'LLM-967k down: % school-of-fish row(s) differ from the Mana Seed rows', bad;
    END IF;
END $$;

COMMIT;
