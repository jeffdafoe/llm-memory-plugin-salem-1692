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
// warrant source keys whose stimulus this Payload conveyed. Pure over the
// Payload — the harness calls it off the world goroutine, after Render, and
// carries the result on TickResult.
//
// Reads Payload.ConveyedSpeech, NOT Payload.RecentConversation: a line the
// heardNow de-dup dropped from the rendered section is still in the prompt
// under "## Since your last turn", and a warrant it stamped must be
// discharged all the same. build.go owns that distinction.
//
// droppedWarrants is Render's DroppedWarrants — the consumed warrants that did
// not fit under MaxWarrants / MaxSectionBytes. It settles the conditional half
// of conveyance: a line that reached the prompt ONLY through other warrants'
// renders did not reach it at all if EVERY one of those carriers was dropped,
// so it is still owed (code_review). One survivor is enough — the text was in
// the prompt. Build cannot know this; it runs before Render.
//
// Returns nil when the tick conveyed no dischargeable stimulus.
func CollectDischargedSourceKeys(p Payload, droppedWarrants []sim.WarrantMeta) []sim.WarrantSourceKey {
	var dropped map[sim.WarrantSourceKey]struct{}
	if len(droppedWarrants) > 0 {
		dropped = make(map[sim.WarrantSourceKey]struct{}, len(droppedWarrants))
		for _, m := range droppedWarrants {
			switch r := m.Reason.(type) {
			case sim.PCSpeechWarrantReason:
				dropped[sim.WarrantSourceKey{Kind: sim.WarrantKindPCSpoke, Discriminator: uint64(r.SpeechID)}] = struct{}{}
			case sim.NPCSpeechWarrantReason:
				dropped[sim.WarrantSourceKey{Kind: sim.WarrantKindNPCSpoke, Discriminator: uint64(r.SpeechID)}] = struct{}{}
			}
		}
	}
	set := map[sim.WarrantSourceKey]struct{}{}
	for _, ref := range p.ConveyedSpeech {
		if ref.SpeechID == 0 {
			continue
		}
		if len(ref.ViaWarrants) > 0 && !anyCarrierRendered(ref.ViaWarrants, dropped) {
			continue // every carrier of this text was dropped from the prompt
		}
		kind := sim.WarrantKindNPCSpoke
		if ref.SpeakerIsPC {
			kind = sim.WarrantKindPCSpoke
		}
		set[sim.WarrantSourceKey{Kind: kind, Discriminator: uint64(ref.SpeechID)}] = struct{}{}
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

// anyCarrierRendered reports whether at least one of a de-duped line's
// supporting warrants survived the prompt caps. One is enough: the carriers all
// hold the same text, so a single surviving render put that text in front of
// the model.
//
// Zero-discriminator entries are not carriers and are skipped — that value is
// WarrantSourceKey's "not event-sourced" sentinel, so it can neither be pruned
// nor meaningfully appear in the dropped set. currentHeardExcerpts already
// refuses to store one; this re-states it because ConveyedSpeech is a public
// payload field a hand-built caller can populate. A list with no usable carrier
// reads as unconditionally conveyed, matching the empty-list case (code_review).
func anyCarrierRendered(carriers []sim.WarrantSourceKey, dropped map[sim.WarrantSourceKey]struct{}) bool {
	usable := false
	for _, key := range carriers {
		if key.Discriminator == 0 {
			continue
		}
		usable = true
		if _, gone := dropped[key]; !gone {
			return true
		}
	}
	return !usable
}
