package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// coin_dealings_test.go — LLM-572. Build gating and phrasing for the
// "## Coin between you and those here" section.

func coinDealingsFixture(t *testing.T, subject *sim.ActorSnapshot, peers map[sim.ActorID]*sim.ActorSnapshot, record map[sim.ActorID]map[sim.ActorID]*sim.CoinPairRecord) Payload {
	t.Helper()
	members := map[sim.ActorID]struct{}{"subject": {}}
	actors := map[sim.ActorID]*sim.ActorSnapshot{"subject": subject}
	for id, a := range peers {
		members[id] = struct{}{}
		actors[id] = a
	}
	snap := &sim.Snapshot{
		PublishedAt:      time.Date(2026, 7, 30, 19, 0, 0, 0, time.UTC),
		Actors:           actors,
		Huddles:          map[sim.HuddleID]*sim.Huddle{"h1": {ID: "h1", Members: members}},
		CoinRecord:       record,
		CoinRecordWindow: sim.DefaultCoinRecordWindow,
	}
	return Build(snap, "subject", nil)
}

func acquaintedSubject(names ...string) *sim.ActorSnapshot {
	acq := make(map[string]sim.Acquaintance, len(names))
	for _, n := range names {
		acq[n] = sim.Acquaintance{}
	}
	return &sim.ActorSnapshot{
		Kind:              sim.KindNPCStateful,
		DisplayName:       "Constable Gideon Marsh",
		InsideStructureID: "farm",
		CurrentHuddleID:   "h1",
		Acquaintances:     acq,
	}
}

// A stranger contributes no line. There is no shared money history to dispute, and
// the section is re-sent every tick — so a "no coin has passed between you" about
// everyone in a crowded room is pure prompt weight.
func TestBuildCoinDealings_SkipsStrangers(t *testing.T) {
	p := coinDealingsFixture(t,
		acquaintedSubject("Moses James"),
		map[sim.ActorID]*sim.ActorSnapshot{
			"moses":    {DisplayName: "Moses James", InsideStructureID: "farm", CurrentHuddleID: "h1"},
			"stranger": {DisplayName: "Goodman Stark", InsideStructureID: "farm", CurrentHuddleID: "h1"},
		}, nil)
	if len(p.CoinDealings) != 1 || p.CoinDealings[0].PeerID != "moses" {
		t.Fatalf("CoinDealings = %+v, want only the acquainted peer", p.CoinDealings)
	}
}

// Travelers are excluded on BOTH sides, and this is correctness rather than cost: a
// visitor's own payments never reach agent_action_log (the durable sink discards
// visitor rows at the AppendActionLogDurable chokepoint, LLM-379), so after any
// restart a traveler pair would read "no coin has passed between you" when coin
// certainly did. Salem deploys several times a day.
func TestBuildCoinDealings_SkipsTravelers(t *testing.T) {
	t.Run("peer is a traveler", func(t *testing.T) {
		p := coinDealingsFixture(t,
			acquaintedSubject("Daniel Holcomb"),
			map[sim.ActorID]*sim.ActorSnapshot{
				"vstr-1": {
					DisplayName:       "Daniel Holcomb",
					InsideStructureID: "farm",
					CurrentHuddleID:   "h1",
					VisitorState:      &sim.VisitorState{Archetype: "factor", Origin: "Boston"},
				},
			}, nil)
		if len(p.CoinDealings) != 0 {
			t.Errorf("CoinDealings = %+v, want none for a traveler peer", p.CoinDealings)
		}
	})
	t.Run("subject is a traveler", func(t *testing.T) {
		subject := acquaintedSubject("Moses James")
		subject.VisitorState = &sim.VisitorState{Archetype: "factor", Origin: "Boston"}
		p := coinDealingsFixture(t, subject,
			map[sim.ActorID]*sim.ActorSnapshot{
				"moses": {DisplayName: "Moses James", InsideStructureID: "farm", CurrentHuddleID: "h1"},
			}, nil)
		if len(p.CoinDealings) != 0 {
			t.Errorf("CoinDealings = %+v, want none when the subject is a traveler", p.CoinDealings)
		}
	})
}

// The section covers a STATEFUL NPC, which is the whole reason it is not folded into
// the relationship view: buildRelationships is gated to KindNPCShared, and the actor
// who refunded five coins on a debt that never existed was stateful.
func TestBuildCoinDealings_CoversStatefulNPC(t *testing.T) {
	at := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	p := coinDealingsFixture(t,
		acquaintedSubject("Moses James"),
		map[sim.ActorID]*sim.ActorSnapshot{
			"moses": {DisplayName: "Moses James", InsideStructureID: "farm", CurrentHuddleID: "h1"},
		},
		map[sim.ActorID]map[sim.ActorID]*sim.CoinPairRecord{
			"subject": {"moses": {Received: []sim.CoinPayment{{At: at, Amount: 1}}}},
		})
	if len(p.Relationships) != 0 {
		t.Fatalf("fixture is not exercising the stateful case — Relationships = %+v", p.Relationships)
	}
	if len(p.CoinDealings) != 1 || p.CoinDealings[0].Dealings.ReceivedCount != 1 {
		t.Fatalf("CoinDealings = %+v, want the peer's one payment", p.CoinDealings)
	}
}

// The peer cap keeps the re-sent-every-tick section bounded, and prefers peers with
// coin on the record — a zero line is worth rendering, but not at the cost of a real
// one.
func TestBuildCoinDealings_CapPrefersPeersWithCoin(t *testing.T) {
	at := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	names := []string{"Peer A", "Peer B", "Peer C", "Peer D", "Peer E", "Peer F"}
	peers := make(map[sim.ActorID]*sim.ActorSnapshot, len(names))
	ids := []sim.ActorID{"p1", "p2", "p3", "p4", "p5", "p6"}
	for i, id := range ids {
		peers[id] = &sim.ActorSnapshot{DisplayName: names[i], InsideStructureID: "farm", CurrentHuddleID: "h1"}
	}
	// Only the LAST peer by id has coin, so a cap that merely took a PeerID-ordered
	// prefix would drop exactly the line that matters.
	record := map[sim.ActorID]map[sim.ActorID]*sim.CoinPairRecord{
		"subject": {"p6": {Received: []sim.CoinPayment{{At: at, Amount: 3}}}},
	}
	p := coinDealingsFixture(t, acquaintedSubject(names...), peers, record)
	if len(p.CoinDealings) != maxRenderedRelationshipPeers {
		t.Fatalf("CoinDealings length = %d, want %d", len(p.CoinDealings), maxRenderedRelationshipPeers)
	}
	var kept bool
	for i, v := range p.CoinDealings {
		if v.PeerID == "p6" {
			kept = true
		}
		if i > 0 && v.PeerID < p.CoinDealings[i-1].PeerID {
			t.Errorf("kept set is not in PeerID order: %+v", p.CoinDealings)
		}
	}
	if !kept {
		t.Errorf("the only peer with coin on the record was dropped by the cap: %+v", p.CoinDealings)
	}
}

// Phrasing. A run of single coins reads as a person recalling a recurring due; a
// mixed run states the total and how many payments it took.
func TestCoinDealingsSentence(t *testing.T) {
	cases := []struct {
		name string
		d    sim.CoinDealings
		want string
	}{
		{
			name: "the live case — two single coins in, nothing out",
			d:    sim.CoinDealings{ReceivedCount: 2, ReceivedTotal: 2, ReceivedAllSingle: true},
			want: "Moses James has paid you a coin twice, and nothing has gone back the other way.",
		},
		{
			name: "one payment states the amount",
			d:    sim.CoinDealings{ReceivedCount: 1, ReceivedTotal: 5},
			want: "Moses James has paid you 5 coins, and nothing has gone back the other way.",
		},
		{
			name: "single coin, once",
			d:    sim.CoinDealings{PaidCount: 1, PaidTotal: 1, PaidAllSingle: true},
			want: "You have paid Moses James 1 coin, and nothing has come back the other way.",
		},
		{
			name: "mixed amounts state the total and the count",
			d:    sim.CoinDealings{PaidCount: 3, PaidTotal: 11},
			want: "You have paid Moses James 11 coins across 3 payments, and nothing has come back the other way.",
		},
		{
			// Both directions unclassified. The peer is named twice rather than
			// carried by "paid back" (LLM-612): the short form asserted that the
			// second direction discharged the first, which nothing on an Unstated
			// record supports.
			name: "both directions",
			d: sim.CoinDealings{
				ReceivedCount: 2, ReceivedTotal: 2, ReceivedAllSingle: true,
				PaidCount: 1, PaidTotal: 5,
			},
			want: "Moses James has paid you a coin twice, and you have paid Moses James 5 coins.",
		},
		{
			// The LLM-612 trigger, at the live figures.
			name: "a distributor's week of buying stock",
			d: sim.CoinDealings{
				ReceivedCount: 2, ReceivedTotal: 4, ReceivedGoodsCount: 2, ReceivedGoodsTotal: 4,
				PaidCount: 24, PaidTotal: 168, PaidGoodsCount: 24, PaidGoodsTotal: 168,
			},
			want: "Moses James has paid you 4 coins across 2 payments for goods, and you have paid Moses James 168 coins across 24 payments for goods.",
		},
		{
			// One-directional purchase. The "nothing has come back" tail is DROPPED,
			// and that is the sharper half of the ticket — something did come back.
			name: "bought from a supplier who has never paid him",
			d: sim.CoinDealings{PaidCount: 4, PaidTotal: 8, PaidGoodsCount: 4, PaidGoodsTotal: 8},
			want: "You have paid Moses James 8 coins across 4 payments for goods.",
		},
		{
			// A partial names the coin, not the payment count. What the rest was,
			// the engine does not know, and the sentence does not say.
			name: "some of a direction bought goods",
			d:    sim.CoinDealings{PaidCount: 5, PaidTotal: 11, PaidGoodsCount: 2, PaidGoodsTotal: 7},
			want: "You have paid Moses James 11 coins across 5 payments, 7 coins of it for goods.",
		},
		{
			// Unstated coin keeps the blanket tail: with nothing known about what it
			// bought, "nothing came back" is the record's honest position.
			name: "an unclassified payment keeps the tail",
			d:    sim.CoinDealings{PaidCount: 1, PaidTotal: 4},
			want: "You have paid Moses James 4 coins, and nothing has come back the other way.",
		},
		{
			// The incoming arm of the same suppression.
			name: "sold to someone who has bought nothing back",
			d:    sim.CoinDealings{ReceivedCount: 3, ReceivedTotal: 9, ReceivedGoodsCount: 3, ReceivedGoodsTotal: 9},
			want: "Moses James has paid you 9 coins across 3 payments for goods.",
		},
		{
			// Suppression is scoped to the DIRECTION, not the pair (code_review).
			// With both directions live there is no tail to suppress at all, and
			// each direction voices only what its own coin did — the goods clause
			// must not leak onto the unclassified side.
			name: "goods one way, unclassified the other",
			d: sim.CoinDealings{
				ReceivedCount: 1, ReceivedTotal: 4,
				PaidCount: 3, PaidTotal: 9, PaidGoodsCount: 3, PaidGoodsTotal: 9,
			},
			want: "Moses James has paid you 4 coins, and you have paid Moses James 9 coins across 3 payments for goods.",
		},
		{
			name: "an eviction softens the count rather than undercounting silently",
			d:    sim.CoinDealings{ReceivedCount: 64, ReceivedTotal: 64, ReceivedAllSingle: true, ReceivedAtLeast: true},
			want: "Moses James has paid you at least a coin 64 times, and nothing has gone back the other way.",
		},
		{
			name: "nothing on the record",
			d:    sim.CoinDealings{},
			want: "No coin has passed between you and Moses James.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := coinDealingsSentence("Moses James", tc.d); got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

// Peers with nothing on the record collapse into one closing line. Stated four times
// in a crowded room the empty record reads as a form being filled in, and in most
// scenes every peer is on that list — so this is what keeps the section to a single
// line almost always.
func TestRenderCoinDealings_CollapsesTheQuietPeers(t *testing.T) {
	var b strings.Builder
	renderCoinDealings(&b, []CoinDealingsPeerView{
		{PeerID: "moses", PeerName: "Moses James", Dealings: sim.CoinDealings{ReceivedCount: 2, ReceivedTotal: 2, ReceivedAllSingle: true}},
		{PeerID: "hannah", PeerName: "Hannah Boggs"},
		{PeerID: "john", PeerName: "John Ellis"},
	})
	got := b.String()
	if !strings.Contains(got, "- Moses James has paid you a coin twice, and nothing has gone back the other way.\n") {
		t.Errorf("missing the peer-with-coin line:\n%s", got)
	}
	if !strings.Contains(got, "- No coin has passed between you and Hannah Boggs or John Ellis.\n") {
		t.Errorf("quiet peers were not collapsed into one line:\n%s", got)
	}
	if n := strings.Count(got, "\n- "); n != 2 {
		t.Errorf("want exactly 2 bullet lines (one peer with coin, one collapsed), got %d:\n%s", n, got)
	}
}

// Content-gated: no peers, no section. Keeps a solitary actor's prompt clean.
func TestRenderCoinDealings_EmptyWritesNothing(t *testing.T) {
	var b strings.Builder
	renderCoinDealings(&b, nil)
	if b.Len() != 0 {
		t.Errorf("wrote %q for an empty view", b.String())
	}
}
