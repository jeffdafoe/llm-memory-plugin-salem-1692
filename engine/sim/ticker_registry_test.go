package sim

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ticker_registry_test.go — the coverage guard for the cadence contract (LLM-395).
//
// The staleness alarm is OPT-IN: a ticker that beats without declaring a cadence
// is never judged (see ticker_health.go). That fail-safe is what stops a beat site
// nobody taught a cadence to from crying wolf — but it also means a FORGOTTEN
// REGISTRATION silently un-covers a ticker, turning a critical alarm off for that
// cadence driver with no error anywhere. Nothing at runtime can tell the two
// apart, so the guard has to live here.
//
// This test walks the engine source and reconciles the two sets:
//
//   - every name passed to beatTicker / BeatTicker  (the tickers that exist)
//   - every name passed to RegisterTicker           (the tickers that are judged)
//
// and requires them to be equal. Both directions are failures with teeth:
//
//   - beaten but not registered → the ticker is invisible to the alarm.
//   - registered but never beaten → a phantom (typo'd name, or a ticker that was
//     deleted and left its declaration behind). It has no beat to keep it fresh, so
//     it goes stale on its own deadline and false-alarms forever — the exact
//     cry-wolf the fail-safe is meant to prevent.
//
// Parsed as Go rather than grepped so a name mentioned in a COMMENT (ticker_health.go
// quotes a `w.BeatTicker("atmosphere")` call in its prose) can't enter either set.

// tickerCallNames returns the string-literal first arguments of every call to one
// of the named methods, across every non-test .go file under root.
func tickerCallNames(t *testing.T, root string, methods ...string) map[string]string {
	t.Helper()

	want := make(map[string]bool, len(methods))
	for _, m := range methods {
		want[m] = true
	}

	found := make(map[string]string) // ticker name -> file it was found in
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !want[sel.Sel.Name] {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				// A non-literal name (a variable, a const) would defeat this scan.
				// None exist today; fail loudly rather than silently under-counting.
				t.Errorf("%s: %s called with a non-literal ticker name — the coverage guard cannot see it", path, sel.Sel.Name)
				return true
			}
			name, uerr := strconv.Unquote(lit.Value)
			if uerr != nil {
				t.Errorf("%s: unquote %s: %v", path, lit.Value, uerr)
				return true
			}
			found[name] = path
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return found
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestTickerCadenceCoverage(t *testing.T) {
	// The test runs in engine/sim, so "." spans the sim package, the cascade
	// package, httpapi, and cmd/engine — every place a ticker beats or is declared.
	const root = "."

	beaten := tickerCallNames(t, root, "beatTicker", "BeatTicker")
	registered := tickerCallNames(t, root, "RegisterTicker")

	if len(beaten) == 0 {
		t.Fatal("found no ticker beats at all — the scan is broken, not the code")
	}

	for _, name := range sortedKeys(beaten) {
		if _, ok := registered[name]; !ok {
			t.Errorf("ticker %q beats (%s) but never declares a cadence via RegisterTicker.\n"+
				"An unregistered ticker is INVISIBLE to the ticker_stale alarm: if its goroutine dies, "+
				"nothing will scream. Declare it — before the goroutine is launched — in "+
				"sim.RegisterCoreTickers, in the cascade's RegisterX helper, or at its wiring site.",
				name, beaten[name])
		}
	}

	for _, name := range sortedKeys(registered) {
		if _, ok := beaten[name]; !ok {
			t.Errorf("ticker %q declares a cadence (%s) but nothing ever beats it.\n"+
				"A declared ticker with no beat never refreshes its last-fire, so it will go stale on its "+
				"own deadline and false-alarm forever. Either the name is a typo, or the ticker is gone "+
				"and its registration should go with it.",
				name, registered[name])
		}
	}
}

// The whole point of declaring before launch is that a ticker which never starts
// is still judged. Guard the ordering property directly: RegisterCoreTickers must
// leave every core ticker alarm-eligible with a positive cadence, having beaten
// nothing at all.
func TestRegisterCoreTickers_DeclaresPositiveCadencesWithoutBeating(t *testing.T) {
	w := NewWorld(Repository{})
	RegisterCoreTickers(w)

	got := w.TickerHealthSnapshot()
	if len(got) == 0 {
		t.Fatal("RegisterCoreTickers declared nothing")
	}
	for _, e := range got {
		if !e.Registered {
			t.Errorf("%s: Registered=false", e.Name)
		}
		if e.Interval <= 0 {
			t.Errorf("%s: Interval=%v — a non-positive cadence silently opts the ticker OUT of the alarm", e.Name, e.Interval)
		}
		if !e.AlarmEligible() {
			t.Errorf("%s: not alarm-eligible after registration", e.Name)
		}
		if e.Count != 0 {
			t.Errorf("%s: Count=%d — registration must not fabricate a beat", e.Name, e.Count)
		}
		if !e.LastFire.IsZero() {
			t.Errorf("%s: LastFire=%v — registration must not fabricate a beat", e.Name, e.LastFire)
		}
	}
}
