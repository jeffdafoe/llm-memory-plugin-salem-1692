package main

// Closed-business arrival narration (ZBBS-179, fixed in ZBBS-181).
//
// When an actor arrives at a 'business'-tagged structure that is
// currently closed (no operational worker — see isBusinessClosed),
// surface a short second-person line in the PC's brown panel:
//
//   "You arrive at General Store. It is closed."
//
// The structure is the walk's target — passed in from the arrival
// hook rather than read off inside_structure_id. PCs walking to
// owner-policy structures they're not associated with land at the
// loiter point without entering, so inside_structure_id stays NULL
// there. The walk's targetStructureID is the truth either way.
//
// PC-only in v1: the room_event broadcast is private=true with the
// arriver's actor_id, so the talk panel renders it in the brown
// narration box for that PC alone. NPCs see no narration today; an
// NPC perception block is a follow-up.
//
// Skip when the arriver works at the structure (their own shop) or
// has active room_access for it (paid lodger entering an
// inn-tagged-business). Both cases would be confusing — "arrive at
// your own closed shop" reads as friction the player created.

import (
	"context"
	"fmt"
	"log"
	"time"
)

// maybeNarrateClosedBusinessArrival is called from
// applyArrivalSideEffects after the arrival commits, with the
// structure the walk targeted. Cheap no-op for non-PCs and for
// arrivals at non-business structures (isBusinessClosed returns
// false when the 'business' tag is absent).
func (app *App) maybeNarrateClosedBusinessArrival(ctx context.Context, actorID, structureID string) {
	if actorID == "" || structureID == "" {
		return
	}

	// Pull the arriver's PC status, whether they work at this structure,
	// and whether they hold active room_access there. Single round-trip.
	// Equality on possibly-NULL columns wrapped in COALESCE — work_structure_id
	// is NULL on PCs, and untreated NULL = NULL surfaces to Scan as a
	// NULL bool which can't bind to *bool.
	var (
		isPC            bool
		arriverIsKeeper bool
		arriverHasRoom  bool
	)
	err := app.DB.QueryRow(ctx,
		`SELECT
		     a.login_username IS NOT NULL,
		     COALESCE(a.work_structure_id::text = $2, false),
		     EXISTS (
		         SELECT 1 FROM room_access ra
		           JOIN structure_room sr ON sr.id = ra.room_id
		          WHERE ra.actor_id = a.id
		            AND ra.active = true
		            AND sr.structure_id = $2::uuid
		     )
		   FROM actor a
		  WHERE a.id = $1::uuid`,
		actorID, structureID,
	).Scan(&isPC, &arriverIsKeeper, &arriverHasRoom)
	if err != nil {
		log.Printf("maybeNarrateClosedBusinessArrival(%s,%s): load row: %v", actorID, structureID, err)
		return
	}

	// PC-only in v1.
	if !isPC || arriverIsKeeper || arriverHasRoom {
		return
	}

	closed, err := app.isBusinessClosed(ctx, structureID)
	if err != nil {
		log.Printf("maybeNarrateClosedBusinessArrival(%s,%s): isBusinessClosed: %v", actorID, structureID, err)
		return
	}
	if !closed {
		return
	}

	// Resolve the structure's display name for the narration text.
	var name string
	if err := app.DB.QueryRow(ctx,
		`SELECT COALESCE(o.display_name, asset.name, '')
		   FROM village_object o
		   JOIN asset ON asset.id = o.asset_id
		  WHERE o.id = $1::uuid`,
		structureID,
	).Scan(&name); err != nil || name == "" {
		log.Printf("maybeNarrateClosedBusinessArrival(%s,%s): name lookup: %v", actorID, structureID, err)
		return
	}

	// Brown-panel narration. Same shape as the PC sleep / consume
	// narration: room_event with private=true and the PC's actor_id.
	// Note: the talk panel's _on_room_event handler had a long-
	// standing empty-actor_name drop that swallowed sleep / consume /
	// arrival narrations; the companion talk_panel.gd patch in
	// ZBBS-181 lifts that drop for matched private events.
	text := fmt.Sprintf("You arrive at %s. It is closed.", name)
	app.Hub.Broadcast(WorldEvent{
		Type: "room_event",
		Data: map[string]interface{}{
			"actor_id":     actorID,
			"actor_name":   "",
			"kind":         "closed_business_arrival",
			"text":         text,
			"private":      true,
			"structure_id": structureID,
			"at":           time.Now().UTC().Format(time.RFC3339),
		},
	})
}
