package pg

// Real-pg integration test for the read-side AttributeDefinitionsRepo
// (ZBBS-HOME-292). Runs against an embedded Postgres with the full
// prod-baseline schema applied; skipped under `go test -short`.
//
// AttributeDefinitionsRepo is a read-only single-table load (attribute_
// definition) — no SaveSnapshot, no gen-marker, no Tx. The substrate fact
// worth exercising against real pg is the scope filter: only actor- and
// both-scoped rows load; object-only rows are excluded. The scope CHECK
// constraint also means a bad scope value can't be seeded, so the filter is
// exercised against exactly the three valid scopes.

import (
	"testing"
)

// LoadAll returns only the actor-assignable subset (scope actor/both), keyed
// by slug, and excludes object-only definitions.
func TestIntegration_AttributeDefinitions_LoadAllScopeFilter(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	// Seed migrations may put rows in attribute_definition (LLM-26 seeds
	// `worker`); clear it first so this case asserts the scope filter against
	// exactly the three rows it seeds. Mirrors LoadAllEmpty.
	if _, err := f.Pool.Exec(ctx, `DELETE FROM attribute_definition`); err != nil {
		t.Fatalf("clear attribute_definition: %v", err)
	}

	if _, err := f.Pool.Exec(ctx, `
		INSERT INTO attribute_definition (slug, display_name, scope)
		VALUES ('tavernkeeper', 'Tavern Keeper', 'actor'),
		       ('businessowner', 'Business Owner', 'both'),
		       ('noticeboard',  'Notice Board',  'object')`); err != nil {
		t.Fatalf("seed attribute_definition: %v", err)
	}

	got, err := NewAttributeDefinitionsRepo(f.Pool).LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	// Object-only definitions must be excluded.
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 (object-only must be excluded); got=%v", len(got), got)
	}
	if _, ok := got["noticeboard"]; ok {
		t.Errorf("object-scoped 'noticeboard' must be excluded, but it loaded")
	}

	keeper := got["tavernkeeper"]
	if keeper == nil || keeper.Slug != "tavernkeeper" || keeper.DisplayName != "Tavern Keeper" {
		t.Errorf("tavernkeeper = %+v, want {tavernkeeper, Tavern Keeper}", keeper)
	}
	owner := got["businessowner"]
	if owner == nil || owner.Slug != "businessowner" || owner.DisplayName != "Business Owner" {
		t.Errorf("businessowner = %+v, want {businessowner, Business Owner}", owner)
	}
}

// An empty table loads an empty (non-nil) map without error.
func TestIntegration_AttributeDefinitions_LoadAllEmpty(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	// The prod baseline schema may seed attribute_definition rows; clear it so
	// this case asserts the genuinely-empty load.
	if _, err := f.Pool.Exec(ctx, `DELETE FROM attribute_definition`); err != nil {
		t.Fatalf("clear attribute_definition: %v", err)
	}

	got, err := NewAttributeDefinitionsRepo(f.Pool).LoadAll(ctx)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if got == nil {
		t.Fatal("LoadAll returned nil map, want empty non-nil")
	}
	if len(got) != 0 {
		t.Errorf("len(got) = %d, want 0", len(got))
	}
}
