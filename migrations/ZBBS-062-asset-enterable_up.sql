-- ZBBS-062: per-asset "enterable" flag.
--
-- Door markers, home/work routing, and the editor's structure picker all
-- gated on category='structure' before this — which meant tents (a non-
-- structure category that's still a building you'd walk into) couldn't be
-- used as a home. Pull the semantic out into its own boolean so admins can
-- toggle it per-asset from the editor.
--
-- Backfill: every asset currently categorised 'structure' becomes enterable,
-- so no behavior change for existing placed houses/castles/monasteries.

BEGIN;

ALTER TABLE asset
    ADD COLUMN enterable BOOLEAN NOT NULL DEFAULT false;

UPDATE asset SET enterable = true WHERE category = 'structure';

COMMIT;
