-- LLM-807 rollback: remove the Tavern's upstairs hall.
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
    WHERE structure_id = '019dbcd2-c0b1-7bf9-98c2-0610cfb7f5e9'
      AND name = 'hall'
);

DELETE FROM structure_room
WHERE structure_id = '019dbcd2-c0b1-7bf9-98c2-0610cfb7f5e9'
  AND name = 'hall';

COMMIT;
