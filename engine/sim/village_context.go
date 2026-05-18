package sim

import (
	"sort"
	"time"
)

// village_context.go — generalized village-state snapshot for engine-
// authored content prompts. Originally `AtmosphereContext` in
// atmosphere.go, scoped to one consumer (the atmosphere refresh
// cascade). Renamed + relocated when the noticeboard authoring slice
// added a second consumer.
//
// The snapshot is the v2 equivalent of v1's chronicler perception
// build: a curated, snapshot-time view of village state that engine-
// authored content prompts can ground themselves in. The chronicler-VA
// in v1 was a thin wrapper — the engine assembled the context and
// pushed it inline; v2 drops the VA wrapper and the engine calls
// salem-generic directly with the assembled context as the prompt.
//
// Consumers select what they need from the snapshot:
//
//   - Atmosphere cascade: Phase / Weather / Roster / ActivityDigest /
//     PriorAtmosphere. Ignores Visitors / BusinessCatalog (atmosphere
//     prose may incidentally reference them but doesn't drive its
//     prompt from them today; future enhancement).
//
//   - Noticeboard authoring cascade: Visitors / BusinessCatalog /
//     PriorAtmosphere. Deliberately excludes ActivityDigest (per-NPC
//     action counts are surveillance-shaped fodder the v1 chronicler
//     fabricated noticeboard prose from — "Ezekiel is tired at the
//     forge" — which is the anti-pattern that prompted v1's hardened
//     anti-surveillance instructions). Distress is NOT in the snapshot
//     at all for the same reason: it can't leak into a prompt if it
//     doesn't exist.
//
// Curation principle: the snapshot is the UNION of safe-for-prompt
// data. Each consumer selects narrower. Adding a new field here is a
// commit to making it safe for any future authoring consumer that
// pulls it.

// VisitorSummary is one visitor's snapshot-time view for the prompt.
// Mirrors VisitorState's archetype / origin / disposition but rendered
// with the actor's display name for prompt convenience. ExpiresAt is
// the wall-clock departure deadline; the prompt builder can reason
// about "leaving soon" vs "just arrived" if it cares.
type VisitorSummary struct {
	ID          ActorID
	DisplayName string
	Archetype   string
	Origin      string
	Disposition string
	ExpiresAt   time.Time
}

// BusinessOffering is one (keeper, structure, items) entry for the
// "wares and services offered" section of a noticeboard prompt or
// similar consumer. One entry per keeper — multi-keeper structures
// produce multiple entries.
//
// Items is the keeper's RestockPolicy.ProduceEntries joined with
// World.Recipes for the retail price. Empty Items means the keeper
// has a work structure but no produce policy — skipped at build time
// rather than rendered as a zero-items entry.
type BusinessOffering struct {
	OwnerID          ActorID
	OwnerDisplayName string
	StructureID      StructureID
	StructureLabel   string
	Items            []BusinessItem
}

// BusinessItem is one item this keeper offers. Price is the retail
// price from World.Recipes; zero if the recipe is absent or the
// recipe has no retail price configured.
type BusinessItem struct {
	Item  ItemKind
	Price int
}

// VillageContext is the snapshot the world goroutine builds for
// off-world prompts. All fields are owned by the caller — no pointers
// back into world state. Freshly allocated slices/maps; the caller
// may mutate without affecting world state.
//
// Atmosphere cascade is the historical first consumer; this type was
// renamed from `AtmosphereContext` when noticeboard authoring became
// the second consumer. Doc-stub kept for `AtmosphereContext` callers
// via a type alias below.
//
// Ordering across slices:
//
//   - Roster: outdoor bucket (StructureLabel == "") last, structure
//     groups in DisplayName-ascending order before it. Names within
//     each bucket sorted ascending.
//
//   - ActivityDigest: DisplayName-ascending across actors. Inner Counts
//     maps freshly allocated per actor.
//
//   - Visitors: DisplayName-ascending.
//
//   - BusinessCatalog: StructureLabel-ascending, then OwnerDisplayName
//     within a structure. Items within each entry: ItemKind-ascending.
type VillageContext struct {
	Now             time.Time
	Phase           Phase
	Weather         string
	PriorAtmosphere string
	Roster          []AtmosphereRosterEntry
	ActivityDigest  []ActivityDigestEntry

	// Visitors carries every Actor with VisitorState != nil at
	// snapshot time. Empty for villages with no current visitors.
	Visitors []VisitorSummary

	// BusinessCatalog carries every (keeper, structure, items)
	// triple for keepers with a work structure + non-empty
	// RestockPolicy ProduceEntries. Item.Price is the retail price
	// from World.Recipes; zero if the recipe is absent.
	BusinessCatalog []BusinessOffering
}

// AtmosphereContext is the legacy name for VillageContext. Retained
// as a type alias so existing callers (atmosphere cascade) compile
// unchanged; new code should prefer VillageContext directly.
type AtmosphereContext = VillageContext

// FetchVillageContext returns a Command that snapshots the world-
// level inputs an engine-authored content prompt may need. `at` is
// the wall-clock moment the build was driven; production passes
// time.Now(), tests pass a fixed value for determinism. The result
// is a fresh allocation; the caller may mutate without affecting
// world state.
//
// Never returns an error — even an empty world produces a valid
// (possibly-empty-slice) context.
//
// Renamed from FetchAtmosphereContext at the same time as the struct
// rename; FetchAtmosphereContext alias retained below for callers
// that haven't updated.
func FetchVillageContext(at time.Time) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			ctx := VillageContext{
				Now:             at,
				Phase:           w.Phase,
				Weather:         w.Environment.Weather,
				PriorAtmosphere: w.Environment.Atmosphere,
			}

			ctx.Roster = buildVillageContextRoster(w)
			ctx.ActivityDigest = buildVillageContextActivityDigest(w)
			ctx.Visitors = buildVillageContextVisitors(w)
			ctx.BusinessCatalog = buildVillageContextBusinessCatalog(w)
			return ctx, nil
		},
	}
}

// FetchAtmosphereContext is the legacy name for FetchVillageContext.
// Atmosphere cascade still calls it; new code should prefer
// FetchVillageContext directly.
func FetchAtmosphereContext(at time.Time) Command {
	return FetchVillageContext(at)
}

// buildVillageContextRoster groups NPCs by their inside structure's
// DisplayName, sorts buckets + names, then appends an outdoor bucket
// (StructureLabel == "") last. Mirrors v1 chronicler's NPC-by-location
// posture without joining village_object / asset (v2 Structure
// carries DisplayName directly).
func buildVillageContextRoster(w *World) []AtmosphereRosterEntry {
	byLoc := make(map[string][]string)
	var outdoor []string
	for _, a := range w.Actors {
		if a == nil || a.Kind == KindPC {
			continue
		}
		if a.InsideStructureID == "" {
			outdoor = append(outdoor, a.DisplayName)
			continue
		}
		s, ok := w.Structures[a.InsideStructureID]
		if !ok || s == nil || s.DisplayName == "" {
			// Indoor-but-unnamed-structure falls through to outdoor.
			outdoor = append(outdoor, a.DisplayName)
			continue
		}
		byLoc[s.DisplayName] = append(byLoc[s.DisplayName], a.DisplayName)
	}

	labels := make([]string, 0, len(byLoc))
	for label := range byLoc {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	roster := make([]AtmosphereRosterEntry, 0, len(byLoc)+1)
	for _, label := range labels {
		names := byLoc[label]
		sort.Strings(names)
		roster = append(roster, AtmosphereRosterEntry{
			StructureLabel: label,
			DisplayNames:   names,
		})
	}
	if len(outdoor) > 0 {
		sort.Strings(outdoor)
		roster = append(roster, AtmosphereRosterEntry{
			StructureLabel: "",
			DisplayNames:   outdoor,
		})
	}
	return roster
}

// buildVillageContextActivityDigest aggregates ActionLog entries
// after LastAtmosphereRefreshAt into per-actor action-type counts.
// NPCs only. First fire (zero LastAtmosphereRefreshAt) returns nil —
// no "since beginning of time" dump at startup.
func buildVillageContextActivityDigest(w *World) []ActivityDigestEntry {
	since := w.Environment.LastAtmosphereRefreshAt
	if since.IsZero() || len(w.ActionLog) == 0 {
		return nil
	}
	perActor := make(map[ActorID]map[ActionType]int)
	for _, e := range w.ActionLog {
		if !e.OccurredAt.After(since) {
			continue
		}
		a, ok := w.Actors[e.ActorID]
		if !ok || a == nil || a.Kind == KindPC {
			continue
		}
		if perActor[e.ActorID] == nil {
			perActor[e.ActorID] = make(map[ActionType]int)
		}
		perActor[e.ActorID][e.ActionType]++
	}
	if len(perActor) == 0 {
		return nil
	}
	ids := make([]ActorID, 0, len(perActor))
	for id := range perActor {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return w.Actors[ids[i]].DisplayName < w.Actors[ids[j]].DisplayName
	})
	digest := make([]ActivityDigestEntry, 0, len(ids))
	for _, id := range ids {
		digest = append(digest, ActivityDigestEntry{
			ActorID:     id,
			DisplayName: w.Actors[id].DisplayName,
			Counts:      perActor[id],
		})
	}
	return digest
}

// buildVillageContextVisitors collects every Actor with
// VisitorState != nil into a sorted list. DisplayName-ascending.
func buildVillageContextVisitors(w *World) []VisitorSummary {
	var visitors []VisitorSummary
	for id, a := range w.Actors {
		if a == nil || a.VisitorState == nil {
			continue
		}
		visitors = append(visitors, VisitorSummary{
			ID:          id,
			DisplayName: a.DisplayName,
			Archetype:   a.VisitorState.Archetype,
			Origin:      a.VisitorState.Origin,
			Disposition: a.VisitorState.Disposition,
			ExpiresAt:   a.VisitorState.ExpiresAt,
		})
	}
	sort.Slice(visitors, func(i, j int) bool {
		if visitors[i].DisplayName != visitors[j].DisplayName {
			return visitors[i].DisplayName < visitors[j].DisplayName
		}
		return visitors[i].ID < visitors[j].ID
	})
	return visitors
}

// buildVillageContextBusinessCatalog collects every (keeper,
// structure, items) triple where the keeper has a work structure +
// produce entries + at least one item with a retail price. Sorted by
// StructureLabel, then OwnerDisplayName, then ItemKind within each
// entry's Items slice.
func buildVillageContextBusinessCatalog(w *World) []BusinessOffering {
	var entries []BusinessOffering
	for id, a := range w.Actors {
		if a == nil || a.Kind == KindPC {
			continue
		}
		if a.WorkStructureID == "" {
			continue
		}
		produce := a.RestockPolicy.ProduceEntries()
		if len(produce) == 0 {
			continue
		}
		structure, ok := w.Structures[a.WorkStructureID]
		if !ok || structure == nil || structure.DisplayName == "" {
			continue
		}
		var items []BusinessItem
		for _, p := range produce {
			recipe, ok := w.Recipes[p.Item]
			if !ok || recipe == nil {
				continue
			}
			items = append(items, BusinessItem{
				Item:  p.Item,
				Price: recipe.RetailPrice,
			})
		}
		if len(items) == 0 {
			continue
		}
		sort.Slice(items, func(i, j int) bool {
			return items[i].Item < items[j].Item
		})
		entries = append(entries, BusinessOffering{
			OwnerID:          id,
			OwnerDisplayName: a.DisplayName,
			StructureID:      a.WorkStructureID,
			StructureLabel:   structure.DisplayName,
			Items:            items,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].StructureLabel != entries[j].StructureLabel {
			return entries[i].StructureLabel < entries[j].StructureLabel
		}
		return entries[i].OwnerDisplayName < entries[j].OwnerDisplayName
	})
	return entries
}
