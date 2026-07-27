package perception

import (
	"reflect"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// discharged_keys_test.go — LLM-542. CollectDischargedSourceKeys reports which
// stimuli the tick's PROMPT conveyed, so the completion path can discharge the
// warrants they stamped instead of answering the same line twice.

func TestCollectDischargedSourceKeys(t *testing.T) {
	p := Payload{
		ConveyedSpeech: []ConveyedSpeechRef{
			{SpeechID: 41},
			// A PC line stamps the OTHER speech kind — the key needs both halves.
			{SpeechID: 42, SpeakerIsPC: true},
			// Same event conveyed twice collapses to one key.
			{SpeechID: 41},
			// Recorded outside the emit path — no event to key on.
			{SpeechID: 0},
		},
	}

	got := CollectDischargedSourceKeys(p, nil)
	want := []sim.WarrantSourceKey{
		{Kind: sim.WarrantKindNPCSpoke, Discriminator: 41},
		{Kind: sim.WarrantKindPCSpoke, Discriminator: 42},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CollectDischargedSourceKeys() = %+v, want %+v", got, want)
	}
}

func TestCollectDischargedSourceKeys_NothingToDischarge(t *testing.T) {
	cases := map[string]Payload{
		"no conveyed speech": {},
		"unidentified lines only": {ConveyedSpeech: []ConveyedSpeechRef{
			{SpeechID: 0},
		}},
		// RecentConversation alone is not the source — the collector reads
		// ConveyedSpeech, which build.go populates from the same ring.
		"rendered lines without a conveyance record": {RecentConversation: []UtteranceView{
			{SpeakerName: "Elizabeth", Text: "What brings you out this midday?"},
		}},
	}
	for name, p := range cases {
		if got := CollectDischargedSourceKeys(p, nil); got != nil {
			t.Errorf("%s: CollectDischargedSourceKeys() = %+v, want nil", name, got)
		}
	}
}

// TestBuildRecentConversation_ConveysDedupedAndCappedLines pins the
// distinction the discharge rests on (code_review, LLM-542):
//
//   - a line the heardNow de-dup drops is NOT rendered here but IS conveyed —
//     its text is in the prompt under "## Since your last turn", so a warrant
//     it stamped has been answered;
//   - a line PAST the maxRenderedConversationLines window is conveyed too when
//     the de-dup covers it — the window bounds what this section shows, not
//     what the prompt contains;
//   - a line past the window that nothing else carried is neither rendered nor
//     conveyed — its warrant is still owed;
//   - the subject's own lines are never conveyed (an actor does not warrant
//     itself for its own speech).
func TestBuildRecentConversation_ConveysDedupedAndCappedLines(t *testing.T) {
	const me = sim.ActorID("gideon")
	base := sim.Utterance{SpeakerID: "elizabeth", SpeakerName: "Elizabeth"}

	ring := make([]sim.Utterance, 0, 8)
	// Two lines beyond the render cap of 5. The first is covered by heardNow —
	// its text IS in the prompt under the other heading, so it must be
	// conveyed even though this section would never show it (code_review).
	// The second is carried by nothing and must not be conveyed.
	oldHeard := base
	oldHeard.Text = "already heard"
	oldHeard.SpeechID = 10
	oldSilent := base
	oldSilent.Text = "old chatter"
	oldSilent.SpeechID = 11
	ring = append(ring, oldHeard, oldSilent)
	// Five that survive the cap: one self line, one de-duped by heardNow, three plain.
	self := sim.Utterance{SpeakerID: me, SpeakerName: "Gideon", Text: "Good day.", SpeechID: 20}
	deduped := base
	deduped.Text = "already heard"
	deduped.SpeechID = 21
	plain := []sim.Utterance{}
	for i := 0; i < 3; i++ {
		u := base
		u.Text = "line " + string(rune('a'+i))
		u.SpeechID = sim.SpeechID(30 + i)
		plain = append(plain, u)
	}
	ring = append(ring, self, deduped)
	ring = append(ring, plain...)

	snap := &sim.Snapshot{
		Huddles: map[sim.HuddleID]*sim.Huddle{"h1": {ID: "h1", RecentUtterances: ring}},
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			me:          {Kind: sim.KindNPCStateful},
			"elizabeth": {Kind: sim.KindNPCShared},
		},
	}
	support := sim.WarrantSourceKey{Kind: sim.WarrantKindNPCSpoke, Discriminator: 900}
	heardNow := map[sim.ActorID]map[string]sim.WarrantSourceKey{"elizabeth": {"already heard": support}}

	views, conveyed := buildRecentConversation(snap, me, &sim.ActorSnapshot{CurrentHuddleID: "h1"}, heardNow)

	for _, v := range views {
		if v.Text == "already heard" {
			t.Error("de-duped line rendered into ## Recent conversation here")
		}
		if v.Text == "old chatter" {
			t.Error("line past the render cap leaked into the view")
		}
	}

	got := map[sim.SpeechID]bool{}
	for _, c := range conveyed {
		got[c.SpeechID] = true
	}
	if !got[21] {
		t.Error("de-duped line inside the window not conveyed — its warrant would fire a second reply")
	}
	if !got[10] {
		t.Error("de-duped line PAST the window not conveyed — its text is in the prompt under the other heading")
	}
	for _, id := range []sim.SpeechID{30, 31, 32} {
		if !got[id] {
			t.Errorf("rendered line %d not conveyed", id)
		}
	}
	if got[11] {
		t.Error("a line past the window that nothing carried was reported as conveyed — it was never shown")
	}
	if got[20] {
		t.Error("the subject's own line was reported as conveyed")
	}
}

// TestBuildRecentConversation_DedupedLinesRecordTheirSupportingWarrant: a line
// dropped by the de-dup reached the prompt ONLY through the warrant whose
// excerpt matched it, so the conveyance record must name that warrant. Build
// runs before Render and cannot know whether the warrant survives the prompt
// caps — CollectDischargedSourceKeys settles it (code_review).
func TestBuildRecentConversation_DedupedLinesRecordTheirSupportingWarrant(t *testing.T) {
	const me = sim.ActorID("gideon")
	support := sim.WarrantSourceKey{Kind: sim.WarrantKindNPCSpoke, Discriminator: 900}
	ring := []sim.Utterance{
		{SpeakerID: "elizabeth", SpeakerName: "Elizabeth", Text: "already heard", SpeechID: 21},
		{SpeakerID: "elizabeth", SpeakerName: "Elizabeth", Text: "a fresh line", SpeechID: 22},
	}
	snap := &sim.Snapshot{
		Huddles: map[sim.HuddleID]*sim.Huddle{"h1": {ID: "h1", RecentUtterances: ring}},
		Actors:  map[sim.ActorID]*sim.ActorSnapshot{"elizabeth": {Kind: sim.KindNPCShared}},
	}
	heardNow := map[sim.ActorID]map[string]sim.WarrantSourceKey{"elizabeth": {"already heard": support}}

	_, conveyed := buildRecentConversation(snap, me, &sim.ActorSnapshot{CurrentHuddleID: "h1"}, heardNow)

	byID := map[sim.SpeechID]ConveyedSpeechRef{}
	for _, c := range conveyed {
		byID[c.SpeechID] = c
	}
	if got := byID[21].ViaWarrant; got != support {
		t.Errorf("de-duped line ViaWarrant = %+v, want %+v — its only carrier", got, support)
	}
	if got := byID[22].ViaWarrant; got != (sim.WarrantSourceKey{}) {
		t.Errorf("line rendered in its own right has ViaWarrant %+v, want zero", got)
	}
}

// TestCollectDischargedSourceKeys_DroppedWarrantUncarriesItsLine: the prompt
// caps (MaxWarrants / MaxSectionBytes) can drop a consumed warrant AFTER Build
// decided a ring line was de-duped against it. Then the text reached the prompt
// through neither heading, so the line is still owed and must not be
// discharged. The dropped warrant itself carries forward as usual.
func TestCollectDischargedSourceKeys_DroppedWarrantUncarriesItsLine(t *testing.T) {
	support := sim.WarrantSourceKey{Kind: sim.WarrantKindNPCSpoke, Discriminator: 900}
	p := Payload{ConveyedSpeech: []ConveyedSpeechRef{
		{SpeechID: 21, ViaWarrant: support}, // carried only by the warrant below
		{SpeechID: 22},                      // rendered in its own right
	}}
	droppedSupport := []sim.WarrantMeta{{
		Reason: sim.NPCSpeechWarrantReason{SpeechID: 900, Speaker: "elizabeth", Excerpt: "already heard"},
	}}

	got := CollectDischargedSourceKeys(p, droppedSupport)
	want := []sim.WarrantSourceKey{{Kind: sim.WarrantKindNPCSpoke, Discriminator: 22}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("with its carrier dropped: got %+v, want %+v", got, want)
	}

	// Control: the same payload with the warrant surviving discharges both.
	if got := CollectDischargedSourceKeys(p, nil); len(got) != 2 {
		t.Errorf("with its carrier rendered: got %+v, want both keys", got)
	}
}
