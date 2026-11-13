-- LLM-967: the Maple Tree draws our own painted maple (tools/tree-art), not
-- the Mana Seed forest sheets.
--
-- The art is client/assets/trees/maple.png, packed into the client; the
-- catalog loads a res://assets/ sheet from the package instead of fetching
-- it (catalog.gd _load_packed_sheet). One 80x112 frame per look, in the
-- order tools/tree-art/build.py LOOKS writes them. The frame size and the
-- asset's anchor are unchanged, so no placed maple moves.
--
-- WHAT changes: the eight Maple Tree states (default and the seven LLM-849
-- season looks) get the new sheet and their frame's rect. Their names and
-- season tags stay as they are, so the engine's look chain picks the same
-- state as before. Nothing else in the catalog is touched.
--
-- Pinned by asset id. A database without the maple (a fresh one: the
-- catalog is not in schema.sql) is left alone. With it, every one of the
-- eight states must end with exactly the authored values, or the migration
-- fails.
--
-- ENGINE-OWNED reference data, read at boot; deploy.sh runs stop -> migrate
-- -> start. Convergent: rerunning it ends in the same place.

BEGIN;

DO $$
DECLARE
    maple CONSTANT uuid := '2d91c8a9-6501-4e16-873a-4d18bdc6f63e';
    new_sheet CONSTANT text := 'res://assets/trees/maple.png';
    good integer;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM asset WHERE id = maple) THEN
        RETURN;
    END IF;

    CREATE TEMP TABLE llm967_maple (state text PRIMARY KEY, src_x integer NOT NULL) ON COMMIT DROP;
    INSERT INTO llm967_maple (state, src_x) VALUES
        ('default',            0),
        ('autumn-clean',      80),
        ('autumn-leaves',    160),
        ('autumn-bare',      240),
        ('winter-clean',     320),
        ('winter-leaves',    400),
        ('winter-snowy-top', 480),
        ('winter-snowy',     560);

    UPDATE asset_state s
       SET sheet = new_sheet, src_x = m.src_x, src_y = 0, src_w = 80, src_h = 112,
           frame_count = 1, frame_rate = 0
      FROM llm967_maple m
     WHERE s.asset_id = maple AND s.state = m.state;

    SELECT count(*) INTO good
      FROM asset_state s
      JOIN llm967_maple m ON m.state = s.state
     WHERE s.asset_id = maple AND s.sheet = new_sheet AND s.src_x = m.src_x AND s.src_y = 0
       AND s.src_w = 80 AND s.src_h = 112 AND s.frame_count = 1 AND s.frame_rate = 0;
    IF good <> 8 THEN
        RAISE EXCEPTION 'LLM-967: % of the 8 Maple Tree states have the painted maple''s values', good;
    END IF;

    IF EXISTS (SELECT 1 FROM asset_state s WHERE s.asset_id = maple AND s.sheet <> new_sheet) THEN
        RAISE EXCEPTION 'LLM-967: the Maple Tree has a state this migration does not know';
    END IF;
END $$;

COMMIT;
