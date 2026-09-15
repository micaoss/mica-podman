#!/usr/bin/env bash
# The Debian packages mica-podman needs, consumed from mica-system-base as its
# README "Consuming a release" requires.
#
#   bash tools/base-check.sh
#
# locks/mica-system-base.lock is the pinned Base release, unchanged, and
# locks/pins/mica-system-base.pin its record (mica:docs/design/release-lock.md
# section 3). The check:
#   1. tools/inputs.sh verifies the lock against the release and the file rules;
#   2. per architecture, the root's /var/lib/dpkg/status is read from the
#      rootfs image the lock names, by digest;
#   3. apt, with the lock's apt row as its only source and that status, names
#      the archives the root lacks for deb/debian-depends; version, SHA256 and
#      URL come from the signed index;
#   4. each such archive must be an upstream row of the Base lock pinned for a
#      root named in deb/debian-depends, or a source row of locks/upstream.lock
#      under the apt URI with the same columns; a recorded row that no longer
#      resolves is refused.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO=micaoss/mica-system-base
LOCK="${REPO_ROOT}/locks/mica-system-base.lock"
RECORD="${REPO_ROOT}/locks/upstream.lock"
ROOTS="${REPO_ROOT}/deb/debian-depends"
REGISTRY="${MICA_BASE_REGISTRY:-https://ghcr.io}"
RESOLVE="${MICA_BASE_RESOLVE:-}"

die() { echo "base-check.sh: error: $*" >&2; exit 1; }
for t in curl jq tar sha256sum; do command -v "${t}" >/dev/null 2>&1 || die "${t} is required and not on PATH"; done
sha() { sha256sum "$1" | cut -d' ' -f1; }

# 1. The pin.
bash "${REPO_ROOT}/tools/inputs.sh" verify mica-system-base >/dev/null || exit 1
TAG="$(awk -F'\t' '$1 == "release" { print $3 }' "${LOCK}")"

# Under the repository, so a sibling build container can mount it.
mkdir -p "${REPO_ROOT}/_out"
WORK="$(mktemp -d "${REPO_ROOT}/_out/base-check.XXXXXX")"
trap 'rm -rf "${WORK}"' EXIT
get() { # <url> <out> [curl args]
    local url="$1" out="$2"
    shift 2
    curl -fsSL --max-time 600 -o "${out}" "$@" "${url}" || die "downloading ${url} failed"
}

# 2. The roots' dpkg status.
if [ "${REGISTRY#file://}" = "${REGISTRY}" ]; then
    token="$(curl -fsS --max-time 60 "https://ghcr.io/token?scope=repository:${REPO}:pull&service=ghcr.io" | jq -r '.token // empty')"
    [ -n "${token}" ] || die "no anonymous pull token for ghcr.io/${REPO}"
    printf 'Authorization: Bearer %s\n' "${token}" >"${WORK}/registry-auth"
else
    : >"${WORK}/registry-auth"
fi
for arch in amd64 arm64; do
    ref="$(awk -F'\t' -v a="${arch}" '$1 == "image" && $2 == "mica-system-base" && $3 == "rootfs" && $4 == a { print $5 }' "${LOCK}")"
    [[ "${ref}" =~ ^ghcr\.io/${REPO}@(sha256:[0-9a-f]{64})$ ]] || die "locks/mica-system-base.lock names the ${arch} rootfs as '${ref}', not a digest reference of ghcr.io/${REPO}"
    manifest="${BASH_REMATCH[1]}"
    get "${REGISTRY}/v2/${REPO}/manifests/${manifest}" "${WORK}/manifest-${arch}.json" -H 'Accept: application/vnd.oci.image.manifest.v1+json' -H @"${WORK}/registry-auth"
    [ "sha256:$(sha "${WORK}/manifest-${arch}.json")" = "${manifest}" ] || die "the ${arch} Base rootfs manifest does not hash to ${manifest}"
    : >"${WORK}/status-${arch}"
    while IFS= read -r layer; do
        get "${REGISTRY}/v2/${REPO}/blobs/${layer}" "${WORK}/layer" -H @"${WORK}/registry-auth"
        [ "sha256:$(sha "${WORK}/layer")" = "${layer}" ] || die "a layer of the ${arch} Base rootfs does not hash to ${layer}"
        tar -xzOf "${WORK}/layer" ./var/lib/dpkg/status >"${WORK}/layer-status" 2>/dev/null || true
        [ ! -s "${WORK}/layer-status" ] || cp "${WORK}/layer-status" "${WORK}/status-${arch}"
    done < <(jq -r '.layers[].digest' "${WORK}/manifest-${arch}.json")
    [ -s "${WORK}/status-${arch}" ] || die "the ${arch} Base rootfs carries no var/lib/dpkg/status"
done

# 3. apt in the build image, the Base apt row alone, against a root's status.
mkdir -p "${WORK}/sources.d"
awk -F'\t' '$1 == "apt" { printf "Types: deb\nURIs: %s\nSuites: %s\nComponents: %s\nCheck-Valid-Until: no\nSigned-By: %s\n", $2, $3, $4, $5 }' \
    "${LOCK}" >"${WORK}/sources.d/mica-system-base.sources"
[ -s "${WORK}/sources.d/mica-system-base.sources" ] || die "locks/mica-system-base.lock has no apt row"
URI="$(sed -n 's/^URIs: //p' "${WORK}/sources.d/mica-system-base.sources")"
sed 's/#.*//; /^[[:space:]]*$/d' "${ROOTS}" >"${WORK}/roots"
[ -s "${WORK}/roots" ] || die "deb/debian-depends names no package"
apt_resolve() { # <arch> <work>: package, arch, version, sha256, url of each archive the root lacks
    local image
    image="$(bash "${REPO_ROOT}/tools/inputs.sh" image base)"
    docker run --rm --label ai-agent=true --platform linux/amd64 -v "$2:/w:ro" "${image}" bash -euo pipefail -c '
        arch="$1"
        o=(-o Dir::Etc::sourcelist=/dev/null -o Dir::Etc::sourceparts=/w/sources.d -o Dir::State::status="/w/status-${arch}"
           -o Dir::State::lists=/tmp/lists -o Dir::Cache=/tmp/cache -o APT::Architecture="${arch}" -o APT::Architectures="${arch}"
           -o Debug::NoLocking=1 -o Acquire::Languages=none)
        mkdir -p /tmp/lists/partial /tmp/cache/archives/partial
        apt-get "${o[@]}" -qq update >&2
        uri="$(sed -n "s/^URIs: //p" /w/sources.d/mica-system-base.sources)"
        apt-get "${o[@]}" install --print-uris --no-install-recommends -qq $(cat /w/roots) | while read -r _ file _ _; do
            name="${file%%_*}"; version="${file#*_}"; version="${version%_*}"; version="$(printf "%b" "${version//%/\\x}")"
            show="$(apt-cache "${o[@]}" show "${name}=${version}")"
            # First line without a pipe: an early-exiting head would SIGPIPE sed under pipefail.
            sha="$(sed -n "s/^SHA256: //p" <<<"${show}")"; sha="${sha%%$'"'"'\n'"'"'*}"
            file_path="$(sed -n "s/^Filename: //p" <<<"${show}")"; file_path="${file_path%%$'"'"'\n'"'"'*}"
            printf "%s\t%s\t%s\t%s\t%s/%s\n" "${name}" "${arch}" "${version}" "${sha}" "${uri}" "${file_path}"
        done' _ "$1"
}

# 4. Every archive the root lacks: Base's upstream row for one of our roots, or our source row.
rows() { awk -F'\t' '!/^#/ && NF' "$1" | LC_ALL=C sort; }
awk -F'\t' 'NR == FNR { ours[$0] = 1; next }
    $1 == "upstream" { n = split($7, r, ","); for (i = 1; i <= n; i++) if (r[i] in ours) { print $2 "\t" $3 "\t" $4 "\t" $5 "\t" $6; break } }' \
    "${WORK}/roots" "${LOCK}" | LC_ALL=C sort >"${WORK}/base-rows"
awk -F'\t' -v u="${URI}/" '$1 == "source" && index($6, u) == 1 { print $2 "\t" $3 "\t" $4 "\t" $5 "\t" $6 }' "${RECORD}" | LC_ALL=C sort >"${WORK}/recorded"
: >"${WORK}/resolved-all"
bad=0
for arch in amd64 arm64; do
    if [ -n "${RESOLVE}" ]; then
        "${RESOLVE}" "${arch}" "${WORK}" >"${WORK}/resolved-${arch}"
    else
        apt_resolve "${arch}" "${WORK}" >"${WORK}/resolved-${arch}"
    fi
    rows "${WORK}/resolved-${arch}" >"${WORK}/resolved-${arch}.sorted"
    cat "${WORK}/resolved-${arch}.sorted" >>"${WORK}/resolved-all"
    from_base="$(comm -12 "${WORK}/resolved-${arch}.sorted" "${WORK}/base-rows" | grep -c . || true)"
    ours="$(comm -23 "${WORK}/resolved-${arch}.sorted" "${WORK}/base-rows" | comm -12 - "${WORK}/recorded" | grep -c . || true)"
    unrecorded="$(comm -23 "${WORK}/resolved-${arch}.sorted" "${WORK}/base-rows" | comm -23 - "${WORK}/recorded")"
    if [ -n "${unrecorded}" ]; then
        printf 'base-check.sh: error: %s: not pinned by mica-system-base %s for a root in deb/debian-depends and not recorded here; record it as a source row of locks/upstream.lock or propose it for Base:\n%s\n' "${arch}" "${TAG}" "${unrecorded}" >&2
        bad=1
    else
        echo "base-check.sh: ${arch}: $(grep -c . "${WORK}/resolved-${arch}.sorted" || true) archive(s) the root lacks, ${from_base} pinned by mica-system-base, ${ours} recorded in locks/upstream.lock (mica-system-base ${TAG})"
    fi
done
stale="$(LC_ALL=C sort "${WORK}/resolved-all" | comm -13 - "${WORK}/recorded")"
if [ -n "${stale}" ]; then
    printf 'base-check.sh: error: locks/upstream.lock records %s, which no longer resolves against mica-system-base %s\n' "${stale}" "${TAG}" >&2
    bad=1
fi
[ "${bad}" -eq 0 ]
