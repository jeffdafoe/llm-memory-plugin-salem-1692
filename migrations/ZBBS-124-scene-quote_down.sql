-- ZBBS-124 rollback: drop scene_quote.
--
-- Quotes are derived from the chat stream and have no persistence
-- value beyond their scene's lifetime, so dropping the table forfeits
-- nothing the engine can't reconstruct from the next round of speak
-- events.

BEGIN;

DROP TABLE IF EXISTS scene_quote;

COMMIT;
