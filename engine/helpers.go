package main

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// jsonResponse writes a JSON response with the given status code.
func jsonResponse(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if data != nil {
		json.NewEncoder(w).Encode(data)
	}
}

// jsonError writes a JSON error response.
func jsonError(w http.ResponseWriter, message string, status int) {
	jsonResponse(w, status, map[string]string{"error": message})
}

// coerceIntInput tolerates the float64/int/string mix that LLM tool
// providers serve up for numeric arguments. Returns 0 on any failure
// (caller decides whether 0 is meaningful or should be rejected).
// Fractional floats truncate to their integer part — callers that need
// strict whole-number handling (the pay amount, where 1.9 silently
// becoming 1 is a money bug) should keep their existing manual switch.
func coerceIntInput(v interface{}) int {
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0
		}
		return int(math.Trunc(x))
	case int:
		return x
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		if err != nil {
			return 0
		}
		return n
	}
	return 0
}

// coerceInt64Input is the int64 sibling of coerceIntInput, intended
// for ID-typed tool inputs (e.g. pay_ledger row ids on in_response_to)
// where the platform-dependent `int` from coerceIntInput is the wrong
// shape and float64 precision past 2^53 would silently truncate.
// Rejects out-of-safe-integer-range floats so a bogus huge value
// becomes 0 (treated as "no parent" downstream) rather than a wrapped
// fake row id. Returns 0 on any unparseable / non-integer input.
func coerceInt64Input(v interface{}) int64 {
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return 0
		}
		// Reject non-integral floats. ID fields shouldn't truncate
		// `1.9` into `1` — that turns a malformed input into a
		// syntactically-valid row id which downstream lookups would
		// "successfully" resolve. Returning 0 here lets the caller's
		// "no parent" branch fire instead.
		if x != math.Trunc(x) {
			return 0
		}
		// 2^53 is the largest integer float64 represents exactly.
		// Past this, conversion-via-float64 loses precision.
		if x > 9007199254740992 || x < -9007199254740992 {
			return 0
		}
		return int64(x)
	case int:
		return int64(x)
	case int64:
		return x
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		if err != nil {
			return 0
		}
		return n
	}
	return 0
}

// newUUIDv7 generates a UUID v7 (time-ordered) as a string.
// UUID v7 uses a 48-bit Unix timestamp in milliseconds plus random bits.
func newUUIDv7() string {
	var uuid [16]byte

	// 48-bit timestamp in milliseconds
	ms := uint64(time.Now().UnixMilli())
	uuid[0] = byte(ms >> 40)
	uuid[1] = byte(ms >> 32)
	uuid[2] = byte(ms >> 24)
	uuid[3] = byte(ms >> 16)
	uuid[4] = byte(ms >> 8)
	uuid[5] = byte(ms)

	// Fill remaining bytes with random data
	rand.Read(uuid[6:])

	// Set version (7) and variant (RFC 4122)
	uuid[6] = (uuid[6] & 0x0F) | 0x70 // version 7
	uuid[8] = (uuid[8] & 0x3F) | 0x80 // variant RFC 4122

	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		binary.BigEndian.Uint32(uuid[0:4]),
		binary.BigEndian.Uint16(uuid[4:6]),
		binary.BigEndian.Uint16(uuid[6:8]),
		binary.BigEndian.Uint16(uuid[8:10]),
		uuid[10:16],
	)
}
