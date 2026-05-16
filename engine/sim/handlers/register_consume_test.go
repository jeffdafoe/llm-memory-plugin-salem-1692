package handlers

import (
	"encoding/json"
	"testing"
)

// register_consume_test.go — verifies the RegisterConsume helper produces a
// registry entry with the expected shape: ClassCommit,
// AvailabilityAvailable, terminal-on-success FALSE, schema bytes are
// valid JSON, decode + handler wired through. Mirrors register_pay_test.go.

func TestRegisterConsume_AddsAvailableCommitEntry(t *testing.T) {
	r := NewRegistry()
	if err := RegisterConsume(r); err != nil {
		t.Fatalf("RegisterConsume: %v", err)
	}
	entry, ok := r.Lookup("consume")
	if !ok {
		t.Fatal("Lookup(consume): not found after RegisterConsume")
	}
	if entry.Class != ClassCommit {
		t.Errorf("Class = %v, want ClassCommit", entry.Class)
	}
	if entry.TerminalPolicy != TerminalNever {
		t.Errorf("TerminalPolicy = %v, want TerminalNever (consume is non-terminal)", entry.TerminalPolicy)
	}
	if entry.Availability != AvailabilityAvailable {
		t.Errorf("Availability = %v, want AvailabilityAvailable", entry.Availability)
	}
	if entry.Description == "" {
		t.Error("Description is empty (model gets no guidance)")
	}
}

func TestRegisterConsume_SchemaIsValidJSON(t *testing.T) {
	r := NewRegistry()
	if err := RegisterConsume(r); err != nil {
		t.Fatalf("RegisterConsume: %v", err)
	}
	entry, _ := r.Lookup("consume")
	if !json.Valid(entry.Schema) {
		t.Fatal("Schema bytes are not valid JSON")
	}
	var doc map[string]any
	if err := json.Unmarshal(entry.Schema, &doc); err != nil {
		t.Fatalf("Schema unmarshal: %v", err)
	}
	req, _ := doc["required"].([]any)
	requiredSet := map[string]bool{}
	for _, r := range req {
		if s, ok := r.(string); ok {
			requiredSet[s] = true
		}
	}
	if !requiredSet["item"] {
		t.Error("Schema required missing 'item'")
	}
	if !requiredSet["qty"] {
		t.Error("Schema required missing 'qty' — design call #6 locked qty as required, no implicit default")
	}
	addl, ok := doc["additionalProperties"].(bool)
	if !ok || addl {
		t.Error("Schema must set additionalProperties: false")
	}
}

func TestRegisterConsume_DecodeWiredThrough(t *testing.T) {
	r := NewRegistry()
	if err := RegisterConsume(r); err != nil {
		t.Fatalf("RegisterConsume: %v", err)
	}
	entry, _ := r.Lookup("consume")
	if entry.Decode == nil {
		t.Fatal("Decode is nil")
	}
	args, err := entry.Decode(json.RawMessage(`{"item":"ale","qty":1}`))
	if err != nil {
		t.Fatalf("entry.Decode: %v", err)
	}
	a, ok := args.(ConsumeArgs)
	if !ok || a.Item != "ale" || a.Qty != 1 {
		t.Errorf("decoded = %+v, want ConsumeArgs{Item:ale, Qty:1}", args)
	}
}

func TestRegisterConsume_CommitFnWiredThrough(t *testing.T) {
	r := NewRegistry()
	if err := RegisterConsume(r); err != nil {
		t.Fatalf("RegisterConsume: %v", err)
	}
	entry, _ := r.Lookup("consume")
	if entry.Commit() == nil {
		t.Error("Commit accessor returned nil for consume entry")
	}
}

func TestRegisterConsume_AdvertisedSpec(t *testing.T) {
	r := NewRegistry()
	if err := RegisterConsume(r); err != nil {
		t.Fatalf("RegisterConsume: %v", err)
	}
	specs := r.AdvertisedSpecs()
	if len(specs) != 1 {
		t.Fatalf("AdvertisedSpecs len = %d, want 1", len(specs))
	}
	if specs[0].Name != "consume" {
		t.Errorf("AdvertisedSpec.Name = %q, want consume", specs[0].Name)
	}
	if specs[0].Description == "" {
		t.Error("AdvertisedSpec.Description is empty")
	}
}

func TestRegisterConsume_RejectsDuplicate(t *testing.T) {
	r := NewRegistry()
	if err := RegisterConsume(r); err != nil {
		t.Fatalf("RegisterConsume 1st: %v", err)
	}
	if err := RegisterConsume(r); err == nil {
		t.Error("RegisterConsume 2nd: expected error for duplicate registration")
	}
}
