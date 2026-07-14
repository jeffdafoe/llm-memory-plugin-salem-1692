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

// WeatherChangedSinceAtmosphere is the fact that lets the prompt name the prior
// as the OLD sky (LLM-399). It is derived — LastWeatherChangeAt vs
// LastAtmosphereRefreshAt — so no new persisted state was added for it.
func TestFetchAtmosphereContext_WeatherChangedSinceAtmosphere(t *testing.T) {
	early := time.Date(2026, 5, 17, 18, 0, 0, 0, time.UTC)
	late := time.Date(2026, 5, 17, 20, 0, 0, 0, time.UTC)
	at := time.Date(2026, 5, 17, 21, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name           string
		lastAtmosphere time.Time
		lastWeather    time.Time
		want           bool
	}{
		// The storm cleared after the prose was written — the prose describes a
		// sky that is gone.
		{"weather turned after the prose", early, late, true},
		// The prose was written after the sky settled — it already describes it.
		{"prose written after the weather", late, early, false},
		// Boot: LastAtmosphereRefreshAt is restart-lossy and reads zero, while
		// SeedWeatherClear stamps the weather clock. The prose restored from the
		// checkpoint predates this boot's sky, so flagging it stale is correct —
		// this is the path that carried rain prose across restarts for days.
		{"boot with restored prose", time.Time{}, late, true},
		// A world whose weather clock has never been seeded has nothing to say.
		{"weather clock unseeded", late, time.Time{}, false},
	} {
		w := newAtmosphereTestWorld(t)
		w.Environment.Atmosphere = "the rain falleth"
		w.Environment.LastAtmosphereRefreshAt = tc.lastAtmosphere
		w.Environment.LastWeatherChangeAt = tc.lastWeather

		v := runAtmosphereCmd(t, w, FetchAtmosphereContext(at))
		ctx, ok := v.(AtmosphereContext)
		if !ok {
			t.Fatalf("%s: FetchAtmosphereContext returned %T, want AtmosphereContext", tc.name, v)
		}
		if ctx.WeatherChangedSinceAtmosphere != tc.want {
			t.Errorf("%s: WeatherChangedSinceAtmosphere = %v, want %v", tc.name, ctx.WeatherChangedSinceAtmosphere, tc.want)
		}
	}
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

// --- Activity digest tests -----------------------------------------

// TestFetchAtmosphereContext_NoDigestOnFirstFire: with
// LastAtmosphereRefreshAt zero, no digest is emitted even if the
// action log has entries — first fire has no prior window.
func TestFetchAtmosphereContext_NoDigestOnFirstFire(t *testing.T) {
	w := newAtmosphereTestWorld(t)
	w.Actors["hannah"] = &Actor{ID: "hannah", DisplayName: "Hannah", Kind: KindNPCShared}
	w.ActionLog = []ActionLogEntry{
		{ActorID: "hannah", OccurredAt: time.Now().UTC(), ActionType: ActionTypeSpoke, Text: "hi"},
	}
	// LastAtmosphereRefreshAt deliberately zero.

	v := runAtmosphereCmd(t, w, FetchAtmosphereContext(time.Now()))
	ctx := v.(AtmosphereContext)
	if ctx.ActivityDigest != nil {
		t.Errorf("ActivityDigest = %v, want nil on first fire (zero LastAtmosphereRefreshAt)", ctx.ActivityDigest)
	}
}

// TestFetchAtmosphereContext_DigestSinceLastRefresh: only entries
// strictly after LastAtmosphereRefreshAt are included.
func TestFetchAtmosphereContext_DigestSinceLastRefresh(t *testing.T) {
	w := newAtmosphereTestWorld(t)
	priorAt := time.Date(2026, 5, 17, 8, 0, 0, 0, time.UTC)
	w.Environment.LastAtmosphereRefreshAt = priorAt
	w.Actors["hannah"] = &Actor{ID: "hannah", DisplayName: "Hannah", Kind: KindNPCShared}
	w.ActionLog = []ActionLogEntry{
		// Before the refresh — should be excluded.
		{ActorID: "hannah", OccurredAt: priorAt.Add(-1 * time.Hour), ActionType: ActionTypeSpoke, Text: "old"},
		// At the refresh — should be excluded (strict After).
		{ActorID: "hannah", OccurredAt: priorAt, ActionType: ActionTypeSpoke, Text: "boundary"},
		// After the refresh — should be included.
		{ActorID: "hannah", OccurredAt: priorAt.Add(30 * time.Minute), ActionType: ActionTypeSpoke, Text: "fresh-1"},
		{ActorID: "hannah", OccurredAt: priorAt.Add(45 * time.Minute), ActionType: ActionTypeSpoke, Text: "fresh-2"},
	}

	v := runAtmosphereCmd(t, w, FetchAtmosphereContext(priorAt.Add(4*time.Hour)))
	ctx := v.(AtmosphereContext)
	if len(ctx.ActivityDigest) != 1 {
		t.Fatalf("ActivityDigest len = %d, want 1", len(ctx.ActivityDigest))
	}
	got := ctx.ActivityDigest[0]
	if got.ActorID != "hannah" {
		t.Errorf("ActorID = %q, want hannah", got.ActorID)
	}
	if got.Counts[ActionTypeSpoke] != 2 {
		t.Errorf("Counts[Spoke] = %d, want 2 (only fresh-1 + fresh-2 after the boundary)", got.Counts[ActionTypeSpoke])
	}
}

// TestFetchAtmosphereContext_DigestGroupsByActor: entries for the
// same actor across multiple ActionTypes collapse into one digest
// entry with per-type counts.
func TestFetchAtmosphereContext_DigestGroupsByActor(t *testing.T) {
	w := newAtmosphereTestWorld(t)
	priorAt := time.Date(2026, 5, 17, 8, 0, 0, 0, time.UTC)
	w.Environment.LastAtmosphereRefreshAt = priorAt
	w.Actors["hannah"] = &Actor{ID: "hannah", DisplayName: "Hannah", Kind: KindNPCShared}
	w.ActionLog = []ActionLogEntry{
		{ActorID: "hannah", OccurredAt: priorAt.Add(10 * time.Minute), ActionType: ActionTypeSpoke},
		{ActorID: "hannah", OccurredAt: priorAt.Add(20 * time.Minute), ActionType: ActionTypeSpoke},
		{ActorID: "hannah", OccurredAt: priorAt.Add(30 * time.Minute), ActionType: ActionTypeSpoke},
		{ActorID: "hannah", OccurredAt: priorAt.Add(40 * time.Minute), ActionType: ActionTypeWalked},
		{ActorID: "hannah", OccurredAt: priorAt.Add(50 * time.Minute), ActionType: ActionTypeConsumed},
	}

	v := runAtmosphereCmd(t, w, FetchAtmosphereContext(priorAt.Add(4*time.Hour)))
	ctx := v.(AtmosphereContext)
	if len(ctx.ActivityDigest) != 1 {
		t.Fatalf("ActivityDigest len = %d, want 1 (one actor)", len(ctx.ActivityDigest))
	}
	got := ctx.ActivityDigest[0]
	if got.Counts[ActionTypeSpoke] != 3 {
		t.Errorf("Counts[Spoke] = %d, want 3", got.Counts[ActionTypeSpoke])
	}
	if got.Counts[ActionTypeWalked] != 1 {
		t.Errorf("Counts[Walked] = %d, want 1", got.Counts[ActionTypeWalked])
	}
	if got.Counts[ActionTypeConsumed] != 1 {
		t.Errorf("Counts[Consumed] = %d, want 1", got.Counts[ActionTypeConsumed])
	}
	if got.Counts[ActionTypePaid] != 0 {
		t.Errorf("Counts[Paid] = %d, want 0 (absent)", got.Counts[ActionTypePaid])
	}
}

// TestFetchAtmosphereContext_DigestExcludesPC: PC entries in the
// action log are filtered out — atmosphere is village-NPC-focused.
func TestFetchAtmosphereContext_DigestExcludesPC(t *testing.T) {
	w := newAtmosphereTestWorld(t)
	priorAt := time.Date(2026, 5, 17, 8, 0, 0, 0, time.UTC)
	w.Environment.LastAtmosphereRefreshAt = priorAt
	w.Actors["jeff"] = &Actor{ID: "jeff", DisplayName: "Jeff", Kind: KindPC}
	w.Actors["hannah"] = &Actor{ID: "hannah", DisplayName: "Hannah", Kind: KindNPCShared}
	w.ActionLog = []ActionLogEntry{
		{ActorID: "jeff", OccurredAt: priorAt.Add(10 * time.Minute), ActionType: ActionTypeSpoke},
		{ActorID: "hannah", OccurredAt: priorAt.Add(20 * time.Minute), ActionType: ActionTypeSpoke},
	}

	v := runAtmosphereCmd(t, w, FetchAtmosphereContext(priorAt.Add(4*time.Hour)))
	ctx := v.(AtmosphereContext)
	if len(ctx.ActivityDigest) != 1 {
		t.Fatalf("ActivityDigest len = %d, want 1 (PC filtered)", len(ctx.ActivityDigest))
	}
	if ctx.ActivityDigest[0].ActorID != "hannah" {
		t.Errorf("ActorID = %q, want hannah (PC excluded)", ctx.ActivityDigest[0].ActorID)
	}
}

// TestFetchAtmosphereContext_DigestOrderedByDisplayName: multiple
// actors are sorted ascending by DisplayName so the prompt rendering
// is deterministic.
func TestFetchAtmosphereContext_DigestOrderedByDisplayName(t *testing.T) {
	w := newAtmosphereTestWorld(t)
	priorAt := time.Date(2026, 5, 17, 8, 0, 0, 0, time.UTC)
	w.Environment.LastAtmosphereRefreshAt = priorAt
	w.Actors["c"] = &Actor{ID: "c", DisplayName: "Charlie", Kind: KindNPCShared}
	w.Actors["a"] = &Actor{ID: "a", DisplayName: "Alice", Kind: KindNPCShared}
	w.Actors["b"] = &Actor{ID: "b", DisplayName: "Bob", Kind: KindNPCShared}
	at := priorAt.Add(30 * time.Minute)
	w.ActionLog = []ActionLogEntry{
		{ActorID: "c", OccurredAt: at, ActionType: ActionTypeSpoke},
		{ActorID: "a", OccurredAt: at, ActionType: ActionTypeSpoke},
		{ActorID: "b", OccurredAt: at, ActionType: ActionTypeSpoke},
	}

	v := runAtmosphereCmd(t, w, FetchAtmosphereContext(priorAt.Add(4*time.Hour)))
	ctx := v.(AtmosphereContext)
	if len(ctx.ActivityDigest) != 3 {
		t.Fatalf("ActivityDigest len = %d, want 3", len(ctx.ActivityDigest))
	}
	want := []string{"Alice", "Bob", "Charlie"}
	for i, w := range want {
		if ctx.ActivityDigest[i].DisplayName != w {
			t.Errorf("ActivityDigest[%d].DisplayName = %q, want %q", i, ctx.ActivityDigest[i].DisplayName, w)
		}
	}
}

// TestFetchAtmosphereContext_DigestUnknownActorSkipped: action log
// entries for actors no longer in World (defensive — actor removed
// while log still has rows) are skipped, not panicked-on.
func TestFetchAtmosphereContext_DigestUnknownActorSkipped(t *testing.T) {
	w := newAtmosphereTestWorld(t)
	priorAt := time.Date(2026, 5, 17, 8, 0, 0, 0, time.UTC)
	w.Environment.LastAtmosphereRefreshAt = priorAt
	w.Actors["hannah"] = &Actor{ID: "hannah", DisplayName: "Hannah", Kind: KindNPCShared}
	at := priorAt.Add(30 * time.Minute)
	w.ActionLog = []ActionLogEntry{
		{ActorID: "ghost", OccurredAt: at, ActionType: ActionTypeSpoke}, // not in Actors
		{ActorID: "hannah", OccurredAt: at, ActionType: ActionTypeSpoke},
	}

	v := runAtmosphereCmd(t, w, FetchAtmosphereContext(priorAt.Add(4*time.Hour)))
	ctx := v.(AtmosphereContext)
	if len(ctx.ActivityDigest) != 1 {
		t.Fatalf("ActivityDigest len = %d, want 1 (ghost skipped)", len(ctx.ActivityDigest))
	}
	if ctx.ActivityDigest[0].ActorID != "hannah" {
		t.Errorf("ActorID = %q, want hannah", ctx.ActivityDigest[0].ActorID)
	}
}

// TestFetchAtmosphereContext_DigestEmptyActionLog: with non-zero
// LastAtmosphereRefreshAt but no action-log entries, digest is empty.
func TestFetchAtmosphereContext_DigestEmptyActionLog(t *testing.T) {
	w := newAtmosphereTestWorld(t)
	w.Environment.LastAtmosphereRefreshAt = time.Date(2026, 5, 17, 8, 0, 0, 0, time.UTC)
	w.Actors["hannah"] = &Actor{ID: "hannah", DisplayName: "Hannah", Kind: KindNPCShared}

	v := runAtmosphereCmd(t, w, FetchAtmosphereContext(time.Now()))
	ctx := v.(AtmosphereContext)
	if ctx.ActivityDigest != nil {
		t.Errorf("ActivityDigest = %v, want nil on empty action log", ctx.ActivityDigest)
	}
}
