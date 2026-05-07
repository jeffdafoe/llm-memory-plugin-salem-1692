package main

// Subspace primitive — first-class "rooms within a structure" with
// per-instance IDs and access control. Schema added by ZBBS-149.
//
// Why this exists: actor.inside_structure_id is a single-bit "am I in
// this building?" flag. NPC perception's co-presence query keys off
// this single bit, so a sleeping lodger upstairs at the Tavern is "here
// with John" and gets greeted while in bed. Real buildings have public
// common areas and private rooms; the engine has been forcing both
// into the same bucket. Subspaces fix that.
//
// Three concepts:
//
//   - structure_subspace: per-instance subspace declarations. The
//     Tavern has one 'common' (the bar/floor) plus N 'bedroom_*'
//     'private' subspaces. Other structures typically have just
//     'common'. Identity lives on the row id, separate from the name.
//
//   - subspace_access: who can enter a 'private' or 'staff' subspace.
//     Lodgers get an access row when deliver_order(nights_stay) flips
//     their fulfillment to 'delivered'. Common subspaces don't need
//     access rows — anyone can enter.
//
//   - actor.inside_subspace_id: which subspace the actor is currently
//     in. NULL when not inside any structure. Co-presence queries that
//     filter by inside_structure_id should also filter by this column.
//
// Helpers in this file:
//
//   - commonSubspaceForStructure: looks up the 'common' subspace id
//     for a structure (used during structure-entry transitions).
//
//   - canEnterSubspace: gates a /pc/move-subspace transition. Common
//     subspaces always allow; private requires an unexpired access
//     row; staff requires the actor's work_structure_id to match.
//
//   - assignBedroomForLodger: picks an available bedroom in the given
//     structure, creates a subspace_access row tied to the ledger,
//     and returns the bedroom's subspace id. Called from
//     executeDeliverOrder's nights_stay branch.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// commonSubspaceForStructure returns the id of the 'common' subspace
// for structureID, or 0 if the structure has no common subspace
// declared. The migration seeds 'common' for every structure that any
// actor is referenced against, so 0 should be rare in practice — it
// only happens if a brand-new structure was added post-migration
// without seeding. Treated as a soft failure: callers who pass NULL
// to inside_subspace_id leave the actor in a "structure but no
// subspace" state, which co-presence queries naturally exclude.
func (app *App) commonSubspaceForStructure(ctx context.Context, structureID string) (int64, error) {
	if structureID == "" {
		return 0, nil
	}
	var id int64
	err := app.DB.QueryRow(ctx,
		`SELECT id FROM structure_subspace
		  WHERE structure_id = $1::uuid AND kind = 'common'
		  LIMIT 1`,
		structureID,
	).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("commonSubspaceForStructure(%s): %w", structureID, err)
	}
	return id, nil
}

// canEnterSubspace reports whether actorID may enter subspaceID. Used
// by /pc/move-subspace before flipping inside_subspace_id.
//
//   - 'common': always allow.
//   - 'private': require an unexpired subspace_access row for this
//     (subspace, actor) pair.
//   - 'staff': require actor.work_structure_id to match the subspace's
//     parent structure. Future kinds add cases here.
//
// Errors propagate so callers can fail closed (default for movement
// gates: deny on doubt).
func (app *App) canEnterSubspace(ctx context.Context, actorID string, subspaceID int64) (bool, error) {
	if actorID == "" || subspaceID == 0 {
		return false, nil
	}
	var (
		kind        string
		structureID string
	)
	err := app.DB.QueryRow(ctx,
		`SELECT kind::text, structure_id::text FROM structure_subspace WHERE id = $1`,
		subspaceID,
	).Scan(&kind, &structureID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("canEnterSubspace lookup: %w", err)
	}
	switch kind {
	case "common":
		return true, nil
	case "private":
		var found bool
		if err := app.DB.QueryRow(ctx,
			`SELECT EXISTS (
				SELECT 1 FROM subspace_access
				 WHERE subspace_id = $1 AND actor_id = $2::uuid
				   AND (expires_at IS NULL OR expires_at > NOW())
			)`,
			subspaceID, actorID,
		).Scan(&found); err != nil {
			return false, fmt.Errorf("canEnterSubspace access check: %w", err)
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
			return false, fmt.Errorf("canEnterSubspace staff check: %w", err)
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
// "Available" = private subspace in the structure with no current
// subspace_access row OR all rows expired. Picks deterministically
// (by name ASC) so re-runs hit the same room when possible.
//
// Inserts a subspace_access row tied to ledgerID with expiresAt =
// lodger_until from the ledger formula (caller computes; see
// executeDeliverOrder). Updates actor.inside_subspace_id so the lodger
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
	// (2) Otherwise pick the first private subspace with no active
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
	var subspaceID int64
	err := tx.QueryRow(ctx,
		`(
		   SELECT ss.id
		     FROM structure_subspace ss
		     JOIN subspace_access sa ON sa.subspace_id = ss.id
		    WHERE ss.structure_id = $1::uuid
		      AND ss.kind = 'private'
		      AND sa.actor_id = $2::uuid
		      AND (sa.expires_at IS NULL OR sa.expires_at > NOW())
		      AND NOT EXISTS (
		        SELECT 1 FROM subspace_access other
		         WHERE other.subspace_id = ss.id
		           AND other.actor_id <> $2::uuid
		           AND (other.expires_at IS NULL OR other.expires_at > NOW())
		      )
		    ORDER BY ss.name ASC
		    LIMIT 1
		 )
		 UNION ALL
		 (
		   SELECT ss.id
		     FROM structure_subspace ss
		    WHERE ss.structure_id = $1::uuid
		      AND ss.kind = 'private'
		      AND NOT EXISTS (
		        SELECT 1 FROM subspace_access sa
		         WHERE sa.subspace_id = ss.id
		           AND (sa.expires_at IS NULL OR sa.expires_at > NOW())
		      )
		    ORDER BY ss.name ASC
		    FOR UPDATE SKIP LOCKED
		    LIMIT 1
		 )
		 LIMIT 1`,
		structureID, buyerID,
	).Scan(&subspaceID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("assignBedroomForLodger pick: %w", err)
	}

	// Insert access row. ON CONFLICT updates expiresAt + ledger so
	// re-checking-in (same lodger paying for another night before
	// the previous expires_at) extends rather than stacks.
	if _, err := tx.Exec(ctx,
		`INSERT INTO subspace_access (subspace_id, actor_id, granted_via_ledger_id, expires_at)
		 VALUES ($1, $2::uuid, $3, $4)
		 ON CONFLICT (subspace_id, actor_id)
		 DO UPDATE SET granted_via_ledger_id = EXCLUDED.granted_via_ledger_id,
		               expires_at = EXCLUDED.expires_at,
		               granted_at = NOW()`,
		subspaceID, buyerID, ledgerID, expiresAt,
	); err != nil {
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
		    SET inside_subspace_id = $1,
		        inside_structure_id = $2::uuid,
		        inside = true
		  WHERE id = $3::uuid`,
		subspaceID, structureID, buyerID,
	); err != nil {
		return 0, fmt.Errorf("assignBedroomForLodger place actor: %w", err)
	}

	return subspaceID, nil
}

// computeLodgerUntil mirrors lodging.go's isLodger SQL formula in Go,
// for callers (assignBedroomForLodger via executeDeliverOrder) that
// need the timestamp to stamp on subspace_access.expires_at.
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
