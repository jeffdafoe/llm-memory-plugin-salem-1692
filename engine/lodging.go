package main

// Lodging — lodger status, eviction exemption, door-lock pass.
//
// Stage A of the lodging design (`shared/tasks/lodging/design`). Adds
// query helpers that materialize lodger status from existing pay_ledger
// rows. No new schema beyond what ZBBS-131 ships (the nights_stay
// item_kind + lodging hour settings).
//
// Why this lives in its own file: lodger status is read from many
// places — eviction sequence (take_break-redesign callsite, not yet
// shipped), door-lock canEnter check (same), future admin/PC status
// surfaces, sleep-mechanic gating (stage B). Centralizing the query
// shape here keeps the SQL in one place so future tweaks (grace
// windows, new exemption rules, vendor-as-lodger nuances) don't
// require scattered edits.
//
// Lodger_until formula (locked 2026-05-06 with Jeff):
//
//   lodger_until = (ready_by + qty) at lodging_check_out_hour
//
// So a buyer who pays today for a 1-night stay (qty=1, ready_by=today)
// is a lodger from check-in until tomorrow at lodging_check_out_hour
// (default 11am). A 2-night stay extends to day-after-tomorrow at 11am.
// Future bookings (ready_by=3 days from now) are eligible from that
// date forward; the keeper checks them in via deliver_order, but
// lodger_until anchors to ready_by regardless of actual check-in time
// (real-hotel logic — late check-in still checks out at 11am).
//
// Timezone (ZBBS-151): lodging_check_out_hour is wall-clock in the
// world timezone (setting `world_timezone`, default America/New_York),
// matching dawn/dusk/world_rotation_time semantics. The SQL applies
// `AT TIME ZONE world_timezone` so the naive timestamp is interpreted
// as village wall-clock. The Go-side mirror in room.go's
// computeLodgerUntil uses cfg.Location and produces the same UTC
// instant for stamping room_access.expires_at.
//
// Conditional `ready` exemption (locked 2026-05-06 with Jeff,
// per home's review `6513a207`):
//
// fulfillment_status='delivered' always counts as lodger status.
// fulfillment_status='ready' counts ONLY when the keeper is unavailable
// (break_until > NOW() OR not at the structure). This avoids the
// deadlock where a keeper accepts payment then takes a break before
// calling deliver_order — the lodger gets auto-exemption when the
// keeper has abdicated, while preserving "the keeper does the check-in"
// semantics during normal operating hours.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// isLodger reports whether actorID currently holds lodger status at
// structureID. Single SQL query covering:
//   - matching pay_ledger row (state='accepted', item_kind='nights_stay')
//   - seller works at this structure (work_structure_id = structureID)
//   - fulfillment_status delivered, or ready when keeper is unavailable
//   - ready_by <= CURRENT_DATE (lodging period has begun)
//   - NOW() < lodger_until (period hasn't expired)
//
// Returns false on missing data — an actor with no nights_stay rows is
// not a lodger anywhere. Errors propagate so callers can distinguish
// "not a lodger" from "couldn't tell" (eviction filter wants to fail
// closed; door-lock check wants to fail open — caller's choice).
func (app *App) isLodger(ctx context.Context, actorID, structureID string) (bool, error) {
	if actorID == "" || structureID == "" {
		return false, nil
	}
	var found bool
	err := app.DB.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1
			  FROM pay_ledger pl
			  JOIN actor seller ON seller.id = pl.seller_id
			 WHERE pl.buyer_id = $1::uuid
			   AND seller.work_structure_id = $2::uuid
			   AND pl.item_kind = 'nights_stay'
			   AND pl.state = 'accepted'
			   AND (
			        pl.fulfillment_status = 'delivered'
			        OR (
			             pl.fulfillment_status = 'ready'
			             AND (
			                  seller.break_until > NOW()
			                  OR seller.inside_structure_id IS DISTINCT FROM $2::uuid
			                 )
			            )
			       )
			   AND pl.ready_by <= CURRENT_DATE
			   AND NOW() < (
			        (
			          (pl.ready_by + COALESCE(pl.qty, 1) * INTERVAL '1 day')::timestamp
			          + (
			              COALESCE(
			                (SELECT value::int FROM setting WHERE key = 'lodging_check_out_hour'),
			                11
			              ) * INTERVAL '1 hour'
			            )
			        ) AT TIME ZONE COALESCE(
			            (SELECT value FROM setting WHERE key = 'world_timezone'),
			            'America/New_York'
			        )
			       )
		)`,
		actorID, structureID,
	).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("isLodger query: %w", err)
	}
	return found, nil
}

// wouldBeEvictionExempt is the umbrella check used by take_break's
// eviction filter and by canEnter's door-lock pass. An actor is
// exempt if any of the following holds:
//
//   1. Their home_structure_id matches the structure (NPCs at home,
//      PCs whose home Jeff has manually assigned).
//   2. Their work_structure_id matches the structure (the keeper
//      themselves can't be evicted from their own shop; vendors who
//      share a structure get the same protection).
//   3. They hold active lodger status here (see isLodger).
//
// Returns false on missing input. Errors propagate.
//
// This is the single canonical exemption query — both eviction
// filtering and door-lock are expected to call this rather than
// checking individual conditions inline. Future exemption rules (e.g.
// "guests of the proprietor's family") add a new clause here.
func (app *App) wouldBeEvictionExempt(ctx context.Context, actorID, structureID string) (bool, error) {
	if actorID == "" || structureID == "" {
		return false, nil
	}
	// home / work check first — single-row read without a join.
	var (
		homeStruct sql.NullString
		workStruct sql.NullString
	)
	err := app.DB.QueryRow(ctx,
		`SELECT home_structure_id::text, work_structure_id::text
		   FROM actor WHERE id = $1::uuid`,
		actorID,
	).Scan(&homeStruct, &workStruct)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, fmt.Errorf("wouldBeEvictionExempt actor lookup: %w", err)
	}
	if homeStruct.Valid && homeStruct.String == structureID {
		return true, nil
	}
	if workStruct.Valid && workStruct.String == structureID {
		return true, nil
	}
	return app.isLodger(ctx, actorID, structureID)
}

// isStructureClosed reports whether structureID is currently closed
// because a vendor working there is on break. The query checks for any
// actor with work_structure_id=structureID AND break_until > NOW().
// Mirrors the door-knock narration query at pc_handlers.go.
//
// "Closed" in this sense is a derived state — no flag stored on the
// structure itself. When the vendor's break_until passes, the next
// canEnter call sees the row as no-longer-on-break and reports the
// structure as open. resetSleptTiredness and the world rotation don't
// affect this query.
func (app *App) isStructureClosed(ctx context.Context, structureID string) (bool, error) {
	if structureID == "" {
		return false, nil
	}
	var closed bool
	err := app.DB.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM actor
			 WHERE work_structure_id = $1::uuid
			   AND break_until IS NOT NULL
			   AND break_until > NOW()
		)`,
		structureID,
	).Scan(&closed)
	if err != nil {
		return false, fmt.Errorf("isStructureClosed query: %w", err)
	}
	return closed, nil
}

// activeLodgerEntry is the perception-shaped row: one buyer who is
// currently a checked-in lodger at this keeper's structure. Pre-check-in
// (fulfillment_status='ready') rows surface via readyOrdersForSeller in
// order_fulfillment.go; this companion covers the post-check-in window
// so the keeper can answer "how many nights have you paid for?" type
// questions without confabulating.
type activeLodgerEntry struct {
	BuyerName   string
	Qty         int
	ReadyBy     time.Time
	LodgerUntil time.Time
}

// activeLodgersForKeeper returns lodgers currently checked in to a
// structure where sellerID works. A lodger appears here when their
// pay_ledger row is state=accepted, fulfillment_status=delivered,
// item_kind=nights_stay, ready_by has arrived, and lodger_until hasn't
// passed.
//
// Sort order is lodger_until ASC so soonest-checkout reads first
// (matches "what's about to free up" mental model). Capped at 10 —
// Salem's Tavern has 4 bedrooms, generous headroom for future villages
// with multiple inns.
//
// Lodger_until expression mirrors isLodger; kept inline rather than
// extracted to avoid a SQL helper that's only used twice.
func (app *App) activeLodgersForKeeper(ctx context.Context, sellerID string) ([]activeLodgerEntry, error) {
	if sellerID == "" {
		return nil, nil
	}
	rows, err := app.DB.Query(ctx,
		`WITH active AS (
		    SELECT pl.buyer_id,
		           COALESCE(pl.qty, 1) AS qty,
		           pl.ready_by,
		           (
		               (
		                   (pl.ready_by + COALESCE(pl.qty, 1) * INTERVAL '1 day')::timestamp
		                   + (
		                       COALESCE(
		                           (SELECT value::int FROM setting WHERE key = 'lodging_check_out_hour'),
		                           11
		                       ) * INTERVAL '1 hour'
		                     )
		               ) AT TIME ZONE COALESCE(
		                   (SELECT value FROM setting WHERE key = 'world_timezone'),
		                   'America/New_York'
		               )
		           ) AS lodger_until
		      FROM pay_ledger pl
		     WHERE pl.seller_id = $1::uuid
		       AND pl.item_kind = 'nights_stay'
		       AND pl.state = 'accepted'
		       AND pl.fulfillment_status = 'delivered'
		       AND pl.ready_by <= CURRENT_DATE
		)
		SELECT a.display_name, active.qty, active.ready_by, active.lodger_until
		  FROM active
		  JOIN actor a ON a.id = active.buyer_id
		 WHERE NOW() < active.lodger_until
		 ORDER BY active.lodger_until ASC
		 LIMIT 10`,
		sellerID,
	)
	if err != nil {
		return nil, fmt.Errorf("query active lodgers: %w", err)
	}
	defer rows.Close()
	var out []activeLodgerEntry
	for rows.Next() {
		var e activeLodgerEntry
		if err := rows.Scan(&e.BuyerName, &e.Qty, &e.ReadyBy, &e.LodgerUntil); err != nil {
			return nil, fmt.Errorf("scan active lodger row: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active lodgers: %w", err)
	}
	return out, nil
}

// formatActiveLodgersForPerception renders the keeper's active lodgers
// as a perception block. Empty input returns "" so the caller can
// suppress the section entirely.
//
// loc is the world timezone (cfg.Location) so the checkout timestamp
// reads as wall-clock village time, matching dawn/dusk semantics the
// LLM already sees elsewhere in the perception. Format:
// "- Jefferey: 3-night stay — checks out Sat May 9 at 11:00".
func formatActiveLodgersForPerception(entries []activeLodgerEntry, loc *time.Location) string {
	if len(entries) == 0 {
		return ""
	}
	if loc == nil {
		loc = time.UTC
	}
	var b strings.Builder
	b.WriteString("Lodgers in your rooms:\n")
	for _, e := range entries {
		nightsPart := fmt.Sprintf("%d-night stay", e.Qty)
		if e.Qty <= 1 {
			nightsPart = "1-night stay"
		}
		checkout := e.LodgerUntil.In(loc).Format("Mon Jan 2 at 15:04")
		fmt.Fprintf(&b, "- %s: %s — checks out %s\n", e.BuyerName, nightsPart, checkout)
	}
	return strings.TrimRight(b.String(), "\n")
}

// canEnter is the single gate for an actor walking into a structure.
// Returns true when:
//
//   1. The structure is open (no vendor on break here), OR
//   2. The actor is exempt (home/work match, or active lodger).
//
// Errors propagate so callers can fail open or closed at their
// discretion. Movement handlers (setNPCInside for NPC arrival,
// handlePCMove for PC click-to-walk) consult this BEFORE flipping
// inside_structure_id so the closed-door semantic actually keeps
// people out.
func (app *App) canEnter(ctx context.Context, actorID, structureID string) (bool, error) {
	closed, err := app.isStructureClosed(ctx, structureID)
	if err != nil {
		return false, err
	}
	if !closed {
		return true, nil
	}
	return app.wouldBeEvictionExempt(ctx, actorID, structureID)
}
