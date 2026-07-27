package perception

import (
	"sort"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// LLM-542 — discharge-at-render.
//
// A reactor tick consumes its warrant batch at EMIT, but builds perception
// LATER, off a snapshot taken after that emit. So a tick's prompt routinely
// contains a stimulus whose warrant it never claimed: the other party spoke
// between the emit and the snapshot read, the speech reactor opened a FRESH
// warrant cycle on the same actor, and the in-flight tick then answered the
// line anyway. When it completes, that fresh cycle fires and the actor answers
// the same utterance a second time — reading its own first reply in "## Recent
// conversation here" as the counterparty repeating herself ("I told you —").
//
// Source-aware dedup cannot see this: all three of its paths key on warrants
// the emit CONSUMED, and this warrant was stamped after the emit.
//
// The missing signal is "which stimuli did this tick's prompt actually
// contain". CollectDischargedSourceKeys is that signal: the harness runs it
// over the rendered Payload and hands the keys to CompleteReactorTick, which
// prunes any matching warrant still pending in the open cycle and records the
// keys as recently-consumed.
//
// Speech is the only kind collected today. A paid beat has the same shape but
// renders from ledger state rather than an id-bearing view, so giving it a
// dischargeable identity is its own change; the []sim.WarrantSourceKey return
// is deliberately generic so that lands as a new branch here, not a re-plumb.

// CollectDischargedSourceKeys returns the deduped, deterministically-ordered
// warrant source keys whose stimulus this Payload rendered. Pure over the
// Payload — the harness calls it off the world goroutine, after Render, and
// carries the result on TickResult.
//
// Only the subject's own lines are skipped: an actor never warrants itself for
// its own speech, so a self line has no warrant to discharge. Lines with a zero
// SpeechID (recorded outside the emit path) are skipped too — there is no event
// behind them to key on.
//
// Returns nil when the tick rendered no dischargeable stimulus.
func CollectDischargedSourceKeys(p Payload) []sim.WarrantSourceKey {
	set := map[sim.WarrantSourceKey]struct{}{}
	for _, u := range p.RecentConversation {
		if u.IsSelf || u.SpeechID == 0 {
			continue
		}
		kind := sim.WarrantKindNPCSpoke
		if u.SpeakerIsPC {
			kind = sim.WarrantKindPCSpoke
		}
		set[sim.WarrantSourceKey{Kind: kind, Discriminator: uint64(u.SpeechID)}] = struct{}{}
	}
	if len(set) == 0 {
		return nil
	}
	out := make([]sim.WarrantSourceKey, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	// Deterministic order so a tick's result is stable across runs — the keys
	// feed a bounded, oldest-first-evicting map, and telemetry reads them.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Discriminator != out[j].Discriminator {
			return out[i].Discriminator < out[j].Discriminator
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}
