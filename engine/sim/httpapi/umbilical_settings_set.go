package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/jeffdafoe/llm-memory-plugin-salem-1692/engine/sim"
)

// umbilical_settings_set.go — LLM-577. The generic world-setting write.
//
// Before this there were eleven bespoke setter routes covering ~15 of the 110
// keys the loader reads, and the coverage grew only when someone building a
// feature happened to add one. The result was a surface with obvious holes: the
// entire visitor_* family was readable and unwritable, and world_dawn_time /
// world_dusk_time were neither — sundown could only be discovered by reading the
// setting table over SSH.
//
// One route driven by the sim settings registry replaces the accretion: every
// registered key is writable here, validated by the same spec that formats it
// for the read side and for the checkpoint. The pre-existing typed routes stay —
// they carry cross-field validation this one deliberately does not attempt (the
// stall-wear route rejects a degrade threshold below the repair threshold, the
// zoom route requires a finite positive floor), and they remain the right way to
// move several coupled knobs in one command.

// umbilicalSettingSetRequest is the body of POST /api/village/umbilical/settings/set.
//
// value is a STRING in the setting table's own encoding, whatever the key's
// underlying type: "570" for an int, "true"/"false" for a bool, "19:00" for a
// time-of-day, and a scalar count for a duration whose unit comes from the key
// suffix ("30" for a _seconds key is thirty seconds). Taking it as a string
// keeps one request shape across five value kinds and matches what is actually
// stored; the registry parses and range-checks it per key.
type umbilicalSettingSetRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// umbilicalSettingSetResponse echoes the applied value as it will be stored,
// plus the metadata a caller needs to know what the write actually bought.
//
// takes_effect is the honest part. "immediately" means the consuming code was
// read and shown to take the value off world.Settings at the point of use.
// "on_restart" means it was shown to read the value once at startup to size a
// ticker or a worker pool — the write lands and persists, but nothing changes
// until the process restarts. "unaudited" means nobody has traced the consumer;
// the write still lands and persists, but whether the running village picks it
// up is unknown. Reporting that third state beats defaulting to "immediately",
// which would hide the one failure an operator cannot see from the outside: a
// 200, a changed read-back, and an engine still using the old value.
type umbilicalSettingSetResponse struct {
	Key         string `json:"key"`
	Kind        string `json:"kind"`
	Value       string `json:"value"`
	TakesEffect string `json:"takes_effect"`
	// Persisted reports whether this key is one the checkpoint writes back to
	// the setting table — i.e. whether the change is meant to survive a restart.
	//
	// It is EVENTUAL, not confirmed. The 200 means the value is in live memory;
	// durability lands on the next checkpoint (default 60s). A checkpoint that
	// then fails does not retract this response — GET /umbilical/checkpoint-health
	// is where broken durability shows up (consecutive_failures, last_error), and
	// a failing checkpointer also raises the durability alarm stamped onto every
	// umbilical response. So: 200 here plus a healthy checkpoint means the change
	// will survive; 200 alone does not.
	Persisted bool `json:"persisted"`
}

// handleUmbilicalSettingSet applies one world setting by key. Operator-gated +
// audited like the rest of the umbilical control surface.
//
// Runs on the world goroutine: WorldSettings is world-goroutine-owned state and
// the per-tick readers touch it without a lock, so an off-goroutine write would
// race them.
func (s *Server) handleUmbilicalSettingSet(w http.ResponseWriter, r *http.Request) {
	user := userFromContext(r.Context())
	if user == nil {
		writeAuthError(w, "invalid")
		return
	}
	var req umbilicalSettingSetRequest
	if !decodeUmbilicalBody(w, r, &req) {
		return
	}
	if req.Key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}
	auditUmbilical(user.Username, "settings.set", req.Key)

	res, err := s.world.SendContext(r.Context(), sim.Command{Fn: func(world *sim.World) (any, error) {
		spec, err := sim.ApplySetting(&world.Settings, req.Key, req.Value)
		if err != nil {
			return nil, err
		}
		return umbilicalSettingSetResponse{
			Key:         spec.Key,
			Kind:        string(spec.Kind),
			Value:       spec.Read(&world.Settings),
			TakesEffect: string(spec.Effect),
			Persisted:   spec.Persist,
		}, nil
	}})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		// Both an unknown key and a malformed value are caller errors, and the
		// registry's message already names the key and what it wanted, so it is
		// passed through verbatim rather than flattened to a generic 400 body.
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out, ok := res.(umbilicalSettingSetResponse)
	if !ok {
		writeError(w, http.StatusInternalServerError, "unexpected settings-set result")
		return
	}
	writeJSON(w, out)
}
