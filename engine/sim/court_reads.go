package sim

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// court_reads.go — the world reads behind the magistrate's tools (LLM-695).
// Each is a Command so it reads live state on the world goroutine; none writes.
// The durable record (agent_action_log) is read off the world goroutine by the
// court runner (engine/sim/court) through the action-log repo.

// CourtRosterEntry is one villager as the court knows them.
type CourtRosterEntry struct {
	ID     ActorID
	Name   string
	Role   string
	Work   string
	Home   string
	Player bool
}

// CourtSessionInfo is what a session opens with.
type CourtSessionInfo struct {
	Case     *CourtCase
	Roster   []CourtRosterEntry
	Location *time.Location
	Sitting  string
}

// CourtSessionContext returns the case and the village roster for a session.
func CourtSessionContext(caseID CourtCaseID) Command {
	return Command{Fn: func(w *World) (any, error) {
		c := w.CourtCases[caseID]
		if c == nil {
			return nil, fmt.Errorf("court: unknown case %q", caseID)
		}
		info := CourtSessionInfo{
			Case:     c.Clone(),
			Location: worldLocation(w),
			Sitting:  CourtSittingPhrase(w.Settings.CourtSittingTime),
		}
		for _, a := range w.Actors {
			if a == nil || !courtCanHear(a) {
				continue
			}
			e := CourtRosterEntry{ID: a.ID, Name: a.DisplayName, Role: a.Role, Player: a.Kind == KindPC}
			if s := w.Structures[a.WorkStructureID]; s != nil {
				e.Work = s.DisplayName
			}
			if s := w.Structures[a.HomeStructureID]; s != nil {
				e.Home = s.DisplayName
			}
			info.Roster = append(info.Roster, e)
		}
		sort.Slice(info.Roster, func(i, j int) bool { return info.Roster[i].Name < info.Roster[j].Name })
		return info, nil
	}}
}

// CourtVillager resolves a name the magistrate gave to a villager.
type CourtVillager struct {
	ID   ActorID
	Name string
}

// CourtResolveVillager resolves a villager by name (the filing rules: exact,
// then a unique name ending with the given words).
func CourtResolveVillager(name string) Command {
	return Command{Fn: func(w *World) (any, error) {
		a, err := courtResidentByName(w, name)
		if err != nil {
			return nil, err
		}
		return CourtVillager{ID: a.ID, Name: a.DisplayName}, nil
	}}
}

// CourtPurse describes what a villager holds right now.
func CourtPurse(name string) Command {
	return Command{Fn: func(w *World) (any, error) {
		a, err := courtResidentByName(w, name)
		if err != nil {
			return nil, err
		}
		var goods []string
		kinds := make([]ItemKind, 0, len(a.Inventory))
		for k, q := range a.Inventory {
			if q > 0 {
				kinds = append(kinds, k)
			}
		}
		sort.Slice(kinds, func(i, j int) bool { return kinds[i] < kinds[j] })
		for _, k := range kinds {
			goods = append(goods, inputCountPhrase(w, k, a.Inventory[k]))
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s holds %s right now", a.DisplayName, coinCount(a.Coins))
		if len(goods) == 0 {
			b.WriteString(" and no goods.")
		} else {
			b.WriteString(", and these goods: " + strings.Join(goods, ", ") + ".")
		}
		b.WriteString(" This is what is held now; it says nothing of what was held before.")
		return b.String(), nil
	}}
}

// CourtGoodsLookup says whether a thing exists in the village's goods at all,
// and who holds any now. The village's goods are a fixed catalog; a thing named
// in talk that is not in it has never been made, bought, held or taken by
// anyone. A thing an NPC once invented in talk is in the catalog under the
// "unknown" category and is never held — the same answer, said plainly.
func CourtGoodsLookup(name string) Command {
	return Command{Fn: func(w *World) (any, error) {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, ModelFacingError{Msg: "name the thing to look up."}
		}
		kind, ok := resolveItemKind(w, name)
		def := w.ItemKinds[kind]
		if !ok || def == nil {
			return fmt.Sprintf("There is no such thing as %q among the village's goods. No one in the village has ever made, bought, sold, held or carried one — it does not exist here.", name), nil
		}
		label := def.DisplayLabel
		if label == "" {
			label = string(kind)
		}
		if def.Category == ItemCategoryUnknown {
			return fmt.Sprintf("%q has been spoken of in the village, but it is not among the village's goods: no one has ever made, bought, sold or held one.", label), nil
		}
		type holder struct {
			name string
			qty  int
		}
		var holders []holder
		for _, a := range w.Actors {
			if a == nil || !courtCanHear(a) {
				continue
			}
			if q := a.Inventory[kind]; q > 0 {
				holders = append(holders, holder{a.DisplayName, q})
			}
		}
		sort.Slice(holders, func(i, j int) bool { return holders[i].name < holders[j].name })
		var b strings.Builder
		fmt.Fprintf(&b, "%q is among the village's goods.", label)
		if len(holders) == 0 {
			b.WriteString(" No villager holds any right now.")
		} else {
			parts := make([]string, 0, len(holders))
			for _, h := range holders {
				parts = append(parts, fmt.Sprintf("%s (%s)", h.name, inputCountPhrase(w, kind, h.qty)))
			}
			b.WriteString(" Held right now by: " + strings.Join(parts, ", ") + ".")
		}
		return b.String(), nil
	}}
}

// CourtEarlierRulings returns the court's rulings on matters a villager was
// party to or brought, oldest first.
func CourtEarlierRulings(name string) Command {
	return Command{Fn: func(w *World) (any, error) {
		a, err := courtResidentByName(w, name)
		if err != nil {
			return nil, err
		}
		var out []*CourtCase
		for _, c := range w.CourtCases {
			if c != nil && c.Status == CourtCaseStatusRuled && c.Involves(a.ID) {
				out = append(out, c.Clone())
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].RuledAt.Before(out[j].RuledAt) })
		return out, nil
	}}
}
