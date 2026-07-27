package perception

import (
	"reflect"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// discharged_keys_test.go — LLM-542. CollectDischargedSourceKeys reports which
// stimuli the tick's PROMPT contained, so the completion path can discharge the
// warrants they stamped instead of answering the same line twice.

func TestCollectDischargedSourceKeys(t *testing.T) {
	p := Payload{
		RecentConversation: []UtteranceView{
			// The subject's own line — it warranted nobody's tick but its own
			// listeners', never itself.
			{SpeakerName: "Gideon", Text: "Good day, Goody Ellis.", IsSelf: true, SpeechID: 40},
			{SpeakerName: "Elizabeth", Text: "What brings you out this midday?", SpeechID: 41},
			// A PC line stamps the OTHER speech kind — the key needs both halves.
			{SpeakerName: "Jeff", Text: "Constable, a word.", SpeechID: 42, SpeakerIsPC: true},
			// Same event rendered twice (ring + a re-entry) collapses to one key.
			{SpeakerName: "Elizabeth", Text: "What brings you out this midday?", SpeechID: 41},
			// Recorded outside the emit path — no event to key on.
			{SpeakerName: "Elizabeth", Text: "hand-built line", SpeechID: 0},
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
		"no conversation": {},
		"self only": {RecentConversation: []UtteranceView{
			{SpeakerName: "Gideon", Text: "Good day.", IsSelf: true, SpeechID: 40},
		}},
		"unidentified lines only": {RecentConversation: []UtteranceView{
			{SpeakerName: "Elizabeth", Text: "hand-built line"},
		}},
	}
	for name, p := range cases {
		if got := CollectDischargedSourceKeys(p); got != nil {
			t.Errorf("%s: CollectDischargedSourceKeys() = %+v, want nil", name, got)
		}
	}
}
