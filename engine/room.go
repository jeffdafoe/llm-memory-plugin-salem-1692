package main

// Room primitive — first-class "rooms within a structure" with
// per-instance IDs and access control. Schema added by ZBBS-149.
//
// Why this exists: actor.inside_structure_id is a single-bit "am I in
// this building?" flag. NPC perception's co-presence query keys off
// this single bit, so a sleeping lodger upstairs at the Tavern is "here
// with John" and gets greeted while in bed. Real buildings have public
// common areas and private rooms; the engine has been forcing both
// into the same bucket. Rooms fix that.
//
// Three concepts:
//
//   - structure_room: per-instance room declarations. The
//     Tavern has one 'common' (the bar/floor) plus N 'bedroom_*'
//     'private' rooms. Other structures typically have just
//     'common'. Identity lives on the row id, separate from the name.
//
//   - room_access: who can enter a 'private' or 'staff' room.
//     Lodgers get an access row when deliver_order(nights_stay) flips
//     their fulfillment to 'delivered'. Common rooms don't need
//     access rows — anyone can enter.
//
//   - actor.inside_room_id: which room the actor is currently
//     in. NULL when not inside any structure. Co-presence queries that
//     filter by inside_structure_id should also filter by this column.
//
// Helpers in this file:
//
//   - commonRoomForStructure: looks up the 'common' room id
//     for a structure (used during structure-entry transitions).
//
//   - canEnterRoom: gates a /pc/move-room transition. Common
//     rooms always allow; private requires an unexpired access
//     row; staff requires the actor's work_structure_id to match.
//
//   - assignBedroomForLodger: picks an available bedroom in the given
//     structure, creates a room_access row tied to the ledger,
//     and returns the bedroom's room id. Called from
//     executeDeliverOrder's nights_stay branch.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// commonRoomForStructure returns the id of the 'common' room
// for structureID, or 0 if the structure has no common room
// declared. The migration seeds 'common' for every structure that any
// actor is referenced against, so 0 should be rare in practice — it
// only happens if a brand-new structure was added post-migration
// without seeding. Treated as a soft failure: callers who pass NULL
// to inside_room_id leave the actor in a "structure but no
// room" state, which co-presence queries naturally exclude.
func (app *App) commonRoomForStructure(ctx context.Context, structureID string) (int64, error) {
	if structureID == "" {
		return 0, nil
	}
	var id int64
	err := app.DB.QueryRow(ctx,
		`SELECT id FROM structure_room
		  WHERE structure_id = $1::uuid AND kind = 'common'
		  LIMIT 1`,
		structureID,
	).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("commonRoomForStructure(%s): %w", structureID, err)
	}
	return id, nil
}

// canEnterRoom reports whether actorID may enter roomID. Used
// by /pc/move-room before flipping inside_room_id.
//
//   - 'common': always allow.
//   - 'private': require an unexpired room_access row for this
//     (room, actor) pair.
//   - 'staff': require actor.work_structure_id to match the room's
//     parent structure. Future kinds add cases here.
//
// Errors propagate so callers can fail closed (default for movement
// gates: deny on doubt).
func (app *App) canEnterRoom(ctx context.Context, actorID string, roomID int64) (bool, error) {
	if actorID == "" || roomID == 0 {
		return false, nil
	}
	var (
		kind        string
		structureID string
	)
	err := app.DB.QueryRow(ctx,
		`SELECT kind::text, structure_id::text FROM structure_room WHERE id = $1`,
		roomID,
	).Scan(&kind, &structureID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("canEnterRoom lookup: %w", err)
	}
	switch kind {
	case "common":
		return true, nil
	case "private":
		// ZBBS-163: gate on active=true. The expireRoomAccess sweep
		// flips active=false on expired rows; this query stays in
		// lockstep with the unique index so a private room that
		// expired but hasn't been flipped yet reads as still-valid
		// for up to one sweep cadence (~1 min). Acceptable real-world
		// hotel slack.
		var found bool
		if err := app.DB.QueryRow(ctx,
			`SELECT EXISTS (
				SELECT 1 FROM room_access
				 WHERE room_id = $1 AND actor_id = $2::uuid
				   AND active = true
			)`,
			roomID, actorID,
		).Scan(&found); err != nil {
			return false, fmt.Errorf("canEnterRoom access check: %w", err)
		}
		return found, nil
	case "staff":
		// COALESCE so a NULL work_structure_id reads as "not staff here"
		// rather than scanning NULL into bool (pgx errors that path).
		var matches bool
		if err := app.DB.QueryRow(ctx,
			`SELECT COALESCE(work_structure_id::text = $2, false)
			   FROM actor WHERE id = $1::uuid`,
			actorID, structureID,
		).Scan(&matches); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return false, nil
			}
			return false, fmt.Errorf("canEnterRoom staff check: %w", err)
		}
		return matches, nil
	default:
		// Unknown kind — fail closed.
		return false, nil
	}
}

// assignBedroomForLodger picks an available bedroom in structureID and
// grants access to buyerID. Called from executeDeliverOrder's
// nights_stay (service capability) branch inside its transaction.
//
// "Available" = private room in the structure with no current
// room_access row OR all rows expired. Picks deterministically
// (by name ASC) so re-runs hit the same room when possible.
//
// Inserts a room_access row tied to ledgerID with expiresAt =
// lodger_until from the ledger formula (caller computes; see
// executeDeliverOrder). Updates actor.inside_room_id so the lodger
// is immediately upstairs after the keeper's deliver_order.
//
// Returns 0 when no bedroom is available — caller should treat this
// as a delivery rejection ("All rooms taken — sorry, traveler.").
func (app *App) assignBedroomForLodger(
	ctx context.Context,
	tx pgx.Tx,
	structureID, buyerID string,
	ledgerID int64,
	expiresAt time.Time,
) (int64, error) {
	if structureID == "" || buyerID == "" {
		return 0, fmt.Errorf("assignBedroomForLodger: missing structureID or buyerID")
	}

	// Two-step pick (UNION ALL with LIMIT 1):
	//
	// (1) Prefer the buyer's existing active access row in this
	//     structure. Re-paying for another night before the prior
	//     expires_at fires hits ON CONFLICT below and just extends
	//     expires_at on the SAME bedroom, no room-hopping mid-stay.
	//
	// (2) Otherwise pick the first private room with no active
	//     access row, locking the row with FOR UPDATE SKIP LOCKED so
	//     two concurrent deliver_order calls can't claim the same
	//     bedroom. The lock is released on tx commit/rollback;
	//     SKIP LOCKED means the second caller sees the next available
	//     instead of blocking.
	//
	// Postgres can't FOR UPDATE on the result of a UNION directly, so
	// the locking subquery is the second branch only — the first
	// branch is the buyer's own existing row, which doesn't need
	// locking (only this buyer is affected by their own ON CONFLICT
	// extension).
	// First branch's NOT EXISTS guard rejects "extend my existing room"
	// when a different active access row already shares the room — a
	// state that shouldn't occur in steady state but can exist
	// transiently or via legacy bad data. Falling through to branch (2)
	// gets the buyer a clean vacant room instead of cementing the
	// double-occupancy.
	// ZBBS-163: pick + NOT EXISTS guards key off active=true to match
	// the partial unique index (ux_room_access_one_private_active).
	// expires_at remains the data of record but isn't consulted at
	// runtime here — the expireRoomAccess sweep keeps active in sync.
	// Picking a room based on a stale (active=true but expired) row
	// would force a unique-index conflict on insert; using active
	// directly avoids the divergence.
	var roomID int64
	err := tx.QueryRow(ctx,
		`(
		   SELECT ss.id
		     FROM structure_room ss
		     JOIN room_access sa ON sa.room_id = ss.id
		    WHERE ss.structure_id = $1::uuid
		      AND ss.kind = 'private'
		      AND sa.actor_id = $2::uuid
		      AND sa.active = true
		      AND NOT EXISTS (
		        SELECT 1 FROM room_access other
		         WHERE other.room_id = ss.id
		           AND other.actor_id <> $2::uuid
		           AND other.active = true
		      )
		    ORDER BY ss.name ASC
		    LIMIT 1
		 )
		 UNION ALL
		 (
		   SELECT ss.id
		     FROM structure_room ss
		    WHERE ss.structure_id = $1::uuid
		      AND ss.kind = 'private'
		      AND NOT EXISTS (
		        SELECT 1 FROM room_access sa
		         WHERE sa.room_id = ss.id
		           AND sa.active = true
		      )
		    ORDER BY ss.name ASC
		    FOR UPDATE SKIP LOCKED
		    LIMIT 1
		 )
		 LIMIT 1`,
		structureID, buyerID,
	).Scan(&roomID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("assignBedroomForLodger pick: %w", err)
	}

	// Defensive: refuse to grant active access with an already-passed
	// expires_at. Current callers compute expiresAt from
	// computeLodgerUntil(readyBy + qty days), which lands in the
	// future for any normal booking; but if a caller ever supplies a
	// past timestamp the row would be active=true with no time left,
	// and runtime gates (active-only) would treat it as valid until
	// the next expireRoomAccess sweep. CHECK constraints can't use
	// NOW(), so this is the practical guard.
	if !expiresAt.IsZero() && !expiresAt.After(time.Now()) {
		return 0, fmt.Errorf("assignBedroomForLodger: expiresAt %s is not in the future", expiresAt.Format(time.RFC3339))
	}

	// Insert access row. ON CONFLICT updates expiresAt + ledger so
	// re-checking-in (same lodger paying for another night before
	// the previous expires_at) extends rather than stacks. ZBBS-163:
	// also re-flips active=true on UPSERT so a re-book after expiry
	// reactivates the same row instead of leaving it stale; kind is
	// re-stamped from EXCLUDED.kind so a legacy row with stale/wrong
	// kind gets corrected to match the room — keeps the row aligned
	// with ux_room_access_one_private_active.
	//
	// Branch-1 race guard: if a concurrent transaction granted a
	// different actor active access to the same room between our
	// pick and this insert, the unique index rejects with 23505.
	// Treat as "no room available right now" (return 0) — caller in
	// executeDeliverOrder surfaces that as a delivery rejection,
	// matching the existing behavior when no room was available at
	// pick time. Lossy under contention (the racing actor wins; we
	// retry on next deliver_order), acceptable at Salem's scale.
	if _, err := tx.Exec(ctx,
		`INSERT INTO room_access (room_id, actor_id, granted_via_ledger_id, expires_at, kind, active)
		 VALUES ($1, $2::uuid, $3, $4, 'private', true)
		 ON CONFLICT (room_id, actor_id)
		 DO UPDATE SET granted_via_ledger_id = EXCLUDED.granted_via_ledger_id,
		               expires_at = EXCLUDED.expires_at,
		               granted_at = NOW(),
		               kind = EXCLUDED.kind,
		               active = true`,
		roomID, buyerID, ledgerID, expiresAt,
	); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "ux_room_access_one_private_active" {
			return 0, nil
		}
		return 0, fmt.Errorf("assignBedroomForLodger insert access: %w", err)
	}

	// Place the lodger in the bedroom. Their inside_structure_id
	// should already be the Tavern (deliver_order's co-location gate
	// ensures buyer is in the same structure as seller for non-
	// service items, but service items skip that gate — the buyer
	// could in principle be elsewhere when deliver_order fires.
	// Place them in the bedroom regardless; the act of "being
	// checked in" implies they're now in their room).
	if _, err := tx.Exec(ctx,
		`UPDATE actor
		    SET inside_room_id = $1,
		        inside_structure_id = $2::uuid,
		        inside = true
		  WHERE id = $3::uuid`,
		roomID, structureID, buyerID,
	); err != nil {
		return 0, fmt.Errorf("assignBedroomForLodger place actor: %w", err)
	}

	return roomID, nil
}

// computeLodgerUntil mirrors lodging.go's isLodger SQL formula in Go,
// for callers (assignBedroomForLodger via executeDeliverOrder) that
// need the timestamp to stamp on room_access.expires_at.
//
// Formula: (ready_by + qty days) at lodging_check_out_hour, interpreted
// as wall-clock in the world timezone (ZBBS-151). The returned
// time.Time carries loc so when pgx binds it as a timestamptz the
// stored UTC instant matches isLodger's `AT TIME ZONE` SQL expression
// using the same world_timezone setting.
func computeLodgerUntil(readyBy time.Time, qty int, checkOutHour int, loc *time.Location) time.Time {
	if qty < 1 {
		qty = 1
	}
	if loc == nil {
		loc = time.UTC
	}
	d := readyBy.AddDate(0, 0, qty)
	return time.Date(d.Year(), d.Month(), d.Day(), checkOutHour, 0, 0, 0, loc)
}

// computeEarliestCheckIn returns the earliest wall-clock instant a
// nights_stay can be checked in: ready_by at lodging_check_in_hour,
// interpreted in the world timezone. Real-hotel semantics — pay any
// time, but the room isn't ready until check-in hour on the booked
// date. Used by executeDeliverOrder's nights_stay branch to gate the
// transition from "paid" to "checked in".
//
// readyBy is the buyer-specified check-in date (defaults to today on
// same-day stays). For a future booking (ready_by=3 days from now),
// the earliest check-in is that future date at check-in hour, so the
// gate also blocks "I'll arrive early" attempts on advance bookings.
func computeEarliestCheckIn(readyBy time.Time, checkInHour int, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	return time.Date(readyBy.Year(), readyBy.Month(), readyBy.Day(), checkInHour, 0, 0, 0, loc)
}

// loadLodgingCheckInHour reads the lodging_check_in_hour setting,
// defaulting to 15 (3pm) when unset. Same shape as
// loadLodgingCheckOutHour — cheap single-row read, range-validated so a
// typo'd setting can't silently roll into a wrong-day check-in.
func (app *App) loadLodgingCheckInHour(ctx context.Context) (int, error) {
	var raw sql.NullString
	if err := app.DB.QueryRow(ctx,
		`SELECT value FROM setting WHERE key = 'lodging_check_in_hour'`,
	).Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 15, nil
		}
		return 0, fmt.Errorf("load lodging_check_in_hour: %w", err)
	}
	if !raw.Valid {
		return 15, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw.String))
	if err != nil {
		return 0, fmt.Errorf("parse lodging_check_in_hour %q: %w", raw.String, err)
	}
	if n < 0 || n > 23 {
		return 0, fmt.Errorf("lodging_check_in_hour out of range [0,23]: %d", n)
	}
	return n, nil
}

// loadLodgingCheckOutHour reads the lodging_check_out_hour setting,
// defaulting to 11 when unset. Cheap single-row read; no caching since
// the setting changes rarely and the read happens once per
// deliver_order(nights_stay) call. Validates the range so a typo'd
// setting (25, -1) doesn't silently get normalized by time.Date into
// a wrong-day check-out.
func (app *App) loadLodgingCheckOutHour(ctx context.Context) (int, error) {
	var raw sql.NullString
	if err := app.DB.QueryRow(ctx,
		`SELECT value FROM setting WHERE key = 'lodging_check_out_hour'`,
	).Scan(&raw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 11, nil
		}
		return 0, fmt.Errorf("load lodging_check_out_hour: %w", err)
	}
	if !raw.Valid {
		return 11, nil
	}
	// strconv.Atoi rejects trailing junk ("11abc" — fmt.Sscanf accepts
	// it as 11, which would silently honour partly-broken settings).
	n, err := strconv.Atoi(strings.TrimSpace(raw.String))
	if err != nil {
		return 0, fmt.Errorf("parse lodging_check_out_hour %q: %w", raw.String, err)
	}
	if n < 0 || n > 23 {
		return 0, fmt.Errorf("lodging_check_out_hour out of range [0,23]: %d", n)
	}
	return n, nil
}

// expireRoomAccess flips active=false on room_access rows whose
// expires_at has passed. Maintains the partial unique index
// (ux_room_access_one_private_active) — the index is the DB-level
// "one active occupant per private room" invariant, and it can only
// observe the active flag (NOW() isn't IMMUTABLE so it can't appear
// in an index predicate). The sweep is what bridges expires_at to
// active.
//
// Idempotent and cheap: filtered to rows where active is still true
// AND expires_at has passed, single batch UPDATE. Runs every server-
// tick interval (default 60s) from runSleepSweep, before
// wakeExpiredSleepers and autoBedIdleLodgers — so by the time those
// downstream sweeps run, rooms freed this minute are already
// available to new lodgers via assignBedroomForLodger's NOT EXISTS.
//
// Effect window: in the gap between expires_at passing and this
// sweep firing, downstream queries (canEnterRoom, autoBedIdleLodgers,
// pc_sleep gate, assignBedroomForLodger pick) read active=true and
// treat the row as still-valid. Up to one tick interval. Acceptable
// real-world hotel slack — a lodger doesn't get kicked out at
// 11:00:00 sharp; sometime in the next minute is fine.
//
// Wake timing is unaffected: wakeExpiredSleepers reads expires_at
// directly, so a sleeping PC's housekeeping wake fires on its own
// schedule independent of this sweep.
func (app *App) expireRoomAccess(ctx context.Context) error {
	_, err := app.DB.Exec(ctx,
		`UPDATE room_access
		    SET active = false
		  WHERE active = true
		    AND expires_at IS NOT NULL
		    AND expires_at <= NOW()`,
	)
	if err != nil {
		return fmt.Errorf("expireRoomAccess: %w", err)
	}
	return nil
}

// evictExpiredOccupants moves PCs whose private room access lapsed back
// to a common room of the same structure. Pairs with expireRoomAccess:
// flipping active=false in the room_access row only revokes the future
// gate — it doesn't physically relocate someone already inside.
//
// Pre-2026-05-08 a checked-out lodger was stuck: still in inside_room_id
// pointing at a bedroom with active=false access, no auto-bed (gated on
// active=true), no recovery unless they walked out manually. wake-on-
// expiry handled the sleeping case (housekeeping knock) but the awake
// case had no path. This sweep closes the gap by teleporting the PC to
// the structure's common room — non-common rooms are virtual subspaces,
// not walkable, so a state flip is the right primitive.
//
// Restricted to PCs and private rooms:
//   - login_username IS NOT NULL — NPC owners and staff hold permanent
//     access via different mechanisms (work_structure_id for staff, no
//     room_access expiry for owned bedrooms); evicting them would
//     surprise behavior. Lodging is a PC use case today.
//   - sr.kind = 'private' — staff-room access is checked against
//     work_structure_id, not room_access; nothing to expire.
//
// Each evictee gets a private brown-box narration ("Your stay has
// ended...") so the player sees what happened. A wake (if they were
// sleeping) fires earlier in the sweep via wakeExpiredSleepers, so by
// the time this runs the PC is already awake.
func (app *App) evictExpiredOccupants(ctx context.Context) error {
	rows, err := app.DB.Query(ctx, `
		SELECT a.id::text, COALESCE(a.display_name, '')
		  FROM actor a
		  JOIN structure_room sr ON sr.id = a.inside_room_id
		 WHERE a.inside_room_id IS NOT NULL
		   AND a.login_username IS NOT NULL
		   AND sr.kind = 'private'
		   AND NOT EXISTS (
		     SELECT 1 FROM room_access ra
		      WHERE ra.actor_id = a.id
		        AND ra.room_id = a.inside_room_id
		        AND ra.active = true
		   )
	`)
	if err != nil {
		return fmt.Errorf("evictExpiredOccupants query: %w", err)
	}
	type evictee struct {
		ID   string
		Name string
	}
	var toEvict []evictee
	for rows.Next() {
		var e evictee
		if err := rows.Scan(&e.ID, &e.Name); err != nil {
			rows.Close()
			return fmt.Errorf("evictExpiredOccupants scan: %w", err)
		}
		toEvict = append(toEvict, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("evictExpiredOccupants iter: %w", err)
	}

	for _, e := range toEvict {
		moveResult, err := app.executePCMoveRoom(ctx, e.ID, "common")
		if err != nil {
			log.Printf("evict move(%s): %v", e.ID, err)
			continue
		}
		if moveResult.Result != "ok" {
			// Race: PC left the structure / common room missing / etc.
			// Surface in logs, skip narration. They'll be re-evaluated
			// next sweep if still eligible, but the typical "common room
			// missing" case is permanent — not worth a recurring log.
			log.Printf("evict move(%s) rejected: %s", e.ID, moveResult.Err)
			continue
		}
		app.Hub.Broadcast(WorldEvent{
			Type: "room_event",
			Data: map[string]interface{}{
				"actor_id":   e.ID,
				"actor_name": e.Name,
				"kind":       "checkout",
				"text":       "Your stay has ended — you head down to the common area.",
				"private":    true,
				"at":         time.Now().UTC().Format(time.RFC3339),
			},
		})
	}
	return nil
}
