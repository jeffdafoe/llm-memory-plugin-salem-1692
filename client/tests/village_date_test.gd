extends SceneTree

## Headless harness for LLM-840: the village date in the top bar title, and the
## re-read of the world when that date ends.
##
## Run headless (CI and local):
##   godot --headless --path client --script res://tests/village_date_test.gd
## Exits 0 when every check passes, 1 if any check fails.
##
## Both helpers are static, so the scripts are loaded but never instanced.

const TESTS := [
    "_test_title_shows_the_date",
    "_test_title_falls_back_without_a_date",
    "_test_refresh_wait_measures_engine_time",
    "_test_refresh_wait_without_times",
]

var _top_bar: GDScript = null
var _world: GDScript = null
var _failures := 0
var _checks := 0
var _completed := {}
var _current := ""


func _initialize() -> void:
    process_frame.connect(_run_once, CONNECT_ONE_SHOT)


func _run_once() -> void:
    _top_bar = load("res://scripts/top_bar.gd")
    _world = load("res://scripts/world.gd")
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
