-- ZBBS-080 (M6.7 step 2): PC character identity + home assignment.
--
-- A PC's actor_name (llm-memory username) is the system identity —
-- stable, used for chat_send addressing, session lookup. But for the
-- in-world social fabric — what NPCs perceive, who they greet by name
-- in their "Here:" block, what name shows up in the audit log when
-- they speak — we want a period-appropriate character_name.
-- "Henry Jacobs" reads natural in a 1692 village. "jeff" does not.
--
-- character_name is REQUIRED for any PC to participate; the talk
-- panel's first-login flow will prompt for one before unlocking
-- chat. Until set, the PC's pc_position row is essentially dormant.
--
-- home_structure_id assigns the PC to lodge at a tavern. Travelers
-- in 1692 Salem boarded at the local ordinary; auto-picked from
-- nearest village_object tagged 'tavern' on PC creation. Stored as
-- a column rather than computed each tick because (a) it's stable
-- across a session, (b) some narrative actions may want to override
-- (a player chooses to lodge with a friend, etc.).

BEGIN;

ALTER TABLE pc_position
    ADD COLUMN character_name VARCHAR(100),
    ADD COLUMN home_structure_id UUID REFERENCES village_object(id) ON DELETE SET NULL;

-- character_name is conceptually NOT NULL after the create flow runs,
-- but existing seeded rows (from manual SQL) may not have one. Backfill
-- with a placeholder; clean up on the next create call.
UPDATE pc_position
SET character_name = COALESCE(character_name, actor_name)
WHERE character_name IS NULL;

ALTER TABLE pc_position
    ALTER COLUMN character_name SET NOT NULL;

CREATE INDEX idx_pc_position_character ON pc_position(character_name);

COMMIT;
