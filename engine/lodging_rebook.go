package main

// Lodger auto-rebook sweep — engine-side backstop for ongoing
// boarding when the LLM-driven negotiation between lodger and keeper
// doesn't fire in time.
//
// Cadence: once per server tick (60s). Walks every active lodger
// whose latest nights_stay row is within 6h of expiry AND who is
// physically inside the keeper's structure at firing time, charges
// one night's rate against their coins, and inserts a fresh
// state='accepted'/fulfillment_status='delivered' row carrying the
// next night. The actor stays a lodger continuously — no eviction-
// then-re-check-in flicker.
//
// Night-by-night renewal (ZBBS-WORK-221, replacing WORK-204's
// week-at-a-time qty=7 renewal). Two reasons:
//
//   - The commitment the engine takes on behalf of the lodger
//     stays minimal: one night ≈ 4 coins. A lodger who walks away
//     after the first night is out at most that.
//   - Combined with the location gate (below), it makes "auto-
//     rebook is part of being present at the inn, not part of
//     having paid for a row once" the operating invariant.
//
// The setting key stays lodging_default_weekly_rate=28 because
// that's the operator-facing concept (and what keeper perception
// advertises); the per-night charge is derived as weeklyRate / 7.
// Operators should keep the rate divisible by 7 so the per-night
// charge doesn't accumulate rounding error.
//
// Location gate: the sweep only renews a lodger whose
// actor.inside_structure_id matches the keeper's work_structure_id
// at firing time. Consequences:
//
//   - PCs who log in, leave the inn, and never come back stop
//     getting auto-renewed at the moment they walk out. Their row
//     expires naturally. No further charges.
//   - PCs who log off at the inn stay covered — the engine rolls
//     their lease forward one night at a time while they're away.
//     Coming back to a still-occupied room is the natural outcome.
//   - NPCs who got displaced / evicted / never made it home that
//     night also skip — they stop being a paying lodger when they
//     stop being a lodger in fact.
//   - To resume renewals after a gap, a lodger has to manually buy
//     another night (LLM-driven pay flow or PC modal) and be inside
//     the inn at the next firing window.
//
// The firing window of ~5–11am local (lodger_until − 6h through
// lodger_until + 30min, with check-out hour = 11am) is convenient
// for the location gate: anyone actually lodging is asleep in their
// room at that hour; anyone who isn't has by definition opted out.
//
// Why a backstop rather than relying on the LLM end-to-end:
//
//   - Lodger NPCs (Ezekiel, et al) get 48h+ of escalating perception
//     cues telling them their room is about to expire. Even with
//     that, an LLM tick can land on speak/move/done instead of
//     walking to the keeper to negotiate. A renewal that falls
//     through is a UX failure, not a narrative beat — the keeper
//     visibly running their business has tenants who pay rent on
//     time, even when the model drifts.
//   - The rebook fires uniformly: VA-driven NPCs, decorative-villager
//     NPCs, and PCs all flow through the same code path. The renewal
//     ledger row is identical regardless of who's lodging — keeps
//     downstream queries (isLodger, activeLodgersForKeeper) shape-
//     stable.
//
// Failure modes:
//
//   - Lodger purse empty at the firing window. Skip with a log line.
//     Their existing row expires naturally; isLodger drops to false
//     once lodger_until passes. They become homeless on the next
//     sleep cycle.
//   - Lodger not at the keeper's structure at firing time. Silently
//     not a candidate (filtered out in findRebookCandidates) — this
//     is the routine "not lodging tonight" case, not an exception.
//   - Keeper missing (admin deleted the actor since check-in).
//     Skip with a log line; the orphaned ledger row times out
//     naturally.
//   - Race with an LLM-driven deliver_order landing the same minute.
//     Idempotency: the partial-unique-index ON CONFLICT plus a
//     "no overlapping coverage" NOT EXISTS check make the rebook a
//     clean no-op when the LLM-driven path landed first.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
)

// autoRebookLeadTime is how far ahead of lodger_until the sweep
// fires the renewal. 6h is the engine giving up on the LLM —
// every cue and cascade window has passed. Picked to be generous:
// the LLM has multiple ticks-per-game-hour worth of perception
// cues across the prior 48h to walk to the keeper; this window
// catches the "they didn't" outcome.
const autoRebookLeadTime = 6 * time.Hour

// autoRebookGraceWindow lets the sweep also catch rows whose
// lodger_until just barely passed (e.g. previous sweep crashed or
// the engine was down at the firing minute). Without this, a missed
// firing minute would silently let the row expire instead of being
// rebooked retroactively. Bound by the same logic that justifies
// 6h ahead: if we're more than 30 minutes past expiry, the lodger
// status has actually dropped on the door-lock side and we
// shouldn't paper over the gap with a backdated row.
const autoRebookGraceWindow = 30 * time.Minute

// dispatchLodgerAutoRebook is the per-server-tick handler. Find
// boarders whose latest nights_stay row is in the renewal window
// and rebook each in its own short transaction. One tx per (buyer,
// seller) pair so a single failing lodger doesn't roll back the
// rest of the sweep.
//
// Idempotency: the INSERT carries a NOT EXISTS guard against a
// future-window row, so re-running the sweep within the same
// firing minute (or a concurrent LLM-driven deliver_order landing
// the same renewal) doesn't double-book.
func (app *App) dispatchLodgerAutoRebook(ctx context.Context) {
	weeklyRate := app.loadIntSetting(ctx, "lodging_default_weekly_rate", 28)
	if weeklyRate <= 0 {
		// Operator turned the feature off via setting. Skip cleanly —
		// existing rows expire naturally and no auto-renewals fire.
		return
	}
	// Per-night charge derived from the operator-set weekly rate.
	// Operators should keep weeklyRate divisible by 7 so this floors
	// cleanly; a weeklyRate of 29 would charge 4/night = 28/week
	// (loses 1 to integer truncation).
	nightlyRate := weeklyRate / 7
	if nightlyRate <= 0 {
		// weeklyRate < 7: can't bill less than 1 coin per night with
		// integer coins. Treat as feature-off rather than billing 0.
		return
	}

	candidates, err := app.findRebookCandidates(ctx)
	if err != nil {
		log.Printf("auto-rebook: find candidates: %v", err)
		return
	}
	for _, c := range candidates {
		if err := app.rebookLodger(ctx, c, nightlyRate); err != nil {
			log.Printf("auto-rebook %s -> %s: %v", c.BuyerID, c.SellerID, err)
		}
	}
}

// rebookCandidate is one lodger/keeper pair due for renewal this
// firing window. Pulled in a single SELECT so the per-pair tx loop
// below runs sequentially without holding any lock from the
// candidate query.
type rebookCandidate struct {
	BuyerID            string
	BuyerName          string
	SellerID           string
	SellerName         string
	StructureID        string
	StructureLabel     string
	CurrentLodgerUntil time.Time
}

// findRebookCandidates returns the buyer's current keeper pair
// when their globally-latest active nights_stay row is in the
// (now - graceWindow, now + leadTime] firing window AND the buyer
// is physically inside the keeper's structure right now.
//
// Partitioning is per-buyer (not per buyer/seller pair) so an actor
// who switched inns mid-stay doesn't get auto-rebooked at the OLD
// keeper after the new keeper's row took over — design intent is
// one current lodging relationship per buyer globally. If a
// per-buyer policy ever splits (e.g. a noble keeping rooms at two
// inns), this CTE needs broadening then.
//
// Ranking is by computed lodger_until, not raw ready_by — when
// rows of the same pair carry different qty, the latest ready_by
// isn't necessarily the latest expiry (a 1-day starter on Jan 6
// expires before a 7-day weekly that started Jan 1). Computing
// lodger_until in the inner CTE and ranking on that picks the
// truly-latest expiring row.
//
// The inside_structure_id = work_structure_id predicate is the
// location gate documented at the top of this file. NULL on either
// side fails the equality (treated as false) — an actor not inside
// any structure isn't at the inn, and a keeper without a work
// structure can't be lodging anyone. Both are correct exclusions.
func (app *App) findRebookCandidates(ctx context.Context) ([]rebookCandidate, error) {
	rows, err := app.DB.Query(ctx,
		`WITH rows AS (
		    SELECT pl.buyer_id,
		           pl.seller_id,
		           pl.created_at,
		           (
		               (
		                   (pl.ready_by + COALESCE(pl.qty, 1) * INTERVAL '1 day')::timestamp
		                   + (
		                       COALESCE(
		                           (SELECT value::int FROM setting WHERE key = 'lodging_check_out_hour'),
		                           11
		                       ) * INTERVAL '1 hour'
		                     )
		               ) AT TIME ZONE 'America/New_York'
		           ) AS lodger_until
		      FROM pay_ledger pl
		     WHERE pl.item_kind = 'nights_stay'
		       AND pl.state = 'accepted'
		       AND pl.fulfillment_status = 'delivered'
		),
		latest AS (
		    SELECT buyer_id, seller_id, lodger_until,
		           ROW_NUMBER() OVER (
		               PARTITION BY buyer_id
		               ORDER BY lodger_until DESC, created_at DESC
		           ) AS rn
		      FROM rows
		)
		SELECT latest.buyer_id::text,
		       buyer.display_name,
		       latest.seller_id::text,
		       seller.display_name,
		       seller.work_structure_id::text,
		       COALESCE(o.display_name, asset.name) AS structure_label,
		       latest.lodger_until
		  FROM latest
		  JOIN actor buyer  ON buyer.id  = latest.buyer_id
		  JOIN actor seller ON seller.id = latest.seller_id
		  JOIN village_object o ON o.id = seller.work_structure_id
		  JOIN asset ON asset.id = o.asset_id
		 WHERE latest.rn = 1
		   AND latest.lodger_until >  NOW() - $1::interval
		   AND latest.lodger_until <= NOW() + $2::interval
		   AND buyer.inside_structure_id = seller.work_structure_id`,
		fmt.Sprintf("%d seconds", int(autoRebookGraceWindow.Seconds())),
		fmt.Sprintf("%d seconds", int(autoRebookLeadTime.Seconds())),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []rebookCandidate
	for rows.Next() {
		var c rebookCandidate
		if err := rows.Scan(&c.BuyerID, &c.BuyerName,
			&c.SellerID, &c.SellerName,
			&c.StructureID, &c.StructureLabel,
			&c.CurrentLodgerUntil); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// rebookLodger executes one renewal in its own transaction. Steps:
//
//   1. Lock the buyer's actor row (FOR UPDATE) to serialize against a
//      concurrent LLM-driven pay() landing in Tx-B style.
//   2. Re-check the buyer's coin balance against nightlyRate. Skip
//      when broke; the existing row expires naturally and isLodger
//      drops on the next sweep.
//   3. INSERT the renewal pay_ledger row guarded by a NOT EXISTS
//      against any future-window row (idempotency). When INSERT...
//      RETURNING returns no rows, the renewal already exists and
//      we treat it as a clean no-op.
//   4. Debit the buyer's coins.
//   5. Broadcast a room-scoped narration so the keeper / co-located
//      lodgers see "X paid for another night" naturally.
//
// The renewal row's ready_by anchors at the current row's lodger_until
// date so the new night's lodger_until lands at (current + 1 day) at
// lodging_check_out_hour. Continuous coverage — no gap minute where
// isLodger flickers to false.
func (app *App) rebookLodger(ctx context.Context, c rebookCandidate, nightlyRate int) error {
	tx, err := app.DB.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var coins int
	if err := tx.QueryRow(ctx,
		`SELECT coins FROM actor WHERE id = $1::uuid FOR UPDATE`,
		c.BuyerID,
	).Scan(&coins); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("lock buyer: %w", err)
	}
	if coins < nightlyRate {
		log.Printf("auto-rebook: %s (%s) has %d coins; need %d — skipping renewal at %s",
			c.BuyerName, c.BuyerID, coins, nightlyRate, c.SellerName)
		return nil
	}

	// New row anchors at the current lodger_until's local-date so
	// the next night starts when the prior one ends. Computing in Go
	// keeps the INSERT body parameter-typed and avoids re-deriving
	// the timezone-aware lodger_until expression for the date pull.
	cfg, err := app.loadWorldConfig(ctx)
	if err != nil {
		return fmt.Errorf("load world config: %w", err)
	}
	loc := cfg.Location
	if loc == nil {
		loc = time.UTC
	}
	nextReadyBy := c.CurrentLodgerUntil.In(loc)
	nextReadyByDate := time.Date(nextReadyBy.Year(), nextReadyBy.Month(), nextReadyBy.Day(),
		0, 0, 0, 0, loc)

	const renewalQty = 1
	unitAmount := nightlyRate / renewalQty

	// Race-safe idempotency: ON CONFLICT against the partial unique
	// index `pay_ledger_lodging_active_once` (created in the
	// migration) makes "one delivered nights_stay row per (buyer,
	// seller, ready_by)" an enforced invariant rather than a
	// best-effort NOT EXISTS check. If a concurrent LLM-driven
	// deliver_order or a neighboring sweep on another engine
	// instance landed the same renewal row first, the conflict
	// returns zero rows and we treat it as a clean idempotent skip.
	//
	// ZBBS-HOME-261: PG distinguishes constraints from indexes.
	// `pay_ledger_lodging_active_once` is a partial UNIQUE INDEX
	// (created via CREATE UNIQUE INDEX ... WHERE), not an ALTER
	// TABLE constraint, so `ON CONFLICT ON CONSTRAINT <name>` raises
	// SQLSTATE 42704 ("constraint does not exist") at parse time
	// and the INSERT never runs — auto-rebook silently fails every
	// minute. The column-list inference form below targets the
	// partial index by columns + matching predicate; the values in
	// the SELECT are all literal so the predicate is trivially
	// satisfied.
	//
	// Belt-and-braces "no overlapping coverage" check — if a
	// manually-inserted long-qty row exists with an earlier
	// ready_by but coverage extending past nextReadyBy, the partial
	// unique index alone wouldn't catch it (different ready_by).
	// The WHERE NOT EXISTS rejects the rebook when any active
	// row's computed lodger_until is past nextReadyBy. $7 carries
	// the nextReadyBy timestamp (start of the new night's coverage,
	// world-local midnight) so the comparison runs as a single
	// timestamptz.
	var newLedgerID int64
	err = tx.QueryRow(ctx,
		`INSERT INTO pay_ledger (
		    buyer_id, seller_id, item_kind, qty, offered_amount,
		    quoted_unit_amount, consume_now, state, message,
		    ready_by, fulfillment_status, delivered_on, resolved_at
		 )
		 SELECT $1::uuid, $2::uuid, 'nights_stay', $3, $4,
		        $5, false, 'accepted', 'engine-auto rebook',
		        $6::date, 'delivered', NOW(), NOW()
		  WHERE NOT EXISTS (
		      SELECT 1 FROM pay_ledger pl
		       WHERE pl.buyer_id  = $1::uuid
		         AND pl.seller_id = $2::uuid
		         AND pl.item_kind = 'nights_stay'
		         AND pl.state = 'accepted'
		         AND pl.fulfillment_status = 'delivered'
		         AND (
		             (
		                 (pl.ready_by + COALESCE(pl.qty, 1) * INTERVAL '1 day')::timestamp
		                 + (
		                     COALESCE(
		                         (SELECT value::int FROM setting WHERE key = 'lodging_check_out_hour'),
		                         11
		                     ) * INTERVAL '1 hour'
		                   )
		             ) AT TIME ZONE 'America/New_York'
		         ) > $7
		  )
		 ON CONFLICT (buyer_id, seller_id, ready_by)
		    WHERE item_kind = 'nights_stay'
		      AND state = 'accepted'
		      AND fulfillment_status = 'delivered'
		 DO NOTHING
		 RETURNING id`,
		c.BuyerID, c.SellerID, renewalQty, nightlyRate, unitAmount,
		nextReadyByDate, c.CurrentLodgerUntil,
	).Scan(&newLedgerID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Idempotent skip: either the WHERE NOT EXISTS guard
			// rejected (overlapping coverage exists) or ON CONFLICT
			// fired (the same buyer/seller/ready_by row landed
			// first via another path). Nothing to do.
			return nil
		}
		return fmt.Errorf("insert renewal row: %w", err)
	}

	// Debit the buyer. Mirror the pay.go pattern (UPDATE actor SET
	// coins = coins - $1) — the FOR UPDATE lock above plus the
	// pre-check makes this safe.
	if _, err := tx.Exec(ctx,
		`UPDATE actor SET coins = coins - $1 WHERE id = $2::uuid`,
		nightlyRate, c.BuyerID,
	); err != nil {
		return fmt.Errorf("debit buyer: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE actor SET coins = coins + $1 WHERE id = $2::uuid`,
		nightlyRate, c.SellerID,
	); err != nil {
		return fmt.Errorf("credit seller: %w", err)
	}

	// Extend (or grant) the buyer's bedroom for the new lodger_until.
	// Pre-fix this step was missing entirely, with two consequences:
	//
	//   1. A starter-seeded ledger (`message='ZBBS-WORK-204 starter'`,
	//      inserted directly as 'delivered' without going through
	//      executeDeliverOrder) never had a room_access row. The rebook
	//      INSERTed a fresh `delivered` ledger and inherited the no-room
	//      state — the buyer read as a lodger via isLodger (which queries
	//      pay_ledger directly) but had no physical bedroom binding.
	//      Observed 2026-05-12 with Jefferey at the Inn.
	//
	//   2. Even when the original deliver_order DID create a room_access
	//      row, expires_at stayed pinned to the FIRST night's
	//      lodger_until. After expireRoomAccess flipped active=false at
	//      that timestamp, the lodger had a still-valid pay_ledger
	//      covering night N+1 but no active room_access — auto-bed and
	//      pc_sleep gates fail.
	//
	// Two-step approach to keep the common case cheap and avoid an
	// unwanted teleport. The common rebook is "lodger already has an
	// active bedroom assignment, just push expires_at forward." We do
	// that as a single UPDATE on room_access — no actor.inside_room_id
	// write, no room pick.
	//
	// Only when the lodger has NO active private room_access do we
	// fall through to assignBedroomForLodger, which picks an available
	// bedroom and places the actor in it. That branch handles starter-
	// inheritance and edge cases where the room_access got swept stale.
	// assignBedroomForLodger's actor-placement UPDATE is correct for
	// the "never had a room, getting one now" semantic.
	checkOutHour, err := app.loadLodgingCheckOutHour(ctx)
	if err != nil {
		return fmt.Errorf("load checkout hour: %w", err)
	}
	newLodgerUntil := computeLodgerUntil(nextReadyByDate, renewalQty, checkOutHour, loc)
	extended, err := tx.Exec(ctx,
		`UPDATE room_access
		    SET expires_at = $1,
		        granted_via_ledger_id = $2,
		        granted_at = NOW()
		  WHERE actor_id = $3::uuid
		    AND kind = 'private'
		    AND active = true
		    AND room_id IN (
		        SELECT id FROM structure_room
		         WHERE structure_id = $4::uuid AND kind = 'private'
		    )`,
		newLodgerUntil, newLedgerID, c.BuyerID, c.StructureID,
	)
	if err != nil {
		return fmt.Errorf("extend room_access: %w", err)
	}
	if extended.RowsAffected() == 0 {
		// No active private room_access — starter-seeded path or a
		// row that got swept inactive between candidate-pick and now.
		// Fresh assignment, which also teleports the actor into the
		// assigned bedroom. The teleport is correct for the
		// "didn't have a room, now you do" semantic; in the firing
		// window (~5am local) most lodgers are asleep so this is a
		// no-op in practice.
		bedroomID, err := app.assignBedroomForLodger(ctx, tx, c.StructureID, c.BuyerID, newLedgerID, newLodgerUntil)
		if err != nil {
			if errors.Is(err, ErrNoPrivateRooms) {
				// Operator data error — structure tagged for lodging
				// but no bedrooms placed. Skip the rebook so the
				// existing ledger expires naturally; the prior pay_
				// ledger and (missing) room_access both lapse and
				// isLodger drops to false. Admin needs to add rooms
				// or remove the lodging tag before re-rebooking can
				// fire on this pair. Logged at high visibility so the
				// canary picks it up.
				log.Printf("auto-rebook: structure %s (%s) has zero private bedrooms — skipping renewal for %s @ %s; ledger %d not committed. Operator: add bedrooms or remove the lodging tag.",
					c.StructureLabel, c.StructureID, c.BuyerName, c.SellerName, newLedgerID)
				return nil
			}
			return fmt.Errorf("assign bedroom: %w", err)
		}
		if bedroomID == 0 {
			log.Printf("auto-rebook: no bedroom currently available at %s (%s) for %s — skipping renewal; ledger %d not committed",
				c.StructureLabel, c.StructureID, c.BuyerName, newLedgerID)
			return nil
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}

	// Post-commit narration so the keeper / lodger-side perception
	// has a recent-activity beat. Room-scoped via structure_id so
	// only co-located actors see the line, matching the rest of the
	// pay-flow narrations.
	app.Hub.Broadcast(WorldEvent{
		Type: "room_event",
		Data: map[string]interface{}{
			"actor_id":     c.BuyerID,
			"actor_name":   c.BuyerName,
			"kind":         "lodging-rebook",
			"text":         fmt.Sprintf("%s settled with %s for another night's lodging.", c.BuyerName, c.SellerName),
			"structure_id": c.StructureID,
			"at":           time.Now().UTC().Format(time.RFC3339),
		},
	})

	log.Printf("auto-rebook: %s renewed at %s (%s) for %d coins (ledger %d)",
		c.BuyerName, c.StructureLabel, c.SellerName, nightlyRate, newLedgerID)

	return nil
}

