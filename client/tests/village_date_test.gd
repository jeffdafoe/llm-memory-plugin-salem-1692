extends SceneTree

## Headless harness for LLM-840: the village date and season in the top bar
## title, the village hour (LLM-854), the ground sheet for each season
## (LLM-834), and the re-read of the world when that date ends.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/village_date_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## The helpers are static, so most scripts are loaded but never instanced; the
## ground-fetch test instances the terrain renderer outside the tree.

const TESTS := [
    "_test_title_shows_the_date",
    "_test_title_shows_the_season",
    "_test_title_shows_the_hour",
    "_test_village_clock_from_a_read",
    "_test_refresh_at_the_offset_change",
    "_test_title_falls_back_without_a_date",
    "_test_ground_sheet_by_season",
    "_test_ground_fetch_timeout_frees_the_retry",
    "_test_refresh_wait_measures_engine_time",
    "_test_refresh_wait_without_times",
]

var _top_bar: GDScript = null
var _world: GDScript = null
var _terrain: GDScript = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    _top_bar = load("res://scripts/top_bar.gd")
    _world = load("res://scripts/world.gd")
    _terrain = load("res://scripts/terrain_renderer.gd")
    _check_test_list()
    for t in TESTS:
        _current = t
        await call(t)
    _check_all_tests_ran()
    print("\n[village_date_test] %d checks, %d failure(s)" % [_checks, _failures])
    if _failures == 0:
        print("[village_date_test] ALL PASS")
    quit(1 if _failures > 0 else 0)


func _check(label: String, got, want) -> void:
    _checks += 1
    if got == want:
        return
    _failures += 1
    printerr("[FAIL] %s: got %s, want %s" % [label, got, want])


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


func _test_title_shows_the_date() -> void:
    _check("October 22", _top_bar.title_text("1692-10-22"), "Salem — 22 October 1692")
    _check("January 1", _top_bar.title_text("1692-01-01"), "Salem — 1 January 1692")
    _check("December 31", _top_bar.title_text("1692-12-31"), "Salem — 31 December 1692")
    _done()


func _test_title_shows_the_season() -> void:
    _check("autumn", _top_bar.title_text("1692-10-22", "autumn"), "Salem — 22 October 1692 (Autumn)")
    _check("winter", _top_bar.title_text("1692-03-31", "winter"), "Salem — 31 March 1692 (Winter)")
    _check("no season", _top_bar.title_text("1692-10-22", ""), "Salem — 22 October 1692")
    _check("unknown season", _top_bar.title_text("1692-10-22", "monsoon"), "Salem — 22 October 1692")
    _check("no date, season ignored", _top_bar.title_text("", "autumn"), "Salem — 1692")
    _done()


func _test_title_falls_back_without_a_date() -> void:
    for bad in ["", "1692", "1692-13-01", "1692-00-10", "1692-10-00", "1692-10-xx", "not-a-date"]:
        _check("fallback for %s" % bad, _top_bar.title_text(bad), "Salem — 1692")
    _done()


func _test_refresh_wait_measures_engine_time() -> void:
    # Midnight is 90 seconds after the engine's now: wait those plus the margin.
    var wait: float = _world.village_date_refresh_wait("2026-10-23T04:00:00Z", "2026-10-23T03:58:30Z")
    _check("90 s before the end", wait, 90.0 + _world.VILLAGE_DATE_REFRESH_MARGIN_SEC)
    # A date that already ended (a late read) re-reads soon, not never.
    _check("already ended", _world.village_date_refresh_wait("2026-10-23T04:00:00Z", "2026-10-23T04:10:00Z"), 1.0)
    _done()


func _test_refresh_wait_without_times() -> void:
    _check("no end time", _world.village_date_refresh_wait("", "2026-10-23T03:58:30Z"), 0.0)
    _check("no now", _world.village_date_refresh_wait("2026-10-23T04:00:00Z", ""), 0.0)
    _check("offset, not Z", _world.village_date_refresh_wait("2026-10-23T00:00:00-04:00", "2026-10-23T03:58:30Z"), 0.0)
    _done()


func _test_ground_sheet_by_season() -> void:
    _check("autumn", _terrain.season_wang_path("autumn"), "/tilesets/mana-seed/autumn-forest/autumn sheets/autumn forest wang tiles.png")
    _check("winter", _terrain.season_wang_path("winter"), "/tilesets/mana-seed/winter-forest/winter sheets/winter forest wang tiles (clean).png")
    for built_in in ["summer", "spring", "", "monsoon"]:
        _check("built-in for %s" % built_in, _terrain.season_wang_path(built_in), "")
    _done()


# A stalled fetch ends at the request timeout, and its completion clears the
# pending mark, so the next world read can fetch the sheet again (code_review,
# LLM-852). The renderer stays out of the tree: _ready (which loads the
# purchased wang.png) never runs.
func _test_ground_fetch_timeout_frees_the_retry() -> void:
    _check("the fetch has a timeout", _terrain.SEASON_WANG_TIMEOUT_SEC > 0.0, true)
    var renderer: Node2D = _terrain.new()
    var path: String = _terrain.season_wang_path("autumn")
    renderer._season = "autumn"
    renderer._season_wang_pending[path] = true
    var http := HTTPRequest.new()
    renderer._on_season_wang_downloaded(HTTPRequest.RESULT_TIMEOUT, 0, PackedStringArray(), PackedByteArray(), http, path)
    _check("timeout clears pending", renderer._season_wang_pending.has(path), false)
    _check("timeout caches nothing", renderer._season_wang_cache.has(path), false)
    _check("timeout keeps the current ground", renderer.wang_texture, null)
    renderer.free()
    _done()


func _test_title_shows_the_hour() -> void:
    _check("3pm", _top_bar.title_text("1692-10-23", "autumn", 15), "Salem — 23 October 1692, 3pm (Autumn)")
    _check("no season", _top_bar.title_text("1692-10-23", "", 9), "Salem — 23 October 1692, 9am")
    _check("unknown hour", _top_bar.title_text("1692-10-23", "autumn", -1), "Salem — 23 October 1692 (Autumn)")
    _check("no date, hour ignored", _top_bar.title_text("", "autumn", 15), "Salem — 1692")
    for pair in [[0, "12am"], [1, "1am"], [11, "11am"], [12, "12pm"], [13, "1pm"], [23, "11pm"], [-1, ""], [24, ""]]:
        _check("hour_text(%d)" % pair[0], _top_bar.hour_text(pair[0]), pair[1])
    _done()


# The hour is the village's: the engine's now moved by the zone's offset, never
# the machine's own zone. The engine serializes now with fractional seconds.
func _test_village_clock_from_a_read() -> void:
    # 19:40 UTC on October 23 is 3:40pm in the village (EDT, -4 h).
    var local: float = _world.village_clock_from_read("2026-10-23T19:40:12.123456789Z", -14400.0)
    _check("EDT read", _world.village_hour_at(local), 15)
    # 04:30 UTC on December 1 is 11:30pm on November 30 (EST, -5 h).
    local = _world.village_clock_from_read("2026-12-01T04:30:00Z", -18000)
    _check("EST read across midnight", _world.village_hour_at(local), 23)
    _check("no offset", _world.village_clock_from_read("2026-10-23T19:40:12Z", null), 0.0)
    _check("no now", _world.village_clock_from_read("", -14400.0), 0.0)
    _check("offset not a number", _world.village_clock_from_read("2026-10-23T19:40:12Z", "-14400"), 0.0)
    _done()


# On a DST day the world is re-read when the offset changes, not at midnight,
# so the hour is right from the change on (code_review, LLM-854).
func _test_refresh_at_the_offset_change() -> void:
    var ends := "2026-11-02T05:00:00Z"
    var until := "2026-11-01T06:00:00Z"
    _check("DST day: the change first", _world.village_refresh_at(ends, until), until)
    _check("ordinary day: the date end", _world.village_refresh_at(ends, ""), ends)
    _check("change after the date end", _world.village_refresh_at("2026-11-01T04:00:00Z", until), "2026-11-01T04:00:00Z")
    _check("only the change", _world.village_refresh_at("", until), until)
    _check("neither", _world.village_refresh_at("", ""), "")
    _done()
