package cascade

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/llm"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// crier_post_read_test.go — LLM-44 author-first town-crier behavior: the crier
// authors today's notices, posts them (the board variant follows the number
// actually authored), reads those notices aloud, then advances — so she reads
// exactly what she posts, not the prior cycle's stale notice. A no-news day
// (target = the empty, zero-capacity variant) passes by silently.

// crierBoardWalkTo is the tile the crier stands on to read the board. The
// fixture seeds the crier there and the synthesized routes use it as WalkTo so
// the active-phase stale-arrival guard accepts the arrival.
var crierBoardWalkTo = sim.Position{X: sim.PadX + 30, Y: sim.PadY + 21}

// buildCrierBoardWorld stands up a minimal world for the crier author/post/read
// path: a notice board whose rotatable states carry the full capacity ladder
// (empty=0, one=1, two=2, three=3), one board object, and a town-crier actor
// standing at the board. The new crier tests install routes directly, so this
// fixture deliberately does NOT wire RegisterNPCRoutes/RegisterNoticeboard.
// defaultCrierBoardStates is the contiguous capacity ladder (empty=0,1,2,3) the
// original crier tests assert against.
func defaultCrierBoardStates() []sim.AssetState {
	return []sim.AssetState{
		{ID: 50, State: "empty", Tags: []string{"rotatable", "notice-board"}},
		{ID: 51, State: "one", Tags: []string{"rotatable", "notice-board", "content-capacity-1"}},
		{ID: 52, State: "two", Tags: []string{"rotatable", "notice-board", "content-capacity-2"}},
		{ID: 53, State: "three", Tags: []string{"rotatable", "notice-board", "content-capacity-3"}},
	}
}

func buildCrierBoardWorld(t *testing.T) *sim.World {
	return buildCrierBoardWorldWithBoardStates(t, defaultCrierBoardStates())
}

func buildCrierBoardWorldWithBoardStates(t *testing.T, boardStates []sim.AssetState) *sim.World {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Terrain.Seed(allGrassTerrain())
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"house": {
			ID: "house", Category: "structure", DefaultState: "default",
			DoorOffsetX: intp(0), DoorOffsetY: intp(2),
			States: []sim.AssetState{{ID: 1, State: "default"}},
		},
		"notice-board": {
			ID: "notice-board", Category: "prop", DefaultState: "empty",
			RotationAlgo: sim.RotationAlgoRandomPerObject,
			States: boardStates,
		},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"home":   {ID: "home", AssetID: "house", Pos: sim.WorldPos{X: 320, Y: 320}},
		"notice": {ID: "notice", AssetID: "notice-board", CurrentState: "empty", Pos: sim.WorldPos{X: 960, Y: 640}},
	})
	handles.Structures.Seed(map[sim.StructureID]*sim.Structure{
		"home": {ID: "home", DisplayName: "Home"},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"crier": {
			ID:              "crier",
			DisplayName:     "Town Crier",
			Kind:            sim.KindNPCShared,
			Pos:             sim.TilePos{X: crierBoardWalkTo.X, Y: crierBoardWalkTo.Y},
			HomeStructureID: "home",
			Attributes:      map[string][]byte{sim.AttrTownCrier: {}},
		},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	w.Settings.Location = time.UTC
	return w
}

// installCrierRoute installs a one-stop active town_crier route at the "notice"
// board with the given target state. Direct mutation — call BEFORE the world
// goroutine starts.
func installCrierRoute(w *sim.World, target string) {
	if w.ActiveRoutes == nil {
		w.ActiveRoutes = map[sim.ActorID]*sim.NPCRoute{}
	}
	w.ActiveRoutes["crier"] = &sim.NPCRoute{
		NPCID:           "crier",
		Label:           sim.AttrTownCrier,
		Stops:           []sim.RouteStop{{ObjectID: "notice", WalkTo: crierBoardWalkTo, NewState: target}},
		StopIdx:         0,
		Phase:           sim.RoutePhaseActive,
		HomeDestination: sim.NewPositionDestination(sim.Position{X: sim.PadX + 10, Y: sim.PadY + 10}),
	}
}

// crierArrives synthesizes the crier's arrival at the board and dispatches the
// route subscriber directly (the LLM author runs off-world, so the post/read
// land asynchronously — callers poll).
func crierArrives(t *testing.T, w *sim.World, client llm.Client) {
	t.Helper()
	evt := &sim.ActorArrived{ActorID: "crier", FinalPosition: crierBoardWalkTo, At: time.Now()}
	if _, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		handleActorArrivedAdvanceRoute(context.Background(), world, evt, client)
		return nil, nil
	}}); err != nil {
		t.Fatalf("invoke arrival handler: %v", err)
	}
}

// boardState reads a village object's CurrentState inside a Command.
func boardState(t *testing.T, w *sim.World, id sim.VillageObjectID) string {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		obj, ok := world.VillageObjects[id]
		if !ok || obj == nil {
			return "", nil
		}
		return obj.CurrentState, nil
	}})
	if err != nil {
		t.Fatalf("boardState: %v", err)
	}
	return res.(string)
}

// routePhaseOf reads an actor's active-route phase ("" when no route).
func routePhaseOf(t *testing.T, w *sim.World, id sim.ActorID) string {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		r, ok := world.ActiveRoutes[id]
		if !ok || r == nil {
			return "", nil
		}
		return string(r.Phase), nil
	}})
	if err != nil {
		t.Fatalf("routePhaseOf: %v", err)
	}
	return res.(string)
}

// pollBoardContent waits up to 2s for the crier's authored content to land.
func pollBoardContent(t *testing.T, w *sim.World) *sim.NoticeboardContent {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if got := readBoardContent(t, w, "notice"); got != nil {
			return got
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for the crier to post content")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestCrierReadsNoticesItPosts: the crier authors today's notices, posts them,
// and reads THOSE — not the stale prior notice — and the board variant matches
// the number authored. This is the core LLM-44 regression: the old flow read
// the prior cycle's posting then flipped to a random variant.
func TestCrierReadsNoticesItPosts(t *testing.T) {
	w := buildCrierBoardWorld(t)
	installCrierRoute(w, "two") // target capacity 2
	// A stale prior posting she must NOT read.
	w.NoticeboardContent = map[sim.VillageObjectID]*sim.NoticeboardContent{
		"notice": {Text: "Yesterday's stale proclamation.", PostedAt: time.Now(), AtState: "empty"},
	}
	spokeRec := &cascadeSpokeRecorder{}
	w.Subscribe(sim.SubscriberFunc(spokeRec.handle))
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	const want = "A town meeting is called for Friday next.\nWolves are seen upon the Andover road."
	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{Content: want}})
	crierArrives(t, w, client)

	// The stale seed is replaced asynchronously; wait for the authored variant
	// to land (state flips to two) before reading, so the poll doesn't return
	// the pre-existing stale content.
	deadline := time.After(2 * time.Second)
	for boardState(t, w, "notice") != "two" {
		select {
		case <-deadline:
			t.Fatalf("crier never posted the authored notices (board still %q)", boardState(t, w, "notice"))
		case <-time.After(10 * time.Millisecond):
		}
	}
	got := readBoardContent(t, w, "notice")
	if got == nil {
		t.Fatal("no content after the crier posted")
	}
	if got.Text != want {
		t.Errorf("posted content = %q, want the freshly authored notices %q", got.Text, want)
	}
	if got.AtState != "two" {
		t.Errorf("content AtState = %q, want two", got.AtState)
	}
	if st := boardState(t, w, "notice"); st != "two" {
		t.Errorf("board variant = %q, want two (variant follows the 2 authored notices)", st)
	}
	spokes := spokeRec.collect()
	if len(spokes) == 0 {
		t.Fatal("crier emitted no Spoke — she should read the notices she posts")
	}
	if spokes[0].Text != "A town meeting is called for Friday next." {
		t.Errorf("first spoken notice = %q, want the first authored notice (not the stale prior)", spokes[0].Text)
	}
	if calls := client.CallCount(); calls != 1 {
		t.Errorf("client.CallCount = %d, want 1", calls)
	}
}

// TestCrierVariantFollowsAuthoredCount: when the model under-produces (2 lines
// for a 3-slot target), the board lands on the capacity-2 variant — drawn =
// read = shown — not the 3-slot target.
func TestCrierVariantFollowsAuthoredCount(t *testing.T) {
	w := buildCrierBoardWorld(t)
	installCrierRoute(w, "three") // target capacity 3
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	client := llm.NewFakeClient(llm.ScriptedTurn{
		Response: llm.Response{Content: "First notice.\nSecond notice."}, // only 2
	})
	crierArrives(t, w, client)

	got := pollBoardContent(t, w)
	if lines := strings.Split(got.Text, "\n"); len(lines) != 2 {
		t.Fatalf("stored %d lines, want 2:\n%s", len(lines), got.Text)
	}
	if st := boardState(t, w, "notice"); st != "two" {
		t.Errorf("board variant = %q, want two (matches the 2 actually authored, not the 3-slot target)", st)
	}
	if got.AtState != "two" {
		t.Errorf("content AtState = %q, want two", got.AtState)
	}
}

// TestCrierNoNewsDayPassesSilently: a no-news day (target = the empty,
// zero-capacity variant) posts the empty board, clears any prior content, makes
// no LLM call, and emits no Spoke — she just moves along.
func TestCrierNoNewsDayPassesSilently(t *testing.T) {
	w := buildCrierBoardWorld(t)
	// Board carries a prior posting that today's empty day should clear.
	w.VillageObjects["notice"].CurrentState = "one"
	w.NoticeboardContent = map[sim.VillageObjectID]*sim.NoticeboardContent{
		"notice": {Text: "Old news.", PostedAt: time.Now(), AtState: "one"},
	}
	installCrierRoute(w, "empty") // target capacity 0
	spokeRec := &cascadeSpokeRecorder{}
	w.Subscribe(sim.SubscriberFunc(spokeRec.handle))
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	client := llm.NewFakeClient() // empty script — any LLM call would error
	crierArrives(t, w, client)

	// The no-news path is synchronous in the arrival handler, but poll the
	// state to be robust against scheduling.
	deadline := time.After(2 * time.Second)
	for boardState(t, w, "notice") != "empty" {
		select {
		case <-deadline:
			t.Fatalf("board did not flip to empty on a no-news day (still %q)", boardState(t, w, "notice"))
		case <-time.After(10 * time.Millisecond):
		}
	}
	if c := readBoardContent(t, w, "notice"); c != nil {
		t.Errorf("content not cleared on a no-news day: %+v", c)
	}
	if calls := client.CallCount(); calls != 0 {
		t.Errorf("client.CallCount = %d, want 0 (a no-news day authors nothing)", calls)
	}
	if spokes := spokeRec.collect(); len(spokes) != 0 {
		t.Errorf("emitted %d Spoke(s) on a no-news day, want 0", len(spokes))
	}
}

// TestCrierAdvancesAfterReading: after authoring, posting, and reading, the
// route advances (the deferred skip-flip advance fires once the spiel's dwell
// elapses) — she doesn't stand at the board forever. crierNoticeBeatDelay is
// shrunk so the timer fires in milliseconds.
func TestCrierAdvancesAfterReading(t *testing.T) {
	orig := crierNoticeBeatDelay
	crierNoticeBeatDelay = 5 * time.Millisecond
	defer func() { crierNoticeBeatDelay = orig }()

	w := buildCrierBoardWorld(t)
	installCrierRoute(w, "two")
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	client := llm.NewFakeClient(llm.ScriptedTurn{
		Response: llm.Response{Content: "Notice one.\nNotice two."},
	})
	crierArrives(t, w, client)

	// The single board stop is the last stop, so advancing past it transitions
	// the route to its returning (home) leg — or clears it if the home walk
	// can't dispatch. Either is "advanced past the board".
	deadline := time.After(2 * time.Second)
	for {
		switch routePhaseOf(t, w, "crier") {
		case string(sim.RoutePhaseReturning), "":
			return // advanced off the board stop
		}
		select {
		case <-deadline:
			t.Fatal("route did not advance after the crier finished reading")
		case <-time.After(3 * time.Millisecond):
		}
	}
}

// TestCrierLoneNoticeWithNoOneSlipFrameSnapsToEmpty (LLM-49): on a board whose
// sheet has no 1-slip frame {0,2,3}, an authoring round that yields a single
// notice snaps DOWN to the empty board — prior content cleared, no speech,
// route advances — rather than drawing a slip count that disagrees with the
// lone notice. She still authored (one LLM call), unlike a no-news day.
func TestCrierLoneNoticeWithNoOneSlipFrameSnapsToEmpty(t *testing.T) {
	w := buildCrierBoardWorldWithBoardStates(t, []sim.AssetState{
		{ID: 50, State: "empty", Tags: []string{"rotatable", "notice-board"}},
		{ID: 52, State: "two", Tags: []string{"rotatable", "notice-board", "content-capacity-2"}},
		{ID: 53, State: "three", Tags: []string{"rotatable", "notice-board", "content-capacity-3"}},
	})
	// A prior posting the lone-notice snap must clear.
	w.NoticeboardContent = map[sim.VillageObjectID]*sim.NoticeboardContent{
		"notice": {Text: "Old news.", PostedAt: time.Now(), AtState: "empty"},
	}
	installCrierRoute(w, "three") // target 3 slots, but the model yields only 1
	spokeRec := &cascadeSpokeRecorder{}
	w.Subscribe(sim.SubscriberFunc(spokeRec.handle))
	cancel := runRouteCascadeWorld(t, w)
	defer cancel()

	client := llm.NewFakeClient(llm.ScriptedTurn{Response: llm.Response{Content: "A lone proclamation."}})
	crierArrives(t, w, client)

	// Snap-to-empty advances the route inline (no spiel dwell); wait for it to
	// leave the board stop.
	deadline := time.After(2 * time.Second)
	advanced := false
	for !advanced {
		switch routePhaseOf(t, w, "crier") {
		case string(sim.RoutePhaseReturning), "":
			advanced = true
		default:
			select {
			case <-deadline:
				t.Fatalf("route did not advance off the lone-notice stop (phase %q)", routePhaseOf(t, w, "crier"))
			case <-time.After(3 * time.Millisecond):
			}
		}
	}

	if st := boardState(t, w, "notice"); st != "empty" {
		t.Errorf("board state = %q, want empty (lone notice with no 1-slip frame snaps to empty)", st)
	}
	if c := readBoardContent(t, w, "notice"); c != nil && strings.TrimSpace(c.Text) != "" {
		t.Errorf("content = %q, want cleared", c.Text)
	}
	if spokes := spokeRec.collect(); len(spokes) != 0 {
		t.Errorf("voiced %d notice(s), want 0 (no speech on the empty snap)", len(spokes))
	}
	if calls := client.CallCount(); calls != 1 {
		t.Errorf("client.CallCount = %d, want 1 (she authored, then snapped to empty)", calls)
	}
}
