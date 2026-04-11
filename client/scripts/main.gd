extends Node2D
## Main scene — handles auth flow and bootstraps the village viewer.

@onready var world: Node2D = $World
@onready var camera: Camera2D = $Camera

# Login screen (added as a CanvasLayer so it renders on top of everything)
var login_screen: Control = null

func _ready() -> void:
    # Always generate terrain — it's visible behind the login screen
    world.build_terrain()

    # Set camera bounds to match the terrain (2x scaled = 32px per tile)
    camera.map_bounds = Rect2(0, 0, world.map_width * 32, world.map_height * 32)
    camera.position = Vector2(
        world.map_width * 32 / 2.0,
        world.map_height * 32 / 2.0
    )

    # Show login screen while checking auth
    var login_scene = load("res://scenes/login_screen.tscn")
    var login_layer = CanvasLayer.new()
    login_layer.name = "LoginLayer"
    login_screen = login_scene.instantiate()
    login_layer.add_child(login_screen)
    add_child(login_layer)

    # Wait for auth check to complete
    if Auth.authenticated:
        _on_authenticated()
    else:
        Auth.auth_ready.connect(_on_auth_ready)

func _on_auth_ready() -> void:
    if Auth.authenticated:
        _on_authenticated()
    # If not authenticated, the login screen is already visible

func _on_authenticated() -> void:
    # Hide login screen
    if login_screen != null:
        login_screen.visible = false

    # Connect for future login events (in case token was saved)
    if not Auth.logged_in.is_connected(_on_authenticated):
        Auth.logged_in.connect(_on_authenticated)

    # Load objects now that we're authenticated
    if Catalog.loaded:
        world.load_objects()
    else:
        Catalog.catalog_loaded.connect(world.load_objects)
