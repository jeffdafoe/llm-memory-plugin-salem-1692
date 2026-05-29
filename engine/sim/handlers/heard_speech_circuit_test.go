package handlers_test

import (
	"testing"
	"time"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// heard_speech_circuit_test.go — ZBBS-HOME-331 integration coverage: the
// heard-speech loop terminator wired through handleSpokeWarrants, driven via
// real sim.Speak commands. Uses the shared speech-reactor harness
// (buildSpeechReactorWorld / peekWarrants / speakActor in speech_reactor_test.go).
//
// circuitThreshold mirrors sim.heardSpeechMissThreshold, restated as a literal
// because that const is unexported and this is an external _test package. Keep
// in sync with engine/sim/heard_speech_circuit.go.
const circuitThreshold = 3

// A stationary listener who never speaks back stops being warranted once the
// speaker crosses the miss threshold — the loop self-terminates instead of
// burning a command-failing tick on every utterance.
func TestSpeechReactor_LoopTerminatesAtThreshold(t *testing.T) {
	w, stop := buildSpeechReactorWorld(t,
		speakActor{id: "speaker", displayName: "Speaker", kind: sim.KindNPCShared, huddleID: "h1"},
		speakActor{id: "listener", displayName: "Listener", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	emit := func() {
		if _, err := w.Send(sim.Speak("speaker", "Good morrow to you.", time.Now().UTC())); err != nil {
			t.Fatalf("Speak: %v", err)
		}
	}

	for i := 0; i < circuitThreshold; i++ {
		emit()
	}
	if got := len(peekWarrants(t, w, "listener")); got != circuitThreshold {
		t.Fatalf("listener warrants after %d utterances = %d, want %d (one per utterance up to threshold)",
			circuitThreshold, got, circuitThreshold)
	}

	// Over threshold: the circuit is open, so no further warrant is minted.
	emit()
	if got := len(peekWarrants(t, w, "listener")); got != circuitThreshold {
		t.Errorf("listener warrants after over-threshold utterance = %d, want %d (loop should have terminated)",
			got, circuitThreshold)
	}
}

// When the listener speaks, its circuit against the speaker resets, so the
// speaker's next utterance warrants it again — a genuine exchange flows; only a
// one-sided ping-pong is cut.
func TestSpeechReactor_ListenerSpeakReopensCircuit(t *testing.T) {
	w, stop := buildSpeechReactorWorld(t,
		speakActor{id: "speaker", displayName: "Speaker", kind: sim.KindNPCShared, huddleID: "h1"},
		speakActor{id: "listener", displayName: "Listener", kind: sim.KindNPCShared, huddleID: "h1"},
	)
	defer stop()

	for i := 0; i < circuitThreshold; i++ {
		if _, err := w.Send(sim.Speak("speaker", "Good morrow to you.", time.Now().UTC())); err != nil {
			t.Fatalf("Speak: %v", err)
		}
	}
	// Circuit now open: an extra utterance does not warrant the listener.
	if _, err := w.Send(sim.Speak("speaker", "Are you well?", time.Now().UTC())); err != nil {
		t.Fatalf("Speak: %v", err)
	}
	gated := len(peekWarrants(t, w, "listener"))
	if gated != circuitThreshold {
		t.Fatalf("listener warrants while circuit open = %d, want %d", gated, circuitThreshold)
	}

	// Listener responds — a productive speak resets its circuit.
	if _, err := w.Send(sim.Speak("listener", "well met, and to you.", time.Now().UTC())); err != nil {
		t.Fatalf("listener Speak: %v", err)
	}
	afterListenerSpeak := len(peekWarrants(t, w, "listener"))

	// Speaker talks again: the reset circuit lets the warrant through.
	if _, err := w.Send(sim.Speak("speaker", "splendid news.", time.Now().UTC())); err != nil {
		t.Fatalf("Speak: %v", err)
	}
	if got := len(peekWarrants(t, w, "listener")); got != afterListenerSpeak+1 {
		t.Errorf("listener warrants after reset+utterance = %d, want %d (circuit should have reopened on listener speak)",
			got, afterListenerSpeak+1)
	}
}
