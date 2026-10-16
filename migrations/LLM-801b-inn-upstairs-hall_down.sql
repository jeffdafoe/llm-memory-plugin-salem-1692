-- LLM-801b rollback: remove the Inn's upstairs hall.
--
-- Clear any actor's room scope that names it first: inside_room_id is a
-- persisted, checkpointed actor column, and a player standing in the hall at
-- rollback time would otherwise boot with a dangling ref
-- (validateActorStructureRefs hard-fails on a room that no longer resolves).
-- Apply with the engine STOPPED.

BEGIN;

UPDATE actor
SET inside_room_id = NULL
WHERE inside_room_id IN (
    SELECT id FROM structure_room
    WHERE structure_id = '019d98af-ac9b-7833-8e03-5a7015bb5b0c'
      AND name = 'hall'
);

DELETE FROM structure_room
WHERE structure_id = '019d98af-ac9b-7833-8e03-5a7015bb5b0c'
  AND name = 'hall';

COMMIT;
