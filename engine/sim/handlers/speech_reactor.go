package handlers

import (
	"log"
	"time"
	"unicode/utf8"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// speech_reactor.go — Spoke event subscriber. Phase 3 PR A.
//
// Mints one NPCSpeechWarrantReason warrant per recipient on every Spoke
// event. The Spoke event carries the authoritative recipient set
// (computed once on the world goroutine by sim.Speak); the subscriber
// does not re-derive membership.
//
// Warrant policy choices (locked at the PR A design walkthrough):
//
//   - Always NPCSpeechWarrantReason. PR A's speak handler is NPC-only —
//     PCs commit speech through a different path (the existing
//     /api/village/pc/speak endpoint, not yet ported). When PC speech
//     ports, the cutover will route PC commits through a different
//     event/subscriber that mints PCSpeechWarrantReason instead.
//   - Force: false. v2's MinReactorTickGap default is 5s, 60x looser
//     than v1's 5-minute floor that motivated v1's force=true. Force is
//     reserved for the Admin warrant kind.
//   - SourceEventID = Spoke.EventID. The Spoke event's ID is the
//     authoritative speech identifier; it flows into both the warrant
//     payload (SpeechID, now uint64) and the meta (SourceEventID,
//     RootEventID), giving free dedup via the (Kind, SourceEventID)
//     source key.
//   - No self-warrant. RecipientIDs already excludes the speaker, so an
//     explicit speaker-skip would be redundant — but defensive in case
//     a future change to sim.Speak forgets the exclusion.
//
// Excerpt truncation: the warrant payload's Excerpt is rune-truncated to
// sim.MaxSalientFactTextLen (220) — every reactor tick this peer takes
// re-renders the excerpt into the perception prompt, so bounding the
// excerpt bounds the per-tick token cost. The full text remains on the
// Spoke event for any consumer that wants it.
func handleSpokeWarrants(w *sim.World, evt sim.Event) {
	spoke, ok := evt.(*sim.Spoke)
	if !ok {
		return
	}
	if len(spoke.RecipientIDs) == 0 {
		return
	}
	now := time.Now().UTC()
	excerpt := truncateRunes(spoke.Text, sim.MaxSalientFactTextLen)
	for _, peerID := range spoke.RecipientIDs {
		if peerID == spoke.SpeakerID {
			// Defensive — sim.Speak filters speaker out of RecipientIDs.
			continue
		}
		if _, ok := w.Actors[peerID]; !ok {
			continue
		}
		meta := sim.WarrantMeta{
			TriggerActorID: spoke.SpeakerID,
			Force:          false,
			Reason: sim.NPCSpeechWarrantReason{
				SpeechID: sim.SpeechID(spoke.EventID()),
				Speaker:  spoke.SpeakerID,
				Excerpt:  excerpt,
			},
			SourceEventID: spoke.EventID(),
			RootEventID:   spoke.RootEventID(),
			SourceActorID: spoke.SpeakerID,
			HuddleID:      spoke.HuddleID,
			OccurredAt:    spoke.At,
		}
		// StampWarrant returns an error only on caller bugs (nil Reason,
		// unknown actor). Both are pre-conditions we just satisfied:
		// Reason is non-nil literal, and we just looked up actor in
		// w.Actors. A failure here is an invariant breach; log + move
		// on rather than aborting the fan-out.
		if _, err := sim.StampWarrant(peerID, meta, now).Fn(w); err != nil {
			log.Printf(
				"handlers: speech-reactor StampWarrant for peer %q (speech %d): %v",
				peerID, spoke.EventID(), err,
			)
		}
	}
}

// RegisterSpeechHandlers wires the Spoke event subscriber into the
// world. Separate from RegisterTickHandlers / cascade.RegisterEncounter
// for the same opt-in-piecewise reason: a build that wants encounters
// but not the speak reactor (or vice versa) can compose. Must run on
// the world goroutine — call before World.Run or from inside a
// Command.Fn.
//
// Idempotency: registering twice would invoke the subscriber twice per
// Spoke event, but tryStampWarrant's source-aware dedup catches the
// duplicate ((WarrantKindNPCSpoke, EventID) collides with itself) and
// drops the second stamp. The general dedup mechanics are tested at the
// substrate level in reactor_pr3a_test.go; this subscriber inherits that
// guarantee by minting with nonzero SourceEventID.
func RegisterSpeechHandlers(w *sim.World) {
	if w == nil {
		panic("handlers: RegisterSpeechHandlers requires a non-nil world")
	}
	w.Subscribe(sim.SubscriberFunc(handleSpokeWarrants))
}

// truncateRunes returns s truncated to at most max runes, dropping any
// runes past the cap. Rune-safe: a multi-byte UTF-8 sequence is either
// fully present or fully absent in the result. max <= 0 returns "".
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max])
}
