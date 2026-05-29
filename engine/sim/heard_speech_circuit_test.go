package sim

import (
	"testing"
	"time"
)

// heard_speech_circuit_test.go — ZBBS-HOME-331 unit coverage of the
// per-(listener, speaker) heard-speech circuit-breaker primitives on *Actor.

func TestHeardSpeechCircuit_SuppressesAfterThreshold(t *testing.T) {
	a := &Actor{ID: "listener"}
	const speaker ActorID = "speaker"
	base := time.Date(2026, 5, 29, 16, 0, 0, 0, time.UTC)

	// The first heardSpeechMissThreshold utterances are admitted.
	for i := 0; i < heardSpeechMissThreshold; i++ {
		now := base.Add(time.Duration(i) * time.Second)
		if a.NoteHeardSpeech(speaker, now) {
			t.Fatalf("suppressed before threshold reached (utterance %d)", i+1)
		}
	}
	// The next one is suppressed.
	at := base.Add(time.Duration(heardSpeechMissThreshold) * time.Second)
	if !a.NoteHeardSpeech(speaker, at) {
		t.Fatalf("not suppressed after %d admitted utterances", heardSpeechMissThreshold)
	}
}

// Regression for the review finding: a CONTINUOUS loop (utterances spaced under
// the recovery window) must stay suppressed indefinitely — lastAt is refreshed
// on every heard utterance, so the recovery decay never fires mid-loop. The
// earlier draft (which refreshed lastAt only on admitted warrants) auto-
// recovered every window and merely throttled the loop; this guards against it.
func TestHeardSpeechCircuit_ContinuousLoopStaysSuppressed(t *testing.T) {
	a := &Actor{ID: "listener"}
	const speaker ActorID = "speaker"
	base := time.Date(2026, 5, 29, 16, 0, 0, 0, time.UTC)

	// Climb to suppression.
	for i := 0; i <= heardSpeechMissThreshold; i++ {
		a.NoteHeardSpeech(speaker, base.Add(time.Duration(i)*time.Second))
	}

	// Keep talking every 30s (well under the 3-min window) for far longer than
	// the recovery window. It must remain suppressed the whole time.
	gap := 30 * time.Second
	now := base.Add(time.Duration(heardSpeechMissThreshold) * time.Second)
	for elapsed := time.Duration(0); elapsed < 2*heardSpeechRecoveryWindow; elapsed += gap {
		now = now.Add(gap)
		if !a.NoteHeardSpeech(speaker, now) {
			t.Fatalf("circuit recovered mid continuous loop at +%s (should stay suppressed)", elapsed+gap)
		}
	}
}

func TestHeardSpeechCircuit_RecoveryWindowAdmitsAfterSilence(t *testing.T) {
	a := &Actor{ID: "listener"}
	const speaker ActorID = "speaker"
	base := time.Date(2026, 5, 29, 16, 0, 0, 0, time.UTC)
	for i := 0; i <= heardSpeechMissThreshold; i++ {
		a.NoteHeardSpeech(speaker, base.Add(time.Duration(i)*time.Second))
	}
	lastHeard := base.Add(time.Duration(heardSpeechMissThreshold) * time.Second)

	// After a quiet gap >= the recovery window, the next utterance is admitted.
	afterLull := lastHeard.Add(heardSpeechRecoveryWindow)
	if a.NoteHeardSpeech(speaker, afterLull) {
		t.Fatalf("still suppressed after a full recovery window of silence")
	}
}

func TestHeardSpeechCircuit_ResetAgainstReopens(t *testing.T) {
	a := &Actor{ID: "listener"}
	const speaker ActorID = "speaker"
	base := time.Date(2026, 5, 29, 16, 0, 0, 0, time.UTC)
	for i := 0; i <= heardSpeechMissThreshold; i++ {
		a.NoteHeardSpeech(speaker, base.Add(time.Duration(i)*time.Second))
	}
	// Confirm suppressed, then reset against this speaker.
	if !a.NoteHeardSpeech(speaker, base.Add(time.Duration(heardSpeechMissThreshold+1)*time.Second)) {
		t.Fatalf("expected suppressed before reset")
	}
	a.resetHeardSpeechMissesAgainst([]ActorID{speaker})
	if a.NoteHeardSpeech(speaker, base.Add(time.Duration(heardSpeechMissThreshold+2)*time.Second)) {
		t.Fatalf("suppressed immediately after reset (expected reopen)")
	}
}

func TestHeardSpeechCircuit_PerPairIsolation(t *testing.T) {
	a := &Actor{ID: "listener"}
	const s1, s2 ActorID = "speaker1", "speaker2"
	base := time.Date(2026, 5, 29, 16, 0, 0, 0, time.UTC)
	for i := 0; i <= heardSpeechMissThreshold; i++ {
		a.NoteHeardSpeech(s1, base.Add(time.Duration(i)*time.Second))
	}
	now := base.Add(time.Duration(heardSpeechMissThreshold+1) * time.Second)
	if !a.NoteHeardSpeech(s1, now) {
		t.Fatalf("s1 circuit should be suppressed")
	}
	if a.NoteHeardSpeech(s2, now) {
		t.Fatalf("s2 circuit should be admitted (per-pair isolation)")
	}
}

// Reset against one speaker must not reopen a different suppressed pair, and an
// empty reset (solo / no-one utterance) must reopen nothing.
func TestHeardSpeechCircuit_ResetIsPerRecipient(t *testing.T) {
	a := &Actor{ID: "listener"}
	const s1, s2 ActorID = "speaker1", "speaker2"
	base := time.Date(2026, 5, 29, 16, 0, 0, 0, time.UTC)
	for i := 0; i <= heardSpeechMissThreshold; i++ {
		a.NoteHeardSpeech(s1, base.Add(time.Duration(i)*time.Second))
		a.NoteHeardSpeech(s2, base.Add(time.Duration(i)*time.Second))
	}
	a.resetHeardSpeechMissesAgainst([]ActorID{s1})
	now := base.Add(time.Duration(heardSpeechMissThreshold+1) * time.Second)
	if a.NoteHeardSpeech(s1, now) {
		t.Fatalf("s1 should be admitted after a targeted reset")
	}
	if !a.NoteHeardSpeech(s2, now) {
		t.Fatalf("s2 should remain suppressed (reset was per-recipient)")
	}
	// Empty reset clears nothing.
	a.resetHeardSpeechMissesAgainst(nil)
	if !a.NoteHeardSpeech(s2, now.Add(time.Second)) {
		t.Fatalf("empty reset must not reopen s2")
	}
}
