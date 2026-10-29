extends Node
## Autoloaded singleton — reports client-only events (effects the engine never
## sees, like the figure at the treeline) to the engine's
## POST /api/village/client-event endpoint for statistics (LLM-880). The engine
## stamps the time, the authed user and the player's PC, accepts only kinds on
## its allowlist, and keeps the rows in Postgres for the operator-read
## umbilical /client-events. Kept apart from ErrorBeacon on purpose: errors and
## statistics never share a route.
##
## Fire-and-forget: nothing waits on a report, a failure is swallowed, and the
## player never sees it.

# Light client-side cap so a loop cannot spam the engine with round-trips. The
# engine ALSO rate-limits per user; this just avoids the calls.
const _MAX_PER_WINDOW: int = 20
const _WINDOW_SECONDS: float = 60.0

var _window_start: float = 0.0
var _window_count: int = 0

## Report one client-only event. `kind` must be on the engine's allowlist
## (client_event.go); `detail` is a small object (the engine refuses anything
## over 1 KB). No-op when unauthenticated or when the local cap is hit.
func report(kind: String, detail: Dictionary = {}) -> void:
    if not Auth.is_authenticated():
        return
    if not _allow():
        return
    var http := HTTPRequest.new()
    http.accept_gzip = false
    # HTTPRequest has no timeout by default; without one a request the server
    # never answers keeps its node forever. A timeout fires request_completed,
    # which frees the node.
    http.timeout = 10.0
    add_child(http)
    http.request_completed.connect(func(_result, _code, _headers, _body): http.queue_free())
    var payload := JSON.stringify({"kind": kind, "detail": detail})
    var err := http.request(Auth.api_base + "/api/village/client-event",
        Auth.auth_headers(), HTTPClient.METHOD_POST, payload)
    if err != OK:
        http.queue_free()

# Fixed-window local throttle. Returns true if a report may be sent now.
func _allow() -> bool:
    var now := Time.get_unix_time_from_system()
    if now - _window_start >= _WINDOW_SECONDS:
        _window_start = now
        _window_count = 1
        return true
    if _window_count >= _MAX_PER_WINDOW:
        return false
    _window_count += 1
    return true
