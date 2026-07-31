package pg

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"testing"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// settings_registry_coverage_test.go — LLM-577. The drift guard.
//
// The whole point of the settings registry is that the loader, the checkpoint
// and the operator API stop being three lists that have to be kept in agreement
// by hand. The registry gives the last two a single source, but the LOADER is
// still where a new setting is born: someone adds
// `s.Foo = parseIntSetting(values, "foo_bar", DefaultFoo)` to buildSettings and,
// without this test, the key is live in the engine and invisible to an operator
// — exactly how the visitor_* family ended up readable-but-unwritable and
// world_dusk_time ended up neither.
//
// So: parse this package's own environment.go, pull the key literal out of every
// parse*Setting / clampNonNegSetting call in it, and require that set to match
// sim.SettingKeys() exactly. Adding a setting to the loader without registering
// it now fails the build.
//
// The scan is deliberately syntactic rather than clever. It keys off the helper
// FUNCTION NAMES and takes the first string literal argument, which is the
// convention every call in the file follows. If someone introduces a new helper
// or stops passing the key as a literal, this test fails loudly with an
// unrecognised-shape message rather than silently under-reporting — a guard that
// can quietly stop guarding is worse than none.

// settingLoaderHelpers are the buildSettings helpers whose second argument is a
// setting key literal. loadNeedThresholds is deliberately absent: it walks the
// sim.Needs registry rather than naming keys, and the registry generates those
// same keys from the same source, so they are covered by construction.
var settingLoaderHelpers = map[string]bool{
	"parseIntSetting":      true,
	"parseFloatSetting":    true,
	"parseBoolSetting":     true,
	"parseStringSetting":   true,
	"parseDurationSetting": true,
	"clampNonNegSetting":   true,
}

func TestSettingRegistryCoversEveryLoaderKey(t *testing.T) {
	loaderKeys := scanLoaderSettingKeys(t)
	if len(loaderKeys) == 0 {
		t.Fatal("scanned environment.go and found no setting keys — the scan shape is wrong, not the loader")
	}

	registered := make(map[string]bool)
	for _, k := range sim.SettingKeys() {
		registered[k] = true
	}
	// The need thresholds are generated on both sides from sim.Needs, so they
	// are registered but never appear as a literal in the loader. Exempt them
	// from the "registered but not loaded" direction only.
	generated := make(map[string]bool, len(sim.Needs))
	for _, n := range sim.Needs {
		generated[n.ThresholdSettingKey] = true
	}

	var missing []string
	for k := range loaderKeys {
		if !registered[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("these setting keys are read by buildSettings but not registered in sim's settings registry, so they are invisible to GET /umbilical/settings and unwritable via /umbilical/settings/set — add them to buildSettingRegistry: %v", missing)
	}

	var orphaned []string
	for k := range registered {
		if !loaderKeys[k] && !generated[k] {
			orphaned = append(orphaned, k)
		}
	}
	sort.Strings(orphaned)
	if len(orphaned) > 0 {
		t.Errorf("these keys are registered in sim's settings registry but never read by buildSettings — either the loader dropped them (a live setting silently reverted to its zero value) or the registry entry is stale: %v", orphaned)
	}
}

// scanLoaderSettingKeys parses environment.go and returns every setting key
// literal passed to one of settingLoaderHelpers.
func scanLoaderSettingKeys(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "environment.go", nil, 0)
	if err != nil {
		t.Fatalf("parse environment.go: %v", err)
	}

	keys := make(map[string]bool)
	// Two functions in the file legitimately call a helper with a non-literal
	// key and are skipped wholesale:
	//
	//   loadNeedThresholds — walks sim.Needs and passes n.ThresholdSettingKey.
	//     Generated on both sides from the same registry, so covered already.
	//   clampNonNegSetting — a wrapper that forwards its own key parameter to
	//     parseIntSetting. Its CALLERS in buildSettings pass literals and are
	//     scanned normally; this is just the delegation hop.
	//
	// Scoping by function (rather than tolerating non-literals everywhere) is
	// what keeps the guard sharp: a non-literal key introduced anywhere else,
	// including in buildSettings itself, still fails.
	skipFuncs := map[string]bool{
		"loadNeedThresholds": true,
		"clampNonNegSetting": true,
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || skipFuncs[fn.Name.Name] {
			continue
		}
		scanFuncForSettingKeys(t, fset, fn, keys)
	}
	return keys
}

// scanFuncForSettingKeys collects the setting-key literals used by one function.
func scanFuncForSettingKeys(t *testing.T, fset *token.FileSet, fn *ast.FuncDecl, keys map[string]bool) {
	t.Helper()
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if !ok || !settingLoaderHelpers[ident.Name] {
			return true
		}
		// Every call in the file is helper(values, "key", default…). A call
		// that does not match that shape means the convention changed, and a
		// scan that silently skipped it would stop guarding without saying so.
		if len(call.Args) < 2 {
			t.Errorf("%s call at %s has fewer than 2 arguments — the loader convention changed and this scan no longer finds keys",
				ident.Name, fset.Position(call.Pos()))
			return true
		}
		lit, ok := call.Args[1].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			t.Errorf("%s call at %s does not pass its setting key as a string literal — this scan cannot verify it; either restore the literal or teach the scan about the new shape",
				ident.Name, fset.Position(call.Pos()))
			return true
		}
		key, err := strconv.Unquote(lit.Value)
		if err != nil {
			t.Errorf("%s call at %s has an unparseable key literal %s: %v", ident.Name, fset.Position(call.Pos()), lit.Value, err)
			return true
		}
		keys[key] = true
		return true
	})
}
