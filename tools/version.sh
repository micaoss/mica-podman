#!/usr/bin/env bash
# The declared identity of mica-podman (mica:docs/decisions/2026-09-15-package-versions.md):
# deb/mica-podman.control declares Version: <podman version>-<revision> and
# X-Mica-Source-Date-Epoch: <seconds>, bumped together. A release never changes
# them; the upstream part is the podman tag of locks/upstream.lock, and any
# other change to what the package holds bumps the revision.
#
#   bash tools/version.sh version   the declared version
#   bash tools/version.sh epoch     the declared SOURCE_DATE_EPOCH
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CONTROL="${REPO_ROOT}/deb/mica-podman.control"
die() { echo "version.sh: error: $*" >&2; exit 1; }
[ -f "${CONTROL}" ] || die "${CONTROL} does not exist"

field() { sed -n "s/^$1: //p" "${CONTROL}"; }
case "${1:-}" in
version)
    v="$(field Version)"
    [[ "${v}" =~ ^([0-9]+(\.[0-9]+)*)-([1-9][0-9]*)$ ]] || die "deb/mica-podman.control Version '${v}' is not <podman version>-<revision>"
    tag="$(awk -F'\t' '$1 == "git" && $2 == "podman" { print $4 }' "${REPO_ROOT}/locks/upstream.lock")"
    [ "v${BASH_REMATCH[1]}" = "${tag}" ] || die "deb/mica-podman.control Version ${v}: its upstream part ${BASH_REMATCH[1]} is not the podman tag ${tag} of locks/upstream.lock"
    echo "${v}"
    ;;
epoch)
    e="$(field X-Mica-Source-Date-Epoch)"
    [ -n "${e}" ] || die "deb/mica-podman.control declares no X-Mica-Source-Date-Epoch"
    [[ "${e}" =~ ^[1-9][0-9]*$ ]] || die "deb/mica-podman.control X-Mica-Source-Date-Epoch '${e}' is not whole seconds"
    echo "${e}"
    ;;
*) die "usage: bash tools/version.sh version|epoch" ;;
esac
