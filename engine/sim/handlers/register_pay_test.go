package handlers

import (
	"encoding/json"
	"testing"
)

// register_pay_test.go — verifies the RegisterPay helper produces a
// registry entry with the expected shape: ClassCommit,
// AvailabilityAvailable, terminal-on-success FALSE, schema bytes are
// valid JSON, decode + handler wired through. Mirrors register_speak_test.go.

func TestRegisterPay_AddsAvailableCommitEntry(t *testing.T) {
	r := NewRegistry()
	if err := RegisterPay(r); err != nil {
		t.Fatalf("RegisterPay: %v", err)
	}
	entry, ok := r.Lookup("pay")
	if !ok {
		t.Fatal("Lookup(pay): not found after RegisterPay")
	}
	if entry.Class != ClassCommit {
		t.Errorf("Class = %v, want ClassCommit", entry.Class)
	}
	if entry.TerminalPolicy != TerminalNever {
		t.Errorf("TerminalPolicy = %v, want TerminalNever (pay is non-terminal)", entry.TerminalPolicy)
	}
	if entry.Availability != AvailabilityAvailable {
		t.Errorf("Availability = %v, want AvailabilityAvailable", entry.Availability)
	}
	if entry.Description == "" {
		t.Error("Description is empty (model gets no guidance)")
	}
}

func TestRegisterPay_SchemaIsValidJSON(t *testing.T) {
	r := NewRegistry()
	if err := RegisterPay(r); err != nil {
		t.Fatalf("RegisterPay: %v", err)
	}
	entry, _ := r.Lookup("pay")
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
	if !requiredSet["recipient"] {
		t.Error("Schema required missing 'recipient'")
	}
	if !requiredSet["amount"] {
		t.Error("Schema required missing 'amount'")
	}
	if requiredSet["for"] {
		t.Error("Schema required includes 'for' (should be optional)")
	}
	addl, ok := doc["additionalProperties"].(bool)
	if !ok || addl {
		t.Error("Schema must set additionalProperties: false")
	}
}

func TestRegisterPay_DecodeWiredThrough(t *testing.T) {
	r := NewRegistry()
	if err := RegisterPay(r); err != nil {
		t.Fatalf("RegisterPay: %v", err)
	}
	entry, _ := r.Lookup("pay")
	if entry.Decode == nil {
		t.Fatal("Decode is nil")
	}
	args, err := entry.Decode(json.RawMessage(`{"recipient":"Ezekiel","amount":3}`))
	if err != nil {
		t.Fatalf("entry.Decode: %v", err)
	}
	a, ok := args.(PayArgs)
	if !ok || a.Recipient != "Ezekiel" || a.Amount != 3 {
		t.Errorf("decoded = %+v, want PayArgs{Recipient:Ezekiel, Amount:3}", args)
	}
}

func TestRegisterPay_CommitFnWiredThrough(t *testing.T) {
	r := NewRegistry()
	if err := RegisterPay(r); err != nil {
		t.Fatalf("RegisterPay: %v", err)
	}
	entry, _ := r.Lookup("pay")
	if entry.Commit() == nil {
		t.Error("Commit accessor returned nil for pay entry")
	}
}

func TestRegisterPay_AdvertisedSpec(t *testing.T) {
	r := NewRegistry()
	if err := RegisterPay(r); err != nil {
		t.Fatalf("RegisterPay: %v", err)
	}
	specs := r.AdvertisedSpecs()
	if len(specs) != 1 {
		t.Fatalf("AdvertisedSpecs len = %d, want 1", len(specs))
	}
	if specs[0].Name != "pay" {
		t.Errorf("AdvertisedSpec.Name = %q, want pay", specs[0].Name)
	}
	if specs[0].Description == "" {
		t.Error("AdvertisedSpec.Description is empty")
	}
}

func TestRegisterPay_RejectsDuplicate(t *testing.T) {
	r := NewRegistry()
	if err := RegisterPay(r); err != nil {
		t.Fatalf("RegisterPay 1st: %v", err)
	}
	if err := RegisterPay(r); err == nil {
		t.Error("RegisterPay 2nd: expected error for duplicate registration")
	}
}
