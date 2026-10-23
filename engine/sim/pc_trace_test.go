package sim

import (
	"testing"
	"time"
)

// pc_trace_test.go — LLM-847. The durable trace of a player's bed-down, wake
// and room moves: one row per event with the right how / reason / cause, never
// a huddle id, and the per-minute safety net for a move nothing recorded.

var traceNow = time.Date(2026, 10, 23, 0, 52, 0, 0, time.UTC)

func traceSink(w *World) *recordingActionLogSink {
	sink := &recordingActionLogSink{}
	w.SetActionLogSink(sink)
	return sink
}

// traceRows returns the sink's rows of type t.
func traceRows(sink *recordingActionLogSink, t ActionType) []DurableActionLogRow {
	var out []DurableActionLogRow
	for _, r := range sink.rows {
		if r.ActionType == t {
			out = append(out, r)
		}
	}
	return out
}

// onlyTraceRow fails unless exactly one row of type t was written, and returns it.
func onlyTraceRow(t *testing.T, sink *recordingActionLogSink, typ ActionType) DurableActionLogRow {
	t.Helper()
	rows := traceRows(sink, typ)
	if len(rows) != 1 {
		t.Fatalf("%s rows = %d (%v), want 1", typ, len(rows), rows)
	}
	return rows[0]
}

// tavernLodger: a player in the Tavern common room holding bedroom_1, present.
func tavernLodger(t *testing.T) (*World, *Actor, *recordingActionLogSink) {
	t.Helper()
	jeff := upLodger("jeff", upBedroom1, traceNow.Add(14*time.Hour))
	jeff.DisplayName = "Jefferey"
	jeff.Needs["tiredness"] = 22
	seen := traceNow
	jeff.LastPCSeenAt = &seen
	w, _ := upstairsWorldAt(upTavern, jeff)
	setActorInsideStructure(w, jeff, upTavern)
	return w, jeff, traceSink(w)
}

func TestPCTraceBedDownArms(t *testing.T) {
	for _, tc := range []struct {
		how string
		bed func(w *World, jeff *Actor)
	}{
		{pcSleptBedTap, func(w *World, _ *Actor) { SleepPC("jeff", traceNow).Fn(w) }},
		{pcSleptIdle, func(w *World, jeff *Actor) {
			idle := traceNow.Add(-time.Hour)
			jeff.LastPCInputAt = &idle
			AutoBedIdleLodgerPCs(traceNow).Fn(w)
		}},
		{pcSleptOffline, func(w *World, jeff *Actor) {
			jeff.LastPCSeenAt = nil
			AutoBedOfflineLodgerPCs(traceNow).Fn(w)
		}},
	} {
		t.Run(tc.how, func(t *testing.T) {
			w, jeff, sink := tavernLodger(t)
			jeff.CurrentHuddleID = "huddle-1" // the trace never carries one
			tc.bed(w, jeff)
			if jeff.SleepingUntil == nil {
				t.Fatal("not bedded")
			}
			slept := onlyTraceRow(t, sink, ActionTypePCSlept)
			if slept.Payload["how"] != tc.how || slept.Payload["room"] != "bedroom_1" || slept.Payload["structure"] != "Tavern" ||
				slept.Payload["tiredness"] != 22 || slept.Payload["wake_cap_at"] == nil {
				t.Errorf("pc_slept payload %v", slept.Payload)
			}
			moved := onlyTraceRow(t, sink, ActionTypePCRoomChanged)
			if moved.Payload["cause"] != pcRoomBedDown || moved.Payload["from_room"] != "common" || moved.Payload["to_room"] != "bedroom_1" {
				t.Errorf("pc_room_changed payload %v", moved.Payload)
			}
			for _, r := range sink.rows {
				if r.HuddleID != "" || r.Source != "engine" || r.SpeakerName != "Jefferey" || r.ActorID != "jeff" {
					t.Errorf("row %s: huddle %q source %q speaker %q actor %q — want no huddle, engine, Jefferey, jeff",
						r.ActionType, r.HuddleID, r.Source, r.SpeakerName, r.ActorID)
				}
			}
		})
	}
}

func TestPCTraceWakeReasons(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reason string
		stale  bool
		wake   func(w *World, jeff *Actor)
	}{
		{"rested while present", pcWokeRested, false, func(w *World, jeff *Actor) {
			jeff.Needs["tiredness"] = 0
			WakeExpiredPCSleepers(traceNow.Add(time.Hour)).Fn(w)
		}},
		{"12 h cap while away", pcWokeCap, true, func(w *World, jeff *Actor) {
			jeff.LastPCSeenAt = nil
			WakeExpiredPCSleepers(*jeff.SleepingUntil).Fn(w)
		}},
		{"reconnect", pcWokeReconnect, true, func(w *World, jeff *Actor) {
			jeff.LastPCSeenAt = nil
			StampConnectedPCsSeen(map[string]struct{}{"jeff": {}}).Fn(w)
		}},
		{"wake button", pcWokeManual, false, func(w *World, _ *Actor) {
			WakePC("jeff", traceNow.Add(time.Hour)).Fn(w)
		}},
		{"acting while asleep", pcWokeInput, true, func(w *World, jeff *Actor) {
			jeff.LastPCSeenAt = nil
			TouchPCInput(w, "jeff", traceNow.Add(time.Hour))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, jeff, sink := tavernLodger(t)
			SleepPC("jeff", traceNow).Fn(w)
			sink.rows = nil
			if !tc.stale {
				fresh := traceNow.Add(time.Hour) // present at the wake
				jeff.LastPCSeenAt = &fresh
			}
			tc.wake(w, jeff)
			if jeff.SleepingUntil != nil {
				t.Fatal("still asleep")
			}
			woke := onlyTraceRow(t, sink, ActionTypePCWoke)
			if woke.Payload["reason"] != tc.reason || woke.Payload["presence_stale"] != tc.stale ||
				woke.Payload["from_room"] != "bedroom_1" || woke.Payload["to_room"] != "bedroom_1" {
				t.Errorf("pc_woke payload %v, want reason %s stale %v, bedroom_1 to bedroom_1", woke.Payload, tc.reason, tc.stale)
			}
			// The Tavern's guest rooms have a plan: they wake in their room, so no move.
			if rows := traceRows(sink, ActionTypePCRoomChanged); len(rows) != 0 {
				t.Errorf("room rows %v, want none (woke in the room slept in)", rows)
			}
		})
	}
}

// A bedroom with no plan: the wake drops them to the common room, and the morning
// descent that follows is no second move (common by id is common by 0).
func TestPCTraceWakeIntoCommonRoom(t *testing.T) {
	jeff := lodgerPC("jeff", traceNow.Add(14*time.Hour))
	seen := traceNow.Add(time.Hour)
	jeff.LastPCSeenAt = &seen
	w := pcSleepWorld(jeff)
	RegisterLodgingMorningDescentSubscriber(w)
	sink := traceSink(w)
	SleepPC("jeff", traceNow).Fn(w)
	sink.rows = nil

	jeff.Needs["tiredness"] = 0
	WakeExpiredPCSleepers(traceNow.Add(time.Hour)).Fn(w)

	moved := onlyTraceRow(t, sink, ActionTypePCRoomChanged)
	if moved.Payload["cause"] != pcRoomWake || moved.Payload["from_room"] != "bedroom_1" || moved.Payload["to_room"] != "common" {
		t.Errorf("pc_room_changed payload %v, want wake, bedroom_1 to common", moved.Payload)
	}
	if woke := onlyTraceRow(t, sink, ActionTypePCWoke); woke.Payload["reason"] != pcWokeRested || woke.Payload["to_room"] != "common" {
		t.Errorf("pc_woke payload %v", woke.Payload)
	}
}

func TestPCTraceCheckoutAndMovedOutWakes(t *testing.T) {
	t.Run("checkout", func(t *testing.T) {
		w, _, sink := tavernLodger(t)
		SleepPC("jeff", traceNow).Fn(w)
		sink.rows = nil
		later := traceNow.Add(15 * time.Hour) // past the grant's end
		WakeExpiredPCSleepers(later).Fn(w)
		if woke := onlyTraceRow(t, sink, ActionTypePCWoke); woke.Payload["reason"] != pcWokeCheckout {
			t.Errorf("pc_woke payload %v, want checkout", woke.Payload)
		}
		// The checkout wake keeps the room; the eviction sweep moves them, and says so.
		ExpireRoomAccess(later).Fn(w)
		EvictExpiredOccupants(later).Fn(w)
		moved := onlyTraceRow(t, sink, ActionTypePCRoomChanged)
		if moved.Payload["cause"] != pcRoomCheckout || moved.Payload["from_room"] != "bedroom_1" || moved.Payload["to_room"] != "common" {
			t.Errorf("pc_room_changed payload %v, want checkout, bedroom_1 to common", moved.Payload)
		}
	})
	t.Run("moved out while asleep", func(t *testing.T) {
		w, jeff, sink := tavernLodger(t)
		SleepPC("jeff", traceNow).Fn(w)
		jeff.InsideRoomID = upBedroom3 // something moved the sleeper
		sink.rows = nil
		WakeExpiredPCSleepers(traceNow.Add(time.Minute)).Fn(w)
		if woke := onlyTraceRow(t, sink, ActionTypePCWoke); woke.Payload["reason"] != pcWokeMovedOut || woke.Payload["from_room"] != "bedroom_3" {
			t.Errorf("pc_woke payload %v, want moved_out from bedroom_3", woke.Payload)
		}
		// The silent move is named first, then the wake's own move.
		rows := traceRows(sink, ActionTypePCRoomChanged)
		if len(rows) != 2 ||
			rows[0].Payload["cause"] != pcRoomUnattributed || rows[0].Payload["from_room"] != "bedroom_1" || rows[0].Payload["to_room"] != "bedroom_3" ||
			rows[1].Payload["cause"] != pcRoomWake || rows[1].Payload["from_room"] != "bedroom_3" || rows[1].Payload["to_room"] != "common" {
			t.Errorf("room rows %v, want unattributed bedroom_1 to bedroom_3, then wake bedroom_3 to common", rows)
		}
	})
}

func TestPCTraceGoRoomAndLeavingTheBuilding(t *testing.T) {
	w, jeff, sink := tavernLodger(t)
	if _, err := goRoom(t, w, "jeff", "hall", traceNow); err != nil {
		t.Fatal(err)
	}
	if _, err := goRoom(t, w, "jeff", "bedroom_1", traceNow); err != nil {
		t.Fatal(err)
	}
	rows := traceRows(sink, ActionTypePCRoomChanged)
	if len(rows) != 2 || rows[0].Payload["to_room"] != "hall" || rows[1].Payload["from_room"] != "hall" || rows[1].Payload["to_room"] != "bedroom_1" {
		t.Fatalf("room rows %v, want common to hall, hall to bedroom_1", rows)
	}
	for _, r := range rows {
		if r.Payload["cause"] != pcRoomGoRoom {
			t.Errorf("cause %v, want go_room", r.Payload["cause"])
		}
	}

	// Out of the building straight from the room upstairs: this test world has no
	// footprints, so every tile reads as outdoors and the walk's reconcile leaves.
	sink.rows = nil
	later := traceNow.Add(time.Minute)
	reconcileInsideAndNarrateDeparture(w, jeff, later)
	moved := onlyTraceRow(t, sink, ActionTypePCRoomChanged)
	if moved.Payload["cause"] != pcRoomStructureChange || moved.Payload["from_room"] != "bedroom_1" ||
		moved.Payload["to_room"] != "outdoors" || moved.Payload["structure"] != "Tavern" || !moved.OccurredAt.Equal(later) {
		t.Errorf("pc_room_changed %v at %v, want structure_change from the Tavern's bedroom_1 to outdoors at the walk's time", moved.Payload, moved.OccurredAt)
	}
	// Outdoors already: nothing to record.
	sink.rows = nil
	reconcileInsideAndNarrateDeparture(w, jeff, later)
	if len(sink.rows) != 0 {
		t.Errorf("a reconcile that changed no room wrote %v", sink.rows)
	}
}

// A move nothing recorded, then a recorded one, before the per-minute sweep: the
// gap is still named, first (code_review).
func TestPCTraceUnrecordedMoveBeforeARecordedOne(t *testing.T) {
	t.Run("then a recorded move", func(t *testing.T) {
		w, jeff, sink := tavernLodger(t)
		if _, err := goRoom(t, w, "jeff", "hall", traceNow); err != nil {
			t.Fatal(err)
		}
		jeff.InsideRoomID = upBedroom3 // moved by a path that records nothing
		sink.rows = nil
		if _, err := goRoom(t, w, "jeff", "hall", traceNow); err != nil {
			t.Fatal(err)
		}
		rows := traceRows(sink, ActionTypePCRoomChanged)
		if len(rows) != 2 ||
			rows[0].Payload["cause"] != pcRoomUnattributed || rows[0].Payload["from_room"] != "hall" || rows[0].Payload["to_room"] != "bedroom_3" ||
			rows[1].Payload["cause"] != pcRoomGoRoom || rows[1].Payload["from_room"] != "bedroom_3" || rows[1].Payload["to_room"] != "hall" {
			t.Fatalf("room rows %v, want unattributed hall to bedroom_3, then go_room bedroom_3 to hall", rows)
		}
		sink.rows = nil
		TracePCRooms(traceNow.Add(time.Minute)).Fn(w)
		if len(sink.rows) != 0 {
			t.Errorf("the sweep caught it again: %v", sink.rows)
		}
	})
	t.Run("then a recorded step that moves nothing", func(t *testing.T) {
		w, jeff, sink := tavernLodger(t)
		if _, err := goRoom(t, w, "jeff", "hall", traceNow); err != nil {
			t.Fatal(err)
		}
		jeff.InsideRoomID = upCommon // moved by a path that records nothing
		sink.rows = nil
		tracePCRoomChange(w, jeff, jeff.InsideRoomID, pcRoomWake, traceNow)
		moved := onlyTraceRow(t, sink, ActionTypePCRoomChanged)
		if moved.Payload["cause"] != pcRoomUnattributed || moved.Payload["from_room"] != "hall" || moved.Payload["to_room"] != "common" {
			t.Errorf("pc_room_changed payload %v, want unattributed hall to common", moved.Payload)
		}
	})
}

func TestPCTraceSafetyNet(t *testing.T) {
	w, jeff, sink := tavernLodger(t)
	// The first pass only learns where they are (a boot restores rooms silently).
	TracePCRooms(traceNow).Fn(w)
	if len(sink.rows) != 0 {
		t.Fatalf("first pass wrote %v", sink.rows)
	}
	// A recorded move is not caught again.
	if _, err := goRoom(t, w, "jeff", "hall", traceNow); err != nil {
		t.Fatal(err)
	}
	sink.rows = nil
	TracePCRooms(traceNow).Fn(w)
	if len(sink.rows) != 0 {
		t.Fatalf("a recorded move was caught again: %v", sink.rows)
	}
	// A move nothing recorded is.
	jeff.InsideRoomID = upCommon
	TracePCRooms(traceNow.Add(time.Minute)).Fn(w)
	moved := onlyTraceRow(t, sink, ActionTypePCRoomChanged)
	if moved.Payload["cause"] != pcRoomUnattributed || moved.Payload["from_room"] != "hall" || moved.Payload["to_room"] != "common" {
		t.Errorf("pc_room_changed payload %v, want unattributed, hall to common", moved.Payload)
	}
	sink.rows = nil
	TracePCRooms(traceNow.Add(2 * time.Minute)).Fn(w)
	if len(sink.rows) != 0 {
		t.Errorf("the same move was caught twice: %v", sink.rows)
	}
}

// Villagers never write the trace.
func TestPCTraceSkipsNPCs(t *testing.T) {
	keeper := outdoors("john", KindNPCStateful)
	w, _ := upstairsWorldAt(upTavern, keeper)
	sink := traceSink(w)
	setActorInsideStructure(w, keeper, upTavern)
	keeper.InsideRoomID = upQuarters
	setActorInsideStructure(w, keeper, "")
	TracePCRooms(traceNow).Fn(w)
	TracePCRooms(traceNow).Fn(w)
	if len(sink.rows) != 0 {
		t.Errorf("an NPC wrote trace rows: %v", sink.rows)
	}
}

func TestRefusalNamesTheGuestsOwnDoor(t *testing.T) {
	eachInn(t, func(t *testing.T, sid StructureID) {
		for _, tc := range []struct {
			name string
			own  RoomID // 0: no room here
			tap  string
			want string
		}{
			{"holder taps a vacant room", upBedroom1, "bedroom_3", "That room is not yours. Yours is number 1."},
			{"holder of number 3 taps number 1", upBedroom3, "bedroom_1", "That room is not yours. Yours is number 3."},
			{"holder taps the keeper's door", upBedroom1, "keeper_quarters", roomRefusedKeeper},
		} {
			t.Run(tc.name, func(t *testing.T) {
				jeff := upLodger("jeff", tc.own, traceNow.Add(14*time.Hour))
				w, _ := upstairsWorldAt(sid, jeff)
				setActorInsideStructure(w, jeff, sid)
				if _, err := goRoom(t, w, "jeff", "hall", traceNow); err != nil {
					t.Fatal(err)
				}
				_, err := MovePCToRoom("jeff", tc.tap, traceNow).Fn(w)
				if got := refusedLine(err); got != tc.want {
					t.Errorf("refusal %q, want %q", got, tc.want)
				}
			})
		}
	})
}

// A guest with a room elsewhere, or none, keeps the old line — and a room held
// at another building does not count.
func TestRefusalUnchangedWithoutARoomHere(t *testing.T) {
	jeff := upLodger("jeff", upBedroom1, traceNow.Add(14*time.Hour))
	w, _ := upstairsWorldAt(upTavern, jeff)
	setActorInsideStructure(w, jeff, upTavern)
	if _, err := goRoom(t, w, "jeff", "hall", traceNow); err != nil {
		t.Fatal(err)
	}
	// Their grant lapses while they stand in the hall: no room here now.
	until := traceNow.Add(-time.Minute)
	jeff.RoomAccess[RoomAccessKey{RoomID: upBedroom1, Source: AccessSourceLedger}].ExpiresAt = &until
	_, err := MovePCToRoom("jeff", "bedroom_3", traceNow).Fn(w)
	if got := refusedLine(err); got != roomRefusedNotYours {
		t.Errorf("refusal %q, want the unchanged %q", got, roomRefusedNotYours)
	}
}

func TestHeldRoomName(t *testing.T) {
	jeff := upLodger("jeff", upBedroom3, traceNow.Add(time.Hour))
	gone := upLodger("abe", upBedroom1, traceNow.Add(-time.Hour))
	w, _ := upstairsWorld(jeff, gone)
	snap := &Snapshot{PublishedAt: traceNow, Structures: map[StructureID]*Structure{upInn: w.Structures[upInn]}}
	if got := HeldRoomName(snap, &ActorSnapshot{RoomAccess: jeff.RoomAccess}, upInn); got != "bedroom_3" {
		t.Errorf("held room %q, want bedroom_3", got)
	}
	if got := HeldRoomName(snap, &ActorSnapshot{RoomAccess: gone.RoomAccess}, upInn); got != "" {
		t.Errorf("lapsed grant gave %q, want none", got)
	}
	if got := HeldRoomName(snap, &ActorSnapshot{RoomAccess: jeff.RoomAccess}, upTavern); got != "" {
		t.Errorf("another building gave %q, want none", got)
	}
}
