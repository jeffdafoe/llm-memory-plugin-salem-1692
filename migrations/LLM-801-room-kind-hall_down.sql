-- LLM-801 rollback: the `hall` room kind.
--
-- Postgres cannot drop a value from an enum type, so the value stays; this
-- removes every room of that kind instead (LLM-801b's down already does for the
-- Inn's), clearing any actor's room scope that names one first —
-- inside_room_id is a persisted, checkpointed actor column and
-- validateActorStructureRefs hard-fails a boot on a room that no longer
-- resolves. Apply with the engine STOPPED.

BEGIN;

UPDATE actor
SET inside_room_id = NULL
WHERE inside_room_id IN (SELECT id FROM structure_room WHERE kind = 'hall');

DELETE FROM structure_room WHERE kind = 'hall';

COMMIT;
