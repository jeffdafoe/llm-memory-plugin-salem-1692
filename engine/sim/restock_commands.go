package sim

import (
	"encoding/json"
	"errors"
)

// restock_commands.go — live, durable per-entry editing of an actor's
// RestockPolicy (LLM-95).
//
// An actor's RestockPolicy is a PROJECTION: it is rebuilt from the union of
// every attribute's `params.restock` array (the actor_attribute rows loaded
// into Actor.Attributes as raw JSONB). The projection used to live only in the
// pg load path (repo/pg.rebuildActorAttributeProjections); RebuildRestockPolicy
// below lifts it into sim so the LoadWorld projection and the live edit command
// share ONE implementation — pg now delegates the restock half to this file.
//
// Durability is free: Actor.Attributes already rides the periodic checkpoint
// (pg.ActorsRepo.SaveSnapshot upserts every attribute row), so mutating the
// `params.restock` blob in memory persists on the next snapshot — the same
// model AddActorAttribute / RemoveActorAttribute rely on. There is no new
// persistence code; an edit is a normal world Command.
//
// The two operator entry points:
//   - SetRestockEntry: add-or-update one entry (produce / buy / forage).
//   - RemoveRestockEntry: drop one entry by item.
// Both re-project RestockPolicy in the same Command so the live view and the
// produce/restock/forage ticks see the change immediately.

// restock-edit error sentinels. Resolved to HTTP status by the umbilical
// handler (httpapi/umbilical_restock.go).
var (
	// ErrInvalidRestockSource — source is not one of produce / buy / forage.
	ErrInvalidRestockSource = errors.New("invalid restock source")
	// ErrNoRecipeForProduce — a produce entry was requested for an item with no
	// recipe in the catalog. Such an entry is silently inert in the produce tick
	// (it looks up w.Recipes[item] and skips when missing), so we refuse it
	// rather than let the operator add a no-op.
	ErrNoRecipeForProduce = errors.New("no recipe exists for produce item")
	// ErrNoAttributeForRestock — the actor carries no attribute row to hold a
	// restock entry. A producer/keeper NPC always has at least one; this guards
	// the degenerate "edit restock on an attribute-less actor" case.
	ErrNoAttributeForRestock = errors.New("actor has no attribute to hold a restock entry")
	// ErrRestockEntryNotFound — remove targeted an item the actor doesn't stock.
	ErrRestockEntryNotFound = errors.New("restock entry not found")
)

// restockParams / restockEntryParams mirror the stored shape of an
// actor_attribute.params blob's restock array ({item, source, max, target}).
// The same wire shape the pg load path reads, kept here so sim owns the
// projection. `target` is the legacy buy-entry alias for the cap; new writes
// canonicalize onto `max`.
type restockParams struct {
	Restock []restockEntryParams `json:"restock"`
}

type restockEntryParams struct {
	Item   string `json:"item"`
	Source string `json:"source"`
	Max    int    `json:"max,omitempty"`
	Target int    `json:"target,omitempty"`
}

// RebuildRestockPolicy reconstructs a.RestockPolicy from the union of every
// attribute's params.restock, in sorted-slug order with first-listed-wins on
// item ties (v1's `ORDER BY slug`). Unparseable params and unknown-source
// entries are skipped (a malformed or partially-valid role row must not fail
// the whole projection). Sets a.RestockPolicy to nil when no entry survives,
// matching the "nil == not a restocker" field semantics the ticks rely on.
//
// Faithful port of the pg load-side union; callable on the world goroutine for
// the live edit commands.
func RebuildRestockPolicy(a *Actor) {
	a.RestockPolicy = nil
	if a == nil {
		return
	}
	var entries []RestockEntry
	seen := make(map[ItemKind]bool)
	for _, slug := range sortedAttributeSlugs(a) {
		raw := a.Attributes[slug]
		if len(raw) == 0 {
			continue
		}
		var params restockParams
		if err := json.Unmarshal(raw, &params); err != nil {
			continue // unparseable — other roles may still be valid (v1 parity)
		}
		for _, e := range params.Restock {
			item := ItemKind(e.Item)
			if item == "" || seen[item] {
				continue
			}
			source := RestockSource(e.Source)
			if source != RestockSourceProduce && source != RestockSourceBuy &&
				source != RestockSourceForage {
				continue // unknown source mode — skip (v1 parity)
			}
			seen[item] = true
			entries = append(entries, RestockEntry{
				Item:   item,
				Source: source,
				Max:    e.Max,
				Target: e.Target,
			})
		}
	}
	if len(entries) > 0 {
		a.RestockPolicy = &RestockPolicy{Restock: entries}
	}
}

// RestockPolicyResult is the command reply: the actor's full post-mutation
// restock entries (so the operator sees the applied result without a follow-up
// read).
type RestockPolicyResult struct {
	ID      ActorID
	Entries []RestockEntry
}

func restockResult(id ActorID, a *Actor) RestockPolicyResult {
	out := RestockPolicyResult{ID: id, Entries: []RestockEntry{}}
	if a.RestockPolicy != nil {
		out.Entries = append(out.Entries, a.RestockPolicy.Restock...)
	}
	return out
}

// SetRestockEntry adds or updates one restock entry on an NPC and re-projects
// the policy. The item is resolved against the catalog (key or label) to its
// canonical kind; an unknown item is ErrUnknownItemKind. A produce-source entry
// additionally requires a recipe (else ErrNoRecipeForProduce — it would never
// fire). PCs are rejected (ErrActorNotFound) — restock is an NPC concept.
//
// The entry is written into whichever attribute row already owns the item (so
// an update lands where the value lives in the union); a brand-new item is
// appended to the actor's existing restock-bearing attribute, or its
// first attribute if none carries restock yet. capacity is the personal-carry
// cap, stored on `max` (0 = no cap configured).
func SetRestockEntry(id ActorID, itemName string, source RestockSource, capacity int) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			a, err := editableNPC(w, id)
			if err != nil {
				return nil, err
			}
			if source != RestockSourceProduce && source != RestockSourceBuy &&
				source != RestockSourceForage {
				return nil, ErrInvalidRestockSource
			}
			kind, ok := resolveItemKind(w, itemName)
			if !ok {
				return nil, ErrUnknownItemKind
			}
			if source == RestockSourceProduce {
				if _, ok := w.Recipes[kind]; !ok {
					return nil, ErrNoRecipeForProduce
				}
			}
			if err := upsertRestockEntry(a, kind, source, capacity); err != nil {
				return nil, err
			}
			RebuildRestockPolicy(a)
			return restockResult(id, a), nil
		},
	}
}

// RemoveRestockEntry drops one entry (by item) from every attribute that
// carries it, then re-projects. ErrRestockEntryNotFound when the item isn't
// stocked. Removing from ALL slugs (not just the first-listed owner) prevents a
// shadowed duplicate in a later slug from resurfacing once the winner is gone.
func RemoveRestockEntry(id ActorID, itemName string) Command {
	return Command{
		Fn: func(w *World) (any, error) {
			a, err := editableNPC(w, id)
			if err != nil {
				return nil, err
			}
			kind, ok := resolveItemKind(w, itemName)
			if !ok {
				return nil, ErrUnknownItemKind
			}
			removed := false
			for _, slug := range sortedAttributeSlugs(a) {
				m, entries, err := slugRestock(a.Attributes[slug])
				if err != nil {
					continue
				}
				kept := make([]restockEntryParams, 0, len(entries))
				for _, e := range entries {
					if ItemKind(e.Item) == kind {
						removed = true
						continue
					}
					kept = append(kept, e)
				}
				if len(kept) != len(entries) {
					if err := writeSlugRestock(a, slug, m, kept); err != nil {
						return nil, err
					}
				}
			}
			if !removed {
				return nil, ErrRestockEntryNotFound
			}
			RebuildRestockPolicy(a)
			return restockResult(id, a), nil
		},
	}
}

// upsertRestockEntry writes the (kind, source, capacity) entry into the right
// attribute row's params.restock — the existing owner of the item, else the
// actor's existing restock-bearing attribute, else its first attribute. Updates
// in place when the item is already present; appends otherwise. Sibling params
// keys (e.g. a businessowner row's `flavor`) are preserved verbatim.
func upsertRestockEntry(a *Actor, kind ItemKind, source RestockSource, capacity int) error {
	slug, ok := findRestockOwnerSlug(a, kind)
	if !ok {
		slug, ok = findRestockAddSlug(a)
		if !ok {
			return ErrNoAttributeForRestock
		}
	}
	m, entries, err := slugRestock(a.Attributes[slug])
	if err != nil {
		return err
	}
	updated := false
	for i := range entries {
		if ItemKind(entries[i].Item) == kind {
			entries[i].Source = string(source)
			entries[i].Max = capacity
			entries[i].Target = 0 // canonicalize the cap onto max
			updated = true
			break
		}
	}
	if !updated {
		entries = append(entries, restockEntryParams{
			Item:   string(kind),
			Source: string(source),
			Max:    capacity,
		})
	}
	return writeSlugRestock(a, slug, m, entries)
}

// findRestockOwnerSlug returns the first (sorted) attribute slug whose
// params.restock already lists `kind` — the row an update must land in so the
// change is the one the union surfaces.
func findRestockOwnerSlug(a *Actor, kind ItemKind) (string, bool) {
	for _, slug := range sortedAttributeSlugs(a) {
		_, entries, err := slugRestock(a.Attributes[slug])
		if err != nil {
			continue
		}
		for _, e := range entries {
			if ItemKind(e.Item) == kind {
				return slug, true
			}
		}
	}
	return "", false
}

// findRestockAddSlug picks the attribute row a brand-new entry is appended to:
// the first slug that already carries restock entries (keep restock together),
// else the first slug whose params parse (an empty/new attribute is fine). The
// returned slug is guaranteed parseable so the caller's re-read can't fail.
func findRestockAddSlug(a *Actor) (string, bool) {
	slugs := sortedAttributeSlugs(a)
	for _, slug := range slugs {
		if _, entries, err := slugRestock(a.Attributes[slug]); err == nil && len(entries) > 0 {
			return slug, true
		}
	}
	for _, slug := range slugs {
		if _, _, err := slugRestock(a.Attributes[slug]); err == nil {
			return slug, true
		}
	}
	return "", false
}

// slugRestock decodes one attribute's raw params into (all keys, restock
// entries). The full key map is returned as raw messages so a write can replace
// just the `restock` key and leave every sibling (e.g. `flavor`) byte-for-byte
// intact. An empty/absent params blob decodes to an empty map with no entries.
func slugRestock(raw []byte) (map[string]json.RawMessage, []restockEntryParams, error) {
	m := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, nil, err
		}
	}
	var entries []restockEntryParams
	if rm, ok := m["restock"]; ok && len(rm) > 0 {
		if err := json.Unmarshal(rm, &entries); err != nil {
			return nil, nil, err
		}
	}
	return m, entries, nil
}

// writeSlugRestock re-marshals entries into the params map's `restock` key and
// stores the blob back on the actor. The column is jsonb (SaveSnapshot casts
// ::jsonb), so the output must be valid JSON — a marshaled map always is.
func writeSlugRestock(a *Actor, slug string, m map[string]json.RawMessage, entries []restockEntryParams) error {
	rm, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	if m == nil {
		m = map[string]json.RawMessage{}
	}
	m["restock"] = rm
	blob, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if a.Attributes == nil {
		a.Attributes = map[string][]byte{}
	}
	a.Attributes[slug] = blob
	return nil
}
