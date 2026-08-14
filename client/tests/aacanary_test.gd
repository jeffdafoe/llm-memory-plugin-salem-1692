extends SceneTree

## TEMPORARY CANARY — LLM-631. Deliberately fails so the run log can be checked
## for the two things the fix has to deliver: the suite's own output printed in
## full, and the job still failing. Removed in the next commit on this branch.

func _initialize() -> void:
    print("[zzcanary_test] this line must appear in the CI log")
    print("  FAIL: canary — deliberate failure, LLM-631 verification")
    print("\n[zzcanary_test] 1 checks, 1 failure(s)")
    quit(1)
