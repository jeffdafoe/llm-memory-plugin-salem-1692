package sim

import "time"

// SceneID identifies a conversational / observational grouping.
type SceneID string

// Scene is the parent grouping for one or more Huddles. Scenes are minted
// at cascade origin (a PC speech, an idle-backstop tick, an atmosphere
// refresh) and represent one "narrative beat" — the LLM thinks within one
// scene at a time.
type Scene struct {
	ID         SceneID
	OriginAt   time.Time
	OriginKind string                // "pc_speak", "chronicler_attend", "idle_backstop", ...
	Huddles    map[HuddleID]struct{} // a scene may have multiple huddles
}
