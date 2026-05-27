extends Node
## Autoloaded singleton — reports browser-runtime failures the engine and nginx
## can't observe (sprite decode, world_ready stall, WS parse, …) to the engine's
## POST /api/village/client-log endpoint. The engine stamps the authed user +
## source IP server-side and keeps these in a pull-only, operator-read umbilical
## ring (see engine clientlog.go) — an untrusted debug aid, never authoritative.
##
## Fire-and-forget: a failed beacon is swallowed, never re-reported (no recursion).

# Light client-side cap so a tight failure loop can't spam the engine with
# round-trips. The engine ALSO rate-limits per user; this just avoids the calls.
const _MAX_PER_WINDOW: int = 20
const _WINDOW_SECONDS: float = 60.0

var _window_start: float = 0.0
var _window_count: int = 0

## Report a client-runtime failure. `kind` is a short stable slug
## (e.g. "npc_sheet_decode_failed"); `message` is free-form detail. No-op when
## unauthenticated (the endpoint needs a session) or when the local cap is hit.
func report(kind: String, message: String = "") -> void:
    if not Auth.is_authenticated():
        return
    if not _allow():
        return
    var http := HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)
    # Free the request node when it completes; ignore the result — this is a
    # best-effort beacon, and beaconing a beacon failure would risk a loop.
    http.request_completed.connect(func(_result, _code, _headers, _body): http.queue_free())
    var payload := JSON.stringify({"kind": kind, "message": message})
    var err := http.request(Auth.api_base + "/api/village/client-log",
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
