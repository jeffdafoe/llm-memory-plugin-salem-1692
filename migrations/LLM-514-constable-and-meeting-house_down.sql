-- Revert LLM-514's data activation: remove Gideon's `constable` attribute and
-- return the Red Monastery (Meeting House) to visible_when_inside=true.
-- Manual-rollback only (the deploy runner never applies _down). This leaves the
-- LLM-514 engine code inert (no constable carrier) and the Meeting House back in
-- its prior visible-interior mode; pair with a code revert to fully back out.

BEGIN;

DELETE FROM actor_attribute
WHERE actor_id = '4561da54-eb08-46c8-8f05-ddc0aadaebff' AND slug = 'constable';

UPDATE asset
SET visible_when_inside = true
WHERE id = '389b2ebe-9430-4691-9b85-3e64898f19cb';

COMMIT;
