package sim

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// gather_commands.go — ZBBS-WORK-328. The v2 revival of v1's `gather` verb
// (engine/gather.go) as a general environmental-harvest substrate.
//
// A gatherable source is a placed VillageObject carrying an ObjectRefresh row
// with GatherItem set (see object_refresh.go). Gather is the "fill a pail"
// complement to ApplyObjectRefreshAtArrival's "drink in place": both resolve
// the same loitering source and, for a finite source, draw down the SAME
// AvailableQuantity — so a well/bush is one shared stock that the regen tick
// (RunObjectRefreshRegen) refills. The same command backs both the NPC
// `gather` tool and the PC pc/gather route; the actor kind doesn't matter
// here (gating happens at the tool/route layer).

// MaxGatherQty bounds qty accepted by the Gather Command — defense in depth
// against int wrap on inventory math from a non-handler caller (Gather is
// exported). Gameplay-reasonable caps belong at the handler/route layer.
const MaxGatherQty = math.MaxInt32

var (
	// ErrNoGatherSource — the actor isn't loitering at a gatherable source
	// (no named object owns their tile, or the one that does carries no
	// GatherItem refresh row). LLM-facing signal: "walk to a well/bush first."
	ErrNoGatherSource = errors.New("no gatherable source here")

	// ErrGatherableDepleted — the source is finite and currently empty. It
	// refills over time via the regen tick; this is a transient reject, not a
	// permanent one.
	ErrGatherableDepleted = errors.New("the source is depleted right now")

	// ErrNotYourSource — the gatherable source is OWNED by another actor.
	// Gather + eat at an owned village object are owner-only (LLM-50 D2,
	// VillageObject.OwnedByOther); unowned sources are commons. A permanent
	// reject for this actor — LLM-facing signal: it belongs to someone else,
	// look for a wild source instead.
	ErrNotYourSource = errors.New("that source belongs to someone else — find a wild one")
)

// GatherResult is the Command reply — what was harvested, for the handler /
// route to build the tool-result or HTTP response narration without
// re-deriving anything.
type GatherResult struct {
	ObjectID   VillageObjectID
	SourceName string // resolved display/catalog name, e.g. "Old Well"
	Item       ItemKind
	Qty        int // units actually gathered (<= requested when finite ran low)
}

// findGatherableObjectNear resolves the named VillageObject the actor is
// loitering at (resolveLoiteringObject → the v1 attribution radius) and
// returns it with its first gatherable refresh row, or (.., nil, nil) when no
// object owns the tile or the resolved one has no GatherItem row.
//
// Resolve-then-check, faithful to ApplyObjectRefreshAtArrival: a single
// loitering object owns the tile; the resolver does NOT skip past a
// non-gatherable object to a gatherable one farther away.
func findGatherableObjectNear(w *World, actorTile TilePos) (VillageObjectID, *VillageObject, *ObjectRefresh) {
	id, obj := findRefreshObjectNear(w, actorTile)
	if obj == nil {
		return "", nil, nil
	}
	for _, r := range obj.Refreshes {
		if r.IsGatherable() {
			return id, obj, r
		}
	}
	return "", nil, nil
}

// Gather returns a Command that harvests qty units of the gatherable source
// the actor is loitering at into their inventory.
//
// Pre-conditions (Gather is exported — non-handler callers must not bypass):
//   - qty defaults to 1 when < 1 (v1 behavior); rejected when > MaxGatherQty
//   - actorID resolves to a real actor in w.Actors
//   - actor.MoveIntent == nil (not walk-in-flight — must have arrived)
//   - the actor is loitering at a gatherable source (ErrNoGatherSource)
//   - the source's GatherItem resolves to a known kind (else misconfiguration
//     surfaces as ErrUnknownItemKind)
//   - a finite source has stock (ErrGatherableDepleted)
//
// On success:
//   - finite source: AvailableQuantity decrements by qty, clamped so qty never
//     exceeds remaining stock (a request for 5 from a bush with 3 yields 3)
//   - infinite source (well, AvailableQuantity nil): no decrement, qty as asked
//   - actor.Inventory[kind] += qty (lazy-inits the map)
//   - emits ItemGathered
func Gather(actorID ActorID, qty int, at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			// Work off locals — never mutate the captured qty, so a re-run of
			// this (exported) Command observes the original request, not a
			// clamped/defaulted value.
			requested := qty
			if requested < 1 {
				requested = 1
			}
			if requested > MaxGatherQty {
				return nil, fmt.Errorf("Gather: qty exceeds maximum (got %d, max %d)", requested, MaxGatherQty)
			}

			actor, ok := w.Actors[actorID]
			if !ok {
				return nil, fmt.Errorf("Gather: actor %q not in world", actorID)
			}
			if actor.MoveIntent != nil {
				return nil, errors.New(
					"you are walking — finish your move before gathering. " +
						"Walk to the source and arrive, then gather.",
				)
			}

			objID, obj, row := findGatherableObjectNear(w, actor.Pos)
			if row == nil {
				return nil, fmt.Errorf("Gather: %w", ErrNoGatherSource)
			}
			// Strict owner-gate (LLM-50 D2): an owned source is owner-only.
			// Same resolve-then-check posture the cue + arrival paths use; the
			// PC route (pc_gather.go) delegates to this Command, so gating here
			// covers both the NPC tool and the PC verb.
			if obj.OwnedByOther(actorID) {
				return nil, fmt.Errorf("Gather: %w", ErrNotYourSource)
			}

			// gather_item is stored canonical, but resolve case-insensitively
			// (and trim) so a hand-edited value still maps; an unresolvable
			// value is a data misconfiguration and surfaces loudly.
			kind, ok := resolveItemKind(w, string(row.GatherItem))
			if !ok {
				return nil, fmt.Errorf("Gather: %w %q (source %s gather_item)", ErrUnknownItemKind, row.GatherItem, objID)
			}

			// Resolve the actual amount (clamped to a finite source's stock) and
			// validate fully BEFORE any mutation — an early return after
			// decrementing supply would lose stock with no inventory credited.
			actual := requested
			if row.IsFinite() {
				avail := *row.AvailableQuantity
				if avail <= 0 {
					return nil, fmt.Errorf("Gather: %w", ErrGatherableDepleted)
				}
				if actual > avail {
					actual = avail
				}
			}
			cur := actor.Inventory[kind]
			if cur > math.MaxInt-actual {
				// Pathological accumulated stock — refuse rather than wrap negative.
				return nil, fmt.Errorf("Gather: inventory quantity overflow for %q (have %d, +%d)", kind, cur, actual)
			}

			// Mutations (post-validation): decrement finite supply, credit inventory.
			if row.IsFinite() {
				next := *row.AvailableQuantity - actual
				row.AvailableQuantity = &next
				// Picking may have emptied a finite bush — recompute its
				// berries/bare visual so it goes bare.
				refreshObjectBerryState(w, obj)
			}
			if actor.Inventory == nil {
				actor.Inventory = make(map[ItemKind]int)
			}
			actor.Inventory[kind] = cur + actual

			catalogName := ""
			if a := w.Assets[obj.AssetID]; a != nil {
				catalogName = a.Name
			}
			name := obj.EffectiveDisplayName(catalogName)

			w.emit(&ItemGathered{
				ActorID:  actorID,
				ObjectID: objID,
				Item:     kind,
				Qty:      actual,
				At:       at,
			})

			return GatherResult{
				ObjectID:   objID,
				SourceName: name,
				Item:       kind,
				Qty:        actual,
			}, nil
		},
	}
}
