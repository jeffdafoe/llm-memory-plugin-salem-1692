package sim

import (
	"math/rand"
	"testing"
	"time"
)

// visitor_persona_internal_test.go — LLM-686: a visitor name is one fixed man.

// TestVisitorPersonas_TableShape pins the table invariants init enforces, plus the
// ones it can't: every passer calling renders (sprite + vocation), and every
// persona's name survives the " the <archetype>" display round trip that promotion
// and the in-village check key on.
func TestVisitorPersonas_TableShape(t *testing.T) {
	for _, p := range visitorPersonas {
		if p.Class == VisitorClassPasser {
			if passerThroughSprite[p.Archetype] == "" {
				t.Errorf("passer %s calling %q has no sprite", p.Name, p.Archetype)
			}
			if passerThroughVocation[p.Archetype] == "" {
				t.Errorf("passer %s calling %q has no vocation line", p.Name, p.Archetype)
			}
		}
		if p.Class == VisitorClassFactor && p.Origin != FactorOrigin {
			t.Errorf("factor %s hails from %q, want %q", p.Name, p.Origin, FactorOrigin)
		}
		if got := personaNameFromDisplayName(p.Name + " the carter"); got != p.Name {
			t.Errorf("display round trip of %q = %q", p.Name, got)
		}
	}
}

// TestVisitorClassOf maps every errand shape to the class whose names may carry it.
func TestVisitorClassOf(t *testing.T) {
	cases := []struct {
		name  string
		trade *TradeErrand
		want  VisitorClass
	}{
		{"no errand", nil, VisitorClassPasser},
		{"carter", &TradeErrand{Direction: TradeDirectionSell, Carter: true}, VisitorClassCarter},
		{"shortage peddler", &TradeErrand{Direction: TradeDirectionSell, Peddler: true, Good: "meat"}, VisitorClassDealer},
		{"buyer", &TradeErrand{Direction: TradeDirectionBuy, Good: "cheese"}, VisitorClassDealer},
		{"factor", &TradeErrand{Direction: TradeDirectionSell, Good: "iron"}, VisitorClassFactor},
	}
	for _, tc := range cases {
		if got := visitorClassOf(tc.trade); got != tc.want {
			t.Errorf("%s: visitorClassOf = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestPickVisitorPersona_ClassAndExclusions: a pick always comes from the asked
// class; a name already walking the village is never picked; and with every name
// of the class in the village there is no pick at all — never a second copy of a
// man who is here.
func TestPickVisitorPersona_ClassAndExclusions(t *testing.T) {
	var factors []string
	for _, p := range visitorPersonas {
		if p.Class == VisitorClassFactor {
			factors = append(factors, p.Name)
		}
	}
	w := &World{Actors: map[ActorID]*Actor{}}
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 50; i++ {
		for _, class := range []VisitorClass{VisitorClassFactor, VisitorClassCarter, VisitorClassDealer, VisitorClassPasser} {
			if p, ok := pickVisitorPersona(w, r, class); !ok || p.Class != class {
				t.Fatalf("pick for %s returned %s (%s, ok=%v)", class, p.Name, p.Class, ok)
			}
		}
	}

	// All factors but the last are in the village: the last is the only pick.
	for i, name := range factors[:len(factors)-1] {
		id := ActorID("vstr-0000000" + string(rune('a'+i)))
		w.Actors[id] = &Actor{ID: id, DisplayName: name + " the factor", VisitorState: &VisitorState{}}
	}
	want := factors[len(factors)-1]
	for i := 0; i < 20; i++ {
		if p, ok := pickVisitorPersona(w, r, VisitorClassFactor); !ok || p.Name != want {
			t.Fatalf("pick = %s (ok=%v), want the one factor not in the village (%s)", p.Name, ok, want)
		}
	}

	// Every factor in the village: no pick.
	w.Actors["vstr-0000000z"] = &Actor{ID: "vstr-0000000z", DisplayName: want + " the factor", VisitorState: &VisitorState{}}
	if p, ok := pickVisitorPersona(w, r, VisitorClassFactor); ok {
		t.Fatalf("pick with every factor present = %s, want none", p.Name)
	}
}

// TestPickVisitorPersona_SkipsVillagerSurname: a name whose surname a seated
// villager holds is not picked while the class has another free name — and when
// it is the only free name it is picked (the surname rule relaxes, the in-village
// rule never does).
func TestPickVisitorPersona_SkipsVillagerSurname(t *testing.T) {
	w := &World{Actors: map[ActorID]*Actor{
		"v1": {ID: "v1", DisplayName: "Ruth Wendell", Kind: KindNPCStateful},
	}}
	r := rand.New(rand.NewSource(3))
	for i := 0; i < 50; i++ {
		if p, _ := pickVisitorPersona(w, r, VisitorClassFactor); p.Name == "Caleb Wendell" {
			t.Fatal("picked Caleb Wendell while a villager is named Wendell")
		}
	}
	i := 0
	for _, p := range visitorPersonas {
		if p.Class == VisitorClassFactor && p.Name != "Caleb Wendell" {
			id := ActorID("vstr-000000" + string(rune('a'+i)) + "0")
			w.Actors[id] = &Actor{ID: id, DisplayName: p.Name + " the factor", VisitorState: &VisitorState{}}
			i++
		}
	}
	if p, ok := pickVisitorPersona(w, r, VisitorClassFactor); !ok || p.Name != "Caleb Wendell" {
		t.Fatalf("only free factor is Caleb Wendell; pick = %s (ok=%v)", p.Name, ok)
	}
}

// TestApplyFixedPersona: a table name takes its hometown (and a passer his
// calling); a merchant row's stored label is left; an unknown name is untouched.
func TestApplyFixedPersona(t *testing.T) {
	merchant := &RecurringVisitor{Name: "Caleb Wendell", Archetype: "provisioner", Origin: "Wenham"}
	if !applyFixedPersona(merchant) || merchant.Origin != FactorOrigin || merchant.Archetype != "provisioner" {
		t.Errorf("merchant row = the %s from %s, want the provisioner (label kept) from %s",
			merchant.Archetype, merchant.Origin, FactorOrigin)
	}
	passer := &RecurringVisitor{Name: "Ephraim Pollard", Archetype: "shovel-buyer", Origin: "Ipswich"}
	if !applyFixedPersona(passer) || passer.Archetype != "itinerant musician" || passer.Origin != "the coast road" {
		t.Errorf("passer row = the %s from %s, want the itinerant musician from the coast road", passer.Archetype, passer.Origin)
	}
	if applyFixedPersona(passer) {
		t.Error("second apply reported a change on an aligned row")
	}
	unknown := &RecurringVisitor{Name: "Obadiah Pratt", Archetype: "circuit preacher", Origin: "Lynn"}
	if applyFixedPersona(unknown) || unknown.Archetype != "circuit preacher" || unknown.Origin != "Lynn" {
		t.Errorf("unknown name changed: the %s from %s", unknown.Archetype, unknown.Origin)
	}
}

// TestPickDueReturner_SkipsMerchantName: a due merchant-name row is never picked —
// he comes back with his class — while a due passer or an unknown name still is.
func TestPickDueReturner_SkipsMerchantName(t *testing.T) {
	now := time.Now().UTC()
	merchant := &RecurringVisitor{ID: "rvis-00000001", Name: "Elias Drum", NextReturnAt: now.Add(-48 * time.Hour)}
	passer := &RecurringVisitor{ID: "rvis-00000002", Name: "Roger Standish", NextReturnAt: now.Add(-time.Hour)}
	w := &World{Actors: map[ActorID]*Actor{}, RecurringVisitors: map[RecurringVisitorID]*RecurringVisitor{
		merchant.ID: merchant, passer.ID: passer,
	}}
	if got, ok := w.pickDueReturner(now); !ok || got.ID != passer.ID {
		t.Fatalf("pickDueReturner = %v (ok=%v), want the passer %s over the more overdue merchant", got, ok, passer.ID)
	}
	delete(w.RecurringVisitors, passer.ID)
	if got, ok := w.pickDueReturner(now); ok {
		t.Fatalf("pickDueReturner returned merchant-name row %s (%s)", got.ID, got.Name)
	}
}
