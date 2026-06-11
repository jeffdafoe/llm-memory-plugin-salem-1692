package sim

import (
	"math/rand"
	"time"
)

// lodging_narration.go — pooled brown-panel narration for the PC lodging
// day-cycle relocations (checkout eviction and, from ZBBS-HOME-312 Part 2,
// natural morning descent). Replaces the single frozen EvictedNarration const
// with small phrase pools so the same relocation does not read identically
// every time. The LLM-on-exhaustion expansion is a separate future task
// (salem-narration-pool-llm-expansion); this is only the static-pool substrate
// that task will later top up.

// Lodging-relocation reasons. Carried on PCRelocatedToCommon.Reason and reused
// verbatim as the client room_event `kind`.
const (
	LodgingReasonCheckout = "lodging_checkout"
	LodgingReasonMorning  = "lodging_morning_descent"
)

// lodgingNarrationPools maps a relocation reason to its phrase pool.
var lodgingNarrationPools = map[string][]string{
	LodgingReasonCheckout: {
		"Your stay has ended — you head down to the common area.",
		"Checkout has come; you gather your things and step down to the common room.",
		"Your lodging is up for the day, and you make your way down to the common area.",
	},
	LodgingReasonMorning: {
		"Morning has come, and you stir from bed and make your way down to the common room.",
		"You wake rested, and head down to the common area to start the day.",
		"The day is begun — you rise and stroll down to the common room.",
	},
}

// lodgingNarrationRand is seeded once and read ONLY from the world goroutine
// (the eviction sweep command and, in Part 2, the morning-descent subscriber
// both run there), so it needs no synchronization. Narration choice is cosmetic
// and never persisted, so a wall-clock seed is fine.
var lodgingNarrationRand = rand.New(rand.NewSource(time.Now().UnixNano()))

// pickLodgingNarration returns a random phrase from the pool for reason, or ""
// when reason has no pool — callers treat "" as "emit no narration". The pool
// comes from the world's narration registry (ZBBS-WORK-399: the seed lines
// above plus any LLM-expanded extras), so the draw is counted toward the
// pool's expansion threshold. World method because of that registry access;
// still world-goroutine-only, same as before.
func (w *World) pickLodgingNarration(reason string) string {
	pool := w.narrationDraw(reason)
	if len(pool) == 0 {
		return ""
	}
	return pool[lodgingNarrationRand.Intn(len(pool))]
}

// LodgingNarrationPool returns a copy of the SEED phrase pool for reason (nil
// for an unknown reason). Exported so tests can assert a picked line is a pool
// member without reaching the unexported map. Runtime draws may also produce
// LLM-expanded lines beyond the seed (World.NarrationPools) — tests built on
// a fresh world see seed-only pools, so membership assertions remain valid.
func LodgingNarrationPool(reason string) []string {
	pool := lodgingNarrationPools[reason]
	if pool == nil {
		return nil
	}
	return append([]string(nil), pool...)
}
