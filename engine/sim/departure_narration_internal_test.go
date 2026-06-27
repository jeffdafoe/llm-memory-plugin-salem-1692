package sim

import (
	"testing"
	"time"
)

// departure_narration_internal_test.go — LLM-146 white-box coverage of
// emitDepartureNarration's leak-sensitive room-scope gate, which the external
// locomotion-driven tests (arrival_narration_test.go) can't reach: the live "X
// leaves the Y" room_event is structure-scoped with NO room_id, so it must NOT
// fire for a mover leaving a private/staff room — that frame would otherwise
// leak the back-room departure to common-room observers. Mirrors
// emitArrivalNarration's gate; the emit runs pre-flip, so the mover's still-set
// InsideRoomID is the one read.
func TestEmitDepartureNarration_SkipsPrivateRoom(t *testing.T) {
	build := func(insideRoom RoomID) (*World, *int) {
		w := &World{
			Structures: map[StructureID]*Structure{
				"inn": {ID: "inn", DisplayName: "the Inn", Rooms: []*Room{
					{ID: 1, StructureID: "inn", Kind: RoomKindPrivate, Name: "bedroom_1"},
				}},
			},
			Actors: map[ActorID]*Actor{
				"wendy":    {ID: "wendy", DisplayName: "Wendy", Kind: KindPC, InsideStructureID: "inn", InsideRoomID: insideRoom},
				"jefferey": {ID: "jefferey", DisplayName: "Jefferey", Kind: KindPC, InsideStructureID: "inn"}, // common-area observer
			},
		}
		count := 0
		w.Subscribe(SubscriberFunc(func(_ *World, e Event) {
			if _, ok := e.(*ActorDepartureNarrated); ok {
				count++
			}
		}))
		return w, &count
	}

	// Private room (InsideRoomID = 1): the back-room departure is suppressed even
	// though a common-area PC (jefferey) is present — proving it's the room gate,
	// not the no-audience gate, doing the suppression.
	wPriv, nPriv := build(1)
	emitDepartureNarration(wPriv, wPriv.Actors["wendy"], "inn", time.Now().UTC())
	if *nPriv != 0 {
		t.Errorf("private-room departure: ActorDepartureNarrated count = %d, want 0 (no room_id-less leak)", *nPriv)
	}

	// Public scope (InsideRoomID = 0) with the same co-present PC: the line emits.
	wPub, nPub := build(0)
	emitDepartureNarration(wPub, wPub.Actors["wendy"], "inn", time.Now().UTC())
	if *nPub != 1 {
		t.Errorf("public departure: ActorDepartureNarrated count = %d, want 1", *nPub)
	}
}
