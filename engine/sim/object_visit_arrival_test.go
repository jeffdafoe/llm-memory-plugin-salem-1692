package sim_test

import (
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// arrivedAtObject drives the unexported arrivedAtDestination (via the
// ArrivedAtDestination test alias) for an ObjectVisit destination targeting
// objID, with a synthesized actor parked at pos. The actor need not be
// registered in the world — arrivedAtDestination reads only actor.Pos and the
// destination's object loiter pin, not w.Actors.
func arrivedAtObject(t *testing.T, w *sim.World, objID sim.VillageObjectID, pos sim.Position) bool {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		actor := &sim.Actor{ID: "visitor", Pos: pos}
		return sim.ArrivedAtDestination(world, actor, sim.NewObjectVisitDestination(objID)), nil
	}})
	if err != nil {
		t.Fatalf("arrivedAtObject command: %v", err)
	}
	return res.(bool)
}

// TestArrivedAtDestination_ObjectVisit covers the object_visit arrival arm
// (ZBBS-HOME-325). Before the fix, object_visit had no case in
// arrivedAtDestination, so it fell through default:false — finishArrival never
// ran, the mover was stuck "walking in place", and no ActorArrived (hence no
// object-refresh-on-arrival) fired. Arrival now checks Chebyshev <=
// LoiterAttributionTiles against the object's loiter pin, NOT ring membership,
// so the pin tile itself — the all-slots-blocked last resort pickObjectVisitorSlot
// can return — also counts as arrived, and the radius matches the one
// resolveLoiteringObject uses to attribute object-refresh-on-arrival.
func TestArrivedAtDestination_ObjectVisit(t *testing.T) {
	w, cancel, pin := seedObjectSlotWorld(t, nil)
	defer cancel()

	cases := []struct {
		name string
		pos  sim.Position
		want bool
	}{
		{"on the pin tile (Chebyshev 0, last-resort slot)", pin, true},
		{"on a king's-move ring slot (Chebyshev 1)", sim.Position{X: pin.X + 1, Y: pin.Y + 1}, true},
		{"one tile beyond the ring (Chebyshev 2)", sim.Position{X: pin.X + 2, Y: pin.Y}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := arrivedAtObject(t, w, "oak", tc.pos); got != tc.want {
				t.Errorf("arrivedAtDestination(pos=%+v, pin=%+v) = %v, want %v", tc.pos, pin, got, tc.want)
			}
		})
	}
}

// TestArrivedAtDestination_ObjectVisit_Unresolvable: an object_visit whose
// object can't be resolved — a missing object id, or a nil ObjectID — is a
// clean "not arrived", never a panic.
func TestArrivedAtDestination_ObjectVisit_Unresolvable(t *testing.T) {
	w, cancel, pin := seedObjectSlotWorld(t, nil)
	defer cancel()

	if arrivedAtObject(t, w, "no-such-object", pin) {
		t.Error("missing object id: want not-arrived")
	}

	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		actor := &sim.Actor{ID: "visitor", Pos: pin}
		return sim.ArrivedAtDestination(world, actor, sim.MoveDestination{Kind: sim.MoveDestinationObjectVisit}), nil
	}})
	if err != nil {
		t.Fatalf("nil-ObjectID command: %v", err)
	}
	if res.(bool) {
		t.Error("nil ObjectID: want not-arrived")
	}
}
