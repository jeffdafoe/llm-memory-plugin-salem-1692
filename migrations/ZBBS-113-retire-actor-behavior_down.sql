-- ZBBS-113 down: re-create the schema scaffold for actor.behavior
-- and npc_behavior. Historical row data isn't restored — an admin
-- reassigns roles via the attribute system after rolling back.

BEGIN;

CREATE TABLE IF NOT EXISTS npc_behavior (
    slug VARCHAR(64) PRIMARY KEY,
    display_name VARCHAR(100) NOT NULL
);

ALTER TABLE actor
    ADD COLUMN IF NOT EXISTS behavior VARCHAR(32) REFERENCES npc_behavior(slug);

COMMIT;
