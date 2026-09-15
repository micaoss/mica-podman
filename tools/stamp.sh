#!/usr/bin/env bash
# Record which locks/upstream.lock and declared SOURCE_DATE_EPOCH _out/podman/<arch>
# was built from, and refuse a stale one.
#
#   bash tools/stamp.sh --stamp <dir>   # write <dir>/upstream.lock (locks/upstream.lock unchanged) and <dir>/source-date-epoch
#   bash tools/stamp.sh --check <dir>   # refuse a missing or different stamp
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOCK="${REPO_ROOT}/locks/upstream.lock"
die() { echo "stamp.sh: error: $*" >&2; exit 1; }
[ -f "${LOCK}" ] || die "${LOCK} does not exist"

[ "$#" -eq 2 ] && [ -d "$2" ] || die "usage: bash tools/stamp.sh --stamp|--check <dir>"
case "$1" in
--stamp)
    cp "${LOCK}" "$2/upstream.lock"
    bash "${REPO_ROOT}/tools/version.sh" epoch >"$2/source-date-epoch"
    ;;
--check)
    rebuild="MICA_ARCH=$(basename "$2") make podman"
    [ -f "$2/upstream.lock" ] || die "$2 carries no upstream.lock, so its binaries cannot be matched to locks/upstream.lock; rebuild: ${rebuild}"
    cmp -s "$2/upstream.lock" "${LOCK}" || die "$2 was built from a different locks/upstream.lock than the one in this tree:
$(diff "$2/upstream.lock" "${LOCK}" | grep '^[<>]' || true)
Rebuild -- ${rebuild} -- or restore locks/upstream.lock."
    epoch="$(bash "${REPO_ROOT}/tools/version.sh" epoch)"
    [ "$(cat "$2/source-date-epoch" 2>/dev/null || true)" = "${epoch}" ] ||
        die "$2 was built with SOURCE_DATE_EPOCH $(cat "$2/source-date-epoch" 2>/dev/null || echo none), and deb/mica-podman.control declares ${epoch}; rebuild: ${rebuild}"
    ;;
*) die "usage: bash tools/stamp.sh --stamp|--check <dir>" ;;
esac
