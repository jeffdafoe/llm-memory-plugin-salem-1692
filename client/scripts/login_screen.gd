extends Control
## Login screen — shown when the user is not authenticated.
## Provides username/password fields and a login button.

@onready var username_field: LineEdit = $Panel/VBox/UsernameField
@onready var password_field: LineEdit = $Panel/VBox/PasswordField
@onready var login_button: Button = $Panel/VBox/LoginButton
@onready var error_label: Label = $Panel/VBox/ErrorLabel
@onready var loading_label: Label = $Panel/VBox/LoadingLabel
@onready var separator: HSeparator = $Panel/VBox/Separator
@onready var background: ColorRect = $Background
@onready var panel: PanelContainer = $Panel

func _ready() -> void:
    login_button.pressed.connect(_on_login_pressed)
    password_field.text_submitted.connect(func(_text): _on_login_pressed())
    username_field.text_submitted.connect(func(_text): password_field.grab_focus())
    error_label.text = ""

    # Focus the username field
    username_field.grab_focus()

## Set a message on the error_label (used when re-showing the screen after
## a mid-session 401). Empty string clears it.
func set_message(message: String) -> void:
    error_label.text = message

## Hide just the login form, keep the dark background AND the title-
## panel visible with a "Loading…" subtitle in place of the form
## fields. Used by main.gd's curtain pattern (ZBBS-HOME-210, refined
## ZBBS-HOME-216) so the auth → world-rendered window shows a styled
## period-appropriate splash instead of bare dim. Symmetric show_form
## re-displays the form for the session-expired path. Called instead
## of `visible = false` on the whole control until the world is fully
## populated, at which point main.gd fades the entire login_screen
## out via modulate tween.
func hide_form() -> void:
    username_field.visible = false
    password_field.visible = false
    login_button.visible = false
    error_label.visible = false
    separator.visible = false
    loading_label.visible = true

func show_form() -> void:
    username_field.visible = true
    password_field.visible = true
    login_button.visible = true
    error_label.visible = true
    separator.visible = true
    loading_label.visible = false

func _on_login_pressed() -> void:
    var user: String = username_field.text.strip_edges()
    var password: String = password_field.text

    if user == "" or password == "":
        error_label.text = "Username and password required"
        return

    error_label.text = ""
    login_button.disabled = true
    login_button.text = "Logging in..."

    # Listen for auth result
    if not Auth.auth_ready.is_connected(_on_auth_result):
        Auth.auth_ready.connect(_on_auth_result, CONNECT_ONE_SHOT)

    Auth.login(user, password)

func _on_auth_result() -> void:
    login_button.disabled = false
    login_button.text = "Enter"

    if Auth.authenticated:
        # Login succeeded — hide just the form. main.gd keeps the
        # dark Background visible until the world is fully rendered,
        # then fades the whole control out (ZBBS-HOME-210).
        hide_form()
    else:
        error_label.text = "Invalid username or password"
        password_field.text = ""
        password_field.grab_focus()
