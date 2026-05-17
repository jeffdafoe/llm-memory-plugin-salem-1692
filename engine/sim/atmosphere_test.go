package sim

import (
	"strings"
	"testing"
	"time"
)

func newAtmosphereTestWorld(t *testing.T) *World {
	t.Helper()
	w := &World{
		Actors:            make(map[ActorID]*Actor),
		Structures:        make(map[StructureID]*Structure),
		Huddles:           make(map[HuddleID]*Huddle),
		Scenes:            make(map[SceneID]*Scene),
		Orders:            make(map[OrderID]*Order),
		VillageObjects:    make(map[VillageObjectID]*VillageObject),
		Quotes:            make(map[QuoteID]*SceneQuote),
		PayLedger:         make(map[LedgerID]*PayLedgerEntry),
		Assets:            make(map[AssetID]*Asset),
		Recipes:           make(map[ItemKind]*ItemRecipe),
		ItemKinds:         make(map[ItemKind]*ItemKindDef),
		actorsByStructure: make(map[StructureID]map[ActorID]struct{}),
		actorsByHuddle:    make(map[HuddleID]map[ActorID]struct{}),
		outdoorActors:     make(map[ActorID]struct{}),
	}
	w.Phase = PhaseDay
	w.Environment = WorldEnvironment{Weather: "clear"}
	return w
}

func runAtmosphereCmd(t *testing.T, w *World, cmd Command) any {
	t.Helper()
	v, err := cmd.Fn(w)
	if err != nil {
		t.Fatalf("Command Fn: %v", err)
	}
	return v
}

func TestFetchAtmosphereContext_BasicShape(t *testing.T) {
	w := newAtmosphereTestWorld(t)
	w.Phase = PhaseNight
	w.Environment.Weather = "drizzle"
	w.Environment.Atmosphere = "the village sleeps"
	w.Structures["tavern"] = &Structure{ID: "tavern", DisplayName: "Tavern"}
	w.Actors["john"] = &Actor{ID: "john", DisplayName: "John Ellis", Kind: KindNPCStateful, InsideStructureID: "tavern"}
	w.Actors["prudence"] = &Actor{ID: "prudence", DisplayName: "Prudence Ward", Kind: KindNPCShared, InsideStructureID: "tavern"}
	w.Actors["ezekiel"] = &Actor{ID: "ezekiel", DisplayName: "Ezekiel Crane", Kind: KindNPCStateful}

	at := time.Date(2026, 5, 17, 20, 0, 0, 0, time.UTC)
	v := runAtmosphereCmd(t, w, FetchAtmosphereContext(at))
	ctx, ok := v.(AtmosphereContext)
	if !ok {
		t.Fatalf("FetchAtmosphereContext returned %T, want AtmosphereContext", v)
	}
	if !ctx.Now.Equal(at) {
		t.Errorf("Now = %v, want %v", ctx.Now, at)
	}
	if ctx.Phase != PhaseNight {
		t.Errorf("Phase = %v, want %v", ctx.Phase, PhaseNight)
	}
	if ctx.Weather != "drizzle" {
		t.Errorf("Weather = %q, want drizzle", ctx.Weather)
	}
	if ctx.PriorAtmosphere != "the village sleeps" {
		t.Errorf("PriorAtmosphere = %q, want %q", ctx.PriorAtmosphere, "the village sleeps")
	}
	if len(ctx.Roster) != 2 {
		t.Fatalf("Roster len = %d, want 2 (Tavern + outdoor)", len(ctx.Roster))
	}
	if ctx.Roster[0].StructureLabel != "Tavern" {
		t.Errorf("Roster[0].StructureLabel = %q, want Tavern", ctx.Roster[0].StructureLabel)
	}
	if got, want := ctx.Roster[0].DisplayNames, []string{"John Ellis", "Prudence Ward"}; !equalStrings(got, want) {
		t.Errorf("Roster[0].DisplayNames = %v, want %v", got, want)
	}
	if ctx.Roster[1].StructureLabel != "" {
		t.Errorf("Roster[1].StructureLabel = %q, want empty (outdoor)", ctx.Roster[1].StructureLabel)
	}
	if got, want := ctx.Roster[1].DisplayNames, []string{"Ezekiel Crane"}; !equalStrings(got, want) {
		t.Errorf("Roster[1].DisplayNames = %v, want %v", got, want)
	}
}

func TestFetchAtmosphereContext_NoActorsEmptyRoster(t *testing.T) {
	w := newAtmosphereTestWorld(t)
	v := runAtmosphereCmd(t, w, FetchAtmosphereContext(time.Now()))
	ctx := v.(AtmosphereContext)
	if len(ctx.Roster) != 0 {
		t.Errorf("Roster len = %d, want 0 on empty world", len(ctx.Roster))
	}
}

func TestFetchAtmosphereContext_PCsExcluded(t *testing.T) {
	w := newAtmosphereTestWorld(t)
	w.Structures["tavern"] = &Structure{ID: "tavern", DisplayName: "Tavern"}
	w.Actors["jeff"] = &Actor{ID: "jeff", DisplayName: "Jeff", Kind: KindPC, InsideStructureID: "tavern"}
	w.Actors["john"] = &Actor{ID: "john", DisplayName: "John Ellis", Kind: KindNPCStateful, InsideStructureID: "tavern"}

	v := runAtmosphereCmd(t, w, FetchAtmosphereContext(time.Now()))
	ctx := v.(AtmosphereContext)
	if len(ctx.Roster) != 1 {
		t.Fatalf("Roster len = %d, want 1", len(ctx.Roster))
	}
	if got, want := ctx.Roster[0].DisplayNames, []string{"John Ellis"}; !equalStrings(got, want) {
		t.Errorf("Roster[0].DisplayNames = %v, want %v (PC excluded)", got, want)
	}
}

func TestFetchAtmosphereContext_GroupOrderingDeterministic(t *testing.T) {
	w := newAtmosphereTestWorld(t)
	w.Structures["tavern"] = &Structure{ID: "tavern", DisplayName: "Tavern"}
	w.Structures["smithy"] = &Structure{ID: "smithy", DisplayName: "Smithy"}
	w.Structures["bakery"] = &Structure{ID: "bakery", DisplayName: "Bakery"}
	w.Actors["a1"] = &Actor{ID: "a1", DisplayName: "Alice", Kind: KindNPCStateful, InsideStructureID: "tavern"}
	w.Actors["a2"] = &Actor{ID: "a2", DisplayName: "Bob", Kind: KindNPCStateful, InsideStructureID: "smithy"}
	w.Actors["a3"] = &Actor{ID: "a3", DisplayName: "Carol", Kind: KindNPCStateful, InsideStructureID: "bakery"}
	w.Actors["a4"] = &Actor{ID: "a4", DisplayName: "Dave", Kind: KindNPCStateful}

	v := runAtmosphereCmd(t, w, FetchAtmosphereContext(time.Now()))
	ctx := v.(AtmosphereContext)
	if len(ctx.Roster) != 4 {
		t.Fatalf("Roster len = %d, want 4", len(ctx.Roster))
	}
	// Buckets alphabetical with outdoor last.
	wantLabels := []string{"Bakery", "Smithy", "Tavern", ""}
	for i, want := range wantLabels {
		if ctx.Roster[i].StructureLabel != want {
			t.Errorf("Roster[%d].StructureLabel = %q, want %q", i, ctx.Roster[i].StructureLabel, want)
		}
	}
}

func TestFetchAtmosphereContext_NamesWithinBucketSorted(t *testing.T) {
	w := newAtmosphereTestWorld(t)
	w.Structures["tavern"] = &Structure{ID: "tavern", DisplayName: "Tavern"}
	w.Actors["c"] = &Actor{ID: "c", DisplayName: "Charlie", Kind: KindNPCStateful, InsideStructureID: "tavern"}
	w.Actors["a"] = &Actor{ID: "a", DisplayName: "Alice", Kind: KindNPCStateful, InsideStructureID: "tavern"}
	w.Actors["b"] = &Actor{ID: "b", DisplayName: "Bob", Kind: KindNPCStateful, InsideStructureID: "tavern"}

	v := runAtmosphereCmd(t, w, FetchAtmosphereContext(time.Now()))
	ctx := v.(AtmosphereContext)
	if len(ctx.Roster) != 1 {
		t.Fatalf("Roster len = %d, want 1", len(ctx.Roster))
	}
	if got, want := ctx.Roster[0].DisplayNames, []string{"Alice", "Bob", "Charlie"}; !equalStrings(got, want) {
		t.Errorf("DisplayNames = %v, want %v (sorted)", got, want)
	}
}

func TestFetchAtmosphereContext_UnknownStructureBucketsOutdoor(t *testing.T) {
	w := newAtmosphereTestWorld(t)
	// No "tavern" structure registered, but actor claims to be inside it.
	w.Actors["john"] = &Actor{ID: "john", DisplayName: "John Ellis", Kind: KindNPCStateful, InsideStructureID: "tavern"}

	v := runAtmosphereCmd(t, w, FetchAtmosphereContext(time.Now()))
	ctx := v.(AtmosphereContext)
	if len(ctx.Roster) != 1 {
		t.Fatalf("Roster len = %d, want 1", len(ctx.Roster))
	}
	if ctx.Roster[0].StructureLabel != "" {
		t.Errorf("StructureLabel = %q, want empty (outdoor fallback for unknown struct)", ctx.Roster[0].StructureLabel)
	}
}

func TestFetchAtmosphereContext_EmptyStructureLabelBucketsOutdoor(t *testing.T) {
	w := newAtmosphereTestWorld(t)
	// Structure exists but DisplayName is empty.
	w.Structures["tavern"] = &Structure{ID: "tavern", DisplayName: ""}
	w.Actors["john"] = &Actor{ID: "john", DisplayName: "John Ellis", Kind: KindNPCStateful, InsideStructureID: "tavern"}

	v := runAtmosphereCmd(t, w, FetchAtmosphereContext(time.Now()))
	ctx := v.(AtmosphereContext)
	if len(ctx.Roster) != 1 {
		t.Fatalf("Roster len = %d, want 1", len(ctx.Roster))
	}
	if ctx.Roster[0].StructureLabel != "" {
		t.Errorf("StructureLabel = %q, want empty (outdoor fallback)", ctx.Roster[0].StructureLabel)
	}
}

func TestApplyAtmosphereRefresh_BasicInstallAndStamp(t *testing.T) {
	w := newAtmosphereTestWorld(t)
	at := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)

	v, err := ApplyAtmosphereRefresh("the morning mist hangs heavy", at).Fn(w)
	if err != nil {
		t.Fatalf("ApplyAtmosphereRefresh: %v", err)
	}
	wrote, ok := v.(bool)
	if !ok || !wrote {
		t.Errorf("Value = %v, want true", v)
	}
	if w.Environment.Atmosphere != "the morning mist hangs heavy" {
		t.Errorf("Atmosphere = %q, want installed text", w.Environment.Atmosphere)
	}
	if !w.Environment.LastAtmosphereRefreshAt.Equal(at) {
		t.Errorf("LastAtmosphereRefreshAt = %v, want %v", w.Environment.LastAtmosphereRefreshAt, at)
	}
}

func TestApplyAtmosphereRefresh_TrimsWhitespace(t *testing.T) {
	w := newAtmosphereTestWorld(t)
	at := time.Now()
	v, err := ApplyAtmosphereRefresh("  the bells toll thrice  \n", at).Fn(w)
	if err != nil {
		t.Fatalf("ApplyAtmosphereRefresh: %v", err)
	}
	if !v.(bool) {
		t.Error("expected wrote=true")
	}
	if w.Environment.Atmosphere != "the bells toll thrice" {
		t.Errorf("Atmosphere = %q, want trimmed", w.Environment.Atmosphere)
	}
}

func TestApplyAtmosphereRefresh_RejectsEmpty(t *testing.T) {
	w := newAtmosphereTestWorld(t)
	w.Environment.Atmosphere = "prior"

	v, err := ApplyAtmosphereRefresh("   ", time.Now()).Fn(w)
	if err == nil {
		t.Fatal("expected error for empty text, got nil")
	}
	wrote, _ := v.(bool)
	if wrote {
		t.Error("expected wrote=false on empty")
	}
	if w.Environment.Atmosphere != "prior" {
		t.Errorf("Atmosphere mutated on reject = %q, want prior untouched", w.Environment.Atmosphere)
	}
}

func TestApplyAtmosphereRefresh_DedupOnIdenticalText(t *testing.T) {
	w := newAtmosphereTestWorld(t)
	priorAt := time.Date(2026, 5, 17, 8, 0, 0, 0, time.UTC)
	w.Environment.Atmosphere = "the morning mist hangs heavy"
	w.Environment.LastAtmosphereRefreshAt = priorAt

	dedupAt := priorAt.Add(4 * time.Hour)
	v, err := ApplyAtmosphereRefresh("the morning mist hangs heavy", dedupAt).Fn(w)
	if err != nil {
		t.Fatalf("ApplyAtmosphereRefresh: %v", err)
	}
	wrote, _ := v.(bool)
	if wrote {
		t.Error("expected wrote=false on dedup")
	}
	if !w.Environment.LastAtmosphereRefreshAt.Equal(priorAt) {
		t.Errorf("LastAtmosphereRefreshAt mutated on dedup = %v, want %v", w.Environment.LastAtmosphereRefreshAt, priorAt)
	}
}

func TestApplyAtmosphereRefresh_DedupHandlesWhitespaceDifferences(t *testing.T) {
	w := newAtmosphereTestWorld(t)
	priorAt := time.Date(2026, 5, 17, 8, 0, 0, 0, time.UTC)
	w.Environment.Atmosphere = "the morning mist hangs heavy"
	w.Environment.LastAtmosphereRefreshAt = priorAt

	v, err := ApplyAtmosphereRefresh("\n  the morning mist hangs heavy  \n", time.Now()).Fn(w)
	if err != nil {
		t.Fatalf("ApplyAtmosphereRefresh: %v", err)
	}
	if v.(bool) {
		t.Error("expected wrote=false — text differs only in whitespace")
	}
}

// equalStrings compares two string slices element-wise.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// guard against the trimmed-empty branch crossing prior atmosphere check.
func TestApplyAtmosphereRefresh_EmptyRejectsEvenWithEmptyPrior(t *testing.T) {
	w := newAtmosphereTestWorld(t)
	w.Environment.Atmosphere = ""

	v, err := ApplyAtmosphereRefresh(strings.Repeat(" ", 10), time.Now()).Fn(w)
	if err == nil {
		t.Fatal("expected empty-text error")
	}
	if v.(bool) {
		t.Error("expected wrote=false")
	}
}
