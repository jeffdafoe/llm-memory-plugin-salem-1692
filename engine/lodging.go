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
// Timezone: lodging_check_out_hour is wall-clock in the world timezone
// (hardcoded America/New_York — single-world game, no need to plumb a
// setting). The SQL applies `AT TIME ZONE 'America/New_York'` so the
// naive timestamp is interpreted as village wall-clock. The Go-side
// mirror in room.go's computeLodgerUntil uses cfg.Location (also
// initialized to America/New_York in world_phase.go) and produces the
// same UTC instant for stamping room_access.expires_at. If the world
// ever needs to relocate, change `defaultTimezone` in world_phase.go
// AND the literal in these SQL queries — grep `America/New_York`.
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
	"log"
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
			        ) AT TIME ZONE 'America/New_York'
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

// isBusinessClosed reports whether structureID is currently closed
// for business — the canonical predicate. True when:
//
//   - structureID carries the 'business' tag (otherwise this concept
//     doesn't apply; non-business structures return false), AND
//   - no worker assigned to this structure is actively manning it,
//     where "actively manning" means inside_structure_id matches AND
//     break_until is not in the future AND sleeping_until is not in
//     the future.
//
// Single source of truth for "is this place currently operating for
// customers." Use this instead of inlining checks against break_until
// / sleeping_until / inside-counts — those can drift, this can't.
//
// canEnter (door-lock) calls into here so the door behavior matches
// the closed-business arrival narration. refreshStructureOccupancyState
// (visual current_state) still keeps its own COUNT-based path; future
// refactor could unify it here as well.
func (app *App) isBusinessClosed(ctx context.Context, structureID string) (bool, error) {
	if structureID == "" {
		return false, nil
	}
	var closed bool
	err := app.DB.QueryRow(ctx,
		`SELECT EXISTS (
		    SELECT 1 FROM village_object_tag t
		     WHERE t.object_id = $1::uuid AND t.tag = 'business'
		)
		AND NOT EXISTS (
		    SELECT 1 FROM actor a
		     WHERE a.work_structure_id   = $1::uuid
		       AND a.inside_structure_id = $1::uuid
		       AND (a.break_until    IS NULL OR a.break_until    <= NOW())
		       AND (a.sleeping_until IS NULL OR a.sleeping_until <= NOW())
		)`,
		structureID,
	).Scan(&closed)
	if err != nil {
		return false, fmt.Errorf("isBusinessClosed query: %w", err)
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
	// Engine-granted starter rows (the ZBBS-WORK-204 migration's
	// grandfather grants and handlePCCreate's day-one comp) are
	// deliberately excluded from the keeper's perception. They keep
	// their effect on isLodger / canEnter / loadLodgerSelfStatus so
	// the PC still walks through doors and counts as a lodger for
	// sleep + eviction; they just don't surface to the keeper as
	// paying customers, since the keeper would otherwise volunteer
	// "you're all set upstairs" to a guest who never booked. Once a
	// PC pays the keeper for a real stay, that row carries a
	// non-starter message and shows up here normally.
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
		               ) AT TIME ZONE 'America/New_York'
		           ) AS lodger_until
		      FROM pay_ledger pl
		     WHERE pl.seller_id = $1::uuid
		       AND pl.item_kind = 'nights_stay'
		       AND pl.state = 'accepted'
		       AND pl.fulfillment_status = 'delivered'
		       AND pl.ready_by <= CURRENT_DATE
		       AND COALESCE(pl.message, '') NOT IN ('ZBBS-WORK-204 starter', 'pc-create starter')
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
//   1. The structure is open (operational keeper inside, see
//      isBusinessClosed), OR
//   2. The actor is exempt (home/work match, or active lodger).
//
// Errors propagate so callers can fail open or closed at their
// discretion. Movement handlers (setNPCInside for NPC arrival,
// handlePCMove for PC click-to-walk) consult this BEFORE flipping
// inside_structure_id so the closed-door semantic actually keeps
// people out.
//
// Pre-ZBBS-183 this used a narrower break_until-only predicate that
// diverged from the closed-business arrival narration's isBusinessClosed
// check: the narration would say "It is closed" while the door still
// let the PC walk in whenever the keeper was merely absent or asleep
// rather than on a formal break. Unified on isBusinessClosed so the
// door matches the narration.
func (app *App) canEnter(ctx context.Context, actorID, structureID string) (bool, error) {
	closed, err := app.isBusinessClosed(ctx, structureID)
	if err != nil {
		return false, err
	}
	if !closed {
		return true, nil
	}
	return app.wouldBeEvictionExempt(ctx, actorID, structureID)
}

// lodgerSelfStatus is the lodger-side mirror of activeLodgerEntry: the
// actor's own current lodger row (if any), shaped for the lodger
// perception cue. Used by the lodger-side block in buildAgentPerception
// so the boarder sees their own paid-through window with the same
// authoritative shape the keeper sees on their side.
type lodgerSelfStatus struct {
	StructureID    string
	StructureLabel string
	KeeperName     string
	LodgerUntil    time.Time
}

// loadLodgerSelfStatus returns the actor's current lodger row, picking
// the latest-expiring active row when multiple exist (e.g. an
// engine-auto rebook landed while the prior week's row is still
// counted as active). Returns ok=false when the actor has no active
// lodger row anywhere — most NPCs (non-boarders) hit this path.
//
// The structure label is COALESCE(display_name, asset.name) — same
// resolution as the perception's existing home/work labels, so a
// boarder's "Your room at the Inn" reads consistently with their
// identity-recap section.
func (app *App) loadLodgerSelfStatus(ctx context.Context, actorID string) (lodgerSelfStatus, bool) {
	var s lodgerSelfStatus
	// Same active-window predicate as isLodger / activeLodgersForKeeper:
	// ready_by <= CURRENT_DATE AND NOW() < lodger_until. Keeping the
	// freshness check in SQL avoids a Go-side host-clock vs. DB
	// timezone mismatch — all three sites compute lodger_until from
	// the same setting and timezone literal, so consistency is the
	// invariant we rely on.
	err := app.DB.QueryRow(ctx,
		`SELECT seller.work_structure_id::text,
		        COALESCE(o.display_name, a.name),
		        seller.display_name,
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
		   JOIN actor seller ON seller.id = pl.seller_id
		   JOIN village_object o ON o.id = seller.work_structure_id
		   JOIN asset a ON a.id = o.asset_id
		  WHERE pl.buyer_id = $1::uuid
		    AND pl.item_kind = 'nights_stay'
		    AND pl.state = 'accepted'
		    AND pl.fulfillment_status = 'delivered'
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
		        ) AT TIME ZONE 'America/New_York'
		    )
		  ORDER BY lodger_until DESC
		  LIMIT 1`,
		actorID,
	).Scan(&s.StructureID, &s.StructureLabel, &s.KeeperName, &s.LodgerUntil)
	if err != nil {
		if err != sql.ErrNoRows {
			log.Printf("loadLodgerSelfStatus(%s): %v", actorID, err)
		}
		return lodgerSelfStatus{}, false
	}
	return s, true
}

// formatLodgerSelfPerception renders the boarder's own paid-through
// status. Always-on while the actor is a lodger; escalates inside
// the 48h pre-expiry window, urgent on the calendar day the lease
// actually ends.
//
// "Today" framing is anchored to the calendar date in loc, not a
// rolling 24h delta — a freshly-checked-in 1-night lodger whose
// expiry lands tomorrow at 11:00 should read as "expires Sunday at
// 11:00", not "expires today" simply because they're <24h away.
//
// loc is the world timezone (cfg.Location) so day-of-week framing
// reads as wall-clock village time.
func formatLodgerSelfPerception(s lodgerSelfStatus, now time.Time, loc *time.Location) string {
	if loc == nil {
		loc = time.UTC
	}
	// Defensive: an expired status row shouldn't surface as
	// "expires today at 11:00" or fall into the 48h band (which
	// matches any past timestamp). loadLodgerSelfStatus filters
	// these in SQL, but keeping the formatter robust means callers
	// who synthesize a lodgerSelfStatus directly (tests, future
	// admin paths) get an empty section rather than a wrong cue.
	if !s.LodgerUntil.After(now) {
		return ""
	}
	until := s.LodgerUntil.In(loc)
	nowLocal := now.In(loc)
	sameDay := until.Year() == nowLocal.Year() &&
		until.YearDay() == nowLocal.YearDay()
	remaining := until.Sub(now)
	switch {
	case sameDay:
		return fmt.Sprintf(
			"Your room at %s expires today at %s — see %s before then to renew, or your boarding ends.",
			s.StructureLabel, until.Format("15:04"), s.KeeperName,
		)
	case remaining <= 48*time.Hour:
		return fmt.Sprintf(
			"Your room at %s expires %s. Find %s soon to arrange another week.",
			s.StructureLabel, until.Format("Monday at 15:04"), s.KeeperName,
		)
	default:
		return fmt.Sprintf(
			"Your room at %s is paid through %s.",
			s.StructureLabel, until.Format("Monday"),
		)
	}
}

// roomsAvailableAtStructure returns (available, total) bedroom counts
// at structureID, where "available" is private rooms with no active
// room_access row (matches assignBedroomForLodger's vacancy gate).
// Both zero when the structure has no private rooms placed —
// caller suppresses the perception line in that case.
//
// Counts are point-in-time at query; the auto-bed sweep and the
// expireRoomAccess sweep both run on the same minute cadence as the
// keeper's perception build, so a boarder who just checked out frees
// their room before the next keeper tick reads it.
func (app *App) roomsAvailableAtStructure(ctx context.Context, structureID string) (available, total int, err error) {
	if structureID == "" {
		return 0, 0, nil
	}
	err = app.DB.QueryRow(ctx,
		`SELECT
		   (SELECT COUNT(*) FROM structure_room
		     WHERE structure_id = $1::uuid AND kind = 'private')
		     - (SELECT COUNT(*) FROM structure_room sr
		         JOIN room_access sa ON sa.room_id = sr.id
		        WHERE sr.structure_id = $1::uuid
		          AND sr.kind = 'private'
		          AND sa.active = true),
		   (SELECT COUNT(*) FROM structure_room
		     WHERE structure_id = $1::uuid AND kind = 'private')`,
		structureID,
	).Scan(&available, &total)
	if err != nil {
		return 0, 0, fmt.Errorf("roomsAvailableAtStructure: %w", err)
	}
	if available < 0 {
		// Defensive: a stale active=true row outside the structure_room
		// inner-join shape could underflow the subtraction. Clamp to 0.
		available = 0
	}
	return available, total, nil
}

// formatKeeperVendorPerception renders the rooms-available block
// (and, for salem-vendor-backed keepers, the standing rate +
// vendor flavor paragraphs) that anchor the keeper's per-tick role
// context. structureLabel matches the work-structure label already
// shown in identity-recap so the lines read consistently.
//
// Salem-vendor keepers (Hannah Boggs and any future shopkeepers
// using the shared VA) get the full block — their generic startup
// prompt doesn't carry per-keeper pricing wisdom, so the engine
// injects a rate anchor and a flavor paragraph each tick. Bespoke
// role-overlay keepers (John Ellis the tavernkeeper, who has his
// own pricing-flexibility instructions) only see the occupancy
// line; their pricing logic stays in their attribute_definition.
//
// Empty when the actor has no private rooms (not a keeper of an
// inn-shaped structure) — caller suppresses the section.
func formatKeeperVendorPerception(available, total, weeklyRate int, structureLabel, vendorFlavor, llmMemoryAgent string) string {
	if total <= 0 {
		return ""
	}
	occupied := total - available
	subject := "Your inn"
	if structureLabel != "" {
		subject = structureLabel
	}
	roomsLine := fmt.Sprintf(
		"%s has %d bedroom%s; %d occupied tonight, %d available.",
		subject, total, pluralS(total), occupied, available,
	)
	if llmMemoryAgent != "salem-vendor" {
		return roomsLine
	}
	perNight := weeklyRate / 7
	rateLine := fmt.Sprintf(
		"Your standing rate is around %d coins per week (%d per night), haggle-able based on occupancy and the customer.",
		weeklyRate, perNight,
	)
	out := roomsLine + "\n" + rateLine
	if strings.TrimSpace(vendorFlavor) != "" {
		out += "\n\n" + strings.TrimSpace(vendorFlavor)
	}
	return out
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
