package sim

import (
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Admin/editor write commands for NPC metadata — the write half of the editor
// surface whose read half is AgentDTO (ZBBS-HOME-290). Each command mutates the
// live Actor on the world goroutine (the HTTP layer dispatches them through
// adminCommand → World.SendContext, so they're serialized with every other
// world mutation) and emits a client-visible WS event ONLY on an actual change —
// a same-value call is a no-op that emits nothing, mirroring the object editor
// commands in village_object.go. These are the v2 home for v1's
// /api/village/npcs/{id}/{field} PATCH surface (ZBBS-HOME-309).
//
// Scheduler entanglement is handled implicitly: the shift-duty (shift_duty.go)
// and social (social.go) tickers are level-triggered — they re-read the actor's
// window every tick — so a live schedule/social edit just changes what the next
// tick evaluates against. There is no stored in-flight shift to corrupt. The one
// edge case is the social edge-trigger stamp (SocialLastBoundaryAt), which
// SetActorSocial resets on a real change so the reconfigured window fires fresh.

// ErrActorNotFound is returned when the target actor id is absent, OR when it
// resolves to a PC — PCs are not editable through the NPC editor surface, so
// they're reported as "not found" rather than leaking a separate sentinel the
// editor has no use for.
var ErrActorNotFound = errors.New("actor not found")

// ErrInvalidAgentLink is returned by SetActorAgentLink when the trimmed agent
// identifier is over-length or carries a control character (→ 400).
var ErrInvalidAgentLink = errors.New("invalid agent link")

// ErrStructureNotFound is returned by SetActorHomeStructure / SetActorWorkStructure
// when a non-empty structure id doesn't resolve in World.Structures (→ 404). An
// empty id is valid — it clears the anchor.
var ErrStructureNotFound = errors.New("structure not found")

// ErrInvalidSchedule is returned by SetActorSchedule when the start/end pair is
// one-sided (exactly one nil) or a bound is outside [0,1439] (→ 400). Both-nil
// is valid (inherit dawn/dusk); start == end is valid (an empty shift window,
// per minuteInShiftWindow).
var ErrInvalidSchedule = errors.New("invalid schedule")

// ErrInvalidSocial is returned by SetActorSocial when the tag/start/end trio is
// not all-or-none, the tag is malformed, or a minute is outside [0,1439] (→ 400).
var ErrInvalidSocial = errors.New("invalid social schedule")

// ErrUnknownAttribute is returned by AddActorAttribute when the slug is empty or
// absent from World.AttributeDefinitions (the actor-assignable catalog) (→ 422).
// Removal does NOT validate against the catalog — a slug no longer in the
// catalog must still be removable.
var ErrUnknownAttribute = errors.New("unknown attribute slug")

// MaxActorDisplayNameLen caps an NPC display name's rune length. Matches the
// village-object cap — both are short client-rendered labels.
const MaxActorDisplayNameLen = 100

// MaxSocialTagLen caps a social-hour tag's rune length (the gathering-structure
// tag an NPC walks to). Matches the village-object tag cap.
const MaxSocialTagLen = 64

// Result types echo the post-mutation value back to the HTTP layer so the route
// can serialize a confirmation body off the world goroutine. Pointer fields are
// fresh copies (copyIntPtr), never aliases of the live Actor's pointers.
type ActorDisplayNameResult struct {
	ID          ActorID
	DisplayName string
}

type ActorAgentResult struct {
	ID       ActorID
	LLMAgent string
}

type ActorStructureResult struct {
	ID          ActorID
	StructureID string
}

type ActorScheduleResult struct {
	ID               ActorID
	ScheduleStartMin *int
	ScheduleEndMin   *int
}

type ActorSocialResult struct {
	ID             ActorID
	SocialTag      string
	SocialStartMin *int
	SocialEndMin   *int
}

type ActorAttributesResult struct {
	ID         ActorID
	Attributes []string
}

// editableNPC resolves id to an editable NPC actor. A missing actor or a PC
// resolves to ErrActorNotFound (PCs are not editable via this surface).
func editableNPC(w *World, id ActorID) (*Actor, error) {
	a, ok := w.Actors[id]
	if !ok || a.Kind == KindPC {
		return nil, ErrActorNotFound
	}
	return a, nil
}

// sortedAttributeSlugs returns the actor's attribute keys, sorted — the
// authoritative set the npc_attributes_changed frame carries (the sim-package
// analog of repo/pg's load-side helper of the same intent).
func sortedAttributeSlugs(a *Actor) []string {
	slugs := make([]string, 0, len(a.Attributes))
	for slug := range a.Attributes {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	return slugs
}

// validMinuteOfDay reports whether m is a valid minute-of-day [0,1439].
func validMinuteOfDay(m int) bool {
	return m >= 0 && m <= 1439
}

// SetActorDisplayName sets an NPC's display name. The name is trimmed and must be
// non-empty (an NPC has no catalog-name fallback — unlike a village object — so a
// blank name is rejected), within MaxActorDisplayNameLen, and free of control
// characters (else ErrInvalidDisplayName). Emits NPCDisplayNameChanged only on an
// actual change.
func SetActorDisplayName(id ActorID, name string) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			trimmed := strings.TrimSpace(name)
			if trimmed == "" || utf8.RuneCountInString(trimmed) > MaxActorDisplayNameLen || containsControlChar(trimmed) {
				return nil, ErrInvalidDisplayName
			}
			a, err := editableNPC(w, id)
			if err != nil {
				return nil, err
			}
			if a.DisplayName == trimmed {
				return ActorDisplayNameResult{ID: id, DisplayName: trimmed}, nil
			}
			a.DisplayName = trimmed
			w.emit(&NPCDisplayNameChanged{ActorID: id, DisplayName: trimmed, At: time.Now().UTC()})
			return ActorDisplayNameResult{ID: id, DisplayName: trimmed}, nil
		},
	}
}

// SetActorAgentLink sets (or clears) the llm_memory_agent backing an NPC. An
// empty agent unlinks. A non-empty agent is trimmed and validated for length /
// control characters (ErrInvalidAgentLink); existence of the VA is not checked
// here (the engine has no VA registry — the link is an opaque identifier the
// memory API resolves). Emits NPCAgentChanged only on an actual change.
func SetActorAgentLink(id ActorID, agent string) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			trimmed := strings.TrimSpace(agent)
			if utf8.RuneCountInString(trimmed) > MaxActorDisplayNameLen || containsControlChar(trimmed) {
				return nil, ErrInvalidAgentLink
			}
			a, err := editableNPC(w, id)
			if err != nil {
				return nil, err
			}
			if a.LLMAgent == trimmed {
				return ActorAgentResult{ID: id, LLMAgent: trimmed}, nil
			}
			a.LLMAgent = trimmed
			w.emit(&NPCAgentChanged{ActorID: id, LLMAgent: trimmed, At: time.Now().UTC()})
			return ActorAgentResult{ID: id, LLMAgent: trimmed}, nil
		},
	}
}

// SetActorHomeStructure sets (or clears) an NPC's home anchor. An empty id
// clears; a non-empty id must resolve in World.Structures (ErrStructureNotFound).
// Emits NPCHomeStructureChanged only on an actual change.
func SetActorHomeStructure(id ActorID, structureID string) Command {
	return setActorStructure(id, structureID, true)
}

// SetActorWorkStructure sets (or clears) an NPC's work anchor — the structure the
// shift-duty ticker sends the NPC to on shift. Same validation as the home
// anchor; emits NPCWorkStructureChanged only on an actual change.
func SetActorWorkStructure(id ActorID, structureID string) Command {
	return setActorStructure(id, structureID, false)
}

// setActorStructure is the shared home/work anchor mutator. home selects which
// field + event; the validation and no-op handling are identical.
func setActorStructure(id ActorID, structureID string, home bool) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			trimmed := strings.TrimSpace(structureID)
			if trimmed != "" {
				if _, ok := w.Structures[StructureID(trimmed)]; !ok {
					return nil, ErrStructureNotFound
				}
			}
			a, err := editableNPC(w, id)
			if err != nil {
				return nil, err
			}
			newVal := StructureID(trimmed)
			cur := a.HomeStructureID
			if !home {
				cur = a.WorkStructureID
			}
			if cur == newVal {
				return ActorStructureResult{ID: id, StructureID: trimmed}, nil
			}
			if home {
				a.HomeStructureID = newVal
				w.emit(&NPCHomeStructureChanged{ActorID: id, StructureID: trimmed, At: time.Now().UTC()})
			} else {
				a.WorkStructureID = newVal
				w.emit(&NPCWorkStructureChanged{ActorID: id, StructureID: trimmed, At: time.Now().UTC()})
			}
			return ActorStructureResult{ID: id, StructureID: trimmed}, nil
		},
	}
}

// SetActorSchedule sets an NPC's work-shift window. start and end are both nil
// (inherit the world dawn/dusk window) or both set in [0,1439]; a one-sided pair
// or an out-of-range bound is ErrInvalidSchedule. start == end is permitted (an
// empty shift window — the NPC is never on shift). Emits NPCScheduleChanged only
// on an actual change.
func SetActorSchedule(id ActorID, start, end *int) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			if (start == nil) != (end == nil) {
				return nil, ErrInvalidSchedule
			}
			if start != nil && (!validMinuteOfDay(*start) || !validMinuteOfDay(*end)) {
				return nil, ErrInvalidSchedule
			}
			a, err := editableNPC(w, id)
			if err != nil {
				return nil, err
			}
			if intPtrEqual(a.ScheduleStartMin, start) && intPtrEqual(a.ScheduleEndMin, end) {
				return ActorScheduleResult{ID: id, ScheduleStartMin: copyIntPtr(start), ScheduleEndMin: copyIntPtr(end)}, nil
			}
			a.ScheduleStartMin = copyIntPtr(start)
			a.ScheduleEndMin = copyIntPtr(end)
			w.emit(&NPCScheduleChanged{
				ActorID:          id,
				ScheduleStartMin: copyIntPtr(start),
				ScheduleEndMin:   copyIntPtr(end),
				At:               time.Now().UTC(),
			})
			return ActorScheduleResult{ID: id, ScheduleStartMin: copyIntPtr(start), ScheduleEndMin: copyIntPtr(end)}, nil
		},
	}
}

// SetActorSocial sets (or clears) an NPC's social-hour overlay — the daily walk
// to the nearest village_object carrying SocialTag and back. The tag/start/end
// trio is all-or-none: either a non-empty tag with both minutes set in [0,1439],
// or an empty tag with both minutes nil (clears). Anything else is
// ErrInvalidSocial. On an actual change the social edge-trigger stamp
// (SocialLastBoundaryAt) is reset so the reconfigured window fires fresh on its
// next boundary. Emits NPCSocialUpdated only on an actual change.
func SetActorSocial(id ActorID, tag string, start, end *int) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			trimmed := strings.TrimSpace(tag)
			hasTag := trimmed != ""
			hasMinutes := start != nil && end != nil
			if (start == nil) != (end == nil) || hasTag != hasMinutes {
				return nil, ErrInvalidSocial
			}
			if hasTag {
				if utf8.RuneCountInString(trimmed) > MaxSocialTagLen || containsControlChar(trimmed) {
					return nil, ErrInvalidSocial
				}
				if !validMinuteOfDay(*start) || !validMinuteOfDay(*end) {
					return nil, ErrInvalidSocial
				}
			}
			a, err := editableNPC(w, id)
			if err != nil {
				return nil, err
			}
			if a.SocialTag == trimmed && intPtrEqual(a.SocialStartMin, start) && intPtrEqual(a.SocialEndMin, end) {
				return ActorSocialResult{ID: id, SocialTag: trimmed, SocialStartMin: copyIntPtr(start), SocialEndMin: copyIntPtr(end)}, nil
			}
			a.SocialTag = trimmed
			a.SocialStartMin = copyIntPtr(start)
			a.SocialEndMin = copyIntPtr(end)
			a.SocialLastBoundaryAt = nil
			w.emit(&NPCSocialUpdated{
				ActorID:        id,
				SocialTag:      trimmed,
				SocialStartMin: copyIntPtr(start),
				SocialEndMin:   copyIntPtr(end),
				At:             time.Now().UTC(),
			})
			return ActorSocialResult{ID: id, SocialTag: trimmed, SocialStartMin: copyIntPtr(start), SocialEndMin: copyIntPtr(end)}, nil
		},
	}
}

// DeleteActorResult reports a deleted NPC's id back to the HTTP layer.
type DeleteActorResult struct {
	ID ActorID
}

// DeleteActor removes an NPC from the world (the v2 port of v1's
// DELETE /api/village/npcs/{id}). Mirrors the visitor-cleanup removal sequence
// in visitor.go: detach from the huddle index, drop the inside-structure flag
// (which repairs the outdoor / by-structure indexes), then delete from
// outdoorActors and Actors. Emits ActorDeparted (its documented
// non-visitor-departure path — VisitorContext nil) BEFORE removal so subscribers
// can still resolve the row; the httpapi hub translates it to the npc_deleted
// frame the client's remove_npc_by_id handler consumes. PCs are not deletable
// here (ErrActorNotFound), matching the editableNPC gate on the edit commands.
func DeleteActor(id ActorID) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			a, err := editableNPC(w, id)
			if err != nil {
				return nil, err
			}
			w.emit(&ActorDeparted{
				ActorID:               id,
				DisplayName:           a.DisplayName,
				LastInsideStructureID: a.InsideStructureID,
				LastPosition:          a.Pos,
				At:                    time.Now().UTC(),
			})
			if a.CurrentHuddleID != "" {
				if members, ok := w.actorsByHuddle[a.CurrentHuddleID]; ok {
					delete(members, id)
					if len(members) == 0 {
						delete(w.actorsByHuddle, a.CurrentHuddleID)
					}
				}
			}
			setActorInsideStructure(w, a, "")
			delete(w.outdoorActors, id)
			delete(w.Actors, id)
			return DeleteActorResult{ID: id}, nil
		},
	}
}

// AddActorAttribute adds a behavior-attribute slug to an NPC's actor_attribute
// set. The slug is trimmed and must resolve in World.AttributeDefinitions
// (ErrUnknownAttribute). Adding a slug already present is a no-op that emits
// nothing. On an actual add it emits NPCAttributesChanged carrying the full
// post-mutation sorted slug set.
func AddActorAttribute(id ActorID, slug string) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			trimmed := strings.TrimSpace(slug)
			if trimmed == "" {
				return nil, ErrUnknownAttribute
			}
			if _, ok := w.AttributeDefinitions[trimmed]; !ok {
				return nil, ErrUnknownAttribute
			}
			a, err := editableNPC(w, id)
			if err != nil {
				return nil, err
			}
			if a.Attributes == nil {
				a.Attributes = map[string][]byte{}
			}
			if _, present := a.Attributes[trimmed]; present {
				return ActorAttributesResult{ID: id, Attributes: sortedAttributeSlugs(a)}, nil
			}
			a.Attributes[trimmed] = []byte{}
			slugs := sortedAttributeSlugs(a)
			w.emit(&NPCAttributesChanged{ActorID: id, Attributes: slugs, At: time.Now().UTC()})
			return ActorAttributesResult{ID: id, Attributes: slugs}, nil
		},
	}
}

// RemoveActorAttribute removes a behavior-attribute slug from an NPC's set.
// Removing an absent slug is a no-op that emits nothing. Unlike Add, removal
// does not validate against the catalog — a slug retired from the catalog must
// still be removable. On an actual removal it emits NPCAttributesChanged carrying
// the full post-mutation sorted slug set (empty when the last attribute is gone).
func RemoveActorAttribute(id ActorID, slug string) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			trimmed := strings.TrimSpace(slug)
			if trimmed == "" {
				return nil, ErrUnknownAttribute
			}
			a, err := editableNPC(w, id)
			if err != nil {
				return nil, err
			}
			if _, present := a.Attributes[trimmed]; !present {
				return ActorAttributesResult{ID: id, Attributes: sortedAttributeSlugs(a)}, nil
			}
			delete(a.Attributes, trimmed)
			slugs := sortedAttributeSlugs(a)
			w.emit(&NPCAttributesChanged{ActorID: id, Attributes: slugs, At: time.Now().UTC()})
			return ActorAttributesResult{ID: id, Attributes: slugs}, nil
		},
	}
}

// ActorSpriteResult echoes the applied sprite id back to the HTTP layer.
type ActorSpriteResult struct {
	ID       ActorID
	SpriteID string
}

// SetActorSprite swaps the sprite an NPC renders with (the v2 port of v1's
// PATCH /api/village/npcs/{id}/sprite — "fix a placement-time mismatch"). The
// sprite_id is required and must resolve in the catalog (ErrUnknownSprite).
// Emits NPCSpriteChanged carrying the resolved *Sprite (Option B — the client's
// apply_npc_sprite_change rebuilds the AnimatedSprite2D off the inlined sprite,
// no follow-up fetch) only on an actual change.
func SetActorSprite(id ActorID, spriteID string) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			trimmed := strings.TrimSpace(spriteID)
			if trimmed == "" {
				return nil, ErrUnknownSprite
			}
			sprite := w.Sprites[SpriteID(trimmed)]
			if sprite == nil {
				return nil, ErrUnknownSprite
			}
			a, err := editableNPC(w, id)
			if err != nil {
				return nil, err
			}
			if a.SpriteID == SpriteID(trimmed) {
				return ActorSpriteResult{ID: id, SpriteID: trimmed}, nil
			}
			a.SpriteID = SpriteID(trimmed)
			w.emit(&NPCSpriteChanged{ActorID: id, Sprite: sprite, At: time.Now().UTC()})
			return ActorSpriteResult{ID: id, SpriteID: trimmed}, nil
		},
	}
}

// SetActorInventory rejects a row whose item_kind is absent from World.ItemKinds
// with the package-level ErrUnknownItemKind (item_commands.go) → 422.

// ErrInvalidInventory is returned by SetActorInventory for a malformed row set:
// an empty item_kind, a duplicate item_kind, or a negative quantity (→ 400).
var ErrInvalidInventory = errors.New("invalid inventory")

// ActorInventoryRow is one inventory entry on the editor wire (item_kind + qty).
type ActorInventoryRow struct {
	ItemKind string
	Quantity int
}

// ActorInventoryResult carries the actor's authoritative inventory back to the
// HTTP layer, sorted by the item catalog's SortOrder then name.
type ActorInventoryResult struct {
	ID   ActorID
	Rows []ActorInventoryRow
}

// sortInventoryRows orders rows by the item catalog's SortOrder then item_kind,
// matching v1's `ORDER BY k.sort_order, k.name`. An item missing from the
// catalog sorts as order 0 (it can't normally be present, but stay total).
func sortInventoryRows(w *World, rows []ActorInventoryRow) {
	sort.Slice(rows, func(i, j int) bool {
		si, sj := 0, 0
		if d := w.ItemKinds[ItemKind(rows[i].ItemKind)]; d != nil {
			si = d.SortOrder
		}
		if d := w.ItemKinds[ItemKind(rows[j].ItemKind)]; d != nil {
			sj = d.SortOrder
		}
		if si != sj {
			return si < sj
		}
		return rows[i].ItemKind < rows[j].ItemKind
	})
}

// GetActorInventory reads an NPC's inventory as sorted rows (the v2 port of v1's
// GET /api/village/npcs/{id}/inventory). Run through the world goroutine like
// the writes so the editor's read is consistent with any just-applied edit.
func GetActorInventory(id ActorID) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			a, err := editableNPC(w, id)
			if err != nil {
				return nil, err
			}
			rows := make([]ActorInventoryRow, 0, len(a.Inventory))
			for kind, qty := range a.Inventory {
				rows = append(rows, ActorInventoryRow{ItemKind: string(kind), Quantity: qty})
			}
			sortInventoryRows(w, rows)
			return ActorInventoryResult{ID: id, Rows: rows}, nil
		},
	}
}

// SetActorInventory replaces an NPC's entire inventory atomically (the v2 port
// of v1's PUT /api/village/npcs/{id}/inventory — whole-set, like object_refresh).
// Validation mirrors v1: reject empty/duplicate item_kind and negative quantity
// (ErrInvalidInventory), drop quantity==0 rows (clearing a slot == no row), and
// reject any item_kind absent from the catalog (ErrUnknownItemKind). No WS event
// — v1's inventory PUT didn't broadcast, and the editing client holds its own
// row state, so a live echo isn't needed.
func SetActorInventory(id ActorID, rows []ActorInventoryRow) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			a, err := editableNPC(w, id)
			if err != nil {
				return nil, err
			}
			seen := make(map[string]bool, len(rows))
			cleaned := make(map[ItemKind]int, len(rows))
			for _, row := range rows {
				if row.ItemKind == "" || seen[row.ItemKind] || row.Quantity < 0 {
					return nil, ErrInvalidInventory
				}
				seen[row.ItemKind] = true
				// Validate against the catalog BEFORE dropping zero-qty rows, so a
				// bogus item_kind is rejected even when its quantity is 0 — a
				// whole-set replace should reject every invalid row, not silently
				// skip the ones that merely clear a slot.
				if w.ItemKinds[ItemKind(row.ItemKind)] == nil {
					return nil, ErrUnknownItemKind
				}
				if row.Quantity == 0 {
					continue
				}
				cleaned[ItemKind(row.ItemKind)] = row.Quantity
			}
			a.Inventory = cleaned
			out := make([]ActorInventoryRow, 0, len(cleaned))
			for kind, qty := range cleaned {
				out = append(out, ActorInventoryRow{ItemKind: string(kind), Quantity: qty})
			}
			sortInventoryRows(w, out)
			return ActorInventoryResult{ID: id, Rows: out}, nil
		},
	}
}
