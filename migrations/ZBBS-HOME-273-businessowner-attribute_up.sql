-- ZBBS-HOME-273 — businessowner attribute + engine-authored greet trigger.
--
-- Schema half of the businessowner-attribute design
-- (shared/tasks/pending/salem-businessowner-attribute). Adds:
--
--   1. attribute_definition row for 'businessowner'. Purely engine-
--      mechanical — gates the greet/handover/farewell triggers in
--      engine/businessowner.go. Description carries the engine-only
--      semantics so an operator browsing the admin UI doesn't expect
--      it to inject prompt copy. instructions stays empty so the
--      LLM prompt isn't bloated; the attribute IS the gate, not a
--      role description.
--
--   2. actor_attribute seed rows for the four current keepers:
--      John Ellis (Tavern, flamboyant), Hannah Boggs (Inn, flamboyant),
--      Josiah Thorne (General Store, flamboyant), Ezekiel Crane
--      (Smithy, reserved). params.flavor selects the phrase pool the
--      engine pulls from at trigger fire time. Adding a new keeper
--      later is an INSERT into actor_attribute, no migration required.
--
--   3. actor_interaction_cooldown table: per-pair, per-trigger
--      cooldown so the village doesn't re-greet the same customer
--      every time they pop back in. PK on the triple
--      (speaker_id, listener_id, trigger) keeps lookups point-query
--      cheap and upserts atomic; no secondary index needed. The
--      table grows roughly O(keepers × villagers × triggers) which
--      maxes at low thousands for a real village — well within the
--      "don't bother GC'ing" regime.
--
--   4. Two operator settings — greet and farewell cooldown windows
--      (default 30 min each, mirrors the task design). handover
--      has no cooldown setting because every delivery deserves a
--      handover line; the cooldown table just isn't consulted on
--      the handover path.

BEGIN;

INSERT INTO attribute_definition (slug, display_name, description) VALUES (
    'businessowner',
    'Business Owner',
    E'Owner-operator of a customer-facing business. Gates engine-authored hospitality triggers: greet on entry, handover on delivery, farewell on exit. Per-actor params.flavor ("flamboyant", "reserved", future "warm") selects the phrase pool the engine pulls from. Engine-only mechanism — does not inject any text into the LLM prompt.'
);

INSERT INTO actor_attribute (actor_id, slug, params)
SELECT id, 'businessowner', '{"flavor":"flamboyant"}'::jsonb
  FROM actor
 WHERE display_name IN ('John Ellis','Hannah Boggs','Josiah Thorne')
ON CONFLICT (actor_id, slug) DO NOTHING;

INSERT INTO actor_attribute (actor_id, slug, params)
SELECT id, 'businessowner', '{"flavor":"reserved"}'::jsonb
  FROM actor
 WHERE display_name = 'Ezekiel Crane'
ON CONFLICT (actor_id, slug) DO NOTHING;

CREATE TABLE actor_interaction_cooldown (
    speaker_id    UUID        NOT NULL REFERENCES actor(id) ON DELETE CASCADE,
    listener_id   UUID        NOT NULL REFERENCES actor(id) ON DELETE CASCADE,
    trigger       VARCHAR(16) NOT NULL,
    last_fired_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (speaker_id, listener_id, trigger)
);

INSERT INTO setting (key, value, description, is_public) VALUES
    ('businessowner_greet_cooldown_minutes', '30',
     'Minutes between engine-authored greets from a businessowner to the same listener. Customer pops out and back in within this window: no second greet.',
     FALSE),
    ('businessowner_farewell_cooldown_minutes', '30',
     'Minutes between engine-authored farewells from a businessowner to the same listener. Symmetric to greet — only one farewell per session-visit.',
     FALSE)
ON CONFLICT (key) DO NOTHING;

COMMIT;
