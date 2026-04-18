-- ZBBS-060: scope the lamplighter to lamp-like objects only.
--
-- Previously the lamplighter walked to every object with day-active or
-- night-active states — including campfires, which should auto-flip via
-- the bulk transition without the lamplighter visiting them.
--
-- Introduce a lamplighter-target tag on the specific states the
-- lamplighter should walk (Hanging Lantern, Torch, Lamp Post 1/2/3).
-- Campfires keep their day-active / night-active tags, so the bulk
-- transition still auto-flips them — the lamplighter just ignores them.

BEGIN;

INSERT INTO asset_state_tag (state_id, tag)
SELECT s.id, 'lamplighter-target'
FROM asset_state s
JOIN asset a ON s.asset_id = a.id
JOIN asset_state_tag t ON t.state_id = s.id
WHERE a.name IN ('Hanging Lantern', 'Torch', 'Lamp Post', 'Lamp Post 2', 'Lamp Post 3')
  AND t.tag IN ('day-active', 'night-active');

COMMIT;
