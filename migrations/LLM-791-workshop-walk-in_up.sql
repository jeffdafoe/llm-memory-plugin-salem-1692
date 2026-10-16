-- LLM-791: a non-stall business is open only while its keeper is there.
--
-- The engine now locks a business-tagged, non-market-stall structure whose
-- static policy is open whenever no keeper is there (engine/sim/business_lock.go
-- businessLocked). Lewis's Workshop was owner-only, so customers could not walk
-- in even with Lewis at his bench. Set it to open so the keeper rule covers it:
-- open while Lewis is there, locked (members only, everyone else knocks) while
-- he is away.
--
-- Pinned by id. Only an owner-only row moves, so a rerun or a hand edit to
-- another policy is left alone.

BEGIN;

UPDATE village_object
   SET entry_policy = 'open'
 WHERE id = '019e0e3c-56fb-71a2-96b3-0f0740b6077b'
   AND entry_policy = 'owner-only';

COMMIT;
