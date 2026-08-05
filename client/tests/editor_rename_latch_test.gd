extends SceneTree

## Headless regression harness for the LLM-587 rename double-post latch
## (client/scripts/editor_panel.gd). Both editor name inputs — the village-object
## name input and the NPC name edit — connect text_submitted AND focus_exited,
## and the submit handlers call release_focus(), which fires focus_exited
## synchronously. Without the _name_last_saved / _npc_name_last_saved latch
## every Enter rename posted twice, and a rejected rename raised two identical
## error toasts.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/editor_rename_latch_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## The panel script is instantiated off-tree via .new() so _ready() never fires
## and none of the sidebar UI is built. Each test wires a real LineEdit into the
## tree, mirrors the production signal connections, and grabs focus before
## submitting — so release_focus() inside the submit handler re-enters the focus
## handler exactly the way a live Enter press does. Every Enter test asserts the
## field held focus beforehand: if headless focus ever stopped working, the suite
## fails loudly instead of passing without exercising the re-entry.
##
## The populate helpers mirror what show_selection / show_npc_selection assign
## (text, object id, latch) rather than calling them — those functions touch the
## whole sidebar UI, which .new() never builds.

const TESTS := [
    "_test_object_enter_posts_once",
    "_test_npc_enter_posts_once",
    "_test_object_click_away_saves_edit",
    "_test_object_click_away_unchanged_skips",
    "_test_object_whitespace_populate_click_away_skips",
    "_test_npc_empty_submit_saves_nothing",
    "_test_npc_enter_retries_after_reject",
]

var _panel: PanelContainer = null
var _line: LineEdit = null
var _emits: Array = []
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


# The suite runs on the first process_frame, not in _initialize: focus needs the
# LineEdit inside the live tree, and root only enters the tree when the main
# loop starts — grab_focus during _initialize errors with !is_inside_tree().
func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    _check_test_list()
    _run_all()
    _check_all_tests_ran()
    print("\n[editor_rename_latch_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[editor_rename_latch_test] ALL PASS")
    quit(1 if _failures > 0 else 0)


func _teardown() -> void:
    if _line != null:
        root.remove_child(_line)
        _line.free()
        _line = null
    if _panel != null:
        _panel.free()
        _panel = null
    _emits = []


## Fresh panel + in-tree LineEdit wired as the village-object name input,
## in the state show_selection leaves after populating.
func _fresh_object_field(populated: String, object_id: String) -> void:
    _teardown()
    _panel = load("res://scripts/editor_panel.gd").new()
    _line = LineEdit.new()
    root.add_child(_line)
    _panel._name_input = _line
    _line.text_submitted.connect(_panel._on_name_submitted)
    _line.focus_exited.connect(_panel._on_name_focus_lost)
    _panel.display_name_changed.connect(func(display_name, id): _emits.append([display_name, id]))
    _line.text = populated
    _panel._name_input_object_id = object_id
    _panel._name_last_saved = populated.strip_edges()


## Fresh panel + in-tree LineEdit wired as the NPC name edit, in the state
## show_npc_selection leaves after populating.
func _fresh_npc_field(populated: String) -> void:
    _teardown()
    _panel = load("res://scripts/editor_panel.gd").new()
    _line = LineEdit.new()
    root.add_child(_line)
    _panel._npc_name_edit = _line
    _line.text_submitted.connect(_panel._on_npc_name_submitted)
    _line.focus_exited.connect(_panel._on_npc_name_focus_lost)
    _panel.npc_name_changed.connect(func(display_name): _emits.append(display_name))
    _line.text = populated
    _panel._npc_name_last_saved = populated.strip_edges()


## Simulate the user pressing Enter: the LineEdit emits text_submitted while
## holding focus, so the submit handler's release_focus() genuinely re-enters
## the focus handler.
func _press_enter(text: String) -> void:
    _line.grab_focus()
    _check("precondition — field holds focus before Enter", _line.has_focus(), true)
    _line.text = text
    _line.text_submitted.emit(text)
    _check("Enter released focus", _line.has_focus(), false)


## Simulate the user clicking away without pressing Enter.
func _click_away(text: String) -> void:
    _line.grab_focus()
    _check("precondition — field holds focus before click-away", _line.has_focus(), true)
    _line.text = text
    _line.release_focus()


func _check(label: String, got, want) -> void:
    _checks += 1
    if got == want:
        return
    _failures += 1
    printerr("[FAIL] %s: got %s, want %s" % [label, got, want])


func _run_all() -> void:
    for t in TESTS:
        _current = t
        call(t)
    _teardown()


## LLM-480 harness convention: a GDScript runtime error aborts ONLY the function
## it happens in, so every test calls _done() as its last statement and the
## harness asserts each one ran to completion.
func _done() -> void:
    _completed[_current] = true


func _check_all_tests_ran() -> void:
    for t in TESTS:
        _check("harness — %s ran to completion" % t, _completed.has(t), true)


func _check_test_list() -> void:
    var listed := {}
    for t in TESTS:
        _check("harness — %s listed only once" % t, not listed.has(t), true)
        _check("harness — %s exists" % t, has_method(t), true)
        listed[t] = true
    for m in get_method_list():
        var name: String = m["name"]
        if name.begins_with("_test_"):
            _check("harness — %s is registered in TESTS" % name, listed.has(name), true)


## The bug itself: Enter on the object name input must save exactly once, and
## trimmed. Before the latch this produced two emits (submit + re-entered focus).
func _test_object_enter_posts_once() -> void:
    _fresh_object_field("Barn", "obj-1")
    _press_enter("  Red Barn  ")
    _check("object Enter emits once", _emits.size(), 1)
    if _emits.size() == 1:
        _check("object Enter emits trimmed name + id", _emits[0], ["Red Barn", "obj-1"])
    _done()


## Same bug on the NPC path.
func _test_npc_enter_posts_once() -> void:
    _fresh_npc_field("Josiah")
    _press_enter("Gideon")
    _check("npc Enter emits once", _emits.size(), 1)
    if _emits.size() == 1:
        _check("npc Enter emits the new name", _emits[0], "Gideon")
    _done()


## The focus-exit path must still save on its own — clicking away after an
## edit, without pressing Enter, is a legitimate save.
func _test_object_click_away_saves_edit() -> void:
    _fresh_object_field("Barn", "obj-1")
    _click_away("Stable")
    _check("object click-away saves the edit", _emits.size(), 1)
    if _emits.size() == 1:
        _check("object click-away emits new name + id", _emits[0], ["Stable", "obj-1"])
    _done()


## Clicking in and away without editing must not post — the server already has
## this name.
func _test_object_click_away_unchanged_skips() -> void:
    _fresh_object_field("Barn", "obj-1")
    _click_away("Barn")
    _check("object unchanged click-away emits nothing", _emits.size(), 0)
    _done()


## A server value with surrounding whitespace must not read as an edit: the
## latch is trimmed at populate because the handlers compare trimmed input.
func _test_object_whitespace_populate_click_away_skips() -> void:
    _fresh_object_field("  Barn  ", "obj-1")
    _click_away("  Barn  ")
    _check("object whitespace-padded populate does not phantom-save", _emits.size(), 0)
    _done()


## The NPC path refuses empty names: an empty Enter emits nothing, must not
## poison the latch, and the old name coming back on click-away is not an edit.
func _test_npc_empty_submit_saves_nothing() -> void:
    _fresh_npc_field("Josiah")
    _press_enter("   ")
    _check("npc empty Enter emits nothing", _emits.size(), 0)
    _click_away("Josiah")
    _check("npc restored name after empty Enter is not an edit", _emits.size(), 0)
    _click_away("Gideon")
    _check("npc real edit after empty Enter still saves", _emits.size(), 1)
    _done()


## After a rejected rename the field still shows the rejected value. Clicking
## away must not re-post it (that doubled the error toast), but pressing Enter
## again is the deliberate retry and must emit.
func _test_npc_enter_retries_after_reject() -> void:
    _fresh_npc_field("Josiah")
    _press_enter("Taken Name")
    _check("npc first Enter emits", _emits.size(), 1)
    _click_away("Taken Name")
    _check("npc click-away after reject does not re-post", _emits.size(), 1)
    _press_enter("Taken Name")
    _check("npc Enter again retries the rejected name", _emits.size(), 2)
    _done()
