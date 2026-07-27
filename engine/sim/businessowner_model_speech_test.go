package sim

import (
	"testing"
	"time"
)

// businessowner_model_speech_test.go — LLM-535 boundary coverage for
// BusinessownerModelSpeechRecent. Internal (package sim) so the cases can be
// written against businessownerModelFarewellWindow itself rather than a literal
// that would silently drift if the window is ever retuned.

func modelSpeechWorld(t *testing.T, huddleID HuddleID) *World {
	t.Helper()
	return &World{Huddles: map[HuddleID]*Huddle{
		huddleID: {ID: huddleID, Members: map[ActorID]struct{}{"keeper": {}}},
	}}
}

func TestBusinessownerModelSpeechRecent_WindowBoundary(t *testing.T) {
	now := time.Now().UTC()

	cases := []struct {
		name string
		age  time.Duration
		want bool
	}{
		{"just spoken", 0, true},
		{"inside the window", businessownerModelFarewellWindow - time.Second, true},
		// The window is exclusive at its far edge, matching
		// businessownerEngineSpeechRecent's comparison.
		{"exactly at the window", businessownerModelFarewellWindow, false},
		{"past the window", businessownerModelFarewellWindow + time.Second, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := modelSpeechWorld(t, "h1")
			w.Huddles["h1"].AppendUtterance("keeper", "Keeper", "safe travels", now.Add(-tc.age), 0)
			if got := BusinessownerModelSpeechRecent(w, "h1", "keeper", now); got != tc.want {
				t.Errorf("age %v: got %v, want %v", tc.age, got, tc.want)
			}
		})
	}
}

// A line stamped in the future of `now` must read as no-recent-speech. Without
// the guard, now.Sub(last) is negative, compares as "more recent than 0 seconds
// ago", and pins the gate open until wall-clock catches up — silencing every
// farewell in that huddle for the duration.
func TestBusinessownerModelSpeechRecent_FutureUtteranceDoesNotSuppress(t *testing.T) {
	now := time.Now().UTC()
	w := modelSpeechWorld(t, "h1")
	w.Huddles["h1"].AppendUtterance("keeper", "Keeper", "safe travels", now.Add(time.Hour), 0)

	if BusinessownerModelSpeechRecent(w, "h1", "keeper", now) {
		t.Error("a future-stamped utterance suppressed the farewell; the gate must fail open")
	}
}

func TestBusinessownerModelSpeechRecent_MissingInputsFailOpen(t *testing.T) {
	now := time.Now().UTC()
	w := modelSpeechWorld(t, "h1")
	w.Huddles["h1"].AppendUtterance("keeper", "Keeper", "safe travels", now, 0)

	cases := []struct {
		name     string
		world    *World
		huddleID HuddleID
		actorID  ActorID
	}{
		{"nil world", nil, "h1", "keeper"},
		{"empty huddle id", w, "", "keeper"},
		{"unknown huddle", w, "gone", "keeper"},
		{"actor never spoke", w, "h1", "someone-else"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if BusinessownerModelSpeechRecent(tc.world, tc.huddleID, tc.actorID, now) {
				t.Error("got true, want false — every unknown must fail toward emitting the farewell")
			}
		})
	}
}

// An engine-authored line is not the keeper choosing to speak, so it must not
// satisfy the gate no matter how recent it is. The live shape is a handover
// seconds before the customer walks out.
func TestBusinessownerModelSpeechRecent_IgnoresEngineAuthored(t *testing.T) {
	now := time.Now().UTC()
	w := modelSpeechWorld(t, "h1")
	w.Huddles["h1"].AppendEngineUtterance("keeper", "Keeper", "There you are — enjoy.", now, 0)

	if BusinessownerModelSpeechRecent(w, "h1", "keeper", now) {
		t.Error("an engine hospitality line satisfied the gate; only model speech should")
	}
}
