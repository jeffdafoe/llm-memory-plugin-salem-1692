extends Node
## Autoloaded singleton — handles authentication with the llm-memory API.
## Stores session token in browser localStorage.

signal auth_ready        # Emitted when auth check completes (logged in or not)
signal logged_in         # Emitted on successful login
signal session_expired   # Emitted when the server rejects our token with 401 mid-session

# Current auth state
var authenticated: bool = false
var session_token: String = ""
var username: String = ""
var can_edit: bool = false

# API base URL
var api_base: String = ""

func _ready() -> void:
    if OS.has_feature("web"):
        api_base = JavaScriptBridge.eval("window.location.origin", true)
    else:
        api_base = "http://zbbs.local"

    # Check for saved session token
    var saved_token: String = _load_token()
    if saved_token != "":
        session_token = saved_token
        _verify_token()
    else:
        auth_ready.emit()

## Try to log in with username and password via llm-memory admin login.
func login(user: String, password: String) -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)

    var payload = JSON.stringify({
        "username": user,
        "password": password,
    })

    var url: String = api_base + "/llm/admin/login"

    http.request_completed.connect(_on_login_response.bind(http))
    var headers = ["Content-Type: application/json"]
    var err = http.request(url, headers, HTTPClient.METHOD_POST, payload)
    if err != OK:
        push_error("Auth: request() returned error " + str(err))

func _on_login_response(result: int, response_code: int, headers: PackedStringArray, body: PackedByteArray, http: HTTPRequest) -> void:
    http.queue_free()

    var body_text: String = body.get_string_from_utf8()

    if result != HTTPRequest.RESULT_SUCCESS or response_code != 200:
        push_error("Login failed: result=" + str(result) + " code=" + str(response_code) + " body=" + body_text)
        # Emit auth_ready so the login screen can reset
        authenticated = false
        auth_ready.emit()
        return

    var json = JSON.parse_string(body_text)
    if json == null or not json.has("session_token"):
        push_error("Login response missing session_token: " + body_text)
        authenticated = false
        auth_ready.emit()
        return

    session_token = json["session_token"]
    _save_token(session_token)

    # Now verify the token to get user info
    _verify_token()

## Verify the stored token by calling the Go engine's /api/me endpoint.
func _verify_token() -> void:
    var http = HTTPRequest.new()
    http.accept_gzip = false
    add_child(http)

    http.request_completed.connect(_on_verify_response.bind(http))
    var headers = ["Authorization: Bearer " + session_token]
    http.request(api_base + "/api/me", headers)

func _on_verify_response(result: int, response_code: int, headers: PackedStringArray, body: PackedByteArray, http: HTTPRequest) -> void:
    http.queue_free()

    if result != HTTPRequest.RESULT_SUCCESS or response_code != 200:
        # Token invalid or expired — clear it
        session_token = ""
        _clear_token()
        authenticated = false
        auth_ready.emit()
        return

    var json = JSON.parse_string(body.get_string_from_utf8())
    if json == null:
        authenticated = false
        auth_ready.emit()
        return

    authenticated = true
    username = json.get("agent", "")
    can_edit = json.get("can_edit", false)
    auth_ready.emit()
    logged_in.emit()

## Log out — clear token and state.
func logout() -> void:
    session_token = ""
    authenticated = false
    username = ""
    can_edit = false
    _clear_token()

## Returns true when a session token is present. Use this for "are we
## authenticated?" checks instead of comparing get_auth_token() to "".
func is_authenticated() -> bool:
    return session_token != ""

## Returns the bare session token (no "Bearer " prefix). Most callers
## should use auth_headers() instead — bare-token access is for the
## rare case (e.g. embedding into a WebSocket URL query param) where
## the value can't ride in an HTTP header.
func get_auth_token() -> String:
    return session_token

## Returns the headers array most API calls need: a Content-Type for
## JSON bodies (toggleable for GETs that don't post anything) and a
## Bearer Authorization line. Single source of truth — call sites that
## hand-rolled "Authorization: " + get_auth_header() were misusing the
## old API in ways the type system couldn't catch (the old function
## returned the value, not the header line).
func auth_headers(include_content_type: bool = true) -> PackedStringArray:
    var headers := PackedStringArray()
    if include_content_type:
        headers.append("Content-Type: application/json")
    if session_token != "":
        headers.append("Authorization: Bearer " + session_token)
    return headers

## Called by HTTP callbacks when the server returns 401. Clears the dead
## token and emits session_expired so the UI can re-show the login screen.
## Idempotent — safe to call from multiple concurrent in-flight requests.
func notify_session_expired() -> void:
    if session_token == "":
        return  # Already cleared — subsequent 401s are noise
    session_token = ""
    authenticated = false
    username = ""
    can_edit = false
    _clear_token()
    session_expired.emit()

## Helper for HTTP callbacks. Returns true if the response was authed, false
## on 401 (after notifying). Callers use this to short-circuit their success
## path when auth fails.
func check_response(response_code: int) -> bool:
    if response_code == 401:
        notify_session_expired()
        return false
    return true

# --- localStorage helpers (browser only) ---

func _save_token(token: String) -> void:
    if OS.has_feature("web"):
        JavaScriptBridge.eval("localStorage.setItem('salem_session_token', '%s')" % token)

func _load_token() -> String:
    if OS.has_feature("web"):
        var val = JavaScriptBridge.eval("localStorage.getItem('salem_session_token') || ''", true)
        if val is String:
            return val
    return ""

func _clear_token() -> void:
    if OS.has_feature("web"):
        JavaScriptBridge.eval("localStorage.removeItem('salem_session_token')")
