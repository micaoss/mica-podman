#!/usr/bin/env bash
# The inputs this repository builds from: locks/ (mica:docs/design/release-lock.md
# section 4). locks/<repository>.lock is that producer's release lock,
# unchanged, and locks/pins/<repository>.pin records its release and the sha256
# of its SHA256SUMS; locks/upstream.lock pins the third-party trees. Moving an
# input replaces its lock and pin together.
#
#   bash tools/inputs.sh check                     the file rules over locks/ (offline)
#   bash tools/inputs.sh verify [<repository>]     each pinned release serves a SHA256SUMS with the pinned hash,
#                                                  listing only its lock, and that lock unchanged (network)
#   bash tools/inputs.sh image <base|c|go|rust>    a mica-build-env image, by its index reference (offline)
#   bash tools/inputs.sh upstream-image <name>     an approved third-party image, by its original reference (offline)
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LOCKS="${MICA_LOCKS:-${REPO_ROOT}/locks}"
DOWNLOAD="${MICA_RELEASE_DOWNLOAD:-https://github.com}"
die() { echo "inputs.sh: error: $*" >&2; exit 1; }

check() {
    local got lock
    got="$(bash "${REPO_ROOT}/tools/check-lock.sh" pins "${LOCKS}" 2>&1 || true)"
    if [ "${got}" = "refused lock-invalid" ]; then
        for lock in "${LOCKS}"/*.lock; do
            [ "$(basename "${lock}")" != upstream.lock ] || continue
            got="$(bash "${REPO_ROOT}/tools/check-lock.sh" lock "${lock}" 2>&1 || true)"
            [ "${got}" = valid ] || die "locks/$(basename "${lock}"): ${got}"
        done
        die "locks/: refused lock-invalid"
    fi
    [ "${got}" = valid ] || die "locks/: ${got}"
    got="$(bash "${REPO_ROOT}/tools/check-lock.sh" upstream "${LOCKS}/upstream.lock" 2>&1 || true)"
    [ "${got}" = valid ] || die "locks/upstream.lock: ${got}"
    [ -f "${LOCKS}/mica-build-env.lock" ] || die "locks/ has no mica-build-env.lock"
}

# row <kind> <columns...>: the rows of the build-env lock whose leading columns match.
row() {
    awk -F'\t' -v want="$*" '!/^#/ { n = split(want, w, " "); ok = 1; for (i = 1; i <= n; i++) if ($i != w[i]) ok = 0; if (ok) print }' "${LOCKS}/mica-build-env.lock"
}

case "${1:-}" in
check)
    [ "$#" -eq 1 ] || die "usage: bash tools/inputs.sh check"
    check
    echo "inputs.sh: locks/ keeps the file rules"
    ;;
verify)
    [ "$#" -le 2 ] || die "usage: bash tools/inputs.sh verify [<repository>]"
    check
    WORK="$(mktemp -d)"
    trap 'rm -rf "${WORK}"' EXIT
    for pin in "${LOCKS}"/pins/*.pin; do
        repository="$(sed -n 's/^REPOSITORY=//p' "${pin}")"
        [ -z "${2:-}" ] || [ "${repository}" = "$2" ] || continue
        release="$(sed -n 's/^RELEASE=//p' "${pin}")"
        trust="$(sed -n 's/^SHA256SUMS=//p' "${pin}")"
        [ "${release}" != offline ] || { echo "inputs.sh: ${repository} is an offline pin; nothing published to verify"; continue; }
        base="${DOWNLOAD}/micaoss/${repository}/releases/download/${release}"
        curl -fsSL --retry 3 --max-time 120 -o "${WORK}/SHA256SUMS" "${base}/SHA256SUMS" || die "downloading ${base}/SHA256SUMS failed"
        got="$(sha256sum "${WORK}/SHA256SUMS" | cut -d' ' -f1)"
        [ "${got}" = "${trust}" ] || die "the SHA256SUMS of ${repository} ${release} hashes to ${got}; locks/pins/${repository}.pin records ${trust}"
        [ "$(cat "${WORK}/SHA256SUMS")" = "$(sha256sum "${LOCKS}/${repository}.lock" | cut -d' ' -f1)  ${repository}.lock" ] ||
            die "the SHA256SUMS of ${repository} ${release} does not list exactly ${repository}.lock at the sha256 of locks/${repository}.lock"
        curl -fsSL --retry 3 --max-time 120 -o "${WORK}/lock" "${base}/${repository}.lock" || die "downloading ${base}/${repository}.lock failed"
        cmp -s "${WORK}/lock" "${LOCKS}/${repository}.lock" || die "the ${repository}.lock of ${repository} ${release} downloads with other bytes than locks/${repository}.lock"
        echo "inputs.sh: locks/${repository}.lock is the lock of ${repository} ${release}; SHA256SUMS ${trust}"
    done
    ;;
image)
    [ "$#" -eq 2 ] && [[ "$2" =~ ^[a-z0-9][a-z0-9.+-]*$ ]] || die "usage: bash tools/inputs.sh image <base|c|go|rust>"
    check
    rows="$(row image mica-build-env "$2" index)"
    [ -n "${rows}" ] || die "locks/mica-build-env.lock names no index of the image $2"
    cut -f5 <<<"${rows}"
    ;;
upstream-image)
    [ "$#" -eq 2 ] && [ -n "$2" ] || die "usage: bash tools/inputs.sh upstream-image <name>"
    check
    refs="$(row image upstream "$2" | cut -f5 | LC_ALL=C sort -u)"
    [ -n "${refs}" ] || die "$2 is not an upstream image of locks/mica-build-env.lock; propose it to mica-build-env"
    [ "$(wc -l <<<"${refs}")" -eq 1 ] || die "locks/mica-build-env.lock names $2 with more than one reference"
    printf '%s\n' "${refs}"
    ;;
*)
    die "usage: bash tools/inputs.sh check | verify [<repository>] | image <name> | upstream-image <name>"
    ;;
esac
