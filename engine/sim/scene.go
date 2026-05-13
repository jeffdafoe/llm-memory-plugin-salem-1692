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

// CloneScene returns a deep copy suitable for publication via Snapshot or
// for the mem-repo serialization boundary.
func CloneScene(s *Scene) *Scene {
	if s == nil {
		return nil
	}
	cp := *s
	if s.Huddles != nil {
		cp.Huddles = make(map[HuddleID]struct{}, len(s.Huddles))
		for k := range s.Huddles {
			cp.Huddles[k] = struct{}{}
		}
	}
	return &cp
}
