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

// noticeboard_test.go — driver-side tests for the noticeboard
// authoring cascade. Substrate Commands (FetchVillageContext +
// SaveNoticeboardContent + EmitTownCrierAnnouncement) have their own
// test coverage in engine/sim/{village_context_test.go,noticeboard_test.go};
// these tests cover the subscriber gates, prompt construction, and
// the full author-and-save cycle via FakeClient.

func buildNoticeboardCascadeWorld(t *testing.T) (*sim.World, func()) {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"notice-board": {
			ID:           "notice-board",
			Category:     "prop",
			DefaultState: "blank",
			RotationAlgo: sim.RotationAlgoDeterministic,
			States: []sim.AssetState{
				{ID: 30, State: "blank", Tags: []string{"rotatable", "notice-board"}},
				{ID: 31, State: "posted", Tags: []string{"rotatable", "notice-board"}},
			},
		},
		"plain-thing": {
			ID:           "plain-thing",
			Category:     "prop",
			DefaultState: "default",
			States: []sim.AssetState{
				{ID: 40, State: "default"},
				{ID: 41, State: "other"},
			},
		},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"board":     {ID: "board", AssetID: "notice-board", CurrentState: "blank", Pos: sim.WorldPos{X: 320, Y: 320}},
		"non-board": {ID: "non-board", AssetID: "plain-thing", CurrentState: "default", Pos: sim.WorldPos{X: 640, Y: 320}},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	return w, func() {}
}

func runNoticeboardCascadeWorld(t *testing.T, w *sim.World) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { w.Run(ctx); close(done) }()
	return func() { cancel(); <-done }
}

// TestRegisterNoticeboard_NilWorldPanics is a wiring guard regression.
func TestRegisterNoticeboard_NilWorldPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("RegisterNoticeboard(nil, ..., client) did not panic")
		}
	}()
	RegisterNoticeboard(context.Background(), nil, llm.NewFakeClient())
}

// TestRegisterNoticeboard_NilClientPanics is the client-side wiring
// guard regression.
func TestRegisterNoticeboard_NilClientPanics(t *testing.T) {
	w, _ := buildNoticeboardCascadeWorld(t)
	defer func() {
		if r := recover(); r == nil {
			t.Error("RegisterNoticeboard(ctx, w, nil) did not panic")
		}
	}()
	RegisterNoticeboard(context.Background(), w, nil)
}

// TestRunNoticeboardAuthor_HappyPath: drives one authoring cycle
// directly via runNoticeboardAuthor (bypassing the goroutine spawn
// for deterministic test timing). Verifies FakeClient.Complete is
// called with model salem-generic + the saved content matches the
// reply (trimmed).
func TestRunNoticeboardAuthor_HappyPath(t *testing.T) {
	w, _ := buildNoticeboardCascadeWorld(t)
	stop := runNoticeboardCascadeWorld(t, w)
	defer stop()

	client := llm.NewFakeClient(llm.ScriptedTurn{
		Response: llm.Response{Content: "  A travelling cobbler lodges at the Ordinary.  "},
	})

	// Move the board to "posted" so the authoring is for that state.
	// (Authoring is invoked directly; we just want a non-blank state
	// to capture as atState.)
	if _, err := w.Send(sim.SetVillageObjectState("board", "posted", 0)); err != nil {
		t.Fatalf("set state: %v", err)
	}

	runNoticeboardAuthor(context.Background(), w, client, "board", "posted", "Notice Board", "")

	if got := client.CallCount(); got != 1 {
		t.Errorf("client.CallCount = %d, want 1", got)
	}
	reqs := client.Requests()
	if len(reqs) != 1 {
		t.Fatalf("len(reqs) = %d, want 1", len(reqs))
	}
	if got := reqs[0].Model; got != "salem-generic" {
		t.Errorf("Request.Model = %q, want salem-generic", got)
	}
	if len(reqs[0].Tools) != 0 {
		t.Errorf("Request.Tools = %d, want 0 (noticeboard is tool-free)", len(reqs[0].Tools))
	}
	if got := len(reqs[0].Messages); got != 2 {
		t.Fatalf("Messages = %d, want 2 (system + user)", got)
	}
	if reqs[0].Messages[0].Role != llm.RoleSystem || reqs[0].Messages[1].Role != llm.RoleUser {
		t.Errorf("Message roles = [%s, %s], want [system, user]",
			reqs[0].Messages[0].Role, reqs[0].Messages[1].Role)
	}

	// Saved content should be the trimmed reply.
	got := readBoardContent(t, w, "board")
	if got == nil {
		t.Fatal("NoticeboardContent[board] missing after authoring")
	}
	if got.Text != "A travelling cobbler lodges at the Ordinary." {
		t.Errorf("Text = %q, want trimmed reply", got.Text)
	}
	if got.AtState != "posted" {
		t.Errorf("AtState = %q, want posted", got.AtState)
	}
}

// TestRunNoticeboardAuthor_MintsFreshSceneID — each authoring cycle
// issues its own scene_id so memory-api's chat_messages history loader
// isolates one cycle's conversation from the next.
func TestRunNoticeboardAuthor_MintsFreshSceneID(t *testing.T) {
	w, _ := buildNoticeboardCascadeWorld(t)
	stop := runNoticeboardCascadeWorld(t, w)
	defer stop()

	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{Content: "first notice"}},
		llm.ScriptedTurn{Response: llm.Response{Content: "second notice"}},
	)
	if _, err := w.Send(sim.SetVillageObjectState("board", "posted", 0)); err != nil {
		t.Fatalf("set state: %v", err)
	}

	runNoticeboardAuthor(context.Background(), w, client, "board", "posted", "Notice Board", "")
	runNoticeboardAuthor(context.Background(), w, client, "board", "posted", "Notice Board", "")

	reqs := client.Requests()
	if len(reqs) != 2 {
		t.Fatalf("Request count = %d, want 2", len(reqs))
	}
	if reqs[0].SceneID == "" || reqs[1].SceneID == "" {
		t.Fatalf("SceneIDs empty: %q / %q", reqs[0].SceneID, reqs[1].SceneID)
	}
	if reqs[0].SceneID == reqs[1].SceneID {
		t.Errorf("SceneIDs equal across cycles: %q (each call must mint fresh)", reqs[0].SceneID)
	}
}

// TestRunNoticeboardAuthor_EmptyReply: FakeClient returns empty →
// nothing saved.
func TestRunNoticeboardAuthor_EmptyReply(t *testing.T) {
	w, _ := buildNoticeboardCascadeWorld(t)
	stop := runNoticeboardCascadeWorld(t, w)
	defer stop()

	client := llm.NewFakeClient(llm.ScriptedTurn{
		Response: llm.Response{Content: "   \n  "},
	})
	if _, err := w.Send(sim.SetVillageObjectState("board", "posted", 0)); err != nil {
		t.Fatalf("set state: %v", err)
	}
	runNoticeboardAuthor(context.Background(), w, client, "board", "posted", "Notice Board", "")

	if got := readBoardContent(t, w, "board"); got != nil {
		t.Errorf("content saved despite empty reply: %+v", got)
	}
}

// TestRunNoticeboardAuthor_StaleStateDropsSave: the goroutine
// completes the LLM call but the board's state has flipped again
// before save → SkipReason=stale_state, content not stored.
func TestRunNoticeboardAuthor_StaleStateDropsSave(t *testing.T) {
	w, _ := buildNoticeboardCascadeWorld(t)
	stop := runNoticeboardCascadeWorld(t, w)
	defer stop()

	client := llm.NewFakeClient(llm.ScriptedTurn{
		Response: llm.Response{Content: "stale text"},
	})
	// Set state to "posted"; authoring captures "posted" as atState.
	if _, err := w.Send(sim.SetVillageObjectState("board", "posted", 0)); err != nil {
		t.Fatalf("set state: %v", err)
	}
	// But before the goroutine runs, rotate again to "blank".
	if _, err := w.Send(sim.SetVillageObjectState("board", "blank", 0)); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	runNoticeboardAuthor(context.Background(), w, client, "board", "posted", "Notice Board", "")

	if got := readBoardContent(t, w, "board"); got != nil {
		t.Errorf("content saved despite stale state: %+v", got)
	}
}

// TestNoticeboardSubscriber_GatesOnTags: a state change on an object
// whose new state lacks the rotatable+notice-board tag combo does NOT
// trigger the LLM goroutine. Verified by zero CallCount after the
// event is emitted via SetVillageObjectState.
func TestNoticeboardSubscriber_GatesOnTags(t *testing.T) {
	w, _ := buildNoticeboardCascadeWorld(t)

	client := llm.NewFakeClient() // empty script — any call would error
	RegisterNoticeboard(context.Background(), w, client)

	stop := runNoticeboardCascadeWorld(t, w)
	defer stop()

	// Flip the non-board object (plain-thing). New state lacks both
	// tags; subscriber should ignore.
	if _, err := w.Send(sim.SetVillageObjectState("non-board", "other", 0)); err != nil {
		t.Fatalf("set state: %v", err)
	}
	// Yield briefly to give the subscriber a chance to (wrongly)
	// spawn a goroutine. If it did spawn, the FakeClient would
	// error on empty script.
	time.Sleep(50 * time.Millisecond)
	if got := client.CallCount(); got != 0 {
		t.Errorf("FakeClient.CallCount = %d, want 0 (non-board state change)", got)
	}
}

// TestBuildNoticeboardPrompt_IncludesVisitorsAndCatalog: the user
// message renders visitor + business catalog data when provided.
func TestBuildNoticeboardPrompt_IncludesVisitorsAndCatalog(t *testing.T) {
	snap := sim.VillageContext{
		Visitors: []sim.VisitorSummary{
			{DisplayName: "Master Babbage", Archetype: "wandering surgeon", Origin: "Boston"},
		},
		BusinessCatalog: []sim.BusinessOffering{
			{
				OwnerDisplayName: "Hannah Wells",
				StructureLabel:   "Ingersoll's Ordinary",
				Items: []sim.BusinessItem{
					{Item: "ale", Price: 3},
					{Item: "stew", Price: 5},
				},
			},
		},
		PriorAtmosphere: "The village rests under heavy mist.",
	}
	msgs := buildNoticeboardPrompt(snap, "The Notice Board", "Previous notice: a found shawl.")
	if len(msgs) != 2 {
		t.Fatalf("len(msgs) = %d, want 2", len(msgs))
	}
	user := msgs[1].Content
	if !strings.Contains(user, "Master Babbage") {
		t.Errorf("user message missing visitor name; got: %s", user)
	}
	if !strings.Contains(user, "wandering surgeon") {
		t.Errorf("user message missing visitor archetype")
	}
	if !strings.Contains(user, "Boston") {
		t.Errorf("user message missing visitor origin")
	}
	if !strings.Contains(user, "Hannah Wells") {
		t.Errorf("user message missing business owner")
	}
	if !strings.Contains(user, "Ingersoll's Ordinary") {
		t.Errorf("user message missing business structure")
	}
	if !strings.Contains(user, "ale") || !strings.Contains(user, "3 coins") {
		t.Errorf("user message missing item + price; got: %s", user)
	}
	if !strings.Contains(user, "heavy mist") {
		t.Errorf("user message missing atmosphere context")
	}
	if !strings.Contains(user, "found shawl") {
		t.Errorf("user message missing prior text context")
	}
	if !strings.Contains(user, "Do not repeat") {
		t.Errorf("user message missing anti-repeat anchor for prior text")
	}
}

// TestNoticeboardSystemPrompt_AntiSurveillance: the system prompt
// carries the v1-hardened anti-surveillance instructions (the
// fabrication-resistance core).
func TestNoticeboardSystemPrompt_AntiSurveillance(t *testing.T) {
	sys := noticeboardSystemPrompt()
	if !strings.Contains(sys, "DO NOT") {
		t.Error("system prompt missing DO NOT anti-pattern callouts")
	}
	if !strings.Contains(sys, "Surveillance-shaped") {
		t.Error("system prompt missing surveillance-shaped guard")
	}
	// Make sure the example anti-patterns are in there (they're
	// the load-bearing concrete examples).
	if !strings.Contains(sys, "Goodman Reeves is at the forge") {
		t.Error("system prompt missing the at-location surveillance example")
	}
}

// --- helpers ---

func readBoardContent(t *testing.T, w *sim.World, id sim.VillageObjectID) *sim.NoticeboardContent {
	t.Helper()
	res, err := w.Send(sim.Command{Fn: func(world *sim.World) (any, error) {
		if world.NoticeboardContent == nil {
			return (*sim.NoticeboardContent)(nil), nil
		}
		c := world.NoticeboardContent[id]
		if c == nil {
			return (*sim.NoticeboardContent)(nil), nil
		}
		cp := *c
		return &cp, nil
	}})
	if err != nil {
		t.Fatalf("readBoardContent: %v", err)
	}
	return res.(*sim.NoticeboardContent)
}
