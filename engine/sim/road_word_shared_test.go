package sim_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// road_word_shared_test.go — LLM-545: the traveler's carried word
// (VisitorState.Payload) is marked as given to the peers he actually conversed
// with, on the real SpeakTo path. Mirrors rumor_integration_test.go's seam
// coverage: the hook placement inside the live-huddle branch, and the
// LastUtteranceAtBy active-conversant gate.

// visitorSharedWithForTest reads a copy of a visitor's PayloadSharedWith on the
// world goroutine (World.Actors is owned by World.Run; a direct read would race).
func visitorSharedWithForTest(t *testing.T, w *sim.World, id sim.ActorID) []sim.ActorID {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		a := world.Actors[id]
		if a == nil || a.VisitorState == nil {
			return []sim.ActorID(nil), nil
		}
		return append([]sim.ActorID(nil), a.VisitorState.PayloadSharedWith...), nil
	}})
	if err != nil {
		t.Fatalf("read PayloadSharedWith for %s: %v", id, err)
	}
	return res.([]sim.ActorID)
}

// seedVisitorStateForTest attaches a VisitorState to an already-seeded actor on
// the world goroutine.
func seedVisitorStateForTest(t *testing.T, w *sim.World, id sim.ActorID, vs *sim.VisitorState) {
	t.Helper()
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors[id].VisitorState = vs
		return nil, nil
	}}); err != nil {
		t.Fatalf("seed VisitorState for %s: %v", id, err)
	}
}

// TestRoadWordSharedThroughRealSpeak drives real SpeakTo calls: a traveler
// carrying a payload speaks in a huddle where one peer has spoken (active
// conversant) and one has not. The active peer is stamped as having had the
// word; the silent bystander is not; a repeat speak does not duplicate the
// stamp.
func TestRoadWordSharedThroughRealSpeak(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "vstr-ashford", displayName: "Brother Ashford the provisioner", kind: sim.KindNPCShared, huddleID: "h1"},
		{id: "hannah", displayName: "Hannah Boggs", kind: sim.KindNPCShared, huddleID: "h1"},
		{id: "mary", displayName: "Mary", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()

	seedVisitorStateForTest(t, w, "vstr-ashford", &sim.VisitorState{
		Archetype: "provisioner",
		Origin:    "Boston",
		Payload:   "Josiah Thorne turned out meat for the inn",
	})

	base := time.Now().UTC()
	// Hannah speaks first → she becomes an active conversant. Mary stays silent.
	if _, err := w.Send(sim.SpeakTo("hannah", "Good evening, stranger.", "", nil, true, base)); err != nil {
		t.Fatalf("hannah speak: %v", err)
	}
	// The traveler speaks → recordRoadWordShared fires.
	if _, err := w.Send(sim.SpeakTo("vstr-ashford", "A word on the road gave me pause, Hannah.", "", nil, true, base.Add(time.Second))); err != nil {
		t.Fatalf("ashford speak: %v", err)
	}

	got := visitorSharedWithForTest(t, w, "vstr-ashford")
	if len(got) != 1 || got[0] != "hannah" {
		t.Fatalf("PayloadSharedWith = %v, want exactly [hannah] (active conversant stamped, silent bystander not)", got)
	}

	// A second speak must not duplicate the stamp.
	if _, err := w.Send(sim.SpeakTo("vstr-ashford", "Fair enough, Hannah — I meant no prying.", "", nil, true, base.Add(2*time.Second))); err != nil {
		t.Fatalf("ashford second speak: %v", err)
	}
	if got := visitorSharedWithForTest(t, w, "vstr-ashford"); len(got) != 1 {
		t.Fatalf("PayloadSharedWith after repeat speak = %v, want no duplicate", got)
	}

	// Once Mary joins the conversation and the traveler speaks again, she is
	// stamped too.
	if _, err := w.Send(sim.SpeakTo("mary", "What word was that, now?", "", nil, true, base.Add(3*time.Second))); err != nil {
		t.Fatalf("mary speak: %v", err)
	}
	if _, err := w.Send(sim.SpeakTo("vstr-ashford", "They say Josiah Thorne turned out meat.", "", nil, true, base.Add(4*time.Second))); err != nil {
		t.Fatalf("ashford third speak: %v", err)
	}
	got = visitorSharedWithForTest(t, w, "vstr-ashford")
	if len(got) != 2 {
		t.Fatalf("PayloadSharedWith after mary joined = %v, want both peers", got)
	}
}

// TestRoadWordSharedReachesPublishedSnapshot covers the live actor →
// Published() boundary (code_review): the stamp written by a real SpeakTo must
// be readable off the production published snapshot — where perception's
// roadWordSharedWith actually reads it — and the published copy must be
// INDEPENDENT of the live world, so a later world-side write cannot appear in a
// snapshot already handed to a perception build.
func TestRoadWordSharedReachesPublishedSnapshot(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "vstr-ashford", displayName: "Brother Ashford the provisioner", kind: sim.KindNPCShared, huddleID: "h1"},
		{id: "hannah", displayName: "Hannah Boggs", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()

	seedVisitorStateForTest(t, w, "vstr-ashford", &sim.VisitorState{
		Archetype: "provisioner",
		Payload:   "Josiah Thorne turned out meat for the inn",
	})

	base := time.Now().UTC()
	if _, err := w.Send(sim.SpeakTo("hannah", "Good evening, stranger.", "", nil, true, base)); err != nil {
		t.Fatalf("hannah speak: %v", err)
	}
	if _, err := w.Send(sim.SpeakTo("vstr-ashford", "A word on the road gave me pause, Hannah.", "", nil, true, base.Add(time.Second))); err != nil {
		t.Fatalf("ashford speak: %v", err)
	}

	snap := w.Published()
	vs := snap.Actors["vstr-ashford"].VisitorState
	if vs == nil {
		t.Fatal("published snapshot dropped VisitorState")
	}
	if len(vs.PayloadSharedWith) != 1 || vs.PayloadSharedWith[0] != "hannah" {
		t.Fatalf("published PayloadSharedWith = %v, want [hannah]", vs.PayloadSharedWith)
	}

	// Independence: an in-place world-side write must not show through the
	// snapshot captured above (an append could reallocate and mask aliasing).
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		world.Actors["vstr-ashford"].VisitorState.PayloadSharedWith[0] = "overwritten"
		return nil, nil
	}}); err != nil {
		t.Fatalf("world-side overwrite: %v", err)
	}
	if vs.PayloadSharedWith[0] != "hannah" {
		t.Error("world-side write reached a previously published snapshot — the slice is aliased, not deep-copied")
	}
}

// TestRoadWordSharedRejectedSpeakStampsNothing: the stamp lives AFTER the
// speak gates and the utterance commit, so a rejected SpeakTo must leave the
// memory untouched. The failing speak here is the WORK-370 directed re-ask gate
// (live unanswered edge to the addressee, hasNewNews=false) — chosen because by
// then Mary is an ACTIVE conversant the hook WOULD stamp if it ran, so the
// assertion is probative, not vacuous.
func TestRoadWordSharedRejectedSpeakStampsNothing(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "vstr-ashford", displayName: "Brother Ashford the provisioner", kind: sim.KindNPCShared, huddleID: "h1"},
		{id: "hannah", displayName: "Hannah Boggs", kind: sim.KindNPCShared, huddleID: "h1"},
		{id: "mary", displayName: "Mary", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()

	seedVisitorStateForTest(t, w, "vstr-ashford", &sim.VisitorState{
		Archetype: "provisioner",
		Payload:   "Josiah Thorne turned out meat for the inn",
	})

	base := time.Now().UTC()
	if _, err := w.Send(sim.SpeakTo("hannah", "Good evening, stranger.", "", nil, true, base)); err != nil {
		t.Fatalf("hannah speak: %v", err)
	}
	// The traveler addresses Hannah directly (hasNewNews=false) — commits, stamps
	// Hannah, and opens an unanswered edge to her.
	if _, err := w.Send(sim.SpeakTo("vstr-ashford", "A word on the road gave me pause.", "Hannah Boggs", nil, false, base.Add(time.Second))); err != nil {
		t.Fatalf("ashford first speak: %v", err)
	}
	if got := visitorSharedWithForTest(t, w, "vstr-ashford"); len(got) != 1 || got[0] != "hannah" {
		t.Fatalf("PayloadSharedWith after committed speak = %v, want [hannah]", got)
	}
	// Mary joins the conversation — now an active, UNSTAMPED conversant.
	if _, err := w.Send(sim.SpeakTo("mary", "What word was that?", "", nil, true, base.Add(2*time.Second))); err != nil {
		t.Fatalf("mary speak: %v", err)
	}
	// The traveler re-addresses Hannah while she still owes him an answer, on a
	// tick with no new news — the WORK-370 gate must reject this speak.
	if _, err := w.Send(sim.SpeakTo("vstr-ashford", "As I was saying of the road.", "Hannah Boggs", nil, false, base.Add(3*time.Second))); err == nil {
		t.Fatal("re-ask speak unexpectedly committed — fixture no longer exercises a rejected speak")
	}
	// The rejected speak stamped no one: Mary stays untold.
	if got := visitorSharedWithForTest(t, w, "vstr-ashford"); len(got) != 1 || got[0] != "hannah" {
		t.Fatalf("PayloadSharedWith after rejected speak = %v, want unchanged [hannah]", got)
	}
}

// TestRoadWordSharedSkipsStaleHuddleMember: a huddle member id with NO live
// actor behind it (only reachable through an invariant breach or a mid-tick
// deletion) must never become a stamp, even with an utterance timestamp in the
// ring — junk stamps eat cap space and persist in the plan jsonb.
func TestRoadWordSharedSkipsStaleHuddleMember(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "vstr-ashford", displayName: "Brother Ashford the provisioner", kind: sim.KindNPCShared, huddleID: "h1"},
		{id: "hannah", displayName: "Hannah Boggs", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()

	seedVisitorStateForTest(t, w, "vstr-ashford", &sim.VisitorState{
		Archetype: "provisioner",
		Payload:   "Josiah Thorne turned out meat for the inn",
	})

	base := time.Now().UTC()
	// Forge the breach: a member id in the huddle, with an utterance in the ring,
	// and no entry in World.Actors.
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		h := world.Huddles["h1"]
		h.Members["ghost"] = struct{}{}
		h.AppendUtterance("ghost", "Ghost", "boo", base, sim.SpeechID(999))
		return nil, nil
	}}); err != nil {
		t.Fatalf("forge stale member: %v", err)
	}
	if _, err := w.Send(sim.SpeakTo("hannah", "Good evening.", "", nil, true, base.Add(time.Second))); err != nil {
		t.Fatalf("hannah speak: %v", err)
	}
	if _, err := w.Send(sim.SpeakTo("vstr-ashford", "A word on the road gave me pause.", "", nil, true, base.Add(2*time.Second))); err != nil {
		t.Fatalf("ashford speak: %v", err)
	}
	got := visitorSharedWithForTest(t, w, "vstr-ashford")
	if len(got) != 1 || got[0] != "hannah" {
		t.Fatalf("PayloadSharedWith = %v, want [hannah] only — the actor-less huddle member must not be stamped", got)
	}
}

// TestRoadWordSharedRequiresPayload: a traveler with no carried word stamps
// nothing — there is no matter to spend.
func TestRoadWordSharedRequiresPayload(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "vstr-elias", displayName: "Elias Drum the peddler", kind: sim.KindNPCShared, huddleID: "h1"},
		{id: "hannah", displayName: "Hannah Boggs", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()

	seedVisitorStateForTest(t, w, "vstr-elias", &sim.VisitorState{Archetype: "peddler"})

	base := time.Now().UTC()
	if _, err := w.Send(sim.SpeakTo("hannah", "Good evening.", "", nil, true, base)); err != nil {
		t.Fatalf("hannah speak: %v", err)
	}
	if _, err := w.Send(sim.SpeakTo("vstr-elias", "And to you.", "", nil, true, base.Add(time.Second))); err != nil {
		t.Fatalf("elias speak: %v", err)
	}
	if got := visitorSharedWithForTest(t, w, "vstr-elias"); len(got) != 0 {
		t.Fatalf("PayloadSharedWith with empty Payload = %v, want empty", got)
	}
}

// TestRoadWordSharedNonVisitorSpeakerNoop: a resident speaker never gains
// the memory — the stamp is scoped to a traveler carrying a payload, and the
// hook must tolerate every ordinary speaker without effect.
func TestRoadWordSharedNonVisitorSpeakerNoop(t *testing.T) {
	w, stop := buildPayWithItemWorld(t, "h1", "sc1", []pwiActor{
		{id: "hannah", displayName: "Hannah Boggs", kind: sim.KindNPCShared, huddleID: "h1"},
		{id: "john", displayName: "John Ellis", kind: sim.KindNPCShared, huddleID: "h1"},
	})
	defer stop()

	base := time.Now().UTC()
	if _, err := w.Send(sim.SpeakTo("john", "Evening, Hannah.", "", nil, true, base)); err != nil {
		t.Fatalf("john speak: %v", err)
	}
	if _, err := w.Send(sim.SpeakTo("hannah", "Evening, John.", "", nil, true, base.Add(time.Second))); err != nil {
		t.Fatalf("hannah speak: %v", err)
	}
	// Neither actor is a visitor; the read helper reports nil for both.
	if got := visitorSharedWithForTest(t, w, "hannah"); len(got) != 0 {
		t.Fatalf("resident speaker gained PayloadSharedWith = %v, want none", got)
	}
}
