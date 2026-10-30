extends Sprite2D
## A well's bucket: one per well, shared by everyone drinking there. The first
## drinker hauls it up out of the shaft; it hangs at the mouth under the rope
## while anyone drinks, and goes back down the shaft after the last one.
## world.gd keeps the count of drinkers and calls raise() and lower(); the bucket
## also checks that count itself while it is up, so a drinker the client lost
## track of (removed, re-rendered) never leaves it up for good.
##
## Places are in the well sprite's own pixels, measured against the Mana Seed
## well (the framed and the roofed well share the base).

const SHEET := "/tilesets/mana-seed/village-accessories/village accessories 16x16.png"
## The full bucket on the sheet.
const REGION := Rect2(144, 32, 16, 16)
## Hanging on the rope in the mouth of the shaft, low enough that a drinker's
## head at the rim meets its bottom rather than sitting under it (Jeff).
const MOUTH := Vector2(16, 40)
## The shaft and the rim stones round it: a drinker kneels with their face on
## its edge (world.gd _well_aim), at the front rim just under the bucket.
const LIP := Rect2(14, 38, 20, 18)
## How far down the shaft the bucket comes up from, and goes back down to.
const DEPTH := 6.0
## Seconds the hauler pulls the bucket up to the mouth.
const HAUL := 0.9
const LOWER := 0.5
## Seconds between a raised bucket's checks for drinkers.
const CHECK := 0.5

enum State {DOWN, RISING, UP, SINKING}

var state := State.DOWN
var tween: Tween = null
## Returns how many are drinking at this well.
var drinkers := Callable()
## The well's art: where the bucket's places are measured from.
var _art: Node2D = null
var _check: Timer = null


## Whether node is a well: it carries a pail to draw water with (the engine's
## water carry row).
static func is_well(node: Node2D) -> bool:
    var rows = node.get_meta("refreshes", [])
    if not (rows is Array):
        return false
    for row in rows:
        if row is Dictionary and str(row.get("gather_item", "")) == "water":
            return true
    return false


## The bucket of well, made the first time it is asked for. null when the well
## has no art to stand the bucket on, or is not on the map (its tweens and its
## checks run on the tree).
static func of(well: Node2D, counter: Callable) -> Node:
    if not well.is_inside_tree():
        return null
    var have := well.get_node_or_null("WellBucket")
    if have != null:
        return have
    var art := _art_of(well)
    if art == null:
        return null
    var bucket = load("res://scripts/well_bucket.gd").new()
    bucket.name = "WellBucket"
    bucket._art = art
    bucket.drinkers = counter
    bucket.centered = false
    bucket.region_enabled = true
    bucket.region_rect = REGION
    bucket.scale = art.scale
    bucket.visible = false
    well.add_child(bucket)
    return bucket


## Haul the bucket up, from the bottom of the shaft or from wherever it has sunk
## to. Returns true when this starts a haul — the drinker who called it pulls the
## rope for HAUL seconds — and false when the bucket is already up or coming up.
func raise() -> bool:
    if state == State.RISING or state == State.UP:
        return false
    if state == State.DOWN:
        position = _at(MOUTH + Vector2(0, DEPTH))
        modulate.a = 0.0
    state = State.RISING
    visible = true
    _start_checks()
    _new_tween()
    tween.tween_property(self, "position", _at(MOUTH), HAUL)
    tween.parallel().tween_property(self, "modulate:a", 1.0, HAUL)
    tween.tween_callback(func() -> void: state = State.UP)
    return true


## Send the bucket back down the shaft, unless it is already going or gone.
func lower() -> void:
    if state == State.DOWN or state == State.SINKING:
        return
    state = State.SINKING
    _new_tween()
    tween.tween_property(self, "position", _at(MOUTH + Vector2(0, DEPTH)), LOWER)
    tween.parallel().tween_property(self, "modulate:a", 0.0, LOWER)
    tween.tween_callback(func() -> void:
        state = State.DOWN
        visible = false
        if _check != null:
            _check.stop())


## Lower the bucket once nobody is drinking at the well.
func check_drinkers() -> void:
    if (state == State.RISING or state == State.UP) and drinkers.is_valid() and int(drinkers.call()) == 0:
        lower()


## The well's LIP in the coordinates the well is placed in (where actors
## stand), or an empty Rect2 when the well has no art.
static func lip_rect(well: Node2D) -> Rect2:
    var art := _art_of(well)
    if art == null:
        return Rect2()
    return Rect2(well.position + art.position + LIP.position * art.scale, LIP.size * art.scale)


## The well's art: its first sprite, never the bucket drawn over it.
static func _art_of(well: Node2D) -> Node2D:
    for child in well.get_children():
        if (child is Sprite2D or child is AnimatedSprite2D) and child.name != "WellBucket":
            return child
    return null


func set_sheet(texture: Texture2D) -> void:
    self.texture = texture


## A place on the well's art, in the well's coordinates.
func _at(p: Vector2) -> Vector2:
    return _art.position + p * _art.scale


func _new_tween() -> void:
    if tween != null and tween.is_valid():
        tween.kill()
    tween = create_tween()


func _start_checks() -> void:
    if _check == null:
        _check = Timer.new()
        _check.wait_time = CHECK
        _check.timeout.connect(check_drinkers)
        add_child(_check)
    _check.start()
