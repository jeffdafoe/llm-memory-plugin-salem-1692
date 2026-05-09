-- ZBBS-WORK-204 (commit 2 of 2): NPC long-term lodging — boarder
-- semantics, lodger seed rows, vendor_flavor column.
--
-- Pairs with the engine code in this same commit (auto-rebook sweep,
-- lodger and keeper perception cues, NPC-lodger sleep fallback,
-- handlePCCreate change). The earlier ZBBS-WORK-204-keeper-seed
-- migration shipped the keeper (Hannah Boggs) and the
-- lodging_default_weekly_rate setting; this one flips boarder
-- semantics on for everyone whose "home" was actually a structure
-- they don't own.
--
-- Net schema change: one new column (actor.vendor_flavor TEXT). The
-- rest is data: clear home_structure_id where it's the boarder shape,
-- seed starter nights_stay rows so isLodger materializes from minute
-- one, and stamp Hannah's vendor_flavor for the keeper preface block.

BEGIN;

-- 1. Per-vendor narrative flavor column. NULLABLE; populated only
-- for salem-vendor-backed keepers where engine-injected per-call
-- context isn't enough to distinguish their voice from another
-- keeper sharing the same generic VA. Engine appends the trimmed
-- text as a trailing paragraph on the keeper rooms-available
-- perception block (formatKeeperVendorPerception). Future
-- shopkeepers / decorative vendors get their own UPDATEs against
-- this column; no per-keeper VA prompt edits needed.
ALTER TABLE actor
    ADD COLUMN vendor_flavor TEXT;

-- 2. Hannah Boggs's flavor. Period-fiction-tropes ambiguity — the
-- LLM voices her as the unflappable innkeeper who knows more than
-- she lets on. Same vendor template as every other salem-vendor
-- keeper; this string is what makes her sound like Hannah.
UPDATE actor
   SET vendor_flavor = 'The village whispers about who comes and goes from her inn after dark — but Hannah herself never confirms or denies.'
 WHERE display_name = 'Hannah Boggs';

-- 3. Seed starter nights_stay ledger rows for actors whose home is
-- a lodging-tagged structure they don't own. INSERT runs BEFORE the
-- UPDATE that clears home_structure_id, so the join through
-- a.home_structure_id still resolves the right keeper. The row
-- counterparty (seller) is the keeper of the actor's lodging
-- structure — first match by created_at among workers who carry
-- nights_stay in their inventory, so a structure with multiple
-- workers picks deterministically.
--
-- One row per (boarder, keeper) pair. The NOT EXISTS guard makes
-- the migration idempotent — re-running on a database where some
-- of these boarders already have an active nights_stay row (e.g.
-- the keeper just check-in'd them via deliver_order in the window
-- before the migration ran) is a no-op for those.
--
-- qty=1 with ready_by=CURRENT_DATE makes day 1 the extension-
-- negotiation window: the lodger sees the "your room expires
-- Friday" perception cue immediately, which is the LLM-driven
-- moment we watch for. If the LLM doesn't act, the engine-auto
-- rebook sweep fires the backstop at 6h pre-expiry.
--
-- offered_amount=0 and quoted_unit_amount=0 because this is a
-- bootstrapping seed, not a real transaction. The check constraint
-- on pay_ledger requires offered_amount >= 0; 0 satisfies it.
INSERT INTO pay_ledger (
    buyer_id, seller_id, item_kind, qty, offered_amount,
    quoted_unit_amount, consume_now, state, message,
    ready_by, fulfillment_status, delivered_on, resolved_at
)
SELECT
    a.id,
    keeper.id,
    'nights_stay',
    1,
    0,
    0,
    false,
    'accepted',
    'ZBBS-WORK-204 starter',
    CURRENT_DATE,
    'delivered',
    NOW(),
    NOW()
FROM actor a
JOIN LATERAL (
    SELECT k.id
      FROM actor k
      JOIN actor_inventory ki ON ki.actor_id = k.id AND ki.item_kind = 'nights_stay'
     WHERE k.work_structure_id = a.home_structure_id
       AND k.llm_memory_agent IS NOT NULL
     ORDER BY k.created_at ASC
     LIMIT 1
) keeper ON true
WHERE a.home_structure_id IS NOT NULL
  AND (a.work_structure_id IS NULL OR a.home_structure_id != a.work_structure_id)
  AND a.home_structure_id IN (
      SELECT object_id FROM village_object_tag WHERE tag = 'lodging'
  )
  AND NOT EXISTS (
      SELECT 1 FROM pay_ledger pl
       WHERE pl.buyer_id  = a.id
         AND pl.seller_id = keeper.id
         AND pl.item_kind = 'nights_stay'
         AND pl.state = 'accepted'
         AND pl.fulfillment_status = 'delivered'
  );

-- 4. Clear home_structure_id for boarder-shaped actors. Keyed to
-- the distinctive message string from step 3 so we ONLY clear
-- home for actors whose starter row this migration just inserted.
-- A boarder whose lodging structure has no keeper (test env, mid-
-- setup deploy) won't have a starter row and so keeps their home
-- column unchanged — they don't end up genuinely homeless until
-- an admin assigns a keeper. Same key as the down migration's
-- DELETE so the two paths agree on which rows belong to this
-- migration.
--
-- After this UPDATE, isLodger (which reads pay_ledger, not
-- actor.home_structure_id) is the only path that gives these
-- actors entry to and exemption inside their lodging structure.
-- canEnter, wouldBeEvictionExempt, and the new lodger perception
-- cue all flow through the same materialized-from-ledger predicate.
-- Hannah's row stays untouched because home == work for her
-- (she owns the place; she's not a boarder).
UPDATE actor a
   SET home_structure_id = NULL
 WHERE a.home_structure_id IS NOT NULL
   AND (a.work_structure_id IS NULL OR a.home_structure_id != a.work_structure_id)
   AND a.home_structure_id IN (
       SELECT object_id FROM village_object_tag WHERE tag = 'lodging'
   )
   AND EXISTS (
       SELECT 1 FROM pay_ledger pl
        WHERE pl.buyer_id = a.id
          AND pl.item_kind = 'nights_stay'
          AND pl.message = 'ZBBS-WORK-204 starter'
   );

-- 5. Race-safe idempotency for the auto-rebook sweep
-- (engine/lodging_rebook.go). Without this index, two concurrent
-- transactions (e.g., the sweep and an LLM-driven deliver_order
-- landing the same minute) could both pass the WHERE NOT EXISTS
-- guard and both insert. The unique constraint enforces "one
-- delivered nights_stay row per (buyer, seller, ready_by)" so
-- the loser's INSERT lands on ON CONFLICT DO NOTHING and we get
-- a clean idempotent skip. Partial because non-active rows
-- (declined, withdrawn, pending, prior delivery iterations) can
-- legitimately repeat. Created with a stable name so the
-- engine's ON CONFLICT clause can target it by name.
CREATE UNIQUE INDEX pay_ledger_lodging_active_once
    ON pay_ledger (buyer_id, seller_id, ready_by)
 WHERE item_kind = 'nights_stay'
   AND state = 'accepted'
   AND fulfillment_status = 'delivered';

COMMIT;
