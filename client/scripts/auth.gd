extends Node
## Autoloaded singleton — handles authentication with the llm-memory API.
## Stores session token in browser localStorage.

signal auth_ready  # Emitted when auth check completes (logged in or not)
signal logged_in   # Emitted on successful login

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
    add_child(http)

    var payload = JSON.stringify({
        "username": user,
        "password": password,
    })

    var url: String = api_base + "/llm/admin/login"
    print("Auth: POST " + url + " payload=" + payload)

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

## Get the Authorization header value for API requests.
func get_auth_header() -> String:
    if session_token == "":
        return ""
    return "Bearer " + session_token

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
