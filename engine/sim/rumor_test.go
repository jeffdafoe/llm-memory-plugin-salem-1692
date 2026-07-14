package sim

import (
	"math/rand"
	"strings"
	"testing"
	"time"
)

// rumor_test.go — LLM-371: the grounded rumor a spawning traveler carries.
// renderRumorClause maps one action-log beat to a diegetic past-tense clause (or
// "" for a beat not worth carrying); selectVisitorRumor filters the action log to
// recent, rumor-worthy beats about real residents and picks one. Together they are
// what gives the stateless salem-visitor VA something true to trade instead of
// empty small-talk.

func rumorTestWorld() *World {
	return &World{
		Actors: map[ActorID]*Actor{
			"smith": {ID: "smith", DisplayName: "Ezekiel Crane", Kind: KindNPCStateful},
			"alice": {ID: "alice", DisplayName: "Goodwife Alice", Kind: KindNPCShared},
			"pc":    {ID: "pc", DisplayName: "The Player", Kind: KindPC},
			"prop":  {ID: "prop", DisplayName: "A Cart", Kind: KindDecorative},
			"trav":  {ID: "trav", DisplayName: "Elias Drum the peddler", Kind: KindNPCShared, VisitorState: &VisitorState{Archetype: "peddler", Phase: VisitorPhasePresent}},
		},
	}
}

func TestRenderRumorClause(t *testing.T) {
	w := rumorTestWorld()
	cases := []struct {
		name  string
		entry ActionLogEntry
		want  string // exact match; "" means the beat renders no rumor
	}{
		{"paid_full", ActionLogEntry{ActorID: "smith", ActionType: ActionTypePaid, CounterpartyName: "Goodwife Alice", Text: "a mended kettle"},
			"Ezekiel Crane settled up with Goodwife Alice over a mended kettle"},
		{"paid_no_text", ActionLogEntry{ActorID: "smith", ActionType: ActionTypePaid, CounterpartyName: "Goodwife Alice"},
			"Ezekiel Crane settled up with Goodwife Alice"},
		{"paid_no_counterparty", ActionLogEntry{ActorID: "smith", ActionType: ActionTypePaid, Amount: 4},
			""},
		{"delivered_full", ActionLogEntry{ActorID: "smith", ActionType: ActionTypeDelivered, Text: "a plow", CounterpartyName: "the Hale farm"},
			"Ezekiel Crane turned out a plow for the Hale farm"},
		{"delivered_no_text", ActionLogEntry{ActorID: "smith", ActionType: ActionTypeDelivered, CounterpartyName: "Alice"},
			""},
		{"labored_full", ActionLogEntry{ActorID: "alice", ActionType: ActionTypeLabored, CounterpartyName: "Ezekiel Crane", Amount: 6},
			"Goodwife Alice put in a day's work for Ezekiel Crane"},
		{"labored_no_counterparty", ActionLogEntry{ActorID: "alice", ActionType: ActionTypeLabored},
			"Goodwife Alice took on a piece of work"},
		{"hired_full", ActionLogEntry{ActorID: "smith", ActionType: ActionTypeHired, CounterpartyName: "Goodwife Alice"},
			"Ezekiel Crane took Goodwife Alice on for a job"},
		{"hired_no_counterparty", ActionLogEntry{ActorID: "smith", ActionType: ActionTypeHired},
			""},
		{"solicited_full", ActionLogEntry{ActorID: "alice", ActionType: ActionTypeSolicitedWork, CounterpartyName: "Ezekiel Crane"},
			"Goodwife Alice went looking to work for Ezekiel Crane"},
		// Non-rumor-worthy beats all degrade to "".
		{"spoke", ActionLogEntry{ActorID: "smith", ActionType: ActionTypeSpoke, Text: "good morrow"}, ""},
		{"consumed", ActionLogEntry{ActorID: "smith", ActionType: ActionTypeConsumed, Text: "porridge"}, ""},
		{"walked", ActionLogEntry{ActorID: "smith", ActionType: ActionTypeWalked, Text: "The Tavern"}, ""},
		{"took_break", ActionLogEntry{ActorID: "smith", ActionType: ActionTypeTookBreak}, ""},
		{"negotiation_offered", ActionLogEntry{ActorID: "smith", ActionType: ActionTypeOffered, CounterpartyName: "Alice"}, ""},
		// Unknown subject renders nothing even for a rumor-worthy type.
		{"unknown_subject", ActionLogEntry{ActorID: "ghost", ActionType: ActionTypePaid, CounterpartyName: "Alice"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderRumorClause(w, tc.entry); got != tc.want {
				t.Errorf("renderRumorClause = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRenderRumorClause_ArticleBeats covers the two beats that route a place name
// through WithDefiniteArticle — asserted by prefix so the test doesn't re-implement
// the article rule.
func TestRenderRumorClause_ArticleBeats(t *testing.T) {
	w := rumorTestWorld()
	gathered := renderRumorClause(w, ActionLogEntry{ActorID: "alice", ActionType: ActionTypeGathered, Text: "firewood", CounterpartyName: "woodpile"})
	if !strings.HasPrefix(gathered, "Goodwife Alice was out gathering firewood at ") {
		t.Errorf("gathered clause = %q", gathered)
	}
	repairing := renderRumorClause(w, ActionLogEntry{ActorID: "smith", ActionType: ActionTypeRepairing, Text: "smithy"})
	if !strings.HasPrefix(repairing, "Ezekiel Crane was mending ") {
		t.Errorf("repairing clause = %q", repairing)
	}
}

// TestSnapshotActorCarriesRumorPayload guards the live Actor -> ActorSnapshot copy
// path (snapshotActor -> cloneVisitorState). Perception reads the rumor off
// ActorSnapshot.VisitorState.Payload, so if the clone ever dropped the field (e.g.
// a refactor from the current whole-struct copy to a field-by-field literal) a
// spawned traveler would persist the rumor but never actually voice it. This pins
// the field through the real snapshot builder, not a hand-built ActorSnapshot.
func TestSnapshotActorCarriesRumorPayload(t *testing.T) {
	const rumor = "Ezekiel Crane turned out a plow for the Hale farm"
	a := &Actor{
		ID:          "vstr-0000abcd",
		DisplayName: "Elias Drum the peddler",
		Kind:        KindNPCShared,
		Needs:       seedVisitorNeeds(),
		Inventory:   map[ItemKind]int{},
		VisitorState: &VisitorState{
			Archetype: "peddler", Origin: "Boston", Disposition: "weary",
			Phase: VisitorPhasePresent, Payload: rumor,
		},
	}
	snap := snapshotActor(a, 0, false)
	if snap.VisitorState == nil {
		t.Fatal("snapshot dropped VisitorState")
	}
	if snap.VisitorState.Payload != rumor {
		t.Errorf("snapshot Payload = %q; want the rumor carried through to perception", snap.VisitorState.Payload)
	}
}

func TestSelectVisitorRumor(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	r := rand.New(rand.NewSource(1))
	recent := now.Add(-time.Hour)
	stale := now.Add(-VisitorRumorLookback - time.Hour)

	t.Run("empty log", func(t *testing.T) {
		w := rumorTestWorld()
		if got := selectVisitorRumor(w, r, now); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("picks a rumor-worthy resident beat", func(t *testing.T) {
		w := rumorTestWorld()
		w.ActionLog = []ActionLogEntry{
			{ActorID: "smith", OccurredAt: recent, ActionType: ActionTypeDelivered, Text: "a plow", CounterpartyName: "the Hale farm"},
		}
		if got := selectVisitorRumor(w, r, now); got != "Ezekiel Crane turned out a plow for the Hale farm" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("skips non-resident and non-rumor subjects", func(t *testing.T) {
		w := rumorTestWorld()
		w.ActionLog = []ActionLogEntry{
			{ActorID: "trav", OccurredAt: recent, ActionType: ActionTypeDelivered, Text: "trinkets", CounterpartyName: "Alice"}, // a visitor
			{ActorID: "pc", OccurredAt: recent, ActionType: ActionTypePaid, CounterpartyName: "Alice"},                          // the player
			{ActorID: "prop", OccurredAt: recent, ActionType: ActionTypeDelivered, Text: "x"},                                   // decorative
			{ActorID: "ghost", OccurredAt: recent, ActionType: ActionTypePaid, CounterpartyName: "Alice"},                       // no such actor
			{ActorID: "smith", OccurredAt: recent, ActionType: ActionTypeSpoke, Text: "hello"},                                  // dull beat
		}
		if got := selectVisitorRumor(w, r, now); got != "" {
			t.Errorf("got %q, want empty (no eligible resident rumor)", got)
		}
	})

	t.Run("skips beats older than the lookback", func(t *testing.T) {
		w := rumorTestWorld()
		w.ActionLog = []ActionLogEntry{
			{ActorID: "smith", OccurredAt: stale, ActionType: ActionTypeDelivered, Text: "a plow", CounterpartyName: "Alice"},
		}
		if got := selectVisitorRumor(w, r, now); got != "" {
			t.Errorf("got %q, want empty (beat is stale)", got)
		}
	})
}
