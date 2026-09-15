#!/usr/bin/env bash
# The package-version guard (mica:docs/decisions/2026-09-15-package-versions.md):
# an archive built here against the previous release of this repository.
#
#   bash tools/reuse.sh [--before <YYYYMMDD-HHMM>] <amd64|arm64> <archive>
#
# The previous release is the latest published release (before <tag>, when
# given) that carries <repository>.lock. Its lock, SHA256SUMS and pool manifest
# are read with no credential. Prints `build` (no previous release, one made
# before layers recorded mica.inputs, or a higher version) or `reuse <release>`
# (the same version, the same inputs and the same bytes). Refused: a lower
# version; the same version with other inputs (a change without a version
# bump); the same version with other bytes; a previous release whose lock, pool
# or archive cannot be read at its digest.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
die() { echo "reuse.sh: error: $*" >&2; exit 1; }
BEFORE=""
[ "${1-}" != --before ] || { BEFORE="${2-}"; shift 2; }
[ "$#" -eq 2 ] && { [ "$1" = amd64 ] || [ "$1" = arm64 ]; } && [ -f "$2" ] || die "usage: bash tools/reuse.sh [--before <tag>] <amd64|arm64> <archive>"
ARCH="$1" DEB="$2"

cd "${REPO_ROOT}"
ORIGIN="$(git remote get-url origin)"
[[ "${ORIGIN}" =~ github\.com[:/]([A-Za-z0-9._-]+)/([A-Za-z0-9._-]+)$ ]] || die "origin ${ORIGIN} is not a GitHub repository"
SLUG="${BASH_REMATCH[1]}/${BASH_REMATCH[2]%.git}"
REPOSITORY="${SLUG#*/}"
POOL="${SLUG,,}"
DOWNLOAD="${MICA_RELEASE_DOWNLOAD:-https://github.com/${SLUG}/releases/download}"
REGISTRY="${MICA_OCI_REGISTRY:-https://ghcr.io}"
PACKAGE="$(dpkg-deb --field "${DEB}" Package)"
VERSION="$(dpkg-deb --field "${DEB}" Version)"
SHA="$(sha256sum "${DEB}" | cut -d' ' -f1)"
INPUTS="$(bash tools/package-inputs.sh "${ARCH}")"

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT
gh api "repos/${SLUG}/releases?per_page=100" >"${WORK}/releases.json" 2>"${WORK}/releases.err" ||
    die "listing the releases of ${SLUG} failed: $(head -c 300 "${WORK}/releases.err")"
PREVIOUS="$(jq -r --arg lock "${REPOSITORY}.lock" '.[] | select(.draft == false and any(.assets[]; .name == $lock)) | .tag_name' "${WORK}/releases.json" |
    grep -E '^[0-9]{8}-[0-9]{4}$' | LC_ALL=C sort | awk -v b="${BEFORE}" 'b == "" || $0 < b' | tail -n1 || true)"
[ -n "${PREVIOUS}" ] || { echo "build"; echo "reuse.sh: ${PACKAGE} ${ARCH} ${VERSION}: no previous release of ${SLUG}" >&2; exit 0; }

get() { curl -fsSL --retry 3 --max-time 600 -o "$2" "$1" 2>/dev/null; }
get "${DOWNLOAD}/${PREVIOUS}/SHA256SUMS" "${WORK}/SHA256SUMS" && get "${DOWNLOAD}/${PREVIOUS}/${REPOSITORY}.lock" "${WORK}/${REPOSITORY}.lock" ||
    die "the lock of ${REPOSITORY} ${PREVIOUS} cannot be downloaded"
(cd "${WORK}" && sha256sum --quiet -c SHA256SUMS 2>/dev/null) || die "the lock of ${REPOSITORY} ${PREVIOUS} does not match its SHA256SUMS"
[ "$(bash tools/check-lock.sh lock "${WORK}/${REPOSITORY}.lock" 2>&1 || true)" = valid ] || die "the lock of ${REPOSITORY} ${PREVIOUS} breaks the file rules"
row="$(awk -F'\t' -v p="${PACKAGE}" -v a="${ARCH}" '$1 == "package" && $2 == p && $3 == a' "${WORK}/${REPOSITORY}.lock")"
pool="$(awk -F'\t' -v a="${ARCH}" '$1 == "pool" && $2 == a { print $3 }' "${WORK}/${REPOSITORY}.lock")"
[ -n "${row}" ] && [ -n "${pool}" ] || die "${REPOSITORY} ${PREVIOUS} has no ${PACKAGE} ${ARCH} package or pool row"
OLD_VERSION="$(cut -f4 <<<"${row}")" OLD_SHA="$(cut -f5 <<<"${row}")" DIGEST="${pool##*@}"

# The pool, anonymously, by digest.
auth="${WORK}/auth"
challenge="$(curl -sS --max-time 60 -o /dev/null -D - "${REGISTRY}/v2/${POOL}/tags/list" 2>/dev/null | tr -d '\r' | grep -i '^www-authenticate: bearer' || true)"
if [ -n "${challenge}" ]; then
    realm="$(sed -n 's/.*realm="\([^"]*\)".*/\1/p' <<<"${challenge}")"
    service="$(sed -n 's/.*service="\([^"]*\)".*/\1/p' <<<"${challenge}")"
    t="$(curl -fsS --max-time 60 --get --data-urlencode "service=${service}" --data-urlencode "scope=repository:${POOL}:pull" "${realm}" 2>/dev/null | jq -r '.token // empty' || true)"
    printf 'Authorization: Bearer %s\n' "${t}" >"${auth}"
else
    printf 'X-Mica-Reuse: 1\n' >"${auth}"
fi
curl -fsS --max-time 120 -H @"${auth}" -H 'Accept: application/vnd.oci.image.manifest.v1+json' -o "${WORK}/pool.json" "${REGISTRY}/v2/${POOL}/manifests/${DIGEST}" 2>/dev/null ||
    die "the ${ARCH} pool of ${REPOSITORY} ${PREVIOUS} cannot be read at ${DIGEST}"
[ "sha256:$(sha256sum "${WORK}/pool.json" | cut -d' ' -f1)" = "${DIGEST}" ] || die "the ${ARCH} pool of ${REPOSITORY} ${PREVIOUS} does not hash to ${DIGEST}"
jq -e --arg d "sha256:${OLD_SHA}" 'any(.layers[]; .digest == $d)' "${WORK}/pool.json" >/dev/null ||
    die "the ${ARCH} pool of ${REPOSITORY} ${PREVIOUS} has no layer ${OLD_SHA} for ${PACKAGE} ${OLD_VERSION}"
if ! jq -e 'any(.layers[]; .annotations["mica.inputs"] != null)' "${WORK}/pool.json" >/dev/null; then
    echo "build"
    echo "reuse.sh: ${PACKAGE} ${ARCH} ${VERSION}: ${REPOSITORY} ${PREVIOUS} predates recorded inputs" >&2
    exit 0
fi
OLD_INPUTS="$(jq -r --arg d "sha256:${OLD_SHA}" '.layers[] | select(.digest == $d) | .annotations["mica.inputs"] // empty' "${WORK}/pool.json")"

dpkg --compare-versions "${VERSION}" lt "${OLD_VERSION}" &&
    die "${PACKAGE} ${ARCH} ${VERSION} is lower than ${OLD_VERSION} of ${REPOSITORY} ${PREVIOUS}"
if dpkg --compare-versions "${VERSION}" gt "${OLD_VERSION}"; then
    echo "build"
    echo "reuse.sh: ${PACKAGE} ${ARCH} ${VERSION}: a higher version than ${OLD_VERSION} of ${PREVIOUS}" >&2
    exit 0
fi
[ "${OLD_INPUTS}" = "${INPUTS}" ] ||
    die "inputs of ${PACKAGE} changed without a version bump: ${ARCH} ${VERSION} has inputs ${INPUTS}, ${REPOSITORY} ${PREVIOUS} recorded ${OLD_INPUTS:-none}; bump the version in deb/mica-podman.control"
[ "${SHA}" = "${OLD_SHA}" ] ||
    die "${PACKAGE} ${ARCH} ${VERSION} rebuilds to ${SHA}, not the published ${OLD_SHA} of ${PREVIOUS}; the bytes changed, so bump the version"
curl -fsSL --max-time 600 -H @"${auth}" -o "${WORK}/published.deb" "${REGISTRY}/v2/${POOL}/blobs/sha256:${OLD_SHA}" 2>/dev/null ||
    die "the published ${PACKAGE} ${ARCH} ${VERSION} of ${PREVIOUS} cannot be read"
[ "$(sha256sum "${WORK}/published.deb" | cut -d' ' -f1)" = "${OLD_SHA}" ] || die "the published ${PACKAGE} ${ARCH} ${VERSION} of ${PREVIOUS} does not hash to ${OLD_SHA}"
echo "reuse ${PREVIOUS}"
echo "reuse.sh: ${PACKAGE} ${ARCH} ${VERSION}: the same inputs and bytes as ${REPOSITORY} ${PREVIOUS}" >&2
