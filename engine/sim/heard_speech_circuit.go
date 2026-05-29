package sim

import "time"

// heard_speech_circuit.go — ZBBS-HOME-331, the heard-speech loop terminator
// (catalog #2 in shared/tasks/pending/salem-v2-reactor-liveness-findings-2026-05-29).
//
// The problem: handlers.handleSpokeWarrants mints one NPCSpeechWarrantReason
// warrant per huddle peer on EVERY utterance, deduped only per-speech-event.
// Two co-located NPCs who keep "conversing" without committing anything new
// re-warrant each other indefinitely — the John Ellis <-> Josiah Thorne
// ping-pong seen live: a chat-view flood of greetings, a command-failing tick
// burned on each. HOME-330 gated the mid-walk half of this; this closes the
// stationary-listener half it left open.
//
// Why a productivity gate, not a flat time cooldown: the livelock's
// inter-utterance cadence is the SAME as real dialogue (both paced by the
// ~15-60s LLM tick), so a flat per-pair time window can't separate them — any
// window short enough to allow turn-taking is too short to break the loop. The
// separating signal is PRODUCTIVITY: a real exchange has the listener commit a
// new speak in reply; the degenerate loop has the listener never productively
// respond (it stays silent, or every tick command-fails). So the breaker counts
// heard-speech warrants a speaker has minted on a listener SINCE THAT LISTENER
// LAST SPOKE TO IT. Once that count crosses heardSpeechMissThreshold, the
// listener stops being warranted by that speaker's speech — the loop
// self-terminates — and the count resets when the listener speaks into the
// huddle (sim.Speak calls resetHeardSpeechMissesAgainst).
//
// Recovery (avoids trading one deadlock for another): a circuit-broken listener
// who never speaks would otherwise go permanently deaf to that speaker. So the
// cell also carries lastAt — the time of the LAST HEARD UTTERANCE — and if the
// pair goes quiet for heardSpeechRecoveryWindow the circuit is treated as reset
// and the next utterance gets through. Crucially, lastAt is refreshed on EVERY
// heard utterance, including SUPPRESSED ones (NoteHeardSpeech is called before
// the suppress decision, not after a stamp), so the window measures real
// silence. A continuous ping-pong therefore never "recovers" into itself — its
// lastAt is always recent — while a genuinely new conversation after a lull is
// admitted. (An earlier draft refreshed lastAt only on admitted warrants, which
// let a continuous loop auto-recover every window and merely throttled it;
// caught in review.)
//
// State lives on the LISTENER (Actor.heardSpeechMisses), ephemeral like the
// other reactor dedup bookkeeping (wiped on LoadWorld; a post-restart re-greet
// is a UX wrinkle, not a correctness failure — same posture as the
// businessowner cooldown map). Per-pair (keyed by speaker) so muting a
// going-nowhere exchange with one peer never deafens the listener to a
// different peer.
//
// Scope / known limitations:
//   - Terminates the ASYMMETRIC loop (a non-responding or failing listener),
//     which the catalog identifies as the live Mode-1 spiral ("no awake
//     counterpart" + anti-spam guards rejecting the redundant calls). A purely
//     SYMMETRIC loop — two NPCs that each keep successfully emitting
//     fresh-looking filler forever — is a narrower residual needing
//     content-salience detection; deliberately out of scope (the deferred
//     tuning-sensitive half), tracked separately.
//   - The count is not double-subscriber-registration-safe: if
//     RegisterSpeechHandlers were wired twice the subscriber runs twice per
//     event and the count advances twice. Same posture as the warrant-dedup
//     idempotency note in speech_reactor.go — RegisterSpeechHandlers is called
//     once at engine boot; a second registration is a wiring bug, not a runtime
//     condition.

const (
	// heardSpeechMissThreshold is how many of a speaker's consecutive utterances
	// a listener is warranted for before further heard-speech warrants from that
	// speaker are dropped — i.e. how many unanswered utterances the listener is
	// given before the engine concludes the exchange is going nowhere. 3 gives a
	// normal back-and-forth ample room (the listener speaking into the huddle
	// resets the count) while capping a degenerate ping-pong quickly. Tuning
	// candidate: promote to a WorldSettings/`setting` row if live tuning is
	// needed (kept a const here to keep the fix migration-free).
	heardSpeechMissThreshold = 3

	// heardSpeechRecoveryWindow is the quiet gap (no heard utterance from the
	// speaker) after which a circuit-broken pair is treated as reset, so a
	// genuinely new conversation gets through. Chosen well above the livelock's
	// per-utterance cadence (MinReactorTickGap .. ~60s) so a continuous loop
	// never decays into itself, yet short enough that a returning interlocutor
	// isn't ignored for long. Tuning candidate.
	heardSpeechRecoveryWindow = 3 * time.Minute
)

// heardSpeechMiss is the per-(listener, speaker) circuit-breaker cell: count of
// the speaker's consecutive utterances since the listener last spoke to it
// (clamped at heardSpeechMissThreshold+1), and the timestamp of the most recent
// heard utterance (for recovery-window decay).
type heardSpeechMiss struct {
	count  int
	lastAt time.Time
}

// NoteHeardSpeech records that `speaker` was just heard by this listener and
// reports whether the resulting heard-speech warrant should be SUPPRESSED
// (circuit open). It MUST be called for EVERY heard utterance from a stationary
// huddle peer — including suppressed ones — so the recovery clock tracks real
// silence rather than the last admitted warrant.
//
// Per call: if the pair has been quiet past the recovery window the count is
// reset first; then the count is bumped (clamped at threshold+1 so it can't
// grow unbounded under a long loop); lastAt is stamped to now; and the method
// returns true once the count exceeds heardSpeechMissThreshold (i.e. after
// threshold utterances have been admitted). Lazily allocates the map. MUST run
// on the world goroutine.
func (a *Actor) NoteHeardSpeech(speaker ActorID, now time.Time) (suppress bool) {
	if a.heardSpeechMisses == nil {
		a.heardSpeechMisses = make(map[ActorID]heardSpeechMiss)
	}
	m := a.heardSpeechMisses[speaker]
	if now.Sub(m.lastAt) >= heardSpeechRecoveryWindow {
		m.count = 0
	}
	if m.count <= heardSpeechMissThreshold {
		m.count++
	}
	m.lastAt = now
	a.heardSpeechMisses[speaker] = m
	return m.count > heardSpeechMissThreshold
}

// resetHeardSpeechMissesAgainst clears this actor's heard-speech circuit state
// against each of the given speakers — called when the actor commits a speak
// INTO a huddle (sim.Speak), with the huddle's recipient set. Speaking to a peer
// is the productive signal that this actor is engaging it, so any "ignoring that
// speaker" suppression standing against it is moot. Per-recipient (not a blanket
// clear) so a solo / no-one utterance — empty peerIDs — doesn't wrongly reopen
// an unrelated suppressed pair. delete on a nil/absent key is a no-op.
// Unexported: same-package callers only; MUST run on the world goroutine.
func (a *Actor) resetHeardSpeechMissesAgainst(speakers []ActorID) {
	for _, s := range speakers {
		delete(a.heardSpeechMisses, s)
	}
}
