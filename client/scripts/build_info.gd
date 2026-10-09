extends RefCounted
## The build this client was exported from (LLM-740). The deploy writes the git
## commit to res://build_info.txt just before the web export, and the Web
## preset's include_filter packs it; a local run has no file and reads "dev".
## The engine reports its own commit on the world read, so the ticker can tell
## a tab that predates a deploy to reload.

const PATH := "res://build_info.txt"
const DEV := "dev"

static var _commit := ""


static func commit() -> String:
    if _commit == "":
        _commit = read(PATH)
    return _commit


## The trimmed contents of the file at path, or DEV when it is missing or empty.
static func read(path: String) -> String:
    if not FileAccess.file_exists(path):
        return DEV
    var text := FileAccess.get_file_as_string(path).strip_edges()
    return text if text != "" else DEV


## Both builds known and different. A local client (DEV) or a local engine
## (no build on the world read) never reads as stale.
static func is_stale(client_build: String, server_build: String) -> bool:
    if client_build == "" or client_build == DEV or server_build == "":
        return false
    return client_build != server_build
