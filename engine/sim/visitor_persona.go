package sim

import (
	"fmt"
	"log"
	"math/rand"
)

// visitor_persona.go — a visitor's name is one fixed man (LLM-686).
//
// Villagers remember a traveler by name: "Caleb Wendell" in a keeper's memory is
// one person. So a name carries its trade and its road with it. Every name in
// visitorPersonas belongs to exactly one visitor class and one hometown, and a
// passer-through name also to one calling. A spawn picks the class first (from
// the bound errand) and then a name from that class, so "Caleb Wendell" is always
// the Boston factor and never a carter out of Andover on his next visit.
//
// The same name is also the returner identity: at most one recurring_visitor row
// per name (unique index, LLM-686 migration). A spawn of a name that already has a
// row links to it, so a player he met before is still recognized. Only
// passer-through names ride the scheduled return (pickDueReturner) — a merchant
// name comes back whenever his class spawns, bound to a real errand.
//
// A merchant's archetype LABEL is still derived from his errand good
// (visitorMerchantLabel: "cheese-buyer", "meat-peddler"); the table fixes his
// class and hometown, not the good he deals in on a given trip.

// VisitorClass is the visitor's line of work, fixed per name.
type VisitorClass string

const (
	// VisitorClassFactor — the wholesale factor out of Boston: a sell errand to the
	// village distributor (the LLM-410/455 import bale).
	VisitorClassFactor VisitorClass = "factor"
	// VisitorClassCarter — the carter who runs goods between the village's own
	// shops (carter.go).
	VisitorClassCarter VisitorClass = "carter"
	// VisitorClassDealer — a country dealer with one good: a buy errand at a keeper,
	// or a shortage peddler (LLM-656) bringing the one input a keeper lacks.
	VisitorClassDealer VisitorClass = "dealer"
	// VisitorClassPasser — a passer-through with no trade errand; his calling is the
	// persona's Archetype.
	VisitorClassPasser VisitorClass = "passer"
)

// VisitorPersona is one fixed traveler. Archetype is set only for a passer-through
// (his calling, a passerThroughArchetypePool entry); a merchant's label comes from
// his errand at spawn.
type VisitorPersona struct {
	Name      string
	Class     VisitorClass
	Origin    string
	Archetype string
}

// visitorPersonas — every traveler who can come to Salem. Male-coded only because
// every visitor sprite family is male-coded (see visitorSpriteName). Surnames are
// chosen not to match Salem's seated villagers; pickVisitorPersona still skips a
// name whose surname a villager holds, so a new villager can't be shadowed.
//
// The classes were assigned to match what the village already remembers of each
// name (the per-villager people notes as of 2026-09-30): the names most known as
// factors stay factors, and so on.
var visitorPersonas = []VisitorPersona{
	{Name: "Caleb Wendell", Class: VisitorClassFactor, Origin: FactorOrigin},
	{Name: "Elias Drum", Class: VisitorClassFactor, Origin: FactorOrigin},
	{Name: "Tobias Hewes", Class: VisitorClassFactor, Origin: FactorOrigin},
	{Name: "Obadiah Brewster", Class: VisitorClassFactor, Origin: FactorOrigin},
	{Name: "Jeremiah Soames", Class: VisitorClassFactor, Origin: FactorOrigin},

	{Name: "Master Whitcombe", Class: VisitorClassCarter, Origin: "Ipswich"},
	{Name: "Nathaniel Pratt", Class: VisitorClassCarter, Origin: "Rowley"},
	{Name: "Jonas Penhallow", Class: VisitorClassCarter, Origin: "Marblehead"},
	{Name: "Daniel Holcomb", Class: VisitorClassCarter, Origin: "Andover"},

	{Name: "Brother Ashford", Class: VisitorClassDealer, Origin: "Salem Town"},
	{Name: "Silas Withrow", Class: VisitorClassDealer, Origin: "Topsfield"},
	{Name: "Asa Larkin", Class: VisitorClassDealer, Origin: "Wenham"},
	{Name: "Amos Tilden", Class: VisitorClassDealer, Origin: "Beverly"},

	{Name: "Ephraim Pollard", Class: VisitorClassPasser, Origin: "the coast road", Archetype: "itinerant musician"},
	{Name: "Roger Standish", Class: VisitorClassPasser, Origin: "Boston", Archetype: "messenger"},
	{Name: "Master Babbage", Class: VisitorClassPasser, Origin: "Lynn", Archetype: "traveling scholar"},
	{Name: "Thaddeus Crowell", Class: VisitorClassPasser, Origin: "Rowley", Archetype: "circuit preacher"},
	{Name: "Josias Merrill", Class: VisitorClassPasser, Origin: "Andover", Archetype: "wandering surgeon"},
}

// visitorPersonaIndex is visitorPersonas keyed by name, built and validated in init.
var visitorPersonaIndex map[string]VisitorPersona

func init() {
	visitorPersonaIndex = make(map[string]VisitorPersona, len(visitorPersonas))
	passerCallings := make(map[string]bool, len(passerThroughArchetypePool))
	for _, a := range passerThroughArchetypePool {
		passerCallings[a] = true
	}
	perClass := map[VisitorClass]int{}
	for _, p := range visitorPersonas {
		if _, dup := visitorPersonaIndex[p.Name]; dup {
			panic("sim/visitor: persona name " + p.Name + " listed twice in visitorPersonas")
		}
		if p.Origin == "" {
			panic("sim/visitor: persona " + p.Name + " has no origin")
		}
		switch p.Class {
		case VisitorClassPasser:
			// A passer's calling picks his sprite and vocation line, so it must be a
			// pool entry (the init in visitor.go checks the pool against both maps).
			if !passerCallings[p.Archetype] {
				panic("sim/visitor: passer persona " + p.Name + " has calling " + p.Archetype + " not in passerThroughArchetypePool")
			}
		case VisitorClassFactor, VisitorClassCarter, VisitorClassDealer:
			if p.Archetype != "" {
				panic("sim/visitor: merchant persona " + p.Name + " sets an archetype; a merchant's label comes from his errand")
			}
		default:
			panic(fmt.Sprintf("sim/visitor: persona %s has unknown class %q", p.Name, p.Class))
		}
		perClass[p.Class]++
		visitorPersonaIndex[p.Name] = p
	}
	for _, c := range []VisitorClass{VisitorClassFactor, VisitorClassCarter, VisitorClassDealer, VisitorClassPasser} {
		if perClass[c] == 0 {
			panic(fmt.Sprintf("sim/visitor: no persona for visitor class %q", c))
		}
	}
}

// VisitorPersonaByName returns the fixed persona for a name, or false for a name
// not in the table (a test fixture, or a persona from before LLM-686).
func VisitorPersonaByName(name string) (VisitorPersona, bool) {
	p, ok := visitorPersonaIndex[name]
	return p, ok
}

// VisitorPersonas returns a copy of the persona table.
func VisitorPersonas() []VisitorPersona {
	return append([]VisitorPersona(nil), visitorPersonas...)
}

// visitorClassOf maps a bound errand to the visitor class whose names may carry
// it. nil (no errand bound) is a passer-through.
func visitorClassOf(trade *TradeErrand) VisitorClass {
	switch {
	case trade == nil:
		return VisitorClassPasser
	case trade.Carter:
		return VisitorClassCarter
	case trade.Peddler || trade.Direction == TradeDirectionBuy:
		return VisitorClassDealer
	default:
		return VisitorClassFactor
	}
}

// isMerchantPersonaName reports whether a name belongs to a merchant class in the
// table. Such a man comes back when his class spawns, not on the return schedule.
func isMerchantPersonaName(name string) bool {
	p, ok := visitorPersonaIndex[name]
	return ok && p.Class != VisitorClassPasser
}

// pickVisitorPersona picks a name from the class for a fresh spawn. It skips a
// name already walking the village (no two of the same man) and one whose surname
// a seated villager holds. When every name in the class is excluded it relaxes
// the in-village rule first, then the surname rule (logged) — a spawn always gets
// a persona of the right class.
//
// MUST be called from inside a Command.Fn (reads w.Actors directly).
func pickVisitorPersona(w *World, r *rand.Rand, class VisitorClass) VisitorPersona {
	villagerSurnames := loadActorSurnames(w)
	inVillage := visitorNamesInVillage(w)
	var inClass, clearSurname, open []VisitorPersona
	for _, p := range visitorPersonas {
		if p.Class != class {
			continue
		}
		inClass = append(inClass, p)
		if villagerSurnames[extractSurname(p.Name)] {
			continue
		}
		clearSurname = append(clearSurname, p)
		if !inVillage[p.Name] {
			open = append(open, p)
		}
	}
	switch {
	case len(open) > 0:
		return open[r.Intn(len(open))]
	case len(clearSurname) > 0:
		return clearSurname[r.Intn(len(clearSurname))]
	default:
		p := inClass[r.Intn(len(inClass))]
		log.Printf("sim/visitor: every %s persona's surname collides with a villager; shipping %q anyway", class, p.Name)
		return p
	}
}

// visitorNamesInVillage is the set of persona names of the visitors present now.
func visitorNamesInVillage(w *World) map[string]bool {
	out := map[string]bool{}
	for _, a := range w.Actors {
		if a != nil && a.VisitorState != nil {
			out[personaNameFromDisplayName(a.DisplayName)] = true
		}
	}
	return out
}

// applyFixedPersona brings a returner row's persona into line with the table: a
// name in the table takes its fixed hometown, and a passer its fixed calling. A
// merchant row's archetype is left as stored — a merchant never spawns from the
// row (his label comes from his errand), and the stored label is admin-read only.
// Disposition stays per row. Reports whether anything changed.
func applyFixedPersona(rv *RecurringVisitor) bool {
	p, ok := visitorPersonaIndex[rv.Name]
	if !ok {
		return false
	}
	changed := false
	if rv.Origin != p.Origin {
		rv.Origin = p.Origin
		changed = true
	}
	if p.Class == VisitorClassPasser && rv.Archetype != p.Archetype {
		rv.Archetype = p.Archetype
		changed = true
	}
	return changed
}

// recurringVisitorByName returns the returner row for a persona name, or nil. The
// set is small (one row per name), so a scan is fine.
func (w *World) recurringVisitorByName(name string) *RecurringVisitor {
	for _, rv := range w.RecurringVisitors {
		if rv != nil && rv.Name == name {
			return rv
		}
	}
	return nil
}
