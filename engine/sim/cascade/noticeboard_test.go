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
// the shared author helper (authorNoticeboardText) via FakeClient.

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
				// "blank"/"posted" carry capacity 1 — the pre-456 single-notice
				// behaviour the existing tests assert.
				{ID: 30, State: "blank", Tags: []string{"rotatable", "notice-board", "content-capacity-1"}},
				{ID: 31, State: "posted", Tags: []string{"rotatable", "notice-board", "content-capacity-1"}},
				// "empty" carries no capacity tag (0) — the empty-board sprite that
				// clears content on rotation. "three" holds 3 notices (multi-line).
				{ID: 32, State: "empty", Tags: []string{"rotatable", "notice-board"}},
				{ID: 33, State: "three", Tags: []string{"rotatable", "notice-board", "content-capacity-3"}},
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

// buildGappedCapacityBoardWorld seeds a notice board whose content-capacity
// frames skip 1 — the live Notice Board's actual slip set {0,2,3,4,5} (LLM-49,
// the sheet has no single-slip art). Used to exercise noticeboardStateForCapacity's
// snap-DOWN behaviour for counts with no exact frame.
func buildGappedCapacityBoardWorld(t *testing.T) *sim.World {
	t.Helper()
	repo, handles := mem.NewRepository()
	handles.Assets.Seed(map[sim.AssetID]*sim.Asset{
		"notice-board": {
			ID: "notice-board", Category: "prop", DefaultState: "empty",
			RotationAlgo: sim.RotationAlgoDeterministic,
			States: []sim.AssetState{
				{ID: 60, State: "empty", Tags: []string{"rotatable", "notice-board"}},                       // capacity 0
				{ID: 62, State: "two", Tags: []string{"rotatable", "notice-board", "content-capacity-2"}},   // 2
				{ID: 63, State: "three", Tags: []string{"rotatable", "notice-board", "content-capacity-3"}}, // 3
				{ID: 64, State: "four", Tags: []string{"rotatable", "notice-board", "content-capacity-4"}},  // 4
				{ID: 65, State: "five", Tags: []string{"rotatable", "notice-board", "content-capacity-5"}},  // 5
			},
		},
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"board": {ID: "board", AssetID: "notice-board", CurrentState: "empty", Pos: sim.WorldPos{X: 320, Y: 320}},
	})
	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}
	return w
}

// TestNoticeboardStateForCapacity_SnapsDownToAvailableFrame is the LLM-49 core:
// with a sheet that has no 1-slip frame, a requested count resolves to the
// largest available capacity <= it (so slips drawn never exceed notices voiced),
// a lone notice falls to the empty board, and an over-large count clamps to the
// fullest frame.
func TestNoticeboardStateForCapacity_SnapsDownToAvailableFrame(t *testing.T) {
	w := buildGappedCapacityBoardWorld(t)
	cases := []struct {
		want      int
		wantState string
		wantCap   int
	}{
		{0, "empty", 0},
		{1, "empty", 0}, // no 1-slip frame -> snap down to the empty board
		{2, "two", 2},
		{3, "three", 3},
		{4, "four", 4},
		{5, "five", 5},
		{6, "five", 5}, // over the fullest frame -> clamp to it
		{9, "five", 5},
	}
	for _, c := range cases {
		gotState, gotCap := noticeboardStateForCapacity(w, "board", c.want)
		if gotState != c.wantState || gotCap != c.wantCap {
			t.Errorf("noticeboardStateForCapacity(want=%d) = (%q, %d), want (%q, %d)",
				c.want, gotState, gotCap, c.wantState, c.wantCap)
		}
	}
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

// TestAuthorNoticeboardText_HappyPath: drives one authoring cycle via
// authorNoticeboardText (the shared author helper the crier uses).
// Verifies FakeClient.Complete is called with model salem-generic, a
// [system, user] message pair, no tools, and the returned content is the
// trimmed reply.
func TestAuthorNoticeboardText_HappyPath(t *testing.T) {
	w, _ := buildNoticeboardCascadeWorld(t)
	stop := runNoticeboardCascadeWorld(t, w)
	defer stop()

	client := llm.NewFakeClient(llm.ScriptedTurn{
		Response: llm.Response{Content: "  A travelling cobbler lodges at the Ordinary.  "},
	})

	// capacity/atState pass straight through; authorNoticeboardText returns
	// the clamped text and does not itself store anything.
	got := authorNoticeboardText(context.Background(), w, client, "board", "posted", "Notice Board", "", 1)
	if got != "A travelling cobbler lodges at the Ordinary." {
		t.Errorf("returned text = %q, want the trimmed reply", got)
	}

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
}

// TestAuthorNoticeboardText_MintsFreshSceneID — each authoring cycle
// issues its own scene_id so memory-api's chat_messages history loader
// isolates one cycle's conversation from the next.
func TestAuthorNoticeboardText_MintsFreshSceneID(t *testing.T) {
	w, _ := buildNoticeboardCascadeWorld(t)
	stop := runNoticeboardCascadeWorld(t, w)
	defer stop()

	client := llm.NewFakeClient(
		llm.ScriptedTurn{Response: llm.Response{Content: "first notice"}},
		llm.ScriptedTurn{Response: llm.Response{Content: "second notice"}},
	)

	authorNoticeboardText(context.Background(), w, client, "board", "posted", "Notice Board", "", 1)
	authorNoticeboardText(context.Background(), w, client, "board", "posted", "Notice Board", "", 1)

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

// TestAuthorNoticeboardText_EmptyReply: FakeClient returns whitespace →
// the helper returns "" (nothing for the caller to store).
func TestAuthorNoticeboardText_EmptyReply(t *testing.T) {
	w, _ := buildNoticeboardCascadeWorld(t)
	stop := runNoticeboardCascadeWorld(t, w)
	defer stop()

	client := llm.NewFakeClient(llm.ScriptedTurn{
		Response: llm.Response{Content: "   \n  "},
	})
	if got := authorNoticeboardText(context.Background(), w, client, "board", "posted", "Notice Board", "", 1); got != "" {
		t.Errorf("returned %q, want empty string on empty reply", got)
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
	if _, err := w.Send(sim.SetVillageObjectState("non-board", "other")); err != nil {
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
	msgs := buildNoticeboardPrompt(snap, "The Notice Board", "Previous notice: a found shawl.", 1)
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
	sys := noticeboardSystemPrompt(1)
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

// TestAuthorNoticeboardText_MultiLineCapacity (ZBBS-HOME-456): a capacity-3
// board is authored as up to 3 newline-separated notices, the prompt asks for
// 3, and an over-producing model is clamped down to 3.
func TestAuthorNoticeboardText_MultiLineCapacity(t *testing.T) {
	w, _ := buildNoticeboardCascadeWorld(t)
	stop := runNoticeboardCascadeWorld(t, w)
	defer stop()

	// Five lines back from the model — the capacity-3 clamp must keep 3.
	client := llm.NewFakeClient(llm.ScriptedTurn{
		Response: llm.Response{Content: "A town meeting is called for Friday next.\nWolves are seen upon the Andover road.\nA plain shawl was left at the meeting house.\nThe surgeon lodges at the Ordinary.\nHands are wanted at the Whittredge raising."},
	})

	got := authorNoticeboardText(context.Background(), w, client, "board", "three", "Notice Board", "", 3)
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("returned %d lines, want 3 (capacity clamp):\n%s", len(lines), got)
	}
	for i, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			t.Errorf("line %d empty after clamp", i)
		}
	}
	reqs := client.Requests()
	if len(reqs) != 1 {
		t.Fatalf("reqs = %d, want 1", len(reqs))
	}
	if !strings.Contains(reqs[0].Messages[0].Content, "exactly 3 notices") {
		t.Errorf("system prompt missing the 3-notice output instruction; got:\n%s", reqs[0].Messages[0].Content)
	}
}

// TestNoticeboardSubscriber_ClearsOnZeroCapacityState (ZBBS-HOME-456): rotating
// a board to a state with no content-capacity tag clears any prior content and
// does NOT spawn an authoring call (v1 cleared content on flip to an empty
// board sprite).
func TestNoticeboardSubscriber_ClearsOnZeroCapacityState(t *testing.T) {
	w, _ := buildNoticeboardCascadeWorld(t)
	client := llm.NewFakeClient() // empty script — a stray author would error
	RegisterNoticeboard(context.Background(), w, client)
	stop := runNoticeboardCascadeWorld(t, w)
	defer stop()

	// Seed content while the board sits in a capacity-1 state ("blank").
	if _, err := w.Send(sim.SaveNoticeboardContent("board", "A shawl found at the meeting house.", "blank", time.Now())); err != nil {
		t.Fatalf("seed content: %v", err)
	}
	// Rotate to the zero-capacity sprite — the subscriber clears inline.
	if _, err := w.Send(sim.SetVillageObjectState("board", "empty")); err != nil {
		t.Fatalf("flip to empty: %v", err)
	}

	deadline := time.After(2 * time.Second)
	for readBoardContent(t, w, "board") != nil {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for content to clear on zero-capacity flip")
		case <-time.After(10 * time.Millisecond):
		}
	}
	time.Sleep(50 * time.Millisecond) // give any (buggy) author goroutine a beat
	if got := client.CallCount(); got != 0 {
		t.Errorf("CallCount = %d, want 0 (zero-capacity flip must not author)", got)
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
