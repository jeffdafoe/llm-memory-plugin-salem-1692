package perception

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// ZBBS-WORK-407 build layer: an UNHUDDLED actor's co-present audience surfaces
// in Surroundings.CoPresent (not HuddleMembers), from the world-precomputed
// ActorSnapshot.ColocatedAudienceIDs, carrying the same acquaintance gating the
// huddle roster uses.
func TestBuild_SurroundingsCoPresentWhenUnhuddled(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Kind:                 sim.KindNPCShared,
		InsideStructureID:    "inn",
		ColocatedAudienceIDs: []sim.ActorID{"hannah", "stranger"},
		Acquaintances:        map[string]sim.Acquaintance{"Hannah Boggs": {}},
	}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"prudence": subj,
			"hannah":   {DisplayName: "Hannah Boggs", Role: "innkeeper"},
			"stranger": {DisplayName: "Goodman Stark", Role: "farmer"},
		},
	}
	p := Build(snap, "prudence", nil)
	if len(p.Surroundings.HuddleMembers) != 0 {
		t.Fatalf("unhuddled: HuddleMembers should be empty, got %v", p.Surroundings.HuddleMembers)
	}
	if len(p.Surroundings.CoPresent) != 2 {
		t.Fatalf("CoPresent = %d, want 2", len(p.Surroundings.CoPresent))
	}
	byID := make(map[sim.ActorID]HuddleMember, 2)
	for _, m := range p.Surroundings.CoPresent {
		byID[m.ID] = m
	}
	if !byID["hannah"].Acquainted {
		t.Errorf("Hannah is in Acquaintances — want Acquainted=true")
	}
	if byID["stranger"].Acquainted {
		t.Errorf("stranger is not in Acquaintances — want Acquainted=false")
	}
}

// A huddled actor uses HuddleMembers and ignores ColocatedAudienceIDs, so the
// co-presence line and the huddle line never double-render (ZBBS-WORK-407).
func TestBuild_NoCoPresentWhenHuddled(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Kind:                 sim.KindNPCShared,
		InsideStructureID:    "inn",
		CurrentHuddleID:      "h1",
		ColocatedAudienceIDs: []sim.ActorID{"hannah"}, // present but must be ignored
	}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"prudence": subj,
			"john":     {DisplayName: "John Ellis"},
			"hannah":   {DisplayName: "Hannah Boggs"},
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			"h1": {ID: "h1", Members: map[sim.ActorID]struct{}{"prudence": {}, "john": {}}},
		},
	}
	p := Build(snap, "prudence", nil)
	if len(p.Surroundings.CoPresent) != 0 {
		t.Errorf("huddled: CoPresent should be empty, got %v", p.Surroundings.CoPresent)
	}
	if len(p.Surroundings.HuddleMembers) != 1 || p.Surroundings.HuddleMembers[0].ID != "john" {
		t.Errorf("HuddleMembers = %v, want [john]", p.Surroundings.HuddleMembers)
	}
}

// ZBBS-WORK-407 render layer: the "## Around you" company line has three shapes —
// huddled (existing), co-present-but-not-huddled, and alone. renderSurroundings
// picks by which slice is populated; co-presence renders every turn.
func TestRenderSurroundings_CoPresentNamesThemPlural(t *testing.T) {
	var b strings.Builder
	renderSurroundings(&b, SurroundingsView{
		InsideStructureID: "inn",
		StructureName:     "the Inn",
		CoPresent: []HuddleMember{
			{ID: "hannah", DisplayName: "Hannah Boggs", Acquainted: true},
			{ID: "john", DisplayName: "John Ellis", Acquainted: true},
		},
	})
	out := b.String()
	if !strings.Contains(out, "Hannah Boggs and John Ellis are here with you.") {
		t.Errorf("co-present plural line missing in:\n%s", out)
	}
	// LLM-220: presence is stated neutrally — the old "speak to start conversing
	// with them" directive pushed unprompted monologues at whoever was present.
	if strings.Contains(out, "speak to start conversing") {
		t.Errorf("co-present line must not coach speaking, got:\n%s", out)
	}
}

func TestRenderSurroundings_CoPresentSingularVerb(t *testing.T) {
	var b strings.Builder
	renderSurroundings(&b, SurroundingsView{
		InsideStructureID: "inn",
		StructureName:     "the Inn",
		CoPresent:         []HuddleMember{{ID: "hannah", DisplayName: "Hannah Boggs", Acquainted: true}},
	})
	if out := b.String(); !strings.Contains(out, "Hannah Boggs is here with you") {
		t.Errorf("singular co-present line wrong in:\n%s", out)
	}
}

func TestRenderSurroundings_UnacquaintedCoPresentUsesDescriptor(t *testing.T) {
	var b strings.Builder
	renderSurroundings(&b, SurroundingsView{
		InsideStructureID: "inn",
		StructureName:     "the Inn",
		CoPresent:         []HuddleMember{{ID: "x", DisplayName: "Goodman Stark", Role: "farmer", Acquainted: false}},
	})
	if out := b.String(); !strings.Contains(out, "the farmer is here with you") {
		t.Errorf("unacquainted co-present should render descriptor 'the farmer', got:\n%s", out)
	}
}

func TestRenderSurroundings_AloneStatesItPlainly(t *testing.T) {
	var b strings.Builder
	renderSurroundings(&b, SurroundingsView{InsideStructureID: "inn", StructureName: "the Inn"})
	if out := b.String(); !strings.Contains(out, "no one else here to hear you speak") {
		t.Errorf("alone line missing in:\n%s", out)
	}
}

// ZBBS-WORK-422 build layer: a co-present member whose most recent
// ActionTypeWalked is within coPresentJustArrivedWindow is flagged JustArrived;
// one that arrived long ago (settled in) is not. The arrival is read from the
// snapshot action log, so no per-actor arrival state is needed.
func TestBuild_CoPresentJustArrivedFromActionLog(t *testing.T) {
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	subj := &sim.ActorSnapshot{
		Kind:                 sim.KindNPCShared,
		InsideStructureID:    "inn",
		ColocatedAudienceIDs: []sim.ActorID{"hannah", "newcomer"},
	}
	snap := &sim.Snapshot{
		PublishedAt: now,
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"prudence": subj,
			"hannah":   {DisplayName: "Hannah Boggs"},
			"newcomer": {DisplayName: "Goodman Stark"},
		},
		ActionLog: []sim.ActionLogEntry{
			// Hannah arrived 10 min ago — settled in, not "just arrived".
			{ActorID: "hannah", ActionType: sim.ActionTypeWalked, OccurredAt: now.Add(-10 * time.Minute)},
			// Newcomer arrived 15s ago — inside the window.
			{ActorID: "newcomer", ActionType: sim.ActionTypeWalked, OccurredAt: now.Add(-15 * time.Second)},
		},
	}
	p := Build(snap, "prudence", nil)
	byID := make(map[sim.ActorID]HuddleMember, 2)
	for _, m := range p.Surroundings.CoPresent {
		byID[m.ID] = m
	}
	if !byID["newcomer"].JustArrived {
		t.Errorf("newcomer arrived 15s ago — want JustArrived=true")
	}
	if byID["hannah"].JustArrived {
		t.Errorf("Hannah arrived 10 min ago — want JustArrived=false")
	}
}

// ZBBS-WORK-422 render layer: a JustArrived co-present member is tagged
// "(just arrived)"; a settled member is not.
func TestRenderSurroundings_JustArrivedTagged(t *testing.T) {
	var b strings.Builder
	renderSurroundings(&b, SurroundingsView{
		InsideStructureID: "inn",
		StructureName:     "the Inn",
		CoPresent: []HuddleMember{
			{ID: "hannah", DisplayName: "Hannah Boggs", Acquainted: true},
			{ID: "ezekiel", DisplayName: "Ezekiel Cheever", Acquainted: true, JustArrived: true},
		},
	})
	out := b.String()
	if !strings.Contains(out, "Ezekiel Cheever (just arrived)") {
		t.Errorf("just-arrived member should be tagged, got:\n%s", out)
	}
	if strings.Contains(out, "Hannah Boggs (just arrived)") {
		t.Errorf("settled member should NOT be tagged, got:\n%s", out)
	}
}

// ZBBS-WORK-426 build layer: an UNHUDDLED actor's co-present SLEEPERS surface in
// Surroundings.CoPresentAsleep (from ColocatedSleeperIDs), separate from the
// awake CoPresent set, with the same acquaintance gating.
func TestBuild_CoPresentAsleepWhenUnhuddled(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Kind:                 sim.KindNPCShared,
		InsideStructureID:    "inn",
		ColocatedAudienceIDs: []sim.ActorID{"hannah"},
		ColocatedSleeperIDs:  []sim.ActorID{"sleeper"},
		Acquaintances:        map[string]sim.Acquaintance{"Hannah Boggs": {}},
	}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"prudence": subj,
			"hannah":   {DisplayName: "Hannah Boggs", Role: "innkeeper"},
			"sleeper":  {DisplayName: "Goodman Stark", Role: "farmer"},
		},
	}
	p := Build(snap, "prudence", nil)
	if len(p.Surroundings.CoPresent) != 1 || p.Surroundings.CoPresent[0].ID != "hannah" {
		t.Fatalf("CoPresent = %v, want [hannah] (the awake peer only)", p.Surroundings.CoPresent)
	}
	if len(p.Surroundings.CoPresentAsleep) != 1 || p.Surroundings.CoPresentAsleep[0].ID != "sleeper" {
		t.Fatalf("CoPresentAsleep = %v, want [sleeper]", p.Surroundings.CoPresentAsleep)
	}
}

// A huddled actor ignores ColocatedSleeperIDs (same as ColocatedAudienceIDs), so
// the asleep clause never double-renders against the huddle line (ZBBS-WORK-426).
func TestBuild_NoCoPresentAsleepWhenHuddled(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Kind:                sim.KindNPCShared,
		InsideStructureID:   "inn",
		CurrentHuddleID:     "h1",
		ColocatedSleeperIDs: []sim.ActorID{"sleeper"}, // present but must be ignored when huddled
	}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"prudence": subj,
			"john":     {DisplayName: "John Ellis"},
			"sleeper":  {DisplayName: "Goodman Stark"},
		},
		Huddles: map[sim.HuddleID]*sim.Huddle{
			"h1": {ID: "h1", Members: map[sim.ActorID]struct{}{"prudence": {}, "john": {}}},
		},
	}
	p := Build(snap, "prudence", nil)
	if len(p.Surroundings.CoPresentAsleep) != 0 {
		t.Errorf("huddled: CoPresentAsleep should be empty, got %v", p.Surroundings.CoPresentAsleep)
	}
}

// ZBBS-WORK-426 render layer: co-present sleepers render in a distinct "(asleep)"
// clause — appended to the awake line when someone is awake, and as its own line
// (with the no-one-awake note) when only sleepers are present.
func TestRenderSurroundings_AsleepClauseAlongsideAwake(t *testing.T) {
	var b strings.Builder
	renderSurroundings(&b, SurroundingsView{
		InsideStructureID: "inn",
		StructureName:     "the Inn",
		CoPresent:         []HuddleMember{{ID: "hannah", DisplayName: "Hannah Boggs", Acquainted: true}},
		CoPresentAsleep:   []HuddleMember{{ID: "prudence", DisplayName: "Prudence Ward", Acquainted: true}},
	})
	out := b.String()
	if !strings.Contains(out, "Hannah Boggs is here with you.") {
		t.Errorf("awake co-present line missing in:\n%s", out)
	}
	if !strings.Contains(out, "Prudence Ward is asleep and won't respond if you speak to them") {
		t.Errorf("asleep clause missing in:\n%s", out)
	}
}

func TestRenderSurroundings_OnlyAsleepNoAwakeAudience(t *testing.T) {
	var b strings.Builder
	renderSurroundings(&b, SurroundingsView{
		InsideStructureID: "inn",
		StructureName:     "the Inn",
		CoPresentAsleep:   []HuddleMember{{ID: "prudence", DisplayName: "Prudence Ward", Acquainted: true}},
	})
	out := b.String()
	if !strings.Contains(out, "Prudence Ward is asleep and won't respond if you speak to them") {
		t.Errorf("asleep clause missing in:\n%s", out)
	}
	if !strings.Contains(out, "no one awake here to hear you speak") {
		t.Errorf("only-asleep should note there's no awake audience, got:\n%s", out)
	}
	// Must NOT fall back to the empty-room wording — someone IS here, just asleep.
	if strings.Contains(out, "no one else here to hear you speak") {
		t.Errorf("only-asleep should not use the empty-room line, got:\n%s", out)
	}
}

func TestRenderSurroundings_AsleepPluralAndDescriptor(t *testing.T) {
	var b strings.Builder
	renderSurroundings(&b, SurroundingsView{
		InsideStructureID: "inn",
		StructureName:     "the Inn",
		CoPresentAsleep: []HuddleMember{
			{ID: "a", DisplayName: "Prudence Ward", Acquainted: true},
			{ID: "b", DisplayName: "Goodman Stark", Role: "farmer", Acquainted: false},
		},
	})
	if out := b.String(); !strings.Contains(out, "Prudence Ward and the farmer are asleep; neither will respond if you speak to them") {
		t.Errorf("asleep plural + descriptor wrong in:\n%s", out)
	}
}

// ZBBS-WORK-426: a co-present RESTING actor is partitioned out of the awake
// audience and rendered in the not-addressable clause — an NPC can't rouse a
// rester by speaking (only a PC can), so it'd sit silent if addressed.
func TestBuild_CoPresentRestingPartitionedFromAudience(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Kind:                 sim.KindNPCShared,
		InsideStructureID:    "inn",
		ColocatedAudienceIDs: []sim.ActorID{"hannah", "rester"},
	}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"prudence": subj,
			"hannah":   {DisplayName: "Hannah Boggs"},
			"rester":   {DisplayName: "Goodman Stark", State: sim.StateResting},
		},
	}
	p := Build(snap, "prudence", nil)
	if len(p.Surroundings.CoPresent) != 1 || p.Surroundings.CoPresent[0].ID != "hannah" {
		t.Fatalf("CoPresent = %v, want [hannah] (rester partitioned out)", p.Surroundings.CoPresent)
	}
	if len(p.Surroundings.CoPresentResting) != 1 || p.Surroundings.CoPresentResting[0].ID != "rester" {
		t.Fatalf("CoPresentResting = %v, want [rester]", p.Surroundings.CoPresentResting)
	}
}

func TestRenderSurroundings_RestingNotAddressableAlongsideAwake(t *testing.T) {
	var b strings.Builder
	renderSurroundings(&b, SurroundingsView{
		InsideStructureID: "inn",
		StructureName:     "the Inn",
		CoPresent:         []HuddleMember{{ID: "hannah", DisplayName: "Hannah Boggs", Acquainted: true}},
		CoPresentResting:  []HuddleMember{{ID: "stark", DisplayName: "Goodman Stark", Acquainted: true}},
	})
	out := b.String()
	if !strings.Contains(out, "Hannah Boggs is here with you.") {
		t.Errorf("awake line missing in:\n%s", out)
	}
	if !strings.Contains(out, "Goodman Stark is resting and won't respond if you speak to them") {
		t.Errorf("resting clause missing in:\n%s", out)
	}
}

// Jeff's combined example: one asleep + one resting render in a single clause.
func TestRenderSurroundings_AsleepAndRestingCombined(t *testing.T) {
	var b strings.Builder
	renderSurroundings(&b, SurroundingsView{
		InsideStructureID: "inn",
		StructureName:     "the Inn",
		CoPresentAsleep:   []HuddleMember{{ID: "prudence", DisplayName: "Prudence Ward", Acquainted: true}},
		CoPresentResting:  []HuddleMember{{ID: "stark", DisplayName: "Goodman Stark", Acquainted: true}},
	})
	out := b.String()
	if !strings.Contains(out, "Prudence Ward is asleep and Goodman Stark is resting; neither will respond if you speak to them") {
		t.Errorf("combined asleep+resting clause wrong in:\n%s", out)
	}
	if !strings.Contains(out, "no one awake here to hear you speak") {
		t.Errorf("only-dormant should note there's no awake audience, got:\n%s", out)
	}
}

// mendingPeerObserverSnapshot builds an observer (Prudence) co-present with a keeper
// (Ezekiel) mid-repair of his market stall, plus a non-busy peer (Grace). Shared by
// the LLM-440 build + end-to-end observer tests. Deterministic — no wall-clock reads.
func mendingPeerObserverSnapshot() (*sim.Snapshot, sim.ActorID) {
	subj := &sim.ActorSnapshot{
		Kind:                 sim.KindNPCShared,
		InsideStructureID:    "market",
		ColocatedAudienceIDs: []sim.ActorID{"ezekiel", "grace"},
		Acquaintances: map[string]sim.Acquaintance{
			"Ezekiel Stone": {},
			"Grace Ward":    {},
		},
	}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"prudence": subj,
			// Ezekiel is mid a timed repair of his market stall (a structure-backed
			// object shares its id, so the object id resolves to the Market structure).
			"ezekiel": {
				DisplayName:            "Ezekiel Stone",
				Role:                   "merchant",
				SourceActivityKind:     sim.SourceActivityRepair,
				SourceActivityObjectID: "market",
			},
			// Grace is just standing here — no activity, so no annotation.
			"grace": {DisplayName: "Grace Ward", Role: "farmer"},
		},
		Structures: map[sim.StructureID]*sim.Structure{
			"market": {DisplayName: "the Market"},
		},
	}
	return snap, "prudence"
}

// LLM-440 build layer: a co-present peer mid one of the three "work" source
// activities (repair / stoke / harvest) is annotated busy on its CoPresent member
// view, sourced from the peer's BusyAtSource-gated SourceActivityKind projection
// (world.go only sets that field while the window is in flight — the same signal the
// subject's own self-line reads). Refresh (eat/drink at a source) is deliberately NOT
// annotated — left to the Eating cue — and a peer with no activity leaves the fields
// zero, so SourceActivityBusy and a rendered phrase stay in lockstep.
func TestBuild_CoPresentSourceActivityAnnotated(t *testing.T) {
	subj := &sim.ActorSnapshot{
		Kind:                 sim.KindNPCShared,
		InsideStructureID:    "market",
		ColocatedAudienceIDs: []sim.ActorID{"ezekiel", "hannah", "josiah", "silence", "grace"},
		Acquaintances: map[string]sim.Acquaintance{
			"Ezekiel Stone": {}, "Hannah Boggs": {}, "Josiah Thorne": {},
			"Silence Walker": {}, "Grace Ward": {},
		},
	}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"prudence": subj,
			// A structure-backed object shares its id, so "market" resolves to the Market.
			"ezekiel": {DisplayName: "Ezekiel Stone", SourceActivityKind: sim.SourceActivityRepair, SourceActivityObjectID: "market"},
			"hannah":  {DisplayName: "Hannah Boggs", SourceActivityKind: sim.SourceActivityStoke},
			"josiah":  {DisplayName: "Josiah Thorne", SourceActivityKind: sim.SourceActivityHarvest},
			// Refresh is a source activity too, but stays unannotated (left to Eating).
			"silence": {DisplayName: "Silence Walker", SourceActivityKind: sim.SourceActivityRefresh},
			// No activity at all.
			"grace": {DisplayName: "Grace Ward"},
		},
		Structures: map[sim.StructureID]*sim.Structure{
			"market": {DisplayName: "the Market"},
		},
	}
	p := Build(snap, "prudence", nil)
	byID := make(map[sim.ActorID]HuddleMember, len(p.Surroundings.CoPresent))
	for _, m := range p.Surroundings.CoPresent {
		byID[m.ID] = m
	}
	for _, tc := range []struct {
		id       sim.ActorID
		wantBusy bool
		wantKind sim.SourceActivityKind
	}{
		{"ezekiel", true, sim.SourceActivityRepair},
		{"hannah", true, sim.SourceActivityStoke},
		{"josiah", true, sim.SourceActivityHarvest},
		{"silence", false, ""}, // refresh: not annotated
		{"grace", false, ""},   // idle: not annotated
	} {
		m, ok := byID[tc.id]
		if !ok {
			t.Fatalf("%s should be co-present, got members: %v", tc.id, p.Surroundings.CoPresent)
		}
		if m.SourceActivityBusy != tc.wantBusy || m.SourceActivityKind != tc.wantKind {
			t.Errorf("%s: SourceActivityBusy=%v kind=%q, want busy=%v kind=%q", tc.id, m.SourceActivityBusy, m.SourceActivityKind, tc.wantBusy, tc.wantKind)
		}
	}
	// Repair carries the mended business's display name; the non-repair busy kinds
	// need no place, so their label stays empty.
	if got := byID["ezekiel"].SourceActivityLabel; got != "the Market" {
		t.Errorf("Ezekiel SourceActivityLabel = %q, want %q", got, "the Market")
	}
	if got := byID["hannah"].SourceActivityLabel; got != "" {
		t.Errorf("stoke needs no place — Hannah SourceActivityLabel = %q, want empty", got)
	}
}

// LLM-440 render layer: busyActivityPhrase annotates a co-present peer mid a source
// activity in "## Around you", keyed on kind — repair names the business it is bound
// to (falling back to a place-less phrase), stoke and gather stand alone.
func TestRenderSurroundings_SourceActivityBusyAnnotated(t *testing.T) {
	cases := []struct {
		name string
		m    HuddleMember
		want string
	}{
		{"repair names the business", HuddleMember{ID: "ez", DisplayName: "Ezekiel Stone", Acquainted: true, SourceActivityBusy: true, SourceActivityKind: sim.SourceActivityRepair, SourceActivityLabel: "the Market"}, "(mending at the Market just now)"},
		{"repair with no resolved place", HuddleMember{ID: "ez", DisplayName: "Ezekiel Stone", Acquainted: true, SourceActivityBusy: true, SourceActivityKind: sim.SourceActivityRepair}, "(mending just now)"},
		{"stoke", HuddleMember{ID: "hannah", DisplayName: "Hannah Boggs", Acquainted: true, SourceActivityBusy: true, SourceActivityKind: sim.SourceActivityStoke}, "(tending the fire just now)"},
		{"harvest", HuddleMember{ID: "josiah", DisplayName: "Josiah Thorne", Acquainted: true, SourceActivityBusy: true, SourceActivityKind: sim.SourceActivityHarvest}, "(gathering just now)"},
		// LLM-454: a co-baking / entering housemate reads a baker as "join her at the
		// bread," not "interrupt her." Place-less like stoke/harvest.
		{"bake", HuddleMember{ID: "silence", DisplayName: "Silence Walker", Acquainted: true, SourceActivityBusy: true, SourceActivityKind: sim.SourceActivityBake}, "(at the hearth, baking just now)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b strings.Builder
			renderSurroundings(&b, SurroundingsView{
				InsideStructureID: "market",
				StructureName:     "the Market",
				CoPresent:         []HuddleMember{tc.m},
			})
			if out := b.String(); !strings.Contains(out, tc.want) {
				t.Errorf("want %q in ## Around you, got:\n%s", tc.want, out)
			}
		})
	}
}

// TestGoldenObserverSeesMendingPeer is the LLM-440 observer guard (the source-activity
// analogue of TestGoldenObserverSeesEatingPeer): an onlooker co-present with a keeper
// mid-repair must see, in "## Around you", that the keeper is mending — so they read a
// busy keeper as occupied rather than free to greet or pitch. Scoped to the "## Around
// you" section so a match elsewhere in the prompt can't make it pass vacuously.
func TestGoldenObserverSeesMendingPeer(t *testing.T) {
	snap, observer := mendingPeerObserverSnapshot()
	got := combinedPrompt(Render(Build(snap, observer, nil), DefaultRenderConfig()))
	section := aroundYouSection(got)
	if section == "" {
		t.Fatalf("no '## Around you' section in prompt:\n%s", got)
	}
	if !strings.Contains(section, "Ezekiel Stone") || !strings.Contains(section, "(mending at the Market just now)") {
		t.Errorf("observer should see the repairing keeper annotated as mending in ## Around you, got section:\n%s", section)
	}
}

// bakingPeerObserverSnapshot builds an observer (Patience) co-present at home with a
// housemate (Silence) mid the evening bake. The LLM-454 sibling of
// mendingPeerObserverSnapshot. Deterministic — no wall-clock reads.
func bakingPeerObserverSnapshot() (*sim.Snapshot, sim.ActorID) {
	subj := &sim.ActorSnapshot{
		Kind:                 sim.KindNPCStateful,
		InsideStructureID:    "walkerhome",
		ColocatedAudienceIDs: []sim.ActorID{"silence"},
		Acquaintances:        map[string]sim.Acquaintance{"Silence Walker": {}},
	}
	snap := &sim.Snapshot{
		Actors: map[sim.ActorID]*sim.ActorSnapshot{
			"patience": subj,
			// Silence is mid the household bake at home; the home structure id is the
			// source-activity object (shared under the WORK-342 id bridge).
			"silence": {
				DisplayName:            "Silence Walker",
				SourceActivityKind:     sim.SourceActivityBake,
				SourceActivityObjectID: "walkerhome",
			},
		},
		Structures: map[sim.StructureID]*sim.Structure{
			"walkerhome": {DisplayName: "the Walker Residence"},
		},
	}
	return snap, "patience"
}

// LLM-454 build layer: a co-present housemate mid the evening bake is annotated busy
// on its CoPresent member view — the same BusyAtSource-gated SourceActivityKind
// projection the repair/stoke/harvest kinds use, now extended to bake.
func TestBuild_CoPresentBakingPeerAnnotated(t *testing.T) {
	snap, observer := bakingPeerObserverSnapshot()
	p := Build(snap, observer, nil)
	if len(p.Surroundings.CoPresent) != 1 || p.Surroundings.CoPresent[0].ID != "silence" {
		t.Fatalf("CoPresent = %v, want [silence]", p.Surroundings.CoPresent)
	}
	m := p.Surroundings.CoPresent[0]
	if !m.SourceActivityBusy || m.SourceActivityKind != sim.SourceActivityBake {
		t.Errorf("baking peer: SourceActivityBusy=%v kind=%q, want true / bake", m.SourceActivityBusy, m.SourceActivityKind)
	}
}

// TestGoldenObserverSeesBakingPeer is the LLM-454 observer guard (the bake analogue of
// TestGoldenObserverSeesMendingPeer): a housemate entering the kitchen must read, in
// "## Around you", that the baker is at the hearth — so a newcomer reads it as "join
// her at the bread," not "interrupt her." Scoped to the "## Around you" section.
func TestGoldenObserverSeesBakingPeer(t *testing.T) {
	snap, observer := bakingPeerObserverSnapshot()
	got := combinedPrompt(Render(Build(snap, observer, nil), DefaultRenderConfig()))
	section := aroundYouSection(got)
	if section == "" {
		t.Fatalf("no '## Around you' section in prompt:\n%s", got)
	}
	if !strings.Contains(section, "Silence Walker") || !strings.Contains(section, "(at the hearth, baking just now)") {
		t.Errorf("observer should see the baking housemate annotated in ## Around you, got section:\n%s", section)
	}
}
