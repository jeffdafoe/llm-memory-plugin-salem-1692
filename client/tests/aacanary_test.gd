extends SceneTree

## TEMPORARY CANARY — LLM-631. Deliberately fails, and sorts FIRST so the run
## also shows that later suites still execute. Removed in the next commit.

func _initialize() -> void:
    print("[aacanary_test] this line must appear in the CI log")
    print("  FAIL: canary — deliberate failure, LLM-631 verification")
    print("\n[aacanary_test] 1 checks, 1 failure(s)")
    quit(1)
