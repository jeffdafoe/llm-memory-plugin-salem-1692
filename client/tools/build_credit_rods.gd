extends SceneTree
## Builds the credits scroll's rod sheets (LLM-802) from credits_scroll.gd's
## rod_image: one PNG per rod, ROD_SHEET_WIDTH wide, one 16 px row per picture,
## ordered step-major (row = step * ROD_TURN_FRAMES + frame). The scroll cuts
## its width out of these at layout instead of drawing rods at runtime, which
## cost ~15 ms a picture.
##
## Rerun after changing rod_image, the rod colours, ROLL_STEPS or
## ROD_TURN_FRAMES (credits_scroll_test checks the sheets still match):
##   godot --headless --path client --script res://tools/build_credit_rods.gd

func _initialize() -> void:
    var scroll: GDScript = load("res://scripts/credits_scroll.gd")
    var width: int = scroll.ROD_SHEET_WIDTH
    var height: int = scroll.ROD_HEIGHT_PX
    var rows: int = scroll.ROLL_STEPS * scroll.ROD_TURN_FRAMES
    for which in 2:
        var sheet := Image.create(width, height * rows, false, Image.FORMAT_RGBA8)
        for step in scroll.ROLL_STEPS:
            for frame in scroll.ROD_TURN_FRAMES:
                var picture: Image = scroll.rod_image(width, height, scroll.ROD_SEEDS[which], TAU * frame / scroll.ROD_TURN_FRAMES, step)
                sheet.blit_rect(picture, Rect2i(0, 0, width, height), Vector2i(0, (step * scroll.ROD_TURN_FRAMES + frame) * height))
        var path: String = scroll.ROD_SHEET_PATHS[which]
        var err := sheet.save_png(ProjectSettings.globalize_path(path))
        print("build_credit_rods: %s %s" % [path, "ok" if err == OK else "failed (%d)" % err])
    quit(0)
