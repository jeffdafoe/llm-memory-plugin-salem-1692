package sim

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// narration_pool.go — the narration-pool registry (ZBBS-WORK-399).
//
// The engine's deterministic narration moments draw from small hand-curated
// phrase pools (businessowner hospitality, lodging day-cycle, the NPC retire
// farewell). A player who triggers the same moment often enough cycles
// through every variant and the prose starts feeling templated. This
// registry makes each pool expandable: every draw counts, and when a pool
// has been drawn NarrationExpansionCycleFactor times its own size, the
// world nudges the narration-expansion cascade (engine/sim/cascade/
// narration_expansion.go), which fires ONE cheap LLM call ("here are my
// lines for this moment — write 5 more in the same voice"), validates the
// output, persists it to the narration_pool_expansion table, and appends
// it to the live pool. Then draws go back to being fully deterministic.
//
// Durability split (Postgres-as-durable-store): generated PHRASES are
// durable — written through to narration_pool_expansion at expansion time
// (never part of the checkpoint) and re-merged at boot via
// MergeNarrationExpansions. COUNTERS are transient — Draws/InFlight reset
// on restart, which at worst delays the next expansion by another
// cycle-factor's worth of draws. Losing counter progress on a restart is
// harmless; expansion is a liveness nicety, not state.
//
// Threading: every draw site runs on the world goroutine (commands and
// inline subscribers), so Draws/InFlight/Extra need no synchronization.
// The expansion cascade goroutine never touches a pool directly — it
// round-trips through FetchNarrationExpansionContext /
// FinishNarrationExpansion Commands, same shape as the atmosphere sweep.

const (
	// NarrationExpansionCycleFactor — a pool is "exhausted" once it has
	// been drawn this many times its current size (K=3: a 6-line pool
	// expands after 18 draws). Counted from boot or from the last
	// expansion attempt, whichever is later.
	NarrationExpansionCycleFactor = 3

	// NarrationPoolMaxPhrases caps a pool's merged (seed + expanded) size.
	// A pool at or past the cap never triggers another expansion, and
	// FinishNarrationExpansion / MergeNarrationExpansions trim to it —
	// so a hot pool can't grow without bound.
	NarrationPoolMaxPhrases = 30

	// NarrationExpansionBatchSize is how many new lines one expansion
	// asks the LLM for (fewer near the cap — see FetchNarrationExpansionContext).
	NarrationExpansionBatchSize = 5

	// NarrationMaxPhraseRunes bounds a single generated line. The longest
	// seed line is ~70 runes; 160 gives the model headroom while staying
	// well inside the 220-rune action-log truncation downstream
	// (MaxActionLogTextLen) so an expanded line never renders clipped.
	NarrationMaxPhraseRunes = 160
)

// NarrationKeyNPCRetire keys the auto-sleep retire-farewell pool
// (npc_sleep.go retireLines). The lodging pools are keyed by their
// existing LodgingReason* strings; the businessowner pools by
// BusinessownerNarrationKey.
const NarrationKeyNPCRetire = "npc_retire"

// NarrationKeyEstablishmentClosing keys the keeper's closing-call pool
// (establishment_closeup.go closingLines) — the "we're shut, head home" beat a
// live-in keeper speaks to lingering non-tenants when it beds down (LLM-129).
const NarrationKeyEstablishmentClosing = "establishment_closing"

// BusinessownerNarrationKey derives the registry key for one hospitality
// pool, e.g. "businessowner_flamboyant_greet". An unknown flavor or
// trigger produces a key absent from the registry, which draws as an
// empty pool — preserving the render path's degrade-to-silence posture.
func BusinessownerNarrationKey(flavor string, trigger BusinessownerTrigger) string {
	return "businessowner_" + flavor + "_" + string(trigger)
}

// NarrationPool is one expandable phrase pool. Seed is the compile-time
// slice (shared with the authoring table, never mutated); Extra holds
// DB-loaded and runtime-generated lines. Draws and InFlight are transient
// expansion bookkeeping (see file header).
type NarrationPool struct {
	Seed     []string
	Extra    []string
	Draws    int
	InFlight bool
}

// Phrases returns the merged seed+extra pool as a fresh slice. Draw sites
// index into it; the expansion flow snapshots it into the LLM prompt.
func (p *NarrationPool) Phrases() []string {
	out := make([]string, 0, len(p.Seed)+len(p.Extra))
	out = append(out, p.Seed...)
	out = append(out, p.Extra...)
	return out
}

// narrationPoolMeta carries what the expansion prompt needs to know about
// a pool beyond its lines: a human description of the moment the lines
// narrate, and whether the {customer} token is part of the pool's
// vocabulary (businessowner pools interpolate it; generated lines for
// any other pool must carry no tokens at all).
type narrationPoolMeta struct {
	Description   string
	CustomerToken bool
}

// narrationPoolMetas maps every seed pool to its prompt metadata. The
// init() check below enforces exhaustiveness at package load — a new
// seed pool without meta (or meta without a pool) panics at startup,
// mirroring businessowner.go's flavor/trigger check.
var narrationPoolMetas = map[string]narrationPoolMeta{
	"businessowner_flamboyant_greet": {
		Description:   "A warm, flamboyant shopkeeper greets a customer who has just come into their establishment.",
		CustomerToken: true,
	},
	"businessowner_flamboyant_handover": {
		Description:   "A warm, flamboyant shopkeeper hands a customer the goods they just bought.",
		CustomerToken: true,
	},
	"businessowner_flamboyant_farewell": {
		Description:   "A warm, flamboyant shopkeeper bids a departing customer farewell.",
		CustomerToken: true,
	},
	"businessowner_reserved_greet": {
		Description:   "A terse, reserved shopkeeper acknowledges a customer who has just come into their establishment.",
		CustomerToken: true,
	},
	"businessowner_reserved_handover": {
		Description:   "A terse, reserved shopkeeper hands a customer the goods they just bought.",
		CustomerToken: true,
	},
	"businessowner_reserved_farewell": {
		Description:   "A terse, reserved shopkeeper sees a customer out, in as few words as possible.",
		CustomerToken: true,
	},
	LodgingReasonCheckout: {
		Description: "Second-person narration to a player whose paid lodging stay has ended; they gather their things and head down to the inn's common area.",
	},
	LodgingReasonMorning: {
		Description: "Second-person narration to a player waking rested in their rented inn room in the morning and heading down to the common room.",
	},
	NarrationKeyNPCRetire: {
		Description: "A villager excusing themselves from an evening conversation to go to bed for the night.",
	},
	NarrationKeyEstablishmentClosing: {
		Description: "A shopkeeper or innkeeper calling closing time as they turn in for the night, telling any patrons still lingering inside that the establishment is shut and they should head home.",
	},
}

// narrationSeedPools builds the boot-time registry from the compile-time
// authoring tables. Called by NewWorld so every World (production or
// test-constructed) has the full pool set; DB-expanded extras merge in
// later via MergeNarrationExpansions.
func narrationSeedPools() map[string]*NarrationPool {
	pools := make(map[string]*NarrationPool, len(narrationPoolMetas))
	for flavor, triggers := range businessownerPhrases {
		for trigger, phrases := range triggers {
			pools[BusinessownerNarrationKey(flavor, trigger)] = &NarrationPool{Seed: phrases}
		}
	}
	for reason, phrases := range lodgingNarrationPools {
		pools[reason] = &NarrationPool{Seed: phrases}
	}
	pools[NarrationKeyNPCRetire] = &NarrationPool{Seed: retireLines}
	pools[NarrationKeyEstablishmentClosing] = &NarrationPool{Seed: closingLines}
	return pools
}

// init enforces registry/meta exhaustiveness at package load: every seed
// pool must have prompt metadata and vice versa. Panic over silent skip —
// a mismatch means a pool that can never expand (or meta describing a
// pool that doesn't exist), and it should fail the build's first test
// run, not surface in production logs.
func init() {
	pools := narrationSeedPools()
	for key := range pools {
		if _, ok := narrationPoolMetas[key]; !ok {
			panic(fmt.Sprintf("sim/narration_pool: seed pool %q has no narrationPoolMetas entry", key))
		}
	}
	for key := range narrationPoolMetas {
		if _, ok := pools[key]; !ok {
			panic(fmt.Sprintf("sim/narration_pool: narrationPoolMetas entry %q matches no seed pool", key))
		}
	}
}

// narrationDraw returns the merged pool for key and counts the draw,
// nudging the expansion cascade when the pool crosses the cycle-factor
// threshold (and is under cap, not already expanding, and a cascade is
// wired). The nudge is a non-blocking send on the buffered trigger
// channel — on a full channel the draw proceeds and the unchanged
// counter retries on a later draw. MUST be called from the world
// goroutine (a Command.Fn or inline subscriber).
//
// Returns nil for an unknown key — callers render "" / skip, the same
// degrade-to-silence the businessowner path always had.
func (w *World) narrationDraw(key string) []string {
	p := w.NarrationPools[key]
	if p == nil {
		return nil
	}
	merged := p.Phrases()
	n := len(merged)
	if n == 0 {
		return nil
	}
	p.Draws++
	if !p.InFlight && n < NarrationPoolMaxPhrases &&
		p.Draws >= NarrationExpansionCycleFactor*n && w.narrationExpandCh != nil {
		select {
		case w.narrationExpandCh <- key:
			p.InFlight = true
			p.Draws = 0
		default:
			// Trigger channel full — leave Draws past threshold so the
			// next draw retries the send.
		}
	}
	return merged
}

// SetNarrationExpansionTrigger installs the channel narrationDraw nudges
// when a pool crosses its expansion threshold. Installed by
// cascade.RegisterNarrationExpansion. Safe to call before Run, or from
// inside a Command.Fn. Nil (the default) disables nudging — draws still
// work, pools just never expand (tests, headless).
func (w *World) SetNarrationExpansionTrigger(ch chan<- string) {
	w.narrationExpandCh = ch
}

// SetNarrationExpansionSink installs the durable narration_pool_expansion
// writer (the pg repo). Wired in main.go before Run starts; nil (the
// default) makes AppendNarrationExpansionDurable a no-op success, so a
// sink-less world still expands in memory.
func (w *World) SetNarrationExpansionSink(s NarrationExpansionSink) {
	w.narrationExpansionSink = s
}

// AppendNarrationExpansionDurable writes accepted phrases through to the
// durable sink. Called from the expansion cascade goroutine (off-world);
// the sink field is set before Run and immutable after, so the read is
// race-free. A nil sink returns nil — in-memory-only expansion is the
// degraded-but-working mode. A sink error is returned to the caller,
// which must NOT apply the phrases in memory (durable-first: a phrase
// the DB never saw would vanish on restart and could be re-generated
// as a near-duplicate).
func (w *World) AppendNarrationExpansionDurable(ctx context.Context, poolKey string, phrases []string, generatedBy string) error {
	sink := w.narrationExpansionSink
	if sink == nil {
		return nil
	}
	return sink.Append(ctx, poolKey, phrases, generatedBy)
}

// normalizeNarrationPhrase is the dedupe key for a line: trimmed,
// case-folded. Two lines differing only in case or padding are the same
// line for membership purposes.
func normalizeNarrationPhrase(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// appendNarrationPhrases appends candidate lines to p, skipping
// duplicates (against the merged pool and within the batch) and stopping
// at the cap. Returns how many were actually appended. Shared by the
// boot-time merge and the runtime expansion apply.
func appendNarrationPhrases(p *NarrationPool, phrases []string) int {
	seen := make(map[string]struct{}, len(p.Seed)+len(p.Extra)+len(phrases))
	for _, s := range p.Seed {
		seen[normalizeNarrationPhrase(s)] = struct{}{}
	}
	for _, s := range p.Extra {
		seen[normalizeNarrationPhrase(s)] = struct{}{}
	}
	appended := 0
	for _, s := range phrases {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if len(p.Seed)+len(p.Extra) >= NarrationPoolMaxPhrases {
			break
		}
		key := normalizeNarrationPhrase(s)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		p.Extra = append(p.Extra, s)
		appended++
	}
	return appended
}

// MergeNarrationExpansions folds DB-loaded expansion rows into the
// registry. Called once from main.go between LoadWorld and Run (before
// the world goroutine starts — direct mutation is safe then). Unknown
// pool keys are logged and skipped, not fatal: a pool retired from the
// code may leave orphan rows behind, and they shouldn't block boot.
func (w *World) MergeNarrationExpansions(rows map[string][]string) {
	for key, phrases := range rows {
		p := w.NarrationPools[key]
		if p == nil {
			log.Printf("sim/narration_pool: %d persisted phrases for unknown pool %q skipped", len(phrases), key)
			continue
		}
		appendNarrationPhrases(p, phrases)
	}
}

// NarrationExpansionContext is the world-state snapshot one expansion
// LLM call works from. Wanted is how many new lines to ask for —
// truncated near the cap, and 0 when the pool reached cap between the
// nudge and the fetch (the cascade then just clears InFlight).
type NarrationExpansionContext struct {
	Key           string
	Phrases       []string
	Description   string
	CustomerToken bool
	Wanted        int
}

// FetchNarrationExpansionContext snapshots a pool for the expansion
// cascade. Errors only on caller-bug shapes (unknown key — the nudge
// channel only ever carries registry keys, so this means wiring drift).
func FetchNarrationExpansionContext(key string) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			p := w.NarrationPools[key]
			if p == nil {
				return NarrationExpansionContext{}, fmt.Errorf("FetchNarrationExpansionContext: unknown pool %q", key)
			}
			meta := narrationPoolMetas[key]
			merged := p.Phrases()
			wanted := NarrationExpansionBatchSize
			if room := NarrationPoolMaxPhrases - len(merged); room < wanted {
				wanted = room
			}
			if wanted < 0 {
				wanted = 0
			}
			return NarrationExpansionContext{
				Key:           key,
				Phrases:       merged,
				Description:   meta.Description,
				CustomerToken: meta.CustomerToken,
				Wanted:        wanted,
			}, nil
		},
	}
}

// FinishNarrationExpansion lands an expansion attempt back on the world:
// appends the accepted phrases (possibly none — every failure path in
// the cascade calls this with nil) and clears the in-flight flag so a
// later threshold crossing can fire again. Draws was already reset when
// the nudge was sent, so a failed attempt naturally rate-limits to one
// retry per cycle-factor's worth of draws. Returns the appended count.
func FinishNarrationExpansion(key string, phrases []string) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			p := w.NarrationPools[key]
			if p == nil {
				return 0, fmt.Errorf("FinishNarrationExpansion: unknown pool %q", key)
			}
			p.InFlight = false
			if len(phrases) == 0 {
				return 0, nil
			}
			return appendNarrationPhrases(p, phrases), nil
		},
	}
}

// ValidateNarrationPhrase checks one generated line against the pool's
// content rules: non-empty after trim, within NarrationMaxPhraseRunes,
// single-line (no control characters), and token discipline — pools
// without the {customer} vocabulary admit no braces at all; pools with
// it admit {customer} and nothing else brace-shaped. Returns a
// human-readable reason or "" when the line passes. Exported for the
// cascade's batch validation; lives here so the rules sit next to the
// pool definitions they protect.
func ValidateNarrationPhrase(phrase string, customerToken bool) string {
	s := strings.TrimSpace(phrase)
	if s == "" {
		return "empty after trim"
	}
	if n := len([]rune(s)); n > NarrationMaxPhraseRunes {
		return fmt.Sprintf("too long (%d runes, max %d)", n, NarrationMaxPhraseRunes)
	}
	if containsControlChar(s) {
		return "contains control character"
	}
	stripped := s
	if customerToken {
		stripped = strings.ReplaceAll(stripped, "{customer}", "")
	}
	if strings.ContainsAny(stripped, "{}") {
		if customerToken {
			return "contains a brace token other than {customer}"
		}
		return "contains a brace token; this pool admits none"
	}
	return ""
}
