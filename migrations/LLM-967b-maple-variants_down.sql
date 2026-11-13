-- LLM-967 down: one maple again. Every placed Maple Tree 2 / 3 goes back to
-- the Maple Tree, the two variant assets are deleted (their states, tags and
-- rest default go with them), and the Maple Tree's eight states return to
-- maple.png, one still 80x112 frame per look, left to right
-- (LLM-967-painted-maple).

BEGIN;

DO $$
DECLARE
    maple CONSTANT uuid := '2d91c8a9-6501-4e16-873a-4d18bdc6f63e';
    maple2 CONSTANT uuid := 'a0882055-bfb9-4b12-8f63-88ea73dd6e3c';
    maple3 CONSTANT uuid := 'f0f99142-2e0d-4a72-89de-838db17082b1';
    good integer;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM asset WHERE id = maple) THEN
        RETURN;
    END IF;

    UPDATE village_object SET asset_id = maple WHERE asset_id IN (maple2, maple3);

    DELETE FROM asset_state_tag WHERE state_id IN (SELECT id FROM asset_state WHERE asset_id IN (maple2, maple3));
    DELETE FROM asset_state WHERE asset_id IN (maple2, maple3);
    DELETE FROM asset_refresh_default WHERE asset_id IN (maple2, maple3);
    DELETE FROM asset WHERE id IN (maple2, maple3);

    CREATE TEMP TABLE llm967v_down (state text PRIMARY KEY, src_x integer NOT NULL) ON COMMIT DROP;
    INSERT INTO llm967v_down VALUES
        ('default', 0), ('autumn-clean', 80), ('autumn-leaves', 160), ('autumn-bare', 240),
        ('winter-clean', 320), ('winter-leaves', 400), ('winter-snowy-top', 480), ('winter-snowy', 560);

    UPDATE asset_state s
       SET sheet = 'res://assets/trees/maple.png', src_x = d.src_x, src_y = 0, src_w = 80, src_h = 112,
           frame_count = 1, frame_rate = 0
      FROM llm967v_down d
     WHERE s.asset_id = maple AND s.state = d.state;

    SELECT count(*) INTO good
      FROM asset_state s JOIN llm967v_down d ON d.state = s.state
     WHERE s.asset_id = maple AND s.sheet = 'res://assets/trees/maple.png' AND s.src_x = d.src_x
       AND s.src_y = 0 AND s.frame_count = 1;
    IF good <> 8 THEN
        RAISE EXCEPTION 'LLM-967 down: % of the 8 Maple Tree states are back on maple.png', good;
    END IF;
    IF EXISTS (SELECT 1 FROM asset WHERE id IN (maple2, maple3))
       OR EXISTS (SELECT 1 FROM village_object WHERE asset_id IN (maple2, maple3)) THEN
        RAISE EXCEPTION 'LLM-967 down: a maple variant is left behind';
    END IF;
END $$;

COMMIT;
