-- ZBBS-159: npc_errand_offer — small fetch/deliver tasks NPCs offer to PCs.
--
-- An NPC requests a fetch (e.g. "fetch eggs from Josiah, reward 3c"),
-- the PC explicitly accepts via /pc/accept-errand, completes by
-- delivering via /pc/complete-errand at the requester's location.
-- Reward paid via existing pay flow.
--
-- v1 scope per work mail 32e8824c:
--   - Explicit accept + complete endpoints (NOT implicit detection).
--     Cleaner than guessing intent from /pc/move + /pc/pay heuristics.
--   - One source actor, one item kind, one reward.
--   - State machine: offered → accepted → completed | expired |
--     rejected. Single forward transition per state.
--   - Authoring: direct INSERT only for v1 (admin / seed). Chronicler
--     tool deferred. Schema is intentionally agnostic so any author
--     can post.
--   - Reward fires via reactor-tick on completion (requester pays the
--     PC via existing pay flow). v1 keeps it simple — direct UPDATE
--     of coins (skip the deliberation gate; this is a contractual
--     handoff, not a haggle).

BEGIN;

CREATE TABLE npc_errand_offer (
    id                   BIGSERIAL PRIMARY KEY,
    requester_actor_id   UUID NOT NULL REFERENCES actor(id) ON DELETE CASCADE,
    target_pc_actor_id   UUID NULL REFERENCES actor(id) ON DELETE SET NULL,
    fetch_item_kind      VARCHAR(32) NOT NULL REFERENCES item_kind(name) ON UPDATE CASCADE,
    fetch_qty            INTEGER NOT NULL DEFAULT 1 CHECK (fetch_qty > 0),
    source_actor_id      UUID NULL REFERENCES actor(id) ON DELETE SET NULL,
    source_structure_id  UUID NULL REFERENCES village_object(id) ON DELETE SET NULL,
    reward_coins         INTEGER NOT NULL CHECK (reward_coins > 0),
    state                VARCHAR(16) NOT NULL CHECK (state IN ('offered','accepted','completed','expired','rejected')),
    offered_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    accepted_at          TIMESTAMPTZ NULL,
    completed_at         TIMESTAMPTZ NULL,
    expires_at           TIMESTAMPTZ NULL
);

-- /pc/me's "active errands" lookup is keyed by target_pc; the PC
-- sees their pending + accepted offers.
CREATE INDEX ix_npc_errand_offer_target_active
    ON npc_errand_offer (target_pc_actor_id)
    WHERE state IN ('offered','accepted');

-- Seed: John Ellis offers Jefferey to fetch milk from Josiah for 3
-- coins. Demonstrates the path; gracefully omits if the actors are
-- absent in test environments.
INSERT INTO npc_errand_offer (requester_actor_id, target_pc_actor_id, fetch_item_kind, fetch_qty,
                              source_actor_id, source_structure_id, reward_coins, state, expires_at)
SELECT
    (SELECT id FROM actor WHERE display_name = 'John Ellis' LIMIT 1),
    (SELECT id FROM actor WHERE login_username = 'jeff' LIMIT 1),
    'milk',
    1,
    (SELECT id FROM actor WHERE display_name = 'Josiah Thorne' LIMIT 1),
    (SELECT work_structure_id FROM actor WHERE display_name = 'Josiah Thorne' LIMIT 1),
    3,
    'offered',
    NOW() + INTERVAL '2 hours'
WHERE EXISTS (SELECT 1 FROM actor WHERE display_name = 'John Ellis')
  AND EXISTS (SELECT 1 FROM actor WHERE login_username = 'jeff')
  AND EXISTS (SELECT 1 FROM actor WHERE display_name = 'Josiah Thorne');

COMMIT;
