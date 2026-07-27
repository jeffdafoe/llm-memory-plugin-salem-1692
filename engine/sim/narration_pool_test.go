package sim

import (
	"strings"
	"testing"
)

// narration_pool_test.go — substrate tests for the narration pool
// registry (ZBBS-WORK-399): seeding, draw accounting, the expansion
// nudge, the apply/merge paths, and phrase validation. The cascade
// driver (LLM call + reply contract) has its own surface in
// engine/sim/cascade/narration_expansion_test.go.

// narrationTestWorld builds a literal world with just the registry —
// the only state these tests touch.
func narrationTestWorld() *World {
	return &World{NarrationPools: narrationSeedPools()}
}

func TestNarrationSeedPools_RegistryShape(t *testing.T) {
	pools := narrationSeedPools()
	if len(pools) != 10 {
		t.Errorf("seed registry has %d pools, want 10 (6 businessowner + 2 lodging + retire + establishment-closing)", len(pools))
	}
	for key, p := range pools {
		if len(p.Seed) == 0 {
			t.Errorf("pool %q has an empty seed", key)
		}
		if len(p.Extra) != 0 || p.Draws != 0 || p.InFlight {
			t.Errorf("pool %q not pristine at seed time: %+v", key, p)
		}
		if _, ok := narrationPoolMetas[key]; !ok {
			t.Errorf("pool %q has no prompt meta", key)
		}
	}
	// Spot-check the derived businessowner key shape.
	if _, ok := pools[BusinessownerNarrationKey("flamboyant", BusinessownerTriggerGreet)]; !ok {
		t.Error("businessowner_flamboyant_greet missing from registry")
	}
}

func TestNarrationDraw_CountsAndNudgesAtThreshold(t *testing.T) {
	w := narrationTestWorld()
	trigger := make(chan string, 1)
	w.SetNarrationExpansionTrigger(trigger)

	p := w.NarrationPools[NarrationKeyNPCRetire]
	threshold := NarrationExpansionCycleFactor * len(p.Seed)

	for i := 0; i < threshold-1; i++ {
		if got := w.narrationDraw(NarrationKeyNPCRetire); len(got) != len(p.Seed) {
			t.Fatalf("draw %d returned %d phrases, want %d", i, len(got), len(p.Seed))
		}
	}
	select {
	case key := <-trigger:
		t.Fatalf("nudged %q after %d draws, threshold is %d", key, threshold-1, threshold)
	default:
	}

	w.narrationDraw(NarrationKeyNPCRetire)
	select {
	case key := <-trigger:
		if key != NarrationKeyNPCRetire {
			t.Errorf("nudge carried %q, want %q", key, NarrationKeyNPCRetire)
		}
	default:
		t.Fatalf("no nudge after %d draws (threshold)", threshold)
	}
	if !p.InFlight {
		t.Error("InFlight not set after nudge")
	}
	if p.Draws != 0 {
		t.Errorf("Draws = %d after nudge, want 0", p.Draws)
	}

	// In flight: further threshold crossings must not re-nudge.
	for i := 0; i < threshold*2; i++ {
		w.narrationDraw(NarrationKeyNPCRetire)
	}
	select {
	case <-trigger:
		t.Error("re-nudged while expansion in flight")
	default:
	}
}

func TestNarrationDraw_NoChannelStillDraws(t *testing.T) {
	w := narrationTestWorld()
	p := w.NarrationPools[NarrationKeyNPCRetire]
	for i := 0; i < NarrationExpansionCycleFactor*len(p.Seed)*2; i++ {
		if got := w.narrationDraw(NarrationKeyNPCRetire); len(got) == 0 {
			t.Fatal("draw returned empty pool")
		}
	}
	if p.InFlight {
		t.Error("InFlight set with no trigger channel installed")
	}
}

func TestNarrationDraw_CapSuppressesNudge(t *testing.T) {
	w := narrationTestWorld()
	trigger := make(chan string, 1)
	w.SetNarrationExpansionTrigger(trigger)

	p := w.NarrationPools[NarrationKeyNPCRetire]
	for i := len(p.Seed) + len(p.Extra); i < NarrationPoolMaxPhrases; i++ {
		p.Extra = append(p.Extra, strings.Repeat("x", 3)+string(rune('a'+i)))
	}
	for i := 0; i < NarrationExpansionCycleFactor*NarrationPoolMaxPhrases*2; i++ {
		w.narrationDraw(NarrationKeyNPCRetire)
	}
	select {
	case <-trigger:
		t.Error("nudged a pool already at cap")
	default:
	}
}

func TestNarrationDraw_UnknownKey(t *testing.T) {
	w := narrationTestWorld()
	if got := w.narrationDraw("no-such-pool"); got != nil {
		t.Errorf("unknown key returned %v, want nil", got)
	}
	// Nil registry (literal-built world) degrades the same way.
	bare := &World{}
	if got := bare.narrationDraw(NarrationKeyNPCRetire); got != nil {
		t.Errorf("nil-registry draw returned %v, want nil", got)
	}
}

func TestFinishNarrationExpansion_AppendsAndClearsFlag(t *testing.T) {
	w := narrationTestWorld()
	p := w.NarrationPools[NarrationKeyNPCRetire]
	p.InFlight = true
	seedLen := len(p.Seed)

	res, err := FinishNarrationExpansion(NarrationKeyNPCRetire, []string{
		"A new line entirely.",
		"  a new line ENTIRELY.  ", // dup of the previous, case/space-insensitive
		p.Seed[0],                  // dup of a seed line
		"Another fresh line.",
	}).Fn(w)
	if err != nil {
		t.Fatalf("FinishNarrationExpansion: %v", err)
	}
	if appended := res.(int); appended != 2 {
		t.Errorf("appended = %d, want 2 (two dups dropped)", appended)
	}
	if p.InFlight {
		t.Error("InFlight still set after finish")
	}
	if len(p.Extra) != 2 {
		t.Errorf("Extra has %d lines, want 2: %v", len(p.Extra), p.Extra)
	}
	if got := len(p.Phrases()); got != seedLen+2 {
		t.Errorf("merged pool = %d lines, want %d", got, seedLen+2)
	}

	// Nil phrases: clears the flag, appends nothing.
	p.InFlight = true
	res, err = FinishNarrationExpansion(NarrationKeyNPCRetire, nil).Fn(w)
	if err != nil {
		t.Fatalf("FinishNarrationExpansion(nil): %v", err)
	}
	if appended := res.(int); appended != 0 {
		t.Errorf("nil finish appended %d", appended)
	}
	if p.InFlight {
		t.Error("InFlight still set after nil finish")
	}

	// Unknown key errors.
	if _, err := FinishNarrationExpansion("no-such-pool", nil).Fn(w); err == nil {
		t.Error("unknown key: want error")
	}
}

func TestFinishNarrationExpansion_EnforcesCap(t *testing.T) {
	w := narrationTestWorld()
	p := w.NarrationPools[NarrationKeyNPCRetire]
	for i := len(p.Seed); i < NarrationPoolMaxPhrases-1; i++ {
		p.Extra = append(p.Extra, "filler line number "+string(rune('a'+i)))
	}
	res, err := FinishNarrationExpansion(NarrationKeyNPCRetire, []string{
		"One more fits.",
		"This one does not.",
	}).Fn(w)
	if err != nil {
		t.Fatalf("FinishNarrationExpansion: %v", err)
	}
	if appended := res.(int); appended != 1 {
		t.Errorf("appended = %d, want 1 (cap)", appended)
	}
	if got := len(p.Phrases()); got != NarrationPoolMaxPhrases {
		t.Errorf("merged pool = %d, want exactly the cap %d", got, NarrationPoolMaxPhrases)
	}
}

func TestMergeNarrationExpansions(t *testing.T) {
	w := narrationTestWorld()
	p := w.NarrationPools[NarrationKeyNPCRetire]
	seedLen := len(p.Seed)

	w.MergeNarrationExpansions(map[string][]string{
		NarrationKeyNPCRetire: {
			"A persisted line from a prior run.",
			p.Seed[1], // seed dup — skipped
		},
		"retired-pool-key": {"orphan row"}, // unknown — logged + skipped, not fatal
	})
	if got := len(p.Phrases()); got != seedLen+1 {
		t.Errorf("merged pool = %d lines, want %d", got, seedLen+1)
	}
}

func TestFetchNarrationExpansionContext(t *testing.T) {
	w := narrationTestWorld()

	res, err := FetchNarrationExpansionContext(NarrationKeyNPCRetire).Fn(w)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	nctx := res.(NarrationExpansionContext)
	if nctx.Key != NarrationKeyNPCRetire {
		t.Errorf("Key = %q", nctx.Key)
	}
	if nctx.CustomerToken {
		t.Error("retire pool must not admit {customer}")
	}
	if nctx.Description == "" {
		t.Error("empty Description")
	}
	if nctx.Wanted != NarrationExpansionBatchSize {
		t.Errorf("Wanted = %d, want %d", nctx.Wanted, NarrationExpansionBatchSize)
	}
	if len(nctx.Phrases) != len(w.NarrationPools[NarrationKeyNPCRetire].Seed) {
		t.Errorf("Phrases = %d lines", len(nctx.Phrases))
	}

	// Businessowner pools carry the customer token.
	res, err = FetchNarrationExpansionContext(BusinessownerNarrationKey("flamboyant", BusinessownerTriggerGreet)).Fn(w)
	if err != nil {
		t.Fatalf("fetch businessowner: %v", err)
	}
	if !res.(NarrationExpansionContext).CustomerToken {
		t.Error("businessowner pool should admit {customer}")
	}

	// Wanted shrinks near the cap, floors at 0 past it.
	p := w.NarrationPools[NarrationKeyNPCRetire]
	for i := len(p.Seed); i < NarrationPoolMaxPhrases-2; i++ {
		p.Extra = append(p.Extra, "filler "+string(rune('a'+i)))
	}
	res, _ = FetchNarrationExpansionContext(NarrationKeyNPCRetire).Fn(w)
	if got := res.(NarrationExpansionContext).Wanted; got != 2 {
		t.Errorf("near-cap Wanted = %d, want 2", got)
	}
	p.Extra = append(p.Extra, "filler y", "filler z")
	res, _ = FetchNarrationExpansionContext(NarrationKeyNPCRetire).Fn(w)
	if got := res.(NarrationExpansionContext).Wanted; got != 0 {
		t.Errorf("at-cap Wanted = %d, want 0", got)
	}

	if _, err := FetchNarrationExpansionContext("no-such-pool").Fn(w); err == nil {
		t.Error("unknown key: want error")
	}
}

// TestFetchNarrationPools covers the operator read (LLM-538): every pool, the
// seed/extra split, the meta, and the transient counters.
func TestFetchNarrationPools(t *testing.T) {
	w := narrationTestWorld()

	// Give one pool a distinguishable runtime state: two expansions and a draw
	// counter mid-cycle, with an attempt in flight.
	retire := w.NarrationPools[NarrationKeyNPCRetire]
	retire.Extra = append(retire.Extra, "Goodnight, and God keep you.", "I'll to my rest now.")
	retire.Draws = 4
	retire.InFlight = true

	res, err := FetchNarrationPools("").Fn(w)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	views := res.([]NarrationPoolView)
	if len(views) != len(w.NarrationPools) {
		t.Fatalf("views = %d, want %d (every pool)", len(views), len(w.NarrationPools))
	}
	for i := 1; i < len(views); i++ {
		if views[i-1].Key >= views[i].Key {
			t.Fatalf("not sorted by key: %q before %q", views[i-1].Key, views[i].Key)
		}
	}

	var got NarrationPoolView
	for _, v := range views {
		if v.Key == NarrationKeyNPCRetire {
			got = v
		}
	}
	if got.Key == "" {
		t.Fatalf("%q missing from the view set", NarrationKeyNPCRetire)
	}
	if len(got.Seed) != len(retireLines) || len(got.Extra) != 2 {
		t.Errorf("seed/extra = %d/%d, want %d/2 — the split is the point of the view",
			len(got.Seed), len(got.Extra), len(retireLines))
	}
	if got.Draws != 4 || !got.InFlight {
		t.Errorf("draws/in_flight = %d/%v, want 4/true", got.Draws, got.InFlight)
	}
	if got.Description == "" || got.CustomerToken {
		t.Errorf("meta = %q/%v, want a description and no {customer} for the retire pool",
			got.Description, got.CustomerToken)
	}
	// Phrases() must agree with the live pool's own merge — the operator read and
	// the draw sites cannot be allowed to show different orders.
	live := retire.Phrases()
	merged := got.Phrases()
	if len(merged) != len(live) {
		t.Fatalf("merged = %d lines, live pool = %d", len(merged), len(live))
	}
	for i := range live {
		if merged[i] != live[i] {
			t.Fatalf("merge order differs at %d: view %q, pool %q", i, merged[i], live[i])
		}
	}

	// A businessowner pool carries the token flag, so the flag is per-pool meta
	// rather than a constant.
	for _, v := range views {
		if v.Key == BusinessownerNarrationKey("flamboyant", BusinessownerTriggerGreet) && !v.CustomerToken {
			t.Error("businessowner pool reads customer_token=false")
		}
	}
}

// TestFetchNarrationPools_NoAliasing pins that a view can outlive the world
// goroutine: the whole reason the command copies rather than handing back the
// registry's slices is that the HTTP goroutine marshals the result AFTER
// SendContext returns, concurrently with the next draw or expansion apply.
func TestFetchNarrationPools_NoAliasing(t *testing.T) {
	w := narrationTestWorld()
	p := w.NarrationPools[NarrationKeyNPCRetire]
	appendNarrationPhrases(p, []string{"an expansion present at read time"})

	res, err := FetchNarrationPools(NarrationKeyNPCRetire).Fn(w)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	view := res.([]NarrationPoolView)[0]
	before := len(view.Extra)

	// Simulate the writer running after the read returned: a new expansion lands,
	// and both existing backing arrays are overwritten in place. An in-place
	// element write is the sharper test of the two — a length change could be
	// missed by a copy that shared the backing array (code_review).
	appendNarrationPhrases(p, []string{"a line generated after the read"})
	p.Seed[0] = "MUTATED SEED"
	p.Extra[0] = "MUTATED EXTRA"

	if len(view.Extra) != before {
		t.Errorf("view.Extra grew to %d after a post-read append — it aliases the pool", len(view.Extra))
	}
	if view.Seed[0] == "MUTATED SEED" {
		t.Error("view.Seed shares a backing array with the pool's seed slice")
	}
	if view.Extra[0] == "MUTATED EXTRA" {
		t.Error("view.Extra shares a backing array with the pool's extra slice")
	}
}

// TestFetchNarrationPools_KeyFilter: the filter narrows to one pool and is
// case-insensitive (the /recipes + /items posture), and an unknown key is an
// EMPTY result rather than an error — "no such pool" is a legitimate empty
// answer to "show me this pool", and the handler turns an error into a 422.
func TestFetchNarrationPools_KeyFilter(t *testing.T) {
	w := narrationTestWorld()

	for _, key := range []string{NarrationKeyNPCRetire, strings.ToUpper(NarrationKeyNPCRetire)} {
		res, err := FetchNarrationPools(key).Fn(w)
		if err != nil {
			t.Fatalf("fetch %q: %v", key, err)
		}
		views := res.([]NarrationPoolView)
		if len(views) != 1 || views[0].Key != NarrationKeyNPCRetire {
			t.Fatalf("key=%q returned %+v, want just the retire pool", key, views)
		}
	}

	res, err := FetchNarrationPools("no-such-pool").Fn(w)
	if err != nil {
		t.Fatalf("unknown key errored: %v", err)
	}
	if views := res.([]NarrationPoolView); len(views) != 0 {
		t.Errorf("unknown key returned %d views, want an empty result", len(views))
	}
}

// TestFetchNarrationPools_DoesNotCountADraw: the read must not advance the draw
// counter. If it did, an operator watching a pool would push it over its own
// expansion threshold — the observation would cause the thing being observed.
func TestFetchNarrationPools_DoesNotCountADraw(t *testing.T) {
	w := narrationTestWorld()
	trigger := make(chan string, 4)
	w.SetNarrationExpansionTrigger(trigger)

	p := w.NarrationPools[NarrationKeyNPCRetire]
	p.Draws = NarrationExpansionCycleFactor*len(p.Seed) - 1 // one draw short of the nudge

	for i := 0; i < 5; i++ {
		if _, err := FetchNarrationPools("").Fn(w); err != nil {
			t.Fatalf("fetch: %v", err)
		}
	}
	if p.Draws != NarrationExpansionCycleFactor*len(p.Seed)-1 {
		t.Errorf("Draws = %d after 5 reads, want it untouched", p.Draws)
	}
	select {
	case key := <-trigger:
		t.Fatalf("a read nudged the expansion cascade for %q", key)
	default:
	}
}

// TestNarrationPoolView_SharedWithExpansionFetch pins the shared lookup: the
// operator read and the expansion prompt must be built from the same merge and
// the same meta, so what /umbilical/narration shows for a pool IS what the next
// expansion will be generated against. Two code paths reading the registry
// independently is exactly how the two would drift.
func TestNarrationPoolView_SharedWithExpansionFetch(t *testing.T) {
	w := narrationTestWorld()
	key := BusinessownerNarrationKey("reserved", BusinessownerTriggerFarewell)
	w.NarrationPools[key].Extra = append(w.NarrationPools[key].Extra, "Fare you well, {customer}.")

	readRes, err := FetchNarrationPools(key).Fn(w)
	if err != nil {
		t.Fatalf("fetch pools: %v", err)
	}
	view := readRes.([]NarrationPoolView)[0]

	fetchRes, err := FetchNarrationExpansionContext(key).Fn(w)
	if err != nil {
		t.Fatalf("fetch expansion context: %v", err)
	}
	nctx := fetchRes.(NarrationExpansionContext)

	if view.Description != nctx.Description {
		t.Errorf("description differs:\n  read      %q\n  expansion %q", view.Description, nctx.Description)
	}
	if view.CustomerToken != nctx.CustomerToken {
		t.Errorf("customer_token differs: read %v, expansion %v", view.CustomerToken, nctx.CustomerToken)
	}
	merged := view.Phrases()
	if len(merged) != len(nctx.Phrases) {
		t.Fatalf("line count differs: read %d, expansion %d", len(merged), len(nctx.Phrases))
	}
	for i := range merged {
		if merged[i] != nctx.Phrases[i] {
			t.Fatalf("line %d differs:\n  read      %q\n  expansion %q", i, merged[i], nctx.Phrases[i])
		}
	}
}

func TestValidateNarrationPhrase(t *testing.T) {
	long := strings.Repeat("a", NarrationMaxPhraseRunes+1)
	cases := []struct {
		name          string
		phrase        string
		customerToken bool
		wantOK        bool
	}{
		{"plain line", "Safe travels, friend.", false, true},
		{"empty", "   ", false, false},
		{"too long", long, false, false},
		{"control char", "two\nlines", false, false},
		{"customer token allowed", "Welcome back, {customer}!", true, true},
		{"customer token forbidden", "Welcome back, {customer}!", false, false},
		{"foreign token with customer pools", "Take this, {item}.", true, false},
		{"stray brace", "A {strange line.", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason := ValidateNarrationPhrase(tc.phrase, tc.customerToken)
			if ok := reason == ""; ok != tc.wantOK {
				t.Errorf("ValidateNarrationPhrase(%q, %v) = %q, wantOK=%v", tc.phrase, tc.customerToken, reason, tc.wantOK)
			}
		})
	}
}
