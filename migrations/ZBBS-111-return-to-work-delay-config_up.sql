-- ZBBS-111: seed return-to-work delay range as a configurable setting.
--
-- Replaces the hardcoded 30s/60s constants in self_tick.go. Stored as a
-- JSON 2-element int array — admins edit the seconds range; the
-- (eventual) settings UI will accept the friendlier "30,60" form and
-- translate to this JSON shape on save. Engine reads JSON only.
--
-- Default 30..60s gives the conversation a beat to land before
-- re-prompting (floor) without dragging the rhythm (ceiling).

BEGIN;

INSERT INTO setting (key, value)
VALUES ('return_to_work_delay_seconds', '[30,60]')
ON CONFLICT (key) DO NOTHING;

COMMIT;
