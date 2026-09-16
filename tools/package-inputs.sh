#!/usr/bin/env bash
# The inputs hash of mica-podman at one architecture: sha256 over a sorted
# manifest of everything in this repository that decides the archive's bytes.
# A release records it as the pool layer annotation mica.inputs; a package whose
# version did not change must carry the same hash (tools/reuse.sh).
#
#   bash tools/package-inputs.sh <amd64|arm64>               the hash
#   bash tools/package-inputs.sh --manifest <amd64|arm64>    the manifest it is taken over
#
# The manifest, as `<kind> <name> <value>` lines: `file <path> <sha256>` for the
# tracked files of the engine build and the pack (locks/upstream.lock, the
# Dockerfile, build.sh, tools/package.sh, tools/stamp.sh, tools/version.sh,
# tools/dev-pins.sh, deb/ with the declared version and epoch, overlay/, and
# pins/ with the Debian build closure), and `arch <arch>`. The
# build-env images are not inputs: a toolchain move that changes the bytes is
# caught by the byte comparison.
set -euo pipefail
export LC_ALL=C

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
die() { echo "package-inputs.sh: error: $*" >&2; exit 1; }
MODE=hash
[ "${1-}" != --manifest ] || { MODE=manifest; shift; }
[ "$#" -eq 1 ] && { [ "$1" = amd64 ] || [ "$1" = arm64 ]; } || die "usage: bash tools/package-inputs.sh [--manifest] <amd64|arm64>"
cd "${REPO_ROOT}"

manifest() {
    git ls-files -z -- locks/upstream.lock Dockerfile Dockerfile.dockerignore build.sh \
        tools/package.sh tools/stamp.sh tools/version.sh tools/dev-pins.sh deb overlay pins |
        while IFS= read -r -d '' f; do
            if [ -L "${f}" ]; then printf 'link %s %s\n' "${f}" "$(readlink "${f}")"; else printf 'file %s %s\n' "${f}" "$(sha256sum "${f}" | cut -d' ' -f1)"; fi
        done
    printf 'arch %s\n' "$1"
}
if [ "${MODE}" = manifest ]; then manifest "$1" | sort; else manifest "$1" | sort | sha256sum | cut -d' ' -f1; fi
