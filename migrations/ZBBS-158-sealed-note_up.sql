-- ZBBS-158: sealed_note — letter/messenger errand chain.
--
-- A short note authored by NPC A, addressed to NPC B, carried by a
-- specific PC. PC walks to recipient, delivers via /pc/deliver-note,
-- the note becomes unsealed and shows in the recipient's perception
-- so they can react in their next speak.
--
-- v1 scope per work mail 32e8824c:
--   - One note per row. Single recipient.
--   - PC-courier-anchored (courier_actor_id). The note "lives" with
--     the PC until delivered; no inventory-row tie-in for v1
--     (skipping the actor_inventory_meta route — schema is enough).
--   - Author can be any actor (chronicler authoring deferred; v1 is
--     direct INSERT).
--   - Delivery flips sealed=false + stamps delivered_at; the
--     recipient's next perception surfaces the body in a
--     "Notes delivered to you:" block.
--
-- Out of scope: multi-recipient notes, note replies, player-authored
-- compose flow.

BEGIN;

CREATE TABLE sealed_note (
    id                BIGSERIAL PRIMARY KEY,
    author_actor_id   UUID NOT NULL REFERENCES actor(id) ON DELETE CASCADE,
    recipient_actor_id UUID NOT NULL REFERENCES actor(id) ON DELETE CASCADE,
    courier_actor_id  UUID NULL REFERENCES actor(id) ON DELETE SET NULL,
    body_text         TEXT NOT NULL CHECK (length(trim(body_text)) > 0),
    sealed            BOOLEAN NOT NULL DEFAULT TRUE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    delivered_at      TIMESTAMPTZ NULL
);

-- Index for the courier's "what notes do I carry" lookup at /pc/me.
CREATE INDEX ix_sealed_note_courier ON sealed_note (courier_actor_id) WHERE sealed = true;

-- Index for the recipient's perception block — recently delivered
-- notes addressed to me. Partial index keeps it small.
CREATE INDEX ix_sealed_note_delivered ON sealed_note (recipient_actor_id, delivered_at DESC) WHERE sealed = false;

-- Seed: one demo note from John Ellis to Josiah Thorne, courier =
-- Jefferey. Demonstrates the path; if Jefferey isn't around (test
-- environment) the note sits sealed until manual cleanup or a
-- compatible courier picks it up via /pc/me's courier list.
INSERT INTO sealed_note (author_actor_id, recipient_actor_id, courier_actor_id, body_text)
SELECT
    (SELECT id FROM actor WHERE display_name = 'John Ellis' LIMIT 1),
    (SELECT id FROM actor WHERE display_name = 'Josiah Thorne' LIMIT 1),
    (SELECT id FROM actor WHERE login_username = 'jeff' LIMIT 1),
    'I shall need three horseshoes by Thursday next, if you can put me to the smith. — John'
WHERE EXISTS (SELECT 1 FROM actor WHERE display_name = 'John Ellis')
  AND EXISTS (SELECT 1 FROM actor WHERE display_name = 'Josiah Thorne')
  AND EXISTS (SELECT 1 FROM actor WHERE login_username = 'jeff');

COMMIT;
