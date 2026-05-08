-- ZBBS-167: tavernkeeper role overlay — already-checked-in lodgers
-- + speech/tool-pairing rule.
--
-- Closes the LLM behavior gap surfaced when Jefferey asked John Ellis
-- "do I have a room booked for tonight?" while already a checked-in
-- lodger. John correctly said "yes" but bolted on "I'll get the key
-- for you" without calling deliver_order — the speech promised an
-- action that has no mechanical backing (he was already checked in
-- and there is no key object). Player-visible broken promise.
--
-- Two new paragraphs appended to the tavernkeeper role overlay:
--
-- (1) ALREADY-CHECKED-IN LODGERS — names the perception block
--     ("Lodgers in your rooms"), explains those guests already have
--     room access and need no fresh handoff, and gives concrete
--     example wording so the LLM has a template to follow.
--
-- (2) SPEECH AND TOOL PAIRING — general rule: speech that announces
--     a concrete action MUST be paired with the matching tool call
--     in the same response. Speech alone is just words. If no tool
--     matches, choose wording that is true at the dialogue level.
--
-- Idempotency: WHERE instructions NOT LIKE '%ALREADY-CHECKED-IN
-- LODGERS%' so re-runs are a no-op once the text is in place.
--
-- Scope: tavernkeeper only — the lodging case is the one that
-- triggered the bug. The SPEECH AND TOOL PAIRING rule generalizes
-- to other vendors (blacksmith, merchant, herbalist) but is held
-- out of this migration to keep the change focused on the observed
-- failure.

BEGIN;

UPDATE attribute_definition
   SET instructions = instructions || E'\n\nALREADY-CHECKED-IN LODGERS:\n- Your perception''s "Lodgers in your rooms" line shows guests who are CURRENTLY staying with you — already checked in, room access already granted. If one of them asks about their room or booking, confirm warmly WITHOUT promising a fresh handoff. There is no key to fetch, no door to unlock, no new check-in to perform. Say something like "you''re all set upstairs" or "your room''s ready whenever you want to turn in" — not "I''ll get the key for you."\n- If they want ANOTHER night beyond what they''ve paid for, they initiate it via a new pay() call. You don''t extend their stay yourself.\n\nSPEECH AND TOOL PAIRING:\n- When you say you''ll do a concrete physical action ("I''ll get the key", "I''ll bring you bread", "I''ll show you to your room"), you MUST call the matching tool (deliver_order, serve, etc.) in the same response. Speech alone is just words; the mechanic only happens through the tool call. If no tool matches what you''re saying, don''t say it — choose wording that''s true at the dialogue level alone (e.g. "right this way" without an unfulfilled handoff).',
       updated_at = NOW()
 WHERE slug = 'tavernkeeper'
   AND instructions NOT LIKE '%ALREADY-CHECKED-IN LODGERS%';

COMMIT;
