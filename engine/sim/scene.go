package sim

import "time"

// SceneID identifies a conversational / observational grouping.
type SceneID string

// Scene is the parent grouping for one or more Huddles. Scenes are minted
// at cascade origin (a PC speech, an idle-backstop tick, an atmosphere
// refresh) and represent one "narrative beat" — the LLM thinks within one
// scene at a time.
//
// A scene captures the snapshot of every participant at its origin (in
// ParticipantStateAtOrigin) so that perception built within the scene
// can surface "what changed for me since this scene started" instead of
// re-deciding from cold state every tick. This is load-bearing for the
// circles / loop-detection seam: a scene that's run several ticks without
// meaningful state change is the signal that the LLM is stuck.
//
// Scene → Huddles is many-to-many over time: one cascade scene typically
// observes one huddle, but an atmosphere refresh may observe all active
// huddles at once. Huddle.SceneID has therefore been removed; Scene.Huddles
// is the canonical mapping.
type Scene struct {
	ID                SceneID
	OriginAt          time.Time
	OriginKind        string      // "pc_speak", "chronicler_attend", "idle_backstop", "atmosphere_refresh", ...
	OriginStructureID StructureID // empty for non-structure-tied cascades

	// Huddles observed by this scene. Populated at scene mint for huddles
	// already present at the origin structure, and extended as actors
	// join huddles within the scene's lifetime.
	Huddles map[HuddleID]struct{}

	// ParticipantStateAtOrigin is the snapshot of every participant at
	// scene mint, keyed by ActorID. Perception build within the scene
	// reads this to compute "what changed for me since this scene
	// started" — the diff seam that supports loop detection (no change
	// across several ticks → you're stuck) and "buy completed last
	// scene → it's in your inventory now" continuity claims.
	//
	// Snapshots are deep-cloned via CloneActorSnapshot at the
	// published-Snapshot and mem-repo boundaries.
	ParticipantStateAtOrigin map[ActorID]*ActorSnapshot
}

// CloneScene returns a deep copy suitable for publication via Snapshot or
// for the mem-repo serialization boundary. Both maps and every captured
// ActorSnapshot are cloned so a snapshot reader cannot reach back into
// world state via a Scene pointer.
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
	if s.ParticipantStateAtOrigin != nil {
		cp.ParticipantStateAtOrigin = make(map[ActorID]*ActorSnapshot, len(s.ParticipantStateAtOrigin))
		for k, v := range s.ParticipantStateAtOrigin {
			cp.ParticipantStateAtOrigin[k] = CloneActorSnapshot(v)
		}
	}
	return &cp
}
