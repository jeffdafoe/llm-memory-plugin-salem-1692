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
// event — EXCEPT recipients who are mid-walk (MoveIntent != nil), which are
// skipped (ZBBS-HOME-330; see the motion gate in the loop). The Spoke event
// carries the authoritative recipient set (computed once on the world
// goroutine by sim.Speak); the subscriber does not re-derive membership.
//
// Warrant policy choices (locked at the PR A design walkthrough):
//
//   - Reason kind follows the speaker (ZBBS-HOME-377). An NPC speaker
//     mints NPCSpeechWarrantReason; a PC speaker (the player, now routed
//     through sim.Speak via the colocated huddle since ZBBS-HOME-358)
//     mints PCSpeechWarrantReason. The split matters downstream:
//     actorCanReactNow lets a PC-speech warrant interrupt a listener's
//     break (a player addressing you in person outranks your nap) while
//     NPC-speech stays gated, and a PC speaker bypasses the heard-speech
//     circuit breaker (the player is never damped as a chatter loop). The
//     mid-walk gate still applies to all speakers (see the loop).
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
	// ZBBS-HOME-377: is the speaker a PC (the player)? A player's words are a
	// deliberate, in-person address — they must reach recipients even when a
	// recipient is on a break (PCSpeechWarrantReason interrupts a break in
	// actorCanReactNow) and must not be damped by the NPC<->NPC loop gates (the
	// mid-walk skip and the heard-speech circuit breaker exist to stop NPC
	// chatter ping-pong, not to silence the player). NPC speech keeps both gates
	// and stamps the parallel NPCSpeechWarrantReason.
	speakerIsPC := false
	if sp, ok := w.Actors[spoke.SpeakerID]; ok && sp.Kind == sim.KindPC {
		speakerIsPC = true
	}
	for _, peerID := range spoke.RecipientIDs {
		if peerID == spoke.SpeakerID {
			// Defensive — sim.Speak filters speaker out of RecipientIDs.
			continue
		}
		peer, ok := w.Actors[peerID]
		if !ok {
			continue
		}
		// ZBBS-HOME-330: don't warrant a listener who is mid-walk. A walking
		// actor can't speak (the speak handler rejects with "you are walking")
		// or re-move, so a heard-speech warrant only produces a wasted tick
		// that command-fails — this is the Josiah<->Elizabeth ping-pong from
		// the play-test. Drop, don't defer: an actor walking away from an
		// exchange isn't engaging with it, and once it stops the next utterance
		// warrants it normally. Stationary listeners are unaffected, so
		// discussion at a stall or in the tavern flows at full speed — the
		// motion gate is deliberately the only thing this suppresses. Applies to
		// PC speech too (ZBBS-HOME-377): a player can't reach an NPC mid-stride
		// either, since the warranted tick would just fail the same way — the
		// listeners a player actually talks to are stationary.
		if peer.MoveIntent != nil {
			continue
		}
		// ZBBS-HOME-331: heard-speech loop terminator. Record this utterance
		// against the per-(speaker, listener) breaker and skip if the circuit is
		// open — the speaker has warranted this listener heardSpeechMissThreshold
		// times with no productive reply, and the pair hasn't gone quiet long
		// enough to recover. NoteHeardSpeech is called for EVERY heard utterance
		// (including the suppressed ones it reports) so the recovery clock
		// measures real silence, not the last admitted warrant. Closes the
		// stationary-listener half HOME-330 left open (it gated only mid-walk
		// listeners). Resets when the listener speaks into the huddle (sim.Speak)
		// or after the recovery window. See engine/sim/heard_speech_circuit.go.
		//
		// ZBBS-HOME-377: NPC speakers only. A PC's address must never be damped
		// as a chatter loop, and PC utterances must not count toward the per-pair
		// breaker — so a PC speaker skips it entirely (neither checked nor
		// recorded).
		if !speakerIsPC && peer.NoteHeardSpeech(spoke.SpeakerID, now) {
			continue
		}
		var reason sim.WarrantReason
		if speakerIsPC {
			reason = sim.PCSpeechWarrantReason{
				SpeechID: sim.SpeechID(spoke.EventID()),
				Speaker:  spoke.SpeakerID,
				Excerpt:  excerpt,
			}
		} else {
			reason = sim.NPCSpeechWarrantReason{
				SpeechID: sim.SpeechID(spoke.EventID()),
				Speaker:  spoke.SpeakerID,
				Excerpt:  excerpt,
			}
		}
		meta := sim.WarrantMeta{
			TriggerActorID: spoke.SpeakerID,
			Force:          false,
			Reason:         reason,
			SourceEventID:  spoke.EventID(),
			RootEventID:    spoke.RootEventID(),
			SourceActorID:  spoke.SpeakerID,
			HuddleID:       spoke.HuddleID,
			OccurredAt:     spoke.At,
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
