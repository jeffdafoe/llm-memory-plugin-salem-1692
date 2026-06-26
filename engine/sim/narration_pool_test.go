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
