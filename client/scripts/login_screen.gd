extends Control
## Login screen — shown when the user is not authenticated.
## Provides username/password fields and a login button.

@onready var username_field: LineEdit = $Panel/VBox/UsernameField
@onready var password_field: LineEdit = $Panel/VBox/PasswordField
@onready var login_button: Button = $Panel/VBox/LoginButton
@onready var error_label: Label = $Panel/VBox/ErrorLabel

func _ready() -> void:
    login_button.pressed.connect(_on_login_pressed)
    password_field.text_submitted.connect(func(_text): _on_login_pressed())
    username_field.text_submitted.connect(func(_text): password_field.grab_focus())
    error_label.text = ""

    # Focus the username field
    username_field.grab_focus()

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
        # Login succeeded — hide this screen
        visible = false
    else:
        error_label.text = "Invalid username or password"
        password_field.text = ""
        password_field.grab_focus()
