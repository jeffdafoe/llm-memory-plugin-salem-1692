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

	got := CollectDischargedSourceKeys(p)
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
		if got := CollectDischargedSourceKeys(p); got != nil {
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
//   - a line cut by the maxRenderedConversationLines cap is neither rendered
//     nor conveyed — nothing carried it, so its warrant is still owed;
//   - the subject's own lines are never conveyed (an actor does not warrant
//     itself for its own speech).
func TestBuildRecentConversation_ConveysDedupedAndCappedLines(t *testing.T) {
	const me = sim.ActorID("gideon")
	base := sim.Utterance{SpeakerID: "elizabeth", SpeakerName: "Elizabeth"}

	ring := make([]sim.Utterance, 0, 8)
	// Two lines beyond the render cap of 5 — dropped entirely.
	for i := 0; i < 2; i++ {
		u := base
		u.Text = "old chatter"
		u.SpeechID = sim.SpeechID(10 + i)
		ring = append(ring, u)
	}
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
	heardNow := map[sim.ActorID]map[string]bool{"elizabeth": {"already heard": true}}

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
		t.Error("de-duped line not conveyed — its warrant would fire a second reply")
	}
	for _, id := range []sim.SpeechID{30, 31, 32} {
		if !got[id] {
			t.Errorf("rendered line %d not conveyed", id)
		}
	}
	if got[10] || got[11] {
		t.Error("a line cut by the render cap was reported as conveyed — it was never shown")
	}
	if got[20] {
		t.Error("the subject's own line was reported as conveyed")
	}
}
