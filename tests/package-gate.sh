#!/usr/bin/env bash
# The package gate (RULES.md section 6) over the pools _out/debs/<arch>/.
#
#   bash tests/package-gate.sh [--arch <amd64|arm64>]... [--reproduce]
#
# Default: both architectures, and one git stamp across them. --reproduce
# rebuilds the engine and each archive with no cache and requires identical
# bytes: every shipped engine binary, then the archive.
set -euo pipefail
cd "$(dirname "$0")/.."
REPO_ROOT=$PWD

ARCHES=() REPRODUCE=0
while [ "$#" -gt 0 ]; do
    case "$1" in
    --arch) ARCHES+=("${2-}"); shift 2 ;;
    --reproduce) REPRODUCE=1; shift ;;
    *) echo "usage: bash tests/package-gate.sh [--arch <amd64|arm64>]... [--reproduce]" >&2; exit 2 ;;
    esac
done
[ "${#ARCHES[@]}" -gt 0 ] || ARCHES=(amd64 arm64)
command -v dpkg-deb >/dev/null 2>&1 || { echo "error: dpkg-deb is required" >&2; exit 2; }

PASS_N=0 FAIL_N=0
pass() { PASS_N=$((PASS_N + 1)); echo "PASS: $*"; }
fail() { FAIL_N=$((FAIL_N + 1)); echo "FAIL: $*"; }
check() { local label="$1"; shift; if "$@"; then pass "${label}"; else fail "${label}"; fi; }

COMMIT="$(git rev-parse HEAD)"
SOURCE_REPO="$(basename "$(git remote get-url origin)" .git)"
MAINTAINER='Mica OS <hi@micaos.dev>'
WORK="$(mktemp -d "${REPO_ROOT}/_out/package-gate.XXXXXX")"
trap 'rm -rf "${WORK}"' EXIT
grep -v '^#' deb/payload.manifest | grep -v '^$' | awk '{ print substr($3, 2) }' | LC_ALL=C sort >"${WORK}/declared"

VERSIONS=()
for arch in "${ARCHES[@]}"; do
    mapfile -t debs < <(find "_out/debs/${arch}/pool" -maxdepth 1 -type f -name '*.deb' 2>/dev/null | LC_ALL=C sort)
    if [ "${#debs[@]}" -ne 1 ]; then
        fail "${arch}: _out/debs/${arch}/pool holds ${#debs[@]} archives, not one"
        continue
    fi
    deb="${debs[0]}"
    check "${arch}: SHA256SUMS lists exactly pool/$(basename "${deb}") at its digest" \
        bash -c 'cd "$1" && [ "$(sed "s/^[0-9a-f]\{64\}  //" SHA256SUMS)" = "pool/$2" ] && sha256sum --quiet -c SHA256SUMS' _ "_out/debs/${arch}" "$(basename "${deb}")"
    check "${arch}: Packages indexes pool/$(basename "${deb}")" \
        bash -c '[ "$(sed -n "s/^Filename: //p" "$1/Packages")" = "pool/$2" ] && [ "$(sed -n "s/^SHA256: //p" "$1/Packages")" = "$(sha256sum "$1/pool/$2" | cut -d" " -f1)" ]' _ "_out/debs/${arch}" "$(basename "${deb}")"
    field() { dpkg-deb --field "${deb}" "$1"; }
    v="$(field Version)"
    VERSIONS+=("${v}")
    check "${arch}: file name is Package_Version_Architecture" [ "$(basename "${deb}")" = "mica-podman_${v}_${arch}.deb" ]
    check "${arch}: Package mica-podman, Architecture ${arch}" [ "$(field Package)/$(field Architecture)" = "mica-podman/${arch}" ]
    check "${arch}: Version ${v} is <upstream>+git<commit12>-1 of HEAD" \
        bash -c '[[ "$1" =~ ^[0-9][0-9.]*\+git$2(\.dirty)?-1$ ]]' _ "${v}" "${COMMIT:0:12}"
    check "${arch}: Maintainer ${MAINTAINER}" [ "$(field Maintainer)" = "${MAINTAINER}" ]
    check "${arch}: Mica-Source-Repo ${SOURCE_REPO}, Mica-Source-Commit HEAD" \
        [ "$(field Mica-Source-Repo)/$(field Mica-Source-Commit)" = "${SOURCE_REPO}/${COMMIT}" ]
    check "${arch}: no Replaces" [ -z "$(field Replaces)" ]
    check "${arch}: Depends is expanded" bash -c '[ -n "$1" ] && case "$1" in *"\${"*) exit 1 ;; esac' _ "$(field Depends)"
    depends="$(field Depends | tr ',' '\n' | sed 's/|.*//; s/(.*//; s/:any//; s/[[:space:]]//g' | grep -v '^mica-' | LC_ALL=C sort -u | tr '\n' ' ')"
    check "${arch}: Debian Depends are exactly deb/debian-depends (${depends% })" \
        [ "${depends}" = "$(sed 's/#.*//; /^[[:space:]]*$/d' deb/debian-depends | LC_ALL=C sort -u | tr '\n' ' ')" ]
    check "${arch}: control archive holds only control and md5sums (no conffiles, no maintainer scripts)" \
        [ "$(dpkg-deb --ctrl-tarfile "${deb}" | tar -t | sed 's|^\./||; /^$/d' | LC_ALL=C sort | tr '\n' ' ')" = "control md5sums " ]
    dpkg-deb --fsys-tarfile "${deb}" | tar -t | sed 's|^\./||; s|/$||; /^$/d' | LC_ALL=C sort >"${WORK}/${arch}.paths"
    check "${arch}: payload is exactly deb/payload.manifest" cmp -s "${WORK}/declared" "${WORK}/${arch}.paths"
    check "${arch}: no enablement symlink" bash -c '! grep -c "\.wants/" "$1" >/dev/null' _ "${WORK}/${arch}.paths"
    check "${arch}: ships a non-empty copyright" \
        [ "$(dpkg-deb --fsys-tarfile "${deb}" | tar -xO ./usr/share/doc/mica-podman/copyright | wc -c)" -gt 0 ]
    if [ "${REPRODUCE}" = 1 ]; then
        MICA_ARCH="${arch}" MICA_PODMAN_OUT="${WORK}/podman-${arch}" MICA_NO_CACHE=1 bash build.sh >"${WORK}/${arch}-engine.log" 2>&1 ||
            { tail -20 "${WORK}/${arch}-engine.log"; fail "${arch}: the no-cache engine rebuild failed"; continue; }
        dpkg-deb -x "${deb}" "${WORK}/${arch}-root"
        for b in usr/bin/podman usr/bin/crun usr/libexec/podman/quadlet usr/libexec/podman/conmon usr/libexec/podman/catatonit \
            usr/libexec/podman/netavark usr/libexec/podman/aardvark-dns; do
            check "${arch}: a no-cache engine rebuild gives the shipped ${b##*/} byte for byte" \
                cmp -s "${WORK}/${arch}-root/${b}" "${WORK}/podman-${arch}/${b##*/}"
        done
        bash tools/package.sh --arch "${arch}" --out "${WORK}/reproduce" --no-cache >"${WORK}/${arch}.log" 2>&1 ||
            { tail -20 "${WORK}/${arch}.log"; fail "${arch}: the no-cache rebuild failed"; continue; }
        check "${arch}: a no-cache rebuild is byte-identical" cmp -s "${deb}" "${WORK}/reproduce/${arch}/pool/$(basename "${deb}")"
    fi
done
if [ "${#VERSIONS[@]}" -gt 1 ]; then
    check "one version across ${ARCHES[*]}" [ "$(printf '%s\n' "${VERSIONS[@]}" | LC_ALL=C sort -u | wc -l)" -eq 1 ]
fi

echo "RESULT: ${FAIL_N} failed, ${PASS_N} passed"
[ "${FAIL_N}" -eq 0 ]
