package sim

import "time"

// events_pc_idle.go — LLM-466. The candle prompt's two transitions, translated
// by the httpapi hub into the pc_idle_prompt / pc_idle_prompt_cleared WS frames
// the Godot client renders as a click-to-dismiss overlay.
//
// Both are edge events: the sweep raises the prompt once per idle stretch, and
// the ack (or any in-world action that stamps activity while a prompt is up)
// clears it once. Broadcast unscoped like the sleep events — the client matches
// on actor_id to decide whether the candle is its own.

// PCIdlePromptShown is emitted when a connected PC crosses the idle horizon:
// the engine has stopped counting it as an audience and is asking whether
// anyone is still there. At is the instant the prompt was raised (UTC).
type PCIdlePromptShown struct {
	EventBase
	ActorID ActorID
	At      time.Time
}

func (PCIdlePromptShown) isSimEvent() {}

// PCIdlePromptCleared is emitted when a pending prompt is answered — the
// /pc/attend ack, or any deliberate action that stamped activity while the
// prompt was up (a player who returns and simply walks somewhere has proven
// presence just as well as one who clicks the candle). At is the instant it
// cleared (UTC).
type PCIdlePromptCleared struct {
	EventBase
	ActorID ActorID
	At      time.Time
}

func (PCIdlePromptCleared) isSimEvent() {}
