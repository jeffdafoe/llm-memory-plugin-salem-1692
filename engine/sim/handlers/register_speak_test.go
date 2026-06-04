package handlers

import (
	"encoding/json"
	"testing"
)

// register_speak_test.go — verifies the RegisterSpeak helper produces a
// registry entry with the expected shape: ClassCommit,
// AvailabilityAvailable, terminal-on-success FALSE, schema bytes are
// valid JSON, decode + handler wired through.

func TestRegisterSpeak_AddsAvailableCommitEntry(t *testing.T) {
	r := NewRegistry()
	if err := RegisterSpeak(r); err != nil {
		t.Fatalf("RegisterSpeak: %v", err)
	}
	entry, ok := r.Lookup("speak")
	if !ok {
		t.Fatal("Lookup(speak): not found after RegisterSpeak")
	}
	if entry.Class != ClassCommit {
		t.Errorf("Class = %v, want ClassCommit", entry.Class)
	}
	if entry.TerminalPolicy != TerminalNever {
		t.Errorf("TerminalPolicy = %v, want TerminalNever (speak is non-terminal)", entry.TerminalPolicy)
	}
	if entry.Availability != AvailabilityAvailable {
		t.Errorf("Availability = %v, want AvailabilityAvailable", entry.Availability)
	}
	if entry.Description == "" {
		t.Error("Description is empty (model gets no guidance)")
	}
}

func TestRegisterSpeak_SchemaIsValidJSON(t *testing.T) {
	r := NewRegistry()
	if err := RegisterSpeak(r); err != nil {
		t.Fatalf("RegisterSpeak: %v", err)
	}
	entry, _ := r.Lookup("speak")
	if !json.Valid(entry.Schema) {
		t.Fatal("Schema bytes are not valid JSON")
	}
	// Schema must declare text required.
	var doc map[string]any
	if err := json.Unmarshal(entry.Schema, &doc); err != nil {
		t.Fatalf("Schema unmarshal: %v", err)
	}
	req, _ := doc["required"].([]any)
	found := false
	for _, r := range req {
		if r == "text" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Schema required does not include 'text'")
	}
	addl, ok := doc["additionalProperties"].(bool)
	if !ok || addl {
		t.Error("Schema must set additionalProperties: false")
	}
}

func TestRegisterSpeak_DecodeWiredThrough(t *testing.T) {
	r := NewRegistry()
	if err := RegisterSpeak(r); err != nil {
		t.Fatalf("RegisterSpeak: %v", err)
	}
	entry, _ := r.Lookup("speak")
	if entry.Decode == nil {
		t.Fatal("Decode is nil")
	}
	// Smoke test the wiring — full decode coverage lives in speak_test.go.
	args, err := entry.Decode(json.RawMessage(`{"text":"hi"}`))
	if err != nil {
		t.Fatalf("entry.Decode: %v", err)
	}
	if a, ok := args.(SpeakArgs); !ok || a.Text != "hi" {
		t.Errorf("decoded = %+v, want SpeakArgs{Text:hi}", args)
	}
}

func TestRegisterSpeak_CommitFnWiredThrough(t *testing.T) {
	r := NewRegistry()
	if err := RegisterSpeak(r); err != nil {
		t.Fatalf("RegisterSpeak: %v", err)
	}
	entry, _ := r.Lookup("speak")
	if entry.Commit() == nil {
		t.Error("Commit accessor returned nil for speak entry")
	}
}

func TestRegisterSpeak_AdvertisedSpec(t *testing.T) {
	r := NewRegistry()
	if err := RegisterSpeak(r); err != nil {
		t.Fatalf("RegisterSpeak: %v", err)
	}
	specs := r.AdvertisedSpecs()
	if len(specs) != 1 {
		t.Fatalf("AdvertisedSpecs len = %d, want 1", len(specs))
	}
	if specs[0].Name != "speak" {
		t.Errorf("AdvertisedSpec.Name = %q, want speak", specs[0].Name)
	}
	if specs[0].Description == "" {
		t.Error("AdvertisedSpec.Description is empty")
	}
}

func TestRegisterSpeak_RejectsDuplicate(t *testing.T) {
	r := NewRegistry()
	if err := RegisterSpeak(r); err != nil {
		t.Fatalf("RegisterSpeak 1st: %v", err)
	}
	if err := RegisterSpeak(r); err == nil {
		t.Error("RegisterSpeak 2nd: expected error for duplicate registration")
	}
}
