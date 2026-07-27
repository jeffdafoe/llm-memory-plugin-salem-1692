package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_routes.go — the live NPC-route read (LLM-539):
//
//	/umbilical/npc-routes[?actor=] — every active NPCRoute: which stop, dwelling
//	                                 or walking, dwell timer armed or not, stops
//	                                 remaining.
//
// Named npc-routes, not routes, on purpose: UmbilicalManifestDTO already calls its
// HTTP-endpoint descriptors "routes", and POST /umbilical/route is the operator
// lever that force-dispatches one of these. A GET /umbilical/routes sitting one
// character from that, and reading like "list the umbilical's endpoints", would be
// two ways to be wrong at once.
//
// Why it exists. World.ActiveRoutes drives the town crier's tour, the washerwoman's
// laundry run, and the constable's rounds, and before this route it was visible from
// nowhere outside the engine process — not on the published snapshot, not on any
// handler. /agent reports the actor's in-flight MOVE target, which is the current
// walk leg, not the route: it says where he is walking, never which stop he is on or
// whether he is parked in a dwell.
//
// The cost of that was concrete. LLM-537 (a constable who stood in a smithy for 9+
// minutes after both parties said goodbye) had to be diagnosed entirely by inference
// — correlating /actions timestamps against /huddles last-activity stamps, then
// reading npc_route.go for a state that would produce that shape. route.Dwelling and
// the armed DwellTimer were never read directly, so the ticket shipped with its
// mechanism labelled "inferred" rather than observed. Three routes share this
// substrate and the constable rounds are the newest of them; this will not have been
// the last route-pacing bug.
//
// Read LIVE via SendContext, NOT the published snapshot — the same rationale
// /umbilical/huddles documents. NPCRoute is world-goroutine-owned by contract
// (see the field comments on sim.NPCRoute) and is deliberately NOT published:
// route state is transient and never persisted, so a snapshot field would add a
// publish-path clone for data only this route reads. The closure copies plain
// values out; no *NPCRoute or *Actor escapes to the HTTP goroutine.
//
// Gated by requireOperator and registered only when the umbilical is enabled, both
// inherited from the umbilicalRoutes() descriptor table like every other read.

// UmbilicalNPCRouteStopDTO is one stop on a route's itinerary. EnterStructureID
// distinguishes the two stop kinds the substrate resolves between: an ENTER stop
// (the carrier walks inside) versus a loiter/tile stop (he stands at WalkTo).
// Which one a business resolved to is itself diagnostic — a closed or locked
// business turns an enter stop into a loiter stop (routeStopEntersStructure), so a
// carrier standing outside a shop he normally enters is expected, not a bug.
type UmbilicalNPCRouteStopDTO struct {
	ObjectID string `json:"object_id"`
	// WalkToX / WalkToY is the stop's target tile — the same tile space
	// /agent reports move_target_tile_x/y in, so the two reads correlate.
	WalkToX int `json:"walk_to_x"`
	WalkToY int `json:"walk_to_y"`
	// NewState is the object state this stop flips the placement to on arrival
	// (the lamplighter lighting a lamp, the washerwoman hanging laundry). Empty
	// for stops that only visit — the constable changes nothing.
	NewState string `json:"new_state,omitempty"`
	// EnterStructureID is set iff this is an enter stop; empty means loiter.
	EnterStructureID string `json:"enter_structure_id,omitempty"`
	// Current marks the stop the route's cursor is on (StopIdx). Exactly one stop
	// carries it while the route is walking its itinerary; none do once the cursor
	// has run past the end (phase "returning").
	Current bool `json:"current"`
}

// UmbilicalNPCRouteDTO is one active route on the wire.
type UmbilicalNPCRouteDTO struct {
	NPCID   string `json:"npc_id"`
	NPCName string `json:"npc_name,omitempty"`
	// Label is the route behavior: town_crier / washerwoman / lamplighter /
	// constable.
	Label string `json:"label"`
	// Phase is the route lifecycle: "active" (walking the itinerary),
	// "returning" (past the last stop, heading home/to post), or "suspended"
	// (part-walked and paused — the carrier is his own man again, so shift duty
	// and the idle backstop treat him as routeless; LLM-531).
	Phase string `json:"phase"`
	// Gen is the route's install identity token, stamped from World.routeInstallSeq
	// at each StartNPCRoute. It disambiguates two routes of the same actor at the
	// same StopIdx, which is what the dwell callback validates against — so when a
	// timer misfire is suspected, this is the number to compare across two reads.
	Gen uint64 `json:"gen"`
	// StopIdx is the cursor into Stops. It can equal StopCount once the itinerary
	// is done and the carrier is returning.
	StopIdx   int `json:"stop_idx"`
	StopCount int `json:"stop_count"`
	// ArrivedAtCurrentStop is the ROUTE'S OWN dispatch condition for the cursor's
	// stop (sim.RouteStopArrived): inside the structure for an enter stop, standing
	// EXACTLY on WalkTo for a loiter stop. False when the cursor is past the end.
	//
	// Read it with ReachedCurrentStopOnFoot, not instead of it. This one is strict
	// tile equality, which is right for a walk the route dispatched (it aims the
	// actor at that exact tile) and routinely FALSE for a carrier who walked himself
	// there — his own move_to resolves to a visitor slot AROUND the loiter pin, so
	// he is visibly at the stop and not on its tile.
	ArrivedAtCurrentStop bool `json:"arrived_at_current_stop"`
	// ReachedCurrentStopOnFoot is the tolerant twin (sim.RouteStopReachedOnFoot):
	// the same test for an enter stop, but within LoiterAttributionTiles of the pin
	// for a loiter stop — "standing at this place" as every other co-location check
	// in the engine means it.
	//
	// The two disagreeing is itself the signal, and has been a real bug shape twice
	// (LLM-530, LLM-531): a carrier who walked himself to the named stop reads
	// reached=true / arrived=false, and any route logic written against the strict
	// form silently fails for exactly the path it was built to serve.
	ReachedCurrentStopOnFoot bool `json:"reached_current_stop_on_foot"`
	// Dwelling marks the constable PAUSED at his current stop — the per-stop dwell
	// window between arrival and moving on.
	Dwelling bool `json:"dwelling"`
	// DwellTimerPresent reports that route.DwellTimer is non-nil. Read alongside
	// Dwelling it is the field that separates "paused on a dwell, working as
	// designed" from "parked with nothing scheduled to move him" — the ambiguity
	// that made LLM-537 diagnosable only by inference. The timer is a *time.Timer
	// and cannot be marshalled, so presence is reported as a bool.
	//
	// PRESENCE, NOT LIVENESS — deliberately named for what it measures. A fired
	// timer leaves its pointer behind until the advance command clears it, and there
	// are two windows where that shows here as true with nothing actually counting
	// down: between the timer firing and the world goroutine running the queued
	// advance, and (unbounded) when that advance early-returns on its
	// actor/Gen/Phase/StopIdx guard, which returns before nilling the pointer. A
	// false NEGATIVE is not possible, so `false` is the trustworthy direction: it
	// means no dwell is scheduled for this stop, full stop.
	DwellTimerPresent bool `json:"dwell_timer_present"`
	// Authoring is the town crier's analogue of Dwelling: an off-world noticeboard
	// author call is in flight for the current stop.
	Authoring bool `json:"authoring"`
	// StaleRetries counts consecutive stale arrivals at the current stop — re-walk
	// attempts since the last clean visit. Climbing values mean the carrier keeps
	// being dispatched to a stop he never registers as reaching; at
	// maxStaleRouteRetries the route abandons.
	StaleRetries int `json:"stale_retries"`
	// HomeDestinationKind + the sibling id/tile are the flattened MoveDestination
	// the carrier walks to after the last stop (the constable's is his POST, not
	// his home). Kind disambiguates which sibling field is the real destination.
	HomeDestinationKind        string `json:"home_destination_kind"`
	HomeDestinationStructureID string `json:"home_destination_structure_id,omitempty"`
	HomeDestinationObjectID    string `json:"home_destination_object_id,omitempty"`
	HomeDestinationX           *int   `json:"home_destination_x,omitempty"`
	HomeDestinationY           *int   `json:"home_destination_y,omitempty"`

	Stops []UmbilicalNPCRouteStopDTO `json:"stops"`
}

// UmbilicalNPCRoutesDTO is the GET /api/village/umbilical/npc-routes response: every
// active route, sorted by actor id for a stable read.
type UmbilicalNPCRoutesDTO struct {
	ContractVersion int                    `json:"contract_version"`
	Now             time.Time              `json:"now"`
	Total           int                    `json:"total"`
	Routes          []UmbilicalNPCRouteDTO `json:"routes"`
}

// handleUmbilicalNPCRoutes serves every active NPC route. Optional `actor` query param
// filters to one carrier; an unknown actor yields an EMPTY list rather than a 404 —
// the /objects + /sell-through optional-filter posture, and the right one here since
// "this actor has no route right now" and "this actor does not exist" are both
// legitimately empty answers to "what is he routing?". Pure read.
func (s *Server) handleUmbilicalNPCRoutes(w http.ResponseWriter, r *http.Request) {
	actorFilter := sim.ActorID(r.URL.Query().Get("actor"))
	res, err := s.world.SendContext(r.Context(), sim.Command{Fn: func(world *sim.World) (any, error) {
		dto := UmbilicalNPCRoutesDTO{
			ContractVersion: ContractVersion,
			Now:             time.Now().UTC(),
			Routes:          make([]UmbilicalNPCRouteDTO, 0, len(world.ActiveRoutes)),
		}
		for id, route := range world.ActiveRoutes {
			if route == nil {
				continue
			}
			if actorFilter != "" && id != actorFilter {
				continue
			}
			dto.Routes = append(dto.Routes, umbilicalNPCRouteDTO(world, id, route))
		}
		dto.Total = len(dto.Routes)
		sort.Slice(dto.Routes, func(i, j int) bool { return dto.Routes[i].NPCID < dto.Routes[j].NPCID })
		return dto, nil
	}})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	dto, ok := res.(UmbilicalNPCRoutesDTO)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected routes result")
		return
	}
	writeJSON(w, dto)
}

// umbilicalNPCRouteDTO copies one live route into a value DTO. MUST run on the world
// goroutine (reads *NPCRoute + *Actor); no pointer escapes.
func umbilicalNPCRouteDTO(world *sim.World, id sim.ActorID, route *sim.NPCRoute) UmbilicalNPCRouteDTO {
	actor := world.Actors[id]
	dto := UmbilicalNPCRouteDTO{
		NPCID:             string(id),
		Label:             route.Label,
		Phase:             string(route.Phase),
		Gen:               route.Gen,
		StopIdx:           route.StopIdx,
		StopCount:         len(route.Stops),
		Dwelling:          route.Dwelling,
		DwellTimerPresent: route.DwellTimer != nil,
		Authoring:         route.Authoring,
		StaleRetries:      route.StaleRetries,
		Stops:             make([]UmbilicalNPCRouteStopDTO, 0, len(route.Stops)),
	}
	if actor != nil {
		dto.NPCName = actor.DisplayName
	}
	for i, stop := range route.Stops {
		current := i == route.StopIdx
		dto.Stops = append(dto.Stops, UmbilicalNPCRouteStopDTO{
			ObjectID:         string(stop.ObjectID),
			WalkToX:          stop.WalkTo.X,
			WalkToY:          stop.WalkTo.Y,
			NewState:         stop.NewState,
			EnterStructureID: string(stop.EnterStructureID),
			Current:          current,
		})
		if current && actor != nil {
			dto.ArrivedAtCurrentStop = sim.RouteStopArrived(actor, stop)
			dto.ReachedCurrentStopOnFoot = sim.RouteStopReachedOnFoot(actor, stop)
		}
	}
	fillRouteHomeDestination(&dto, route.HomeDestination)
	return dto
}

// fillRouteHomeDestination flattens the route's home MoveDestination tagged union
// onto the DTO, mirroring how the deadlock log and /agent surface a destination:
// Kind names which sibling field carries the real target. The position is a *int
// pair so an unset destination reads absent rather than as tile (0,0), which is a
// real tile in the padded grid.
func fillRouteHomeDestination(dto *UmbilicalNPCRouteDTO, dest sim.MoveDestination) {
	dto.HomeDestinationKind = string(dest.Kind)
	if dest.StructureID != nil {
		dto.HomeDestinationStructureID = string(*dest.StructureID)
	}
	if dest.ObjectID != nil {
		dto.HomeDestinationObjectID = string(*dest.ObjectID)
	}
	if dest.Position != nil {
		x, y := dest.Position.X, dest.Position.Y
		dto.HomeDestinationX, dto.HomeDestinationY = &x, &y
	}
}
