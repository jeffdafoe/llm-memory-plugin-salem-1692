package sim

import "time"

// Event is the marker interface for in-world events emitted from command
// handlers as state mutations land. Subscribers (registered via
// World.Subscribe) receive every event in emission order, synchronously
// inside the world goroutine.
//
// Concrete event types in this package describe what changed; subscribers
// type-switch on the concrete type to react. The marker method
// (isSimEvent) is unexported so external packages cannot accidentally
// satisfy the interface — events are a closed set defined here.
//
// Why event-driven side effects instead of inline calls in the command
// handler: the v1 huddle code mixed lifecycle (join/leave/conclude),
// acquaintance recording, audit emission, greet/farewell narration, and
// loiter-slot adoption into one 621-LOC file with five overlapping
// concerns. Each concern was hard-wired into joinOrCreateHuddle. Adding
// a new side effect (e.g. "warn the LLM about loop patterns") would have
// meant touching the lifecycle primitive. Here, lifecycle commands emit
// typed events; concerns subscribe independently.
//
// Every Event also carries causal identity (EventID + RootEventID) stamped
// by World.emit — see EventBase. The setEventBase mutation path is
// unexported (pointer receiver), which both keeps the Event set closed at
// the sim boundary AND makes every concrete event pointer-only: only
// *ConcreteEvent satisfies Event, never the value.
type Event interface {
	isSimEvent()

	// EventID is the event's unique per-run identifier.
	EventID() EventID
	// RootEventID is the EventID of the cascade's causal root.
	RootEventID() EventID
	// setEventBase stamps identity at emit time. Unexported — only
	// World.emit assigns IDs, and the pointer receiver keeps the Event
	// set closed (external packages can neither emit nor forge identity).
	setEventBase(id, root EventID)
}

// EventID uniquely identifies an emitted event within a single world run.
// Assigned by World.emit from a plain monotonic counter — the world
// goroutine is the only emitter, so no atomic is needed. EventID(0) is the
// reserved invalid/unset sentinel: the counter starts at 1, so a real
// emitted event never has ID 0. The monotonic order is also a free total
// emission order PR 3's prompt builder relies on.
//
// EventID is per-run only — it is NOT persisted across the checkpoint
// boundary (warrants and event identity stay ephemeral).
type EventID uint64

// EventBase carries the causal identity every Event is stamped with at
// emit time. It is embedded BY VALUE (not *EventBase) in every concrete
// event type. A pointer embed would create a nil-base hazard — a
// zero-value event pointer would satisfy Event with a nil base, and
// setEventBase would panic. Value embedding has no nil risk and still
// yields pointer-only events: setEventBase has a pointer receiver, so
// only *ConcreteEvent satisfies Event, never the bare value.
type EventBase struct {
	id     EventID
	rootID EventID
}

// EventID returns the event's unique per-run identifier, or 0 if the event
// has not been emitted yet.
func (b *EventBase) EventID() EventID { return b.id }

// RootEventID returns the EventID of the causal root of the cascade this
// event belongs to. A fresh-origin event is its own root; a consequent
// event (emitted by a subscriber, or by a worker tool-call command
// continuing the tick) inherits the triggering event's root.
func (b *EventBase) RootEventID() EventID { return b.rootID }

// setEventBase stamps identity onto the event. Unexported with a pointer
// receiver: it is the mutation path World.emit uses to assign IDs, and the
// pointer receiver is what keeps the Event set closed at the sim package
// boundary.
func (b *EventBase) setEventBase(id, root EventID) { b.id, b.rootID = id, root }

// EventSubscriber consumes Events emitted by command handlers. Handle
// runs inline in the world goroutine after the command's mutation lands,
// so subscribers may mutate world state freely (atomically with the
// emitting command). They MUST NOT block on I/O — any DB write-through
// goes via a buffered channel feeding a dedicated background goroutine.
//
// Subscribers must not call World.Send / SendContext (would deadlock the
// single world goroutine). To trigger a follow-up command, mutate state
// directly here, or schedule the follow-up via the reactor when that
// lands in a later phase.
type EventSubscriber interface {
	Handle(w *World, evt Event)
}

// SubscriberFunc adapts a plain function to the EventSubscriber interface
// for tests and small subscribers that don't need their own struct.
type SubscriberFunc func(w *World, evt Event)

// Handle satisfies EventSubscriber by invoking the underlying function.
func (f SubscriberFunc) Handle(w *World, evt Event) { f(w, evt) }

// SceneMinted fires when a fresh Scene is created at cascade origin.
// Bound carries the scene's spatial scope (structure / area / unbounded);
// OriginPosition is the scene's anchor tile. Subscribers can read Bound
// directly or use the helper OriginStructureID() for the legacy
// "structure-id-or-empty" pattern.
type SceneMinted struct {
	EventBase
	SceneID        SceneID
	OriginKind     string
	Bound          SceneBound
	OriginPosition Position
	At             time.Time
}

// OriginStructureID returns the structure ID this scene was minted at,
// or empty string for non-structure-bound scenes. Convenience accessor
// for subscribers that only care about the legacy structure-tied case.
func (e SceneMinted) OriginStructureID() StructureID {
	if e.Bound.Kind != SceneBoundStructure || e.Bound.StructureID == nil {
		return ""
	}
	return *e.Bound.StructureID
}

func (SceneMinted) isSimEvent() {}

// HuddleJoined fires when ActorID enters HuddleID. OtherMembers carries
// the IDs of actors who were already in the huddle at the moment of the
// join (does not include the joining actor). SceneID is non-empty when
// the join was associated with a specific scene's narrative beat.
//
// Subscribers see this once per join. Pairwise "introductions" are
// emitted separately as ActorMet events so subscribers (acquaintance
// reactor, future relationship reactor) don't have to derive pairs.
type HuddleJoined struct {
	EventBase
	ActorID      ActorID
	HuddleID     HuddleID
	SceneID      SceneID
	StructureID  StructureID
	OtherMembers []ActorID
	HuddleNew    bool // true if the huddle was created by this join
	At           time.Time
}

func (HuddleJoined) isSimEvent() {}

// HuddleLeft fires when ActorID is removed from HuddleID. RemainingMembers
// carries the IDs of actors still in the huddle after the departure. When
// the huddle becomes empty, a HuddleConcluded event is emitted in
// addition to (and after) HuddleLeft.
type HuddleLeft struct {
	EventBase
	ActorID          ActorID
	HuddleID         HuddleID
	StructureID      StructureID
	RemainingMembers []ActorID
	At               time.Time
}

func (HuddleLeft) isSimEvent() {}

// HuddleConcluded fires when a huddle reaches zero members (or is
// force-concluded by ConcludeHuddle). Always preceded by the HuddleLeft
// (or, for force-conclude, no HuddleLeft) for the last departing
// member.
type HuddleConcluded struct {
	EventBase
	HuddleID    HuddleID
	StructureID StructureID
	At          time.Time
}

func (HuddleConcluded) isSimEvent() {}

// ActorMet fires once per (joining, prior-member) pair when an actor
// joins a huddle — captures the pairwise introductions produced by
// huddle membership. The acquaintance reactor consumes these to update
// Actor.Acquaintances and to write through to npc_acquaintance.
//
// Symmetric: a join with two prior members produces two ActorMet events
// (one per pair). Subscribers handle both directions of the relationship
// inside their handler — the event itself is a single pair (A joined,
// B was already there).
type ActorMet struct {
	EventBase
	A, B     ActorID
	HuddleID HuddleID
	At       time.Time
}

func (ActorMet) isSimEvent() {}

// ReactorTickDue fires when the evaluator emits a warrant-driven tick
// opportunity for an actor. Phase 2 PR 2 lands the event; PR 3's tick
// handler subscribes — handler builds perception (off the world goroutine
// via Published snapshot), calls the LLM, then sends CompleteReactorTick
// back through the command channel.
//
// AttemptID is the generation that makes stale completions detectable —
// the handler echoes AttemptID in CompleteReactorTick; the completion
// command is a no-op when the actor's current AttemptID has moved on.
//
// Warrants is the snapshot of the actor's pending signals at emit time,
// in stamp order. The list is consumed (cleared on the actor) at emit
// time, so this is the only place the metadata travels — the consumer
// can't fetch it later from the actor.
//
// Subscribers MUST NOT call the LLM inline (would hold the world
// goroutine for seconds). Pattern: copy IDs + AttemptID + Warrants into a
// worker queue; the worker reads world.Published() for perception build.
type ReactorTickDue struct {
	EventBase
	ActorID        ActorID
	AttemptID      TickAttemptID
	Warrants       []WarrantMeta // snapshot at emit; consumed from actor
	WarrantedSince time.Time     // when the warrant cycle began
	DueAt          time.Time     // when the warrant became due (= WarrantedSince + jitter)
	EmittedAt      time.Time
}

func (ReactorTickDue) isSimEvent() {}

// ActorDeparted fires when an actor is removed from World.Actors —
// today only emitted by the visitor cleanup path (engine/sim/visitor.go
// CleanupExpiredVisitor) past the visitor's ExpiresAt + grace window.
// Subscribers handle "left the village" semantics: action-log entry,
// analytics, downstream cache invalidation. Distinct from ActorMoveStopped
// (which is a movement-state transition for a still-present actor) and
// HuddleLeft (which only fires while the actor is alive).
//
// LastInsideStructureID and LastPosition capture the actor's last known
// location BEFORE removal so subscribers needn't read a freshly-deleted
// row off the world. Both are snapshots, not pointers into world state.
//
// VisitorContext is non-nil when the departing actor was a visitor;
// carries Archetype/Origin/Disposition so action-log / analytics can
// surface "Elias the peddler left the village" prose without joining
// back to a world record that no longer exists. Nil for non-visitor
// departures (no such path today; reserved for future).
type ActorDeparted struct {
	EventBase
	ActorID               ActorID
	DisplayName           string
	LastInsideStructureID StructureID
	LastPosition          Position
	VisitorContext        *VisitorState
	At                    time.Time
}

func (ActorDeparted) isSimEvent() {}

// RotationApplied fires when the daily rotation cascade completes a
// rotation pass. ObjectsAffected is the count of village_object flips
// scheduled this pass (after scope filtering); Gen is the WorldEventGen
// stamped on each flip (generation-checked at flip-fire time so
// rotations that complete during a subsequent transition don't overwrite
// fresh state).
//
// ExcludedTags carries the scope.ExcludeTags from the calling
// ApplyDailyRotation. The emitter defensively copies the caller's slice
// before emit (so a caller mutating after the call doesn't leak through),
// but the resulting slice is then SHARED across all subscribers — they
// all receive the same backing array. Subscribers MUST treat it as
// read-only; mutating would affect later subscribers in the dispatch
// order. True per-subscriber isolation would require clone-on-dispatch
// in World.emit, which is out of scope for this slice.
//
// Subscribers in this slice: none. The npc_behaviors cascade (Slice 2)
// will subscribe here to start washerwoman / town_crier routes — when
// the pass carved out their domain tag, the handler walks the NPC
// through each carved-out object to flip on arrival.
type RotationApplied struct {
	EventBase
	At              time.Time
	Gen             uint64
	ObjectsAffected int
	ExcludedTags    []string
}

func (RotationApplied) isSimEvent() {}

// PhaseApplied is emitted by ApplyPhaseTransition immediately after the
// per-object flips have been scheduled. Mirrors RotationApplied's shape
// for the day/night boundary side of the engine: subscribers (notably the
// lamplighter cascade slice) react by starting an NPC route over the
// objects the bulk pass carved out (via excludeTag=TagLamplighterTarget).
//
// At is the wall-clock instant the transition command landed (matches
// World.Environment.LastTransitionAt). From / To bracket the phase change;
// idempotent re-applies (admin force-phase to the current phase) still
// emit with From == To.
//
// Gen is the WorldEventGen value after the bump — subscribers stamping
// their own follow-up flips can use it as the guardGen on
// SetVillageObjectState so a rapid re-transition supersedes their work
// cleanly (same pattern as the bulk pass's PendingFlip.Gen).
//
// ObjectsAffected counts the bulk-pass flips scheduled; objects carved
// out for an NPC route are NOT counted here (the route's own per-stop
// flips are out-of-band from the bulk path).
//
// Subscribers in this slice (engine/sim/cascade/npc_route.go): the
// lamplighter cascade reads (At, To, Gen) to decide which target tag to
// walk to (day-active when To==PhaseDay, night-active when To==PhaseNight).
type PhaseApplied struct {
	EventBase
	At              time.Time
	From            Phase
	To              Phase
	Gen             uint64
	ObjectsAffected int
}

func (PhaseApplied) isSimEvent() {}

// VillageObjectStateChanged is emitted by SetVillageObjectState
// whenever an object's CurrentState actually changes (Applied=true
// path). Subscribers wanting to react to state transitions — the
// noticeboard authoring cascade reads this, schedules an LLM call
// to author content for the new state — register against this
// event.
//
// FromState / ToState carry the pre-mutation + post-mutation state
// names. At is the wall-clock instant the command landed. The
// post-mutation WorldEventGen is NOT included here — subscribers
// that need to gate against generation drift consult
// World.WorldEventGen.Load() themselves.
type VillageObjectStateChanged struct {
	EventBase
	ObjectID  VillageObjectID
	FromState string
	ToState   string
	At        time.Time
}

func (VillageObjectStateChanged) isSimEvent() {}

// NoticeboardContentChanged is emitted by SaveNoticeboardContent on the
// Applied=true path — a noticeboard's authored prose was (re)written for its
// current state. Text is the trimmed/capped content just stored; PostedAt is
// the instant it landed (== the content's persisted PostedAt, also surfaced on
// ObjectDTO.content_posted_at). At is the same command time (== PostedAt) —
// one mutation, one clock, so replay/tests/causal ordering can't diverge
// (the EventBase At convention; code_review #2). The skip paths
// (empty_after_trim / not_found / stale_state) emit nothing.
//
// Translates to the additive noticeboard_content_changed WS frame so an open
// content modal live-updates; until this, the client only saw new prose on the
// next full objects fetch (ZBBS-HOME-291 left this as a deferred fast-follow).
type NoticeboardContentChanged struct {
	EventBase
	ObjectID VillageObjectID
	Text     string
	PostedAt time.Time
	At       time.Time
}

func (NoticeboardContentChanged) isSimEvent() {}

// VillageObjectMoved is emitted by MoveVillageObject when an admin repositions
// a placed object. X / Y are the new absolute world-pixel anchor coordinates
// (the same space ObjectDTO emits). The client moves the rendered object on
// receipt (object_moved). Distinct from VillageObjectStateChanged, which is a
// sprite/state flip rather than a position change.
type VillageObjectMoved struct {
	EventBase
	ObjectID VillageObjectID
	X        float64
	Y        float64
	At       time.Time
}

func (VillageObjectMoved) isSimEvent() {}

// VillageObjectDeleted is emitted by DeleteVillageObject — once for the target
// object and once for each overlay object cascade-removed with it (attached_to
// chain). The client removes the rendered object on receipt (object_deleted).
// Children are emitted before the parent they were attached to.
type VillageObjectDeleted struct {
	EventBase
	ObjectID VillageObjectID
	At       time.Time
}

func (VillageObjectDeleted) isSimEvent() {}

// VillageObjectLoiterOffsetChanged is emitted by SetVillageObjectLoiterOffset
// when an admin edits where visiting actors stand at a placed object (the
// editor's draggable loiter pin). LoiterOffsetX/Y are the raw per-instance
// override (nil when cleared back to the asset/footprint default);
// EffectiveLoiterOffsetX/Y are the resolved offset (tile units relative to the
// object anchor) the editor renders the pin at — both carried so a live editor
// updates the pin without recomputing the fallback. Unlike owner / entry-policy
// (admin-only labels the editor re-reads on save), the loiter pin is a visible
// position other editing admins care about, so it broadcasts (ZBBS-HOME-289;
// matches v1's object_loiter_offset_changed).
type VillageObjectLoiterOffsetChanged struct {
	EventBase
	ObjectID               VillageObjectID
	LoiterOffsetX          *int
	LoiterOffsetY          *int
	EffectiveLoiterOffsetX int
	EffectiveLoiterOffsetY int
	At                     time.Time
}

func (VillageObjectLoiterOffsetChanged) isSimEvent() {}

// VillageObjectDisplayNameChanged is emitted by SetVillageObjectDisplayName when
// an object's DisplayName actually changes (the no-op short-circuit suppresses a
// same-name emit, mirroring setVillageObjectStateInline). DisplayName is the new
// post-mutation name; "" means the override was cleared (the client falls back
// to the catalog name). display_name IS in ObjectDTO, so this change is
// client-visible — the httpapi hub translates it to the object_display_name_changed
// frame (ZBBS-HOME-283; WS seam settled with work, mail 6aad4f26).
type VillageObjectDisplayNameChanged struct {
	EventBase
	ObjectID    VillageObjectID
	DisplayName string
	At          time.Time
}

func (VillageObjectDisplayNameChanged) isSimEvent() {}

// VillageObjectTagsUpdated is emitted by AddVillageObjectTag /
// RemoveVillageObjectTag when the tag set actually changes (an add of a tag
// already present, or a remove of an absent tag, is a no-op and emits nothing).
// Tags carries the AUTHORITATIVE full tag set AFTER the mutation — not a delta —
// so a subscriber and the client read surface converge on one source of truth
// regardless of which mutation produced it. tags IS in ObjectDTO; the httpapi
// hub translates this to the village_object_tags_updated frame, always as a JSON
// array (never null). (ZBBS-HOME-283; WS seam settled with work, mail 6aad4f26.)
type VillageObjectTagsUpdated struct {
	EventBase
	ObjectID VillageObjectID
	Tags     []string
	At       time.Time
}

func (VillageObjectTagsUpdated) isSimEvent() {}

// NPC editor write events (ZBBS-HOME-309). Each is emitted by the matching
// SetActor* / {Add,Remove}ActorAttribute command in actor_admin.go ONLY on an
// actual change, and the httpapi hub translates it to the npc_* WS frame the
// Godot editor already consumes (event_client.gd) so a second client editing the
// same NPC refreshes live. All carry the full post-mutation value (not a delta).

// NPCDisplayNameChanged — new DisplayName (always non-empty; an NPC has no
// catalog-name fallback). Frame: npc_display_name_changed.
type NPCDisplayNameChanged struct {
	EventBase
	ActorID     ActorID
	DisplayName string
	At          time.Time
}

func (NPCDisplayNameChanged) isSimEvent() {}

// NPCAgentChanged — the new llm_memory_agent link; "" means unlinked (the frame
// carries null). Frame: npc_agent_changed.
type NPCAgentChanged struct {
	EventBase
	ActorID  ActorID
	LLMAgent string
	At       time.Time
}

func (NPCAgentChanged) isSimEvent() {}

// NPCHomeStructureChanged / NPCWorkStructureChanged — the new anchor structure
// id; "" means cleared (the frame carries null). Frames:
// npc_home_structure_changed / npc_work_structure_changed.
type NPCHomeStructureChanged struct {
	EventBase
	ActorID     ActorID
	StructureID string
	At          time.Time
}

func (NPCHomeStructureChanged) isSimEvent() {}

type NPCWorkStructureChanged struct {
	EventBase
	ActorID     ActorID
	StructureID string
	At          time.Time
}

func (NPCWorkStructureChanged) isSimEvent() {}

// NPCScheduleChanged — the new shift window. Both pointers nil means "inherit
// dawn/dusk" (the frame carries null for each). Frame: npc_schedule_changed.
type NPCScheduleChanged struct {
	EventBase
	ActorID          ActorID
	ScheduleStartMin *int
	ScheduleEndMin   *int
	At               time.Time
}

func (NPCScheduleChanged) isSimEvent() {}

// NPCSocialUpdated — the new social-hour overlay. Empty tag + nil minutes means
// cleared (the frame carries null for each). Frame: npc_social_updated.
type NPCSocialUpdated struct {
	EventBase
	ActorID        ActorID
	SocialTag      string
	SocialStartMin *int
	SocialEndMin   *int
	At             time.Time
}

func (NPCSocialUpdated) isSimEvent() {}

// NPCAttributesChanged — the AUTHORITATIVE full sorted slug set after an add or
// remove (never a delta), so the client converges on one source of truth
// regardless of which mutation produced it. Frame: npc_attributes_changed
// (Attributes always marshals as an array, never null).
type NPCAttributesChanged struct {
	EventBase
	ActorID    ActorID
	Attributes []string
	At         time.Time
}

func (NPCAttributesChanged) isSimEvent() {}

// NPCSpriteChanged is emitted by SetActorSprite when an NPC's sprite actually
// changes. Carries the resolved *Sprite inline (same posture as NPCCreated) so
// the hub builds an npc_sprite_changed frame with the full render data the
// client's apply_npc_sprite_change rebuilds the AnimatedSprite2D from — no
// catalog round-trip. Frame: npc_sprite_changed.
type NPCSpriteChanged struct {
	EventBase
	ActorID ActorID
	Sprite  *Sprite
	At      time.Time
}

func (NPCSpriteChanged) isSimEvent() {}

// NPCCreated is emitted by CreateNPC when a new villager is materialized. It
// carries the full render identity inline — including the resolved *Sprite (a
// pointer into the immutable, lock-free sprite catalog, safe to read after the
// world goroutine moves on) — so the httpapi hub can build a complete
// npc_created frame (an AgentDTO) without a sprite-catalog round-trip, matching
// the per-NPC shape the initial /api/village/agents load delivers. Editor
// metadata (agent link, schedule, social, anchors, attributes) is all unset at
// creation, so the frame carries only the render fields. Frame: npc_created.
type NPCCreated struct {
	EventBase
	ActorID     ActorID
	DisplayName string
	Kind        ActorKind
	X           int
	Y           int
	Facing      string
	Sprite      *Sprite
	At          time.Time
}

func (NPCCreated) isSimEvent() {}
