package handlers

import (
	"encoding/json"
	"testing"
)

// stop_test.go — ZBBS-HOME-338. DecodeStopArgs accepts the empty object (and
// absent/null), rejects unknown fields and non-objects.

func TestDecodeStopArgs_AcceptsEmpty(t *testing.T) {
	for _, raw := range []string{`{}`, ``, `null`, `  {}  `} {
		if _, err := DecodeStopArgs(json.RawMessage(raw)); err != nil {
			t.Errorf("DecodeStopArgs(%q) = error %v, want ok", raw, err)
		}
	}
}

func TestDecodeStopArgs_RejectsUnknownAndNonObject(t *testing.T) {
	for _, raw := range []string{`{"qty":1}`, `{"dest":"inn"}`, `5`, `"halt"`, `[]`, `{} {}`, `{} 5`} {
		if _, err := DecodeStopArgs(json.RawMessage(raw)); err == nil {
			t.Errorf("DecodeStopArgs(%q) = ok, want error", raw)
		}
	}
}
