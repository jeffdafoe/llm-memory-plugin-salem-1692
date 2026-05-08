package main

// Closed-business arrival narration (ZBBS-179).
//
// When an actor arrives at a 'business'-tagged structure that is
// currently closed (no operational worker — see isBusinessClosed),
// surface a short second-person line in the PC's brown panel:
//
//   "You arrive at General Store. It is closed."
//
// PC-only in v1: the room_event broadcast goes to the player's
// connected client and renders in the talk panel's private narration
// area. NPCs see no narration today; an NPC perception block is a
// follow-up if the LLM-driven NPCs need closed-state surfacing
// beyond what's already in their perception via current_state.
//
// Skip when the arriver is the proprietor (their own shop) or has
// active room_access for the structure (paid lodger entering an
// inn-tagged-business). Both cases would be confusing — "arrive at
// your own closed shop" reads as friction the player created.

import (
	"context"
	"fmt"
	"log"
	"time"
)

// maybeNarrateClosedBusinessArrival is called from applyArrivalSideEffects
// after the arrival commits. Cheap no-op for non-PCs and for arrivals
// at non-business structures.
func (app *App) maybeNarrateClosedBusinessArrival(ctx context.Context, actorID string) {
	if actorID == "" {
		return
	}

	// Single round-trip: pull the arriver's PC status, the structure
	// they just arrived at (if any), the structure's display name, and
	// whether the arriver works there or has active room_access. Any
	// missing piece short-circuits.
	var (
		isPC               bool
		structureID        string
		structureName      string
		arriverIsKeeper    bool
		arriverHasRoom     bool
	)
	err := app.DB.QueryRow(ctx,
		`SELECT
		     a.login_username IS NOT NULL,
		     COALESCE(a.inside_structure_id::text, ''),
		     COALESCE(vo.display_name, asset.name, ''),
		     a.work_structure_id = a.inside_structure_id,
		     EXISTS (
		         SELECT 1 FROM room_access ra
		           JOIN structure_room sr ON sr.id = ra.room_id
		          WHERE ra.actor_id = a.id
		            AND ra.active = true
		            AND sr.structure_id = a.inside_structure_id
		     )
		   FROM actor a
		   LEFT JOIN village_object vo ON vo.id = a.inside_structure_id
		   LEFT JOIN asset asset       ON asset.id = vo.asset_id
		  WHERE a.id = $1::uuid`,
		actorID,
	).Scan(&isPC, &structureID, &structureName, &arriverIsKeeper, &arriverHasRoom)
	if err != nil {
		log.Printf("maybeNarrateClosedBusinessArrival(%s): load row: %v", actorID, err)
		return
	}

	// PC-only in v1.
	if !isPC || structureID == "" || arriverIsKeeper || arriverHasRoom {
		return
	}

	closed, err := app.isBusinessClosed(ctx, structureID)
	if err != nil {
		log.Printf("maybeNarrateClosedBusinessArrival(%s): isBusinessClosed: %v", actorID, err)
		return
	}
	if !closed {
		return
	}

	// Brown-panel narration. Same shape as the PC sleep / consume
	// narration: room_event with private=true and the PC's actor_id so
	// the talk panel renders it as a second-person line in the
	// arriver's private narration box.
	text := fmt.Sprintf("You arrive at %s. It is closed.", structureName)
	app.Hub.Broadcast(WorldEvent{
		Type: "room_event",
		Data: map[string]interface{}{
			"actor_id":   actorID,
			"actor_name": "",
			"kind":       "closed_business_arrival",
			"text":       text,
			"private":    true,
			"at":         time.Now().UTC().Format(time.RFC3339),
		},
	})
}
