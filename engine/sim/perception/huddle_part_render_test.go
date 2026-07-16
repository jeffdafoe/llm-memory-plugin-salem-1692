package perception

import (
	"strings"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// --- huddle join/leave peer lines (LLM-438) --------------------------------

// huddlePartLine renders a HuddlePartReason warrant through the full
// renderWarrantLine dispatch with a canned name map, so the tests exercise
// the same path production takes (case selection included).
func huddlePartLine(t *testing.T, kind sim.WarrantKind, peers []sim.ActorID, names map[sim.ActorID]string) string {
	t.Helper()
	nameOf := func(id sim.ActorID) string {
		if label, ok := names[id]; ok {
			return label
		}
		return "someone"
	}
	line, truncated := renderWarrantLine(1, sim.WarrantMeta{
		TriggerActorID: "self",
		Reason:         sim.HuddlePartReason{K: kind, PeerIDs: peers},
	}, nameOf, func(string) string { return "" }, func(string) string { return "" }, func(sim.ItemKind) bool { return false }, func(sim.ItemKind) (bool, bool) { return false, false }, 200)
	if truncated {
		t.Error("huddle part line reported truncation — it has no free-text payload")
	}
	return line
}

func TestRenderWarrantLine_HuddlePartPeers(t *testing.T) {
	names := map[sim.ActorID]string{
		"mercy":    "Mercy Lewis",
		"john":     "John Ellis",
		"tabitha":  "a stranger",
		"stranger": "a stranger",
		"smith":    "the blacksmith",
	}

	cases := []struct {
		label string
		kind  sim.WarrantKind
		peers []sim.ActorID
		want  string
	}{
		{
			label: "left with one acquainted peer",
			kind:  sim.WarrantKindHuddleLeft,
			peers: []sim.ActorID{"mercy"},
			want:  "1. You left the conversation with Mercy Lewis.\n",
		},
		{
			label: "left with acquainted peer and a stranger",
			kind:  sim.WarrantKindHuddleLeft,
			peers: []sim.ActorID{"mercy", "tabitha"},
			want:  "1. You left the conversation with Mercy Lewis and a stranger.\n",
		},
		{
			label: "joined with role-known peer",
			kind:  sim.WarrantKindHuddleJoined,
			peers: []sim.ActorID{"smith"},
			want:  "1. You joined a conversation with the blacksmith.\n",
		},
		{
			// Two unacquainted peers merge into one stranger phrase — never
			// "a stranger and a stranger".
			label: "left with two strangers",
			kind:  sim.WarrantKindHuddleLeft,
			peers: []sim.ActorID{"tabitha", "stranger"},
			want:  "1. You left the conversation with two strangers.\n",
		},
		{
			// Long list caps at two labels plus "and others".
			label: "joined a full room",
			kind:  sim.WarrantKindHuddleJoined,
			peers: []sim.ActorID{"mercy", "john", "smith"},
			want:  "1. You joined a conversation with Mercy Lewis, John Ellis, and others.\n",
		},
		{
			// A duplicated ID (impossible from the set-derived stamp sites,
			// but the phrase guards it) neither double-names a peer nor
			// inflates the stranger count.
			label: "left with duplicated peer ids",
			kind:  sim.WarrantKindHuddleLeft,
			peers: []sim.ActorID{"mercy", "mercy", "tabitha", "tabitha"},
			want:  "1. You left the conversation with Mercy Lewis and a stranger.\n",
		},
		{
			// Empty peer list (lone-member dissolve) keeps the bare sentence.
			label: "left alone",
			kind:  sim.WarrantKindHuddleLeft,
			peers: nil,
			want:  "1. You left the conversation.\n",
		},
		{
			// Every peer gone from the snapshot — nothing to name, bare
			// sentence rather than "with someone".
			label: "joined, peers unresolvable",
			kind:  sim.WarrantKindHuddleJoined,
			peers: []sim.ActorID{"ghost"},
			want:  "1. You joined a conversation.\n",
		},
	}

	for _, tc := range cases {
		if got := huddlePartLine(t, tc.kind, tc.peers, names); got != tc.want {
			t.Errorf("%s: line = %q, want %q", tc.label, got, tc.want)
		}
	}
}

// TestRenderWarrantLine_HuddlePartNeverLeaksStrangerName pins the
// acquaintance gate end-to-end through Build: the subject left a huddle with
// one acquainted peer and one it never interacted with. The rendered line
// names the acquaintance and shows the other as "a stranger" — the
// unacquainted peer's display name must not appear anywhere in the prompt.
func TestRenderWarrantLine_HuddlePartNeverLeaksStrangerName(t *testing.T) {
	const (
		selfID     = sim.ActorID("hannah")
		mercyID    = sim.ActorID("mercy")
		strangerID = sim.ActorID("tabitha")
	)
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			selfID: {
				Kind:        sim.KindNPCStateful,
				DisplayName: "Hannah Putnam",
				Acquaintances: map[string]sim.Acquaintance{
					"Mercy Lewis": {},
				},
			},
			mercyID:    {Kind: sim.KindNPCStateful, DisplayName: "Mercy Lewis"},
			strangerID: {Kind: sim.KindNPCStateful, DisplayName: "Tabitha Porter"},
		},
	}
	warrants := []sim.WarrantMeta{{
		TriggerActorID: selfID,
		Reason:         sim.HuddlePartReason{K: sim.WarrantKindHuddleLeft, PeerIDs: []sim.ActorID{mercyID, strangerID}},
	}}
	out := combinedPrompt(Render(Build(snap, selfID, warrants), DefaultRenderConfig()))
	if !strings.Contains(out, "You left the conversation with Mercy Lewis and a stranger.") {
		t.Errorf("expected peer-naming left line, got:\n%s", out)
	}
	if strings.Contains(out, "Tabitha") {
		t.Errorf("unacquainted peer's name leaked into the prompt:\n%s", out)
	}
}
