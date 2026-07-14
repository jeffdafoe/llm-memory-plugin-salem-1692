package handlers

import (
	"log"
	"time"

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
//     NPC-speech stays gated. The mid-walk gate still applies to all
//     speakers (see the loop).
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
// Excerpt carries the FULL utterance (LLM-396). It used to be rune-truncated
// to sim.MaxSalientFactTextLen (220) to bound per-tick token cost, but that
// cut landed mid-word with no marker, and 40% of live utterances were long
// enough to hit it. A listener shown "...they're about finding home in small"
// reads a dangling, unfinished sentence and does the socially obvious thing —
// asks the speaker to finish it. The reply is truncated too, so every turn
// manufactures a fresh conversational obligation and the huddle never
// terminates (observed live: a 13-minute unbreakable politeness loop in the
// Inn). It also defeated the "you already spoke, wait for their reply" guard,
// which can never be the most pressing matter while a question dangles.
//
// Token cost stays bounded where it belongs — in the renderer, which caps the
// warrant payload at perception.RenderConfig.MaxBytesPerWarrant (600 bytes)
// and, unlike this path, MARKS the cut with an ellipsis. Every utterance the
// speak tool can produce is already bounded upstream at MaxSpeakTextChars
// (1000 runes), and no observed utterance exceeded 473 bytes, so in practice
// the full line now reaches the listener intact; a pathological one is elided
// visibly rather than silently.
func handleSpokeWarrants(w *sim.World, evt sim.Event) {
	spoke, ok := evt.(*sim.Spoke)
	if !ok {
		return
	}
	if len(spoke.RecipientIDs) == 0 {
		return
	}
	now := time.Now().UTC()
	excerpt := spoke.Text
	// ZBBS-HOME-377: is the speaker a PC (the player)? A player's words are a
	// deliberate, in-person address — they stamp PCSpeechWarrantReason so they
	// reach a recipient even when it is on a break (PCSpeechWarrantReason
	// interrupts a break in actorCanReactNow; a player addressing you in person
	// outranks your nap). The mid-walk skip below still applies to ALL speakers,
	// PC included — a walking listener can't act on the warrant either way. NPC
	// speech stamps the parallel NPCSpeechWarrantReason.
	//
	// Fail-closed to NPC if the speaker isn't a known PC. sim.Speak (the
	// /pc/speak path since ZBBS-HOME-358) is the only Spoke producer that can
	// carry a PC speaker; the other emitters — businessowner hospitality, town
	// crier (noticeboard), the npc_sleep retire line, and summon — all carry an
	// NPC/system speaker and so correctly classify here. A missing/unknown
	// SpeakerID therefore can only be a malformed NPC-system event, and treating
	// it as NPC speech (gated behind a break, never waking) is the safe default:
	// the only thing the fail-closed branch forgoes is break-interruption, which
	// must never fire on an unattributable utterance anyway.
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
