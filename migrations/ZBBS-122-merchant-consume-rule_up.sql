-- ZBBS-122: append the consume-your-own-stock rule to the merchant role.
--
-- The blacksmith, tavernkeeper, and herbalist instructions all end with
-- some variant of "When you consume your own stock, ALWAYS call
-- consume. Never narrate eating via act." The merchant instructions
-- (added in ZBBS-115's vendor-prompts sweep) are missing this rule
-- entirely — Josiah Thorne sat off-shift in the General Store next to
-- bread, cheese, and meat with all three needs in red, and his prompt
-- never told him eating his own stock was an option (or that consume
-- was the right tool for it). Diagnosed 2026-05-04 from the
-- bc63b12d → 5f384ca3 mail thread.
--
-- Phrasing matches the tavernkeeper rule (eat from your own stock /
-- never narrate eating via act) — fits merchant goods better than the
-- blacksmith phrasing, since merchant stock is consumables not tools.
--
-- Append-only: preserves any other edits to the merchant instructions
-- since ZBBS-115 landed. Idempotent via the "ALWAYS call consume"
-- marker phrase — re-runs no-op once the sentence is present.

BEGIN;

UPDATE attribute_definition
   SET instructions = instructions || E'\n- When you eat from your own stock, ALWAYS call consume. Never narrate eating via act.',
       updated_at = NOW()
 WHERE slug = 'merchant'
   AND instructions NOT LIKE '%ALWAYS call consume%';

COMMIT;
