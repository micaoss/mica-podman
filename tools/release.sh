#!/usr/bin/env bash
# Publish _out/debs/{amd64,arm64}/pool of a clean HEAD as the release <tag>
# (mica:docs/design/release-lock.md): the pools
# ghcr.io/<owner>/<repository>:pool.<arch>.<tag>, then the lock on the published
# GitHub Release <tag> of this repository.
#
#   GH_TOKEN=<token with contents:write, packages:write> bash tools/release.sh <YYYYMMDD-HHMM>
#
# Run by release.yml for the release the user cut with
# `gh release create <YYYYMMDD-HHMM> --target <commit of main>`. A pool is an OCI
# manifest (artifactType application/vnd.mica.pool) with the archive as its one
# layer (application/vnd.mica.deb, titled with its file name, mica.inputs its
# inputs hash) and only release-independent annotations (mica.source-repo,
# mica.arch), so an unchanged pool keeps its digest and the new tag names the
# same bytes. tools/reuse.sh first checks every archive against the previous
# release: a lower version, or the same version with other inputs or bytes,
# is refused. The release carries exactly
# <repository>.lock (mica-lock v1: release, pool and package rows) and
# SHA256SUMS listing it. Every archive is checked before any gh call. Nothing is
# written unless the tag is a real UTC time, the checked-out commit is on main,
# the tag names that commit and the published release of this repository
# exists. An attached asset or a pool tag must already hold these bytes and
# anything not expected is refused; only what is missing is written, never
# replaced. The pools are read back with no credential before the lock is
# uploaded; the tag and both assets are then read back the same way.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PACKAGE=mica-podman
ARCHES=(amd64 arm64)
die() { echo "release.sh: error: $*" >&2; exit 1; }
for t in gh git curl jq sha256sum dpkg-deb date; do
    command -v "${t}" >/dev/null 2>&1 || die "${t} is required and not on PATH"
done

[ "$#" -eq 1 ] || die "usage: bash tools/release.sh <YYYYMMDD-HHMM>"
TAG="$1"
[[ "${TAG}" =~ ^[0-9]{8}-[0-9]{4}$ ]] &&
    [ "$(date -u -d "${TAG:0:4}-${TAG:4:2}-${TAG:6:2} ${TAG:9:2}:${TAG:11:2}" +%Y%m%d-%H%M 2>/dev/null || true)" = "${TAG}" ] ||
    die "the tag '${TAG}' is not a UTC time YYYYMMDD-HHMM"

cd "${REPO_ROOT}"
[ -z "$(git status --porcelain)" ] || die "the checkout has uncommitted changes; only a clean HEAD is released"
COMMIT="$(git rev-parse HEAD)"
ORIGIN="$(git remote get-url origin)"
[[ "${ORIGIN}" =~ github\.com[:/]([A-Za-z0-9._-]+)/([A-Za-z0-9._-]+)$ ]] || die "origin ${ORIGIN} is not a GitHub repository"
SLUG="${BASH_REMATCH[1]}/${BASH_REMATCH[2]%.git}"
REPOSITORY="${SLUG#*/}"
DOWNLOAD="${MICA_RELEASE_DOWNLOAD:-https://github.com/${SLUG}/releases/download}"
GIT_URL="${MICA_RELEASE_GIT:-https://github.com/${SLUG}.git}"
REGISTRY="${MICA_OCI_REGISTRY:-https://ghcr.io}"
POOL="${SLUG,,}"
EMPTY_CONFIG=sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a
MANIFEST_TYPE=application/vnd.oci.image.manifest.v1+json

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT
mkdir -p "${WORK}/assets" "${WORK}/download"

DECLARED="$(bash tools/version.sh version)"
VERSION=""
for arch in "${ARCHES[@]}"; do
    dir="_out/debs/${arch}/pool"
    mapfile -t debs < <(find "${dir}" -maxdepth 1 -type f -name '*.deb' 2>/dev/null | LC_ALL=C sort)
    [ "${#debs[@]}" -eq 1 ] || die "${dir} holds ${#debs[@]} archives; exactly one ${PACKAGE} archive is released per architecture"
    deb="${debs[0]}"
    field() { dpkg-deb --field "${deb}" "$1"; }
    [ "$(field Package)" = "${PACKAGE}" ] || die "${deb} is Package $(field Package), not ${PACKAGE}"
    [ "$(field Architecture)" = "${arch}" ] || die "${deb} is Architecture $(field Architecture), not ${arch}"
    [ "$(field Mica-Source-Repo)" = "${REPOSITORY}" ] || die "${deb} carries Mica-Source-Repo $(field Mica-Source-Repo), not ${REPOSITORY}"
    [ -z "$(field Mica-Source-Commit)" ] || die "${deb} carries Mica-Source-Commit; a package names no commit"
    v="$(field Version)"
    [ "${v}" = "${DECLARED}" ] || die "${deb} Version ${v} is not ${DECLARED}, the version deb/mica-podman.control declares"
    [ -z "${VERSION}" ] || [ "${v}" = "${VERSION}" ] || die "${deb} Version ${v} differs from ${VERSION}; one stamp per release"
    VERSION="${v}"
    [ "$(basename "${deb}")" = "${PACKAGE}_${v}_${arch}.deb" ] || die "${deb} is not named ${PACKAGE}_${v}_${arch}.deb"
    cp "${deb}" "${WORK}/pool-${arch}.deb"
    sha="$(sha256sum "${deb}" | cut -d' ' -f1)"
    jq -cn --arg type "${MANIFEST_TYPE}" --arg config "${EMPTY_CONFIG}" --arg d "sha256:${sha}" --argjson size "$(stat -c %s "${deb}")" \
        --arg title "$(basename "${deb}")" --arg inputs "$(bash tools/package-inputs.sh "${arch}")" --arg repository "${REPOSITORY}" --arg arch "${arch}" \
        '{schemaVersion: 2, mediaType: $type, artifactType: "application/vnd.mica.pool",
          config: {mediaType: "application/vnd.oci.empty.v1+json", digest: $config, size: 2},
          layers: [{mediaType: "application/vnd.mica.deb", digest: $d, size: $size,
            annotations: {"org.opencontainers.image.title": $title, "mica.inputs": $inputs}}],
          annotations: {"mica.source-repo": $repository, "mica.arch": $arch}}' \
        | tr -d '\n' >"${WORK}/pool-${arch}.json"
    printf 'pool\t%s\tghcr.io/%s:pool.%s.%s@sha256:%s\n' "${arch}" "${POOL}" "${arch}" "${TAG}" \
        "$(sha256sum "${WORK}/pool-${arch}.json" | cut -d' ' -f1)" >>"${WORK}/pools"
    printf 'package\t%s\t%s\t%s\t%s\n' "${PACKAGE}" "${arch}" "${v}" "${sha}" >>"${WORK}/packages"
done
LOCK="${REPOSITORY}.lock"
{
    echo "# mica-lock v1"
    echo "# ${LOCK}: ${REPOSITORY} ${TAG}, written when the release was published."
    printf 'release\t%s\t%s\t%s\n' "${REPOSITORY}" "${TAG}" "${COMMIT}"
    cat "${WORK}/pools" "${WORK}/packages"
} >"${WORK}/assets/${LOCK}"
checked="$(bash "${REPO_ROOT}/tools/check-lock.sh" lock "${WORK}/assets/${LOCK}" 2>&1 || true)"
[ "${checked}" = valid ] || die "the lock this release would carry is ${checked}"
(cd "${WORK}/assets" && sha256sum "${LOCK}" >SHA256SUMS)
# The lock, then SHA256SUMS: a changed lock is named before the sums that follow from it.
ASSETS=("${WORK}/assets/${LOCK}" "${WORK}/assets/SHA256SUMS")

[ -n "${GH_TOKEN:-}" ] || die "GH_TOKEN must be set; releasing is CI's, with its own token"
git merge-base --is-ancestor "${COMMIT}" origin/main 2>/dev/null || die "${COMMIT} is not on origin/main; only a commit of main is released"

tag_commit() { # the commit the tag names at GIT_URL, read anonymously
    git ls-remote --tags "${GIT_URL}" 2>/dev/null | awk -v t="refs/tags/${TAG}" '$2 == t || $2 == t "^{}" { sha = $1 } END { print sha }'
}
tagged="$(tag_commit)"
[ "${tagged}" = "${COMMIT}" ] || die "tag ${TAG} is ${tagged:-absent} at ${GIT_URL}, not HEAD ${COMMIT}"

if ! gh api "repos/${SLUG}/releases/tags/${TAG}" >"${WORK}/release.json" 2>"${WORK}/release.err"; then
    grep -c 'HTTP 404' "${WORK}/release.err" >/dev/null && die "there is no published release ${TAG} in ${SLUG}; cut it with gh release create ${TAG} --target ${COMMIT}"
    die "reading release ${TAG} of ${SLUG} failed: $(head -c 300 "${WORK}/release.err")"
fi
[ "$(jq -r .tag_name "${WORK}/release.json")" = "${TAG}" ] &&
    [ "$(jq -r .html_url "${WORK}/release.json")" = "https://github.com/${SLUG}/releases/tag/${TAG}" ] ||
    die "$(jq -r .html_url "${WORK}/release.json") is not the release ${TAG} of ${SLUG}"
[ "$(jq -r .draft "${WORK}/release.json")" = false ] || die "release ${TAG} of ${SLUG} is a draft; assets go to a published release only"

# Attached assets: each must be expected, uploaded and hold these bytes.
missing=()
for f in "${ASSETS[@]}"; do
    n="$(basename "${f}")"
    attached="$(jq -r --arg n "${n}" '.assets[] | select(.name == $n) | "\(.state) \(.digest)"' "${WORK}/release.json")"
    if [ -z "${attached}" ]; then
        missing+=("${f}")
        continue
    fi
    [ "${attached%% *}" = uploaded ] || die "${n} is attached but not uploaded (state ${attached%% *}); delete that asset by hand after inspection, then rerun"
    [ "${attached#* }" = "sha256:$(sha256sum "${f}" | cut -d' ' -f1)" ] ||
        die "${n} is already attached with other bytes (${attached#* }); a published asset is never replaced"
done
while IFS= read -r n; do
    [ -e "${WORK}/assets/${n}" ] || die "release ${TAG} carries ${n}, which this release does not publish"
done < <(jq -r '.assets[].name' "${WORK}/release.json")

# Every archive against the previous release, before anything is written.
for arch in "${ARCHES[@]}"; do
    decision="$(bash tools/reuse.sh --before "${TAG}" "${arch}" "${WORK}/pool-${arch}.deb")" || exit 1
    echo "release.sh: ${PACKAGE} ${arch} ${VERSION}: ${decision}"
done

# The pools. A bearer from the registry's challenge, as GH_TOKEN or anonymously;
# none when the registry does not challenge. The token stays out of argv.
bearer() { # <actions> [anonymous]
    local challenge realm service
    challenge="$(curl -sS --max-time 60 -o /dev/null -D - "${REGISTRY}/v2/${POOL}/tags/list" 2>/dev/null | tr -d '\r' | grep -i '^www-authenticate: bearer' || true)"
    [ -n "${challenge}" ] || return 0
    realm="$(sed -n 's/.*realm="\([^"]*\)".*/\1/p' <<<"${challenge}")"
    service="$(sed -n 's/.*service="\([^"]*\)".*/\1/p' <<<"${challenge}")"
    if [ "${2:-}" = anonymous ]; then
        curl -fsS --max-time 60 --get --data-urlencode "service=${service}" --data-urlencode "scope=repository:${POOL}:$1" "${realm}"
    else
        printf 'user = "x-access-token:%s"\n' "${GH_TOKEN}" |
            curl -fsS -K - --max-time 60 --get --data-urlencode "service=${service}" --data-urlencode "scope=repository:${POOL}:$1" "${realm}"
    fi 2>/dev/null | jq -r '.token // .access_token // empty' || true
}
header() { # <out> <actions> [anonymous]: a curl header file
    local t
    t="$(bearer "$2" "${3:-}")"
    if [ -n "${t}" ]; then printf 'Authorization: Bearer %s\n' "${t}" >"$1"; else printf 'X-Mica-Release: 1\n' >"$1"; fi
}
http() { # <out> <header file> [curl args] -> status
    local out="$1" h="$2"
    shift 2
    curl -sS --max-time 1800 -o "${out}" -w '%{http_code}' -H @"${h}" "$@" 2>/dev/null || echo 000
}
header "${WORK}/push-auth" pull,push
PUSH=()
for arch in "${ARCHES[@]}"; do
    ref="ghcr.io/${POOL}:pool.${arch}.${TAG}"
    status="$(http "${WORK}/existing-${arch}.json" "${WORK}/push-auth" -H "Accept: ${MANIFEST_TYPE}" "${REGISTRY}/v2/${POOL}/manifests/pool.${arch}.${TAG}")"
    case "${status}" in
    200) cmp -s "${WORK}/existing-${arch}.json" "${WORK}/pool-${arch}.json" ||
        die "${ref} already holds another manifest (sha256:$(sha256sum "${WORK}/existing-${arch}.json" | cut -d' ' -f1)); a published pool is never replaced" ;;
    404) PUSH+=("${arch}") ;;
    *) die "reading ${ref} answered HTTP ${status}" ;;
    esac
done
blob() { # <file>: upload unless the registry has it
    local file="$1" digest location status
    digest="sha256:$(sha256sum "${file}" | cut -d' ' -f1)"
    [ "$(http /dev/null "${WORK}/push-auth" -I "${REGISTRY}/v2/${POOL}/blobs/${digest}")" != 200 ] || return 0
    status="$(http "${WORK}/upload" "${WORK}/push-auth" -X POST -D "${WORK}/upload.h" -H 'Content-Length: 0' "${REGISTRY}/v2/${POOL}/blobs/uploads/")"
    [ "${status}" = 202 ] || die "starting an upload to ghcr.io/${POOL} answered HTTP ${status}: $(head -c 200 "${WORK}/upload")"
    # First line without a pipe: an early-exiting head would SIGPIPE sed under pipefail.
    location="$(tr -d '\r' <"${WORK}/upload.h" | sed -n 's/^[Ll]ocation: //p')"
    location="${location%%$'\n'*}"
    case "${location}" in /*) location="${REGISTRY}${location}" ;; esac
    case "${location}" in *\?*) location="${location}&digest=${digest}" ;; *) location="${location}?digest=${digest}" ;; esac
    status="$(http "${WORK}/upload" "${WORK}/push-auth" -X PUT -H 'Content-Type: application/octet-stream' --data-binary "@${file}" "${location}")"
    [ "${status}" = 201 ] || die "uploading ${digest} to ghcr.io/${POOL} answered HTTP ${status}: $(head -c 200 "${WORK}/upload")"
}
printf '{}' >"${WORK}/config"
for arch in ${PUSH[@]+"${PUSH[@]}"}; do
    blob "${WORK}/config"
    blob "${WORK}/pool-${arch}.deb"
    status="$(http "${WORK}/put" "${WORK}/push-auth" -X PUT -H "Content-Type: ${MANIFEST_TYPE}" --data-binary "@${WORK}/pool-${arch}.json" \
        "${REGISTRY}/v2/${POOL}/manifests/pool.${arch}.${TAG}")"
    [ "${status}" = 201 ] || die "putting ghcr.io/${POOL}:pool.${arch}.${TAG} answered HTTP ${status}: $(head -c 200 "${WORK}/put")"
    echo "release.sh: pushed ghcr.io/${POOL}:pool.${arch}.${TAG}"
done
header "${WORK}/anonymous" pull anonymous
PUBLIC="make the package public once (https://github.com/orgs/${SLUG%%/*}/packages/container/package/${POOL#*/}, Package settings, Change visibility: Public) and rerun this job"
for arch in "${ARCHES[@]}"; do
    digest="sha256:$(sha256sum "${WORK}/pool-${arch}.json" | cut -d' ' -f1)"
    status="$(http "${WORK}/back.json" "${WORK}/anonymous" -H "Accept: ${MANIFEST_TYPE}" "${REGISTRY}/v2/${POOL}/manifests/${digest}")"
    [ "${status}" = 200 ] || die "ghcr.io/${POOL}@${digest} cannot be read anonymously (HTTP ${status}); ${PUBLIC}"
    cmp -s "${WORK}/back.json" "${WORK}/pool-${arch}.json" || die "ghcr.io/${POOL}@${digest} reads back with other bytes"
    layer="sha256:$(sha256sum "${WORK}/pool-${arch}.deb" | cut -d' ' -f1)"
    status="$(http "${WORK}/back.deb" "${WORK}/anonymous" -L "${REGISTRY}/v2/${POOL}/blobs/${layer}")"
    [ "${status}" = 200 ] || die "the ${arch} archive ${layer} cannot be read anonymously from ghcr.io/${POOL} (HTTP ${status}); ${PUBLIC}"
    cmp -s "${WORK}/back.deb" "${WORK}/pool-${arch}.deb" || die "the ${arch} archive reads back from ghcr.io/${POOL} with other bytes"
done
echo "release.sh: ghcr.io/${POOL}:pool.{amd64,arm64}.${TAG} read back anonymously"

if [ "${#missing[@]}" -gt 0 ]; then
    gh release upload "${TAG}" "${missing[@]}" -R "${SLUG}" >/dev/null
    echo "release.sh: attached $(for f in "${missing[@]}"; do basename "${f}"; done | tr '\n' ' ')to ${SLUG} ${TAG}"
else
    echo "release.sh: ${SLUG} ${TAG} already carries these assets"
fi

# Anonymous: the tag, then every asset through the download URL.
tagged="$(tag_commit)"
[ "${tagged}" = "${COMMIT}" ] || die "tag ${TAG} is ${tagged:-absent} at ${GIT_URL}, not ${COMMIT}"
for f in "${ASSETS[@]}"; do
    n="$(basename "${f}")"
    curl -fsSL --retry 5 --retry-delay 5 --max-time 600 -o "${WORK}/download/${n}" "${DOWNLOAD}/${TAG}/${n}" ||
        die "${DOWNLOAD}/${TAG}/${n} cannot be downloaded anonymously"
    cmp -s "${WORK}/download/${n}" "${f}" || die "${DOWNLOAD}/${TAG}/${n} downloads with other bytes"
done
(cd "${WORK}/download" && sha256sum --quiet -c SHA256SUMS) || die "the downloaded assets of ${TAG} do not match SHA256SUMS"
echo "release.sh: ${DOWNLOAD}/${TAG}/ downloaded anonymously, tag ${TAG} at ${COMMIT}"
echo "release.sh: SHA256SUMS sha256 $(sha256sum "${WORK}/assets/SHA256SUMS" | cut -d' ' -f1)"
sed 's/^/release.sh:   /' "${WORK}/assets/${LOCK}"
