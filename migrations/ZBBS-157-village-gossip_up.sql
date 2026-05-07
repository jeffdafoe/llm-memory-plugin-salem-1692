-- ZBBS-157: village_gossip — shared observations NPCs reference in speak.
--
-- Extension of the ZBBS-117 retained-concerns pattern. The chronicler
-- (or a future auto-authoring path) records small village-level
-- observations; NPCs other than the subject see them in their
-- perception's "Around the village:" block and may organically
-- reference them in speak outputs.
--
-- v1 scope per work mail 32e8824c:
--   - Freeform `text` body — keep it simple, no predicate/object split.
--   - Optional `subject_actor_id` for "exclude subject from seeing" rule.
--   - Time-bounded via `expires_at`. NULL = permanent (rare).
--   - Author-agnostic: anyone with INSERT can post. v1 has direct
--     INSERT (admin / seed) only; chronicler tool integration
--     deferred.
--
-- Out of scope: reputation scoring, gossip mutation across NPCs,
-- player-authored gossip, region scoping.

BEGIN;

CREATE TABLE village_gossip (
    id               BIGSERIAL PRIMARY KEY,
    text             TEXT NOT NULL CHECK (length(trim(text)) > 0),
    subject_actor_id UUID NULL REFERENCES actor(id) ON DELETE SET NULL,
    authored_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at       TIMESTAMPTZ NULL,
    region_scope     TEXT NULL  -- reserved for future region-aware filtering
);

-- Selection index: ranks active gossip by authored_at DESC for the
-- perception's "Around the village:" block (newest first). expires_at
-- can't go in the partial-index predicate (NOW() not IMMUTABLE).
CREATE INDEX ix_village_gossip_recent ON village_gossip (authored_at DESC, id DESC);

-- Seed two starter gossip lines, both expiring 24h after creation.
-- Subject is left NULL on the first to demonstrate the no-subject case.
INSERT INTO village_gossip (text, expires_at) VALUES
    ('They say a stranger walked the lane near the Tavern at dusk yesterday — none knew the man.', NOW() + INTERVAL '24 hours');

INSERT INTO village_gossip (text, subject_actor_id, expires_at)
SELECT 'Goody Ward hath taken to gathering wild berries again — her larder is full.',
       a.id,
       NOW() + INTERVAL '24 hours'
  FROM actor a
 WHERE a.display_name = 'Prudence Ward'
 LIMIT 1;

COMMIT;
