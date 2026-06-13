package sim_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim/repo/mem"
)

// buildNoticeboardTestWorld seeds a minimal world for noticeboard +
// VillageObjectStateChanged tests. One actor (`crier`) carrying
// AttrTownCrier, one noticeboard at variant-1 state. Returns the world
// + event/spoke recorders + stop func. Recorders are subscribed
// BEFORE Run starts (per the Subscribe contract).
func buildNoticeboardTestWorld(t *testing.T) (*sim.World, *eventRecorder, *spokeRecorder, func()) {
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
	})
	handles.VillageObjects.Seed(map[sim.VillageObjectID]*sim.VillageObject{
		"board": {ID: "board", AssetID: "notice-board", CurrentState: "blank", Pos: sim.WorldPos{X: 320, Y: 320}},
	})
	handles.Actors.Seed(map[sim.ActorID]*sim.Actor{
		"crier": {
			ID:          "crier",
			DisplayName: "The Crier",
			Kind:        sim.KindNPCShared,
			Attributes:  map[string][]byte{sim.AttrTownCrier: {}},
		},
		"non-crier": {
			ID:          "non-crier",
			DisplayName: "Just A Villager",
			Kind:        sim.KindNPCShared,
		},
	})

	w, err := sim.LoadWorld(context.Background(), repo)
	if err != nil {
		t.Fatalf("LoadWorld: %v", err)
	}

	evtRec := &eventRecorder{}
	spokeRec := &spokeRecorder{}
	w.Subscribe(sim.SubscriberFunc(evtRec.handle))
	w.Subscribe(sim.SubscriberFunc(spokeRec.handle))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()
	return w, evtRec, spokeRec, func() { cancel(); <-done }
}

// TestSetVillageObjectStateEmitsEvent pins the VillageObjectStateChanged
// event emission on an applied flip.
func TestSetVillageObjectStateEmitsEvent(t *testing.T) {
	w, rec, _, stop := buildNoticeboardTestWorld(t)
	defer stop()

	if _, err := w.Send(sim.SetVillageObjectState("board", "posted")); err != nil {
		t.Fatalf("SetVillageObjectState: %v", err)
	}
	got := rec.collect()
	if len(got) == 0 {
		t.Fatal("no VillageObjectStateChanged event emitted")
	}
	last := got[len(got)-1]
	if last.ObjectID != "board" || last.FromState != "blank" || last.ToState != "posted" {
		t.Errorf("event = %+v, want ObjectID=board From=blank To=posted", last)
	}
}

// TestSetVillageObjectStateNoEventOnNoOp confirms the event does NOT
// fire on already_at_target / superseded / not_found.
func TestSetVillageObjectStateNoEventOnNoOp(t *testing.T) {
	w, rec, _, stop := buildNoticeboardTestWorld(t)
	defer stop()

	// Same-state set — should be already_at_target, no event.
	if _, err := w.Send(sim.SetVillageObjectState("board", "blank")); err != nil {
		t.Fatalf("SetVillageObjectState: %v", err)
	}
	if got := rec.collect(); len(got) != 0 {
		t.Errorf("emitted %d events on no-op, want 0", len(got))
	}
}

// TestSaveNoticeboardContent_HappyPath: state matches atState, content
// stored with trimmed text.
func TestSaveNoticeboardContent_HappyPath(t *testing.T) {
	w, _, _, stop := buildNoticeboardTestWorld(t)
	defer stop()

	now := time.Now().UTC()
	res, err := w.Send(sim.SaveNoticeboardContent("board", "  Hear ye, hear ye  ", "blank", now))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	r := res.(sim.SaveNoticeboardContentResult)
	if !r.Applied {
		t.Errorf("Applied = false, want true (skip reason %q)", r.SkipReason)
	}

	got := readNoticeboardContent(t, w, "board")
	if got == nil {
		t.Fatal("NoticeboardContent[board] missing after save")
	}
	if got.Text != "Hear ye, hear ye" {
		t.Errorf("Text = %q, want trimmed", got.Text)
	}
	if got.AtState != "blank" {
		t.Errorf("AtState = %q, want blank", got.AtState)
	}
}

// TestSaveNoticeboardContent_EmitsContentChanged (ZBBS-HOME-293): the
// Applied=true path emits NoticeboardContentChanged carrying the stored
// (trimmed) text + posted-at, so the WS layer can live-update an open modal.
func TestSaveNoticeboardContent_EmitsContentChanged(t *testing.T) {
	w, rec, _, stop := buildNoticeboardTestWorld(t)
	defer stop()

	now := time.Now().UTC()
	res, err := w.Send(sim.SaveNoticeboardContent("board", "  Town meeting at dusk.  ", "blank", now))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !res.(sim.SaveNoticeboardContentResult).Applied {
		t.Fatal("Applied = false, want true")
	}

	got := rec.collectContent()
	if len(got) != 1 {
		t.Fatalf("emitted %d NoticeboardContentChanged, want 1", len(got))
	}
	ev := got[0]
	if ev.ObjectID != "board" || ev.Text != "Town meeting at dusk." || !ev.PostedAt.Equal(now) {
		t.Errorf("event = {ObjectID:%q Text:%q PostedAt:%v}, want {board, trimmed, %v}",
			ev.ObjectID, ev.Text, ev.PostedAt, now)
	}
	// At must equal PostedAt (== the command time) — one mutation, one clock
	// (code_review #2), not a fresh time.Now() read.
	if !ev.At.Equal(ev.PostedAt) {
		t.Errorf("At = %v, want == PostedAt %v (single command clock)", ev.At, ev.PostedAt)
	}
}

// TestSaveNoticeboardContent_NoContentEventOnSkip: the skip paths
// (stale_state / not_found / empty_after_trim) emit NO NoticeboardContentChanged.
func TestSaveNoticeboardContent_NoContentEventOnSkip(t *testing.T) {
	w, rec, _, stop := buildNoticeboardTestWorld(t)
	defer stop()

	// stale_state (board is "blank", author for "posted").
	w.Send(sim.SaveNoticeboardContent("board", "stale", "posted", time.Now()))
	// not_found.
	w.Send(sim.SaveNoticeboardContent("ghost", "nope", "blank", time.Now()))
	// empty_after_trim.
	w.Send(sim.SaveNoticeboardContent("board", "   ", "blank", time.Now()))

	if got := rec.collectContent(); len(got) != 0 {
		t.Errorf("emitted %d NoticeboardContentChanged on skip paths, want 0", len(got))
	}
}

// TestSaveNoticeboardContent_StaleState: atState doesn't match current
// state → skip with stale_state.
func TestSaveNoticeboardContent_StaleState(t *testing.T) {
	w, _, _, stop := buildNoticeboardTestWorld(t)
	defer stop()

	res, _ := w.Send(sim.SaveNoticeboardContent("board", "stale content", "posted", time.Now()))
	r := res.(sim.SaveNoticeboardContentResult)
	if r.Applied || r.SkipReason != "stale_state" {
		t.Errorf("result = %+v, want Applied=false stale_state", r)
	}
	if got := readNoticeboardContent(t, w, "board"); got != nil {
		t.Error("content stored despite stale_state")
	}
}

// TestSaveNoticeboardContent_NotFound: unknown ID → skip with
// not_found.
func TestSaveNoticeboardContent_NotFound(t *testing.T) {
	w, _, _, stop := buildNoticeboardTestWorld(t)
	defer stop()

	res, _ := w.Send(sim.SaveNoticeboardContent("ghost", "some text", "blank", time.Now()))
	r := res.(sim.SaveNoticeboardContentResult)
	if r.Applied || r.SkipReason != "not_found" {
		t.Errorf("result = %+v, want Applied=false not_found", r)
	}
}

// TestSaveNoticeboardContent_EmptyText: whitespace-only → skip with
// empty_after_trim.
func TestSaveNoticeboardContent_EmptyText(t *testing.T) {
	w, _, _, stop := buildNoticeboardTestWorld(t)
	defer stop()

	res, _ := w.Send(sim.SaveNoticeboardContent("board", "   \n  ", "blank", time.Now()))
	r := res.(sim.SaveNoticeboardContentResult)
	if r.Applied || r.SkipReason != "empty_after_trim" {
		t.Errorf("result = %+v, want Applied=false empty_after_trim", r)
	}
}

// TestSaveNoticeboardContent_TruncatesLongText: text over
// MaxNoticeboardContentLen rune-truncated.
func TestSaveNoticeboardContent_TruncatesLongText(t *testing.T) {
	w, _, _, stop := buildNoticeboardTestWorld(t)
	defer stop()

	long := strings.Repeat("a", sim.MaxNoticeboardContentLen+50)
	res, _ := w.Send(sim.SaveNoticeboardContent("board", long, "blank", time.Now()))
	r := res.(sim.SaveNoticeboardContentResult)
	if !r.Applied {
		t.Fatalf("Applied = false, want true (reason %q)", r.SkipReason)
	}
	got := readNoticeboardContent(t, w, "board")
	if got == nil {
		t.Fatal("content missing")
	}
	if runes := []rune(got.Text); len(runes) != sim.MaxNoticeboardContentLen {
		t.Errorf("Text rune-len = %d, want %d (truncation cap)",
			len(runes), sim.MaxNoticeboardContentLen)
	}
}

// readNoticeboardContent reads World.NoticeboardContent[id] via a
// Command (no Snapshot field today). Returns nil if absent.
func readNoticeboardContent(t *testing.T, w *sim.World, id sim.VillageObjectID) *sim.NoticeboardContent {
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
		t.Fatalf("readNoticeboardContent: %v", err)
	}
	return res.(*sim.NoticeboardContent)
}

// TestEmitTownCrierAnnouncement_HappyPath: emits Spoke with the crier
// as speaker, empty huddle + recipients (atmospheric), trimmed text.
func TestEmitTownCrierAnnouncement_HappyPath(t *testing.T) {
	w, _, rec, stop := buildNoticeboardTestWorld(t)
	defer stop()

	now := time.Now().UTC()
	res, err := w.Send(sim.EmitTownCrierAnnouncement("crier", "  Today's news!  ", now))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	r := res.(sim.EmitTownCrierAnnouncementResult)
	if !r.Fired {
		t.Errorf("Fired = false (skip %q), want true", r.SkipReason)
	}

	got := rec.collect()
	if len(got) != 1 {
		t.Fatalf("Spoke events = %d, want 1", len(got))
	}
	e := got[0]
	if e.SpeakerID != "crier" {
		t.Errorf("SpeakerID = %q, want crier", e.SpeakerID)
	}
	if e.HuddleID != "" {
		t.Errorf("HuddleID = %q, want empty (atmospheric)", e.HuddleID)
	}
	if len(e.RecipientIDs) != 0 {
		t.Errorf("RecipientIDs = %+v, want empty (atmospheric)", e.RecipientIDs)
	}
	if e.Text != "Today's news!" {
		t.Errorf("Text = %q, want trimmed", e.Text)
	}
}

// TestEmitTownCrierAnnouncement_EmptyContent: whitespace → skip
// without emitting.
func TestEmitTownCrierAnnouncement_EmptyContent(t *testing.T) {
	w, _, rec, stop := buildNoticeboardTestWorld(t)
	defer stop()

	res, _ := w.Send(sim.EmitTownCrierAnnouncement("crier", "   ", time.Now()))
	r := res.(sim.EmitTownCrierAnnouncementResult)
	if r.Fired || r.SkipReason != "empty_content" {
		t.Errorf("result = %+v, want Fired=false empty_content", r)
	}
	if got := rec.collect(); len(got) != 0 {
		t.Errorf("emitted %d Spoke events on empty content, want 0", len(got))
	}
}

// TestEmitTownCrierAnnouncement_SpeakerMissing: unknown SpeakerID → skip.
func TestEmitTownCrierAnnouncement_SpeakerMissing(t *testing.T) {
	w, _, _, stop := buildNoticeboardTestWorld(t)
	defer stop()

	res, _ := w.Send(sim.EmitTownCrierAnnouncement("ghost", "anything", time.Now()))
	r := res.(sim.EmitTownCrierAnnouncementResult)
	if r.Fired || r.SkipReason != "speaker_missing" {
		t.Errorf("result = %+v, want Fired=false speaker_missing", r)
	}
}

// TestEmitTownCrierAnnouncement_SpeakerNotTownCrier: actor exists but
// lacks AttrTownCrier → skip with speaker_not_town_crier.
func TestEmitTownCrierAnnouncement_SpeakerNotTownCrier(t *testing.T) {
	w, _, _, stop := buildNoticeboardTestWorld(t)
	defer stop()

	res, _ := w.Send(sim.EmitTownCrierAnnouncement("non-crier", "anything", time.Now()))
	r := res.(sim.EmitTownCrierAnnouncementResult)
	if r.Fired || r.SkipReason != "speaker_not_town_crier" {
		t.Errorf("result = %+v, want Fired=false speaker_not_town_crier", r)
	}
}

// TestContentCapacityForState (ZBBS-HOME-456): a content-capacity-N tag parses
// to N; no tag / malformed / negative / duplicate / nil all yield 0.
func TestContentCapacityForState(t *testing.T) {
	cases := []struct {
		name  string
		state *sim.AssetState
		want  int
	}{
		{"nil state", nil, 0},
		{"no capacity tag", &sim.AssetState{Tags: []string{"rotatable", "notice-board"}}, 0},
		{"capacity 1", &sim.AssetState{Tags: []string{"notice-board", "content-capacity-1"}}, 1},
		{"capacity 4", &sim.AssetState{Tags: []string{"content-capacity-4"}}, 4},
		{"capacity 0 explicit", &sim.AssetState{Tags: []string{"content-capacity-0"}}, 0},
		{"malformed suffix", &sim.AssetState{Tags: []string{"content-capacity-x"}}, 0},
		{"negative", &sim.AssetState{Tags: []string{"content-capacity--2"}}, 0},
		{"duplicate tags", &sim.AssetState{Tags: []string{"content-capacity-2", "content-capacity-3"}}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sim.ContentCapacityForState(c.state); got != c.want {
				t.Errorf("ContentCapacityForState = %d, want %d", got, c.want)
			}
		})
	}
}

// TestClampNoticeboardContent (ZBBS-HOME-456): splits on newlines, drops blank
// lines, caps the line count to maxLines, truncates each line to maxLineLen
// runes, rejoins with "\n".
func TestClampNoticeboardContent(t *testing.T) {
	if got := sim.ClampNoticeboardContent("a\nb\n\n  \nc\nd\ne", 3, 240); got != "a\nb\nc" {
		t.Errorf("over-count clamp = %q, want %q", got, "a\nb\nc")
	}
	if got := sim.ClampNoticeboardContent(strings.Repeat("x", 300), 2, 10); got != strings.Repeat("x", 10) {
		t.Errorf("per-line truncation = %q, want 10 runes", got)
	}
	if got := sim.ClampNoticeboardContent("   \n  \n", 3, 240); got != "" {
		t.Errorf("blank input = %q, want empty", got)
	}
	if got := sim.ClampNoticeboardContent("a\nb", 0, 240); got != "" {
		t.Errorf("zero maxLines = %q, want empty", got)
	}
}

// TestClearNoticeboardContent (ZBBS-HOME-456): clears stored content, emits a
// NoticeboardContentChanged with empty Text, honours the stale-state guard, and
// reports nothing_to_clear when there's nothing stored.
func TestClearNoticeboardContent(t *testing.T) {
	w, evtRec, _, stop := buildNoticeboardTestWorld(t)
	defer stop()

	now := time.Now()
	if _, err := w.Send(sim.SaveNoticeboardContent("board", "A notice.", "blank", now)); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Stale atState → not cleared, content remains.
	res, _ := w.Send(sim.ClearNoticeboardContent("board", "posted", now))
	if r := res.(sim.SaveNoticeboardContentResult); r.Applied || r.SkipReason != "stale_state" {
		t.Errorf("stale clear = %+v, want Applied=false stale_state", r)
	}
	if readNoticeboardContent(t, w, "board") == nil {
		t.Fatal("content gone after stale clear (should remain)")
	}

	// Matching atState → cleared.
	res, _ = w.Send(sim.ClearNoticeboardContent("board", "blank", now))
	if r := res.(sim.SaveNoticeboardContentResult); !r.Applied {
		t.Errorf("clear = %+v, want Applied=true", r)
	}
	if got := readNoticeboardContent(t, w, "board"); got != nil {
		t.Errorf("content present after clear: %+v", got)
	}
	var sawEmpty bool
	for _, e := range evtRec.collectContent() {
		if e.ObjectID == "board" && e.Text == "" {
			sawEmpty = true
		}
	}
	if !sawEmpty {
		t.Error("no empty-Text NoticeboardContentChanged emitted on clear")
	}

	// Second clear → nothing_to_clear.
	res, _ = w.Send(sim.ClearNoticeboardContent("board", "blank", now))
	if r := res.(sim.SaveNoticeboardContentResult); r.Applied || r.SkipReason != "nothing_to_clear" {
		t.Errorf("re-clear = %+v, want Applied=false nothing_to_clear", r)
	}
}

// --- helpers ---

type eventRecorder struct {
	mu      sync.Mutex
	events  []sim.VillageObjectStateChanged
	content []sim.NoticeboardContentChanged
}

func (r *eventRecorder) handle(_ *sim.World, evt sim.Event) {
	switch e := evt.(type) {
	case *sim.VillageObjectStateChanged:
		r.mu.Lock()
		r.events = append(r.events, *e)
		r.mu.Unlock()
	case *sim.NoticeboardContentChanged:
		r.mu.Lock()
		r.content = append(r.content, *e)
		r.mu.Unlock()
	}
}

func (r *eventRecorder) collect() []sim.VillageObjectStateChanged {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sim.VillageObjectStateChanged(nil), r.events...)
}

func (r *eventRecorder) collectContent() []sim.NoticeboardContentChanged {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sim.NoticeboardContentChanged(nil), r.content...)
}

type spokeRecorder struct {
	mu     sync.Mutex
	events []sim.Spoke
}

func (r *spokeRecorder) handle(_ *sim.World, evt sim.Event) {
	if e, ok := evt.(*sim.Spoke); ok {
		r.mu.Lock()
		r.events = append(r.events, *e)
		r.mu.Unlock()
	}
}

func (r *spokeRecorder) collect() []sim.Spoke {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sim.Spoke(nil), r.events...)
}
